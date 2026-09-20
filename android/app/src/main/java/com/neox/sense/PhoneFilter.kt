package com.neox.sense

import kotlin.math.abs
import kotlin.math.asin
import kotlin.math.cos
import kotlin.math.min
import kotlin.math.PI
import kotlin.math.round
import kotlin.math.sin
import kotlin.math.sqrt

/**
 * 手机端降采样 —— **Go 那份 sense/phone.go 的逐条移植**.
 *
 * ── 为什么是"移植"而不是"另写一个" ──
 *
 * 这是这套系统里的第三份实现(Go 的规格 / 这份 Kotlin / 未来可能的 iOS).
 * 而多份实现的经典死法是**差一点**: 阈值差 5%、边界条件差一个等号,
 * 表现是"信号多了几倍或少了几条", 而**没有任何地方会报错**.
 *
 * 所以这份代码的判据不是"看起来对", 是 GoldenVectorTest 逐条对得上.
 * 改这个文件之前先看那个测试.
 *
 * ── 为什么降采样必须在手机上 ──
 *
 * 开着定位, 原始定点每几秒一个, 一天两万多个. 全传上去的话:
 * 事件日志是 append-only 且要落盘的, 账本几天就爆;
 * 而且你在公司坐四小时是**一件事**, 不是 2400 件事 ——
 * 传 2400 个点上去, 模型看到的是噪音的海.
 *
 * 上传的应该是「19:00–19:40 在公司」, 不是 2400 个 GPS 点.
 */

data class Fix(val at: Long, val lat: Double, val lon: Double, val acc: Double)

data class Signal(
    val id: String,
    val source: String,
    val kind: String,
    /** 事件**发生**的时刻. 内容用它 —— "你 19:00 到的公司" */
    val at: Long,
    /**
     * 这件事**什么时候才算得出来**. 0 = 跟 at 相同(绝大多数信号).
     *
     * "你到了"要到停留够久(dwell)才判得出来. 不填的话这条信号比总线的
     * 水位线老一整个 dwell(生产配置 5 分钟, 而容忍度只有 2 分钟),
     * 于是**每一条"你到公司了"都被判成历史**, 走补传通道 ——
     * 而这恰恰是最该实时说的一类. 真机上撞到过.
     *
     * **它不是"上传时刻"**: 离线一天后补传上来的信号当时就算出来了,
     * 只是发不出去, 它们的 knownAt 是当时 —— 填成上传时刻的话,
     * 补传会伪装成实时.
     */
    val knownAt: Long = 0,
    val body: Map<String, Any>,
)

data class PhoneConfig(
    /**
     * 多近算"还在原地", 米.
     *
     * 不能太小: 静止的手机定位每次都不一样(城市里误差 10~50 米是常态),
     * 半径设成 20 米的话, 你坐着不动也会被判成一直在"离开-到达".
     */
    val stayRadius: Double = 120.0,
    /**
     * 待多久才算"停留", 毫秒.
     *
     * 没有它的话, 等红灯、堵车、地铁进站都会变成一次"到达" ——
     * 一天几十条假的"你到了某地".
     */
    val stayDwell: Long = 5 * 60 * 1000,
    /**
     * 精度差过这个值的定点**直接扔掉**, 米.
     *
     * 进了商场或隧道 GPS 失锁, 系统拿基站粗定位顶上, 精度掉到几百米甚至几公里.
     * 那种点跟真实位置差得远, 而它**看起来完全合法**: 有坐标、有时间戳.
     * 后果很具体: 你在公司坐着没动, 一个 1500 米精度的点飘出去 → 判成"离开",
     * 下一个点飘回来 → "到达". 一天全是假的进出, 而真实世界里你一步没挪.
     */
    val maxAccuracy: Double = 200.0,
    /** 电量到哪几档才报. 每 1% 报一次 = 一天一百多条, 而 87%→86% 不含信息 */
    val batteryLevels: List<Int> = listOf(100, 50, 30, 20, 10, 5),
)

class PhoneFilter(private val source: String, private val cfg: PhoneConfig = PhoneConfig()) {

    private var anchor: Fix? = null
    private var since: Long = 0
    private var lastAt: Long = 0
    private var arrived = false

    private var lastLevel = -1
    private var charging = false
    private var haveCharge = false

    /**
     * 因为精度太差被扔掉的定点数.
     *
     * 这个数要能被问出来: 一部手机如果 90% 的定点都被扔掉, 那不是算法在工作,
     * 是这台设备的定位坏了/权限被限制了. 两种情况的表现一模一样(信号很少),
     * 只有这个计数能把它们分开.
     */
    var droppedFixes = 0
        private set

