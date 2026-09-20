package com.neox.sense

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.ServerSocket
import kotlin.concurrent.thread

class HeartbeatTest {

    // 裸 ServerSocket —— 安卓的单测 classpath 里没有 com.sun.net.httpserver
    private fun withServer(handler: (String) -> Unit, block: (String) -> Unit) {
        val server = ServerSocket(0)
        val t = thread(isDaemon = true) {
            try {
                server.accept().use { sock ->
                    val line = BufferedReader(InputStreamReader(sock.getInputStream()))
                        .readLine() ?: ""
                    // "POST /heartbeat?source=... HTTP/1.1"
                    handler(line.split(" ").getOrElse(1) { "" })
                    sock.getOutputStream().write(
                        ("HTTP/1.1 200 OK\r\nContent-Length: 11\r\n\r\n" +
                            """{"ok":true}""").toByteArray()
                    )
                    sock.getOutputStream().flush()
                }
            } catch (_: Exception) {
            }
        }
        try {
            block("http://127.0.0.1:${server.localPort}")
            t.join(3000)
        } finally {
            server.close()
        }
    }

    @Test
    fun `报到打到 heartbeat 并带上自己的节奏`() {
        var seen = ""
        withServer({ seen = it }) { base ->
            Uploader(base, "t").beat("phone.mk", 600)
        }
        assertTrue("没打到 /heartbeat: $seen", seen.startsWith("/heartbeat"))
        assertTrue("没带 source: $seen", seen.contains("source=phone.mk"))
        // **节奏必须自己报**: 手机十分钟传一次是正常的, 家居桥接十分钟
        // 不吭声就是坏了 —— OS 不该替它定这个数
        assertTrue("没带节奏: $seen", seen.contains("everySec=600"))
    }

    @Test
    fun `拿不到定位时也要报到, 而且要说清原因`() {
        var seen = ""
        withServer({ seen = it }) { base ->
            Uploader(base, "t").beatFailing("phone.mk", 600, "没有定位权限")
        }
        assertTrue("没带原因: $seen", seen.contains("why="))
        // **照打不误** —— 不打的话这就退化成"失联", 而失联和"活着但瞎了"
        // 的下一步不一样: 一个是去看手机还在不在, 一个是去看权限
        assertTrue("没打到 /heartbeat: $seen", seen.startsWith("/heartbeat"))
    }

    @Test
    fun `报到打不通不该把这一轮弄崩`() {
        // 报到是尽力而为的: 为它抛异常等于让一个次要的东西挤掉投递
        Uploader("http://127.0.0.1:1", "t").beat("phone.mk", 600)
        assertEquals(true, true)
    }
}
