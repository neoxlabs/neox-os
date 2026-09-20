package com.neox.neox

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * 开机自启.
 *
 * **手机半夜会重启** —— 系统更新、没电关机之后充上电、或者就是崩了.
 * 不接这一条的话, "早上七点喊我"会在任何一次重启之后**永久失效**,
 * 而用户第二天早上才会发现, 并且不知道为什么.
 *
 * 只有用户真的开过这个开关才自启(prefs 里有地址) —— 装了没用过的 App
 * 在开机时偷偷起一个常驻服务, 是另一种该被骂的行为.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        // MY_PACKAGE_REPLACED: **装一版新包也算一次重启** —— 旧进程连同
        // 服务一起没了, 而用户什么都没关. 不接这一条的话, 每发一版
        // 常驻就静默地停一次, 直到他下次打开 App 才发现
        if (intent.action != Intent.ACTION_BOOT_COMPLETED &&
            intent.action != Intent.ACTION_MY_PACKAGE_REPLACED &&
            intent.action != "android.intent.action.QUICKBOOT_POWERON") return
        val p = KeepAliveService.prefs(context)
        if (p.getString("base", "").isNullOrBlank()) return
        if (!p.getBoolean("wanted", false)) return
        context.startForegroundService(Intent(context, KeepAliveService::class.java))
    }
}
