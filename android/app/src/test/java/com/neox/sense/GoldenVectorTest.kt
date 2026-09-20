package com.neox.sense

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * **跟 Go 那份实现逐条对拍.**
 *
 * ── 为什么这个测试比别的都重要 ──
 *
 * 降采样在这套系统里有两份实现: Go 的 sense/phone.go 是可执行的规格,
 * 这份 Kotlin 是真正跑在手机上的那个.
 *
 * 多份实现的经典死法是**差一点** —— 阈值差 5%、边界条件差一个等号.
 * 而"差一点"在这里的表现是: 上传的信号多了几倍或者少了几条,
 * **没有任何地方会报错**, 只会表现为"它最近好像有点吵"或者
 * "它好像不知道我去过哪儿".
 *
 * 这个仓库已经用同样的办法锁 Go/TS 两份 confine 实现(confine/testdata),
 * 这是第三次用它.
 *
 * 样本从 go/sense/testdata/phone_trace.json 直接拷过来 —— 它是
 * Go 侧生成的, 也就是说这个测试断言的是"Kotlin 的输出 == Go 的输出".
 */
class GoldenVectorTest {

    private fun loadTrace(): JSONObject {
        val raw = javaClass.classLoader!!
            .getResourceAsStream("phone_trace.json")!!
            .bufferedReader().readText()
        return JSONObject(raw)
    }

    @Test
    fun kotlinMatchesGoSignalForSignal() {
        val v = loadTrace()
        val trace = v.getJSONArray("trace")
        val expect = v.getJSONArray("expect")

        val p = PhoneFilter("phone.mk")
        val got = mutableListOf<Signal>()
        for (i in 0 until trace.length()) {
            val f = trace.getJSONObject(i)
            got += p.location(
                Fix(
                    at = f.getLong("At"),
                    lat = f.getDouble("Lat"),
                    lon = f.getDouble("Lon"),
                    acc = f.getDouble("Acc"),
                )
            )
        }

        assertEquals(
            "产出的信号条数跟 Go 不一致 —— 阈值或边界条件差了一点。" +
                "得到 ${got.map { it.kind }}",
            expect.length(), got.size,
        )
        for (i in 0 until expect.length()) {
            val e = expect.getJSONObject(i)
            assertEquals("第 $i 条的种类对不上", e.getString("kind"), got[i].kind)
            assertEquals("第 $i 条的事件时间对不上", e.getLong("at"), got[i].at)
        }
        // 降噪比也要对得上量级 —— 只对条数不对比例的话,
        // 一个"恰好产出 7 条但全错"的实现也能过
        assertTrue("降噪比不对: ${trace.length()} → ${got.size}", got.size < trace.length() / 100)
        println("${trace.length()} 个定点 → ${got.size} 条信号, 扔掉低精度 ${p.droppedFixes} 个")
    }

    /**
     * 低精度定点要被扔掉, 而且数量要跟 Go 一致.
     *
     * 这条单独断言是因为**它是最容易悄悄写错的一个**: 判断写成 >= 还是 >
     * 都能跑, 只是扔掉的点数差几个 —— 而那几个点恰好可能是打断停留的那个.
     */
    @Test
    fun lowAccuracyFixesAreDroppedLikeGo() {
        val v = loadTrace()
        val trace = v.getJSONArray("trace")
        val p = PhoneFilter("phone.mk")
        for (i in 0 until trace.length()) {
            val f = trace.getJSONObject(i)
            p.location(Fix(f.getLong("At"), f.getDouble("Lat"), f.getDouble("Lon"), f.getDouble("Acc")))
        }
        // Go 侧实测扔掉 60 个(商场那段一半的点失锁)
        assertEquals("扔掉的低精度定点数跟 Go 不一致", 60, p.droppedFixes)
    }

    /** 静止半小时只该产出一条到达 —— 静止定位每次坐标都不一样 */
    @Test
    fun sittingStillProducesOneArrival() {
        val p = PhoneFilter("phone.mk")
        val got = mutableListOf<Signal>()
        for (i in 0 until 60) {
            val jitter = (i % 7) * 0.00005
            got += p.location(Fix(i * 30_000L, 31.86 + jitter, 117.28, 15.0))
        }
        assertEquals("静止半小时该只产出一条 place.arrived, 实际 ${got.map { it.kind }}",
            1, got.size)
        assertEquals("place.arrived", got[0].kind)
    }

    /** 等红灯、堵车、地铁进站不该变成"到达" */
    @Test
    fun passingThroughProducesNothing() {
        val p = PhoneFilter("phone.mk")
        val got = mutableListOf<Signal>()
        for (i in 0 until 20) {
            got += p.location(Fix(i * 60_000L, 31.86 + i * 0.0027, 117.28, 15.0))
        }
        assertEquals("一路移动却报了 ${got.map { it.kind }}", 0, got.size)
    }

    /** 电量 100→1 该跨六档(含 5% 以下那档) */
    @Test
    fun batteryCrossingsMatchGo() {
        val p = PhoneFilter("phone.mk")
        p.battery(100, false)
        var n = 0
        for (pct in 99 downTo 1) n += p.battery(pct, false).size
        assertEquals("电量跨档数跟 Go 不一致", 6, n)
    }

    /**
     * **到达/离开必须带可知时间, 而且要跟 Go 一致.**
     *
     * 不填的话它比总线的水位线老一整个 dwell, 每一条"你到公司了"
     * 都会被判成历史走补传通道 —— 而这是最该实时说的一类.
     */
    @Test
    fun staySignalsCarryKnownAtLikeGo() {
        val p = PhoneFilter("phone.mk")
        val got = mutableListOf<Signal>()
        for (i in 0..40) got += p.location(Fix(i * 60_000L, 31.86, 117.28, 15.0))
        assertEquals(1, got.size)
        assertEquals("发生时间该是进入那一刻", 0L, got[0].at)
        assertEquals("可知时间该是判出来那一刻(第 5 分钟)", 5 * 60_000L, got[0].knownAt)

        got += p.location(Fix(41 * 60_000L, 31.88, 117.28, 15.0))
        assertEquals("离开的可知时间该是出簇那一刻", 41 * 60_000L, got[1].knownAt)
    }

    /** 球面距离必须跟 Go 一致 —— 半径阈值对它很敏感 */
    @Test
    fun haversineMatchesGo() {
        val d = PhoneFilter.metersBetween(
            Fix(0, 31.86, 117.28, 0.0), Fix(0, 31.88, 117.28, 0.0))
        // 纬度差 0.02 度 ≈ 2223 米
        assertTrue("球面距离算错了: $d", d > 2200 && d < 2250)
    }
}
