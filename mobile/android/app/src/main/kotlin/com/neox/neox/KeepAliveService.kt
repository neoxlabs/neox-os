package com.neox.neox

import android.Manifest
import android.app.AlarmManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import android.os.SystemClock
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder
import kotlin.concurrent.thread

/**
 * 常驻服务 —— **"早上主动喊你"这件事的全部依赖**.
 *
 * ── 为什么不能是 Dart 那边的一个定时器 ──
 *
 * Flutter 的引擎跟着 Activity 走. 用户一划掉任务卡、或者系统一回收后台,
 * Dart 侧的定时器和长连接当场就没了 —— 而且是**静默的**:
 * 没有报错, 只是从此不再响. 用户看到的是"它不提醒我了", 查不到根.
 *
 * ── 两条腿, 因为一条走不通 ──
 *
 * 只有前台服务是不够的: 手机半夜进 Doze 之后, **连前台服务的网络也会被掐**,
 * 只在维护窗口里放行. 于是"我睡前它还连着"跟"早上七点它能喊我"
 * 是两件事, 靠一条长连接同时办到是幻想.
 *
 *	① 前台服务 + SSE     醒着的时候实时. 断了自己接回来
 *	② AlarmManager 补课  Doze 里照样会响(setAndAllowWhileIdle),
 *	                     醒来就补一次课: 连上去、把欠的读完、该响的响,
 *	                     **顺路报到、把攒着的信号发掉**(见 catchupAwake)
 *
 * **第二条才是早上那一嗓子的真正来源**, 第一条只是让白天更跟手.
 *
 * ── 补课怎么补 ──
 *
 * OS 的 /stream 会先按游标把欠的补齐, 补完发一行 `: live` 再转直播.
 * 所以补课 = 连上去、读到 `: live` 为止、断开. 不用另开一个接口,
 * 也不用轮询 —— 那一行注释就是"欠的都给你了"的信号.
 */
class KeepAliveService : Service() {

    companion object {
        const val CH_ALIVE = "neox.alive"
        const val CH_NOTICE = "neox.notice"
        const val CH_REMIND = "neox.remind"
        const val CH_DAILY = "neox.daily"
        const val NOTIF_ALIVE = 1
        const val ACTION_CATCHUP = "com.neox.neox.CATCHUP"

        /** 补课的节奏. Doze 里 setAndAllowWhileIdle 最快也就 ~9 分钟一次,
         *  写 15 分钟是**照系统的规矩来**, 写 1 分钟只会被静默地拉长 —— 
         *  而那种"我以为一分钟一次"的假设最难查 */
        const val CATCHUP_MS = 15 * 60 * 1000L

        /**
         * 补课那一下最多攥着 CPU 多久.
         *
         *	补课 = 报到(15s 超时) + 冲一批信号(20s) + 读欠的(30s). 闹钟本身
         *	只保证叫醒的那十来秒, 之后 CPU 随时会睡回去 —— **请求发到一半
         *	睡着了, 那一次就白叫了**. 所以自己攥一把锁, 并且一定带超时:
         *	哪一步卡住了, 锁也会自己放, 不会整夜不让手机睡
         */
        const val CATCHUP_WAKE_MS = 90 * 1000L

        @Volatile var running = false
        @Volatile var note = ""

        fun prefs(c: Context) = c.getSharedPreferences("neox_keepalive", Context.MODE_PRIVATE)

        /** 这个 App 在不在系统的电池白名单里 */
        fun batteryExempt(c: Context): Boolean = try {
            (c.getSystemService(Context.POWER_SERVICE) as PowerManager)
                .isIgnoringBatteryOptimizations(c.packageName)
        } catch (e: Exception) {
            false
        }

        /**
         * 补课闹钟多久响一次 —— **看在不在白名单里**.
         *
         *	在白名单里: 跟心跳一个节奏(8 分钟). Doze 里 AllowWhileIdle 的闹钟
         *	最快也就九分钟上下一次, 写 8 分钟会被系统拉到九分钟左右 —— 那没
         *	关系, 要的是"睡着了也在报到", 不是精确到秒.
         *
         *	不在白名单里: 照旧 15 分钟. 不在白名单的 App 在 Doze 里连网都
         *	只在闹钟那十来秒放行, 叫得再勤也是一次次白叫.
         *
         *	**不用 setExactAndAllowWhileIdle**: 安卓 12 起它要"精确闹钟"权限,
         *	13 起缺省不给, 没给的时候调用直接抛 —— 为了省那一分钟不值
         */
        fun catchupEvery(c: Context): Long =
            if (batteryExempt(c)) Collector.BEAT_MS else CATCHUP_MS
    }

