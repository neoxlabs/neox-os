import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';

import '../api/events.dart' as ev;
import '../state/app_state.dart';
import '../theme.dart';
import 'rich.dart';

/// 卡片 —— bot 的多模态出口，画成微信那种一块一块的东西。
///
/// ── 为什么手机端非补齐不可 ──
///
/// OS 那边一共发十六种卡（见 go/agent/show.go 和桌面端 view/widgets.tsx），
/// 而手机端原来只认三种。剩下的落到通用卡里，**列表和嵌套对象被直接
/// `toString()`** —— 用户看到的是一行 `[{label: …, value: …}]`。
///
/// 屏幕上是一段 JSON，而 bot 收到的是「已经展示了」，于是它接着说
/// 「进度如上」。没有任何一处报错。
///
/// ── 补哪几种：手机上真用得上的 ──
///
///	map/image/gallery  用一段文字讲不清的东西，本来就是卡片存在的理由
///	progress           它汇报干到哪儿了。done/total 也认 —— 那是模型
///	                   最自然的写法，只认 percent 会画出「空条 + 0%」
///	table/code/diff    横向能滚，**不折行**：一张对不齐的表比没有更糟
///	doc/weather/file   一行标题一段正文的那几种
///	choice             只画不点：答一句话的通道在聊天框里，
///	                   而画成按钮却按不下去比画成文字糟
///
/// 剩下的（video/audio/form）留给通用卡：手机上它们要么播不了，
/// 要么需要一条这边还没有的回话通道。
///
/// ── 认不出的种类也要画 ──
///
/// OS 那边允许 bot 发表外的种类，界面照字段排一张通用卡。丢掉的话，
/// 用户什么都看不见，而 bot 收到的是「已经展示了」。
class CardView extends StatelessWidget {
  const CardView({super.key, required this.card, required this.state});
  final ev.Card card;
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final wide = MediaQuery.of(context).size.width * .74;
    // 表格和代码要宽一点 —— 挤在 74% 里横滚两下才看得完一列
    final roomy = const {'table', 'code', 'diff'}.contains(card.type);
    return ConstrainedBox(
      constraints: BoxConstraints(
          maxWidth: roomy ? MediaQuery.of(context).size.width * .88 : wide),
      child: Container(
        decoration: BoxDecoration(
          color: NX.bgElevated,
          borderRadius: BorderRadius.circular(NX.rMd),
          // 卡片要跟气泡分得开 —— 它是"一件东西", 不是一句话
          border: Border.all(color: NX.line),
        ),
        clipBehavior: Clip.antiAlias,
        child: switch (card.type) {
          'map' => _Map(card: card, state: state),
          'link' => _Link(card: card),
          'image' => _Image(card: card, state: state),
          'gallery' => _Gallery(card: card, state: state),
          'progress' => _Progress(card: card),
          'table' => _Table(card: card),
          'code' || 'diff' => _Code(card: card),
          'doc' => _Doc(card: card),
          'weather' => _Weather(card: card),
          'file' => _File(card: card),
          'choice' => _Choice(card: card),
          _ => _Generic(card: card),
        },
      ),
    );
  }
}

/// 卡片外壳 —— 标题、正文、脚注三段.
///
///	**只有一处**: 十几种卡各写一遍内边距, 迟早有两种对不齐,
///	而那种不齐没人会当成 bug 报上来
class _Shell extends StatelessWidget {
  const _Shell({this.title, this.sub, this.foot, required this.child});
  final String? title, sub, foot;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    final t = (title ?? '').trim();
    final s = (sub ?? '').trim();
    final f = (foot ?? '').trim();
    return Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
      if (t.isNotEmpty || s.isNotEmpty)
        Padding(
          padding: const EdgeInsets.fromLTRB(NX.s4, NX.s3, NX.s4, NX.s2),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            if (t.isNotEmpty)
              Text(t,
                  style: NX.body.copyWith(fontWeight: NX.wMed),
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis),
            if (s.isNotEmpty) ...[
              const SizedBox(height: 2),
              Text(s,
                  style: NX.caption.copyWith(color: NX.text3),
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis),
            ],
          ]),
        ),
      child,
      if (f.isNotEmpty)
        Padding(
          padding: const EdgeInsets.fromLTRB(NX.s4, NX.s2, NX.s4, NX.s3),
          child: Text(f, style: NX.caption.copyWith(color: NX.text3)),
        ),
    ]);
  }
}

