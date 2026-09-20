import 'package:flutter/material.dart';

import '../api/events.dart';
import '../theme.dart';

/// Avatar —— 由 id 确定性生成的一张脸.
///
/// **这是 packages/apps/console/src/view/Avatar.tsx 的移植**, 连哈希都一样:
/// 同一个 bot 在电脑上和手机上必须是同一只小家伙. 各生成各的话,
/// 用户在两块屏幕之间来回切的时候要重新认一遍人.
///
/// 客户端那三条照搬:
///
///	① **圆的**. 不是圆角方 —— 圆形读起来是"一个人", 圆角方是"一个应用图标".
///	② **不放文字**. 拿名字第一个字当头像是省事不是设计: 一排看过去
///	   全是字, 认不出谁是谁.
///	③ 状态是**角上一个点**, 不是一圈光. 头像只管身份.
class Avatar extends StatelessWidget {
  const Avatar({
    super.key,
    required this.id,
    this.size = NX.avatarLg,
    this.presence,
    this.ringColor,
  });

  final String id;
  final double size;

  /// presence 右下角那个点. null = 不画(用户自己那张脸不需要状态)
  final Presence? presence;

  /// ringColor 点外面那圈描边的颜色 —— **必须跟它压着的底色同色**,
  /// 否则点会糊在头像边缘上读不清
  final Color? ringColor;

  @override
  Widget build(BuildContext context) {
    final dotSize = (size * 8 / 34).clamp(7.0, 11.0);
    return SizedBox(
      width: size,
      height: size,
      child: Stack(
        clipBehavior: Clip.none,
        children: [
          ClipOval(
            child: SizedBox(
              width: size,
              height: size,
              child: CustomPaint(painter: _CreaturePainter(_Traits.of(id))),
            ),
          ),
          if (presence != null)
            Positioned(
              right: -1,
              bottom: -1,
              child: _Dot(
                presence: presence!,
                size: dotSize,
                ring: ringColor ?? NX.bg,
              ),
            ),
        ],
      ),
    );
  }
}

/// 头像上那个点 —— **整个界面上信息密度最高的一个像素**.
///
/// 一列 bot 扫一眼, 它是唯一告诉你"谁能使唤、谁在忙、谁需要我"的东西.
/// 五档各对应一个你会做的不同决定(判据在 api/events.dart 的 presenceOf).
///
/// **永远给一个点**, 包括"不在"那种(空心的): 不给点等于"不知道",
/// 而不知道和离线是两件事.
class _Dot extends StatefulWidget {
  const _Dot({required this.presence, required this.size, required this.ring});
  final Presence presence;
  final double size;
  final Color ring;

  @override
  State<_Dot> createState() => _DotState();
}

class _DotState extends State<_Dot> with SingleTickerProviderStateMixin {
  late final _c = AnimationController(
      vsync: this, duration: const Duration(milliseconds: 1600))
    ..repeat(reverse: true);

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final busy = widget.presence == Presence.busy;
    final color = switch (widget.presence) {
      Presence.online => NX.ok,
      // 忙碌换的是**色相**而不是亮度 —— 一列头像里靠亮度分辨太吃力,
      // 而且色盲的人分不出来. 呼吸只是第二重线索, 不是唯一那重
      Presence.busy => NX.accent,
      Presence.needsYou => NX.warn,
      Presence.failed => NX.bad,
      Presence.offline => NX.bgSubtle,
    };
    final dot = Container(
      width: widget.size,
      height: widget.size,
      decoration: BoxDecoration(
        color: color,
        shape: BoxShape.circle,
        border: widget.presence == Presence.offline
            // 不在: 空心 —— 它不该跟"在线"抢注意力, 但必须看得见
            ? Border.all(color: NX.text4, width: 1.5)
            : null,
        boxShadow: [
          BoxShadow(color: widget.ring, spreadRadius: 2, blurRadius: 0),
        ],
      ),
    );
    if (!busy) return dot;
    return FadeTransition(
      opacity: Tween(begin: 1.0, end: .45).animate(
          CurvedAnimation(parent: _c, curve: Curves.easeInOut)),
      child: dot,
    );
  }
}

/// 一只小家伙的特征. 耳朵/眼睛/嘴各三种, 十个色相 —— 组合够区分,
/// 而且同一个 id 永远是同一只, 不用存不用配
class _Traits {
  const _Traits(this.hue, this.ears, this.eyes, this.mouth);
  final double hue;
  final int ears, eyes, mouth;

  static const _hues = [206, 158, 266, 22, 322, 186, 240, 96, 44, 288];

