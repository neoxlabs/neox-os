import 'dart:convert';

import 'events.dart';

/// 正文里手写的卡片标记 —— **它是个 bug, 但不能让它变成屏幕上的 JSON**。
///
/// ── 这种标记会直接出现在正文里 ──
///
/// 模型可能自行生成卡片标记，把一张卡直接写进回话里：
///
///	[[card: progress {"title":"提醒已设","items":[…]}]]
///
///	现在 17:24，今天下午 5:30 的提醒设好了…
///
/// 而卡片的**唯一出口是 show 工具**（go/agent/show.go，走 ui 通道）。
/// 正文里写标记不会变成卡片；不处理这些标记，上面那一段就会原样显示给用户。
///
/// ── 为什么客户端也要管 ──
///
/// 源头已经在提示词里堵了（show 的工具说明明说了正文里的标记不渲染）。
/// 但堵不干净：模型下一版、换一个供应商，随时可能又发明一次。
///
/// 而这时候客户端有三条路：
///
///	原样显示  用户看到一段 JSON —— 就是现在这样
///	悄悄删掉  用户什么都看不见，而 bot 说着「进度如上」
///	照着画    信息不丢，而且看起来是对的
///
/// 第三条。这跟通用卡「认不出的种类也要画」是同一条道理：
/// **画错一点也比让用户看一段 JSON 强**，而丢掉是最坏的那种。
class InlineCards {
  const InlineCards(this.text, this.cards);

  /// text 去掉标记之后剩下的话
  final String text;

  /// cards 抠出来的那几张
  final List<Card> cards;

  static const _open = '[[card:';

  /// take 从一段话里把卡片标记抠出来.
  ///
  ///	没有标记就原样返回 —— 绝大多数消息走的是这条路,
  ///	所以先做一次 contains 再谈解析
  static InlineCards take(String raw) {
    if (!raw.contains(_open)) return InlineCards(raw, const []);
    final out = StringBuffer();
    final cards = <Card>[];
    var i = 0;
    while (i < raw.length) {
      final at = raw.indexOf(_open, i);
      if (at < 0) {
        out.write(raw.substring(i));
        break;
      }
      // 标记之前那一段照原样留着
      out.write(raw.substring(i, at));
      final got = _one(raw, at);
      if (got == null) {
        // 解不开就**原样留着**: 半个标记也比无声吞掉一段话强
        out.write(_open);
        i = at + _open.length;
        continue;
      }
      if (got.card != null) cards.add(got.card!);
      i = got.end;
    }
    return InlineCards(_tidy(out.toString()), cards);
  }

  /// _one 从 at 处读一个标记. 返回 null = 这儿不是一个完整的标记
  static _Hit? _one(String raw, int at) {
    var j = at + _open.length;
    // 种类: 到第一个 '{' 为止
    final brace = raw.indexOf('{', j);
    if (brace < 0) return null;
    final kind = raw.substring(j, brace).trim();
    // JSON 对象: **数括号, 而且认字符串里的括号** ——
    // 找 ']]' 那种写法会被 "items":[[…]] 这样的嵌套骗到
    final close = _endOfObject(raw, brace);
    if (close < 0) return null;
    final body = raw.substring(brace, close + 1);
    var end = close + 1;
    // 后面跟着的 ']]' 吃掉; 没有也算(模型少写一个括号不该让整段话变形)
    final tail = raw.indexOf(']]', end);
    if (tail >= 0 && raw.substring(end, tail).trim().isEmpty) end = tail + 2;
    try {
      final v = jsonDecode(body);
      if (v is! Map<String, dynamic>) return _Hit(null, end);
      // type 以标记里写的那个为准; 里面自己带了就用里面的
      final fields = Map<String, dynamic>.of(v);
      final t = '${fields['type'] ?? kind}'.trim();
      if (t.isEmpty) return _Hit(null, end);
      fields['type'] = t;
      return _Hit(Card(type: t, fields: fields), end);
    } catch (_) {
      return _Hit(null, end);
    }
  }

  /// _endOfObject 配对的那个 '}' 在哪儿. -1 = 没配上
  static int _endOfObject(String s, int start) {
    var depth = 0;
    var inStr = false;
    var esc = false;
    for (var i = start; i < s.length; i++) {
      final c = s[i];
      if (inStr) {
        if (esc) {
          esc = false;
        } else if (c == r'\') {
          esc = true;
        } else if (c == '"') {
          inStr = false;
        }
        continue;
      }
      if (c == '"') {
        inStr = true;
      } else if (c == '{') {
        depth++;
      } else if (c == '}') {
        depth--;
        if (depth == 0) return i;
      }
    }
    return -1;
  }

  /// _tidy 抠掉标记之后收一下空行 —— 标记独占一行的话会留下两个换行
  static String _tidy(String s) {
    final lines = s.split('\n');
    final out = <String>[];
    for (final l in lines) {
      if (l.trim().isEmpty && (out.isEmpty || out.last.trim().isEmpty)) continue;
      out.add(l);
    }
    while (out.isNotEmpty && out.last.trim().isEmpty) {
      out.removeLast();
    }
    return out.join('\n').trim();
  }
}

class _Hit {
  const _Hit(this.card, this.end);
  final Card? card;
  final int end;
}
