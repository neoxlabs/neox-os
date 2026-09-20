import 'dart:async';

import 'package:flutter/material.dart';

import '../api/events.dart';
import '../audio/asr.dart';
import '../audio/utterance.dart';
import '../sense/keepalive.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/neural_core.dart';

/// 语音模式 —— **这个 bot 变成一个能说话的人**。
///
/// ── 为什么它在聊天里, 不是一个独立的 tab ──
///
/// 独立页**不知道该跟谁说话**：如果把话发给 `bots.first`
/// （感知判断那个，它压根不回聊天），用户就会对着屏幕说半天，
/// 却得不到回应，也没有任何一处报错。
///
/// 从聊天里进来的话，"跟谁"这件事这一页本来就知道。
///
/// ── 语音这一轮的回答必须短 ──
///
/// 同一句话，打字看和念出来听是两种东西：屏幕上三行扫一眼就过去了，
/// 念出来是二十秒——而他多半在开车、在做饭、手上腾不出来。
///
/// 所以这一轮带一位 `voice` 到模型跟前（见 go/abi/wire.go），由它把
/// 话说短。**不在客户端截断**：截出来的是半句话，而半句话比长话糟。
class VoicePage extends StatefulWidget {
  const VoicePage({
    super.key,
    required this.state,
    required this.pid,
    required this.name,
  });

  final AppState state;
  final ProcessID pid;
  final String name;

  @override
  State<VoicePage> createState() => _VoicePageState();
}

class _VoicePageState extends State<VoicePage> {
  Asr? _asr;
  StreamSubscription<double>? _lvSub;
  StreamSubscription<AsrText>? _txtSub;
  StreamSubscription<bool>? _ttsSub;

  /// 一两百毫秒看一眼"他说完了没有" —— 见 [Utterance.tick]
  Timer? _tick;

  /// 念完之后再闭耳 500ms 的那个定时器 —— 见 [_echoTail]
  Timer? _unmute;

  /// 保险: 念了但原生那边一直没报"开始念" —— 见 [_speakOut]
  Timer? _ttsWatch;

  /// 碎片拼句 + 噪音判定. 见 audio/utterance.dart
  final _utt = Utterance();

  double _level = 0;
  bool _listening = false;
  bool _speaking = false;
  bool _waiting = false;
  DateTime? _sentAt;
  String _heard = '';
  String _said = '';
  String? _err;

  /// _dropped 上一句**没发**的那句和为什么 —— 灰着画出来.
  ///
  /// **不许悄悄吞**: 被判成噪音的万一是一句真话, 他得看得见它没发出去,
  /// 才知道要再说一遍. 悄悄吞掉的话, 他看到的又是"它不理我"
  String? _dropped;

  /// _muted 这会儿听到的一概不算: 它在念, 或者刚念完那一小会儿.
  ///
  /// ── 为什么非有不可 ──
  ///
  /// 2026-09-11 车上: 它念回话的时候麦克风开着, 念的话被自己听进去,
  /// 再当成他说的发出去, 模型对着自己的话又回一句 —— 一路上它在跟自己
  /// 聊天. 回声消除(见 asr.dart)减得掉手机喇叭的, 减不掉车机喇叭的.
  ///
  /// 代价是**念的时候不能用嗓子打断它** —— 点一下那颗球就能(见 _toggle)
  bool _muted = false;

  /// 念完之后再闭耳多久. 喇叭停了, 车里的混响和识别器肚子里那半句还在
  static const _echoTail = Duration(milliseconds: 500);

  /// 等回话最多等多久. 等不到(断网、OS 重启)就不等了 —— 否则这一页
  /// 永远停在"在想", 他后面说的话全攒着发不出去
  static const _waitCap = Duration(seconds: 90);

  @override
  void initState() {
    super.initState();
    widget.state.onBotSaid = _onBotSaid;
    widget.state.onBotFailed = _onBotFailed;
    _ttsSub = KeepAliveService.ttsEvents.listen(_onTts);
    _tick = Timer.periodic(const Duration(milliseconds: 150), (_) => _onTick());
    // 进来就开听 —— 他点"语音"要的就是"现在能说了",
    // 再让他点一下开始是多一道没有意义的门
    WidgetsBinding.instance.addPostFrameCallback((_) => _listen());
  }

