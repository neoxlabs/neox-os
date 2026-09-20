package com.neox.neox

import android.Manifest
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.net.wifi.WifiManager
import android.os.BatteryManager
import android.os.Build
import android.os.Looper
import android.os.SystemClock
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder
import kotlin.math.abs
import kotlin.math.cos
import kotlin.math.sqrt

/**
 * 采集 —— 这台手机看得见什么, 报给那台 OS.
 *
 * ── 第一约束是电, 不是数据量 ──
 *
 *	一个 24 小时跑着的采集器, 只要费电就一定会被用户关掉 —— 而关掉之后
 *	它一条数据都采不到. 所以每一处的取舍都是同一条: **宁可少一点精度,
 *	不要多一分功耗**.
 *
 *	具体到四件事:
 *
 *	① **人不动的时候不用 GPS**. 只用 NETWORK_PROVIDER(基站 + WiFi 定位).
 *	   城里它的误差是几十米, 而地点判定的半径是 150 米 —— 也就是说
 *	   GPS 那点额外精度**不会让任何一次判断变得更对**, 却是几十倍的电.
 *
 *	   **人在动的时候是另一回事** —— 见下面"两档".
 *
 *	② **按距离触发, 不是按时间轮询**. minDistance=120m 交给系统去判:
 *	   人没动的时候(一天里大部分时候)我们这个进程根本不会被唤醒.
 *	   自己起个定时器每 5 分钟醒一次算距离, 是把系统已经做好的事
 *	   又做一遍, 而且做得更费.
 *
 *	③ **WiFi 是白捡的**. 连着的那个 AP 的 id 不用花任何电就拿得到,
 *	   而它是"在不在家"最可靠的信号 —— 比 GPS 还准, 因为家里的 WiFi
 *	   只有在家才连得上.
 *
 *	④ **攒着一起发**. 每条信号一个请求 = 每条一次 TLS 握手 + 一次
 *	   射频唤醒. 攒到有一批再发, 射频醒一次干完所有的事.
 *
 * ── 只在变化时报 ──
 *
 *	同一个位置每 5 分钟报一遍, 对 OS 没有任何新信息, 却是一整天的
 *	流量、电和账本条目. 位置挪了不到 100 米就当没动.
 *
 * ── 两档: 不动 / 在动 ──
 *
 *	上面那套是按"人一天大部分时候坐着"设计的, 而它在车上是错的:
 *	2026-09-11 他开车上班, 位置 4 分钟 / 120 米才一条, 还只有基站那一路 ——
 *	OS 那边问"现在在哪条路", 手上那条是两公里以前的.
 *
 *	所以分两档, 按**此刻是不是在动**切:
 *
 *	  不动   NETWORK        4 分钟 / 120 米     一天大部分时候
 *	  在动   GPS + NETWORK  25 秒 / 50 米       车上、走路
 *
 *	进"在动": 速度过 1.5 m/s, 或者前后两条位置算出来在挪, 或者车机蓝牙
 *	连上了(见 BtAudio). 出"在动": 5 分钟没有像样的挪动, 而且车没连着.
 *
 *	**电花在人在动的那一小段上**: 通勤一天一两个小时, 那一两个小时
 *	GPS 开着, 换来的是"他问路的时候答得出来". 其余二十多个小时照旧省.
 *
 * ── 时间戳必须是定位的时间 ──
 *
 *	上一版 queue 的时候盖的是"现在": 从系统缓存里拿出来一条三分钟前的
 *	位置, 发出去说是刚才的 —— OS 那边就当它是新鲜的, 而车早就开出去
 *	两公里了. **一个假的新坐标比"不知道"糟**, 因为它会被照着用.
 *
 *	所以位置那几条的 at 一律是这条定位**真正取到的时刻**(见 fixTime).
 */
