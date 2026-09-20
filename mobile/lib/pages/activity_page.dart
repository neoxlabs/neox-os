import 'package:flutter/material.dart';

import '../api/events.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/panel.dart';

/// 它干过什么 —— **跟"它说过什么"是两件事**.
///
/// ── 为什么必须单独有这一页 ──
///
/// 会话页只画 say(说给人听的那句), 这是对的: 一次干活能产生几十条
/// 工具流水, 摊进对话里真正说给人听的那句会被淹掉.
///
/// 但那些流水**必须有个地方能看**. 出了事(它说干完了而实际没有、
/// 它说做不到而你不知道为什么)只有这里能对上号:
///
///	调了哪个工具
///	用了哪条能力, **被哪条能力拦了** ← 通常这条就是答案
///	烧了多少 token
///	状态怎么变的
///
/// 只留最近 200 条. 更老的在 OS 的事件账本里 —— 那份才是完整的,
/// 而且是只增不删的.
class ActivityPage extends StatefulWidget {
  const ActivityPage({super.key, required this.state, required this.pid});
  final AppState state;
  final ProcessID pid;

  @override
  State<ActivityPage> createState() => _ActivityPageState();
}

class _ActivityPageState extends State<ActivityPage> {
  /// 只看被拦下来的那几条 —— 查"为什么没做成"时唯一要看的
  bool _onlyDenied = false;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: NX.bg,
      body: ListenableBuilder(
        listenable: widget.state,
        builder: (context, _) {
          final all = widget.state.activity(widget.pid);
          final denied = all.where((a) => a.kind == ActKind.denied).length;
          final rows = _onlyDenied
              ? all.where((a) => a.kind == ActKind.denied).toList()
              : all;
          return Column(children: [
            PageBar(
              title: '它干过什么',
              // 有被拦的就给一个开关. **没有的时候不给** ——
              // 一个永远筛不出东西的筛子是纯噪音
              action: denied == 0
                  ? null
                  : TextButton(
                      onPressed: () =>
                          setState(() => _onlyDenied = !_onlyDenied),
                      child: Text(
                        _onlyDenied ? '看全部' : '只看被拦的 $denied',
                        style: NX.label.copyWith(
                            color: _onlyDenied ? NX.accent : NX.warn),
                      ),
                    ),
            ),
            Expanded(
              child: rows.isEmpty
                  ? Center(
                      child: Padding(
                        padding: const EdgeInsets.all(NX.s7),
                        child: Text(
                          all.isEmpty
                              ? '还没干过什么\n\n这一页记的是它干活的痕迹：'
                                  '调了哪个工具、用了哪条能力、烧了多少。'
                                  '只从这次连上之后开始记。'
                              : '这一段里没有被拦下来的',
                          style: NX.bodyDim,
                          textAlign: TextAlign.center,
                        ),
                      ),
                    )
                  : ListView.builder(
                      padding: const EdgeInsets.only(bottom: NX.s7),
                      // **倒着列**: 最新的在最上面. 活动是用来查
                      // "刚才发生了什么"的, 不是从头读的
                      reverse: false,
                      itemCount: rows.length,
                      itemBuilder: (_, i) => _Line(act: rows[rows.length - 1 - i]),
                    ),
            ),
          ]);
        },
      ),
    );
  }
}

class _Line extends StatelessWidget {
  const _Line({required this.act});
  final Act act;

  @override
  Widget build(BuildContext context) {
    final (icon, color) = switch (act.kind) {
      ActKind.tool => (Icons.build_rounded, NX.text2),
      ActKind.think => (Icons.psychology_outlined, NX.text4),
      ActKind.cap => (Icons.key_rounded, NX.text3),
      // 被拦的用警告色 —— 它是这一页里最该被看见的一类
      ActKind.denied => (Icons.block_rounded, NX.warn),
      ActKind.spend => (Icons.local_fire_department_rounded, NX.text3),
      ActKind.done => (Icons.check_circle_rounded, NX.ok),
      ActKind.failed => (Icons.error_rounded, NX.bad),
      ActKind.state => (Icons.circle_outlined, NX.text4),
    };
    return Padding(
      padding: const EdgeInsets.symmetric(
          horizontal: NX.gutter, vertical: NX.s2),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: 2),
            child: Icon(icon, size: 17, color: color),
          ),
          const SizedBox(width: NX.s3),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(children: [
                  Expanded(
                    child: Text(act.what,
                        style: NX.label.copyWith(
                            color: act.kind == ActKind.denied
                                ? NX.warn
                                : NX.text2)),
                  ),
                  Text(_hm(act.at), style: NX.caption.copyWith(color: NX.text4)),
                ]),
                if (act.detail.isNotEmpty) ...[
                  const SizedBox(height: 2),
                  // 细节用等宽: 这里全是路径、能力轴、数字 ——
                  // 等宽字下这些对齐得起来
                  Text(act.detail,
                      style: NX.caption.copyWith(
                          color: NX.text3, fontFamily: 'monospace'),
                      maxLines: 3,
                      overflow: TextOverflow.ellipsis),
                ],
              ],
            ),
          ),
        ],
      ),
    );
  }

  static String _hm(DateTime t) =>
      '${t.hour.toString().padLeft(2, '0')}:${t.minute.toString().padLeft(2, '0')}:'
      '${t.second.toString().padLeft(2, '0')}';
}
