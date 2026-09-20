import 'package:flutter/material.dart';

import '../api/events.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/avatar.dart';
import '../widgets/empty_state.dart';
import '../widgets/panel.dart';

/// 它主动跟你说过什么 —— 闹钟 / 判断 / 日报, 都在这儿.
///
/// ── 为什么必须单独有这一页 ──
///
/// 主动消息本来埋在各自 bot 的会话里。你想回头看"今天它都提醒过我
/// 什么", 得一个个点进去翻 —— 而这类消息**恰恰是最需要一个总览的**:
/// 它们不是你问出来的, 你不知道该去哪儿找。
///
/// ── 三条设计 ──
///
///	**why 一定要显示**: 一次打扰值不值得, 只有看见它凭哪条判据说的
///	才判得出来 —— 判不出来的话你唯一能做的就是把整个通道关掉。
///
///	**主角是那句话**: 一条提醒里你真正要读的是正文, 不是"提醒"两个字。
///	所以正文占第一行, 谁说的、什么时候说的、凭什么说的一律收进第二行
///	的一句注脚 —— 一屏扫过去先读到的得是内容, 不是一列"提醒提醒提醒"。
///
///	**竖线留给真正的例外**: 见 [_Item] 里那段。
class InboxPage extends StatefulWidget {
  const InboxPage({super.key, required this.state});
  final AppState state;

  @override
  State<InboxPage> createState() => _InboxPageState();
}

class _InboxPageState extends State<InboxPage> {
  @override
  void initState() {
    super.initState();
    // 进来就算看过了 —— 这一页存在的意义就是"看一眼"
    WidgetsBinding.instance.addPostFrameCallback((_) {
      widget.state.markDeliveriesSeen();
    });
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: NX.bg,
      body: ListenableBuilder(
        listenable: widget.state,
        builder: (context, _) {
          final list = widget.state.deliveries;
          return Column(children: [
            const PageBar(title: '它跟我说过什么'),
            Expanded(
              child: list.isEmpty
                  ? const EmptyState(
                      art: 'assets/empty_inbox.png',
                      line: '它还没主动找过你',
                      hint: '闹钟到点、它判断该告诉你的事、每天的日报，'
                          '都会出现在这儿',
                    )
                  : ListView(
                      padding: const EdgeInsets.fromLTRB(
                          NX.gutter, NX.s3, NX.gutter, NX.s7),
                      children: [
                        for (final day in _days(list))
                          Sec(
                            label: day.label,
                            children: [
                              for (final d in day.items)
                                _Item(d: d, state: widget.state),
                            ],
                          ),
                      ],
                    ),
            ),
          ]);
        },
      ),
    );
  }

  /// 按天切开. 一整列消息不分段的话, 你没法回答"这条是今天的还是
  /// 昨天的" —— 而每条右边那个 HH:mm 单独看是**答不了这个问题的**.
  ///
  /// 每天一张面板, 用的是设置页那套 [Sec]/分隔线 —— 这个 App 里
  /// "一组东西"就长这样, 这一页没有理由自己发明一个
  static List<_Day> _days(List<Delivery> list) {
    final out = <_Day>[];
    for (final d in list) {
      final label = _dayLabel(d.at);
      if (out.isEmpty || out.last.label != label) out.add(_Day(label));
      out.last.items.add(d);
    }
    return out;
  }
}

class _Day {
  _Day(this.label);
  final String label;
  final items = <Delivery>[];
}

class _Item extends StatelessWidget {
  const _Item({required this.d, required this.state});
  final Delivery d;
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final (icon, label) = switch (d.kind) {
      DeliveryKind.remind => (Icons.alarm_rounded, '提醒'),
      DeliveryKind.proactive => (Icons.bolt_rounded, '主动'),
      DeliveryKind.daily => (Icons.wb_sunny_rounded, '日报'),
      DeliveryKind.needsYou => (Icons.pan_tool_rounded, '等你拍板'),
    };

    // ── 标记只给"它自己决定要打断你"的那条 ──
    //
    // 第一版是 `d.urgent`, 结果满屏每张卡都挂着蓝竖线 —— 因为
    // 闹钟一律 urgent(你自己定的点, 当然要吵醒你)。**人人都有的
    // 标记等于没有标记**: 它本来是"这条跟别的不一样"的意思,
    // 而这里恰恰是"这条跟别的一样"。
    //
    // 真正的例外是**你没要求、它自己判断现在就得说**的那种,
    // 给一颗小点 + 注脚变色, 不给整行加边框
    final exceptional = d.urgent && d.kind != DeliveryKind.remind;

    // 头像按 **pid** 生成, 不是按名字 —— 见 [AppState.pidOfName].
    // 主动消息带的是给人看的名字, 拿它当种子的话同一个 bot 在这页
    // 和消息列表里是两张不同的脸
    final pid = state.pidOfName(d.from);

    return Padding(
      padding: const EdgeInsets.fromLTRB(NX.s4, NX.s3, NX.s4, NX.s3),
      child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
        // ── 左边一列永远是同一个形状 ──
        //
        // 有 bot 的画脸, 没 bot 的(感知层判出来的、或者名字对不上进程表
        // 的老消息)画一枚同样大小的圆底图标. 原来那版是一个光秃秃的闹钟
        // 字形, 跟旁边圆头像**连轮廓都对不齐**, 一列下来是参差的
        SizedBox(
          width: 32,
          height: 32,
          child: pid == null
              ? DecoratedBox(
                  decoration:
                      BoxDecoration(color: NX.fill2, shape: BoxShape.circle),
                  child: Icon(icon, size: 16, color: NX.text3),
                )
              : Avatar(id: pid, size: 32),
        ),
        const SizedBox(width: NX.s3),
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            // ── 正文在第一行 ──
            //
            // 上一版第一行是"值守 · 提醒", 正文排在下面 —— 于是一屏扫过去
            // 你先读到的全是"提醒提醒提醒", 而**你要读的是那句话**.
            // 谁说的、什么时候说的是注脚, 降到第二行
            Row(crossAxisAlignment: CrossAxisAlignment.baseline,
                textBaseline: TextBaseline.alphabetic, children: [
              Expanded(child: Text(d.text, style: NX.body)),
              const SizedBox(width: NX.s3),
              Text(_hm(d.at), style: NX.caption.copyWith(color: NX.text4)),
            ]),
            const SizedBox(height: 3),
            Row(children: [
              if (exceptional) ...[
                Container(
                  width: 5,
                  height: 5,
                  decoration:
                      BoxDecoration(color: NX.warn, shape: BoxShape.circle),
                ),
                const SizedBox(width: 5),
              ],
              Flexible(
                child: Text(
                  [
                    if (d.from.isNotEmpty) d.from,
                    label,
                    if (d.why.isNotEmpty) d.why,
                  ].join(' · '),
                  style: NX.caption.copyWith(
                      color: exceptional ? NX.warn : NX.text4),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                ),
              ),
            ]),
          ]),
        ),
      ]),
    );
  }

  static String _hm(DateTime t) =>
      '${t.hour.toString().padLeft(2, '0')}:'
      '${t.minute.toString().padLeft(2, '0')}';
}

String _dayLabel(DateTime t) {
  final now = DateTime.now();
  final d0 = DateTime(now.year, now.month, now.day);
  final d = DateTime(t.year, t.month, t.day);
  final diff = d0.difference(d).inDays;
  if (diff == 0) return '今天';
  if (diff == 1) return '昨天';
  if (t.year == now.year) return '${t.month} 月 ${t.day} 日';
  return '${t.year} 年 ${t.month} 月 ${t.day} 日';
}
