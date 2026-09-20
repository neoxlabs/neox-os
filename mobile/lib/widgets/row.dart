import 'package:flutter/material.dart';

import '../state/app_state.dart';
import '../theme.dart';
import 'card_view.dart';
import 'rich.dart';
import 'avatar.dart';

/// 时间线里的一行 —— **照客户端 .row 那套**.
///
/// ── 最要紧的一条: bot 说的话不是气泡 ──
///
/// 客户端里只有**用户自己那条**是气泡(右对齐、fill-ghost-2 的底、
/// radius 16、最宽 78%), bot 说的话是"头像 + 名字 + 纯文本", 左对齐,
/// 没有底色.
///
/// 这不是审美偏好: 一屏里 bot 说的话占九成, 全套上气泡的话整屏都是框,
/// 而**长回答在气泡里读起来最累** —— 行宽被气泡钳住, 眼睛要在一个
/// 窄条里来回跳. 用户那条短, 套气泡正好用来把"我说的"跟"它说的"分开.
class MsgRow extends StatelessWidget {
  const MsgRow({
    super.key,
    required this.msg,
    this.showStamp = false,
    this.name,
    this.state,
  });

  final Msg msg;

  /// name 这个 bot **此刻**叫什么.
  ///
  /// **不用消息里存的那个**: 名字是进程表的属性, 而消息可能比进程表
  /// 先到(重连时事件流是流式的, /processes 是一次请求) —— 那一瞬间
  /// 存进去的是 pid, 之后就再也不会变了.
  /// 重连时事件流可能先于 /processes 返回, 如果此时保存消息里的 pid,
  /// 满屏的名字会变成 p68046.1.1, 后续也不会更新.
  final String? name;

  /// showStamp 这一行上面要不要摆一条时间分隔
  final bool showStamp;

  /// state 卡片要它 —— 地图缩略图走 OS 代取那条路(/mapshot),
  /// 而地址和 token 在它手上
  final AppState? state;

  @override
  Widget build(BuildContext context) {
    // 卡片走自己那条路 —— 它是"一件东西", 不是一句话
    final body = msg.card != null && state != null
        ? _cardRow(context)
        : switch (msg.kind) {
      MsgKind.user => _mine(context),
      MsgKind.system => _note(context),
      MsgKind.proactive => _proactive(context),
      MsgKind.relay => _theirs(context, dim: true),
      MsgKind.bot => _theirs(context),
    };
    if (!showStamp) return body;
    return Column(children: [
      // 时间分隔: 一行小字, 居中, 不抢任何东西
      Padding(
        padding: const EdgeInsets.fromLTRB(0, NX.s5, 0, NX.s3),
        child: Text(_stamp(msg.at), style: NX.captionDim),
      ),
      body,
    ]);
  }

