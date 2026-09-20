/// 把识别出来的碎片拼成**一句话**, 再判这句值不值得发.
///
/// ── 为什么要拼 ──
///
/// 识别器每个自然的换气就断一次句(见 asr.dart 的 rule2: 0.7 秒).
/// 那对"字幕跟手"是对的, 对"发给模型"是错的: 2026-09-11 车上, 语音页
/// 每几秒发一条 ——「前方刺猬」「但是」「参交工作」「这个东西」「的机器」
/// 「是是」「有」「errr」—— **每一条都是一整轮大模型**, 而没有一条是
/// 一句完整的话.
///
/// 所以断句只进缓冲; 最后一段定稿之后**再静 1.2 秒**(期间没有新的字冒出来)
/// 才算他说完了, 攒着的几段合成一条发. 中途又开口了就接着等.
///
/// ── 为什么是纯 Dart、时钟可以换 ──
///
/// 这里面全是"过了多久"的判断. 拿真的时钟测, 要么测试里 sleep,
/// 要么测不了 —— 而这套规矩恰恰是最该钉住的(它错了, 要么一句话被
/// 劈成三条, 要么他说完了它一直不发).
library;

/// 判完的结果: 要么发, 要么不发(带着为什么).
class Heard {
  const Heard.send(this.text) : why = null;
  const Heard.drop(this.text, this.why);

  final String text;

  /// why 不发的理由 —— **给人看的**, 界面上那行灰字里会写出来.
  /// null = 发
  final String? why;

  bool get send => why == null;

  @override
  String toString() => send ? 'send($text)' : 'drop($text: $why)';
}

/// 拼句子.
///
/// 用法: 识别器每出一次字就 [heard]; 定时(一两百毫秒一次)调 [tick],
/// 它返回非 null 的时候就是一句拼好了、判完了的话.
class Utterance {
  Utterance({
    this.settle = const Duration(milliseconds: 1200),
    this.hold = const Duration(milliseconds: 2500),
    DateTime Function()? now,
  }) : _now = now ?? DateTime.now;

  /// 最后一段定稿之后, 静多久算说完了.
  ///
  ///	1.2 秒: 比识别器自己断句的 0.7 秒长一截 —— 中文口语里想词的停顿
  ///	常常过一秒, 断在这儿就是把一句话劈成两条. 再长的话他说完了要
  ///	干等, 在车上那一两秒像是它没听见
  final Duration settle;

  /// 只有一个短碎片(「但是」「那个」)的时候, 多等这么久看后面有没有话.
  ///
  ///	**不单独发**: 一条「但是」发出去, 模型只能回"但是什么?" ——
  ///	一整轮花在一个连词上. 等到了后话就并进去, 等不到就不发
  final Duration hold;

  final DateTime Function() _now;

  /// 识别器那边已经"用掉"的全文前缀 —— 发了的、扔了的、自己念的都算.
  ///
  ///	**识别器报的永远是全文**(已定稿的几段 + 正在说的这一段), 不是最后
  ///	一句. 照着发的话第二句会把第一句再发一遍.
  String _base = '';

  /// 最后一次定稿时的全文 —— 发掉的时候 _base 推到这儿
  String _whole = '';

  /// 定稿了、还没发的那一截 = _whole 去掉 _base
  String _fresh = '';

  /// 识别器被重置过(见 [restart]), 那之前攒着没发的
  String _carry = '';

  /// 最后一次听到动静(定稿或者新冒出来的字)
  DateTime? _lastVoice;

  /// 攒着的这些里有没有定稿过的 —— 只有还在跳的半句不算"说完了"
  bool _final = false;

  /// 现在攒着的那一句(只算定稿了的)
  String get pending => join(_carry, _fresh);

  /// 界面上该画的那一截: 攒着没发的 + 正在说的. **已经发掉的不再画** ——
  /// 识别器报的是全文, 照着画的话屏幕上是今天说过的所有话
  String showing(String whole) {
    final all = whole.trim();
    final rest = all.startsWith(_base) ? all.substring(_base.length).trim() : all;
    return join(_carry, rest);
  }

  /// 识别器出字了.
  ///
  /// @param done 这是不是一次定稿(断句). 不是的话只说明"他还在说",
  ///   用来把等待往后推
  void heard(String whole, {required bool done}) {
    final all = whole.trim();
    _lastVoice = _now();
    if (!done) return;
    if (all.startsWith(_base)) {
      _fresh = all.substring(_base.length).trim();
    } else {
      // ── 前缀对不上了 ──
      //
      // 第二遍识别会改写前面的字; 改的要是**还没发的**那一截, 上面那条
      // 路本来就对(_fresh 每次从最新的全文重算, 改写自然盖掉旧的).
      // 走到这儿说明连**已经发掉的**那一截都被改了 —— 发出去的收不回来,
      // 能做的只有把能对上的前缀剥掉, 剩下的当新的.
      final n = _common(_base, all);
      _base = all.substring(0, n);
      _fresh = all.substring(n).trim();
    }
    _whole = all;
    if (_fresh.isNotEmpty || _carry.isNotEmpty) _final = true;
  }

