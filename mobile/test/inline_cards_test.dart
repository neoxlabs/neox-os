import 'package:flutter_test/flutter_test.dart';
import 'package:neox/api/inline_cards.dart';

void main() {
  // 模型可能在正文里手写卡片标记; 提取卡片并保留正文, 避免把卡片显示成一段 JSON.
  test('抠出正文里手写的卡片, 话留着', () {
    const raw = '[[card: progress {"title":"提醒已设","items":'
        '[{"label":"09-10 17:30","value":"该打卡了。","state":"done"}],'
        '"note":"5 分钟后响"}]]\n\n现在 17:24，提醒设好了。';
    final got = InlineCards.take(raw);
    expect(got.cards.length, 1);
    expect(got.cards.first.type, 'progress');
    expect(got.cards.first.str('title'), '提醒已设');
    expect(got.text, '现在 17:24，提醒设好了。');
  });

  // 没有标记的走原路 —— 绝大多数消息是这一条
  test('没标记就原样', () {
    final got = InlineCards.take('就一句话。');
    expect(got.cards, isEmpty);
    expect(got.text, '就一句话。');
  });

  // **嵌套的方括号骗不过它**: 找 ']]' 那种写法会在这儿断在半路
  test('JSON 里带 ]] 也认得出边界', () {
    const raw = '[[card: table {"title":"表","rows":[["a","b"]]}]]后面还有话';
    final got = InlineCards.take(raw);
    expect(got.cards.length, 1);
    expect(got.cards.first.type, 'table');
    expect(got.text, '后面还有话');
  });

  // 解不开的**原样留着**: 半个标记也比无声吞掉一段话强 ——
  // 吞掉的话用户看不见, 而 bot 说着"如上"
  test('解不开就不动它', () {
    const raw = '[[card: progress {坏掉的]] 后面的话';
    final got = InlineCards.take(raw);
    expect(got.cards, isEmpty);
    expect(got.text.contains('后面的话'), isTrue);
  });

  test('一段话里两张卡', () {
    const raw = '看:\n[[card: doc {"body":"一"}]]\n[[card: doc {"body":"二"}]]';
    final got = InlineCards.take(raw);
    expect(got.cards.length, 2);
    expect(got.text, '看:');
  });

  // 标记里的种类和 JSON 里的 type 冲突时, 以 JSON 里的为准
  test('type 字段优先', () {
    final got = InlineCards.take('[[card: progress {"type":"doc","body":"x"}]]');
    expect(got.cards.first.type, 'doc');
  });
}
