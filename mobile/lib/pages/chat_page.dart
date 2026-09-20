import 'dart:async';
import 'dart:math' as math;
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';

import '../api/events.dart';
import '../api/os_client.dart';
import '../audio/asr.dart';
import '../state/app_state.dart';
import 'voice_page.dart';
import '../theme.dart';
import '../widgets/motion.dart';
import '../widgets/avatar.dart';
import '../widgets/empty_state.dart';
import '../widgets/row.dart';
import 'bot_page.dart';

/// 一个 bot 的会话页.
///
/// 从消息列表推进来, 顶上一个返回键 —— 手机上标准的那种.
///
/// 里面那些行的画法在 widgets/row.dart: **bot 说的话不是气泡**,
/// 只有用户自己那条是. 客户端就是这样.
class ChatPage extends StatefulWidget {
  const ChatPage({super.key, required this.state, required this.pid});
  final AppState state;

  /// 跟谁说话. **一页一个人** —— 客户端也是这样, 先选人再说话
  final ProcessID pid;

  @override
  State<ChatPage> createState() => _ChatPageState();
}

class _ChatPageState extends State<ChatPage> {
  final _input = TextEditingController();
  final _scroll = ScrollController();

  /// 输入框自己的滚动条.
  ///
  /// **光标设到末尾不等于会滚过去**: TextField 长到 maxLines 之后
  /// 内部开始滚, 而语音是一个字一个字灌进去的 —— 不主动滚的话
  /// 屏幕上停在最开头那几行, 新出来的字全在看不见的地方
  final _inputScroll = ScrollController();
  bool _sending = false;

  /// 待发的图 —— **先摆在输入条上面, 不是选完就发**.
  /// 选完就发的话选错一张就没法撤, 而相册里点错一张太容易了
  final _pending = <Attach>[];

  /// 说话转文字 —— **端侧**, 不用系统识别器.
  ///
  /// 上一版用 speech_to_text(安卓自带), 三个毛病: 依赖 Google 服务、
  /// 不能配热词、要联网. 换成 sherpa-onnx 之后三个全没了.
  ///
  /// 天玑 9400 在 4 线程下的 RTF 为 0.025 —— **40 倍实时**,
  /// 一块 100ms 的音频只要 2.5ms. 参数怎么定的见 audio/asr.dart.
  ///
  /// ── 结果落在输入框里, 不直接发 ──
  ///
  /// 识别一定会错, 而发出去就收不回来. 而且落在框里之后,
  /// 走的是**跟打字完全一样的那条路**: 同一个 /say、同一条账本,
  /// bot 完全不知道这句话是说出来的还是打出来的
  Asr? _asr;
  StreamSubscription<AsrText>? _asrSub;
  StreamSubscription<double>? _lvSub;
  bool _listening = false;

  /// 最近这几十毫秒的音量, 画成波形.
  ///
  /// ── 为什么非要有 ──
  ///
  /// 上一版说话时只有麦克风键变蓝, 别的什么都不动 —— 用户不知道
  /// 它到底听没听见。而**"它在听"这件事必须每时每刻都看得见**:
  /// 一个没反应的麦克风和一个坏掉的麦克风长得一模一样。
  ///
  /// 环形缓冲, 定长 —— 每帧挪数组在说话那几秒里是白烧的 CPU
  final _wave = List<double>.filled(48, 0);
  int _waveHead = 0;

  /// 波形/输入框那一小块的重建信号 —— 见上面两处为什么不用 setState
  final _waveTick = ValueNotifier(0);

  /// 页面刚打开时已经有多少条 —— 这些是历史, 不播出现动画.
  /// **进页面时记一次就不再变**: 用 msgs.length 实时算的话,
  /// 每来一条新的它就跟着涨, 于是永远没有"新来的"
  int _historyCount = -1;

  /// 开麦之前用户已经打了的字. 语音识别的内容接在它后面 ——
  /// 他可能先打了半句再改用说的
  String _settled = '';

  /// 用户自己往上翻的时候不许把他拽回底部 —— 那是所有聊天界面里
  /// 最招人烦的一件事, 而且它总发生在他正在读一段长回答的时候
  bool _pinned = true;

