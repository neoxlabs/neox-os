import 'dart:math' as math;
import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/scheduler.dart';

import '../theme.dart';

/// 核心 —— 一颗由几千颗粒子铺成的球, 表面跑着波.
///
/// ── 前两版错在哪 ──
///
///	第一版  60 秒 × 0.35 圈 = 171 秒转一圈. 技术上在动, 肉眼是张静图
///	第二版  连线画成了网, 于是它是个**线框球**: 一眼看见的是"结构",
///	        而且蓝色线在黑底上又灰又糊
///
/// 参考的那种(混沌球 / 声音球)长的完全不是这样: **没有一根线**,
/// 只有极密的点铺满球面, 靠明暗和疏密读出体积; 表面有一层层的波在跑,
/// 说话时波变大. 线是画不出这个的 —— 线一多就成了网, 点再多也还是面.
///
/// ── 三件事撑起它 ──
///
///	密度   3600 颗点. 少了是星空, 多了才是"一个面"
///	波     几层不同频率的正弦叠出来的径向起伏, 音量决定幅度
///	对比   点是青白色的, 底是纯黑 —— 亮的地方接近纯白.
///	       上一版最亮处才 .78 的蓝, 那是"能看见"不是"好看"
class NeuralCore extends StatefulWidget {
  const NeuralCore({
    super.key,
    required this.level,
    required this.thinking,
    this.online = true,
    this.speaking = false,
  });

  /// speaking 它正在说话.
  ///
  /// ── 为什么这一位单独存在 ──
  ///
  /// 一颗只会"听"的球是死的: 它讲话的时候屏幕上什么都不变, 于是
  /// 用户看到的是一个哑巴对着他念字。
  ///
  /// **律动必须是它自己的, 不是麦克风的**: 听的时候 level 来自
  /// 用户的嗓门, 说的时候没有输入可测 —— 那时候的起伏要自己生成,
  /// 否则一开口球就塌成一条直线。
  final bool speaking;

  /// level 麦克风音量 0..1
  final double level;

  /// thinking 屋里几个 bot 在跑
  final int thinking;

  /// online 这条线通不通. 断了整颗褪成灰 ——
  /// **一颗照常发光的球会让人以为它还在听**
  final bool online;

  @override
  State<NeuralCore> createState() => _NeuralCoreState();
}

