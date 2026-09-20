import 'package:flutter/material.dart';

/// 动效的一套值 —— **跟颜色字号一样, 只能从这儿取**.
///
/// ── 为什么动效也要立标尺 ──
///
/// 时长和曲线各写各的话, 后果跟字号各写各的一样: 每个组件的手感
/// 都差一点点, 而"差一点点"人是感觉得到的 —— 它读起来是"这个 App
/// 有点卡", 而不是"这两处时长不一样".
///
/// 三档时长, 一条主曲线:
///
///	fast  120ms  按下的反馈 · 图标切换 —— 快到几乎察觉不到延迟
///	base  220ms  页面转场 · 卡片进出 —— 看得见但不用等
///	slow  400ms  第一次出现的那种铺开
///
/// 曲线用 easeOutCubic: **出场快、收尾慢**. 人对"开始"敏感对"结束"
/// 不敏感, 所以前半段要立刻动起来, 后半段慢慢停 —— 反过来(easeIn)
/// 会让人觉得点了没反应.
class Mo {
  Mo._();

  static const fast = Duration(milliseconds: 120);
  static const base = Duration(milliseconds: 220);
  static const slow = Duration(milliseconds: 400);

  static const curve = Curves.easeOutCubic;

  /// 回弹 —— 只给"发出去了"这种一次性的成功反馈用.
  /// 到处用回弹会显得廉价
  static const spring = Curves.easeOutBack;
}

/// 页面转场 —— 从右侧滑入 + 淡入.
///
/// Flutter 默认在安卓上是"从下往上淡入", 那是 Material 的语言;
/// 而这个 App 的层级是**横向**的(列表 → 会话 → 资料), 横向滑入
/// 才说得出"我进到里面一层了".
class SlideRoute<T> extends PageRouteBuilder<T> {
  SlideRoute({required WidgetBuilder builder})
      : super(
          transitionDuration: Mo.base,
          reverseTransitionDuration: Mo.base,
          pageBuilder: (c, a, b) => builder(c),
          transitionsBuilder: (c, a, b, child) {
            final slide = Tween(begin: const Offset(.18, 0), end: Offset.zero)
                .chain(CurveTween(curve: Mo.curve))
                .animate(a);
            return SlideTransition(
              position: slide,
              // 淡入只走前半段: 全程淡入会让内容在移动时还是半透明的,
              // 看着像没加载完
              child: FadeTransition(
                opacity: CurvedAnimation(parent: a, curve: const Interval(0, .6)),
                child: child,
              ),
            );
          },
        );
}

/// 新出现的一条消息: 从下方微微抬起 + 淡入.
///
/// **只在第一次出现时播一次**. 流式消息每来一块都重建, 如果每次
/// 都播动画, 那条正在长的消息会一直在抖 —— 所以动画绑在
/// widget 的生命周期上, 而不是绑在内容变化上.
class RiseIn extends StatefulWidget {
  const RiseIn({super.key, required this.child, this.enabled = true});
  final Widget child;

  /// enabled 补历史的时候关掉 —— 一次补进来五十条,
  /// 五十个动画同时播是一片乱抖
  final bool enabled;

  @override
  State<RiseIn> createState() => _RiseInState();
}

class _RiseInState extends State<RiseIn>
    with SingleTickerProviderStateMixin {
  late final AnimationController _c = AnimationController(
    vsync: this,
    duration: Mo.base,
    // 关掉的时候直接停在终点, 而不是不建控制器 ——
    // 少一个分支就少一处会不一致的地方
    value: widget.enabled ? 0 : 1,
  );

  @override
  void initState() {
    super.initState();
    if (widget.enabled) _c.forward();
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
        animation: _c,
        builder: (_, child) {
          final t = Mo.curve.transform(_c.value);
          return Opacity(
            opacity: t,
            // 抬 8px, 不是 30px: 大幅位移在一条列表里会让上下几行
            // 看着都在动
            child: Transform.translate(offset: Offset(0, 8 * (1 - t)), child: child),
          );
        },
        child: widget.child,
      );
}

/// 按下去会缩一下 —— 给那些"整块可点"的东西用.
///
/// 手机上没有 hover, **按下的那一下是唯一的反馈**. 没有它,
/// 用户不确定自己点上了没有, 于是会点第二次
class Pressable extends StatefulWidget {
  const Pressable({super.key, required this.child, this.onTap});
  final Widget child;
  final VoidCallback? onTap;

  @override
  State<Pressable> createState() => _PressableState();
}

class _PressableState extends State<Pressable> {
  bool _down = false;

  @override
  Widget build(BuildContext context) => GestureDetector(
        onTapDown: widget.onTap == null ? null : (_) => setState(() => _down = true),
        onTapUp: (_) => setState(() => _down = false),
        onTapCancel: () => setState(() => _down = false),
        onTap: widget.onTap,
        child: AnimatedScale(
          // 0.97 而不是 0.9: 缩太多会让人觉得这个东西"软",
          // 而且在小控件上会糊
          scale: _down ? .97 : 1,
          duration: Mo.fast,
          curve: Mo.curve,
          child: widget.child,
        ),
      );
}