/// 位置卡 —— 微信发定位那种：标题、地址、一张图。
class _Map extends StatelessWidget {
  const _Map({required this.card, required this.state});
  final ev.Card card;
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final lat = card.num_('lat');
    final lon = card.num_('lon');
    final title = card.str('title');
    return InkWell(
      // 点一下用系统地图打开 —— **卡片上那张图是死的**，
      // 真要看周围得去地图 app 里
      onTap: () => _openInMaps(lat, lon, title),
      child: _Shell(
        title: title.isEmpty ? '一个位置' : title,
        sub: card.str('sub'),
        // 图由 **OS 代取**（/mapshot）——地图 key 不落到手机上，
        // 而且服务端 key 绑了出口 IP，手机每换一次基站 IP 就变一个
        child: AspectRatio(
          aspectRatio: 2,
          child: Image.network(
            '${state.base}/mapshot?lat=$lat&lon=$lon'
            '&token=${Uri.encodeComponent(state.token)}',
            fit: BoxFit.cover,
            // **加载不出来要留住位置**，不能塌成 0 高：塌了的话
            // 整条消息列表会在图到达的那一刻跳一下
            loadingBuilder: (c, child, p) =>
                p == null ? child : Container(color: NX.fill),
            errorBuilder: (c, e, s) => _Broken(text: '地图加载不出来'),
          ),
        ),
      ),
    );
  }

  /// 交给系统去挑地图 app —— **不写死百度或高德**：
  /// 用户装了哪个是他的事，写死一个就是在替他决定
  static Future<void> _openInMaps(double lat, double lon, String label) async {
    final uri = Uri.parse(
        'geo:$lat,$lon?q=$lat,$lon(${Uri.encodeComponent(label)})');
    if (await canLaunchUrl(uri)) {
      await launchUrl(uri, mode: LaunchMode.externalApplication);
    }
  }
}

class _Link extends StatelessWidget {
  const _Link({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final url = card.str('url');
    return InkWell(
      onTap: () => _openUrl(url),
      child: Padding(
        padding: const EdgeInsets.all(NX.s4),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(card.str('title').isEmpty ? url : card.str('title'),
              style: NX.body.copyWith(fontWeight: NX.wMed),
              maxLines: 2, overflow: TextOverflow.ellipsis),
          const SizedBox(height: 2),
          Text(url,
              style: NX.caption.copyWith(color: NX.accent),
              maxLines: 1, overflow: TextOverflow.ellipsis),
        ]),
      ),
    );
  }
}

Future<void> _openUrl(String url) async {
  final uri = Uri.tryParse(url);
  if (uri != null && await canLaunchUrl(uri)) {
    await launchUrl(uri, mode: LaunchMode.externalApplication);
  }
}

/// 图卡 —— bot 拿一张图回答你.
///
///	**path 才是常用的那个**: bot 画完一张图存在自己工作区里, 它知道的
///	就是那条路径. 手机打不开那条路径, 由 OS 代取(/blob?path=)
class _Image extends StatelessWidget {
  const _Image({required this.card, required this.state});
  final ev.Card card;
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final src = _srcOf(card, state, card.str('path'), card.str('src'));
    final cap = card.str('caption');
    return _Shell(
      title: card.str('title'),
      foot: cap.isEmpty ? null : cap,
      child: src.isEmpty
          ? _Broken(text: '这张图没有地址')
          : Image.network(src,
              fit: BoxFit.cover,
              loadingBuilder: (c, child, p) => p == null
                  ? child
                  : AspectRatio(
                      aspectRatio: 4 / 3, child: Container(color: NX.fill)),
              errorBuilder: (c, e, s) => _Broken(text: '图加载不出来')),
    );
  }
}

/// 一组图 —— 横着滚一排.
///
///	**不做九宫格**: 一行三张在手机上每张只有 100 多像素宽, 什么都看不清.
///	横滚保住了每张的尺寸, 而"还有更多"这件事由滚动条本身说
class _Gallery extends StatelessWidget {
  const _Gallery({required this.card, required this.state});
  final ev.Card card;
  final AppState state;