    /**
     * 喂一个定点, 吐出值得上报的信号(通常是零条).
     *
     * **到达和离开分成两条**: 一段停留只有在结束时才能完整地知道(待了多久),
     * 但那时候再说"你刚才在公司待了 40 分钟"已经晚了 —— 主动智能要的是
     * "你到公司了"这件事在你到的时候就知道.
     */
    fun location(f: Fix): List<Signal> {
        // 精度闸放最前面: 一个飘了 1500 米的点不该有资格打断一段停留
        if (f.acc > cfg.maxAccuracy) {
            droppedFixes++
            return emptyList()
        }
        val a = anchor
        if (a == null) {
            anchor = f; since = f.at; lastAt = f.at; arrived = false
            return emptyList()
        }
        if (metersBetween(a, f) <= cfg.stayRadius) {
            lastAt = f.at
            if (!arrived && f.at - since >= cfg.stayDwell) {
                arrived = true
                return listOf(
                    Signal(
                        id = "$source|arrived|$since",
                        source = source, kind = "place.arrived", at = since,
                        knownAt = f.at, // 判出来的就是这一个定点
                        body = mapOf("lat" to round5(a.lat), "lon" to round5(a.lon), "acc" to a.acc),
                    )
                )
            }
            return emptyList()
        }

        val out = mutableListOf<Signal>()
        if (arrived) {
            out += Signal(
                id = "$source|left|$since",
                source = source, kind = "place.left",
                // 事件时间用**离开的那一刻**: 这条信号说的是"他走了"
                at = lastAt,
                knownAt = f.at, // 出簇的这一个定点才判得出来
                body = mapOf(
                    "lat" to round5(a.lat), "lon" to round5(a.lon),
                    "stayedMs" to (lastAt - since),
                ),
            )
        }
        // **没待够就出簇的, 一个字都不报.**
        // 这是降噪的大头: 走路/开车/坐地铁的路上会不停地出簇进簇,
        // 每次都报的话一趟通勤就是几十条"到达/离开".
        anchor = f; since = f.at; lastAt = f.at; arrived = false
        return out
    }

    /** 电量: 只有跨档和充电状态变化才报 */
    fun battery(pct: Int, isCharging: Boolean): List<Signal> {
        val out = mutableListOf<Signal>()
        val lvl = levelOf(pct)
        if (haveCharge && isCharging != charging) {
            out += Signal(
                id = "$source|charge|$isCharging|$pct", source = source,
                kind = "battery.charging", at = 0,
                body = mapOf("charging" to isCharging, "pct" to pct),
            )
        }
        charging = isCharging; haveCharge = true

        // 只在**往下掉**跨档时报: 充电时 20→100 会跨四档,
        // 而"电充上去了"是一件事不是四件事
        if (lvl != lastLevel && (lastLevel == -1 || lvl < lastLevel) && !isCharging) {
            lastLevel = lvl
            out += Signal(
                id = "$source|battery|$lvl", source = source,
                kind = "battery.level", at = 0,
                body = mapOf("level" to lvl, "pct" to pct),
            )
            return out
        }
        if (lvl > lastLevel) lastLevel = lvl // 充上电了, 重新武装下一次下跌
        return out
    }

    /**
     * 离散事件(来电/未接/闹钟) —— **一条不许省**.
     *
     * 降采样只针对连续量. 对来电这类事件做降采样, 等于把最该知道的丢了.
     */
    // 全部用具名参数 —— 加 knownAt 那次, 位置参数的第 5 位从 body 变成了
    // knownAt, 编译当场炸. 炸了是好事, 但下一次可能就不炸了(如果类型碰巧兼容)
    fun event(kind: String, at: Long, body: Map<String, Any>) =
        listOf(Signal(id = "$source|$kind|$at", source = source, kind = kind,
            at = at, body = body))

    private fun levelOf(pct: Int): Int {
        for (l in cfg.batteryLevels) if (pct >= l) return l
        return 0
    }

    companion object {
        /**
         * 球面距离(haversine).
         *
         * 不用平面近似: 它在高纬度会系统性偏大, 而"半径 120 米"这种阈值
         * 对误差很敏感 —— 偏 20% 就等于换了个算法.
         */
        fun metersBetween(a: Fix, b: Fix): Double {
            val r = 6371000.0
            val rad = PI / 180
            val dLat = (b.lat - a.lat) * rad
            val dLon = (b.lon - a.lon) * rad
            val la1 = a.lat * rad
            val la2 = b.lat * rad
            val h = sin(dLat / 2) * sin(dLat / 2) +
                cos(la1) * cos(la2) * sin(dLon / 2) * sin(dLon / 2)
            return 2 * r * asin(min(1.0, sqrt(h)))
        }

        /**
         * 坐标留 5 位小数 ≈ 1 米.
         *
         * 再多没有意义(GPS 本身没那个精度), 但会让**同一个地方每次序列化出
         * 不同的字节** —— 而这些信号最终会进上下文, 那是前缀缓存的载体.
         */
        fun round5(v: Double): Double = round(v * 1e5) / 1e5

        @Suppress("unused")
        fun absOf(v: Double) = abs(v)
    }
}
