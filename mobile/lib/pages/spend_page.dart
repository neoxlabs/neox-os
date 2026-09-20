import 'package:flutter/material.dart';

import '../api/os_client.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/panel.dart';

/// 花了多少 —— 照网页端那一页.
///
/// ── 这一页存在的理由 ──
///
/// 一群 bot 在替你干活, 而**它们烧的是真钱**. 不给一个地方看,
/// 用户对成本的唯一感知就是月底账单 —— 那时候已经晚了.
///
/// 数字全部来自事件账本, 不另记一份: 另记一份就会有"账本说 A、
/// 这里说 B"的那天, 而那时候没人知道该信哪个.
class SpendPage extends StatefulWidget {
  const SpendPage({super.key, required this.state});
  final AppState state;

  @override
  State<SpendPage> createState() => _SpendPageState();
}

class _SpendPageState extends State<SpendPage> {
  List<Spend>? _rows;
  String? _err;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final r = await widget.state.client!.spend();
      // 贵的在前 —— 这一页要回答的是"钱花在谁身上了"
      r.sort((a, b) => b.total.compareTo(a.total));
      if (mounted) setState(() => _rows = r);
    } catch (e) {
      if (mounted) setState(() => _err = '$e');
    }
  }

  @override
  Widget build(BuildContext context) {
    final rows = _rows;
    return Scaffold(
      backgroundColor: NX.bg,
      body: Column(children: [
        PageBar(
          title: '花了多少',
          action: IconButton(
            onPressed: () {
              setState(() {
                _rows = null;
                _err = null;
              });
              _load();
            },
            icon: const Icon(Icons.refresh_rounded, size: 20),
            color: NX.text3,
          ),
        ),
        Expanded(
          child: _err != null
              ? Center(
                  child: Padding(
                    padding: const EdgeInsets.all(NX.s7),
                    child: Text(_err!,
                        style: NX.bodyDim, textAlign: TextAlign.center),
                  ),
                )
              : rows == null
                  ? const Center(child: CircularProgressIndicator())
                  : _body(rows),
        ),
      ]),
    );
  }

  Widget _body(List<Spend> rows) {
    // ── 花过的和没花过的分开 ──
    //
    // 原来一视同仁地排下去, 于是"发版 0""回归 0""文案 0"各占一整行,
    // 每行底下还拖着一条空的灰槽 —— 那条槽长得就像还没加载完.
    // 而**零本来就不需要一行**: 它要回答的只是"这几个还没动过",
    // 一句话的事
    final spent = rows.where((r) => r.total > 0).toList();
    final idle = rows.where((r) => r.total == 0).toList();

    return ListView(
      padding: const EdgeInsets.fromLTRB(NX.gutter, NX.s3, NX.gutter, NX.s7),
      children: [
        _Hero(rows: rows),
        Sec(
          label: '按人 · 贵的在前',
          foot: idle.isEmpty
              ? null
              : '另外 ${idle.length} 个还没花过：'
                  '${idle.map((r) => r.bot).join('、')}',
          children: [
            if (spent.isEmpty)
              const Cell(title: '还没有人花过钱')
            else
              for (final r in spent) _Row(row: r, top: spent.first.total),
          ],
        ),
      ],
    );
  }
}

/// 顶上那一块 —— 一个大数, 一条分段条, 三行图例.
///
/// ── 为什么是一条分段条而不是三张卡 ──
///
/// 三个数并排的时候, 它们看着像三件互不相干的事; 而**它们其实是同一
/// 笔钱的三个切片**: 吐出来的 + 命中的 + 剩下那截送进去的 = 一共.
/// 一条按比例切开的横条把这层关系直接画出来了 —— 缓存命中那一段有多
/// 长, 就是这个月的钱有多少是便宜的.
class _Hero extends StatelessWidget {
  const _Hero({required this.rows});
  final List<Spend> rows;

  @override
  Widget build(BuildContext context) {
    var pin = 0, pout = 0, cached = 0, turns = 0, calls = 0;
    for (final r in rows) {
      pin += r.prompt;
      pout += r.completion;
      cached += r.cached;
      turns += r.turns;
      calls += r.calls;
    }
    final all = pin + pout;
    final fresh = (pin - cached).clamp(0, pin); // 送进去里没命中的那截
    final hit = pin == 0 ? 0 : (cached * 100 / pin).round();

    return Container(
      margin: const EdgeInsets.only(bottom: NX.s6),
      padding: const EdgeInsets.all(NX.s4),
      decoration: BoxDecoration(
        color: NX.fill,
        borderRadius: BorderRadius.circular(NX.rMd),
      ),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        // 大数跟单位坐同一条基线 —— 差一点点都看得出来
        Row(crossAxisAlignment: CrossAxisAlignment.baseline,
            textBaseline: TextBaseline.alphabetic, children: [
          Text(k(all),
              style: TextStyle(
                  fontSize: 34,
                  height: 1,
                  color: NX.text,
                  fontWeight: NX.wBold,
                  fontFeatures: const [FontFeature.tabularFigures()])),
          const SizedBox(width: NX.s2),
          Text('token', style: NX.label.copyWith(color: NX.text3)),
          const Spacer(),
          Text('$turns 轮 · $calls 次调用',
              style: NX.caption.copyWith(color: NX.text3)),
        ]),
        const SizedBox(height: NX.s4),
        _Bar(parts: [
          (cached, NX.ok),
          (fresh, NX.accent),
          (pout, NX.accent.withValues(alpha: .35)),
        ]),
        const SizedBox(height: NX.s3),
        _Leg(color: NX.ok, label: '缓存命中', v: k(cached), note: '送进去的 $hit%'),
        _Leg(color: NX.accent, label: '送进去', v: k(fresh), note: '没命中的那截'),
        _Leg(
            color: NX.accent.withValues(alpha: .35),
            label: '吐出来',
            v: k(pout),
            note: '最贵的那部分'),
      ]),
    );
  }

  /// 千分位收成 k. 五位数并排看不出大小, 收成 k 之后一眼就分得开
  static String k(int n) =>
      n >= 1000 ? '${(n / 1000).toStringAsFixed(n >= 10000 ? 0 : 1)}k' : '$n';
}