  @override
  void dispose() {
    _asr?.stop();
    _tick?.cancel();
    _unmute?.cancel();
    _ttsWatch?.cancel();
    _ttsSub?.cancel();
    KeepAliveService.shutUp();
    if (widget.state.onBotSaid == _onBotSaid) widget.state.onBotSaid = null;
    if (widget.state.onBotFailed == _onBotFailed) widget.state.onBotFailed = null;
    super.dispose();
  }

  Future<void> _listen() async {
    try {
      _asr ??= await Asr.instance();
    } catch (e) {
      if (mounted) setState(() => _err = '$e');
      return;
    }
    if (!await _asr!.start()) {
      if (mounted) setState(() => _err = '没有麦克风权限');
      return;
    }
    _lvSub = _asr!.levels.listen((v) {
      if (mounted) setState(() => _level = v);
    });
    _txtSub = _asr!.texts.listen((r) {
      // 它在念 —— 听到的多半是它自己. 见 [_muted]
      if (!mounted || _muted) return;
      // **定稿只进缓冲, 不直接发**: 攒到他真的说完(静 1.2 秒)再合成
      // 一条. 见 Utterance
      _utt.heard(r.text, done: r.done);
      setState(() => _heard = _utt.showing(r.text));
    });
    if (mounted) setState(() { _listening = true; _err = null; });
  }

  /// 看一眼他说完了没有.
  void _onTick() {
    if (!mounted || !_listening) return;
    if (_waiting) {
      // **在等回话的时候不发**, 攒着 —— 上一轮还没答, 再塞一轮进去
      // 两个回答会叠在一起念. 等太久就不等了(见 _waitCap)
      final at = _sentAt;
      if (at == null || DateTime.now().difference(at) < _waitCap) return;
      setState(() => _waiting = false);
    }
    final h = _utt.tick();
    if (h == null) return;
    if (h.send) {
      _send(h.text);
    } else {
      setState(() {
        _dropped = '没发：${h.text}（${h.why}）';
        _heard = '';
      });
    }
  }

  Future<void> _send(String text) async {
    // 主动消息可能正在念 —— 先让它闭嘴. **不闭的话两句话叠在一起**
    await KeepAliveService.shutUp();
    if (mounted) {
      setState(() {
        _waiting = true;
        _sentAt = DateTime.now();
        _speaking = false;
        _said = '';
        _dropped = null;
        _heard = text;
      });
    }
    try {
      await widget.state.send(text, widget.pid, voice: true);
    } catch (e) {
      if (mounted) setState(() { _waiting = false; _err = '$e'; });
    }
  }

  void _onBotSaid(ProcessID pid, String text) {
    if (!mounted || pid != widget.pid) return;
    setState(() {
      _said = text;
      _waiting = false;
    });
    _speakOut(text);
  }

  /// 这一轮没走完 —— 说一声, 然后接着听.
  ///
  /// 原来这里什么都没有: 失败的那一轮不会有回话, 于是这一页一直停在
  /// "在想", 他后面说的全被当成"还在等"扔掉. 他以为是网不好
  void _onBotFailed(ProcessID pid, String brief) {
    if (!mounted || pid != widget.pid) return;
    setState(() {
      _said = '这一轮没做完：$brief';
      _waiting = false;
    });
    // 念的那句短一点 —— 原因可能是一长串, 在车上念完要十几秒
    final short = brief.length > 40 ? '${brief.substring(0, 40)}…' : brief;
    _speakOut('出错了：$short');
  }

