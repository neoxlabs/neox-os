import 'package:lpinyin/lpinyin.dart';

/// 拼音级热词纠错 —— 用动态热词表修正同音异字的专名.
///
/// ── 它治什么病 ──
///
/// 流式模型不认得专名. 你说「找值守」, 它写成「找直守」;
/// 说「文案」写成「文按」. 音是对的, 字是错的 —— 而这类错**恰好可以
/// 无损修回来**: 拼音一致、写法不同, 那就是同音异字.
///
/// 替换的边界:
///
///	只在拼音完全一致、写法不同时才替换, 不做模糊猜测 ——
///	**宁可放过不可改错**.
///
/// 近音(zh/z、an/ang)一律不碰. 一旦开始猜, 就会把本来对的字改错,
/// 而那种错用户没法理解也没法预防.
///
/// ── 热词表是动态的 ──
///
/// **热词来自 OS 的进程表**, 不使用固定词表 —— 当前有哪几个 bot、在哪几个项目下,
/// 那就是用户最可能说到、也最容易被写错的一批词.
///
/// 所以它每次连上 OS 就重建一次. sherpa-onnx 自带的 HomophoneReplacer
/// 走的是编译期 FST, 改一次要重新生成 —— 那条路在这儿走不通.
class Hotwords {
  Hotwords._();

  /// [(词, 逐字拼音, 长度)], 按长度倒序 —— **长词优先**,
  /// 否则「井冈山」会被「井冈」抢先匹配掉
  static List<(String, List<String>, int)> _table = const [];

  /// 重建热词表.
  ///
  /// 两个字以下的不收: 单字的同音字太多, 收进来必然改错
  /// (「文」能对上一大片字)
  static void load(Iterable<String> words) {
    final seen = <String>{};
    final out = <(String, List<String>, int)>[];
    for (final w in words) {
      final t = w.trim();
      if (t.length < 2 || !seen.add(t)) continue;
      final py = _pinyinOf(t);
      if (py == null) continue; // 里面有非汉字, 跳过
      out.add((t, py, t.length));
    }
    out.sort((a, b) => b.$3.compareTo(a.$3));
    _table = out;
  }

  static int get size => _table.length;

  /// 逐字拼音. **必须跟字一一对应** —— 用词组转换的话
  /// 「重」在不同词里给出不同音, 长度还可能对不上
  static List<String>? _pinyinOf(String s) {
    final out = <String>[];
    for (final ch in s.characters_) {
      if (!_isHan(ch)) return null;
      final py = PinyinHelper.getPinyinE(ch,
          separator: '', defPinyin: '', format: PinyinFormat.WITHOUT_TONE);
      if (py.isEmpty) return null;
      out.add(py);
    }
    return out;
  }

  static bool _isHan(String ch) {
    final c = ch.runes.first;
    return c >= 0x4E00 && c <= 0x9FFF;
  }

  /// 对一段识别结果做同音替换.
  ///
  /// 滑窗从左到右, 命中就跳过整个词 —— 命中之后不许在词内部再匹配,
  /// 否则「井冈山」会被拆成「井冈」+「山」各匹配一次
  static String fix(String text) {
    if (_table.isEmpty || text.isEmpty) return text;
    final chars = text.characters_.toList();
    final n = chars.length;
    // 整句的逐字拼音只算一次. 每个窗口各算一次的话,
    // 一句二十字要算上百遍
    final py = <String?>[];
    for (final ch in chars) {
      py.add(_isHan(ch)
          ? PinyinHelper.getPinyinE(ch,
              separator: '', defPinyin: '', format: PinyinFormat.WITHOUT_TONE)
          : null);
    }

    final out = StringBuffer();
    var i = 0;
    while (i < n) {
      var hit = false;
      for (final (word, wpy, len) in _table) {
        if (i + len > n) continue;
        var same = true;
        for (var k = 0; k < len; k++) {
          if (py[i + k] != wpy[k]) {
            same = false;
            break;
          }
        }
        if (!same) continue;
        out.write(word);
        i += len;
        hit = true;
        break;
      }
      if (!hit) {
        out.write(chars[i]);
        i++;
      }
    }
    return out.toString();
  }
}

extension on String {
  /// 按**字符**切, 不是按 code unit —— 汉字在 Dart 里是单个 code unit,
  /// 但表情和生僻字是代理对, 按 code unit 切会把它们劈成两半
  Iterable<String> get characters_ sync* {
    for (final r in runes) {
      yield String.fromCharCode(r);
    }
  }
}
