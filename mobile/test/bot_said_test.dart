import 'package:flutter_test/flutter_test.dart';
import 'package:neox/state/app_state.dart';

/// 「它说完一句了」这个吆喝，**两条封口的路上都得响**。
///
/// ── 为什么单独钉一条 ──
///
/// 封口有两条路：收过增量的那条（phase:reply 拿最终那份盖掉）和
/// 没收过增量的那条（reply 本身就是整句）。这个吆喝原来只挂在后一条
/// 上——而生产上流式是开着的，于是**语音模式永远等不到回话**：球一直
/// 转，而账本里明明 1 秒就回了，没有任何一处报错。
///
/// 同一件事有两个出口的时候，挂在其中一个上就是挂错了。
void main() {
  // AppState 在构造里挂了 WidgetsBindingObserver —— 测试里要先有 binding
  TestWidgetsFlutterBinding.ensureInitialized();

  late AppState s;
  late List<String> said;

  setUp(() {
    s = AppState();
    said = [];
    s.onBotSaid = (pid, text) => said.add(text);
  });

  /// 一条 proc.output 事件长什么样 —— 照账本里的形状造
  Map<String, dynamic> out(Map<String, dynamic> payload) => {
        'pid': 'p1.1.9',
        'seq': said.length + 1,
        'kind': 'proc.output',
        'at': DateTime.now().millisecondsSinceEpoch,
        'payload': payload,
      };

  test('没收过增量: reply 本身就是整句', () {
    s.debugFeed(out({'phase': 'reply', 'text': '七点二十五出门。'}));
    expect(said, ['七点二十五出门。']);
  });

  test('收过增量: 最终那条盖掉之后也要响', () {
    // 先来两段增量, 再来最终那一条 —— 生产上就是这个顺序
    s.debugFeed(out({'channel': 'say.delta', 'stream': 'r7', 'text': '七点'}));
    s.debugFeed(out({'channel': 'say.delta', 'stream': 'r7', 'text': '二十五'}));
    expect(said, isEmpty, reason: '还没封口, 不该吆喝');
    s.debugFeed(
        out({'phase': 'reply', 'stream': 'r7', 'text': '七点二十五出门。'}));
    expect(said, ['七点二十五出门。'],
        reason: '流式那条路封口时没吆喝 —— 语音模式会永远等下去');
  });

  test('空回复不吆喝 —— 念一句空话比不念糟', () {
    s.debugFeed(out({'phase': 'reply', 'text': '   '}));
    expect(said, isEmpty);
  });
}