  @override
  Widget build(BuildContext context) {
    final raw = card.fields['images'];
    final items = raw is List ? raw : const [];
    return _Shell(
      title: card.str('title'),
      foot: items.isEmpty ? null : '${items.length} 张',
      child: SizedBox(
        height: 140,
        child: ListView.separated(
          scrollDirection: Axis.horizontal,
          padding: const EdgeInsets.symmetric(horizontal: NX.s3),
          itemCount: items.length,
          separatorBuilder: (_, __) => const SizedBox(width: NX.s2),
          itemBuilder: (c, i) {
            final it = items[i];
            final path = it is Map ? '${it['path'] ?? ''}' : '';
            final src = it is Map ? '${it['src'] ?? ''}' : '$it';
            final url = _srcOf(card, state, path, src);
            return ClipRRect(
              borderRadius: BorderRadius.circular(NX.rSm),
              child: url.isEmpty
                  ? Container(width: 140, color: NX.fill)
                  : Image.network(url,
                      width: 140,
                      fit: BoxFit.cover,
                      errorBuilder: (c, e, s) =>
                          Container(width: 140, color: NX.fill)),
            );
          },
        ),
      ),
    );
  }
}

/// 进度卡.
///
///	**done/total 也认**: "5 之 8"是表达进度最自然的写法, 模型十有八九
///	这么给. 只认 ratio/percent 的话, 它说着"进度 5/8", 卡上画着
///	**空条 + 0%** —— 不报错, 只是画了个假数.
class _Progress extends StatelessWidget {
  const _Progress({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final total = card.num_('total');
    final has = card.fields.containsKey;
    // ── 一个进度值都没给 → 别画进度条 ──
    //
    //	画出来是**空条 + 0%**: 不报错, 只是画了个假数. 而模型接着
    //	说"进度如上", 上面写着 0%.
    //
    //	产出侧本来就拦着这种卡(见 go/agent/show.go), 但正文里手写的
    //	标记绕过了那道闸 —— 那时候照字段老实排一张通用卡, 比画个
    //	假进度诚实
    if (!has('ratio') && !has('percent') && !has('done')) {
      return _Generic(card: card);
    }
    var pct = 0.0;
    if (has('ratio')) {
      pct = card.num_('ratio') * 100;
    } else if (has('percent')) {
      pct = card.num_('percent');
    } else if (has('done') && total > 0) {
      pct = card.num_('done') / total * 100;
    }
    pct = pct.clamp(0, 100);
    final detail = card.str('detail');
    // 给了 done/total 就把原数也摆出来 —— "5/8" 比 "63%" 好对账
    final counted = has('done') && total > 0
        ? '${card.num_('done').toStringAsFixed(0)}/'
            '${total.toStringAsFixed(0)} · '
        : '';
    return _Shell(
      title: card.str('title').isEmpty
          ? (card.str('label').isEmpty ? '进行中' : card.str('label'))
          : card.str('title'),
      foot: '$counted${pct.round()}%${detail.isEmpty ? '' : ' · $detail'}',
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: NX.s4),
        child: ClipRRect(
          borderRadius: BorderRadius.circular(NX.rPill),
          child: LinearProgressIndicator(
            value: pct / 100,
            minHeight: 6,
            backgroundColor: NX.fill2,
            valueColor: AlwaysStoppedAnimation(NX.accent),
          ),
        ),
      ),
    );
  }
}

/// 表格 —— **横向滚, 不折行**.
///
///	折行的表在手机上是一团糊: 一格里两行字, 上下两行的格子对不上.
///	一张对不齐的表比没有更糟 —— 它看着像数据, 但读出来的是错的
class _Table extends StatelessWidget {
  const _Table({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final cols = _strings(card.fields['columns']);
    final raw = card.fields['rows'];
    final rows = raw is List
        ? raw.map((r) => _strings(r)).toList()
        : const <List<String>>[];
    if (cols.isEmpty && rows.isEmpty) {
      return _Shell(title: card.str('title'), child: _Broken(text: '这张表是空的'));
    }
    return _Shell(
      title: card.str('title'),
      child: SingleChildScrollView(
        scrollDirection: Axis.horizontal,
        padding: const EdgeInsets.fromLTRB(NX.s4, 0, NX.s4, NX.s3),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          if (cols.isNotEmpty) _row(cols, head: true),
          for (final r in rows) _row(r),
        ]),
      ),
    );
  }

  Widget _row(List<String> cells, {bool head = false}) => Container(
        decoration: head
            ? BoxDecoration(
                border: Border(bottom: BorderSide(color: NX.line2)))
            : null,
        padding: const EdgeInsets.symmetric(vertical: NX.s2),
        child: Row(children: [
          for (final c in cells)
            Container(
              width: 110,
              padding: const EdgeInsets.only(right: NX.s3),
              child: Text(c,
                  style: head
                      ? NX.caption.copyWith(
                          color: NX.text3, fontWeight: NX.wMed)
                      : NX.label,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis),
            ),
        ]),
      );
}