class Collector(
    private val ctx: Context,
    private val base: () -> String,
    private val token: () -> String,
) {
    companion object {
        /** 位置最快多久报一次 —— 再快也没有意义, 人走不了那么远 */
        const val MIN_INTERVAL_MS = 4 * 60 * 1000L

        /**
         * 挪了这么远才算换了地方.
         *
         *	120 米: 比地点判定半径(150m)略小 —— 小一点是为了在人真正
         *	跨出一个地点的时候能报出来, 而不是刚好卡在边界上不动.
         */
        const val MIN_DISTANCE_M = 120f

        // ── 在动那一档 ──

        /**
         * 在动的时候多久要一条.
         *
         *	25 秒: 市区里 40 km/h 是 280 米 —— 大概一个路口. 再密就是
         *	GPS 一直醒着(冷启一次十几秒, 间隔短于这个它根本睡不下去),
         *	电就不是"通勤那一小时"的量级了.
         */
        const val MOVE_INTERVAL_MS = 25 * 1000L

        /**
         * 在动的时候挪多远报一次.
         *
         *	50 米: 比 GPS 在城里的抖动(10–30 米)大一截, 停在红绿灯下
         *	不会因为漂移刷出一串"又挪了".
         */
        const val MOVE_DISTANCE_M = 50f

        /**
         * 快过这个就算在动 —— 1.5 m/s ≈ 5.4 km/h, 快走的速度.
         *
         *	慢于它的"速度"多半是 GPS 漂出来的: 人坐着不动, 定位也会报
         *	0.3、0.8 m/s. 拿 0 当门槛的话, 桌上放着的手机会一直开着 GPS
         */
        const val MOVING_SPEED_MPS = 1.5f

        /**
         * 这么久没有像样的挪动, 就退回"不动"那一档.
         *
         *	5 分钟: 比一个长红灯、一次加油、堵车里的一段停滞都长 ——
         *	短了的话一路上会在两档之间来回切, 每切一次都是重挂一遍监听.
         *	车机蓝牙连着的时候不退(堵在路上也是在开车).
         */
        const val SETTLE_MS = 5 * 60 * 1000L

        /** 多久看一眼"该不该退档" —— 只在"在动"那一档里跑 */
        const val SETTLE_CHECK_MS = 60 * 1000L

        /**
         * 前后两条位置算速度时, 两条最多隔多久.
         *
         *	隔了半小时的两条算出来的是"平均速度", 一次步行加半小时的
         *	咖啡, 说明不了此刻在不在动
         */
        const val IMPLIED_MAX_GAP_MS = 10 * 60 * 1000L

        // ── OS 现在就要 ──

        /** 现取一次最多等多久. 等不到就算了, OS 那边照实说手上那条多旧 */
        const val ASK_TIMEOUT_MS = 10 * 1000L

        /** 系统缓存里那条多旧还值得先发. 发的时候带着它**真的时间** */
        const val CACHED_MAX_AGE_MS = 5 * 60 * 1000L

        /**
         * 现取的时候, 误差在这之内就算"像样", 当场用它.
         *
         *	100 米: 够认出是哪条路. 纯基站定位动辄几百上千米 —— 那种的
         *	先攥着当备胎, 等不到更好的才用
         */
        const val ASK_GOOD_ACC_M = 100f

        /**
         * 两次报到至少隔这么久.
         *
         *	心跳现在有两个来源: 进程里那个 8 分钟的定时器, 和 Doze 里的补课闹钟
         *	(见 KeepAliveService.catchup). 手机醒着的时候两个会前后脚响 ——
         *	一分钟之内报过就不再报, 省下一次射频唤醒
         */
        const val MIN_BEAT_GAP_MS = 60 * 1000L

        /** 攒到这么多条就发一次. 也可能被别的事件提前冲掉 */
        const val BATCH = 8

        /**
         * 多久报到一次"我还在".
         *
         * ── 为什么非有不可 ──
         *
         *	"人不动就不报"是省电的对的做法, 但它有一个后果: OS 那边分不出
         *	**"他没动"**和**"这个采集端死了"** —— 两者都是一段沉默.
         *	分不出就只能按最短的那个假设走(位置事实 10 分钟过期), 于是
         *	人一坐下来, 它就"忘了"你在哪. 而人不动恰恰是一天里的大部分时候.
         *
         *	真机上就是这样: 打开"我"那一页, "它现在知道什么"里一条位置都没有.
         *
         * ── 为什么是心跳而不是"再报一次位置" ──
         *
         *	再报一次位置的话, 一天 180 条"什么都没变"进账本 —— 而账本是
         *	只增不删的. 心跳**不进账本**(这正是它存在的理由, 见
         *	osinit/senseapi.go: "落账的话一天几千条, 那正是感知层最该
         *	避免的东西").
         *
         *	OS 那边只要知道"这个采集端还活着", 就敢继续信它报的最后一个
         *	位置 —— 因为它是**按移动触发**的: 没叫过, 就是没挪过.
         *
         *	而且心跳是一个空请求, 比一条带 body 的信号还便宜.
         */
        const val BEAT_MS = 8 * 60 * 1000L
    }

    private val pending = mutableListOf<JSONObject>()

    /** 上一条**报出去的**位置 —— 挪没挪够、要不要占用账本, 拿它比 */
    private var lastLat = 0.0
    private var lastLon = 0.0
    private var lastAt = 0L

    /**
     * 上一条**收到的**位置 —— 算"在不在动"用.
     *
     *	跟上面那条分开: 报出去的那条可能是一小时前的(人一直没挪),
     *	拿它跟现在这条算速度, 算出来的是一小时的平均, 说明不了此刻
     */
    private var prevLat = 0.0
    private var prevLon = 0.0
    private var prevAt = 0L
    private var prevAcc = 0f

    /** 上一条 GPS 定位的时间和精度 —— 两路同时开着的时候, 拿它挡掉更糙的那条基站定位 */
    private var lastGpsAt = 0L
    private var lastGpsAcc = Float.MAX_VALUE

    private var lastWifi = ""

    /** 现在是哪一档. 只在采集线程上读写 */
    private var moving = false

    /** 上一次看到像样的挪动 —— elapsedRealtime, 不怕改系统时间 */
    private var lastMovedAt = 0L

    /** 上一次心跳真的报到了 —— elapsedRealtime. 见 MIN_BEAT_GAP_MS */
    @Volatile private var lastBeatOk = 0L
    private val beatLock = Any()

    /**
     * 车机蓝牙. 连着的时候一直在"在动"那一档 —— 堵车时速度是 0,
     * 而人确实在开车
     */
    private val bt = BtAudio(ctx,
        onChange = { name, on, car -> ticker.post { onBluetooth(name, on, car) } },
        onAlreadyCar = { ticker.post { enterMoving("车机蓝牙本来就连着") } })

    /** 上一次的电量和插没插 —— **只报变化**用. null = 还没收到过第一条 */
    private var lastPlugged: Boolean? = null
    private var lastPct = -1
    private var running = false

    private val lm by lazy { ctx.getSystemService(Context.LOCATION_SERVICE) as LocationManager }

    /**
     * 采集自己的一条线程 —— **网络绝不能在主线程上**.
     *
     * ── 这一条静默地废掉了整个采集 ──
     *
     *	第一版所有东西都跑在主 looper 上: 位置回调、心跳、上传.
     *	安卓对主线程上的网络请求抛 NetworkOnMainThreadException,
     *	而 flush() 里那个 catch-all(它本来是给"网不通"准备的)把它吞了.
     *
     *	表现: 一切看着正常 —— 服务在跑、权限给了、开关是开的 ——
     *	而**一条信号都没有真的发出去**. 服务端那边看到的是"这个采集端
     *	从来没报到过", 而手机这边一个错都不报.
     *
     *	所以位置回调也挂在这条线程上: 回调本身会调 flush().
     */
    private val thread = android.os.HandlerThread("neox-collect").apply { start() }
    private val ticker = android.os.Handler(thread.looper)

    /** 位置来了 —— 系统只在人真的挪了 MIN_DISTANCE_M 之后才叫我们 */
    private val onLoc = Fix { loc -> onLocation(loc) }

    /**
     * 电量 —— **纯白捡**: 这条广播本来就在发, 我们只是顺路听一耳朵,
     * 不额外唤醒任何东西.
     *
     *	它有用是因为"手机快没电了"是一条真实的、会改变人行为的事实:
     *	OS 可以据此判断"这会儿别指望他看手机".
     */
    private val onBattery = object : BroadcastReceiver() {
        override fun onReceive(c: Context?, i: Intent?) {
            if (i == null) return
            val level = i.getIntExtra(BatteryManager.EXTRA_LEVEL, -1)
            val scale = i.getIntExtra(BatteryManager.EXTRA_SCALE, 100)
            if (level < 0 || scale <= 0) return
            val pct = level * 100 / scale
            val plugged = i.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) != 0
            // ── 只报**变化**, 不报状态 ──
            //
            //	ACTION_BATTERY_CHANGED 是每几秒一条的. 上一版的闸是
            //	`if (pct > 20 && !plugged) return` —— 插着电的时候
            //	**一条都拦不住**, 于是充一夜等于往账本里塞几百条
            //	一模一样的"在充电（59%）".
            //
            //	真机上抓到过: 一份 5 分钟的摘要里 50 条 battery.level,
            //	而模型对着它们只能回一句"没什么要紧的" —— 那正是
            //	感知层最该避免的东西.
            //
            //	有信息量的只有三件事: 插上了/拔了、第一次跌破 20%、充满了.
            //	别的一律不报.
            val was = lastPlugged
            val crossedLow = pct <= 20 && !plugged && (lastPct > 20 || lastPct < 0)
            val full = plugged && pct >= 100 && lastPct < 100
            val pluggedChanged = was != null && was != plugged
            lastPlugged = plugged
            lastPct = pct
            if (!pluggedChanged && !crossedLow && !full) return
            // **挪到采集线程**: 广播是在主线程收的, 而 queue 可能触发
            // 一次上传 —— 见 thread 那段
            ticker.post { queueBattery(pct, plugged) }
        }
    }

    private fun queueBattery(pct: Int, plugged: Boolean) {
        queue(
            "battery.level", JSONObject()
                .put("pct", pct)
                .put("plugged", plugged)
                .put(
                    "text", when {
                        plugged && pct >= 100 -> "充满了"
                        plugged -> "插上了（$pct%）"
                        pct <= 20 -> "手机快没电了（$pct%）"
                        else -> "拔了（$pct%）"
                    }
                )
                // 电量这种事半小时之后就不作数了
                .put("validSec", 1800)
        )
    }

    /**
     * 电量的**快照** —— OS 问起来的时候给的, 不是"变了".
     *
     *	平时那条是只报变化的(见 onBattery), 于是 OS 手上最新那条可能是
     *	早上拔电那一刻的"拔了（100%）". 他问"手机还有多少电"的时候,
     *	这条才是答案. 电量广播是粘性的, 读一下不唤醒任何东西
     */
    private fun queueBatterySnapshot() {
        val i = try {
            ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        } catch (e: Exception) { null } ?: return
        val level = i.getIntExtra(BatteryManager.EXTRA_LEVEL, -1)
        val scale = i.getIntExtra(BatteryManager.EXTRA_SCALE, 100)
        if (level < 0 || scale <= 0) return
        val pct = level * 100 / scale
        val plugged = i.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) != 0
        queue(
            "battery.level", JSONObject()
                .put("pct", pct)
                .put("plugged", plugged)
                .put("text", if (plugged) "在充电（$pct%）" else "电量 $pct%")
                .put("validSec", 1800)
        )
    }

    /** WiFi 的快照 —— 同上. 没连着就不报: "没连"不等于"不在家" */
    private fun queueWifiSnapshot() {
        val ssid = wifiNow() ?: return
        lastWifi = ssid
        queue("network.wifi", JSONObject().put("ssid", ssid).put("text", "连着 $ssid"))
    }

    /** 有没有拿到定位权限 —— 没有的话整段位置采集不启动, 而不是每次报错 */
    fun canLocate(): Boolean =
        ctx.checkSelfPermission(Manifest.permission.ACCESS_COARSE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED

    /** GPS 要精确定位权限 —— 只给了粗定位的话, "在动"那档就只有基站那一路 */
    private fun canGps(): Boolean =
        ctx.checkSelfPermission(Manifest.permission.ACCESS_FINE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED

    fun start() {
        // 已经在跑的话**重挂一遍监听**: 服务是在权限刚给下来的时候被重起的
        // (见 MainActivity.onRequestPermissionsResult) —— 不重挂的话, 刚给的
        // 精确定位要等到下次进程重起才用得上
        if (running) {
            ticker.post { subscribe() }
            return
        }
        running = true
        ctx.registerReceiver(onBattery, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        // **心跳跟位置无关, 先挂上**: 没有定位权限的时候它照样要报到 ——
        // 电量那一路还在, 而"这台设备还活着"本身就是 OS 要的信息
        ticker.post(beat)
        // 蓝牙也跟位置无关: 没给定位, "他上车了"照样是一条有用的事实
        bt.start()
        if (!canLocate()) return
        ticker.post { subscribe() }
    }

    /**
     * 按现在这一档挂监听. 切档的时候先全摘掉再挂 —— 两档的门槛不同,
     * 同一个 listener 在同一个 provider 上重挂, 系统按新的算.
     *
     *	requestLocationUpdates 的两个门槛都交给系统: 人不动的时候
     *	这个进程根本不会被唤醒, 而"不被唤醒"才是真正省电的那一步
     */
    private fun subscribe() {
        if (!running || !canLocate()) return
        try { lm.removeUpdates(onLoc) } catch (e: Exception) { /* 本来就没挂 */ }
        val (every, far) = if (moving) MOVE_INTERVAL_MS to MOVE_DISTANCE_M
        else MIN_INTERVAL_MS to MIN_DISTANCE_M
        try {
            lm.requestLocationUpdates(
                LocationManager.NETWORK_PROVIDER, every, far, onLoc, thread.looper
            )
        } catch (e: SecurityException) {
            // 权限在运行时被撤了 —— 不崩, 只是这一路没有了
        } catch (e: IllegalArgumentException) {
            // 这台设备没有 NETWORK 定位(极少见的定制 ROM)
        }
        // **GPS 只在"在动"那一档挂**: 见开头 ①
        if (moving && canGps()) {
            try {
                lm.requestLocationUpdates(
                    LocationManager.GPS_PROVIDER, every, far, onLoc, thread.looper
                )
            } catch (e: Exception) {
                // 没有 GPS / 权限被撤 —— 基站那一路照样在
            }
        }
    }

    /**
     * 进"在动"那一档.
     *
     *	已经在这一档里的话只续一下时间 —— 不重挂: 重挂一次监听, GPS 就要
     *	重新找一次星
     */
    private fun enterMoving(why: String) {
        lastMovedAt = SystemClock.elapsedRealtime()
        if (moving) return
        moving = true
        android.util.Log.i("neox", "采集切到在动: $why")
        subscribe()
        ticker.removeCallbacks(settle)
        ticker.postDelayed(settle, SETTLE_CHECK_MS)
    }

    /** 看一眼该不该退回"不动" —— 只在"在动"那一档里跑 */
    private val settle = object : Runnable {
        override fun run() {
            if (!running || !moving) return
            val quiet = SystemClock.elapsedRealtime() - lastMovedAt
            if (quiet >= SETTLE_MS && !bt.carLinked) {
                moving = false
                android.util.Log.i("neox", "采集退回不动: ${quiet / 1000} 秒没挪")
                subscribe()
                return
            }
            ticker.postDelayed(this, SETTLE_CHECK_MS)
        }
    }

    /** 蓝牙音频连上/断开 —— 在采集线程上 */
    private fun onBluetooth(name: String, on: Boolean, car: Boolean) {
        queue(
            "bluetooth.audio", JSONObject()
                .put("name", name)
                .put("connected", on)
                .put("car", car)
                .put("text", btText(name, on, car))
        )
        if (car && on) enterMoving("车机蓝牙连上了")
        // 车机断开 = 多半刚停好车. **不立刻退档**: 再采 5 分钟, 停车的
        // 那个位置("车停哪了")正是这几分钟里取到的. 退档交给 settle
        if (car && !on) lastMovedAt = SystemClock.elapsedRealtime()
        flush()
    }

    /**
     * 报到 —— "我还在, 我的节奏是每 N 秒一次".
     *
     *	**不进账本**(见 BEAT_MS). 它唯一的作用是让 OS 分得出
     *	"他没动"和"这个采集端死了".
     *
     *	顺路也把攒着还没发出去的那几条冲一下: 一个位置信号可能因为
     *	没凑够一批而躺在内存里, 而进程随时会被杀.
     */
    private val beat = object : Runnable {
        override fun run() {
            if (!running) return
            beatOnce()
            ticker.postDelayed(this, BEAT_MS)
        }
    }

    /**
     * 补课闹钟叫醒的那一次也报到 —— **在调用者的线程上同步做完**.
     *
     * ── 为什么非有不可 ──
     *
     *	上面那个 beat 挂在 Handler.postDelayed 上, 而**手机一进 Doze,
     *	CPU 睡着, 这个定时器就不走了**. 2026-09-11 那一夜: 00:30 到
     *	08:04 一次报到都没有, OS 那边以为这个采集端死了. 闹钟是 Doze 里
     *	唯一还会响的东西(见 KeepAliveService.scheduleCatchup), 所以报到
     *	得搭它的车.
     *
     *	同步做, 因为调用者手里拿着 wake lock —— 丢到采集线程上异步去做,
     *	锁一放, CPU 可能在请求发出去之前就睡回去了.
     *
     *	顺手把进程里那个定时器往后推: 刚报过, 八分钟之后再说
     */
    fun beatFromAlarm() {
        if (!running) return
        beatOnce()
        ticker.removeCallbacks(beat)
        ticker.postDelayed(beat, BEAT_MS)
    }

    private fun beatOnce() {
        flush()
        // 一分钟内报过就不报 —— 见 MIN_BEAT_GAP_MS. **flush 照做**:
        // 攒着的那几条跟心跳是两件事
        synchronized(beatLock) {
            val now = SystemClock.elapsedRealtime()
            if (lastBeatOk != 0L && now - lastBeatOk < MIN_BEAT_GAP_MS) return
            if (heartbeat()) lastBeatOk = now
        }
        // 顺路扫一眼日历 —— **搭心跳的车**: 日历一天变不了几次,
        // 单开一个定时器等于多一次唤醒, 而唤醒才是耗电的那一半
        Agenda.sweep(ctx)
    }

    /**
     * 报到的节奏 —— **报最坏的那个**.
     *
     *	醒着的时候是 8 分钟一次; 睡着之后只有闹钟, 而闹钟的节奏看这个
     *	App 在不在电池白名单里(见 KeepAliveService.catchupEvery). 报 8 分钟
     *	而实际 15 分钟才来一次的话, OS 那边每一夜都会判它"掉线"好几回 ——
     *	那种"掉线了又回来了"的噪音, 跟真掉线分不开
     */
    private fun everySec(): Long =
        maxOf(BEAT_MS, KeepAliveService.catchupEvery(ctx)) / 1000

    /** 一次报到. 报上了返回 true */
    private fun heartbeat(): Boolean {
        val b = base().trimEnd('/')
        val t = token()
        if (b.isNotEmpty() && t.isNotEmpty()) {
            try {
                val url = "$b/heartbeat?source=" +
                    URLEncoder.encode(deviceId(), "UTF-8") +
                    "&everySec=" + everySec() +
                    // ── 时区跟着心跳走 ──
                    //
                    //	Docker 里默认是 UTC, 而时区进的是**判断**不是显示:
                    //	日报几点发、"晚上七点提醒我"算哪一段、"明天早上八点"
                    //	是哪一刻 —— 全都差 8 小时, 而一处都不会报错.
                    //
                    //	**这是设备自己知道的事, 不该让人去设置页里填**.
                    //
                    //	挂在心跳上而不是每条信号上: 心跳不进账本, 也不进
                    //	摘要 —— 时区是设备的属性, 不是一条观测. 塞进 body
                    //	的话它会出现在给模型看的摘要里("最新: tz=…"),
                    //	纯噪音.
                    //
                    //	跟设备种类无关: 换成 ESP32 或者耳机照样带一个就行
                    "&tz=" + URLEncoder.encode(
                        java.util.TimeZone.getDefault().id, "UTF-8"
                    )
                val conn = (URL(url).openConnection() as HttpURLConnection).apply {
                    requestMethod = "POST"
                    setRequestProperty("Authorization", "Bearer $t")
                    connectTimeout = 15000
                    readTimeout = 15000
                }
                val code = conn.responseCode
                conn.disconnect()
                return code in 200..299
            } catch (e: Exception) {
                // 网不通 —— 下一轮再说. 报到失败不该让采集停下来
            }
        }
        return false
    }

    fun stop() {
        if (!running) return
        running = false
        ticker.removeCallbacks(beat)
        ticker.removeCallbacks(settle)
        try { lm.removeUpdates(onLoc) } catch (e: Exception) { /* 本来就没挂上 */ }
        try { ctx.unregisterReceiver(onBattery) } catch (e: Exception) { /* 同上 */ }
        bt.stop()
        // **最后一次上传也要在采集线程上** —— 主线程上发请求会抛,
        // 而那个异常会被 flush 里的 catch-all 吞掉(见 thread 那段)
        ticker.post {
            flush()
            thread.quitSafely()
        }
    }

    /**
     * OS 现在就要 —— **绕开那两道门槛**.
     *
     * ── 为什么要绕 ──
     *
     *	平时那两道门槛(挪够 120 米 / 隔够 4 分钟)是为了省电, 而它们的
     *	代价是**他问"我在哪条路"的时候, 手上最新那条是几分钟前的**。
     *	市区里三分钟是两个路口 —— 答一个过期的路名比说"不知道"糟,
     *	因为他会照着走。
     *
     *	所以这条路上门槛全不算数: 他是**问了才走这条**的, 一天几次,
     *	而按变化触发那套省下的是一整天的电。
     *
     * ── 拿最后一次已知的先垫上, 但**带着它真的时间** ──
     *
     *	冷启一次 GPS 要几十秒, 而他在等着看一句话。系统缓存里那条
     *	通常是几秒前的, 够用; 真的取到新的再补发一条。
     *
     *	上一版垫上去的那条盖的是"现在"的戳 —— 三分钟前的位置被 OS 当成
     *	刚取的, 那正是 2026-09-11 答错路名的原因之一. 现在它的 at 是
     *	定位那一刻(见 fixTime), OS 自己看得出它多旧.
     *
     * ── 顺手一起给: WiFi 和电量 ──
     *
     *	他问"我在哪"的时候, 连着哪个 WiFi 是"在不在家/公司"最准的那条,
     *	而电量那条平时只报变化, OS 手上可能是早上的. 同一次射频唤醒
     *	一起发掉, 比它下一轮再来问一次便宜.
     *
     * @param what OS 要的是什么: location(缺省) / battery / wifi.
     *   location 那条三样都给; 另外两样只给自己那一样
     */
    fun askNow(what: String = "location") {
        ticker.post {
            try {
                when (what) {
                    "battery" -> { queueBatterySnapshot(); flush(); return@post }
                    "wifi" -> { queueWifiSnapshot(); flush(); return@post }
                }
                queueWifiSnapshot()
                queueBatterySnapshot()
                if (!canLocate()) {
                    android.util.Log.i("neox", "askNow: 没有定位权限")
                    flush()
                    return@post
                }
                android.util.Log.i("neox", "askNow: OS 要一次位置")
                // 缓存里最新的那条先发 —— 两路里挑**定位时间**最近的,
                // 不是挑先问到的那一路
                val cached = listOf(LocationManager.GPS_PROVIDER, LocationManager.NETWORK_PROVIDER)
                    .mapNotNull { p ->
                        try { lm.getLastKnownLocation(p) } catch (e: Exception) { null }
                    }
                    .maxByOrNull { fixTime(it) }
                if (cached != null &&
                    System.currentTimeMillis() - fixTime(cached) < CACHED_MAX_AGE_MS
                ) {
                    android.util.Log.i("neox", "askNow: 缓存里那条先发 " + cached.provider)
                    onFix(cached, force = true)
                }
                // 不管有没有位置, WiFi 和电量这会儿就发 —— 别让它们等 GPS
                flush()
                askFresh()
            } catch (e: Exception) {
                // 取不到就算了 —— **不编**: OS 那边等不到就照实说
                // 手上这条多旧, 那比一个假的新坐标强
            }
        }
    }

    /**
     * 现取一条 —— **GPS 和基站两路一起要, 谁先给一条像样的就用谁**.
     *
     *	只要基站那一路的话(上一版), 在车上拿到的是几百米误差的一条;
     *	只要 GPS 的话, 在室内等十秒什么都没有. 两路一起要, 先到的像样的
     *	那条赢, 另一路当场摘掉 —— GPS 不会因为这一问一直醒着.
     *
     *	"像样" = 误差不超过 ASK_GOOD_ACC_M. 先来的是一条糙的(纯基站定位
     *	动辄上千米)就先攥着, 十秒到了还没有更好的, 才拿它交差.
     */
    private fun askFresh() {
        var done = false
        var best: Location? = null
        val providers = buildList {
            add(LocationManager.NETWORK_PROVIDER)
            if (canGps()) add(LocationManager.GPS_PROVIDER)
        }
        lateinit var l: LocationListener
        val timeout = Runnable {
            if (done) return@Runnable
            done = true
            try { lm.removeUpdates(l) } catch (e: Exception) {}
            best?.let { onFix(it, force = true) }
            android.util.Log.i("neox", "askFresh: 等满 ${ASK_TIMEOUT_MS / 1000} 秒, " +
                if (best == null) "一条都没有" else "拿糙的那条交差")
        }
        l = Fix { loc ->
            if (done) return@Fix
            if (loc.accuracy > ASK_GOOD_ACC_M) {
                if (best == null || loc.accuracy < best!!.accuracy) best = loc
                return@Fix
            }
            done = true
            ticker.removeCallbacks(timeout)
            try { lm.removeUpdates(l) } catch (e: Exception) {}
            onFix(loc, force = true)
        }
        var any = false
        for (p in providers) {
            try {
                lm.requestLocationUpdates(p, 0L, 0f, l, thread.looper)
                any = true
            } catch (e: Exception) {
                // 这一路没有 / 关着 —— 另一路照样在
            }
        }
        if (any) ticker.postDelayed(timeout, ASK_TIMEOUT_MS)
    }

    private fun onLocation(loc: Location) = onFix(loc, force = false)

    /**
     * 这条定位**真正取到的时刻**, 按手机的墙钟算.
     *
     *	不直接用 Location.time: GPS 那一路的 time 是卫星给的 UTC, 跟手机
     *	自己的钟可能差几秒, 有的芯片还有周数翻转的老毛病(差出去几年).
     *	elapsedRealtimeNanos 跟 SystemClock 是同一个钟, 拿它算出"这条
     *	多旧了", 再从现在往回减 —— 跟别的信号的 at 在同一个钟上.
     */
    private fun fixTime(loc: Location): Long {
        val now = System.currentTimeMillis()
        val ns = loc.elapsedRealtimeNanos
        if (ns > 0) {
            val ageMs = (SystemClock.elapsedRealtimeNanos() - ns) / 1_000_000
            if (ageMs >= 0) return now - ageMs
        }
        return if (loc.time in 1..now) loc.time else now
    }

    /**
     * 这一条算不算"在动" —— 速度, 或者跟上一条收到的比挪了多远.
     *
     *	速度只信定位自己报的(hasSpeed): GPS 那一路才有, 基站那一路没有.
     *	没有速度的时候拿前后两条算 —— 但**两条的误差圈加起来盖得住的
     *	挪动不算**: 基站定位在同一个地方来回跳一两百米是常事.
     */
    private fun judgeMotion(loc: Location, at: Long) {
        val fast = loc.hasSpeed() && loc.speed >= MOVING_SPEED_MPS
        var moved = 0.0
        var implied = false
        if (prevAt != 0L) {
            val dt = at - prevAt
            if (dt in 1..IMPLIED_MAX_GAP_MS) {
                moved = metersBetween(prevLat, prevLon, loc.latitude, loc.longitude)
                val noise = maxOf(MOVE_DISTANCE_M, loc.accuracy + prevAcc).toDouble()
                implied = moved > noise && moved / (dt / 1000.0) >= MOVING_SPEED_MPS
            }
        }
        if (at > prevAt) {
            prevLat = loc.latitude
            prevLon = loc.longitude
            prevAt = at
            prevAcc = loc.accuracy
        }
        if (fast) enterMoving("速度 %.1f m/s".format(loc.speed))
        else if (implied) enterMoving("两条位置之间挪了 ${moved.toInt()} 米")
    }

    private fun onFix(loc: Location, force: Boolean) {
        val at = fixTime(loc)
        judgeMotion(loc, at)
        val gps = loc.provider == LocationManager.GPS_PROVIDER
        // ── 两路同时开着的时候, 糙的那条让路 ──
        //
        //	"在动"那一档 GPS 和基站都挂着, 同一段路两边各报一条. 基站那条
        //	误差大十倍, 夹在两条 GPS 中间报出去, OS 那边的轨迹就是锯齿 ——
        //	刚拿到过一条更准的 GPS 的话, 基站这条不要
        if (gps) {
            lastGpsAt = at
            lastGpsAcc = loc.accuracy
        } else if (!force && lastGpsAt != 0L &&
            at - lastGpsAt < MOVE_INTERVAL_MS * 2 && loc.accuracy > lastGpsAcc
        ) {
            return
        }
        // **不报比已经报过的更旧的**: 缓存里那条可能早于上一条, 报出去
        // OS 那边"最新位置"就倒退了. 同一条也不再报(同一个定位时间)
        if (lastAt != 0L && at <= lastAt) return
        // 系统那两个门槛已经过了一道, 这里再挡一道 —— **两道不是重复**:
        // 系统那道是"要不要唤醒我们", 这道是"要不要占用射频和账本".
        // 有些 ROM 的 minDistance 是摆设
        val gate = if (moving) MOVE_DISTANCE_M else MIN_DISTANCE_M
        if (!force && lastAt != 0L &&
            metersBetween(lastLat, lastLon, loc.latitude, loc.longitude) < gate
        ) {
            return
        }
        lastLat = loc.latitude
        lastLon = loc.longitude
        lastAt = at
        val body = JSONObject()
            .put("lat", loc.latitude)
            .put("lon", loc.longitude)
            .put("acc", loc.accuracy.toDouble())
        loc.provider?.let { body.put("provider", it) }
        if (loc.hasSpeed()) body.put("speed", Math.round(loc.speed * 10) / 10.0)
        if (loc.hasBearing()) body.put("bearing", Math.round(loc.bearing).toDouble())
        // 一句人话 —— **只在有速度的时候说**: 基站那一路没有速度, 那时候
        // 说"停着"是编的. 地名 OS 那边自己查(见 signalview), 这里不说
        if (loc.hasSpeed()) {
            body.put(
                "text",
                if (loc.speed >= MOVING_SPEED_MPS) "在动（约 ${Math.round(loc.speed * 3.6)} km/h）"
                else "停着"
            )
        }
        // **at 是定位那一刻, 不是现在** —— 见开头"时间戳必须是定位的时间"
        queue("location", body, at)
        // 换地方的时候顺路看一眼连的是哪个 WiFi —— 白捡的信号(见开头 ③)
        wifiNow()?.let { ssid ->
            if (ssid != lastWifi) {
                lastWifi = ssid
                queue("network.wifi", JSONObject().put("ssid", ssid).put("text", "连着 $ssid"))
            }
        }
        flush()
    }

    /**
     * 现在连的是哪个 WiFi.
     *
     *	安卓 10 起拿 SSID 要定位权限 —— 拿不到就算了, 不报错:
     *	这条是锦上添花, 不该因为它让整段采集看起来是坏的.
     */
    private fun wifiNow(): String? {
        return try {
            val wm = ctx.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
            @Suppress("DEPRECATION")
            val ssid = wm.connectionInfo?.ssid?.trim('"') ?: return null
            if (ssid.isBlank() || ssid == "<unknown ssid>") null else ssid
        } catch (e: Exception) {
            null
        }
    }

    /**
     * 攒一条.
     *
     * @param at 这件事**发生**的时刻. 缺省是现在 —— 电量、WiFi、蓝牙这些
     *   是广播一来就报的, "现在"就是它发生的时候. 位置不是: 它可能是
     *   缓存里拿出来的, 必须传定位那一刻(见 fixTime)
     */
    @Synchronized
    private fun queue(kind: String, body: JSONObject, at: Long = System.currentTimeMillis()) {
        pending.add(
            JSONObject()
                // **幂等键带上时间**: 补发和重试是常态, 而 OS 那边靠这个
                // 去重 —— 没有它的话一次重试会在账本里多一条.
                // 位置那几条用的是定位时间, 于是同一条定位被报两次(缓存那条
                // 又被问到一次)也只落一条
                .put("id", "${deviceId()}-$kind-$at")
                .put("source", deviceId())
                .put("kind", kind)
                .put("at", at)
                .put("body", body)
        )
        if (pending.size >= BATCH) flush()
    }

    /**
     * 攒着的一起发.
     *
     *	**发不出去就留着**: 手机的网是断续的, 而位置这类信号迟到了照样
     *	有用(OS 那边有补传通道). 丢掉的话, 地铁里那一段就是一个洞,
     *	而没有任何一处会说.
     *
     *	但留着要有上限 —— 见下面那句 trim.
     */
    @Synchronized
    fun flush() {
        if (pending.isEmpty()) return
        val b = base().trimEnd('/')
        val t = token()
        if (b.isEmpty() || t.isEmpty()) return

        val batch = JSONArray()
        pending.forEach { batch.put(it) }
        try {
            val conn = (URL("$b/signals").openConnection() as HttpURLConnection).apply {
                requestMethod = "POST"
                setRequestProperty("Authorization", "Bearer $t")
                setRequestProperty("Content-Type", "application/json")
                connectTimeout = 15000
                readTimeout = 20000
                doOutput = true
            }
            conn.outputStream.use { it.write(batch.toString().toByteArray()) }
            val code = conn.responseCode
            conn.disconnect()
            if (code in 200..299) {
                pending.clear()
                return
            }
            // 400 一类是**这批本身有问题**, 留着只会一直失败 —— 丢掉.
            // 5xx 和网络错才值得留
            if (code in 400..499) pending.clear()
        } catch (e: Exception) {
            // 网不通 —— 留着下次发
        }
        // 攒太多就丢最老的: 一部离线一周的手机不该把内存吃光,
        // 而一周前的位置也早就没人要了
        while (pending.size > 200) pending.removeAt(0)
    }

    private fun deviceId(): String =
        KeepAliveService.prefs(ctx).getString("deviceId", "") ?: "phone"

    /** 两点间多少米 —— 等距圆柱近似, 一百公里内误差可忽略 */
    private fun metersBetween(lat1: Double, lon1: Double, lat2: Double, lon2: Double): Double {
        val dLat = (lat2 - lat1) * 111_320.0
        val dLon = (lon2 - lon1) * 111_320.0 * cos(Math.toRadians((lat1 + lat2) / 2))
        return sqrt(dLat * dLat + dLon * dLon).let { if (abs(it) < 0.001) 0.0 else it }
    }
}

/** 这台安卓要不要单独申请后台定位 —— 10 以上要 */
fun needsBackgroundLocation(): Boolean = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q

/**
 * 位置回调 —— **四个方法都实现, 不用 SAM 那一行写法**.
 *
 *	LocationListener 另外三个方法是安卓 11 才变成默认实现的. 在 8–10
 *	上系统照样会调 onStatusChanged, 而 lambda 那种写法没有这个方法 ——
 *	AbstractMethodError, 整个采集线程崩掉.
 */
class Fix(private val got: (Location) -> Unit) : LocationListener {
    override fun onLocationChanged(loc: Location) = got(loc)

    @Deprecated("老接口, 8–10 上系统还会调")
    @Suppress("DEPRECATION")
    override fun onStatusChanged(provider: String?, status: Int, extras: android.os.Bundle?) {}

    override fun onProviderEnabled(provider: String) {}

    override fun onProviderDisabled(provider: String) {}
}