class _NeuralCoreState extends State<NeuralCore>
    with SingleTickerProviderStateMixin {
  late final Ticker _ticker;

  /// 单位球面上的点 —— **只算一次**, 每帧只做形变和投影
  late final Float32List _ux, _uy, _uz;
  static const _n = 3600;

  double _t = 0;
  Duration _last = Duration.zero;
  double _smooth = 0;

  /// 音量历史, 外圈波形环读它
  final _levels = Float32List(_ticks);
  int _head = 0;
  double _acc = 0;
  static const _ticks = 140;

  @override
  void initState() {
    super.initState();
    _ux = Float32List(_n);
    _uy = Float32List(_n);
    _uz = Float32List(_n);
    // 斐波那契球 —— 点在球面上铺得最均匀的排法.
    // 随机撒点会有肉眼可见的疏密团块(而这一版全靠疏密读体积,
    // 团块会被当成波); 经纬网格则在两极挤成一堆
    const golden = math.pi * (3 - 2.2360679); // π(3−√5)
    // **加一点固定抖动**. 纯斐波那契球在屏幕上会显出那条生成螺旋 ——
    // 它会在球体左半边形成一道道纹路. 球体全靠疏密读体积,
    // 一道规则的纹路会被眼睛当成"表面的结构".
    //
    // 抖动是**固定的**(定种子, 只算一次), 不是每帧随机: 每帧随机
    // 的话整颗球会沙沙地抖, 那是噪点不是材质
    final rnd = math.Random(19);
    for (var i = 0; i < _n; i++) {
      final y = 1 - 2 * i / (_n - 1);
      final r = math.sqrt(math.max(0, 1 - y * y));
      final th = golden * i;
      var x = math.cos(th) * r, yy = y, z = math.sin(th) * r;
      const j = .022;
      x += (rnd.nextDouble() - .5) * j;
      yy += (rnd.nextDouble() - .5) * j;
      z += (rnd.nextDouble() - .5) * j;
      final len = math.sqrt(x * x + yy * yy + z * z);
      _ux[i] = x / len;
      _uy[i] = yy / len;
      _uz[i] = z / len;
    }
    _ticker = createTicker(_tick)..start();
  }

  void _tick(Duration now) {
    final dt = ((now - _last).inMicroseconds / 1e6).clamp(0.0, 0.05);
    _last = now;
    _t += dt;
    // 上升快、下降慢 —— 说话时立刻响应, 停下时缓缓落回去.
    // 对称的平滑会让它在句子之间一顿一顿的
    // ── 它说话时的律动是**自己生成的** ──
    //
    //	听的时候 level 来自用户的嗓门; 说的时候没有输入可测 ——
    //	直接用 level 的话, 它一开口球就塌成一条直线, 而屏幕上看着
    //	像它死了。
    //
    //	三个不同周期的正弦叠起来: 单一正弦是机械的心跳, 叠三个
    //	(1.9 / 3.7 / 7.3 Hz, 互质) 才有说话那种忽快忽慢的起伏。
    //	底噪 0.28 保证它永远不塌到零 —— 那是"还在说"和"说完了"
    //	的区别。
    var want = widget.level;
    if (widget.speaking) {
      final a = math.sin(_t * 1.9) * .5 + .5;
      final b = math.sin(_t * 3.7 + 1.3) * .5 + .5;
      final c = math.sin(_t * 7.3 + 2.1) * .5 + .5;
      want = (.28 + (a * .45 + b * .3 + c * .25) * .5).clamp(0.0, 1.0);
    }
    final k = want > _smooth
        ? 1 - math.pow(0.02, dt)
        : 1 - math.pow(0.55, dt);
    _smooth += (want - _smooth) * k;

    // 波形环按固定节奏推进, 不是每帧一格 —— 每帧一格的话环的转速
    // 会跟着帧率变, 掉帧时波形整个慢下来
    _acc += dt;
    while (_acc >= 1 / 60) {
      _acc -= 1 / 60;
      _head = (_head + 1) % _ticks;
      _levels[_head] = _smooth;
    }
    setState(() {});
  }

  @override
  void dispose() {
    _ticker.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => RepaintBoundary(
        child: CustomPaint(
          painter: _CorePainter(
            t: _t,
            level: _smooth,
            thinking: widget.thinking,
            online: widget.online,
            ux: _ux,
            uy: _uy,
            uz: _uz,
            levels: _levels,
            head: _head,
            dark: NX.isDark,
          ),
          size: Size.infinite,
        ),
      );
}

class _CorePainter extends CustomPainter {
  _CorePainter({
    required this.t,
    required this.level,
    required this.thinking,
    required this.online,
    required this.ux,
    required this.uy,
    required this.uz,
    required this.levels,
    required this.head,
    required this.dark,
  });

  final double t, level;
  final int thinking;
  final bool online, dark;
  final Float32List ux, uy, uz, levels;
  final int head;

  /// 亮度分档.
  ///
  /// 三千多颗点逐颗 drawCircle 是三千次绘制调用, 手机上撑不住 60 帧.
  /// 按亮度分档、每档用 drawRawPoints 一次画完 —— 调用数从三千降到十,
  /// 而**看上去没区别**: 亮度本来就是连续的, 分十档人眼分不出来
  static const _bands = 10;

  @override
  void paint(Canvas canvas, Size size) {
    final c = Offset(size.width / 2, size.height / 2);
    final base = math.min(size.width, size.height) * .33;

    _ring(canvas, c, base);

    // ── 转 ──
    //
    // 单位是**圈每秒**, 不是某个控制器的 0..1: 一眼看得出快慢,
    // 改一个数就是改一个能说清的量
    final spin = 1 + thinking * .5 + level * .3;
    final a = t * 2 * math.pi / 26 * spin;
    final cosA = math.cos(a), sinA = math.sin(a);
    // 固定倾角 + 极慢的章动: 正对着看球面是个圆盘, 看不出是立体的;
    // 完全不变的倾角又会让它像在一个平面里干转
    final tilt = .38 + math.sin(t * .13) * .09;
    final cosT = math.cos(tilt), sinT = math.sin(tilt);

    // 波的幅度: 待命时也有一点(它活着, 在呼吸), 说话时明显鼓起来
    final amp = .045 + level * .16 + thinking * .012;
    final n = ux.length;

    // 每档一个点缓冲. 定长分配再按实际数量切片 ——
    // 每帧 new 十个 List 会把 GC 拉起来, 而这一页会长时间开着
    final buf = List.generate(_bands, (_) => Float32List(n * 2));
    final cnt = List.filled(_bands, 0);

    for (var i = 0; i < n; i++) {
      var x = ux[i], y = uy[i], z = uz[i];

      // ── 波段 ──
      //
      // 几层不同频率、不同轴向的正弦叠加. 用正弦而不是噪声:
      // **要的正是"一圈一圈的带"**, 而噪声给的是随机的疙瘩 ——
      // 参考里那种一层层扫过表面的波, 本来就是低频的驻波
      final w = math.sin(y * 5.5 + t * 1.7) * .55 +
          math.sin(x * 4.2 - t * 1.15) * .30 +
          math.sin(z * 7.0 + t * 2.3) * .22 +
          math.sin((x + y + z) * 9.0 - t * 3.1) * .12;
      final r = 1 + w * amp;

      x *= r;
      y *= r;
      z *= r;

      // 转 + 倾
      final x1 = x * cosA + z * sinA;
      final z1 = -x * sinA + z * cosA;
      final y2 = y * cosT - z1 * sinT;
      final z2 = y * sinT + z1 * cosT;

      // ── 亮度: 边缘亮, 中心暗 ──
      //
      // **这是空心球壳该有的样子**, 也是参考里那颗球最明显的特征:
      // 均匀铺在球面上的点投到平面之后, 越靠近轮廓越挤(球面在那儿
      // 沿着视线方向被压扁), 于是边缘自然亮成一圈, 中间是透的.
      //
      // 上一版按 depth² 给亮度, 于是最靠近观察者的那片(正中央)
      // 最亮 —— 屏幕上是中心一团白, 看着像颗实心的灯泡,
      // 完全不是球壳.
      //
      // rim = 这个点离视线轴多远(单位球上 0..1)
      final rim = math.sqrt(x1 * x1 + y2 * y2) / (r == 0 ? 1 : r);
      // 2.6 次方: 亮度要**贴着轮廓**才收得住. 次数小了整颗球都在发亮,
      // 中间那口"空"就没了
      final crest = w * .5 + .5; // 0..1, 波峰
      var b = math.pow(rim.clamp(0.0, 1.0), 2.6) * .92 + crest * .26 + .04;
      // 背面压暗但不抹掉: 抹掉的话球没有厚度, 留着才看得出
      // "透过前面看到后面"
      if (z2 < 0) b *= .46;
      b = b.clamp(0.0, 1.0).toDouble();

      final band = (b * (_bands - 1)).round();
      final k = cnt[band] * 2;
      buf[band][k] = c.dx + x1 * base;
      buf[band][k + 1] = c.dy + y2 * base;
      cnt[band]++;
    }

    // ── 画 ──
    //
    // 从暗到亮: 亮点要压在暗点上面, 反过来的话最亮的那些会被
    // 背面的暗点糊掉
    final p = Paint()
      ..strokeCap = StrokeCap.round
      ..isAntiAlias = true;
    for (var b = 0; b < _bands; b++) {
      if (cnt[b] == 0) continue;
      final f = b / (_bands - 1);
      p
        // 亮的点也大一点 —— 只靠亮度的话高光不够"实"
        ..strokeWidth = 1.15 + f * 1.5
        ..color = _dot(f);
      canvas.drawRawPoints(
          ui.PointMode.points, buf[b].sublist(0, cnt[b] * 2), p);
    }

    _bloom(canvas, c, base);
  }

  /// 点的颜色.
  ///
  /// **青 → 白**, 不是蓝. 蓝在纯黑上是"暗的彩色", 怎么调都发灰;
  /// 青绿的明度本来就高, 到了顶档直接接近纯白 —— 对比度是这么来的.
  ///
  /// 顶上那一档掺一点品红: 参考里那颗球的高光带着一丝洋红,
  /// 那一点点色相偏移让高光看起来是**发光**而不是"更白的白"
  Color _dot(double f) {
    if (!online) return NX.text4.withValues(alpha: .25 + f * .5);
    if (dark) {
      const low = Color(0xFF0E5C6B);   // 暗处: 深青
      const mid = Color(0xFF2FE0DA);   // 中段: 亮青
      const high = Color(0xFFEAFDFF);  // 高光: 近白
      if (f < .55) {
        return Color.lerp(low, mid, f / .55)!
            .withValues(alpha: .30 + f * .95);
      }
      final c = Color.lerp(mid, high, (f - .55) / .45)!;
      // 最顶上一点点洋红
      return Color.lerp(c, const Color(0xFFFF7AE0), (f - .82).clamp(0, 1) * .5)!
          .withValues(alpha: .85 + f * .15);
    }
    // 浅色主题: 反过来 —— 白底上要暗的点, 亮度轴整个翻转
    const low = Color(0xFFBFE9EC);
    const mid = Color(0xFF1C9AA8);
    const high = Color(0xFF06323C);
    final c = f < .55
        ? Color.lerp(low, mid, f / .55)!
        : Color.lerp(mid, high, (f - .55) / .45)!;
    return c.withValues(alpha: .35 + f * .65);
  }

  /// 外圈: 一道细环 + 一圈随音量长短的刻度 + 两段慢转的弧.
  ///
  /// 刻度环和波形环合成一样 —— **刻度的长度就是那一刻的音量**.
  /// 它既是仪表的刻度又是你说过的话的形状, 而且不用多画一个圈
  void _ring(Canvas canvas, Offset c, double base) {
    final r = base * 1.34;
    final line = online
        ? (dark ? const Color(0xFF2FE0DA) : const Color(0xFF1C9AA8))
        : NX.text4;

    canvas.drawCircle(
        c,
        r,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = .7
          ..isAntiAlias = true
          ..color = line.withValues(alpha: .18));

    if (online) {
      final arc = Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1.5
        ..strokeCap = StrokeCap.round
        ..isAntiAlias = true
        ..color = line.withValues(alpha: .45);
      final a0 = t * .42;
      for (final off in [0.0, math.pi]) {
        canvas.drawArc(Rect.fromCircle(center: c, radius: r * 1.08), a0 + off,
            .5, false, arc);
      }
      canvas.drawArc(
          Rect.fromCircle(center: c, radius: r * 1.15),
          -t * .28,
          1.4,
          false,
          Paint()
            ..style = PaintingStyle.stroke
            ..strokeWidth = .9
            ..strokeCap = StrokeCap.round
            ..isAntiAlias = true
            ..color = line.withValues(alpha: .26));
    }

    final n = levels.length;
    final p = Paint()
      ..strokeCap = StrokeCap.round
      ..isAntiAlias = true;
    for (var i = 0; i < n; i++) {
      // 从 head 往回读: 最新的音量画在正上方(12 点), 越老越往回绕.
      // 顺时针 = 时间流向, 跟人读表的方向一致
      final v = levels[(head - i + n * 2) % n];
      final ang = -math.pi / 2 + i * 2 * math.pi / n;
      // 底噪也要有一小截: 全无声时一圈什么都没有的话,
      // "很安静"跟"麦克风坏了"长得一模一样
      final len = base * (.025 + v * .26);
      final ca = math.cos(ang), sa = math.sin(ang);
      p
        ..strokeWidth = 1.15
        ..color = line.withValues(alpha: .2 + v * .7);
      canvas.drawLine(Offset(c.dx + ca * r, c.dy + sa * r),
          Offset(c.dx + ca * (r + len), c.dy + sa * (r + len)), p);
    }
  }

  /// 整颗球外面一层极淡的辉光.
  ///
  /// 用 blur 而不是 RadialGradient: 渐变的边缘在纯黑底上会有一圈
  /// 看得见的色带(8 位色深不够), 模糊没有这个问题.
  ///
  /// **只在外圈, 不在中心**: 参考里那颗球中心是暗的(球体内部),
  /// 中心打光会把它变成一颗灯泡
  void _bloom(Canvas canvas, Offset c, double r) {
    if (!online || !dark) return;
    canvas.drawCircle(
        c,
        r * .95,
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = r * .42
          ..color = const Color(0xFF2FE0DA)
              .withValues(alpha: .07 + level * .10)
          ..maskFilter = ui.MaskFilter.blur(ui.BlurStyle.normal, r * .32));
  }

  @override
  bool shouldRepaint(_CorePainter old) => true;
}