  /// 识别器从头开始记了(asr.resetText) —— 之后报的全文从空的起.
  ///
  ///	攒着没发的那截**挪到 _carry 里留着**: 重置是为了扔掉它自己念的
  ///	那几句回声, 不是扔掉他说的话
  void restart() {
    _carry = join(_carry, _fresh);
    _fresh = '';
    _base = '';
    _whole = '';
  }

  /// 扔掉攒着的一切, 包括"已经用掉到哪儿"
  void clear() {
    _carry = '';
    _fresh = '';
    _base = '';
    _whole = '';
    _final = false;
    _lastVoice = null;
  }

  /// 看一眼: 说完了没有. 说完了就返回判好的那句, 否则 null.
  Heard? tick() {
    final text = pending;
    final last = _lastVoice;
    if (!_final || text.isEmpty || last == null) return null;
    final quiet = _now().difference(last);
    if (quiet < settle) return null;

    final why = Junk.why(text);
    if (why == null && Junk.fragment(text) && quiet < settle + hold) {
      // 短碎片先攥着 —— 见 [hold]
      return null;
    }
    _consume();
    if (why != null) return Heard.drop(text, why);
    if (Junk.fragment(text)) return Heard.drop(text, '话没说完');
    return Heard.send(text);
  }

  void _consume() {
    _base = _whole;
    _fresh = '';
    _carry = '';
    _final = false;
  }

  /// 两截拼一起. 中文直接接; 两边都是字母数字的话中间补个空格,
  /// 不然 "ok" 和 "google" 拼成 "okgoogle"
  static String join(String a, String b) {
    if (a.isEmpty) return b;
    if (b.isEmpty) return a;
    final wordy = RegExp(r'[A-Za-z0-9]');
    if (wordy.hasMatch(a[a.length - 1]) && wordy.hasMatch(b[0])) return '$a $b';
    return '$a$b';
  }

  static int _common(String a, String b) {
    final n = a.length < b.length ? a.length : b.length;
    var i = 0;
    while (i < n && a.codeUnitAt(i) == b.codeUnitAt(i)) {
      i++;
    }
    return i;
  }
}

/// 这句是不是噪音.
///
/// ── 为什么要在手机上拦 ──
///
/// 每一条发出去的都是一整轮大模型: 读上下文、想、可能还调工具.
/// 「嗯」「是是」「errr」没有一条是在跟它说话 —— 它们是开车时的自言自语、
/// 车里的收音机、导航在播报. 发出去的结果是模型认真地回一句
/// "你是想说什么?", 然后被念出来, 又被麦克风听进去.
///
/// **只拦确定是噪音的**. 拿不准的放过去 —— 被拦下的一句真话比多花
/// 一轮更糟, 所以界面上会把拦下的那句灰着写出来(见 voice_page).
class Junk {
  Junk._();

  /// 语气词 / 应声词. 一句话去掉这些之后什么都不剩, 就是噪音.
  ///
  ///	只收**单独说出来不表达任何意思**的: 「好」「行」「对」不在这儿 ——
  ///	它们是回答(见 [answers]). 「么」「呢」「吧」也不在: 它们粘在词上
  ///	(「什么」「好吧」), 当语气词剥掉的话「什么?」就只剩一个字了
  static const fillers = [
    '嗯', '啊', '呃', '哦', '额', '唉', '哎', '诶', '欸', '噢', '喔', '呀',
    '哈', '嘿', '唔', '哼', '咦', '嗷', '哇',
  ];

  /// 英文那边的语气词 —— 识别器把"呃……"听成 errr、em、uh 是常事.
  /// 整词匹配, 允许拖长音(errrr, emmm, uhhh)
  static final _latinFiller = RegExp(
      r'^(e+r+|e+m+|u+h+|u+m+|a+h+|o+h+|h+m+|m+|e+h+|e+|a+|o+)$',
      caseSensitive: false);

  /// 重复的应声 —— 「是是」「对对对」「好好好」「嗯嗯」.
  ///
  ///	单说一个「好」是回答; 连着说两三遍, 多半是在跟别人(或者收音机)
  ///	打哈哈. 识别结果常见为「是是」
  static const _echoes = ['是', '对', '好', '行', '嗯', '哦', '啊', '呃'];

  /// 短但完整的回答 —— **不当碎片扣下**.
  ///
  ///	它问"要不要提醒你?", 他答"好的" —— 这两个字就是一整句话.
  ///	拿"两个字以内都是碎片"那条规矩一刀切的话, 语音模式里所有的
  ///	确认都发不出去
  static const answers = [
    '好', '好的', '行', '可以', '要', '要的', '不要', '不用', '不行', '是的',
    '对的', '没有', '没了', '取消', '停', '停下', '继续', '算了', '谢谢',
    '再见', '知道了', '明白', '收到', '确定', '没事', '好吧', '行吧', '好嘞',
    'ok', 'okay', 'yes', 'no',
  ];