/// 代码 / diff —— 等宽, 横向滚.
///
///	diff 里加的减的**要分开看得见**, 那是这张卡存在的唯一理由
class _Code extends StatelessWidget {
  const _Code({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final isDiff = card.type == 'diff';
    final text = isDiff
        ? (card.str('diff').isEmpty ? card.str('patch') : card.str('diff'))
        : card.str('code');
    final lines = text.split('\n');
    return _Shell(
      title: card.str('title').isEmpty ? card.str('path') : card.str('title'),
      sub: isDiff ? null : card.str('lang'),
      child: Container(
        width: double.infinity,
        color: NX.bgSubtle,
        child: SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          padding: const EdgeInsets.all(NX.s3),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            // **只画前 200 行**: 再多也读不完, 而全画会让整条消息列表卡住
            for (final l in lines.take(200))
              Text(l.isEmpty ? ' ' : l,
                  style: NX.caption.copyWith(
                    fontFamily: 'monospace',
                    height: 1.5,
                    color: !isDiff
                        ? NX.text
                        : l.startsWith('+')
                            ? NX.ok
                            : l.startsWith('-')
                                ? NX.bad
                                : NX.text2,
                  )),
            if (lines.length > 200)
              Text('… 还有 ${lines.length - 200} 行',
                  style: NX.caption.copyWith(color: NX.text4)),
          ]),
        ),
      ),
    );
  }
}

/// 一段正文 —— 走同一套 Markdown, 不另开一份
class _Doc extends StatelessWidget {
  const _Doc({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) => _Shell(
        title: card.str('title'),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(NX.s4, 0, NX.s4, NX.s4),
          child: Text.rich(TextSpan(children: richSpans(card.str('body'), NX.body))),
        ),
      );
}

class _Weather extends StatelessWidget {
  const _Weather({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final temp = card.fields.containsKey('tempC')
        ? '${card.num_('tempC').round()}°'
        : '';
    return Padding(
      padding: const EdgeInsets.all(NX.s4),
      child: Row(children: [
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(card.str('place'), style: NX.body.copyWith(fontWeight: NX.wMed)),
            const SizedBox(height: 2),
            Text(card.str('text'), style: NX.caption.copyWith(color: NX.text3)),
          ]),
        ),
        if (temp.isNotEmpty) Text(temp, style: NX.heading),
      ]),
    );
  }
}

/// 一个文件 —— **只报它是什么, 不给下载**.
///
///	手机上点了也存不到哪儿去(那要一条这边还没有的取文件通道),
///	而画一个按不下去的下载按钮比不画糟
class _File extends StatelessWidget {
  const _File({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final path = card.str('path');
    final name = path.split('/').last;
    return Padding(
      padding: const EdgeInsets.all(NX.s4),
      child: Row(children: [
        Icon(Icons.description_outlined, size: 20, color: NX.text3),
        const SizedBox(width: NX.s3),
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(name.isEmpty ? '一个文件' : name,
                style: NX.body, maxLines: 1, overflow: TextOverflow.ellipsis),
            const SizedBox(height: 2),
            Text(path,
                style: NX.caption.copyWith(color: NX.text4),
                maxLines: 1, overflow: TextOverflow.ellipsis),
          ]),
        ),
      ]),
    );
  }
}