  @override
  void initState() {
    super.initState();
    widget.state.addListener(_onChange);
    _historyCount = widget.state.thread(widget.pid).length;
    // **不在这儿 setState**. 输入框每敲一个字都重建整页, 而这一页
    // 底下挂着整条消息列表 —— 打字的时候那股顿挫就是这么来的.
    // 现在只有输入条那一小块跟着 controller 重建, 见 build 里的
    // AnimatedBuilder
    _scroll.addListener(() {
      if (!_scroll.hasClients) return;
      // reverse:true 之后, **0 就是最底部**(最新那条) ——
      // 不是 maxScrollExtent
      _pinned = _scroll.position.pixels <= 80;
    });
  }

  /// 把输入框滚到最新那一行
  void _scrollInputToEnd() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_inputScroll.hasClients) {
        _inputScroll.jumpTo(_inputScroll.position.maxScrollExtent);
      }
    });
  }

  @override
  void dispose() {
    widget.state.removeListener(_onChange);
    _inputScroll.dispose();
    _asrSub?.cancel();
    _lvSub?.cancel();
    // **只停不销毁**: recognizer 建一次要一秒, 留着给下一个会话用
    _asr?.stop();
    _input.dispose();
    _scroll.dispose();
    _waveTick.dispose();
    super.dispose();
  }

  void _onChange() {
    if (!mounted) return;
    // 第一次收到数据时把历史条数定下来
    if (_historyCount < 0) {
      _historyCount = widget.state.thread(widget.pid).length;
    }
    if (!_pinned) return;
    // 新消息来了滚回底部. reverse 之下底部是 0 —— 而且这个值
    // **一开始就是准的**, 不像 maxScrollExtent 要等下面全量完
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_scroll.hasClients && _scroll.position.pixels > 0) {
        _scroll.animateTo(0, duration: Mo.fast, curve: Mo.curve);
      }
    });
  }

  /// 挑图.
  ///
  /// **在这边就把大小拦下来**: OS 侧一次请求 2MB 封顶, base64 还要
  /// 涨三分之一 —— 传上去再被 413 拒的话, 用户只看到"发送失败".
  /// 所以先按长边压到 1600, 还超的话当场说
  Future<void> _pickImage() async {
    try {
      final xs = await ImagePicker().pickMultiImage(
          maxWidth: 1600, maxHeight: 1600, imageQuality: 85);
      if (xs.isEmpty) return;
      final add = <Attach>[];
      var tooBig = 0;
      for (final x in xs) {
        final b = await x.readAsBytes();
        if (b.lengthInBytes > Attach.maxBytes) {
          tooBig++;
          continue;
        }
        add.add(Attach(x.name, Uint8List.fromList(b)));
      }
      if (!mounted) return;
      setState(() => _pending.addAll(add));
      if (tooBig > 0) _toast('$tooBig 张太大了，没加进来');
    } catch (e) {
      if (mounted) _toast('$e');
    }
  }

  /// 点一下开始听, 再点一下停
  Future<void> _toggleMic() async {
    if (_listening) {
      await _asr?.stop();
      await _asrSub?.cancel();
      await _lvSub?.cancel();
      _asrSub = null;
      _lvSub = null;
      if (mounted) setState(() => _listening = false);
      return;
    }
    try {
      // 第一次要建 recognizer(读 25MB 模型建图, 真机约 0.95s),
      // 之后是常驻的. 所以第一次点会顿一下, 后面都是立刻
      _asr ??= await Asr.instance();
    } catch (e) {
      if (mounted) _toast('语音识别起不来: $e');
      return;
    }
    final ok = await _asr!.start();
    if (!ok) {
      if (mounted) _toast('没有麦克风权限');
      return;
    }
    // 开始听的时候把输入框里已有的话记下来, 识别的字接在它后面 ——
    // 用户可能先打了半句再改用说的
    _settled = _input.text;
    _asrSub = _asr!.texts.listen((r) {
      if (!mounted) return;
      {
        // ── 每一条都是**这次说话的全文**, 界面不许自己拼 ──
        //
        // 拼这件事有状态(已经定稿了几段), 而状态放两处必然对不上:
        // 识别那边以为定稿了三段, 界面这边以为两段, 于是重复一段
        // 或者少一段. 全文由识别那边给, 界面只管显示.
        //
        // _settled 是**开麦之前用户已经打的字** —— 语音接在它后面,
        // 不是覆盖: 他可能先打了半句再改用说的
        _input.text = _settled.isEmpty ? r.text : '$_settled${r.text}';
        // 改 text 会自己通知 controller —— 输入条那块跟着重建就够了,
        // 不用惊动整页
        _input.selection =
            TextSelection.collapsed(offset: _input.text.length);
      }
      _scrollInputToEnd();
    });
    _lvSub = _asr!.levels.listen((v) {
      if (!mounted) return;
      // 音量一秒来几十次. setState 的话就是**一秒把整条消息列表
      // 重建几十遍** —— 说话时那股卡顿全在这儿.
      // 现在只捅一下这个 notifier, 只有波形那块重画
      _waveHead = (_waveHead + 1) % _wave.length;
      _wave[_waveHead] = v;
      _waveTick.value++;
    });
    if (mounted) setState(() => _listening = true);
  }

  void _toast(String m) => ScaffoldMessenger.of(context)
    ..hideCurrentSnackBar()
    ..showSnackBar(SnackBar(
      content: Text(m, style: NX.body.copyWith(color: Colors.white)),
      backgroundColor: NX.bad,
      behavior: SnackBarBehavior.floating,
    ));

  Future<void> _send() async {
    final t = _input.text.trim();
    if ((t.isEmpty && _pending.isEmpty) || _sending) return;
    setState(() => _sending = true);
    final imgs = List<Attach>.from(_pending);
    _input.clear();
    _pending.clear();
    // **发出去了就从头记** —— 输入框清空只清了界面那一半,
    // 识别那边攒的已定稿段落还在(见 Asr.resetText)
    _asr?.resetText();
    _settled = '';
    try {
      await widget.state.send(t, widget.pid, images: imgs);
      _pinned = true;
    } catch (err) {
      // ── 发失败了要两件事一起做 ──
      //
      // ① 把话还给他 —— 让他重打一遍是最糟的收场
      // ② **说一声**. 只把字还回去的话, 屏幕上看起来就是
      //    "点了发送但没反应", 而他不知道是没送到还是自己没点上
      _input.text = t;
      _pending.addAll(imgs);
      if (mounted) {
        ScaffoldMessenger.of(context)
          ..hideCurrentSnackBar()
          ..showSnackBar(SnackBar(
            content: Text('$err', style: NX.body.copyWith(color: Colors.white)),
            backgroundColor: NX.bad,
            behavior: SnackBarBehavior.floating,
          ));
      }
    } finally {
      if (mounted) setState(() => _sending = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final s = widget.state;
    return Scaffold(
      backgroundColor: NX.bg,
      body: ListenableBuilder(
        listenable: s,
        builder: (context, _) {
          final msgs = s.thread(widget.pid);
          final bot = s.bots.where((b) => b.pid == widget.pid).firstOrNull;
          final alive = bot?.state.alive ?? false;
          return Column(
            children: [
              _Head(state: s, bot: bot),
              Expanded(
                child: msgs.isEmpty
                    ? _Empty(state: s, bot: bot)
                    : ListView.builder(
                        controller: _scroll,
                        padding:
                            const EdgeInsets.only(top: NX.s2, bottom: NX.s3),
                        // ── 倒着建, 于是天然停在最底部 ──
                        //
                        // 上一版是正着建 + 进页面后 jumpTo(max)。那有两个
                        // 毛病: 消息高度是逐条量出来的, jumpTo 那一刻
                        // maxScrollExtent 还不是最终值(下面还有没量到的),
                        // 于是**停在半途**; 而且用户看得见"先在顶部,
                        // 然后闪一下跳到底"。
                        //
                        // reverse:true 让 ListView 从底部往上排 ——
                        // 打开就在最新那条, 一帧都不用跳。这也是微信/
                        // Telegram 的做法。
                        reverse: true,
                        itemCount: msgs.length,
                        // 倒着建, 所以下标要翻过来
                        itemBuilder: (_, ri) {
                          final i = msgs.length - 1 - ri;
                          return RiseIn(
                          // **只给新来的那几条播**. 一次补进来五十条历史,
                          // 五十个动画同时播是一片乱抖 —— 判据是
                          // "这一条是不是在这个页面打开之后才到的"
                          enabled: i >= _historyCount,
                          child: MsgRow(
                          msg: msgs[i],
                          name: bot?.name,
                          state: s,
                          // 隔了 5 分钟才摆一条时间分隔. 每条都摆的话
                          // 它比消息本身还密, 反而没人看
                          showStamp: i == 0 ||
                              msgs[i]
                                      .at
                                      .difference(msgs[i - 1].at)
                                      .inMinutes >=
                                  5,
                          ),
                          );
                        },
                      ),
              ),
              // 输入条单独跟着 controller 和音量走 —— **不牵动上面
              // 那条消息列表**
              AnimatedBuilder(
                animation: Listenable.merge([_input, _waveTick]),
                builder: (context, _) => _Composer(
                controller: _input,
                sending: _sending,
                enabled: alive,
                hint: alive ? '发消息' : '它已经不在了',
                onSend: _send,
                onPick: _pickImage,
                onMic: _toggleMic,
                listening: _listening,
                wave: _wave,
                waveHead: _waveHead,
                inputScroll: _inputScroll,
                pending: _pending,
                onDrop: (i) => setState(() => _pending.removeAt(i)),
                ),
              ),
            ],
          );
        },
      ),
    );
  }
}

/// 会话头 —— 返回 · 头像 · 名字 · 一行状态.
///
/// 状态那行说的是**此刻最要紧的一件事**, 不是把所有数字并排列出来:
/// 连不上的时候没人关心它在不在干活
class _Head extends StatelessWidget {
  const _Head({required this.state, required this.bot});
  final AppState state;
  final ProcInfo? bot;

  @override
  Widget build(BuildContext context) {
    final p = bot == null ? null : presenceOf(bot!.state);
    // ── "在线"不是这时候最该说的那句话 ──
    //
    // 你刚说完一句, 最想知道的是**它收到了没有、动起来没有**.
    // 而这一格原来一直写着"在线" —— 那句话在等回复的那十几秒里
    // 一个字的信息量都没有.
    //
    // 三档分开: 正在回复(字已经在出来了) / 正在思考(进程在跑, 还没
    // 开口 —— 可能在调工具) / 其它照旧
    final replying = bot != null && state.replying(bot!.pid);
    final (line, color) = switch (state.conn) {
      Conn.badToken => ('token 不对', NX.bad),
      Conn.error => ('重连中…', NX.warn),
      Conn.connecting => ('连接中…', NX.text3),
      _ when bot == null => ('这个 bot 不在了', NX.text4),
      _ when replying => ('正在回复', NX.accent),
      _ when bot!.state.busy => ('正在思考', NX.accent),
      _ => (p!.label, p.kind == Presence.busy ? NX.accent : NX.text3),
    };
    final live = replying || (bot?.state.busy ?? false);
    return Container(
      padding: EdgeInsets.only(top: MediaQuery.of(context).padding.top),
      decoration: BoxDecoration(
        color: NX.bg,
        border: Border(bottom: BorderSide(color: NX.line)),
      ),
      child: SizedBox(
        height: NX.headH,
        child: Row(
          children: [
            IconButton(
              onPressed: () => Navigator.of(context).pop(),
              icon: const Icon(Icons.arrow_back_ios_new_rounded, size: 20),
              color: NX.text2,
              splashRadius: 22,
            ),
            // **头像和名字整块可点** —— 点进去是它的资料页.
            // 这是所有 IM 的惯例, 而这儿尤其要有: 一个 bot 能干什么
            // 取决于它手上有哪几条能力, 而那件事没有别的地方能看
            Expanded(
              child: GestureDetector(
                behavior: HitTestBehavior.opaque,
                onTap: bot == null
                    ? null
                    : () => Navigator.of(context).push(SlideRoute(builder: (_) =>
                            BotPage(state: state, pid: bot!.pid))),
                child: Row(children: [
                  if (bot != null) ...[
                    Avatar(
                        id: bot!.pid,
                        size: NX.avatarSm,
                        presence: p!.kind,
                        ringColor: NX.bg),
                    const SizedBox(width: NX.s3),
                  ],
                  Expanded(
                    child: Column(
                      mainAxisAlignment: MainAxisAlignment.center,
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(bot?.name ?? '?',
                            style: NX.heading,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis),
                        Row(children: [
                          Flexible(
                            child: Text(line,
                                style: NX.caption.copyWith(color: color),
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis),
                          ),
                          // 三个点自己跳 —— **一个静止的"正在回复"跟一个
                          // 卡住的界面长得一模一样**, 而这一格存在的意义
                          // 正是回答"它到底动没动"
                          if (live) _Dots(color: color),
                        ]),
                      ],
                    ),
                  ),
                ]),
              ),
            ),
            // ── 语音模式 ──
            //
            //	**跟谁说话这件事在这一页是已知的** —— 它曾经是一个独立
            //	的 tab, 而那时候它把话发给了 bots.first(感知判断那个,
            //	它压根不回聊天), 于是用户对着屏幕说了半天什么都没发生.
            //
            //	不是输入框里那个"语音转文字": 那是换一种打字方式,
            //	这是**换一种谈话方式** —— 它会把回答念出来, 而且知道
            //	自己在被听, 所以说得短.
            if (bot != null && bot!.state.alive)
              IconButton(
                onPressed: () => Navigator.of(context).push(MaterialPageRoute(
                  fullscreenDialog: true,
                  builder: (_) => VoicePage(
                      state: state, pid: bot!.pid, name: bot!.name),
                )),
                icon: const Icon(Icons.graphic_eq_rounded, size: 21),
                color: NX.text3,
                splashRadius: 22,
                tooltip: '语音',
              ),
            // 静音 —— **在谁的会话里静音谁**, 才是它该在的地方.
            // 原来它在设置页的一个 bot 列表里, 而那个列表跟消息列表
            // 是同一份东西画了两遍
            if (bot != null && bot!.state.alive)
              IconButton(
                onPressed: () => state.toggleMute(bot!.pid),
                icon: Icon(
                    state.muted.contains(bot!.pid)
                        ? Icons.notifications_off_rounded
                        : Icons.notifications_none_rounded,
                    size: 21),
                color: state.muted.contains(bot!.pid) ? NX.warn : NX.text3,
                splashRadius: 22,
                tooltip: state.muted.contains(bot!.pid) ? '取消静音' : '静音',
              ),
            const SizedBox(width: NX.s1),
          ],
        ),
      ),
    );
  }
}

