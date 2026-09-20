package com.neox.neox

import android.Manifest
import android.bluetooth.BluetoothA2dp
import android.bluetooth.BluetoothClass
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothHeadset
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.Build

/**
 * 蓝牙音频连上/断开 —— **"他上车了"最便宜也最准的一条信号**.
 *
 * ── 为什么是它 ──
 *
 *	车机蓝牙一连上, 就几乎可以断定人坐进了车里: 比速度准(堵车时速度是 0),
 *	比位置快(位置要等挪够几百米才看得出来), 而且**一分电都不花** ——
 *	这条广播本来就在发, 我们只是顺路听一耳朵.
 *
 *	2026-09-11 那次事故里, 他开着车跟它说话, 而 OS 那边以为他还坐在
 *	家里: 位置是 4 分钟 / 120 米一条的粗定位. 这条信号到了, 采集那边
 *	立刻切到"在动"那一档(见 Collector.enterMoving).
 *
 * ── 一台车 = 两个 profile, 只算一次 ──
 *
 *	车机一般同时连 A2DP(放音乐/导航)和 HEADSET(打电话), 两条广播
 *	各来一次. 照着报的话账本里是两条"连着蓝牙「我的车」" —— 所以按
 *	**设备地址**记着连着哪几个 profile: 第一个连上才算"连上",
 *	最后一个断开才算"断开".
 *
 * ── 权限 ──
 *
 *	安卓 12 起, 这两条广播和设备名都要 BLUETOOTH_CONNECT. 没给就
 *	**安静地什么都不报** —— 它是锦上添花, 不该因为它让采集看起来是坏的.
 */
