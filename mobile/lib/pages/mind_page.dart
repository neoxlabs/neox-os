import 'package:flutter/material.dart';

import '../api/os_client.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/panel.dart';

/// 它在管你什么 —— **一眼看得到的那一页**。
///
/// ── 为什么非做不可 ──
///
/// 用户的原话：「我每天总会有个待办事项，我一眼能看到我现在有哪些
/// 东西，但在界面上我根本找不到这些东西。」
///
/// 他说得对，而且比他想的更糟：**提醒和盯着的事连 HTTP 路由都没有** ——
/// 手机上看不到、桌面上看不到，连 bot 自己都只能去读一个文本文件，
/// 然后回一句「撤不掉，系统没给我可撤的编号」。
///
/// 一个你看不见的助理，你没法信任它；而信不过的东西，你会绕过它自己
/// 记一遍——那时候它就白装了。
///
/// ── 为什么四样东西挤在一页 ──
///
/// 待办、日程、提醒、盯着的事在 OS 那边是四份存储（语义和生命周期
/// 都不一样，合起来会坏）。但他问的是**同一个问题**：这东西在管我什么。
///
/// 所以存储照旧分开，视图合成一页，排序由 OS 那边定死（`/upcoming`）——
/// 让每个客户端自己排的话，手机上「接下来」的第一条和桌面上不是同一件事。
///
/// ── 为什么每一条都能撤 ──
///
/// 能立就要能撤。一条撤不掉的提醒，他唯一的处置办法是把整个通知关掉，
/// 而那会把真正要紧的那次也一起关掉。
class MindPage extends StatefulWidget {
  const MindPage({super.key, required this.state});
  final AppState state;

  @override
  State<MindPage> createState() => _MindPageState();
}