/// 输入条 —— **一行**: 一个胶囊输入框 + 一个发送键.
///
/// ── 上一版错在哪 ──
///
/// 输入框一行、按钮另起一行的输入壳适合桌面：那里有 + 号、@ 点名、
/// 附件等多个按钮，需要一条自己的行来装。
///
/// 手机上只有一个发送键, 却为它单占一行 —— 于是整条输入区高出一倍,
/// 而多出来的那一半是空的。
///
/// 手机上这块地方应保持一行：**胶囊输入框，发送键贴在右边**。
/// 这是一个不需要学习就能使用的常见形状。
class _Composer extends StatelessWidget {
  const _Composer({
    required this.controller,
    required this.sending,
    required this.enabled,
    required this.hint,
    required this.onSend,
    required this.onPick,
    required this.onMic,
    required this.listening,
    required this.wave,
    required this.waveHead,
    required this.pending,
    required this.onDrop,
    required this.inputScroll,
  });

  final TextEditingController controller;
  final bool sending, enabled, listening;
  final String hint;
  final VoidCallback onSend, onPick, onMic;
  final List<double> wave;
  final int waveHead;
  final ScrollController inputScroll;
  final List<Attach> pending;
  final ValueChanged<int> onDrop;

  @override
  Widget build(BuildContext context) {
    final canSend = enabled &&
        !sending &&
        (controller.text.trim().isNotEmpty || pending.isNotEmpty);
    return Container(
      padding: EdgeInsets.fromLTRB(NX.s3, NX.s2, NX.s3,
          MediaQuery.of(context).padding.bottom + NX.s2),
      decoration: BoxDecoration(
        color: NX.bg,
        border: Border(top: BorderSide(color: NX.line)),
      ),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          // 说话时上面浮一条波形 —— 它是"它在听"唯一每时每刻都在动
          // 的东西。**放在输入条上面不放里面**: 放里面的话它跟正在
          // 长出来的字抢同一块地方, 而那几个字才是用户真正在看的
          // 波形出现和消失都要过渡: 硬出现的话输入条会整体往上跳一下
          AnimatedSize(
            duration: Mo.base,
            curve: Mo.curve,
            child: listening
                ? Padding(
                    padding: const EdgeInsets.only(bottom: NX.s2),
                    child: SizedBox(
                      height: 22,
                      child: CustomPaint(
                        painter: _WavePainter(wave, waveHead, NX.accent),
                        size: Size.infinite,
                      ),
                    ),
                  )
                : const SizedBox(width: double.infinity),
          ),
          // 待发的图摆在输入条**上面** —— 摆在里面的话输入框会被
          // 挤成一条缝, 而图本来就该比那行字显眼
          if (pending.isNotEmpty)
            SizedBox(
              height: 72,
              child: ListView.separated(
                scrollDirection: Axis.horizontal,
                padding: const EdgeInsets.only(
                    left: NX.s1, right: NX.s1, bottom: NX.s2),
                itemCount: pending.length,
                separatorBuilder: (_, __) => const SizedBox(width: NX.s2),
                itemBuilder: (_, i) => _Thumb(
                    bytes: pending[i].bytes, onDrop: () => onDrop(i)),
              ),
            ),
          Row(
            // 多行的时候两颗键要贴着**底**, 不是浮在中线上
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              // 加号 —— 附件.
              // **在左边**: 所有 IM 都是左边加东西、右边发出去,
              // 反过来的话每次都要找一下
              _RoundBtn(
                icon: Icons.add_rounded,
                onTap: enabled && !sending ? onPick : null,
              ),
              const SizedBox(width: NX.s2),
              Expanded(
                child: Container(
                  // **跟两颗圆钮同高**. 上一版这儿的高度是"内边距 + 行高"
                  // 算出来的(46), 而圆钮是写死的 38 —— 于是三块东西
                  // 一排摆着, 中间那块高出一截.
                  //
                  // 现在三块共用 NX.btn: 高度由一个值管, 想不一样高
                  // 都难
                  constraints: const BoxConstraints(minHeight: NX.btn),
                  padding: const EdgeInsets.symmetric(horizontal: NX.s4),
                  alignment: Alignment.centerLeft,
                  decoration: BoxDecoration(
                    // ── 无边框 ──
                    //
                    // 上一版给胶囊描了一圈 line2。**有了底色就不该再有边**:
                    // 一个东西同时用"填充"和"描边"两种手段说自己在哪儿,
                    // 说了两遍 —— 而屏幕上多出来的那圈线是这一条里
                    // 最脏的东西, 尤其它旁边两颗圆钮是没有边的.
                    //
                    // 底色本身够不够看得见? 够: fill 是墨色 6%,
                    // 在两套主题下都比背景高一档
                    color: NX.isDark ? NX.bgSubtle : NX.fill,
                    // 胶囊: 单行时圆头, 长到多行就是大圆角 ——
                    // 一直用 pill 的话三行文字外面套个药丸很怪
                    borderRadius: BorderRadius.circular(NX.rLg),
                  ),
                  child: TextField(
                    controller: controller,
                    scrollController: inputScroll,
                    enabled: enabled && !sending,
                    style: NX.body,
                    minLines: 1,
                    // **在听的时候封到 3 行**: 语音会一直往里灌,
                    // 5 行的话输入条会长到占掉半屏, 把上面的对话挤没。
                    // 打字时 5 行没问题 —— 人不会一口气打五行
                    maxLines: listening ? 3 : 5,
                    keyboardAppearance:
                        NX.isDark ? Brightness.dark : Brightness.light,
                    decoration: InputDecoration(
                      hintText: hint,
                      hintStyle: NX.body.copyWith(color: NX.text3),
                      border: InputBorder.none,
                      isDense: true,
                      // 8 上下 + 16×1.5 的行高 = 正好 40, 跟圆钮同高.
                      // 这个数不能拍脑袋: 大了胶囊会顶出去, 小了
                      // 单行文字在胶囊里偏上
                      contentPadding:
                          const EdgeInsets.symmetric(vertical: NX.s2),
                    ),
                  ),
                ),
              ),
              const SizedBox(width: NX.s2),
              // ── 在听的时候, 停止键必须一直在 ──
              //
              // 上一版是"麦克风和发送占同一个位置, 谁该出现谁出现",
              // 理由写的是"这两个动作互斥"。**那句判断是错的**:
              // 说话的时候它们同时需要 —— 一旦识别出第一个字,
              // canSend 变 true, 停止键当场被发送键顶掉,
              // 于是**再也停不下来**, 只能等它自己断句或者退出页面。
              //
              // 因此在听 = 两颗都在(停止在左),
              // 没在听 = 一颗(有字发送, 没字麦克风)。
              if (listening) ...[
                _RoundBtn(
                  icon: Icons.stop_rounded,
                  filled: true,
                  fillColor: NX.bad,
                  onTap: onMic,
                ),
                const SizedBox(width: NX.s2),
              ],
              _RoundBtn(
                icon: (canSend || sending || listening)
                    ? Icons.arrow_upward_rounded
                    : Icons.mic_none_rounded,
                filled: canSend,
                busy: sending,
                onTap: sending
                    ? null
                    : (canSend
                        ? onSend
                        : (listening ? null : (enabled ? onMic : null))),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _RoundBtn extends StatelessWidget {
  const _RoundBtn({
    required this.icon,
    this.onTap,
    this.filled = false,
    this.busy = false,
    this.fillColor,
  });

  final IconData icon;
  final VoidCallback? onTap;
  final bool filled, busy;

  /// fillColor 停止键用红 —— 它跟发送是两种性质的动作,
  /// 同色的话一排两颗蓝钮分不出哪颗是哪颗
  final Color? fillColor;

  @override
  Widget build(BuildContext context) => Pressable(
        onTap: onTap,
        child: AnimatedContainer(
          // 底色和图标都渐变: 打第一个字的时候这颗键从灰变蓝,
          // 硬切的话像闪了一下
          duration: Mo.fast,
          curve: Mo.curve,
          width: NX.btn,
          height: NX.btn,
          decoration: BoxDecoration(
            color: filled ? (fillColor ?? NX.accent) : NX.fill,
            shape: BoxShape.circle,
          ),
          child: busy
              ? Padding(
                  padding: const EdgeInsets.all(NX.s3),
                  child: CircularProgressIndicator(
                      strokeWidth: 1.8, color: NX.text3),
                )
              // 图标换的时候转着换(麦克风 ⇄ 发送 ⇄ 停止):
              // 硬换会让人以为是两颗不同的键
              : AnimatedSwitcher(
                  duration: Mo.fast,
                  transitionBuilder: (child, a) => ScaleTransition(
                    scale: a,
                    child: FadeTransition(opacity: a, child: child),
                  ),
                  child: Icon(icon,
                      key: ValueKey(icon.codePoint),
                      size: 20,
                      color: filled
                          ? Colors.white
                          : (onTap == null ? NX.text4 : NX.text2)),
                ),
        ),
      );
}

/// 待发的一张图.
///
/// **叉子压在角上而不是另起一行**: 一排缩略图下面再来一排删除按钮,
/// 占掉的高度比图本身还多
class _Thumb extends StatelessWidget {
  const _Thumb({required this.bytes, required this.onDrop});
  final Uint8List bytes;
  final VoidCallback onDrop;

  @override
  Widget build(BuildContext context) => Stack(
        clipBehavior: Clip.none,
        children: [
          ClipRRect(
            borderRadius: BorderRadius.circular(NX.rSm + 2),
            child: Image.memory(bytes,
                width: 56, height: 56, fit: BoxFit.cover),
          ),
          Positioned(
            right: -6,
            top: -6,
            child: GestureDetector(
              onTap: onDrop,
              child: Container(
                width: 22,
                height: 22,
                decoration: BoxDecoration(
                  color: NX.bg,
                  shape: BoxShape.circle,
                  border: Border.all(color: NX.line2),
                ),
                child: Icon(Icons.close_rounded, size: 14, color: NX.text2),
              ),
            ),
          ),
        ],
      );
}

/// 空屏 —— **不画"暂无消息"**. 那句话什么都没说
class _Empty extends StatelessWidget {
  const _Empty({required this.state, required this.bot});
  final AppState state;
  final ProcInfo? bot;

  @override
  Widget build(BuildContext context) => switch (state.conn) {
        Conn.badToken => const EmptyState(
            art: 'assets/empty_offline.png',
            line: 'token 不对',
            hint: '去设置页改一下'),
        Conn.error => EmptyState(
            art: 'assets/empty_offline.png',
            line: '连不上',
            hint: state.lastError ?? ''),
        _ when bot == null => const EmptyState(
            art: 'assets/empty_nobots.png',
            line: '这个 bot 已经不在了',
            hint: '它说过的话还在账本里'),
        _ when !bot!.state.alive => const EmptyState(
            art: 'assets/empty_nobots.png',
            line: '它退出了',
            hint: '要用得先在电脑上把它拉起来'),
        _ => const EmptyState(
            art: 'assets/empty_chat.png',
            line: '还没说过话',
            hint: '说句话试试'),
      };
}


/// 说话时那条波形.
///
/// 一排竖条, 最新的在**右边** —— 跟文字一样从左往右长, 眼睛不用换方向.
/// 静音时留一小截而不是归零: 全平的一条线跟"麦克风坏了"长得一样
class _WavePainter extends CustomPainter {
  _WavePainter(this.wave, this.head, this.color);
  final List<double> wave;
  final int head;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final n = wave.length;
    final gap = size.width / n;
    final w = (gap * .5).clamp(1.5, 3.0);
    final mid = size.height / 2;
    final p = Paint()
      ..strokeCap = StrokeCap.round
      ..strokeWidth = w
      ..isAntiAlias = true;
    for (var i = 0; i < n; i++) {
      // 从 head 往回读, 画在最右边 —— 最新的在右
      final v = wave[(head - (n - 1 - i) + n * 2) % n];
      final h = (2 + v * (size.height - 4)) / 2;
      final x = gap * (i + .5);
      // 越新越实: 老的那几条淡下去, 波形才有"在流动"的样子
      p.color = color.withValues(alpha: .25 + (i / n) * .75);
      canvas.drawLine(Offset(x, mid - h), Offset(x, mid + h), p);
    }
  }

  @override
  bool shouldRepaint(_WavePainter old) => true;
}

/// 三个跳动的点 —— 接在"正在回复"后面.
///
/// **不用 AnimatedOpacity 之类**: 这东西一秒重画几次, 挂在页面的
/// setState 上就又回到了"每秒重建整页"那条老路. 自带 Ticker,
/// 只重画自己这一小块
class _Dots extends StatefulWidget {
  const _Dots({required this.color});
  final Color color;

  @override
  State<_Dots> createState() => _DotsState();
}

class _DotsState extends State<_Dots> with SingleTickerProviderStateMixin {
  late final _c = AnimationController(
      vsync: this, duration: const Duration(milliseconds: 1200))
    ..repeat();

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
        animation: _c,
        builder: (_, __) => Row(mainAxisSize: MainAxisSize.min, children: [
          for (var i = 0; i < 3; i++) ...[
            const SizedBox(width: 3),
            Opacity(
              // 三个点错开三分之一周期 —— 同相位的话它们一起明一起暗,
              // 看着像整块在闪, 而不是在"走"
              opacity: .25 +
                  .75 *
                      (0.5 +
                          0.5 *
                              math.sin((_c.value - i / 3) * 2 * math.pi)),
              child: Container(
                width: 3,
                height: 3,
                decoration: BoxDecoration(
                    color: widget.color, shape: BoxShape.circle),
              ),
            ),
          ],
        ]),
      );
}
