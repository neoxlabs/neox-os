import 'package:flutter_test/flutter_test.dart';
import 'package:neox/audio/utterance.dart';

/// 碎片拼句 + 噪音判定.
///
/// ── 为什么钉这么细 ──
///
/// 语音页每几秒可能发一条碎片(「前方刺猬」「但是」「是是」「errr」…),
/// 每条都是一整轮大模型. 这套规矩错了的后果两头都难受:
/// 松了是一路刷屏, 紧了是他说完了它不发 —— 两种问题都需要在连续使用
/// 语音页时观察才容易发现.
void main() {
  // ── 可以拨的钟 ──
  var t = DateTime(2026, 9, 11, 8, 0, 0);
  DateTime now() => t;
  void after(int ms) => t = t.add(Duration(milliseconds: ms));

  late Utterance u;
  setUp(() {
    t = DateTime(2026, 9, 11, 8, 0, 0);
    u = Utterance(now: now);
  });

  group('拼句', () {
    test('两段定稿之间停顿不到 1.2 秒: 合成一条', () {
      u.heard('明天早上', done: true);
      after(800);
      expect(u.tick(), isNull, reason: '还没静够 1.2 秒');
      // 识别器报的是全文: 已定稿 + 这一段
      u.heard('明天早上七点叫我', done: true);
      after(1199);
      expect(u.tick(), isNull);
      after(1);
      final h = u.tick();
      expect(h?.send, isTrue);
      expect(h?.text, '明天早上七点叫我');
      expect(u.tick(), isNull, reason: '发过了就不再发');
    });

    test('又开口了(冒出新的半句): 等待往后推', () {
      u.heard('帮我查一下', done: true);
      after(1000);
      u.heard('帮我查一下明天', done: false); // 还在说
      after(1000);
      expect(u.tick(), isNull, reason: '半句才过去 1 秒');
      u.heard('帮我查一下明天的天气', done: true);
      after(1200);
      expect(u.tick()?.text, '帮我查一下明天的天气');
    });

    test('只有还在跳的半句, 没有定稿: 不发', () {
      u.heard('我想', done: false);
      after(5000);
      expect(u.tick(), isNull);
    });

    test('发过的那截不再发: 下一句只发新的', () {
      u.heard('今天几号', done: true);
      after(1200);
      expect(u.tick()?.text, '今天几号');
      u.heard('今天几号星期几来着', done: true);
      after(1200);
      expect(u.tick()?.text, '星期几来着');
    });

    test('第二遍识别改写了还没发的那截: 用改过的', () {
      u.heard('帮我定个直守', done: true);
      after(500);
      // 第二遍把「直守」改成「值守」—— 全文前缀变了, 但那截还没发
      u.heard('帮我定个值守', done: true);
      after(1200);
      expect(u.tick()?.text, '帮我定个值守');
    });

    test('改写碰到了已经发掉的那截: 只发对不上之后的', () {
      u.heard('今天天气', done: true);
      after(1200);
      expect(u.tick()?.text, '今天天气');
      // 已发的「天气」被改成「天汽」, 后面又说了新的
      u.heard('今天天汽怎么样啊', done: true);
      after(1200);
      expect(u.tick()?.text, '汽怎么样啊');
    });

    test('识别器重置(念完回话之后): 攒着没发的留着, 新的接在后面', () {
      u.heard('帮我看看', done: true);
      u.restart(); // asr.resetText —— 之后全文从空的起
      after(300);
      u.heard('明天的日程', done: true);
      after(1200);
      expect(u.tick()?.text, '帮我看看明天的日程');
    });

    test('showing: 已经发掉的不再画', () {
      u.heard('几点了', done: true);
      after(1200);
      u.tick();
      expect(u.showing('几点了你好'), '你好');
    });

    test('英文两截之间补空格', () {
      expect(Utterance.join('ok', 'google'), 'ok google');
      expect(Utterance.join('好', '的'), '好的');
    });
  });

  group('碎片', () {
    test('「但是」单独一句: 攥着, 后面有话就并进去', () {
      u.heard('但是', done: true);
      after(1500);
      expect(u.tick(), isNull, reason: '短碎片要多等一会儿');
      u.heard('但是明天可能下雨', done: true);
      after(1200);
      expect(u.tick()?.text, '但是明天可能下雨');
    });

    test('「但是」后面一直没话: 不发, 并且说清为什么', () {
      u.heard('但是', done: true);
      after(1200 + 2500);
      final h = u.tick();
      expect(h?.send, isFalse);
      expect(h?.text, '但是');
      expect(h?.why, '话没说完');
    });

    test('「好的」是一句完整的回答, 不当碎片扣下', () {
      u.heard('好的', done: true);
      after(1200);
      expect(u.tick()?.send, isTrue);
    });

    test('「有」一个字: 不发', () {
      u.heard('有', done: true);
      after(1200);
      final h = u.tick();
      expect(h?.send, isFalse);
      expect(h?.why, '太短');
    });
  });

  group('噪音', () {
    // 常见的语气词和不完整发音
    for (final s in ['是是', '嗯', '啊啊', 'errr', 'emm', 'uh', '呃，嗯', '对对对', '嗯嗯']) {
      test('语气词「$s」不发', () {
        expect(Junk.why(s), isNotNull);
      });
    }

    for (final s in [
      '前方刺猬',
      '前方在地处有有东西通照',
      '前方测速拍照',
      '前方路口右转',
      '前方有电子眼',
      '前方三百米进入隧道',
      '300米后右转',
      '请沿当前道路继续行驶',
      '您已超速',
      '进入中山路',
      '即将进入京港澳高速',
    ]) {
      test('导航播报「$s」不发', () {
        expect(Junk.why(s), '像导航播报');
      });
    }

    for (final s in [
      '明天早上七点叫我',
      '前面堵不堵',
      '这个东西',
      '参交工作',
      '什么',
      '好',
      'ok',
      '帮我导航到公司',
    ]) {
      test('正常的话「$s」放过去', () {
        expect(Junk.why(s), isNull);
      });
    }

    test('拼好的一句里只有语气词: 整条不发, 灰着说为什么', () {
      u.heard('嗯', done: true);
      after(600);
      u.heard('嗯是是', done: true);
      after(1200);
      final h = u.tick();
      expect(h?.send, isFalse);
      expect(h?.why, '只有语气词');
    });

    test('不发的那句也算用掉了 —— 下一句不会把它带上', () {
      u.heard('前方测速', done: true);
      after(1200);
      expect(u.tick()?.send, isFalse);
      u.heard('前方测速几点下班', done: true);
      after(1200);
      expect(u.tick()?.text, '几点下班');
    });
  });

  test('他自己说"几公里左右"不是导航播报', () {
    expect(Junk.why('到公司还有五公里左右吗'), isNull);
    expect(Junk.why('前方500米后右转'), '像导航播报');
  });
}
