package com.neox.neox

import android.Manifest
import android.content.ContentUris
import android.content.Context
import android.content.pm.PackageManager
import android.provider.CalendarContract
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import kotlin.concurrent.thread

/**
 * 读手机上的日历 —— **他已经有的日程, 不该让他再说一遍**.
 *
 * ── 为什么要接 ──
 *
 *	"下周三三点开会"这件事十有八九已经在他手机日历里了 —— 公司发的
 *	会议邀请、家人共享的日程、订的机票酒店, 都会自动落进去。
 *
 *	而助理这边一无所知, 于是它算"今天该几点出门"时把整个下午当空的,
 *	问"我今天有什么事"只能答"你没跟我说过"。
 *
 * ── 为什么不做双向同步 ──
 *
 *	**只读**。往他的日历里写东西是一件他没同意过的事: 一条误加的日程
 *	会出现在他老板的共享视图里, 而他不知道是谁加的。
 *
 *	它自己的待办走 OS 那边的 agenda(见 go/osinit/agenda.go), 两边分开:
 *	他的日历是他的, 它记的是它记的。
 *
 * ── 为什么只看往后 7 天 ──
 *
 *	再往后的事今天用不上, 而每一条都要过一次账本。7 天覆盖了"这周
 *	还有什么"和"明天几点出门"这两个真问题。
 */
object Agenda {

    /** on 用户开了没有 */
    fun on(c: Context) = KeepAliveService.prefs(c).getBoolean("readCalendar", false)

    fun can(c: Context) =
        c.checkSelfPermission(Manifest.permission.READ_CALENDAR) ==
            PackageManager.PERMISSION_GRANTED

    private const val AHEAD_MS = 7L * 24 * 3600 * 1000

    /**
     * 扫一遍往后 7 天, 有变化就报上去.
     *
     *	**整批发, 不是一条一条**: 日历不像验证码, 早几分钟晚几分钟没差别,
     *	而一次请求比十次省电。
     *
     *	**只报变化**: 每次扫出来的东西大部分跟上次一样, 全发一遍等于
     *	一天几百条重复进账本 —— 电量那条已经栽过一次了。
     */
    fun sweep(ctx: Context) {
        if (!on(ctx) || !can(ctx)) return
        val now = System.currentTimeMillis()
        val rows = read(ctx, now, now + AHEAD_MS)
        if (rows.isEmpty()) return

        val p = KeepAliveService.prefs(ctx)
        // 上一次报过的那一批, 一个指纹. 一样就不再发
        val fp = rows.joinToString("|") { it.optString("id") + it.optLong("at") }
        if (p.getString("calFp", "") == fp) return
        p.edit().putString("calFp", fp).apply()

        val b = p.getString("base", "")?.trimEnd('/').orEmpty()
        val t = p.getString("token", "").orEmpty()
        val src = p.getString("deviceId", "").orEmpty()
        if (b.isEmpty() || t.isEmpty() || src.isEmpty()) return

        val batch = JSONArray()
        for (row in rows) {
            batch.put(
                JSONObject()
                    .put("id", src + "-cal-" + row.optString("id"))
                    .put("source", src)
                    .put("kind", "calendar.event")
                    .put("at", now)
                    .put("body", row)
            )
        }
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
                conn.outputStream.use { it.write(batch.toString().toByteArray()) }
                conn.responseCode
                conn.disconnect()
            } catch (e: Exception) {
                // 网不通 —— 把指纹清掉, 下次重来. **不清的话这一批
                // 就永远发不出去了**: 指纹已经记成"发过了"
                p.edit().remove("calFp").apply()
            }
        }
    }

    /** 读 [from, to) 之间的日程. 出错返回空 —— 读不到不是错误, 是没有 */
    private fun read(ctx: Context, from: Long, to: Long): List<JSONObject> {
        val out = mutableListOf<JSONObject>()
        val uri = CalendarContract.Instances.CONTENT_URI.buildUpon().let {
            ContentUris.appendId(it, from)
            ContentUris.appendId(it, to)
            it.build()
        }
        val cols = arrayOf(
            CalendarContract.Instances.EVENT_ID,
            CalendarContract.Instances.TITLE,
            CalendarContract.Instances.BEGIN,
            CalendarContract.Instances.END,
            CalendarContract.Instances.EVENT_LOCATION,
            CalendarContract.Instances.ALL_DAY,
        )
        try {
            ctx.contentResolver.query(
                uri, cols, null, null, CalendarContract.Instances.BEGIN + " ASC"
            )?.use { c ->
                while (c.moveToNext() && out.size < 40) {
                    val title = c.getString(1)?.trim().orEmpty()
                    // 没标题的日程对模型是零信息
                    if (title.isEmpty()) continue
                    out.add(
                        JSONObject()
                            .put("id", c.getLong(0))
                            .put("what", title)
                            .put("at", c.getLong(2))
                            .put("until", c.getLong(3))
                            .put("where", c.getString(4)?.trim().orEmpty())
                            .put("allDay", c.getInt(5) != 0)
                    )
                }
            }
        } catch (e: Exception) {
            // 权限被中途撤掉、或者这台机器没有日历 provider
        }
        return out
    }
}