class _MindPageState extends State<MindPage> {
  Upcoming? _up;
  String _err = '';
  bool _loading = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final c = widget.state.client;
    if (c == null) return;
    setState(() => _loading = true);
    try {
      final up = await c.upcoming();
      if (mounted) setState(() => _up = up);
    } catch (e) {
      // 老版本 OS 上这个口子是 404 —— **那不是错误**，
      // 只是它还没有这个概念
      if (mounted) setState(() => _err = '$e');
    }
    if (mounted) setState(() => _loading = false);
  }

  @override
  Widget build(BuildContext context) {
    final up = _up;
    return Scaffold(
      backgroundColor: NX.bg,
      body: Column(children: [
        PageBar(
          title: '事项',
          action: IconButton(
            onPressed: _loading ? null : _load,
            icon: const Icon(Icons.refresh_rounded, size: 20),
            color: NX.text3,
          ),
        ),
        Expanded(
          child: RefreshIndicator(
            onRefresh: _load,
            child: ListView(
              padding: EdgeInsets.fromLTRB(NX.gutter, 0, NX.gutter,
                  MediaQuery.of(context).padding.bottom + NX.s7),
              children: [
                if (up == null && _err.isEmpty)
                  const Cell(title: '加载中')
                else if (_err.isNotEmpty)
                  Sec(label: '加载失败', children: [
                    Cell(
                      icon: Icons.info_outline_rounded,
                      title: _err.contains('404')
                          ? '当前 OS 版本不支持'
                          : '加载失败：$_err',
                    ),
                  ])
                else ...[
                  // ── 一句总账 ──
                  //
                  //	**"它什么都没管"和"还没连上"要分得开**：前者是
                  //	正常状态，后者是故障，而两者在一页空列表上长得
                  //	一模一样
                  Padding(
                    padding:
                        const EdgeInsets.fromLTRB(NX.s1, NX.s4, NX.s1, NX.s5),
                    child: Text(
                      up!.count == 0
                          ? '暂无事项'
                          : '共 ${up.count} 项',
                      style: NX.display,
                    ),
                  ),

                  // ── 接下来 ──
                  //
                  //	待办和日程排在一起：他不区分"这是我记的"和
                  //	"这是日历上的"，他要的是"接下来干什么"
                  if (up.tasks.isNotEmpty)
                    Sec(label: '待办', children: [
                      for (final t in up.tasks)
                        Cell(
                          icon: Icons.radio_button_unchecked_rounded,
                          // 过期的标出来 —— 一件昨天该做的事混在列表里，
                          // 看起来跟还没到点的一模一样
                          iconColor: t.overdue ? NX.warn : null,
                          title: t.what,
                          sub: _whenTask(t),
                          // **手机日历那些划不掉**：那是他日历里的事，
                          // 这边划掉不会改那边，而他会以为改了
                          note: t.from.isEmpty ? null : t.from,
                          onTap: () => _detail(
                            t.what,
                            _whenTask(t),
                            // 手机日历那些划不掉: 那是他日历里的事,
                            // 这边划掉不会改那边
                            act: t.from.isEmpty ? '标记完成' : null,
                            onAct: () => _done(t),
                          ),
                        ),
                    ]),

                  // ── 到点会响的 ──
                  if (up.reminders.isNotEmpty)
                    Sec(
                      label: '提醒',
                      foot: '点击撤销',
                      children: [
                        for (final r in up.reminders)
                          Cell(
                            icon: r.repeats
                                ? Icons.repeat_rounded
                                : Icons.alarm_rounded,
                            title: r.text,
                            sub: _whenAt(r.at, r.repeats),
                            onTap: () => _detail(
                              r.text,
                              _whenAt(r.at, r.repeats),
                              act: '撤销',
                              danger: true,
                              onAct: () => _cancel(r),
                            ),
                          ),
                      ],
                    ),

                  // ── 盯着的 ──
                  if (up.watches.isNotEmpty)
                    Sec(
                      label: '关注',
                      foot: '触发时主动通知。点击取消',
                      children: [
                        for (final w in up.watches)
                          Cell(
                            icon: Icons.visibility_outlined,
                            title: w.label,
                            onTap: () => _detail(
                              w.label,
                              null,
                              act: '取消关注',
                              danger: true,
                              onAct: () => _unwatch(w),
                            ),
                          ),
                      ],
                    ),

                  // ── 记着的 ──
                  //
                  //	放在下面：它们不会自己冒出来，是他问起才用的
                  if (up.notes.isNotEmpty)
                    Sec(label: '记事', children: [
                      for (final n in up.notes)
                        Cell(
                          icon: Icons.bookmark_rounded,
                          title: n.key,
                          sub: n.text,
                          onTap: () => _detail(n.text, n.key),
                        ),
                    ]),

                  if (up.places.isNotEmpty)
                    Sec(
                      label: '地点',
                      // **圈多大要显示, 而且要能改**: 圈小了他人在里面
                      // 而系统说不认识, 一次都不报错
                      foot: '点击调整范围',
                      children: [
                        for (final p in up.places)
                          Cell(
                            icon: Icons.place_rounded,
                            title: p.name,
                            note: '${p.radius.round()} 米',
                            onTap: () => _resize(p),
                          ),
                      ],
                    ),

                  if (up.count == 0)
                    Sec(label: '添加方式', children: const [
                      Cell(
                          icon: Icons.chat_bubble_outline_rounded,
                          title: '下周三三点开会',
                          sub: '记入日程'),
                      Cell(
                          icon: Icons.alarm_rounded,
                          title: '五点半提醒我打卡',
                          sub: '到点提醒'),
                      Cell(
                          icon: Icons.visibility_outlined,
                          title: '我到家时通知我',
                          sub: '触发时通知'),
                      Cell(
                          icon: Icons.bookmark_border_rounded,
                          title: '记住我妻子生日 3 月 2 日',
                          sub: '长期保存'),
                    ]),
                ],
              ],
            ),
          ),
        ),
      ]),
    );
  }

  /// 看全文 —— **列表回答"有哪几件", 这里回答"这件写了什么"**.
  ///
  ///	一条提醒的正文可能是模型给自己出的一大段题目, 铺在列表里
  ///	会把整张卡撑成半屏。动作也收进来: 一行既要能读又要能点的话,
  ///	点哪儿都会误触。
  Future<void> _detail(
    String text,
    String? when, {
    String? act,
    bool danger = false,
    VoidCallback? onAct,
  }) async {
    final go = await showModalBottomSheet<bool>(
      context: context,
      backgroundColor: NX.bgElevated,
      isScrollControlled: true,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c) => SafeArea(
        child: ConstrainedBox(
          constraints: BoxConstraints(
              maxHeight: MediaQuery.of(c).size.height * .7),
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            if (when != null)
              Padding(
                padding: const EdgeInsets.fromLTRB(NX.s5, NX.s5, NX.s5, NX.s2),
                child: Align(
                  alignment: Alignment.centerLeft,
                  child: Text(when,
                      style: NX.caption.copyWith(color: NX.text3)),
                ),
              ),
            Flexible(
              child: SingleChildScrollView(
                padding: EdgeInsets.fromLTRB(
                    NX.s5, when == null ? NX.s5 : 0, NX.s5, NX.s4),
                child: Align(
                  alignment: Alignment.centerLeft,
                  child: Text(text, style: NX.body),
                ),
              ),
            ),
            if (act != null && onAct != null)
              Padding(
                padding: const EdgeInsets.fromLTRB(NX.s5, 0, NX.s5, NX.s5),
                child: SizedBox(
                  width: double.infinity,
                  child: TextButton(
                    onPressed: () => Navigator.pop(c, true),
                    style: TextButton.styleFrom(
                      backgroundColor: NX.fill,
                      padding: const EdgeInsets.symmetric(vertical: NX.s3),
                      shape: RoundedRectangleBorder(
                          borderRadius: BorderRadius.circular(NX.rMd)),
                    ),
                    child: Text(act,
                        style: NX.label.copyWith(
                            color: danger ? NX.bad : NX.text,
                            fontWeight: NX.wMed)),
                  ),
                ),
              ),
          ]),
        ),
      ),
    );
    if (go == true && onAct != null) onAct();
  }

  /// 改一个地方的圈.
  ///
  ///	**给几个档而不是让他填数**: 他知道"我家是个小区", 不知道
  ///	"我家是 480 米"
  Future<void> _resize(Place p) async {
    const opts = {
      120: '门店 · 120 米',
      250: '楼宇 · 250 米',
      450: '小区 · 450 米',
      800: '园区 · 800 米',
    };
    final pick = await showModalBottomSheet<int>(
      context: context,
      backgroundColor: NX.bgElevated,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c) => SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(NX.s5, NX.s5, NX.s5, NX.s3),
            child: Align(
              alignment: Alignment.centerLeft,
              child: Text('${p.name} 的范围', style: NX.heading),
            ),
          ),
          for (final e in opts.entries)
            ListTile(
              leading: Icon(
                  (p.radius.round() - e.key).abs() < 40
                      ? Icons.radio_button_checked_rounded
                      : Icons.radio_button_off_rounded,
                  size: 20,
                  color: (p.radius.round() - e.key).abs() < 40
                      ? NX.accent
                      : NX.text4),
              title: Text(e.value, style: NX.body),
              onTap: () => Navigator.pop(c, e.key),
            ),
        ]),
      ),
    );
    if (pick == null || !mounted) return;
    try {
      await widget.state.client!
          .namePlace(p.name, p.lat, p.lon, radius: pick.toDouble());
      await _load();
    } catch (e) {
      if (mounted) _toast('$e');
    }
  }

  Future<void> _done(Task t) async {
    try {
      await widget.state.client!.doneTask(t.id);
      await _load();
    } catch (e) {
      if (mounted) _toast('$e');
    }
  }

  /// 撤一条提醒 —— **要问一次**：它是他自己设的，而撤掉之后到点
  /// 什么都不会发生，跟"我忘了设"长得一模一样
  Future<void> _cancel(Reminder r) async {
    try {
      await widget.state.client!.cancelReminder(r.id);
      await _load();
    } catch (e) {
      if (mounted) _toast('$e');
    }
  }

  Future<void> _unwatch(Watch w) async {
    try {
      await widget.state.client!.removeWatch(w.id);
      await _load();
    } catch (e) {
      if (mounted) _toast('$e');
    }
  }


  void _toast(String m) => ScaffoldMessenger.of(context)
    ..hideCurrentSnackBar()
    ..showSnackBar(SnackBar(
      content: Text(m, style: NX.body.copyWith(color: Colors.white)),
      backgroundColor: NX.bad,
      behavior: SnackBarBehavior.floating,
    ));

  static String? _whenTask(Task t) {
    final at = t.at;
    if (at == null) return t.where.isEmpty ? null : t.where;
    var s = _whenAt(at, false);
    if (t.overdue) s += ' · 已逾期';
    if (t.where.isNotEmpty) s += ' · ${t.where}';
    return s;
  }

  /// 什么时候 —— **今天的只报钟点**：多写一个日期是他已经知道的事
  static String _whenAt(DateTime at, bool repeats) {
    String two(int n) => n.toString().padLeft(2, '0');
    final now = DateTime.now();
    final clock = '${two(at.hour)}:${two(at.minute)}';
    if (repeats) return '每日 $clock';
    final sameDay =
        at.year == now.year && at.month == now.month && at.day == now.day;
    if (sameDay) return '今天 $clock';
    final tomorrow = now.add(const Duration(days: 1));
    if (at.year == tomorrow.year &&
        at.month == tomorrow.month &&
        at.day == tomorrow.day) {
      return '明天 $clock';
    }
    return '${two(at.month)}-${two(at.day)} $clock';
  }
}