  /// 一张卡片 —— 跟"它说的"同一个排法(头像 + 名字), 只是正文换成卡片
  Widget _cardRow(BuildContext context) => Padding(
        padding: const EdgeInsets.fromLTRB(NX.s4, NX.s1, NX.s6, NX.s1),
        child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Padding(
            padding: const EdgeInsets.only(top: 2),
            child: Avatar(id: msg.pid, size: NX.avatarSm),
          ),
          const SizedBox(width: NX.s3),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(name ?? msg.who, style: NX.speaker),
              const SizedBox(height: NX.s1),
              CardView(card: msg.card!, state: state!),
            ]),
          ),
        ]),
      );

  /// 它说的 —— 头像 · 名字 · 正文. 没有气泡
  Widget _theirs(BuildContext context, {bool dim = false}) => Padding(
        padding: const EdgeInsets.fromLTRB(NX.s4, NX.s1, NX.s6, NX.s1),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // 头像中线对齐**首行文字**的中线 —— 两者相差 3px,
            // 用负 margin 提上去而不是改 padding(padding 会把行高一起改了)
            Padding(
              padding: const EdgeInsets.only(top: 2),
              child: Avatar(id: msg.pid, size: NX.avatarSm),
            ),
            const SizedBox(width: NX.s3),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(dim ? '${name ?? msg.who} 交办' : (name ?? msg.who),
                      style: dim ? NX.section : NX.speaker),
                  const SizedBox(height: NX.s1),
                  SelectableText.rich(TextSpan(children: [
                    // **粗体和行内代码要真的画出来** —— 不画的话
                    // 模型写的 **重点** 原样显示成一串星号,
                    // 而用户看到的是"渲染不完整"
                    ...richSpans(msg.text, dim ? NX.bodyDim : NX.body),
                    // 还在长的那条尾巴上挂一根竖条
                    if (msg.streaming)
                      WidgetSpan(
                          alignment: PlaceholderAlignment.middle,
                          child: _Caret()),
                  ])),
                ],
              ),
            ),
          ],
        ),
      );

  /// 我说的 —— 右对齐气泡, 最宽 78%
  Widget _mine(BuildContext context) => Padding(
        padding: const EdgeInsets.fromLTRB(NX.s7, NX.s1, NX.s4, NX.s1),
        child: Row(mainAxisAlignment: MainAxisAlignment.end, children: [
          // 还没被账本确认的那条, 气泡左边一枚小钟.
          // **不用把气泡整个调灰**: 那看着像"这条出错了", 而它只是
          // 在路上. 一枚小钟说的正好是"在发" —— 而且它一进账本就没了
          if (msg.sending) ...[
            Icon(Icons.schedule_rounded, size: 13, color: NX.text4),
            const SizedBox(width: NX.s2),
          ],
          ConstrainedBox(
            constraints: BoxConstraints(
                maxWidth: MediaQuery.of(context).size.width * .78),
            child: Container(
              padding: const EdgeInsets.symmetric(horizontal: NX.s4, vertical: NX.s3),
              decoration: BoxDecoration(
                color: NX.fill2,
                borderRadius: BorderRadius.circular(NX.rLg),
              ),
              child: SelectableText.rich(
                  TextSpan(children: richSpans(msg.text, NX.body))),
            ),
          ),
        ]),
      );

  /// 主动 —— 没有人问, 它自己开的口.
  ///
  /// **这是屋里唯一一种带底色的 bot 消息**. 它必须跟上面那条长得不一样:
  /// 画成普通回答的话, "它主动跟我说了一句"就淹在对话里, 而那恰恰是
  /// 这整个东西存在的理由.
  ///
  /// 理由要跟着一起显示: 一次打扰值不值得, 只有看见它凭哪一条判据说的
  /// 才判得出来; 判不出来的话, 用户唯一能做的就是把整个通道关掉.
  Widget _proactive(BuildContext context) => Padding(
        padding: const EdgeInsets.fromLTRB(NX.s4, NX.s2, NX.s6, NX.s2),
        child: Container(
          padding: const EdgeInsets.all(NX.s4),
          decoration: BoxDecoration(
            color: NX.accent.withValues(alpha: NX.isDark ? .10 : .07),
            borderRadius: BorderRadius.circular(NX.rMd),
            border: Border.all(color: NX.accent.withValues(alpha: .25)),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(children: [
                Icon(Icons.bolt_rounded, size: 15, color: NX.accent),
                const SizedBox(width: NX.s1),
                Text(msg.who,
                    style: NX.speaker.copyWith(
                        color: NX.accent, fontWeight: NX.wBold)),
              ]),
              const SizedBox(height: NX.s2),
              SelectableText(msg.text, style: NX.body),
              if (msg.why != null && msg.why!.isNotEmpty) ...[
                const SizedBox(height: NX.s2),
                Text(msg.why!, style: NX.captionDim),
              ],
            ],
          ),
        ),
      );

  /// 系统自己的一行 —— 小、灰、居中. 它不是对话的一部分
  Widget _note(BuildContext context) => Padding(
        padding: const EdgeInsets.symmetric(
            horizontal: NX.gutter, vertical: NX.s2),
        child: Center(
          // 出事的那一行(一轮没走完)用警示色、字重一档 —— 跟"连上了"那种
          // 灰字长得一样的话, 他扫一眼会以为是一行无关紧要的状态
          child: Text(msg.text,
              style: msg.alert
                  ? NX.caption.copyWith(color: NX.warn)
                  : NX.captionDim,
              textAlign: TextAlign.center),
        ),
      );

  static String _stamp(DateTime t) {
    final now = DateTime.now();
    final sameDay =
        t.year == now.year && t.month == now.month && t.day == now.day;
    final hm =
        '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}';
    if (sameDay) return hm;
    return '${t.month}月${t.day}日 $hm';
  }
}

/// 还在长 —— 一根闪的竖条. 客户端是 2×14px 的 text-3
class _Caret extends StatefulWidget {
  @override
  State<_Caret> createState() => _CaretState();
}

class _CaretState extends State<_Caret> with SingleTickerProviderStateMixin {
  late final _c = AnimationController(
      vsync: this, duration: const Duration(milliseconds: 1100))
    ..repeat();

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.only(left: NX.s1),
        child: FadeTransition(
          // steps(2): 硬闪, 不是渐变 —— 渐变的光标看着像在呼吸,
          // 而它要表达的是"这里还没写完"
          opacity: _c.drive(CurveTween(curve: const _Steps(2))),
          child: Container(width: 2, height: 16, color: NX.text3),
        ),
      );
}

class _Steps extends Curve {
  const _Steps(this.n);
  final int n;
  @override
  double transformInternal(double t) => (t * n).floor() % 2 == 0 ? 1 : 0;
}