  /// 导航播报里的词 —— 句子以「前方」开头、又带着这些之一, 就是导航在说话.
  ///
  ///	开车时导航一直在播, 而麦克风分不出那是导航还是他. 常见识别结果包括:
  ///	「前方刺猬」(测速)、「前方在地处有有东西通照」(路口有电子眼拍照).
  ///
  ///	末尾那几个单字是**识别器听岔了之后还剩下的那一个字**:「拍照」
  ///	听成「通照」,「测速」听成「刺速」—— 整词对不上, 那个字还在
  static const navWords = [
    '测速', '拍照', '摄像', '监控', '电子眼', '限速', '路口', '米', '掉头',
    '左转', '右转', '车道', '违章', '红绿灯', '隧道', '收费站', '服务区',
    '匝道', '并线',
    '照', '速', '灯',
  ];

  /// 导航的固定句式 —— 不以「前方」开头的那些
  static const navPhrases = [
    '请沿当前道路', '沿当前道路', '您已超速', '你已超速', '已为您', '重新规划',
    '请保持直行', '请靠左', '请靠右', '请走左侧', '请走右侧', '即将到达目的地',
    '已到达目的地', '本次导航结束', '导航结束', 'GPS信号弱', '开启导航',
    '请注意安全驾驶', '前方道路拥堵', '全程约',
  ];

  /// 「300 米后右转」「进入中山路」「一公里后」—— 导航最常见的两种句头
  static final _navRegex = [
    // 不收"左右": "到公司还有五公里左右吗"是他在问, 导航只说"500 米后""300 米处"
    RegExp(r'([0-9]+|[一二两三四五六七八九十百千]+)\s*(米|公里)(后|处)'),
    RegExp(r'^(即将|已经|已)?(驶入|进入).{1,10}(路|街|道|高速|隧道|匝道|桥|环线)'),
  ];

  /// 标点和空白 —— 判之前先剥掉, 它们不是话
  static final _punct = RegExp(r'[\s\p{P}\p{S}]', unicode: true);
  static final _cjk = RegExp(r'[\u3400-\u9fff]');
  static final _word = RegExp(r'[A-Za-z0-9]+');

  /// 为什么不发. null = 发.
  static String? why(String text) {
    final t = text.replaceAll(_punct, '');
    if (t.isEmpty) return '空的';
    if (_isNav(t)) return '像导航播报';
    if (answers.contains(t.toLowerCase())) return null;
    if (_onlyFillers(t)) return '只有语气词';
    if (_meaningful(t) <= 1) return '太短';
    return null;
  }

  /// 短碎片: 两个字以内、又不是一句完整的回答 ——「但是」「那个」「有」.
  /// **不单独发**, 等后话(见 Utterance.hold)
  static bool fragment(String text) {
    final t = text.replaceAll(_punct, '');
    if (answers.contains(t.toLowerCase())) return false;
    return _meaningful(t) <= 2;
  }

  static bool _isNav(String t) {
    if (navPhrases.any(t.contains)) return true;
    if (_navRegex.any((r) => r.hasMatch(t))) return true;
    if (!t.startsWith('前方')) return false;
    final rest = t.substring(2);
    if (navWords.any(rest.contains)) return true;
    // 「前方」后面只跟了一两个字 —— 半截的播报(「前方刺猬」).
    // 他自己问路会说「前面堵不堵」, 很少说成四个字的「前方 xx」
    return rest.length <= 2;
  }

  static bool _onlyFillers(String t) {
    // 英文那一路: 每个词都是语气词
    final words = _word.allMatches(t).map((m) => m.group(0)!).toList();
    final cjk = t.replaceAll(_word, '');
    final latinJunk = words.every(_latinFiller.hasMatch);
    if (cjk.isEmpty) return words.isNotEmpty && latinJunk;
    if (!latinJunk) return false;
    // 中文那一路: 去掉语气词之后不剩东西, 或者剩下的是同一个应声字的重复
    var rest = cjk;
    for (final f in fillers) {
      rest = rest.replaceAll(f, '');
    }
    if (rest.isEmpty) return true;
    // 剥掉语气词之后看: 「嗯是是」剩下的「是是」也是应声
    final chars = rest.runes.toSet();
    if (rest.length >= 2 && chars.length == 1 &&
        _echoes.contains(String.fromCharCode(chars.first))) {
      return true;
    }
    return false;
  }

  /// 有意义的字数 —— 汉字(不算语气词)每个算一, 英文和数字每个词算二
  /// (一个英文词大致抵得上两个汉字的信息)
  static int _meaningful(String t) {
    var n = 0;
    for (final r in t.runes) {
      final c = String.fromCharCode(r);
      if (_cjk.hasMatch(c) && !fillers.contains(c)) n++;
    }
    for (final m in _word.allMatches(t)) {
      if (!_latinFiller.hasMatch(m.group(0)!)) n += 2;
    }
    return n;
  }
}
