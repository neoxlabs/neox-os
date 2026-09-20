package com.neox.neox

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {

    /**
     * 开着的常驻服务, 每次 App 起来都补一刀.
     *
     * ── 为什么必须在这儿补 ──
     *
     * 服务活在一个**随时会被端掉的进程**里: 装一次新包、用户在最近任务
     * 里划掉、系统内存紧了、厂商省电策略半夜清一遍 —— 每一种都会让它
     * 没了. 而原来唯一会把它拉起来的只有 BOOT_COMPLETED, 于是重装之后
     * 开关"自己变回了关", 用户以为是这个开关没存住.
     *
     * 存住了 —— wanted 一直是 true. 没存住的是**服务本身**.
     * 所以看的是 wanted(用户的意思), 而不是 running(此刻活着没有):
     * 服务被杀过一次不代表他不想要.
     */
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val p = KeepAliveService.prefs(this)
        if (p.getBoolean("wanted", false) &&
            !p.getString("base", "").isNullOrBlank() &&
            !KeepAliveService.running) {
            startForegroundService(Intent(this, KeepAliveService::class.java))
        }
    }

    override fun configureFlutterEngine(engine: FlutterEngine) {
        super.configureFlutterEngine(engine)
        val ch = MethodChannel(engine.dartExecutor.binaryMessenger, "neox/keepalive")
        // ── 反过来的那一条: 它开始念了 / 念完了 ──
        //
        //	语音那一页原来每 400ms 问一次"还在念吗" —— 那对画一颗球够了,
        //	对"念的时候别听"不够: 400ms 的空档里, 它自己念的头几个字已经
        //	进了麦克风. 所以念的状态一变就推过去(Speaker 那边已经挪到主线程)
        Speaker.listener = { on -> ch.invokeMethod("tts", mapOf("speaking" to on)) }
        ch.setMethodCallHandler { call, result ->
                when (call.method) {
                    "start" -> {
                        val base = call.argument<String>("base") ?: ""
                        val token = call.argument<String>("token") ?: ""
                        // wanted 记的是**用户的意思**, 不是服务此刻活着没有.
                        // 开机自启要看的是前者: 服务被系统杀过一次
                        // 不代表他不想要
                        KeepAliveService.prefs(this).edit()
                            .putBoolean("wanted", true).apply()
                        startForegroundService(
                            Intent(this, KeepAliveService::class.java)
                                .putExtra("base", base)
                                .putExtra("token", token))
                        result.success(true)
                    }
                    // 我是谁 —— 保活服务据此决定一条通知响不响.
                    //
                    //	**必须存到 prefs 而不是只放内存**: 判"这条是不是给我的"
                    //	发生在保活服务里, 而那个进程可能是被闹钟叫醒的 ——
                    //	那时候 Dart 那一侧根本没起来.
                    "whoAmI" -> {
                        KeepAliveService.prefs(this).edit()
                            .putString("me", call.argument<String>("id") ?: "")
                            .apply()
                        result.success(true)
                    }
                    // 采集开关 + 要权限.
                    //
                    //	**权限要在 Activity 里要**, 服务里要不了 —— 而这也是
                    //	对的: 一个后台服务突然弹一个定位权限框, 用户不知道
                    //	是谁在要, 十有八九点拒绝.
                    "setCollect" -> {
                        val on = call.argument<Boolean>("on") ?: false
                        KeepAliveService.prefs(this).edit()
                            .putString("deviceId", call.argument<String>("deviceId") ?: "")
                            .putBoolean("collect", on).apply()
                        if (on) ensureLocationPermission()
                        // 服务重起一下, 让它按新开关重新决定采不采
                        if (KeepAliveService.running) {
                            startForegroundService(Intent(this, KeepAliveService::class.java))
                        }
                        result.success(hasLocation())
                    }
                    // 念不念出来 —— off / urgent / all.
                    //
                    //	**跟 me 一样存 prefs**: 念这件事发生在保活服务里,
                    //	而那个进程可能是被闹钟叫醒的 —— Dart 那侧没起来
                    "speakMode" -> {
                        val mode = call.argument<String>("mode") ?: "urgent"
                        KeepAliveService.prefs(this).edit()
                            .putString("speak", mode).apply()
                        result.success(mode)
                    }
                    // ── 读通知 ──
                    //
                    //	**两个开关是两件事**: 系统那个是"能不能读"(只有
                    //	用户在系统设置里亲手点得动), 我们这个是"要不要用".
                    //	系统给了权限不等于他同意了 —— 所以这儿存我们自己
                    //	那一位, 而 NoticeReader 两个都看.
                    "readNotices" -> {
                        val on = call.argument<Boolean>("on") ?: false
                        KeepAliveService.prefs(this).edit()
                            .putBoolean("readNotices", on).apply()
                        if (on && !noticeAccess()) openNoticeSettings()
                        result.success(noticeAccess())
                    }
                    "hasNoticeAccess" -> result.success(noticeAccess())
                    // 读日历 —— 跟位置同一个形状: 开关归我们, 权限归系统
                    "readCalendar" -> {
                        val on = call.argument<Boolean>("on") ?: false
                        KeepAliveService.prefs(this).edit()
                            .putBoolean("readCalendar", on).apply()
                        if (on && !Agenda.can(this)) {
                            requestPermissions(
                                arrayOf(android.Manifest.permission.READ_CALENDAR), 77
                            )
                        }
                        result.success(Agenda.can(this))
                    }
                    "hasCalendar" -> result.success(Agenda.can(this))
                    // ── 念一句 ──
                    //
                    //	对话里的回话走这条: **打断上一句**(他又说了一句,
                    //	上一句的回话就作废了), 跟主动消息那条排队念不一样
                    "say" -> {
                        Speaker.say(this, call.argument<String>("text") ?: "",
                            queue = false)
                        result.success(true)
                    }
                    "shutUp" -> {
                        Speaker.stop()
                        result.success(true)
                    }
                    "speaking" -> result.success(Speaker.speaking)
                    "hasLocation" -> result.success(hasLocation())
                    // ── 电池白名单 ──
                    //
                    //	**不在白名单里, 手机一睡着这个 App 就聋了**: 心跳停、
                    //	网被掐, 只剩 15 分钟一次的闹钟那十来秒. 2026-09-11 那一夜
                    //	00:30 到 08:04 一次报到都没有.
                    //
                    //	这个口子只能**请他点**, 替不了他 —— 系统弹一个框,
                    //	他点"允许"才算. 返回的是此刻在不在白名单里
                    "batteryExempt" -> result.success(KeepAliveService.batteryExempt(this))
                    "askBatteryExempt" -> {
                        askBatteryExempt()
                        result.success(KeepAliveService.batteryExempt(this))
                    }
                    // 厂商自己那一页 —— 见 openVendorBackground
                    "openAutoStart" -> result.success(openVendorBackground())
                    "askNotify" -> {
                        result.success(ensureNotifyPermission())
                    }
                    "stop" -> {
                        KeepAliveService.prefs(this).edit()
                            .putBoolean("wanted", false).apply()
                        stopService(Intent(this, KeepAliveService::class.java))
                        KeepAliveService.running = false
                        result.success(true)
                    }
                    // **running 和 wanted 是两件事**, 都报上去:
                    // 开关画的是 wanted(他要不要), 底下那句说的是 running
                    // (它此刻活着没有). 只报 running 的话, 服务刚被杀掉
                    // 的那一下开关会自己弹回"关" —— 而他从没关过
                    "status" -> result.success(mapOf(
                        "running" to KeepAliveService.running,
                        "wanted" to KeepAliveService.prefs(this)
                            .getBoolean("wanted", false),
                        "note" to noteFor(),
                    ))
                    else -> result.notImplemented()
                }
            }
    }

    /**
     * 通知权限 —— 安卓 13 起要在运行时要.
     *
     * **要不到的话整套主动智能就断在最后一米**: 服务照常跑、事件照常收、
     * 判断照常做, 而那句话谁也看不见 —— 而且这种坏法是静默的,
     * 用户只会觉得"它从来不提醒我".
     *
     * 所以开关打开的时候就问, 而不是等第一条通知发不出去.
     */
    private fun ensureNotifyPermission(): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return true
        val ok = checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        if (!ok) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 7717)
        }
        return ok
    }

    /**
     * 请他把这个 App 加进电池白名单.
     *
     *	直达那个框(ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)是最好的:
     *	一个"允许"就完事. 有的 ROM 把它阉了 —— 那就退到白名单列表那一页,
     *	让他自己找; 再不行就是这个 App 的详情页.
     *
     *	**已经在白名单里就不弹**: 弹一个"已经允许了"的框是在打扰他
     */
    private fun askBatteryExempt() {
        if (KeepAliveService.batteryExempt(this)) return
        val tries = listOf(
            Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
                .setData(Uri.parse("package:$packageName")),
            Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS),
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                .setData(Uri.parse("package:$packageName")),
        )
        for (i in tries) {
            try {
                startActivity(i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                return
            } catch (e: Exception) {
                // 这台没有这一页 —— 试下一个
            }
        }
    }

    /**
     * 厂商的"自启动 / 后台运行"那一页.
     *
     * ── 为什么光有电池白名单不够 ──
     *
     *	小米、华为、OPPO、vivo 各自在系统的 Doze 之上又加了一层: 不在它们
     *	那张"允许自启动/后台运行"的表里, 锁屏几分钟之后进程直接被杀, 连
     *	闹钟都叫不起来. 这一层**没有公开接口能查**, 只能把他送到那一页.
     *
     *	各家的页面名是逆向出来的, 升一次系统就可能变 —— 一个个试, 都不行
     *	就退到这个 App 的详情页(那里至少有"耗电"那一项). 返回有没有打开什么
     */
    private fun openVendorBackground(): Boolean {
        val vendor = listOf(
            // 小米 / 红米
            "com.miui.securitycenter" to "com.miui.permcenter.autostart.AutoStartManagementActivity",
            // 华为 / 荣耀
            "com.huawei.systemmanager" to "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity",
            "com.hihonor.systemmanager" to "com.hihonor.systemmanager.startupmgr.ui.StartupNormalAppListActivity",
            // OPPO / 一加 / realme
            "com.coloros.safecenter" to "com.coloros.safecenter.permission.startup.StartupAppListActivity",
            "com.oplus.battery" to "com.oplus.powermanager.fuelgaue.PowerUsageModelActivity",
            // vivo / iQOO
            "com.vivo.permissionmanager" to "com.vivo.permissionmanager.activity.BgStartUpManagerActivity",
            "com.iqoo.secure" to "com.iqoo.secure.ui.phoneoptimize.BgStartUpManager",
            // 魅族
            "com.meizu.safe" to "com.meizu.safe.permission.SmartBGActivity",
        )
        for ((pkg, cls) in vendor) {
            try {
                startActivity(
                    Intent().setClassName(pkg, cls).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                )
                return true
            } catch (e: Exception) {
                // 不是这家, 或者这一版改了名 —— 下一个
            }
        }
        return try {
            startActivity(
                Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                    .setData(Uri.parse("package:$packageName"))
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
            true
        } catch (e: Exception) {
            false
        }
    }

    override fun onDestroy() {
        // Activity 没了就别往一个死掉的引擎上推 —— 服务里念的那几句照念
        Speaker.listener = null
        super.onDestroy()
    }

    private fun hasLocation(): Boolean =
        checkSelfPermission(Manifest.permission.ACCESS_COARSE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED

    /**
     * 定位权限.
     *
     * ── 分两步要, 不能一次全要 ──
     *
     *	安卓 10 起, "后台定位"必须在**已经拿到前台定位之后**单独要 ——
     *	一次全要的话系统会直接拒掉后台那一条, 而且不给任何提示.
     *	表现出来就是: 用户明明点了同意, 息屏之后位置还是不上报.
     *
     *	所以: 先要前台的; 拿到了, 下一次(用户再点一下开关)再要后台的.
     */
    private fun ensureLocationPermission() {
        if (!hasLocation()) {
            requestPermissions(
                arrayOf(
                    Manifest.permission.ACCESS_COARSE_LOCATION,
                    Manifest.permission.ACCESS_FINE_LOCATION,
                ), 7718
            )
            return
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q &&
            checkSelfPermission(Manifest.permission.ACCESS_BACKGROUND_LOCATION) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.ACCESS_BACKGROUND_LOCATION), 7719)
            return
        }
        askBluetooth()
    }

    /**
     * 蓝牙权限 —— 安卓 12 起"车机蓝牙连上了"要它才收得到(见 BtAudio).
     *
     *	**排在定位后面, 不跟定位挤一个框**: 系统同一时刻只弹一个权限框,
     *	第二个请求会被静默丢掉. 所以定位那个框关掉之后(onRequestPermissionsResult)
     *	再接着要这个. 他拒了就拒了 —— 采集照常, 只是认不出"上车了"
     */
    private fun askBluetooth() {
        if (BtAudio.can(this)) return
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return
        requestPermissions(arrayOf(Manifest.permission.BLUETOOTH_CONNECT), 7720)
    }

    override fun onRequestPermissionsResult(
        requestCode: Int, permissions: Array<out String>, grantResults: IntArray
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == 7718 || requestCode == 7719) askBluetooth()
        // 拿到了新权限(位置 / 蓝牙) —— 服务重起一下, 让采集按新权限重新挂监听
        if (requestCode in 7718..7720 && KeepAliveService.running &&
            grantResults.any { it == PackageManager.PERMISSION_GRANTED }
        ) {
            startForegroundService(Intent(this, KeepAliveService::class.java))
        }
    }

    /**
     * 给人看的那一句.
     *
     * **不要只报 running** —— 一个"开着"但因为省电策略永远连不上的服务,
     * 跟一个真的在工作的服务, 在开关上长得一模一样.
     */
    private fun noteFor(): String {
        if (!KeepAliveService.running) return ""
        val n = KeepAliveService.note
        val base = if (n.isBlank()) "在线" else n
        // ── 两层省电, 一层查得到, 一层查不到 ──
        //
        //	系统的电池白名单查得到, 所以设置页上有一个真的开关(见
        //	batteryExempt / askBatteryExempt), 这里只在没开的时候提一句.
        //
        //	厂商那层("自启动 / 后台运行")没有接口能查 —— 那是这类服务在
        //	国产 ROM 上最常见的死因, 只能说出来, 让他自己去开
        return if (KeepAliveService.batteryExempt(this)) {
            "$base · 如果它还是老掉线, 去系统设置里给它开「自启动」和「允许后台运行」"
        } else {
            "$base · 还没加进电池白名单, 手机睡着之后它会掉线"
        }
    }
    /**
     * 系统那一页给权限了没有.
     *
     *	**只能读, 不能替他点**: 这是安卓刻意的 —— 一个能自己打开
     *	"读所有通知"的 app, 用户就没有任何防线了.
     */
    private fun noticeAccess(): Boolean {
        val flat = android.provider.Settings.Secure.getString(
            contentResolver, "enabled_notification_listeners"
        ) ?: return false
        return flat.contains(packageName)
    }

    /** 把他送到系统那一页 —— 光说"去设置里开"他找不到 */
    private fun openNoticeSettings() {
        try {
            startActivity(
                Intent("android.settings.ACTION_NOTIFICATION_LISTENER_SETTINGS")
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
        } catch (e: Exception) {
            // 有的 ROM 没有这一页 —— 那就只能让他自己找, 界面上说清楚
        }
    }

}
