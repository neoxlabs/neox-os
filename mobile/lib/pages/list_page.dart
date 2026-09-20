import 'package:flutter/material.dart';

import '../api/events.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/motion.dart';
import '../widgets/avatar.dart';
import '../widgets/empty_state.dart';
import 'bot_page.dart';
import 'chat_page.dart';
import 'inbox_page.dart';

/// 消息 —— 主页就是一列 bot, 点谁进谁的会话.
///
/// ── 这是客户端左栏那一列 ──
///
/// 桌面上它是一条 252px 的侧栏; 手机上没有并排的地方, 于是侧栏变成
/// 一整屏、会话变成推进去的一页. **形状没变**: 先选人, 再说话.
/// 微信、Telegram、飞书, 主页全是一列会话.
class ListPage extends StatelessWidget {
  const ListPage({super.key, required this.state, required this.onGoSettings});

  final AppState state;

  /// 空态里那颗"去设置"要能真的把人送过去 ——
  /// **只说"去设置页填地址"而不给路, 等于让他自己找**
  final VoidCallback onGoSettings;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: state,
      builder: (context, _) {
        final groups = _grouped(state.bots);
        // 每组都只有一个人的话就别分组了: 六个组头配六行,
        // 组头比内容还多, 那不是分组是加噪音
        final worthGrouping =
            groups.length > 1 && groups.values.any((g) => g.length > 1);
        return Column(
          children: [
            _Top(state: state, onNew: () => _newBot(context)),
            Expanded(
              child: state.bots.isEmpty
                  ? _empty(context)
                  : ListView(
                      padding: const EdgeInsets.only(
                          top: NX.s2, bottom: NX.s6),
                      children: [
                        for (final g in groups.entries) ...[
                          if (worthGrouping)
                            _GroupHead(title: g.key, n: g.value.length),
                          for (final b in g.value)
                            _Item(state: state, bot: b),
                        ],
                      ],
                    ),
            ),
          ],
        );
      },
    );
  }

  Widget _empty(BuildContext context) => switch (state.conn) {
        // **连接中不许说"没有 bot"** —— 那时候根本还不知道有没有。
        // 刚打开 App 时进程表可能尚未返回；先说"这台 OS 上还没有 bot"
        // 会把服务器上已有的 bot 误报成不存在。
        Conn.connecting => const EmptyState(
            art: 'assets/empty_chat.png',
            line: '连接中…',
            hint: ''),
        Conn.off => EmptyState(
            art: 'assets/empty_offline.png',
            line: '还没连上任何一台 OS',
            hint: '容器跑在哪台机器上，就填哪台的地址',
            action: EmptyAction(label: '去连接', onTap: onGoSettings)),
        Conn.badToken => EmptyState(
            art: 'assets/empty_offline.png',
            line: 'token 不对',
            hint: '跟容器里的 NEOX_OBSERVE_TOKEN 对一下',
            action: EmptyAction(label: '去改', onTap: onGoSettings)),
        Conn.error => EmptyState(
            art: 'assets/empty_offline.png',
            line: '连不上',
            hint: state.lastError ?? ''),
        _ => const EmptyState(
            art: 'assets/empty_nobots.png',
            line: '这台 OS 上还没有 bot',
            hint: '在电脑上建一个，它就会出现在这儿'),
      };

  /// 新建 —— 一个底部弹层, 只问两件事.
  ///
  /// **只问名字和岗位**: 能力不在这儿给(要更多得走 request_access
  /// 让 OS 批准), 工作区留空由 OS 按默认给. 新建表单每多一格,
  /// 就多一个用户答不上来的问题
  Future<void> _newBot(BuildContext context) async {
    final name = TextEditingController();
    final role = TextEditingController();
    final ok = await showModalBottomSheet<bool>(
      context: context,
      backgroundColor: NX.bgElevated,
      isScrollControlled: true,
      showDragHandle: true,
      shape: const RoundedRectangleBorder(
          borderRadius:
              BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c) => Padding(
        padding: EdgeInsets.only(
            left: NX.gutter,
            right: NX.gutter,
            bottom: MediaQuery.of(c).viewInsets.bottom + NX.s6),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('新建一个 bot', style: NX.title),
            const SizedBox(height: NX.s2),
            Text('它是一个真进程：有自己的能力、自己的预算，关掉这个 App 也照样跑。',
                style: NX.caption),
            const SizedBox(height: NX.s5),
            _Field(controller: name, label: '名字', hint: '比如 值守'),
            const SizedBox(height: NX.s4),
            _Field(
                controller: role,
                label: '岗位（可以不填）',
                hint: '一句话说它管什么'),
            const SizedBox(height: NX.s5),
            SizedBox(
              width: double.infinity,
              child: FilledButton(
                onPressed: () => Navigator.pop(c, true),
                style: FilledButton.styleFrom(
                  backgroundColor: NX.accent,
                  foregroundColor: Colors.white,
                  padding: const EdgeInsets.symmetric(vertical: NX.s4),
                  shape: RoundedRectangleBorder(
                      borderRadius: BorderRadius.circular(NX.rMd)),
                ),
                child: const Text('建',
                    style: TextStyle(
                        fontSize: NX.fBody, fontWeight: NX.wMed)),
              ),
            ),
          ],
        ),
      ),
    );
    if (ok != true || name.text.trim().isEmpty) return;
    try {
      await state.createBot(name.text, role: role.text);
    } catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(context)
          ..hideCurrentSnackBar()
          ..showSnackBar(SnackBar(
            content:
                Text('$e', style: NX.body.copyWith(color: Colors.white)),
            backgroundColor: NX.bad,
            behavior: SnackBarBehavior.floating,
          ));
      }
    }
  }

  /// 按项目分组 —— 客户端就是这么分的: 一列七八个项目, 光看高亮
  /// 那一行只知道"选的是谁", 不知道"这是哪摊活"
  static Map<String, List<ProcInfo>> _grouped(List<ProcInfo> bots) {
    final out = <String, List<ProcInfo>>{};
    for (final b in bots) {
      (out[b.app] ??= []).add(b);
    }
    return out;
  }
}