  /// 念一句. **先闭耳朵再开口** —— 原生那边"开始念了"的回报是异步的,
  /// 等它到了再闭, 头几个字已经进了麦克风
  void _speakOut(String text) {
    _mute();
    setState(() => _speaking = true);
    KeepAliveService.say(text);
    // 保险: 两秒内没听到"开始念"(这台机器没装语音引擎), 就当念完了 ——
    // 不然耳朵永远闭着
    _ttsWatch?.cancel();
    _ttsWatch = Timer(const Duration(seconds: 2), () async {
      if (!await KeepAliveService.speaking()) _onTts(false);
    });
  }

  void _mute() {
    _unmute?.cancel();
    _muted = true;
  }

  /// 原生那边: 开始念了 / 全念完了. 主动消息(闹钟、提醒)念的时候也会来
  void _onTts(bool on) {
    if (!mounted) return;
    if (on) {
      _mute();
      setState(() => _speaking = true);
      return;
    }
    setState(() => _speaking = false);
    _unmute?.cancel();
    _unmute = Timer(_echoTail, () {
      _muted = false;
      // 念的时候识别器照样在听, 听进去的回声还攒在它肚子里 —— 清掉,
      // 从头记. 他在那之前说了没发的那截 Utterance 自己留着
      _asr?.resetText();
      _utt.restart();
      if (mounted) setState(() => _heard = '');
    });
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: NX.bg,
      appBar: AppBar(
        backgroundColor: NX.bg,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
        leading: IconButton(
          icon: const Icon(Icons.keyboard_arrow_down_rounded),
          color: NX.text2,
          onPressed: () => Navigator.pop(context),
        ),
        title: Text(widget.name, style: NX.heading),
        centerTitle: true,
      ),
      body: SafeArea(
        child: Column(children: [
          Expanded(
            child: GestureDetector(
              // 整颗球就是开关 —— 一屏只有一个东西的时候，
              // 再放一个按钮是多余的
              onTap: _toggle,
              behavior: HitTestBehavior.opaque,
              child: NeuralCore(
                level: _level,
                thinking: widget.state.thinkingCount,
                online: widget.state.conn == Conn.live,
                speaking: _speaking,
              ),
            ),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(NX.s6, 0, NX.s6, NX.s7),
            child: Column(children: [
              // ── 它答的那句压过你说的那句 ──
              //
              //	你说完就知道自己说了什么，而它答了什么才是你在等的
              SizedBox(
                height: 84,
                child: SingleChildScrollView(
                  reverse: true,
                  child: Text(
                    _said.isNotEmpty
                        ? _said
                        : (_waiting ? '…' : _heard),
                    style: _said.isNotEmpty
                        ? NX.body
                        : NX.body.copyWith(color: NX.text3),
                    textAlign: TextAlign.center,
                  ),
                ),
              ),
              const SizedBox(height: NX.s3),
              Text(_status(), style: NX.captionDim),
              // 没发的那句 —— 小一号的灰字, 一行. 见 [_dropped]
              if (_dropped != null)
                Padding(
                  padding: const EdgeInsets.only(top: NX.s1),
                  child: Text(_dropped!,
                      style: NX.captionDim,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis),
                ),
            ]),
          ),
        ]),
      ),
    );
  }

  String _status() {
    if (_err != null) return _err!;
    if (_speaking) return '它在说 · 轻触打断';
    if (_waiting) return '在想';
    if (_listening) return '在听 · 轻触暂停';
    return '轻触开始';
  }

  Future<void> _toggle() async {
    // 它在念的时候, 点一下是"别说了" —— 念的时候耳朵是闭着的(见 _muted),
    // 用嗓子打断不了它, 只能靠这一下
    if (_speaking) {
      await KeepAliveService.shutUp();
      _onTts(false);
      return;
    }
    if (!_listening) {
      await _listen();
      return;
    }
    await _asr?.stop();
    // 识别器下次 start 会从空的全文记起 —— 这边"用掉到哪儿"也得一起归零,
    // 两处状态必须一起清(asr.dart resetText 那段栽过的坑)
    _utt.clear();
    await _lvSub?.cancel();
    await _txtSub?.cancel();
    _lvSub = null;
    _txtSub = null;
    if (mounted) setState(() { _listening = false; _level = 0; });
  }
}
