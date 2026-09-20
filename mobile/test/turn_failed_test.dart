import 'package:flutter_test/flutter_test.dart';
import 'package:neox/api/events.dart';
import 'package:neox/state/app_state.dart';

/// 这一轮没走完, **手机上必须看得见**.
///
/// 原来 proc.output 里 phase:turn_failed 那条被当成"不是 say"丢了:
/// 模型出错、这一轮中止, 他看到的是"它不理我". 2026-09-11 车上就是这样.
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  group('原因取一行', () {
    test('供应商吐回来的 JSON: 只取 message', () {
      const err = 'anthropic: 400 {"type":"error","error":'
          '{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens"}}';
      expect(TurnFailed.brief(err), 'prompt is too long: 210000 tokens');
    });

    test('message 里有转义: 还原成字', () {
      const err = r'{"error":{"message":"余额不足 \"deepseek\"\n请充值"}}';
      expect(TurnFailed.brief(err), '余额不足 "deepseek"');
    });

    test('没有 JSON: 取第一行', () {
      expect(TurnFailed.brief('\n模型连续 3 次出错\n第二行细节'), '模型连续 3 次出错');
    });

    test('太长的截到 160 字', () {
      final s = TurnFailed.brief('错' * 500);
      expect(s.length, 160);
      expect(s.endsWith('…'), isTrue);
    });

    test('什么都没有: 照实说没说原因, 不编', () {
      expect(TurnFailed.brief(''), '没说原因');
      expect(TurnFailed.of({}), '没说原因');
    });

    test('payload 里的先后: err > detail > why', () {
      expect(TurnFailed.of({'why': '止损线', 'detail': '这一轮花掉 15k'}), '这一轮花掉 15k');
      expect(TurnFailed.of({'err': 'boom', 'why': 'x'}), 'boom');
    });
  });

  group('画出来', () {
    late AppState s;
    late List<String> failed;
    var seq = 0;

    setUp(() {
      s = AppState();
      failed = [];
      s.onBotFailed = (pid, brief) => failed.add(brief);
    });

    Map<String, dynamic> out(Map<String, dynamic> payload) => {
          'pid': 'p1.1.9',
          'seq': ++seq,
          'kind': 'proc.output',
          'at': DateTime.now().millisecondsSinceEpoch,
          'payload': payload,
        };

    test('turn_failed: 落一行警示, 并吆喝语音页', () {
      s.debugFeed(out({'phase': 'turn_failed', 'err': '模型连续 3 次出错'}));
      final t = s.threads.values.single;
      expect(t.last.text, '这一轮没做完：模型连续 3 次出错');
      expect(t.last.alert, isTrue);
      expect(t.last.kind, MsgKind.system);
      expect(failed, ['模型连续 3 次出错']);
    });

    test('还在长的那条先封口 —— 不然光标一直闪', () {
      s.debugFeed(out({'channel': 'say', 'stream': 'r1', 'text': '我查一下'}));
      final live = s.threads.values.single.single;
      expect(live.streaming, isTrue);
      s.debugFeed(out({'phase': 'turn_failed', 'err': 'boom'}));
      expect(live.streaming, isFalse);
    });

    test('同一件事报两遍(一条包着另一条): 只画一行', () {
      s.debugFeed(out({'phase': 'turn_failed', 'err': '同一类错误连着出现 3 次了'}));
      s.debugFeed(out({'phase': 'turn_failed', 'err': '检测到停滞: 同一类错误连着出现 3 次了'}));
      expect(s.threads.values.single.where((m) => m.alert).length, 1);
      expect(failed.length, 1, reason: '语音页也只该念一次');
    });

    test('model_err 是重试途中的, 不画', () {
      s.debugFeed(out({'phase': 'model_err', 'err': '502'}));
      expect(s.threads.values.expand((t) => t).where((m) => m.alert), isEmpty);
      expect(failed, isEmpty);
    });
  });
}