/// 顶栏 —— 字标 + 屋里的情况.
///
/// **不做搜索框**: 一台机器上就那么几个 bot, 一屏放得下.
/// 搜索框是给"多到找不着"准备的, 现在放上去只是占掉一行高
class _Top extends StatelessWidget {
  const _Top({required this.state, required this.onNew});
  final AppState state;
  final VoidCallback onNew;

  @override
  Widget build(BuildContext context) {
    final busy = state.bots.where((b) => b.state.busy).length;
    final live = state.bots.where((b) => b.state.alive).length;
    // **点的颜色和字的颜色分开**: 连上了却配一个灰点看着像没连上 ——
    // 而那个点是这一屏唯一说"这条线通不通"的东西
    final (line, dot, tone) = switch (state.conn) {
      Conn.off => ('还没连上', NX.text4, NX.text3),
      Conn.badToken => ('token 不对', NX.bad, NX.bad),
      Conn.error => ('重连中…', NX.warn, NX.warn),
      Conn.connecting => ('连接中…', NX.text3, NX.text3),
      Conn.live when busy > 0 => ('$busy 个在干活', NX.accent, NX.accent),
      Conn.live => ('$live 个在线', NX.ok, NX.text3),
    };
    return Container(
      padding: EdgeInsets.fromLTRB(NX.gutter,
          MediaQuery.of(context).padding.top + NX.s4, NX.gutter, NX.s3),
      decoration: BoxDecoration(
        color: NX.bg,
        border: Border(bottom: BorderSide(color: NX.line)),
      ),
      // ── 一行, 不是两行 ──
      //
      // 上一版字标在上、状态在下叠成一个两行高的块, 右边那两枚图标
      // 按这个块居中 —— 于是字标的中线比图标高出半行, 一眼就看得出
      // 左右不齐. 状态本来也不需要独占一行: 它是一个点加四个字
      child: Row(children: [
        // 字标 —— **用现成的那张图, 不排字也不自己画**.
        // 品牌标是已经画好的东西, 拿字体去凑一个近似的,
        // 结果一定是"看着像但不对".
        // 按高度定死、宽度自适应, 不许拉伸(原图 2034×352)
        Image.asset(
          NX.isDark ? 'assets/lockup-dark.png' : 'assets/lockup-light.png',
          height: 20,
          fit: BoxFit.contain,
          filterQuality: FilterQuality.medium,
        ),
        const SizedBox(width: NX.s3),
        Container(
          width: 6,
          height: 6,
          decoration: BoxDecoration(color: dot, shape: BoxShape.circle),
        ),
        const SizedBox(width: NX.s2),
        // **Expanded 而不是 Flexible + Spacer**: 那两个会平分剩余空间,
        // 于是"6 个在线"被压成"6 ⋯" —— 一个截断到只剩数字的状态,
        // 比不显示更糟(它看着像出错了)
        Expanded(
          child: Text(line,
              style: NX.caption.copyWith(color: tone),
              maxLines: 1,
              overflow: TextOverflow.ellipsis),
        ),
        // 「它跟我说过什么」—— **有新的时候带一个点**.
        //
        // 主动消息本来埋在各自 bot 的会话里, 你不知道该去哪儿找 ——
        // 而这类消息恰恰是最需要一个入口的: 它们不是你问出来的。
        Stack(clipBehavior: Clip.none, children: [
          IconButton(
            onPressed: () => Navigator.of(context)
                .push(SlideRoute(builder: (_) => InboxPage(state: state))),
            icon: const Icon(Icons.notifications_none_rounded, size: 24),
            color: NX.text2,
            tooltip: '它跟我说过什么',
          ),
          if (state.unseenDeliveries > 0)
            Positioned(
              right: 8,
              top: 8,
              // 一个点不是数字 —— 跟未读那条一个道理:
              // 数字会变成一个永远清不掉的焦虑源
              child: Container(
                width: 8,
                height: 8,
                decoration:
                    BoxDecoration(color: NX.accent, shape: BoxShape.circle),
              ),
            ),
        ]),
        // 新建 —— **这一屏唯一的动作, 所以它在右上角**.
        // 没连上的时候是灰的而不是藏起来: 藏起来的话用户会以为
        // 这个 App 不支持建 bot
        IconButton(
          onPressed: state.conn == Conn.live ? onNew : null,
          icon: const Icon(Icons.add_rounded, size: 26),
          color: NX.text2,
          disabledColor: NX.text4,
          tooltip: '新建 bot',
        ),
      ]),
    );
  }
}

