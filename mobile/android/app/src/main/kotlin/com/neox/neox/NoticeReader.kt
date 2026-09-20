package com.neox.neox

import android.app.Notification
import android.content.Context
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification
import org.json.JSONArray
import org.json.JSONObject
import java.io.OutputStream
import java.net.HttpURLConnection
import java.net.URL
import kotlin.concurrent.thread

/**
 * 读手机上的通知 —— **它记住任何事情的入口**.
 *
 * ── 为什么这一条比看起来重要 ──
 *
 *	短信里的验证码和快递单号、外卖到了没、银行扣了多少、群里谁@了你 ——
 *	这些事全都以通知的形式经过这台手机, 而它们**没有一个有 API**.
 *	读通知是唯一一条能把它们收进来的路。
 *
 *	用户的原话: "他只要能读取到通知，就能帮我记住任何事情"。
 *
 * ── 这是这个 App 权限最大的一件事, 所以三条闸都在这儿 ──
 *
 *	① **默认关着**, 而且开关不在我们手里: 系统那一页要用户亲手点。
 *	   我们只能引导他过去。
 *	② **只在他开了"读通知"之后才真的往外发** —— 系统给了权限不等于
 *	   他同意了。两个开关是两件事: 系统那个是"能不能读", 我们这个是
 *	   "要不要用"。
 *	③ **筛掉绝大多数**: 常驻通知(音乐、导航、我们自己那条)、没正文的、
 *	   重复的, 一条都不发。一天几百条原样上传, 账本会被它撑爆,
 *	   而那正是感知层最该避免的东西。
 *
 * ── 为什么不做关键词白名单 ──
 *
 *	"只收短信和银行"看着更安全, 实际是把判断权从他手里拿走了:
 *	他想让它记住的那件事, 十有八九不在我们想得到的名单里。
 *	要减就整个关掉 —— 那是一个他看得懂的决定。
 */
class NoticeReader : NotificationListenerService() {

    companion object {
        /** on 用户在我们这边开了没有. 系统权限另算 —— 见文件头 ② */
        fun on(c: Context) = KeepAliveService.prefs(c).getBoolean("readNotices", false)

        /** 我们自己那条常驻通知**绝不能回传** —— 那是一个自己喂自己的环 */
        private const val SELF = "com.neox.neox"

        /** 同一条在这么久之内重复就不再发 —— 很多 app 会反复 post 同一条 */
        private const val DEDUP_MS = 10 * 60 * 1000L
    }

    /** 最近发过的, 去重用. 键是 包名+标题+正文 */
    private val seen = object : LinkedHashMap<String, Long>(64, .75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, Long>?) = size > 200
    }

    override fun onNotificationPosted(sbn: StatusBarNotification?) {
        val n = sbn ?: return
        if (!on(this)) return
        if (n.packageName == SELF) return
        // 常驻的那些是**状态不是事件**: 音乐在播、导航在走、别的 app 的
        // 前台服务 —— 它们一天刷新几百次, 而没有一次是"发生了什么"
        if (n.isOngoing) return

        val ex = n.notification?.extras ?: return
        val title = ex.getCharSequence(Notification.EXTRA_TITLE)?.toString()?.trim().orEmpty()
        val text = (ex.getCharSequence(Notification.EXTRA_BIG_TEXT)
            ?: ex.getCharSequence(Notification.EXTRA_TEXT))?.toString()?.trim().orEmpty()
        // 标题和正文都没有的通知, 对模型是零信息 —— 一个光秃秃的 app 名字
        if (text.isEmpty() && title.isEmpty()) return

        val key = n.packageName + " " + title + " " + text
        val now = System.currentTimeMillis()
        synchronized(seen) {
            val last = seen[key]
            if (last != null && now - last < DEDUP_MS) return
            seen[key] = now
        }

        send(
            JSONObject()
                .put("app", appName(n.packageName))
                .put("title", title)
                .put("text", text.take(400))
        )
    }

    /**
     * 发出去 —— **自己走一条最简单的路, 不借 Collector**.
     *
     *	Collector 是攒批发的(位置那类信号迟到了照样有用), 而通知是
     *	**当下的事**: 验证码攒五分钟再发就没用了。所以一条来一条发,
     *	代价是多几次请求, 换的是它真的算"刚刚发生"。
     */
    private fun send(body: JSONObject) {
        val p = KeepAliveService.prefs(this)
        val b = p.getString("base", "")?.trimEnd('/').orEmpty()
        val t = p.getString("token", "").orEmpty()
        val src = p.getString("deviceId", "").orEmpty()
        if (b.isEmpty() || t.isEmpty() || src.isEmpty()) return
        val one = JSONObject()
            .put("id", src + "-notice-" + System.currentTimeMillis())
            .put("source", src)
            .put("kind", "notice.posted")
            .put("at", System.currentTimeMillis())
            .put("body", body)
        thread {
            try {
                val conn = (URL(b + "/signals").openConnection() as HttpURLConnection).apply {
                    requestMethod = "POST"
                    setRequestProperty("Authorization", "Bearer " + t)
                    setRequestProperty("Content-Type", "application/json")
                    connectTimeout = 15000
                    readTimeout = 20000
                    doOutput = true
                }
                conn.outputStream.use { s: OutputStream ->
                    s.write(JSONArray().put(one).toString().toByteArray())
                }
                conn.responseCode
                conn.disconnect()
            } catch (e: Exception) {
                // 网不通就算了 —— **通知不补发**: 一条迟到两小时的
                // "外卖到了"比不发更让人困惑
            }
        }
    }

    /** 包名换成人看得懂的名字 —— 模型对 com.tencent.mm 能做的判断为零 */
    private fun appName(pkg: String): String = try {
        val pm = packageManager
        pm.getApplicationLabel(pm.getApplicationInfo(pkg, 0)).toString()
    } catch (e: Exception) {
        pkg
    }
}