    private var base = ""
    private var token = ""
    @Volatile private var alive = false
    private var worker: Thread? = null

    /**
     * 采集 —— 这台手机看得见什么, 报给那台 OS. 见 Collector.kt.
     *
     *	**挂在这个服务里而不是另起一个**: 它已经是前台服务、已经活着,
     *	而一个被动的位置监听几乎不花电. 另起一个服务的代价是多一个
     *	常驻通知和多一处会被系统杀掉的地方.
     */
    private var collector: Collector? = null


    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val p = prefs(this)
        base = intent?.getStringExtra("base") ?: p.getString("base", "")!!
        token = intent?.getStringExtra("token") ?: p.getString("token", "")!!
        p.edit().putString("base", base).putString("token", token).apply()

        if (base.isBlank() || token.isBlank()) {
            note = "没有地址或 token"
            stopSelf()
            return START_NOT_STICKY
        }

        channels()
        goForeground("待命")
        running = true
        scheduleCatchup()

        // 采集跟着服务一起起 —— 用户在设置里关掉的话就不起.
        //
        //	**要在补课那个分支之前**: 进程在夜里被杀过的话, 闹钟叫起来的
        //	是一个全新的进程, 走的是补课那条路. 原来采集只在下面直播那条
        //	路上起, 于是那一夜剩下的时间里一次报到都没有 —— 服务"在",
        //	采集不在
        if (p.getBoolean("collect", false)) {
            if (collector == null) {
                collector = Collector(applicationContext, { base }, { token })
            }
            collector?.start()
        } else {
            collector?.stop()
            collector = null
        }

        // 补课那一下是闹钟叫起来的: 读完欠的就收工, 不要开长连接 ——
        // Doze 里那条流本来也活不长, 而 CPU 醒着的每一秒都是电
        if (intent?.action == ACTION_CATCHUP) {
            thread(name = "neox-catchup") {
                catchupAwake()
                scheduleCatchup()
            }
            return START_STICKY
        }