class _GroupHead extends StatelessWidget {
  const _GroupHead({required this.title, required this.n});
  final String title;
  final int n;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.fromLTRB(
            NX.gutter, NX.s4, NX.gutter, NX.s2),
        child: Row(children: [
          Text(title.isEmpty ? '其他' : title, style: NX.section),
          const SizedBox(width: NX.s2),
          Text('$n', style: NX.captionDim),
        ]),
      );
}

/// 一行 —— 头像 · 名字 · 摘要 · 时间 · 未读点
class _Item extends StatelessWidget {
  const _Item({required this.state, required this.bot});
  final AppState state;
  final ProcInfo bot;

  @override
  Widget build(BuildContext context) {
    final last = state.lastOf(bot.pid);
    final n = state.unread[bot.pid] ?? 0;
    final p = presenceOf(bot.state);
    final preview = last == null
        ? p.label
        : (last.kind == MsgKind.user ? '你: ${last.text}' : last.text)
            .replaceAll('\n', ' ');

    return Material(
      color: Colors.transparent,
      child: InkWell(
        onTap: () {
          state.openThread(bot.pid);
          Navigator.of(context)
              .push(SlideRoute(builder: (_) => ChatPage(state: state, pid: bot.pid)))
              .then((_) => state.closeThread());
        },
        // **长按出菜单** —— 手机上列表项的第二组动作就该在这儿.
        // 不做的话, 静音一个 bot 要先点进会话再点顶栏,
        // 删一个要点进会话→点头像→划到底
        onLongPress: () => _menu(context),
        child: Container(
          constraints: const BoxConstraints(minHeight: NX.rowH),
          padding: const EdgeInsets.symmetric(
              horizontal: NX.gutter, vertical: NX.s3),
          child: Row(
            children: [
              Avatar(
                  id: bot.pid,
                  size: NX.avatarLg,
                  presence: p.kind,
                  ringColor: NX.bg),
              const SizedBox(width: NX.s3),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    Row(children: [
                      Expanded(
                        // 未读时名字变实 —— 同一档字号, 只换字重.
                        // 换字号的话行高会跟着变, 一列行就参差了
                        child: Text(bot.name,
                            style: n > 0 ? NX.headingOn : NX.heading,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis),
                      ),
                      const SizedBox(width: NX.s2),
                      if (last != null)
                        Text(_time(last.at),
                            style: NX.caption.copyWith(
                                color: n > 0 ? NX.accent : NX.text4)),
                    ]),
                    const SizedBox(height: NX.s1),
                    Row(children: [
                      Expanded(
                        child: Text(preview,
                            style: n > 0 ? NX.label : NX.labelDim,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis),
                      ),
                      const SizedBox(width: NX.s2),
                      // 未读是一个点不是数字气泡: 一队 bot 的消息本来
                      // 就没有"读完"这回事, 数字只会变成一个永远清不掉
                      // 的焦虑源.
                      // **读过的行位置照留**(透明) —— 不留的话这一列
                      // 会塌掉, 时间跟着左右跳
                      Container(
                        width: 8,
                        height: 8,
                        decoration: BoxDecoration(
                            color: n > 0 ? NX.accent : Colors.transparent,
                            shape: BoxShape.circle),
                      ),
                    ]),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  /// 长按菜单.
  ///
  /// **只放这一行上用得着的**: 静音、看资料、停、删.
  /// 菜单里每多一项, 用户找那一项的时间就长一点 ——
  /// 而长按菜单存在的理由正是"比点进去快"
  Future<void> _menu(BuildContext context) async {
    final muted = state.muted.contains(bot.pid);
    final alive = bot.state.alive;
    final pick = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: NX.bgElevated,
      showDragHandle: true,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c) => SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(NX.gutter, 0, NX.gutter, NX.s3),
            child: Row(children: [
              Avatar(id: bot.pid, size: 36),
              const SizedBox(width: NX.s3),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(bot.name, style: NX.heading),
                    Text(presenceOf(bot.state).label, style: NX.caption),
                  ],
                ),
              ),
            ]),
          ),
          Divider(height: 1, color: NX.line),
          _MenuItem(
            icon: muted
                ? Icons.notifications_active_rounded
                : Icons.notifications_off_rounded,
            label: muted ? '取消静音' : '静音',
            onTap: () => Navigator.pop(c, 'mute'),
          ),
          _MenuItem(
            icon: Icons.info_outline_rounded,
            label: '它能干什么',
            onTap: () => Navigator.pop(c, 'info'),
          ),
          if (alive)
            _MenuItem(
              icon: Icons.stop_circle_outlined,
              label: '停掉它',
              onTap: () => Navigator.pop(c, 'stop'),
            ),
          _MenuItem(
            icon: Icons.delete_outline_rounded,
            label: '删掉它',
            danger: true,
            onTap: () => Navigator.pop(c, 'del'),
          ),
          const SizedBox(height: NX.s2),
        ]),
      ),
    );
    if (pick == null || !context.mounted) return;
    switch (pick) {
      case 'mute':
        state.toggleMute(bot.pid);
      case 'info':
        Navigator.of(context).push(SlideRoute(builder: (_) => BotPage(state: state, pid: bot.pid)));
      case 'stop':
        _confirm(context, '停掉「${bot.name}」？',
            '它正在干的活会中断。工作区和账本都留着。', '停掉', false,
            () => state.stopBot(bot.name));
      case 'del':
        _confirm(
            context,
            '删掉「${bot.name}」？',
            '进程会被删掉，这台手机上再也看不到它。\n\n'
                '它说过的话还在 OS 的事件账本里，但要在电脑上才翻得到。',
            '删掉',
            true,
            () => state.forgetBot(bot.pid));
    }
  }

  /// **误触在手机上比电脑上容易得多** —— 口袋里蹭一下就点了.
  /// 所以停和删都要再问一次
  Future<void> _confirm(BuildContext context, String title, String body,
      String action, bool danger, Future<void> Function() run) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        backgroundColor: NX.bgElevated,
        shape:
            RoundedRectangleBorder(borderRadius: BorderRadius.circular(NX.rLg)),
        title: Text(title, style: NX.heading),
        content: Text(body, style: NX.bodyDim),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(c, false),
              child: Text('算了', style: NX.label.copyWith(color: NX.text2))),
          TextButton(
              onPressed: () => Navigator.pop(c, true),
              child: Text(action,
                  style: NX.label.copyWith(
                      color: danger ? NX.bad : NX.accent,
                      fontWeight: NX.wMed))),
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

  static String _time(DateTime t) {
    final now = DateTime.now();
    if (t.year == now.year && t.month == now.month && t.day == now.day) {
      return '${t.hour.toString().padLeft(2, '0')}:'
          '${t.minute.toString().padLeft(2, '0')}';
    }
    if (now.difference(t).inDays < 7) return '${t.month}/${t.day}';
    return '${t.year}/${t.month}/${t.day}';
  }
}


