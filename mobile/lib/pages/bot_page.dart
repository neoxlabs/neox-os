import 'package:flutter/material.dart';

import '../api/events.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/motion.dart';
import '../widgets/avatar.dart';
import '../widgets/panel.dart';
import '../widgets/toggle.dart';
import 'activity_page.dart';

/// 一个 bot 的资料页 —— 点会话顶栏的头像进来.
///
/// 能力是内核强制的, 只读; 岗位、工作区、房间是启动参数, **可以改** ——
/// 改一次换一个进程(对话不断), 见 [AppState.setRole].
class BotPage extends StatelessWidget {
  const BotPage({super.key, required this.state, required this.pid});

  final AppState state;
  final ProcessID pid;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: NX.bg,
      body: ListenableBuilder(
        listenable: state,
        builder: (context, _) {
          final bot = state.bots.where((b) => b.pid == pid).firstOrNull;
          if (bot == null) {
            return _Gone(onBack: () => Navigator.of(context).pop());
          }
          final p = presenceOf(bot.state);
          final muted = state.muted.contains(bot.pid);
          // 顶栏在列表外面 —— 它要顶到屏幕边, 而下面的面板要缩进一个
          // gutter. 上一版把两者放在同一个列表里且只给了 bottom padding,
          // 于是卡片贴着屏幕边, 比它上面那行小标题还宽
          return Column(children: [
            const PageBar(title: ''),
            Expanded(
              child: ListView(
            padding: EdgeInsets.fromLTRB(NX.gutter, 0, NX.gutter,
                MediaQuery.of(context).padding.bottom + NX.s7),
            children: [
              // ── 脸 ──
              Padding(
                padding: const EdgeInsets.symmetric(vertical: NX.s5),
                child: Column(children: [
                  Avatar(
                      id: bot.pid,
                      size: 88,
                      presence: p.kind,
                      ringColor: NX.bg),
                  const SizedBox(height: NX.s3),
                  Text(bot.name, style: NX.title),
                  const SizedBox(height: NX.s1),
                  Text(p.label,
                      style: NX.label.copyWith(
                          color: p.kind == Presence.busy
                              ? NX.accent
                              : NX.text3)),
                ]),
              ),

              // ── 改得动的那几样 ──
              //
              // 这三条都是**启动参数**: 改一次换一个进程, 对话不断.
              // 能力不在这儿 —— 它是内核强制的, 见下面那一段
              Sec(label: '设置', children: [
                Cell(
                  icon: Icons.badge_outlined,
                  title: '岗位',
                  sub: bot.role.isEmpty ? '没设' : bot.role,
                  onTap: () => _editRole(context, bot),
                ),
                Cell(
                  icon: Icons.folder_open_rounded,
                  title: '工作区',
                  sub: bot.work.isEmpty ? '没派' : bot.work,
                  mono: true,
                  onTap: () => _editWork(context, bot),
                ),
                Cell(
                  icon: Icons.meeting_room_outlined,
                  title: '房间',
                  sub: bot.thread.isEmpty ? '单聊' : bot.thread,
                  onTap: () => _editRoom(context, bot),
                ),
              ]),

              // ── 能力 ──
              //
              // **有几条就说几条, 一条都没有也要说出来**: 一个没有任何
              // 能力的 bot 只能说话不能干活, 而那件事用户必须知道
              Sec(
                label: '它能干什么',
                foot: '内核强制，改不了。',
                children: bot.caps.isEmpty
                    ? [
                        const Cell(
                            icon: Icons.block_rounded,
                            title: '一条能力都没有 —— 它只能说话'),
                      ]
                    : [
                        for (final c in bot.caps)
                          Cell(
                            icon: switch (c.axis) {
                              'read' => Icons.folder_open_rounded,
                              'write' => Icons.edit_rounded,
                              'net' => Icons.public_rounded,
                              'proc' => Icons.account_tree_rounded,
                              'secret' => Icons.vpn_key_rounded,
                              _ => Icons.circle_outlined,
                            },
                            title: c.label,
                            // 出网/写盘/凭据这三条能造成外部后果, 标成警告色
                            iconColor: c.heavy ? NX.warn : NX.text3,
                          ),
                      ],
              ),

              // ── 它干过什么 ──
              //
              // 放在能力后面: 先看它**能**干什么, 再看它**干了**什么
              Sec(label: '记录', children: [
                Cell(
                  icon: Icons.history_rounded,
                  title: '它干过什么',
                  note: state.activity(bot.pid).isEmpty
                      ? '还没有'
                      : '${state.activity(bot.pid).length} 条',
                  onTap: () => Navigator.of(context).push(SlideRoute(
                      builder: (_) =>
                          ActivityPage(state: state, pid: bot.pid))),
                ),
              ]),

              // ── 这一屋子的设置 ──
              Sec(label: '在这儿', children: [
                Cell(
                            icon: muted
                      ? Icons.notifications_off_rounded
                      : Icons.notifications_none_rounded,
                  title: muted ? '已静音' : '会提醒我',
                  // **跟设置页同一个开关**. 系统那个 Switch 在安卓上是
                  // 52 宽还带个大水波, 跟隔壁页那个 40 宽的不是一个东西 ——
                  // 同一个动作在两页长两个样, 正是"组件之间没关系"
                  ctl: NxToggle(
                    on: !muted,
                    onChanged: bot.state.alive
                        ? (_) => state.toggleMute(bot.pid)
                        : (_) {},
                  ),
                ),
              ]),

              // ── 会造成后果的那两个 ──
              //
              // **放在最后, 而且分开两条**: 停和删完全不是一回事,
              // 挨在一起画成一样会点错
              Sec(
                label: '危险动作',
                children: [
                  Cell(
                    icon: Icons.stop_circle_outlined,
                    title: '停掉它',
                    sub: bot.state.alive ? '工作区和账本保留' : '它已经停了',
                    onTap: bot.state.alive
                        ? () => _confirm(
                              context,
                              title: '停掉「${bot.name}」？',
                              body: '正在干的活会中断。',
                              action: '停掉',
                              danger: false,
                              run: () => state.stopBot(bot.name),
                            )
                        : null,
                  ),
                  Cell(
                    icon: Icons.delete_outline_rounded,
                    danger: true,
                    title: '删掉它',
                    sub: '不可逆',
                    onTap: () => _confirm(
                      context,
                      title: '删掉「${bot.name}」？',
                      // **说清楚到底没了什么** —— 只写"不可逆"的话,
                      // 用户不知道是连聊天记录一起没了还是只是列表里消失
                      body: '进程和会话都没了。账本里的记录还在，'
                          '但要在电脑上翻。',
                      action: '删掉',
                      danger: true,
                      run: () async {
                        await state.forgetBot(bot.pid);
                        if (context.mounted) Navigator.of(context).pop();
                      },
                    ),
                  ),
                ],
              ),

              // ── 那些 id 和时间 ──
              //
              // 平时没人看, 出事的时候是唯一能对上号的东西.
              // 所以留着, 但放最后、用小字
              Sec(label: '进程', children: [
                Cell(
                            icon: Icons.tag_rounded, title: bot.pid, mono: true),
                Cell(
                            icon: Icons.workspaces_outline, title: bot.app),
                Cell(
                            icon: Icons.schedule_rounded,
                    title: '起于 ${_when(bot.createdAt)}'),
              ]),
            ],
              ),
            ),
          ]);
        },
      ),
    );
  }

  /// 改岗位 —— 多行, 它是要进系统段的一段话
  Future<void> _editRole(BuildContext context, ProcInfo bot) => _edit(
        context,
        title: '岗位',
        hint: '它负责什么',
        initial: bot.role,
        lines: 5,
        run: (v) => state.setRole(bot.name, v),
      );

  /// 换工作区 —— **先给选的, 再给填的**.
  ///
  /// 让人手打一个绝对路径是最差的一种输入: 打错一个字符的后果是
  /// "OS 说这个目录不行", 而他看不出错在哪儿. 而屋里那几个 bot
  /// 已经在用的目录是**真实存在、而且已经验证过能派**的 —— 那才是
  /// 候选该有的样子.
  Future<void> _editWork(BuildContext context, ProcInfo bot) async {
    // 候选: 屋里在用的 + 起过名的项目. 两个来源都要 —— 前者是事实,
    // 后者是用户认得的名字
    final seen = <String, String>{}; // 路径 -> 给人看的名字
    for (final b in state.bots) {
      if (b.work.isNotEmpty) seen.putIfAbsent(b.work, () => b.work);
    }
    try {
      final named = await state.client!.projects();
      named.forEach((path, name) => seen[path] = name);
    } catch (_) {
      // 拉不到项目名不影响选 —— 屋里在用的那几个已经够开工了
    }
    if (!context.mounted) return;
    final picked = await _pick(
      context,
      title: '派到哪个工作区',
      current: bot.work,
      options: [for (final e in seen.entries) (e.key, e.value, e.key)],
      manual: '手填一个路径',
      manualHint: '/root/.neox-os/work/…',
      mono: true,
    );
    if (picked == null || picked == bot.work) return;
    try {
      await state.rebind(bot.name, picked);
    } catch (e) {
      if (context.mounted) _toast(context, '$e');
    }
  }

  /// 换房间 —— 同一个房间名的几个 bot 就在一屋. 空 = 拉出来单聊
  Future<void> _editRoom(BuildContext context, ProcInfo bot) async {
    final rooms = <String>{
      for (final b in state.bots)
        if (b.thread.isNotEmpty) b.thread,
    };
    final picked = await _pick(
      context,
      title: '进哪个房间',
      current: bot.thread,
      options: [
        ('', '单聊', '只有你和它'),
        for (final r in rooms) (r, r, '${_countIn(r)} 个人在里面'),
      ],
      manual: '开一个新房间',
      manualHint: '房间名',
    );
    if (picked == null || picked == bot.thread) return;
    try {
      await state.moveBot(bot.name, picked);
    } catch (e) {
      if (context.mounted) _toast(context, '$e');
    }
  }

  int _countIn(String thread) =>
      state.bots.where((b) => b.thread == thread).length;

  /// 选一个 —— 底部弹一张单子, 最后一条是"自己填".
  ///
  /// options 每条是 (值, 标题, 副标题). 返回 null = 没选
  Future<String?> _pick(
    BuildContext context, {
    required String title,
    required String current,
    required List<(String, String, String)> options,
    required String manual,
    required String manualHint,
    bool mono = false,
  }) async {
    final chosen = await showModalBottomSheet<String?>(
      context: context,
      backgroundColor: NX.bgElevated,
      shape: const RoundedRectangleBorder(
          borderRadius:
              BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      isScrollControlled: true,
      builder: (c) => SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(NX.s5, NX.s5, NX.s5, NX.s3),
            child: Row(children: [
              Expanded(child: Text(title, style: NX.heading)),
              Text('换一个就重起一个进程',
                  style: NX.caption.copyWith(color: NX.text4)),
            ]),
          ),
          Flexible(
            child: ListView(shrinkWrap: true, children: [
              for (final (value, label, sub) in options)
                ListTile(
                  title: Text(label,
                      style: (mono && value.isNotEmpty
                              ? NX.body.copyWith(fontFamily: 'monospace')
                              : NX.body)
                          .copyWith(
                              color: value == current ? NX.accent : NX.text),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis),
                  subtitle: sub.isEmpty
                      ? null
                      : Text(sub,
                          style: NX.caption.copyWith(color: NX.text4),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis),
                  trailing: value == current
                      ? Icon(Icons.check_rounded, size: 20, color: NX.accent)
                      : null,
                  onTap: () => Navigator.pop(c, value),
                ),
              Divider(height: 1, color: NX.line),
              ListTile(
                leading: Icon(Icons.edit_rounded, size: 20, color: NX.text3),
                title: Text(manual, style: NX.body.copyWith(color: NX.text2)),
                onTap: () => Navigator.pop(c, '\u0000manual'),
              ),
            ]),
          ),
        ]),
      ),
    );
    if (chosen == null) return null;
    if (chosen != '\u0000manual') return chosen;
    if (!context.mounted) return null;
    return _ask(context,
        title: title, hint: manualHint, initial: current, mono: mono);
  }

  /// 手填那一条 —— 返回填的内容, 取消返回 null
  Future<String?> _ask(
    BuildContext context, {
    required String title,
    required String hint,
    required String initial,
    bool mono = false,
  }) async {
    final ctl = TextEditingController(text: initial);
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        backgroundColor: NX.bgElevated,
        shape:
            RoundedRectangleBorder(borderRadius: BorderRadius.circular(NX.rLg)),
        title: Text(title, style: NX.heading),
        content: TextField(
          controller: ctl,
          autofocus: true,
          style: mono ? NX.body.copyWith(fontFamily: 'monospace') : NX.body,
          decoration: InputDecoration(
            hintText: hint,
            hintStyle: NX.body.copyWith(color: NX.text3),
            filled: true,
            fillColor: NX.fill,
            border: OutlineInputBorder(
              borderRadius: BorderRadius.circular(NX.rMd),
              borderSide: BorderSide.none,
            ),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: Text('算了', style: NX.label.copyWith(color: NX.text2)),
          ),
          TextButton(
            onPressed: () => Navigator.pop(c, true),
            child: Text('保存',
                style:
                    NX.label.copyWith(color: NX.accent, fontWeight: NX.wMed)),
          ),
        ],
      ),
    );
    if (ok != true) return null;
    return ctl.text.trim();
  }

  /// 三个编辑共用一个框.
  ///
  /// **改这三样都会换一个进程**: 它们是启动参数, 不是能中途改的状态.
  /// 所以框里要说一句 —— 否则用户不知道自己刚把它正干的活打断了
  Future<void> _edit(
    BuildContext context, {
    required String title,
    required String hint,
    required String initial,
    required Future<void> Function(String) run,
    int lines = 1,
    bool mono = false,
  }) async {
    final ctl = TextEditingController(text: initial);
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        backgroundColor: NX.bgElevated,
        shape:
            RoundedRectangleBorder(borderRadius: BorderRadius.circular(NX.rLg)),
        title: Text(title, style: NX.heading),
        content: Column(mainAxisSize: MainAxisSize.min, children: [
          TextField(
            controller: ctl,
            autofocus: true,
            maxLines: lines,
            style: mono
                ? NX.body.copyWith(fontFamily: 'monospace')
                : NX.body,
            decoration: InputDecoration(
              hintText: hint,
              hintStyle: NX.body.copyWith(color: NX.text3),
              filled: true,
              fillColor: NX.fill,
              border: OutlineInputBorder(
                borderRadius: BorderRadius.circular(NX.rMd),
                borderSide: BorderSide.none,
              ),
            ),
          ),
          const SizedBox(height: NX.s3),
          Align(
            alignment: Alignment.centerLeft,
            child: Text('会重起一个进程，正在干的活会断。对话记录不受影响。',
                style: NX.caption.copyWith(color: NX.text4)),
          ),
        ]),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: Text('算了', style: NX.label.copyWith(color: NX.text2)),
          ),
          TextButton(
            onPressed: () => Navigator.pop(c, true),
            child: Text('保存',
                style:
                    NX.label.copyWith(color: NX.accent, fontWeight: NX.wMed)),
          ),
        ],
      ),
    );
    if (ok != true) return;
    final v = ctl.text.trim();
    if (v == initial.trim()) return;
    try {
      await run(v);
    } catch (e) {
      if (context.mounted) _toast(context, '$e');
    }
  }

  static void _toast(BuildContext context, String m) =>
      ScaffoldMessenger.of(context)
        ..hideCurrentSnackBar()
        ..showSnackBar(SnackBar(
          content: Text(m, style: NX.body.copyWith(color: Colors.white)),
          backgroundColor: NX.bad,
          behavior: SnackBarBehavior.floating,
        ));

  /// 危险动作走同一个确认.
  ///
  /// **一个都不许省**: 停和删都会让正在干的活中断, 而手机上误触
  /// 比电脑上容易得多 —— 口袋里蹭一下就点了
  Future<void> _confirm(
    BuildContext context, {
    required String title,
    required String body,
    required String action,
    required bool danger,
    required Future<void> Function() run,
  }) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        backgroundColor: NX.bgElevated,
        shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(NX.rLg)),
        title: Text(title, style: NX.heading),
        content: Text(body, style: NX.bodyDim),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: Text('算了', style: NX.label.copyWith(color: NX.text2)),
          ),
          TextButton(
            onPressed: () => Navigator.pop(c, true),
            child: Text(action,
                style: NX.label.copyWith(
                    color: danger ? NX.bad : NX.accent,
                    fontWeight: NX.wMed)),
          ),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await run();
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context)
          ..hideCurrentSnackBar()
          ..showSnackBar(SnackBar(
            content: Text('$e', style: NX.body.copyWith(color: Colors.white)),
            backgroundColor: NX.bad,
            behavior: SnackBarBehavior.floating,
          ));
      }
    }
  }

  static String _when(int ms) {
    if (ms == 0) return '不知道';
    final t = DateTime.fromMillisecondsSinceEpoch(ms);
    final d = DateTime.now().difference(t);
    if (d.inMinutes < 60) return '${d.inMinutes} 分钟前';
    if (d.inHours < 24) return '${d.inHours} 小时前';
    return '${t.month} 月 ${t.day} 日';
  }
}

class _Gone extends StatelessWidget {
  const _Gone({required this.onBack});
  final VoidCallback onBack;

  @override
  Widget build(BuildContext context) => Column(children: [
        const PageBar(title: ''),
        const Expanded(
          child: Center(child: Text('这个 bot 已经不在了')),
        ),
      ]);
}
