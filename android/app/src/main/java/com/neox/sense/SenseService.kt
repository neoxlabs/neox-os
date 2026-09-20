package com.neox.sense

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.BatteryManager
import android.os.Build
import android.os.IBinder
import android.telephony.PhoneStateListener
import android.telephony.TelephonyManager
import kotlin.concurrent.thread

/**
 * 采集前台服务.
 *
 * ── 为什么必须是前台服务 ──
 *
 * 安卓从 8.0 起对后台限制极严: 普通后台服务几分钟就被杀, 位置更新会被
 * 降频到几分钟一次甚至停掉. 而一个"偶尔醒一下的采集端"表现出来的样子是
 * **"它最近好像什么都不知道了"** —— 没有报错, 只是安静了.
 *
 * 前台服务 + 常驻通知是安卓明确支持的路径: 用户看得见它在跑,
 * 这也是应该的 —— 一个持续读你位置的东西, 本来就不该藏起来.
 */
class SenseService : Service() {

    private lateinit var filter: PhoneFilter
    private lateinit var uploader: Uploader
    private val backlog = Backlog()
    private var source = "phone.mk"

    // UPLOAD_EVERY_SEC 上传循环的周期. **它就是报到的节奏** ——
    // OS 判失联看的是"多久没消息", 而消息是从上传这条路出去的.
    // 手机跟家居完全不是一个量级: 手机十分钟传一次是正常的,
    // 家居桥接十分钟不吭声就是坏了 —— 所以节奏必须自己报
    private val UPLOAD_EVERY_SEC = 10

    // UPLOAD_EVERY_SEC 上传循环的周期. **它就是报到的节奏** ——
    // OS 判失联看的是"多久没消息", 而消息是从上传这条路出去的
    @Volatile private var running = false

    private val batteryReceiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context?, i: Intent?) {
            i ?: return
            val level = i.getIntExtra(BatteryManager.EXTRA_LEVEL, -1)
            val scale = i.getIntExtra(BatteryManager.EXTRA_SCALE, 100)
            if (level < 0) return
            val pct = level * 100 / scale
            val status = i.getIntExtra(BatteryManager.EXTRA_STATUS, -1)
            val charging = status == BatteryManager.BATTERY_STATUS_CHARGING ||
                status == BatteryManager.BATTERY_STATUS_FULL
            enqueue(filter.battery(pct, charging).map { it.copy(at = System.currentTimeMillis()) })
        }
    }

    private val locListener = LocationListener { loc: Location ->
        enqueue(filter.location(Fix(loc.time, loc.latitude, loc.longitude, loc.accuracy.toDouble())))
    }

    @Suppress("DEPRECATION")
    private val callListener = object : PhoneStateListener() {
        override fun onCallStateChanged(state: Int, number: String?) {
            // **来电一条不许省** —— 降采样只针对连续量.
            // 而且它是穿透表里的 kind: OS 侧会立刻叫醒用户
            val kind = when (state) {
                TelephonyManager.CALL_STATE_RINGING -> "call.incoming"
                else -> return
            }
            enqueue(filter.event(kind, System.currentTimeMillis(),
                mapOf("from" to (number ?: "未知"))))
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (running) return START_STICKY
        running = true

        val prefs = getSharedPreferences("neox_sense", Context.MODE_PRIVATE)
        val url = intent?.getStringExtra("url") ?: prefs.getString("url", "")!!
        val token = intent?.getStringExtra("token") ?: prefs.getString("token", "")!!
        prefs.edit().putString("url", url).putString("token", token).apply()

        // 停留门槛可配: 开车、步行、在家不动, 合适的阈值不是一个数.
        // (也让真机验证不必等 5 分钟真实时间才看到一次"到达")
        val dwell = intent?.getLongExtra("dwellMs", 0L)?.takeIf { it > 0 }
            ?: prefs.getLong("dwellMs", 5 * 60 * 1000)
        prefs.edit().putLong("dwellMs", dwell).apply()
        source = prefs.getString("source", "phone.mk")!!
        filter = PhoneFilter(source,
            PhoneConfig(stayDwell = dwell))
        uploader = Uploader(url, token)

        startForeground(1, notification(url))
        registerReceiver(batteryReceiver, IntentFilter(Intent.ACTION_BATTERY_CHANGED))

        try {
            val lm = getSystemService(Context.LOCATION_SERVICE) as LocationManager
            // 2 秒 / 0 米: 采样密不要紧, **降采样在本地做**,
            // 上传的量跟采样频率无关
            lm.requestLocationUpdates(LocationManager.GPS_PROVIDER, 2000L, 0f, locListener)
            lm.requestLocationUpdates(LocationManager.NETWORK_PROVIDER, 2000L, 0f, locListener)
        } catch (e: SecurityException) {
            log("没有定位权限: ${e.message}")
        }
        try {
            val tm = getSystemService(Context.TELEPHONY_SERVICE) as TelephonyManager
            @Suppress("DEPRECATION")
            tm.listen(callListener, PhoneStateListener.LISTEN_CALL_STATE)
        } catch (e: SecurityException) {
            log("没有电话状态权限: ${e.message}")
        }

        // 上传循环. **跟采集解耦**: 采集可以很密, 上传按批走,
        // 而且网断的时候采集不能跟着停
        thread(isDaemon = true) {
            while (running) {
                flush()
                Thread.sleep(UPLOAD_EVERY_SEC * 1000L)
            }
        }
        return START_STICKY
    }

    private fun enqueue(sigs: List<Signal>) {
        if (sigs.isEmpty()) return
        val dropped = backlog.add(sigs)
        if (dropped > 0) log("缓冲满, 丢了 $dropped 条最老的(累计 ${backlog.totalDropped})")
        for (s in sigs) log("产出 ${s.kind} @${s.at}")
    }

    private fun flush() {
        // **先报到, 再干活.**
        //
        // 顺序不能反: 放在后面的话, 没东西可投时(最常见的情况 ——
        // 一天里绝大多数时候人没动)这一句永远走不到, 而 OS 会以为
        // 手机已经死了. 而"没东西可投"跟"手机没了"是完全不同的两件事.
        //
        // 节奏报的是上传循环的周期(10 秒), 不是采集周期 ——
        // OS 判失联看的就是"多久没消息", 而消息是从这儿出去的.
        uploader.beat(source, UPLOAD_EVERY_SEC)

        val pending = backlog.take()
        if (pending.isEmpty()) return
        try {
            val counts = uploader.post(pending)
            log("投了 ${pending.size} 条: $counts")
        } catch (e: Exception) {
            // 投不出去就放回去 —— 降采样之后剩下的每一条都是一段停留或一次来电,
            // 丢了就永久没有了
            backlog.putBack(pending)
            log("投递失败(缓冲 ${backlog.size} 条): ${e.message}")
        }
    }

    override fun onDestroy() {
        running = false
        try { unregisterReceiver(batteryReceiver) } catch (_: Exception) {}
        super.onDestroy()
    }

    private fun notification(url: String): Notification {
        val ch = "neox_sense"
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val nm = getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(
                NotificationChannel(ch, "Neox 感知", NotificationManager.IMPORTANCE_LOW))
        }
        return Notification.Builder(this, ch)
            .setContentTitle("Neox 感知采集中")
            .setContentText(url)
            .setSmallIcon(android.R.drawable.ic_menu_mylocation)
            .build()
    }

    private fun log(msg: String) = android.util.Log.i("NeoxSense", msg)
}
