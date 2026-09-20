package com.neox.sense

import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

/**
 * 把信号投给 OS 的采集入口.
 *
 * ── 唯一有判断的地方是"投不出去怎么办" ──
 *
 * 手机的网是断续的: 电梯、地铁、飞行模式、后台被限流. 这几分钟里
 * 产出的信号如果直接丢掉, 那些事件就**永久没有了** ——
 * 降采样之后剩下的每一条都是"一段停留"或"一次来电", 不是可以补采的原始点.
 *
 * 但缓冲不能无限长: 离线一整天的话, 无限缓冲会吃光内存, 而且恢复时
 * 一次性灌进去一天的信号 —— OS 那边会把它们判成迟到走补传通道(那没问题),
 * 但手机这边的内存不是无限的.
 *
 * 所以: **有界, 满了丢最老的, 而且丢这件事要能被看见.**
 */
class Backlog(private val max: Int = 500) {
    private val items = ArrayDeque<Signal>()
    var totalDropped = 0
        private set

    /** 返回这次丢了多少 —— 静默丢弃会让"为什么那段时间它什么都不知道"永远查不出来 */
    fun add(sigs: List<Signal>): Int {
        items.addAll(sigs)
        var dropped = 0
        while (items.size > max) {
            items.removeFirst() // 丢最老的: 新的更可能还有意义
            dropped++
        }
        totalDropped += dropped
        return dropped
    }

    fun take(): List<Signal> {
        val out = items.toList()
        items.clear()
        return out
    }

    fun putBack(sigs: List<Signal>) {
        // 放回队首 —— 顺序要保住, 否则事件时间会乱
        for (s in sigs.asReversed()) items.addFirst(s)
        while (items.size > max) {
            items.removeFirst()
            totalDropped++
        }
    }

    val size: Int get() = items.size
}

class Uploader(private val baseUrl: String, private val token: String) {

    /**
     * 批量投递. 返回 OS 侧的逐条统计, 失败抛异常(由调用方决定放回缓冲).
     *
     * **用批量端点而不是一条一条发**: 离线之后可能积了几百条,
     * 一条一个请求就是几百次 TLS 握手, 而且中间断一次就得从头对账.
     */
    /**
     * 跟 OS 报到: "我还在, 我的节奏是每 N 秒一次".
     *
     * **手机是最容易死的那一端**: 没电、被安卓杀后台、进省电模式、断网.
     * 不报到的话它静默地消失, 而 OS 一个字都没有 ——
     * "家里有没有人"从此停在最后一次位置上(S49 的判据正是靠它),
     * 用户看到的是"它不再提醒我了", 查不到根.
     *
     * **节奏自己报**: 手机十分钟传一次是正常的, 家居桥接十分钟不吭声
     * 就是坏了 —— OS 不该替它定这个数.
     *
     * **没东西可投的时候也要打**: 一个采集端最常见的死法不是崩溃,
     * 是投不进去(令牌过期、网断), 而那时候恰恰最需要让 OS 知道
     * "我还在, 只是投不出东西".
     */
    fun beat(source: String, everySec: Int) = ping(source, everySec, null)

    /**
     * 报到, 但如实说"我拿不到数据, 原因是…"(没权限、定位关了、GPS 拿不到点).
     *
     * **照打不误** —— 不打的话这就退化成"失联", 而失联和"活着但瞎了"
     * 的下一步不一样: 一个是去看手机还在不在, 一个是去看权限.
     */
    fun beatFailing(source: String, everySec: Int, why: String) =
        ping(source, everySec, why)

    private fun ping(source: String, everySec: Int, why: String?) {
        // **打不通不算错**: 报到本来就是尽力而为的, 为它重试或者抛异常
        // 等于让一个次要的东西挤掉投递的机会
        try {
            val q = StringBuilder("/heartbeat?source=")
                .append(URLEncoder.encode(source, "UTF-8"))
                .append("&everySec=").append(everySec)
            if (why != null) q.append("&why=").append(URLEncoder.encode(why, "UTF-8"))
            val conn = URL("$baseUrl$q").openConnection() as HttpURLConnection
            conn.requestMethod = "POST"
            conn.setRequestProperty("Authorization", "Bearer $token")
            conn.connectTimeout = 10_000
            conn.readTimeout = 10_000
            conn.responseCode
            conn.disconnect()
        } catch (_: Exception) {
        }
    }

    fun post(sigs: List<Signal>): Map<String, Int> {
        if (sigs.isEmpty()) return emptyMap()
        val arr = JSONArray()
        for (s in sigs) {
            val body = JSONObject()
            for ((k, v) in s.body) body.put(k, v)
            arr.put(
                JSONObject()
                    .put("id", s.id)
                    .put("source", s.source)
                    .put("kind", s.kind)
                    .put("at", s.at)
                    .put("knownAt", if (s.knownAt > 0) s.knownAt else s.at)
                    .put("body", body)
            )
        }
        val conn = URL("$baseUrl/signals").openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.setRequestProperty("Authorization", "Bearer $token")
        conn.setRequestProperty("Content-Type", "application/json")
        conn.doOutput = true
        conn.connectTimeout = 15_000
        conn.readTimeout = 20_000
        conn.outputStream.use { it.write(arr.toString().toByteArray()) }

        val code = conn.responseCode
        val text = (if (code == 200) conn.inputStream else conn.errorStream)
            ?.bufferedReader()?.readText() ?: ""
        conn.disconnect()
        if (code != 200) {
            // 报错要说清是哪一类 —— 401 和"连不上"是完全不同的两件事,
            // 而手机端只会看到"上传失败"
            throw RuntimeException(
                if (code == 401) "采集入口拒绝了 token(401) —— 检查 NEOX_SENSE_TOKEN"
                else "采集入口返回 $code: ${text.take(200)}"
            )
        }
        val counts = JSONObject(text).optJSONObject("counts") ?: return emptyMap()
        val out = mutableMapOf<String, Int>()
        for (k in counts.keys()) out[k] = counts.getInt(k)
        return out
    }
}