  /// **哈希必须跟客户端一字不差**(FNV-1a + Math.imul 的 32 位语义),
  /// 否则同一个 bot 两边是两只不同的小家伙
  static _Traits of(String id) {
    var h = 2166136261;
    for (final c in id.codeUnits) {
      h = _i32(h ^ c);
      h = _i32(h * 16777619);
    }
    h = h.abs();
    return _Traits(
      _hues[h % _hues.length].toDouble(),
      (h >> 4) % 3, // 0 无 · 1 猫耳 · 2 天线
      (h >> 8) % 3, // 0 圆 · 1 竖椭圆 · 2 面罩
      (h >> 12) % 3, // 0 点 · 1 微笑 · 2 一横
    );
  }

  /// JS 的 `|0` / Math.imul 都是 32 位有符号截断. Dart 的 int 是 64 位,
  /// 不截的话第三个字符之后就跟客户端分道扬镳了
  static int _i32(int x) {
    x &= 0xFFFFFFFF;
    return x >= 0x80000000 ? x - 0x100000000 : x;
  }
}

class _CreaturePainter extends CustomPainter {
  _CreaturePainter(this.t);
  final _Traits t;

  @override
  void paint(Canvas canvas, Size size) {
    // 客户端的 viewBox 恒为 32×32, 缩放交给外面 —— 这里照做
    canvas.scale(size.width / 32, size.height / 32);

    final skin = HSLColor.fromAHSL(1, t.hue, .42, .62).toColor();
    final deep = HSLColor.fromAHSL(1, t.hue, .44, .34).toColor();
    final ink = HSLColor.fromAHSL(1, t.hue, .46, .17).toColor();
    final p = Paint()..isAntiAlias = true;

    // 底: 圆里要填满, 不然 ClipOval 之后四角是透明的
    canvas.drawRect(const Rect.fromLTWH(0, 0, 32, 32),
        p..color = HSLColor.fromAHSL(1, t.hue, .30, .22).toColor());

    p.color = deep;
    if (t.ears == 1) {
      canvas.drawPath(_tri(8, 11, 9.5, 4.5, 15, 9), p);
      canvas.drawPath(_tri(24, 11, 22.5, 4.5, 17, 9), p);
    } else if (t.ears == 2) {
      canvas.drawRRect(
          RRect.fromLTRBR(15.2, 3.2, 16.8, 8.2, const Radius.circular(.8)), p);
      canvas.drawCircle(const Offset(16, 3), 2, p);
    }

    // 头
    canvas.drawRRect(
        RRect.fromLTRBR(6.5, 8.5, 25.5, 25.5,
            Radius.circular(t.ears == 2 ? 5.5 : 8.5)),
        p..color = skin);

    if (t.eyes == 2) {
      canvas.drawRRect(
          RRect.fromLTRBR(9.5, 13.5, 22.5, 19, const Radius.circular(2.75)),
          p..color = ink);
      p.color = skin;
      canvas.drawCircle(const Offset(13, 16.2), 1.05, p);
      canvas.drawCircle(const Offset(19, 16.2), 1.05, p);
    } else {
      p.color = ink;
      final rx = t.eyes == 1 ? 1.5 : 1.9;
      canvas.drawOval(
          Rect.fromCenter(
              center: const Offset(12.6, 16.4), width: rx * 2, height: 4.2),
          p);
      canvas.drawOval(
          Rect.fromCenter(
              center: const Offset(19.4, 16.4), width: rx * 2, height: 4.2),
          p);
    }

    switch (t.mouth) {
      case 0:
        canvas.drawCircle(const Offset(16, 21), 1.15,
            p..color = ink.withValues(alpha: .75));
      case 1:
        canvas.drawPath(
            Path()
              ..moveTo(13.4, 20.4)
              ..quadraticBezierTo(16, 22.8, 18.6, 20.4),
            Paint()
              ..color = ink.withValues(alpha: .8)
              ..style = PaintingStyle.stroke
              ..strokeWidth = 1.4
              ..strokeCap = StrokeCap.round
              ..isAntiAlias = true);
      default:
        canvas.drawRRect(
            RRect.fromLTRBR(13.6, 20.5, 18.4, 21.9, const Radius.circular(.7)),
            p..color = ink.withValues(alpha: .7));
    }
  }

  Path _tri(double ax, double ay, double bx, double by, double cx, double cy) =>
      Path()
        ..moveTo(ax, ay)
        ..lineTo(bx, by)
        ..lineTo(cx, cy)
        ..close();

  @override
  bool shouldRepaint(_CreaturePainter old) => old.t.hue != t.hue;
}
