package com.neox.neox

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.os.Handler
import android.os.Looper
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import java.util.Locale

/**
 * 念出来 —— **一个引擎, 两个用处**.
 *
 * ── 为什么要抽出来 ──
 *
 *	主动消息(闹钟、提醒)在保活服务里念, 而对话里的回话在前台念。
 *	各建一个 TextToSpeech 的话, 一台手机上同时挂两个引擎, 两边会
 *	抢音频焦点、抢着说 —— 而用户听到的是两句话叠在一起。
 *
 * ── 构造是异步的, 这件事栽过 ──
 *
 *	建完立刻 speak 会静默返回 ERROR, 而没人看返回值。所以攒着回调,
 *	好了再一起放。
 *
 * ── 念的时候要让别人知道 ──
 *
 *	两个"别人":
 *
 *	① **车上的导航和音乐**. 要一个"暂时的、可以压低别人"的音频焦点
 *	   (TRANSIENT_MAY_DUCK): 导航/音乐把音量压一下, 念完还回去.
 *	   不要焦点的话两边叠着响, 他哪句都听不清; 要独占的话导航那句
 *	   "前方右转"被掐掉, 他会把这个功能关了.
 *
 *	② **语音那一页的麦克风**. 它念的话会被自己的麦克风听进去, 再当成
 *	   他说的发出去 —— 2026-09-11 车上就是这样, 一路上它在跟自己说话.
 *	   所以开始念/念完了都报给界面(见 listener), 界面在念的时候不收.
 */
object Speaker {
    private val lock = Any()
    private var tts: TextToSpeech? = null
    private var ready = false
    private val waiting = mutableListOf<(TextToSpeech) -> Unit>()

    /**
     * listener 开始念(true) / 全念完了(false) —— **在主线程上叫**.
     *
     *	MainActivity 把它接到 MethodChannel 上, 语音那一页据此在念的时候
     *	把麦克风听到的扔掉. 是 null 的时候(界面没开)就没人听, 照念不误
     */
    @Volatile
    var listener: ((Boolean) -> Unit)? = null

    private val main = Handler(Looper.getMainLooper())

    /** 还没念完的那几句的 id —— 空了才算"念完了", 才还焦点 */
    private val live = mutableSetOf<String>()

    private val attrs: AudioAttributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_ASSISTANT)
        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
        .build()

    private var focus: AudioFocusRequest? = null
    private var am: AudioManager? = null

    /** speaking 现在正在念. 界面问得到 */
    @Volatile
    var speaking = false
        private set

    /**
     * 念一句.
     *
     * @param queue true = 排在后面念(主动消息那条), false = 打断当前
     *   这一句(对话那条: 他又说了一句, 上一句的回话就作废了)
     */
    fun say(ctx: Context, text: String, queue: Boolean) {
        val t = text.trim()
        if (t.isEmpty()) return
        // 太长的不念完 —— 一条念两分钟的通知比不念更烦
        val one = if (t.length > 300) t.take(300) + "。" else t
        if (am == null) {
            am = ctx.applicationContext.getSystemService(Context.AUDIO_SERVICE) as? AudioManager
        }
        ensure(ctx) { engine ->
            val id = "neox-" + System.nanoTime()
            // 打断上一句的话, 上一句那几个 id 就不算数了 —— 它们会各自
            // 回一个 onStop, 那时候从空集合里删, 不碍事
            if (!queue) live.clear()
            live.add(id)
            grab()
            mark(true)
            engine.speak(
                one,
                if (queue) TextToSpeech.QUEUE_ADD else TextToSpeech.QUEUE_FLUSH,
                null,
                id
            )
        }
    }

    fun stop() {
        synchronized(lock) {
            tts?.stop()
            live.clear()
            release()
            mark(false)
        }
    }

    /** 一句结束了(念完 / 出错 / 被打断) —— 最后一句结束才算念完 */
    private fun ended(id: String?) {
        synchronized(lock) {
            if (id != null) live.remove(id)
            if (live.isNotEmpty()) return
            release()
            mark(false)
        }
    }

    /** 报给界面. **变了才报**, 并且挪到主线程 —— MethodChannel 只认主线程 */
    private fun mark(on: Boolean) {
        if (speaking == on) return
        speaking = on
        val l = listener ?: return
        main.post { l(on) }
    }

    /** 要一个"暂时的、可以压低别人"的焦点. 要不到也照念 —— 念不出来比叠着响糟 */
    private fun grab() {
        if (focus != null) return
        val m = am ?: return
        val req = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
            .setAudioAttributes(attrs)
            .setOnAudioFocusChangeListener { }
            .build()
        try {
            m.requestAudioFocus(req)
            focus = req
        } catch (e: Exception) {
            // 有的 ROM 在这儿抛 —— 不要焦点也照念
        }
    }

    /** 还回去. **一定要还**: 不还的话导航的音量一直是压着的 */
    private fun release() {
        val req = focus ?: return
        focus = null
        try { am?.abandonAudioFocusRequest(req) } catch (e: Exception) {}
    }

    private fun ensure(ctx: Context, run: (TextToSpeech) -> Unit) {
        synchronized(lock) {
            val have = tts
            if (have != null && ready) {
                run(have); return
            }
            waiting.add(run)
            if (have != null) return
            tts = TextToSpeech(ctx.applicationContext) { status ->
                synchronized(lock) {
                    ready = status == TextToSpeech.SUCCESS
                    val engine = tts
                    if (!ready || engine == null) {
                        // 这台机器没装语音引擎 —— **不报错也不重试**:
                        // 通知已经发出去了, 念不出来只是少一层
                        waiting.clear()
                        return@synchronized
                    }
                    engine.language = Locale.CHINESE
                    // 声音走"助理"那一路 —— 系统据此决定压谁、从哪儿出声
                    // (车上是车机喇叭, 跟导航同一路)
                    engine.setAudioAttributes(attrs)
                    engine.setOnUtteranceProgressListener(
                        object : UtteranceProgressListener() {
                            override fun onStart(id: String?) {
                                synchronized(lock) { mark(true) }
                            }

                            override fun onDone(id: String?) = ended(id)

                            @Deprecated("老接口, 但不覆盖的话有的 ROM 不回调")
                            override fun onError(id: String?) = ended(id)

                            override fun onError(id: String?, errorCode: Int) = ended(id)

                            /** 被 QUEUE_FLUSH 或 stop() 掐掉的那几句 */
                            override fun onStop(id: String?, interrupted: Boolean) = ended(id)
                        })
                    val pending = waiting.toList()
                    waiting.clear()
                    pending.forEach { it(engine) }
                }
            }
        }
    }
}