        if (!alive) {
            alive = true
            worker = thread(name = "neox-stream") { loop() }
        }
        return START_STICKY
    }

    /**
     * 起前台服务 —— **类型要按此刻的权限现算, 不能照抄 manifest**.
     *
     * ── 这一条崩过一次, 而且是开机即崩 ──
     *
     *	manifest 里写 `foregroundServiceType="dataSync|location"`, 本意是
     *	"这个服务有可能用到位置". 但安卓 14 起它不是这个意思: **声明了
     *	location, 就意味着每一次 startForeground 都要求定位权限** ——
     *	不管这一次到底用不用位置.
     *
     *	而这个服务在 App 一起来就被拉起(见 MainActivity.onCreate 那段补刀),
     *	那时候用户还没给过定位权限. 于是 startForeground 抛
     *	SecurityException, 整个 App 打开就闪退 —— 一个从没打开过定位的
     *	用户, 连界面都见不到.
     *
     *	所以: **平时只报 dataSync**; 只有真的开了采集、而且真的拿到了
     *	定位权限, 这一次才加上 location.
     */
    private fun goForeground(state: String) {
        val n = aliveNotification(state)
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            startForeground(NOTIF_ALIVE, n)
            return
        }
        var type = ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
        if (prefs(this).getBoolean("collect", false) &&
            checkSelfPermission(Manifest.permission.ACCESS_COARSE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED
        ) {
            type = type or ServiceInfo.FOREGROUND_SERVICE_TYPE_LOCATION
        }
        startForeground(NOTIF_ALIVE, n, type)
    }

    override fun onDestroy() {
        alive = false
        running = false
        // 攒着还没发出去的那几条**先发掉**: 服务被杀是常态, 而丢掉的
        // 那几条不会有任何一处说
        collector?.stop()
        collector = null
        worker?.interrupt()
        super.onDestroy()
    }

    /**
     * 任务卡被划掉 —— **这是最常见的死法, 而且用户不认为自己关掉了它**.
     *
     * 他只是清理了一下后台. 所以在这儿把自己重新拉起来,
     * 并且常驻通知一直在: 真要关的话, 那条通知点进去就能关.
     */
    override fun onTaskRemoved(rootIntent: Intent?) {
        if (running) {
            val i = Intent(this, KeepAliveService::class.java)
            startForegroundService(i)
        }
        super.onTaskRemoved(rootIntent)
    }

    // ── 直播 ────────────────────────────────────────────────

    /** 退避. 连上的那一刻归零 —— 见 [stream] 里拿到 200 那句 */
    @Volatile private var backoff = 2000L

    private fun loop() {
        while (alive) {
            try {
                notifyAlive("在线")
                stream(untilLive = false)
            } catch (e: InterruptedException) {
                return
            } catch (e: Exception) {
                notifyAlive("重连中")
                note = e.message ?: "断了"
            }
            if (!alive) return
            try { Thread.sleep(backoff) } catch (e: InterruptedException) { return }
            // 退避到上限就不再涨: 涨到几十分钟的话, 网一回来它还在睡
            backoff = (backoff * 2).coerceAtMost(60_000L)
        }
    }

    /**
     * 直播那条上一次收到东西 —— elapsedRealtime. OS 每 20 秒发一行心跳注释,
     * 所以它是"这条流此刻活着没有"最直接的证据
     */
    @Volatile private var lastLineAt = 0L

    /**
     * 闹钟叫醒的那一次 —— **报到 + 冲信号 + 读欠的**, 攥着 CPU 做完.
     *
     * ── 原来这一下只读 /stream ──
     *
     *	于是 Doze 里闹钟照常响, 欠的通知也补了, 而**报到和位置一条都没发**:
     *	报到挂在进程里的 Handler 上, 手机睡着它就不走. 2026-09-11 那一夜
     *	00:30 到 08:04 OS 那边一次都没收到这台手机.
     *
     * ── 醒着的时候别白干 ──
     *
     *	闹钟白天也响. 那时候直播那条连着、心跳也在走 —— 再开一条流去
     *	"补课"是多一次连接; 报到那边有一分钟的闸(Collector.MIN_BEAT_GAP_MS).
     */
    private fun catchupAwake() {
        val wl = try {
            (getSystemService(Context.POWER_SERVICE) as PowerManager)
                .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "neox:catchup")
                .apply {
                    setReferenceCounted(false)
                    acquire(CATCHUP_WAKE_MS)
                }
        } catch (e: Exception) {
            null
        }
        try {
            collector?.beatFromAlarm()
            val healthy = alive &&
                SystemClock.elapsedRealtime() - lastLineAt < 45_000L
            if (!healthy) catchup()
        } finally {
            try { if (wl?.isHeld == true) wl.release() } catch (e: Exception) {}
        }
    }

    /** 闹钟叫醒的那一次: 只把欠的读完 */
    private fun catchup() {
        try {
            stream(untilLive = true)
            note = "上次补课 " + java.text.SimpleDateFormat(
                "HH:mm", java.util.Locale.getDefault()).format(java.util.Date())
        } catch (e: Exception) {
            note = "补课失败: " + (e.message ?: "")
        }
    }

    /**
     * 连上去读事件.
     *
     * @param untilLive true = 读到 `: live` 就收工(补课); false = 一直读(直播)
     */
    private fun stream(untilLive: Boolean) {
        val cursors = loadCursors()
        var url = "$base/stream?token=" + URLEncoder.encode(token, "UTF-8")
        if (cursors.isNotEmpty()) {
            url += "&from=" + URLEncoder.encode(
                cursors.entries.joinToString(",") { "${it.key}:${it.value}" }, "UTF-8")
        }
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            setRequestProperty("Accept", "text/event-stream")
            connectTimeout = 15000
            // ── 直播那条也必须设读超时 ──
            //
            // 原来这里是 0(无限等), 理由写着"SSE 本来就是长时间不出声".
            // 那句话对普通 SSE 成立, 对**我们这条**不成立: OS 每 20 秒
            // 发一次心跳注释, 所以它从不长时间不出声.
            //
            // 代价是最恶心的那种坏法: 切基站、WiFi 转 4G、反代那头静悄悄
            // 掐掉之后, socket 半死不活地挂着, readLine() 就在这儿**永远
            // 阻塞**. 服务还在前台跑着、通知渠道也都在, 但一条推送都下不来,
            // 而且下面那个退避重连循环根本跑不到 —— 它被卡在这一行里.
            // 表现出来就是"闹钟响了手机没动静", 手点一下重连又全好了.
            //
            // 连丢三个心跳就当它死了, 抛 SocketTimeoutException 出去重连.
            readTimeout = if (untilLive) 30000 else 70000
        }
        try {
            if (conn.responseCode == 401) {
                note = "token 不对"
                alive = false
                notifyAlive("token 不对")
                return
            }
            if (conn.responseCode != 200) throw Exception("HTTP ${conn.responseCode}")
            // 接上了就归零. **不能等 stream() 正常返回再归零** —— 它只在
            // 断线时返回, 于是挂了一整天再断的那次会拿着上一轮退到 60 秒的
            // 值去睡, 闹钟白白晚一分钟
            if (!untilLive) backoff = 2000L
            BufferedReader(InputStreamReader(conn.inputStream)).use { r ->
                var eventName = ""
                while (alive || untilLive) {
                    val line = r.readLine() ?: break
                    if (!untilLive) lastLineAt = SystemClock.elapsedRealtime()
                    when {
                        // 这一行就是"欠的都给你了"
                        line == ": live" || line == ":live" -> if (untilLive) return
                        line.startsWith(":") -> {}
                        line.isEmpty() -> eventName = ""
                        line.startsWith("event: ") -> eventName = line.substring(7).trim()
                        line.startsWith("data: ") -> {
                            if (eventName == "gap") return  // OS 说断过一段, 重连补齐
                            handle(line.substring(6))
                        }
                    }
                }
            }
        } finally {
            conn.disconnect()
        }
    }

    private fun handle(raw: String) {
        val ev = try { JSONObject(raw) } catch (e: Exception) { return }
        val kind = ev.optString("kind")
        val pid = ev.optString("pid")
        val seq = ev.optInt("seq", -1)
        if (pid.isEmpty() || seq < 0) return

        // 游标先落盘, **哪怕这条不响** —— 不落的话下次补课会把它再读一遍,
        // 而"重复响一次"比"没响"更快让人关掉通知
        saveCursor(pid, seq + 1)

        val p = ev.optJSONObject("payload") ?: return
        /*
            ── 只认一种形状 ──

            上一版认三种(interrupt.verdict / daily.report / wake.fired),
            于是**必然漏**: remind_me 到点走的是 proc.output, 三种里
            一个都不是 —— 闹钟响了、账本里有、桌面上有, 而手机上
            一条通知都没有。真机验过。

            而且那三种里的 wake.fired 那条本身就是死代码: 它读 payload
            里的 "why", 而 wake 的 payload 根本没有这个键(照着
            interrupt.verdict 的字段名抄的, 没核对)。

            现在 OS 侧把所有"该让用户知道的事"汇进一种 delivery,
            手机端只认它 —— 以后 OS 加第五种主动来源, 这边一行不改。
        */
        // ── OS 反过来问我们要东西 ──
        //
        //	采集是按变化触发的: 人没挪 120 米就一条都不报。那对省电是
        //	对的，对"我现在在哪条路"却是致命的 —— 他问的那一刻，OS 手上
        //	最新那条可能是三分钟前的，而市区里三分钟是两个路口。
        //
        //	所以留一条反向的路: 他真问起来时 OS 吆喝一声，我们立刻取一次。
        //	**平时不吆喝** —— 这条路一天走不了几次。
        if (kind == "device.ask") {
            android.util.Log.i("neox", "收到吆喝 what=" + p.optString("what") +
                " collector=" + (collector != null))
            // what 不认得或者没写, 就当是要位置 —— 那是这条路存在的理由.
            // 电量和 WiFi 也能单独要(见 Collector.askNow)
            val what = p.optString("what").ifBlank { "location" }
            collector?.askNow(if (what == "battery" || what == "wifi") what else "location")
            return
        }
        if (kind != "delivery") return

        val text = p.optString("text").trim()
        if (text.isEmpty()) return

        // ── 不是给我的就不响 ──
        //
        //	一台家用的 OS 上不止一个人. 她的"到家提醒"在他手机上响一次,
        //	他就会把整个通知关掉 —— 而那一关, 真正要紧的那次也到不了他.
        //
        //	空的 to = 屋里所有人(闹钟、天气这类). 没登录的时候 me 是空的,
        //	那就只收公共的: 这台手机那时候不代表任何人.
        val to = p.optString("to")
        val me = prefs(this).getString("me", "") ?: ""
        if (to.isNotEmpty() && to != me) return
        notice(
            // from 是哪个 bot 说的。**可以是空** —— 感知层判出来的
            // 那些不属于任何一个 bot, 那时候用产品名顶上
            from = p.optString("from").ifBlank { "NeoxPilot" },
            kind = p.optString("kind"),
            text = text,
            why = p.optString("why"),
            urgent = p.optBoolean("urgent", false),
        )
    }

    // ── 念出来 ──────────────────────────────────────────────
    //
    //	**一条只能看的通知, 在他开车、做饭、手上有东西的时候等于没有.**
    //	而那恰恰是最需要它开口的几个时刻: 该出门了、锅在烧、有人按门铃.
    //
    //	三档, 缺省"只念要紧的":
    //
    //	  off      一句不念
    //	  urgent   只念标着要紧的那些(缺省)
    //	  all      每条主动消息都念
    //
    //	**缺省不是 all**: 一天几条日报和提醒全念出来, 他会把整个通道
    //	关掉 —— 而那一关, 真正要紧的那次也到不了他. 跟打扰预算同一条道理.
    //
    //	**不抢音频焦点的独占权**: 用 TRANSIENT_MAY_DUCK, 他在听歌/导航
    //	的时候把音量压一下念完就还回去. 抢断的话他会关掉这个功能.
    //	(这一段原来只写在注释里, 代码里一行都没有 —— 现在在 Speaker 里)
    private fun speak(text: String, urgent: Boolean) {
        val mode = prefs(this).getString("speak", "urgent") ?: "urgent"
        if (mode == "off") return
        if (mode == "urgent" && !urgent) return
        // **排队念, 不打断**: 主动消息一次可能来两三条, 后一条把前一条
        // 掐了的话他只听得见最后半句. 见 Speaker
        Speaker.say(this, text, queue = true)
    }

    // ── 游标 ────────────────────────────────────────────────
    //
    // 存在磁盘上, 因为**进程会死**: 被系统杀、手机重启、用户划掉.
    // 存在内存里的话, 每次起来都从头补一遍, 于是一屏昨天的通知

    private fun loadCursors(): Map<String, Int> {
        val raw = prefs(this).getString("cursors", "") ?: ""
        if (raw.isBlank()) return emptyMap()
        return raw.split(",").mapNotNull {
            val i = it.lastIndexOf(':')
            if (i <= 0) null else it.substring(0, i) to (it.substring(i + 1).toIntOrNull() ?: return@mapNotNull null)
        }.toMap()
    }

    private fun saveCursor(pid: String, next: Int) {
        val m = loadCursors().toMutableMap()
        if ((m[pid] ?: 0) >= next) return
        m[pid] = next
        prefs(this).edit()
            .putString("cursors", m.entries.joinToString(",") { "${it.key}:${it.value}" })
            .apply()
    }

    // ── 闹钟 ────────────────────────────────────────────────

    private fun scheduleCatchup() {
        val am = getSystemService(Context.ALARM_SERVICE) as AlarmManager
        val pi = PendingIntent.getService(
            this, 42,
            Intent(this, KeepAliveService::class.java).setAction(ACTION_CATCHUP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        // setAndAllowWhileIdle 而不是 setRepeating: 后者在 Doze 里
        // 会被攒到维护窗口一起放 —— 那正好是"半夜它一声不吭"的来源.
        // 代价是每次响完要自己再排一次
        //
        // 节奏看在不在电池白名单里 —— 见 catchupEvery
        am.setAndAllowWhileIdle(
            AlarmManager.ELAPSED_REALTIME_WAKEUP,
            SystemClock.elapsedRealtime() + catchupEvery(this),
            pi
        )
    }

    // ── 通知 ────────────────────────────────────────────────

    private fun channels() {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(
            CH_ALIVE, "常驻", NotificationManager.IMPORTANCE_MIN
        ).apply { description = "它还在替你听着" })
        // 主动消息用高优先级: 它已经过了 OS 那边的打扰预算,
        // **能走到这儿的每一条都是被判定值得打扰的** ——
        // 在手机上再压一次等于把那套判断作废
        // 主动 —— 它已经过了 OS 那边的打扰预算, **能走到这儿的每一条
        // 都是被判定值得打扰的**。在手机上再压一次等于把那套判断作废
        nm.createNotificationChannel(NotificationChannel(
            CH_NOTICE, "主动", NotificationManager.IMPORTANCE_HIGH
        ).apply { description = "它自己判断出该告诉你的事" })
        // 提醒 —— 你自己设的。**不占打扰额度, 也一定要响**:
        // 静默地不响是闹钟最糟的失败方式
        nm.createNotificationChannel(NotificationChannel(
            CH_REMIND, "提醒", NotificationManager.IMPORTANCE_HIGH
        ).apply {
            description = "你自己设的闹钟"
            enableVibration(true)
        })
        // 日报 —— 一天一次、时间固定, 所以它是可预期的。
        // 可预期的打扰不消耗信任, 也就不该出声
        nm.createNotificationChannel(NotificationChannel(
            CH_DAILY, "日报", NotificationManager.IMPORTANCE_LOW
        ).apply { description = "每天攒下的那些" })
    }

    private fun openApp(): PendingIntent = PendingIntent.getActivity(
        this, 0,
        Intent(this, MainActivity::class.java)
            .setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
        PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
    )

    private fun aliveNotification(state: String): Notification =
        Notification.Builder(this, CH_ALIVE)
            .setContentTitle("NeoxPilot · $state")
            .setContentText(if (note.isBlank()) base else note)
            .setSmallIcon(android.R.drawable.ic_menu_compass)
            .setOngoing(true)
            .setContentIntent(openApp())
            .build()

    private fun notifyAlive(state: String) {
        getSystemService(NotificationManager::class.java)
            .notify(NOTIF_ALIVE, aliveNotification(state))
    }

    /*
        推一条.

        ── 三个渠道, 分开设 ──

            提醒  你自己设的闹钟      高优先级 + 出声, 不占打扰额度
            主动  它判断出要说的      高优先级(已经过了预算那道闸)
            日报  每天一次           低优先级, 不出声

        **闹钟和主动必须分开**: 闹钟是你要的, 主动是它想说的 ——
        你可能想关掉后者而留着前者, 而 Android 的渠道正是给这件事用的
        (用户在系统设置里能单独关掉某个渠道, App 拦不住也不该拦)。
    */
    private fun notice(from: String, kind: String, text: String, why: String, urgent: Boolean) {
        val ch = when (kind) {
            "remind" -> CH_REMIND
            "daily" -> CH_DAILY
            else -> CH_NOTICE
        }
        val label = when (kind) {
            "remind" -> "提醒"
            "daily" -> "日报"
            "needs_you" -> "等你拍板"
            else -> "主动"
        }
        val b = Notification.Builder(this, ch)
            // **标题是"谁 · 哪一类"**: 上一版只写了类别, 于是三个 bot
            // 各提醒一条的话, 通知栏里是三条一模一样的"提醒"
            .setContentTitle("$from · $label")
            .setContentText(text)
            // 长文本要能展开: 一条被截断成一行的提醒, 用户还得点进来读,
            // 而他多半在走路
            .setStyle(Notification.BigTextStyle().bigText(
                if (why.isBlank()) text else "$text\n\n$why"))
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setAutoCancel(true)
            .setContentIntent(openApp())
        if (urgent) b.setPriority(Notification.PRIORITY_HIGH)
        speak(text, urgent)
        getSystemService(NotificationManager::class.java)
            .notify(System.currentTimeMillis().toInt(), b.build())
    }
}