/// 让你选一个 —— **只画不点**.
///
///	答一句话的通道就在聊天框里. 画成按钮却按不下去, 比画成文字糟:
///	他会点, 什么都不发生, 然后不知道该怎么答
class _Choice extends StatelessWidget {
  const _Choice({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final opts = _strings(card.fields['options']);
    return _Shell(
      title: card.str('title').isEmpty ? '选一个' : card.str('title'),
      foot: '回一句就行',
      child: Column(children: [
        for (final o in opts)
          Container(
            width: double.infinity,
            padding: const EdgeInsets.symmetric(
                horizontal: NX.s4, vertical: NX.s2),
            child: Row(children: [
              Text('· ', style: NX.body.copyWith(color: NX.text4)),
              Expanded(child: Text(o, style: NX.body)),
            ]),
          ),
      ]),
    );
  }
}

/// 通用卡 —— 照字段排.
///
/// **这一张比它看起来重要**: OS 那边的卡片种类表是开的(bot 可以发表外的
/// 种类), 没有它的话那些卡片会被静默丢掉 —— 而 bot 收到的是"已经展示了".
///
/// ── 列表和对象要摊开, 不能 toString ──
///
///	如果直接用 `'${e.value}'`, items 列表在屏幕上就会变成
///	`[{label: 09-10 17:30, value: 该打卡了…}]` 这样的单行文本,
///	字段和条目挤在一起难以阅读, 所以列表和对象都要按结构展开.
class _Generic extends StatelessWidget {
  const _Generic({required this.card});
  final ev.Card card;

  @override
  Widget build(BuildContext context) {
    final rows = card.fields.entries
        .where((e) => e.key != 'type' && e.key != 'id')
        .where((e) => e.value != null && '${e.value}'.trim().isNotEmpty)
        .toList();
    return Padding(
      padding: const EdgeInsets.all(NX.s4),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text(card.type, style: NX.caption.copyWith(color: NX.text4)),
        const SizedBox(height: NX.s2),
        for (final e in rows) ...[
          Text(e.key, style: NX.caption.copyWith(color: NX.text3)),
          ..._flat(e.value),
          const SizedBox(height: NX.s2),
        ],
      ]),
    );
  }

  /// 把一个值摊成几行字 —— 列表按条排, 对象按 `键: 值` 排
  static List<Widget> _flat(dynamic v) {
    if (v is List) {
      return [
        for (final it in v.take(12))
          Padding(
            padding: const EdgeInsets.only(left: NX.s2),
            child: Text('· ${_one(it)}', style: NX.body),
          ),
        if (v.length > 12)
          Text('… 还有 ${v.length - 12} 条',
              style: NX.caption.copyWith(color: NX.text4)),
      ];
    }
    return [Text(_one(v), style: NX.body, maxLines: 8,
        overflow: TextOverflow.ellipsis)];
  }

  static String _one(dynamic v) {
    if (v is Map) {
      // label/value 这一对最常见 —— 照它读, 读不出来就按 键: 值 排
      final l = v['label'] ?? v['name'] ?? v['title'];
      final r = v['value'] ?? v['text'] ?? v['detail'];
      if (l != null && r != null) return '$l — $r';
      return v.entries.map((e) => '${e.key}: ${e.value}').join(' · ');
    }
    return '$v';
  }
}

/// 画不出来时的那一块 —— **要留住高度**.
///
///	塌成 0 高的话, 整条消息列表会在图到达(或失败)的那一刻跳一下
class _Broken extends StatelessWidget {
  const _Broken({required this.text});
  final String text;

  @override
  Widget build(BuildContext context) => Container(
        height: 72,
        color: NX.fill,
        alignment: Alignment.center,
        child: Text(text, style: NX.caption.copyWith(color: NX.text4)),
      );
}

/// path 或 src 变成一个真能取的地址.
///
///	**path 由 OS 代取**(/blob?path=): 那条路径在 OS 的工作区里,
///	手机上根本不存在这个文件
String _srcOf(ev.Card card, AppState state, String path, String src) {
  if (path.trim().isNotEmpty) {
    return '${state.base}/blob?path=${Uri.encodeComponent(path)}'
        '&token=${Uri.encodeComponent(state.token)}';
  }
  return src.trim();
}

List<String> _strings(dynamic v) {
  if (v is! List) return const [];
  return v.map((e) {
    if (e is Map) return _Generic._one(e);
    return '$e';
  }).toList();
}