/// 一条按比例切开的横条. 段与段之间留一道缝, 不然两段同色系的会糊在一起
class _Bar extends StatelessWidget {
  const _Bar({required this.parts});
  final List<(int, Color)> parts;

  @override
  Widget build(BuildContext context) {
    final total = parts.fold<int>(0, (s, p) => s + p.$1);
    if (total == 0) {
      return Container(
        height: 8,
        decoration: BoxDecoration(
          color: NX.fill2,
          borderRadius: BorderRadius.circular(4),
        ),
      );
    }
    return SizedBox(
      height: 8,
      child: Row(children: [
        for (final (i, p) in parts.indexed)
          if (p.$1 > 0) ...[
            if (i > 0) const SizedBox(width: 2),
            Expanded(
              flex: p.$1,
              child: DecoratedBox(
                decoration: BoxDecoration(
                  color: p.$2,
                  borderRadius: BorderRadius.circular(4),
                ),
              ),
            ),
          ],
      ]),
    );
  }
}

/// 图例一行: 色点 · 名字 · 数 · 一句注. **数右对齐**, 三行的数字要成一列
class _Leg extends StatelessWidget {
  const _Leg({
    required this.color,
    required this.label,
    required this.v,
    required this.note,
  });
  final Color color;
  final String label, v, note;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.only(top: NX.s2),
        child: Row(children: [
          Container(
            width: 8,
            height: 8,
            decoration: BoxDecoration(color: color, shape: BoxShape.circle),
          ),
          const SizedBox(width: NX.s2),
          Text(label, style: NX.caption.copyWith(color: NX.text2)),
          const SizedBox(width: NX.s2),
          Expanded(
            child: Text(note,
                style: NX.caption.copyWith(color: NX.text4),
                maxLines: 1,
                overflow: TextOverflow.ellipsis),
          ),
          Text(v,
              style: NX.caption.copyWith(
                  color: NX.text2,
                  fontFeatures: const [FontFeature.tabularFigures()])),
        ]),
      );
}

class _Row extends StatelessWidget {
  const _Row({required this.row, required this.top});
  final Spend row;

  /// top 最贵那个的数 —— 条长按它归一化. **不按总和**: 按总和的话
  /// 人一多每条都短得看不出差别, 而这一页要比的是彼此
  final int top;

  @override
  Widget build(BuildContext context) {
    final ratio = top == 0 ? 0.0 : row.total / top;
    return Padding(
      padding: const EdgeInsets.fromLTRB(NX.s4, NX.s3, NX.s4, NX.s3),
      child: Column(children: [
        Row(crossAxisAlignment: CrossAxisAlignment.baseline,
            textBaseline: TextBaseline.alphabetic, children: [
          Expanded(
            child: Text(row.bot,
                style: NX.body, maxLines: 1, overflow: TextOverflow.ellipsis),
          ),
          const SizedBox(width: NX.s2),
          Text(_Hero.k(row.total),
              style: NX.body.copyWith(
                  fontWeight: NX.wMed,
                  fontFeatures: const [FontFeature.tabularFigures()])),
        ]),
        const SizedBox(height: NX.s2),
        // 一条比例条 —— **比数字快**: 谁最贵一眼就看出来,
        // 不用在几个五位数之间比大小.
        // 里面深的那截是命中的部分: 同样一条长条, 命中多的那条更便宜
        LayoutBuilder(builder: (_, c) {
          final w = c.maxWidth * ratio;
          final hit = row.prompt == 0 ? 0.0 : row.cached / row.prompt;
          return Stack(children: [
            Container(
              height: 4,
              decoration: BoxDecoration(
                color: NX.fill2,
                borderRadius: BorderRadius.circular(2),
              ),
            ),
            Container(
              height: 4,
              width: w,
              decoration: BoxDecoration(
                color: NX.accent.withValues(alpha: .35),
                borderRadius: BorderRadius.circular(2),
              ),
            ),
            Container(
              height: 4,
              width: w * hit,
              decoration: BoxDecoration(
                color: NX.accent,
                borderRadius: BorderRadius.circular(2),
              ),
            ),
          ]);
        }),
        const SizedBox(height: NX.s2),
        Align(
          alignment: Alignment.centerLeft,
          child: Text(
            '${row.turns} 轮 · ${row.calls} 次调用'
            '${row.cached > 0 ? " · 缓存省了 ${_Hero.k(row.cached)}" : ""}',
            style: NX.caption.copyWith(color: NX.text4),
          ),
        ),
      ]),
    );
  }
}