class _Field extends StatelessWidget {
  const _Field({
    required this.controller,
    required this.label,
    required this.hint,
  });
  final TextEditingController controller;
  final String label, hint;

  @override
  Widget build(BuildContext context) => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: NX.section),
          const SizedBox(height: NX.s2),
          TextField(
            controller: controller,
            style: NX.body,
            autofocus: label.startsWith('名字'),
            keyboardAppearance:
                NX.isDark ? Brightness.dark : Brightness.light,
            decoration: InputDecoration(
              hintText: hint,
              hintStyle: NX.body.copyWith(color: NX.text4),
              filled: true,
              fillColor: NX.fill,
              isDense: true,
              contentPadding: const EdgeInsets.symmetric(
                  horizontal: NX.s4, vertical: NX.s4),
              border: OutlineInputBorder(
                borderRadius: BorderRadius.circular(NX.rMd),
                borderSide: BorderSide.none,
              ),
              enabledBorder: OutlineInputBorder(
                borderRadius: BorderRadius.circular(NX.rMd),
                borderSide: BorderSide.none,
              ),
              focusedBorder: OutlineInputBorder(
                borderRadius: BorderRadius.circular(NX.rMd),
                borderSide: BorderSide(color: NX.accent, width: 1.5),
              ),
            ),
          ),
        ],
      );
}


class _MenuItem extends StatelessWidget {
  const _MenuItem({
    required this.icon,
    required this.label,
    required this.onTap,
    this.danger = false,
  });

  final IconData icon;
  final String label;
  final VoidCallback onTap;
  final bool danger;

  @override
  Widget build(BuildContext context) => InkWell(
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.symmetric(
              horizontal: NX.gutter, vertical: NX.s4),
          child: Row(children: [
            Icon(icon, size: 21, color: danger ? NX.bad : NX.text2),
            const SizedBox(width: NX.s4),
            Text(label,
                style: NX.body.copyWith(color: danger ? NX.bad : NX.text)),
          ]),
        ),
      );
}
