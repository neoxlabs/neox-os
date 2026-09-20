import 'package:flutter_test/flutter_test.dart';
import 'package:neox/audio/hotwords.dart';

void main() {
  setUp(() {
    // 用六个中文 bot 名和一个英文名称构造热词表, 验证同音纠正且不改动英文.
    Hotwords.load(['研究', '值守', '构建', '发版', '回归', '文案', 'research']);
  });

  test('同音异字修回来 —— 这是它存在的全部理由', () {
    expect(Hotwords.fix('找直守看一眼'), '找值守看一眼');
    expect(Hotwords.fix('让文按起个草'), '让文案起个草');
    expect(Hotwords.fix('发板跑一下'), '发版跑一下');
  });

  test('本来就对的不许动', () {
    expect(Hotwords.fix('找值守看一眼'), '找值守看一眼');
    expect(Hotwords.fix('回归跑完了'), '回归跑完了');
  });

  test('拼音不一致一律放过 —— 宁可放过不可改错', () {
    // 「治守」的 zhi shou 跟「值守」同音 → 该改
    expect(Hotwords.fix('治守'), '值守');
    // 「知道」跟任何热词都不同音 → 不许动
    expect(Hotwords.fix('我知道了'), '我知道了');
  });

  test('长词优先 —— 短词不许抢先把长词吃掉', () {
    Hotwords.load(['井冈山', '井冈']);
    expect(Hotwords.fix('景刚山'), '井冈山');
  });

  test('非汉字原样留着', () {
    expect(Hotwords.fix('research 跑完了'), 'research 跑完了');
    expect(Hotwords.fix('版本 3.4.0'), '版本 3.4.0');
  });

  test('单字不进表 —— 单字同音字太多, 收进来必然改错', () {
    Hotwords.load(['文', '案']);
    expect(Hotwords.size, 0);
  });
}