class BtAudio(
    private val ctx: Context,
    /** 状态真的变了才叫 —— (名字, 连上没有, 是不是车) */
    private val onChange: (name: String, connected: Boolean, car: Boolean) -> Unit,
    /** 起来的时候车已经连着了 —— 只改档位, 不报信号(见 start) */
    private val onAlreadyCar: () -> Unit,
) {
    companion object {
        /**
         * 名字里带这些的算车.
         *
         *	**类别码是第一判据**(AUDIO_VIDEO_CAR_AUDIO / HANDSFREE), 名字是
         *	兜底: 不少车机把自己报成普通音箱, 而名字里老老实实写着"BYD".
         *
         *	拉丁字母那几个按**整词**匹配 —— 光查子串的话 "Oscar 的耳机"
         *	也成了车.
         */
        private val CAR_WORDS = listOf(
            "车", "汽车", "车载", "车机",
            "比亚迪", "吉利", "理想", "蔚来", "小鹏", "问界", "极氪", "领克",
            "哈弗", "长安", "奇瑞", "红旗", "五菱", "宝马", "奔驰", "奥迪",
            "大众", "丰田", "本田", "日产", "别克", "特斯拉",
        )
        private val CAR_LATIN = Regex(
            "\\b(car|carplay|carlife|carkit|byd|tesla|bmw|audi|benz|mercedes|" +
                "toyota|honda|nissan|vw|volkswagen|buick|geely|lynk|nio|xpeng|" +
                "zeekr|aito|mazda|ford|lexus|volvo|porsche|hyundai|kia|jeep|" +
                "chevrolet|cadillac|skoda|subaru|mitsubishi|haval|chery|wuling|" +
                "uconnect|mylink|entune|idrive|mbux|sync)\\b",
            RegexOption.IGNORE_CASE
        )

        /** 名字像不像车 —— 单独拿出来是为了好测, 也给 Collector 用 */
        fun looksLikeCar(name: String): Boolean {
            if (name.isBlank()) return false
            if (CAR_WORDS.any { name.contains(it) }) return true
            return CAR_LATIN.containsMatchIn(name)
        }

        fun can(ctx: Context): Boolean =
            Build.VERSION.SDK_INT < Build.VERSION_CODES.S ||
                ctx.checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) ==
                PackageManager.PERMISSION_GRANTED
    }

    /** 地址 → 连着的那几个 profile. 空了 = 这台设备断开了 */
    private val linked = mutableMapOf<String, MutableSet<Int>>()

    /** 地址 → 是不是车. 断开的时候设备名/类别可能已经读不到了, 记着 */
    private val isCar = mutableMapOf<String, Boolean>()
    private val names = mutableMapOf<String, String>()

    /** 这会儿有没有车连着 */
    val carLinked: Boolean
        @Synchronized get() = linked.any { (addr, set) -> set.isNotEmpty() && isCar[addr] == true }

    private val rx = object : BroadcastReceiver() {
        override fun onReceive(c: Context?, i: Intent?) {
            if (i == null) return
            val profile = when (i.action) {
                BluetoothA2dp.ACTION_CONNECTION_STATE_CHANGED -> BluetoothProfile.A2DP
                BluetoothHeadset.ACTION_CONNECTION_STATE_CHANGED -> BluetoothProfile.HEADSET
                else -> return
            }
            val state = i.getIntExtra(BluetoothProfile.EXTRA_STATE, -1)
            // 只认两个终态. CONNECTING / DISCONNECTING 是过程, 报了就是噪音
            if (state != BluetoothProfile.STATE_CONNECTED &&
                state != BluetoothProfile.STATE_DISCONNECTED) return
            val dev: BluetoothDevice = (if (Build.VERSION.SDK_INT >= 33) {
                i.getParcelableExtra(BluetoothDevice.EXTRA_DEVICE, BluetoothDevice::class.java)
            } else {
                @Suppress("DEPRECATION")
                i.getParcelableExtra(BluetoothDevice.EXTRA_DEVICE)
            }) ?: return
            seen(dev, profile, state == BluetoothProfile.STATE_CONNECTED, report = true)
        }
    }

    fun start() {
        val f = IntentFilter().apply {
            addAction(BluetoothA2dp.ACTION_CONNECTION_STATE_CHANGED)
            addAction(BluetoothHeadset.ACTION_CONNECTION_STATE_CHANGED)
        }
        try { ctx.registerReceiver(rx, f) } catch (e: Exception) { return }
        // ── 起来的时候车可能已经连着了 ──
        //
        //	服务是会被杀了再拉起来的, 半路上被杀一次, 那条"连上"的广播
        //	就永远错过了 —— 于是一路都按"没动"那档采.
        //
        //	所以问一遍现在连着谁. **只改档位, 不报信号**: 每次进程重起都
        //	往账本里塞一条"连着车载蓝牙", 而那件事其实早就发生了
        if (!can(ctx)) return
        val adapter = try {
            (ctx.getSystemService(Context.BLUETOOTH_SERVICE) as BluetoothManager).adapter
        } catch (e: Exception) { null } ?: return
        for (p in listOf(BluetoothProfile.A2DP, BluetoothProfile.HEADSET)) {
            try {
                adapter.getProfileProxy(ctx, object : BluetoothProfile.ServiceListener {
                    override fun onServiceConnected(profile: Int, proxy: BluetoothProfile) {
                        try {
                            proxy.connectedDevices.forEach { seen(it, profile, true, report = false) }
                            if (carLinked) onAlreadyCar()
                        } catch (e: SecurityException) {
                            // 权限半路被撤了 —— 算了
                        } finally {
                            try { adapter.closeProfileProxy(profile, proxy) } catch (e: Exception) {}
                        }
                    }

                    override fun onServiceDisconnected(profile: Int) {}
                }, p)
            } catch (e: Exception) {
                // 没蓝牙 / 没权限 —— 不报错
            }
        }
    }

    fun stop() {
        try { ctx.unregisterReceiver(rx) } catch (e: Exception) { /* 本来就没挂上 */ }
    }

    private fun seen(dev: BluetoothDevice, profile: Int, on: Boolean, report: Boolean) {
        val addr = try { dev.address } catch (e: Exception) { null } ?: return
        var fire: Triple<String, Boolean, Boolean>? = null
        synchronized(this) {
            if (on) {
                // 连上那一下才读得到名字和类别 —— 记下来, 断开时要用
                val name = try { dev.name } catch (e: SecurityException) { null } ?: ""
                if (name.isNotBlank()) names[addr] = name
                val cls = try { dev.bluetoothClass?.deviceClass } catch (e: SecurityException) { null }
                isCar[addr] = cls == BluetoothClass.Device.AUDIO_VIDEO_CAR_AUDIO ||
                    cls == BluetoothClass.Device.AUDIO_VIDEO_HANDSFREE ||
                    looksLikeCar(name)
            }
            val set = linked.getOrPut(addr) { mutableSetOf() }
            val was = set.isNotEmpty()
            if (on) set.add(profile) else set.remove(profile)
            val now = set.isNotEmpty()
            if (was != now) {
                fire = Triple(names[addr] ?: "", now, isCar[addr] == true)
            }
            if (!now) linked.remove(addr)
        }
        val f = fire ?: return
        if (report) onChange(f.first, f.second, f.third)
    }
}

/**
 * bluetooth.audio 那条信号的正文 —— 给 OS 的一句人话.
 *
 *	**名字读不到的时候不编一个**: 说"连着蓝牙「」"比说"连着蓝牙设备"难看,
 *	但编一个名字比两者都糟.
 */
fun btText(name: String, connected: Boolean, car: Boolean): String {
    val who = if (name.isBlank()) "" else "「$name」"
    return when {
        connected && car -> "连着车载蓝牙$who（多半在开车）"
        connected -> "连着蓝牙$who"
        car -> "断开了车载蓝牙$who（多半下车了）"
        else -> "断开了蓝牙$who"
    }
}
