import 'dart:async';
import 'dart:io';
import 'dart:math' as math;
import 'dart:typed_data';

import 'package:flutter/services.dart' show rootBundle;
import 'package:path/path.dart' as p;
import 'package:path_provider/path_provider.dart';
import 'package:record/record.dart';
import 'package:sherpa_onnx/sherpa_onnx.dart' as sherpa;

import 'hotwords.dart';

/// 端侧流式语音识别.
///
/// ── 为什么不用系统那个识别器 ──
///
/// 上一版用的是 speech_to_text(安卓自带的识别器), 三个毛病:
/// 依赖 Google 服务(国行手机可能根本没有)、不能配热词、要联网.
///
/// 换成 sherpa-onnx 之后三个全没了, 而且**快得离谱**.
///
/// ── 参数是量出来的, 不是拍的 ──
///
/// 天玑 9400(MT6991, 全大核: 1×X925@3.6G + 3×X4@3.3G + 4×A720@2.4G)
/// 5.6 秒中文音频在模型 streaming-zipformer-small-ctc-zh-int8 上的性能:
///
///	线程 1   RTF 0.033–0.037   30× 实时
///	线程 2   RTF 0.031         32× 实时
///	线程 4   RTF 0.025–0.027   **40× 实时** ← 用这个
///	线程 8   RTF 0.031–0.036   比 4 线程还慢
///
/// **8 线程反而慢**: 线程数超过大核数量之后, 调度和同步的开销
/// 盖过并行收益. 所以写死 4, 不跟着 CPU 核数走.
///
/// ── 两段式: 说的时候出字, 说完了重写整句 ──
///
/// **这才是"补全"**. 移植自 hbdigtalhuman 的 asr.py `_second_pass()`:
///
///	流式 zipformer  负责实时字幕(体验) —— 边说边出字
///	离线 paraformer 负责最终定稿(准确率) —— 一句说完, 拿整段音频
///	                重新识别一遍, 把这句整个换掉
///
/// 为什么第二遍更准: 流式模型只能看见"到目前为止"的音频, 每一步都得
/// 在延迟和准确率之间让步; 离线模型拿到的是**完整的一句**, 前后文
/// 都在, 该纠的自然就纠了.
///
/// 天玑 9400 上 paraformer-zh-small int8 78MB 模型的性能:
///
///	RTF 0.007 —— 10.7 秒音频 0.07 秒跑完
///	而且多识别出流式漏掉的字: 「介绍啊」「感兴趣呢嗯」
///
/// 0.07 秒意味着**说完的那一瞬间句子就改好了**, 人看到的就是
/// "它自己把这句重新调整了一遍".
///
/// ── NNAPI 不能用 ──
///
/// 同一台机器上 provider=nnapi 编译到 API 27 之后确实启用了
/// (日志 "Use nnapi"), 但**进程直接 Abort** —— 这个 int8 zipformer
/// 的算子它接不住. 不是慢, 是崩.
///
/// GPU 也没测的必要: 单线程一块 100ms 音频只要 3.3ms, 而移动 GPU
/// 每次提交的固定开销就是 1–3ms —— 这个体量的模型, 搬上去赢不了.
class Asr {
  Asr._(this._recognizer, this._offline);

  final sherpa.OnlineRecognizer _recognizer;

  /// 第二遍那个. **可以是 null** —— 模型没带上或者建失败时,
  /// 整套退回只有第一遍, 行为跟以前一样(原版注释里那条:
  /// "没装 SenseVoice 模型时返回空串, 前端自动退回用流式累积结果")
  final sherpa.OfflineRecognizer? _offline;

  sherpa.OnlineStream? _stream;

  /// 这一句的原始音频. 断句时拿它去跑第二遍.
  ///
  /// **攒的是浮点采样不是字节**: 反正第二遍要的就是这个,
  /// 存字节的话到时候还要再转一遍
  final _utterance = <double>[];
  final _rec = AudioRecorder();
  StreamSubscription<Uint8List>? _sub;

  final _out = StreamController<AsrText>.broadcast();
  final _lv = StreamController<double>.broadcast();

  /// 识别结果流. **部分结果和最终结果走同一条** ——
  /// 界面据 [AsrText.done] 区分
  Stream<AsrText> get texts => _out.stream;

  /// 音量 0..1 —— 核心页那颗球跟着它动.
  ///
  /// ── 为什么从这儿出, 不另开一路采集 ──
  ///
  /// 原来核心页自己有一套 MicLevel: 又开一次麦克风、又算一遍 RMS.
  /// 两条路**抢同一个麦克风** —— 在会话页开着识别的时候切到核心页,
  /// 第二次 startStream 要么失败要么把第一条掐掉, 而两种都是静默的.
  ///
  /// 而算音量本来就是白送的: PCM 已经在手里, 一次遍历的事
  Stream<double> get levels => _lv.stream;

  /// 平滑后的音量. 上升快下降慢 —— 说话时立刻响应, 停下时缓缓落回去;
  /// 对称的平滑会让球在句子之间一顿一顿的
  double _level = 0;

  bool _on = false;
  bool get running => _on;

  /// 上一次报出去的文本 —— 流式解码每块都会给结果, 大多数时候没变.
  /// 不去重的话界面每 80ms 重建一次, 而且日志会被刷满
  String _last = '';

  /// 这一轮流式有没有出过字 —— **静音也会触发断句**,
  /// 那时候不该跑第二遍(见 _onChunk)
  bool _spokeThisRound = false;

  /// 已经定稿的那几段, 拼在一起就是这次说话的全文.
  ///
  /// 分段是为了**动态定稿**: 每个自然停顿锁定一段, 前面的字定下来
  /// 不再变, 只有正在说的那一段还在跳
  final _settled = StringBuffer();

  /// 上次跑滚动定稿时, 这一段已经攒了多少采样.
  /// 用它判"又说了一秒半没有", 而不是用墙钟 —— 掉帧或者卡顿时
  /// 墙钟会跑, 而音频没进来
  int _lastRolling = 0;

  /// 滚动定稿的间隔: 1.5 秒音频.
  ///
  /// 真机 RTF 0.007, 重识别 10 秒只要 70ms —— **一秒半跑一次
  /// 占不到 5% 的 CPU**. 这个数是量出来能撑得住的, 不是拍的
  static const _rollingEvery = 16000 * 3 ~/ 2;

  /// 滚动定稿最多回看多久. 超过就只看尾巴 ——
  /// 不封顶的话一段说了两分钟的话, 每次都要重识别两分钟
  static const _rollingWindow = 16000 * 12;

  static Asr? _instance;
  static Future<void>? _loading;

  /// 起来一次就留着.
  ///
  /// **建 recognizer 要接近 1 秒**(耗时约 0.95s, 要把 25MB 模型
  /// 读进内存并建图). 每次点麦克风都重建的话, 用户按下去要等一秒 ——
  /// 而那一秒里他以为没反应, 会再按一次.
  static Future<Asr> instance() async {
    if (_instance != null) return _instance!;
    _loading ??= _create();
    await _loading;
    return _instance!;
  }

  static Future<void> _create() async {
    sherpa.initBindings();
    final model = await _copyAsset('assets/asr/model.int8.onnx');
    final tokens = await _copyAsset('assets/asr/tokens.txt');
    final config = sherpa.OnlineRecognizerConfig(
      model: sherpa.OnlineModelConfig(
        zipformer2Ctc: sherpa.OnlineZipformer2CtcModelConfig(model: model),
        tokens: tokens,
        // 4 是量出来的最优(见类注释). **不写 Platform.numberOfProcessors**:
        // 那会给出 8, 而 8 比 4 慢
        numThreads: 4,
        provider: 'cpu',
      ),
      // 说完两秒半算一句 —— 比默认稍长一点: 中文口语里想词的停顿
      // 常常超过 2 秒, 断早了会把一句话劈成两条
      ruleFsts: '',
      enableEndpoint: true,
      // ── 断句阈值决定"多久锁定一次" ──
      //
      // rule2 是"说过话之后静了多久算一句完". 原来 1.4 秒 ——
      // 那意味着你得停一下才看得到定稿.
      //
      // 收到 0.7 秒: 说话时**每个自然的换气都会锁定一段**,
      // 于是前面的字一段段定下来, 而不是憋到整句说完.
      //
      // **不能再短**: 短于 0.5 秒会切在词中间, 而离线那遍之所以更准
      // 靠的正是"拿到完整的一句、前后文都在" —— 切碎了这个优势就没了
      rule1MinTrailingSilence: 2.4,
      rule2MinTrailingSilence: 0.7,
      rule3MinUtteranceLength: 20,
    );
    // 第二遍的离线模型. **起不来不算致命** —— 没有它只是少了定稿,
    // 实时那一遍照样能用
    sherpa.OfflineRecognizer? offline;
    try {
      final m2 = await _copyAsset('assets/asr2/model.int8.onnx');
      final t2 = await _copyAsset('assets/asr2/tokens.txt');
      offline = sherpa.OfflineRecognizer(sherpa.OfflineRecognizerConfig(
        model: sherpa.OfflineModelConfig(
          paraformer: sherpa.OfflineParaformerModelConfig(model: m2),
          tokens: t2,
          numThreads: 4,
          provider: 'cpu',
        ),
      ));
    } catch (_) {
      // 说不出话来也没关系: 下面 _secondPass 会自己判 null
    }
    _instance = Asr._(sherpa.OnlineRecognizer(config), offline);
  }

  /// 算这一块的响度, 报给核心页.
  ///
  /// **按分贝不按 rms**: 人说话的 rms 大多落在 0.01–0.1, 线性映射之后
  /// 整条曲线贴在底上, 球看着根本不动. 听觉本来就是对数的.
  ///
  /// −55dB 当静、−12dB 当满 —— 这个范围覆盖安静房间的底噪
  /// 落在 −60 上下, 正常说话在 −30 到 −15
  void _pushLevel(Float32List f) {
    if (f.isEmpty) return;
    var sum = 0.0;
    for (final v in f) {
      sum += v * v;
    }
    final rms = math.sqrt(sum / f.length);
    final db = 20 * math.log(rms + 1e-7) / math.ln10;
    final v = ((db + 55) / 43).clamp(0.0, 1.0);
    _level += (v - _level) * (v > _level ? .5 : .12);
    if (!_lv.isClosed) _lv.add(_level);
  }

  /// 第二遍 —— 拿整句音频重新识别一遍.
  ///
  /// 短于 0.3 秒的不跑: 那种长度重识别没有价值, 而且多半是噪声
  /// (原版也是这么判的)
  String? _secondPass() {
    final off = _offline;
    if (off == null || _utterance.length < 16000 * 0.3) return null;
    try {
      final s = off.createStream();
      // 只看尾巴那一段 —— 不封顶的话说了两分钟就要重识别两分钟,
      // 而前面早就定稿了, 再识别一遍纯属白烧
      final from = _utterance.length > _rollingWindow
          ? _utterance.length - _rollingWindow
          : 0;
      s.acceptWaveform(
          samples: Float32List.fromList(_utterance.sublist(from)),
          sampleRate: 16000);
      off.decode(s);
      final t = off.getResult(s).text.trim();
      s.free();
      return t.isEmpty ? null : t;
    } catch (_) {
      return null;
    }
  }

  /// 资产要先落到磁盘 —— 底层是 C++, 它只认文件路径, 读不了 Flutter 的
  /// 资产包. 已经落过而且大小一样就不重写(冷启动省半秒)
  /// 把资产落到磁盘 —— 底层是 C++, 只认文件路径, 读不了 Flutter 的资产包.
  ///
  /// ── 落地名必须带上目录 ──
  ///
  /// 两个模型的文件都叫 model.int8.onnx / tokens.txt。**只取 basename
  /// 的话它们会落到同一个路径互相覆盖** —— 结果是:
  /// paraformer 把流式那份冲掉了, 于是流式 recognizer 加载到一个
  /// paraformer 模型, 报 "'encoder_dims' does not exist in the metadata"
  /// 然后整个起不来。
  ///
  /// 而且这个错**只在加了第二个模型那天才出现** —— 之前一个模型时
  /// 一直是对的。所以用 assets/ 之后的完整相对路径当落地名。
  static Future<String> _copyAsset(String src) async {
    final dir = await getApplicationSupportDirectory();
    final target = p.join(dir.path, src.replaceFirst('assets/', ''));
    await Directory(p.dirname(target)).create(recursive: true);
    final data = await rootBundle.load(src);
    final f = File(target);
    if (!f.existsSync() || f.lengthSync() != data.lengthInBytes) {
      await f.writeAsBytes(data.buffer
          .asUint8List(data.offsetInBytes, data.lengthInBytes));
    }
    return target;
  }

  Future<bool> start() async {
    if (_on) return true;
    if (!await _rec.hasPermission()) return false;
    _stream = _recognizer.createStream();
    _last = '';
    _utterance.clear();
    _lastRolling = 0;
    _settled.clear();
    final pcm = await _rec.startStream(const RecordConfig(
      // 16k 单声道 pcm16 —— 模型就是按这个训的, 换采样率要重采样,
      // 而重采样本身比识别还贵
      encoder: AudioEncoder.pcm16bits,
      sampleRate: 16000,
      numChannels: 1,
      // **不要自动增益**: 它会把安静的房间也拉到满格, 底噪被当成语音
      autoGain: false,
      echoCancel: true,
      noiseSuppress: true,
      // ── 麦克风用"通话"那一路 ──
      //
      // 缺省那一路(MIC / DEFAULT)上, echoCancel 是软件的 AcousticEchoCanceler,
      // 很多机器上挂了等于没挂 —— 它自己念的话照样被听进去, 再当成他说的
      // 发出去, 车内场景尤其容易触发这个问题.
      //
      // VOICE_COMMUNICATION 是打电话用的那一路: 厂商在这一路上接的是
      // **硬件的回声消除**, 拿喇叭正在放的声音当参考去减. 这是整个系统里
      // 唯一真的调过的一套 AEC.
      //
      // 光靠它不够(车机喇叭不在手机的参考信号里), 所以语音页在念的时候
      // 还会把听到的整个扔掉 —— 见 voice_page 的 _muted. 两层都要
      androidConfig: AndroidRecordConfig(
        audioSource: AndroidAudioSource.voiceCommunication,
      ),
    ));
    _sub = pcm.listen(_onChunk, onError: (_) => stop());
    _on = true;
    return true;
  }

  void _onChunk(Uint8List bytes) {
    final s = _stream;
    if (s == null || bytes.length < 2) return;
    // ── 不能用 asInt16List ──
    //
    // 录音回调给的 Uint8List 是**大缓冲区里的一个视图**, 其
    // offsetInBytes = 5(奇数) —— 而 asInt16List 要求 2 字节对齐,
    // 于是每一块都抛 "Offset (5) must be a multiple of BYTES_PER_ELEMENT".
    //
    // ByteData.getInt16 没有对齐要求. 官方示例也是这么写的,
    // 我一开始以为那是老代码没优化, 其实那正是在绕这个坑
    final data = ByteData.sublistView(bytes);
    final n = bytes.lengthInBytes ~/ 2;
    final f = Float32List(n);
    for (var i = 0; i < n; i++) {
      f[i] = data.getInt16(i * 2, Endian.little) / 32768.0;
    }
    s.acceptWaveform(samples: f, sampleRate: 16000);
    _utterance.addAll(f);
    _pushLevel(f);
    // isReady 是"攒够一块了吗" —— 喂进去的音频不一定正好凑满一块,
    // 所以要循环解到不 ready 为止
    while (_recognizer.isReady(s)) {
      _recognizer.decode(s);
    }
    // ── 拼音级纠错 ──
    //
    // 流式模型不认得专名: 说「值守」它写「直守」, 说「文案」写「文按」.
    // 音对字错这一类**可以无损修回来**(见 hotwords.dart).
    //
    // 每块都修, 不是只修定稿: 用户看着字一个个出来, 而中途显示
    // 一个错字再跳成对的, 比一开始就是对的更晃眼
    final text = Hotwords.fix(_recognizer.getResult(s).text);
    final end = _recognizer.isEndpoint(s);
    if (text.isNotEmpty) _spokeThisRound = true;

    // ── 滚动定稿 ──
    //
    // 不等这一段说完, 每攒够 1.5 秒就把**当前这一段**重识别一遍,
    // 用离线那版盖掉流式那版. 于是你说着说着, 前面的字自己就改好了 ——
    // 而不是憋到停顿才一次性重写.
    //
    // 能这么干的唯一理由是第二遍够便宜: 真机 RTF 0.007,
    // 12 秒音频 84ms. 换个 RTF 0.3 的模型这条路直接堵死
    if (!end &&
        _spokeThisRound &&
        _utterance.length - _lastRolling >= _rollingEvery) {
      _lastRolling = _utterance.length;
      final rolled = _secondPass();
      if (rolled != null) {
        final fixed = Hotwords.fix(rolled);
        if (fixed != _last) {
          _last = fixed;
          _emit(fixed, false);
        }
        return;
      }
    }

    if (!end && text.isNotEmpty && text != _last) {
      _last = text;
      // 只在调试构建打. **发布版一个字都不打** —— 识别结果是用户
      // 说的话, 印进系统日志等于把它交给任何能读 logcat 的东西
      // **这里也要报全文**. 只报当前段的话, 界面上的字会在
      // "全文"(滚动那条)和"半句"(流式这条)之间来回跳 ——
      // 前面已经定稿的几段一会儿在一会儿没
      _emit(text, false);
    }
    if (end) {
      // ── 静音也会触发断句, 而那时候没有话要定稿 ──
      //
      // 没人说话时 isEndpoint 可能每 2.5 秒响一次, 于是
      // 第二遍拿着**上一句的残留音频**反复跑, 同一句话被"定稿"四遍
      // (日志里 "我们这的这的皇呢" 刷了四遍).
      //
      // 判据是**流式那边这一轮有没有出过字**: 出过才说明真有人说话.
      // 只看音频长度不行 —— 静音也是有长度的
      if (_spokeThisRound) {
        _finish(s);
      }
      _utterance.clear();
      _lastRolling = 0;
      _spokeThisRound = false;
      _recognizer.reset(s);
      _last = '';
      return;
    }
  }

  /// 一句说完了 —— 拿整段音频重新识别, 把这一句**整个换掉**.
  /// 这就是用户看到的"它自己把句子重新调整了一遍"
  void _finish(sherpa.OnlineStream s) {
    final better = _secondPass();
    if (better == null) return;
    final fixed = Hotwords.fix(better);
    // **先报再并**, 顺序反了就重复一遍.
    //
    // 如果顺序反过来: 先 write 进 _settled, 再 _emit(fixed) —— 而
    // _emit 拼的是 "已定稿 + 这一段", 已定稿里已经含着这一段了,
    // 于是最后那段出现两次(日志: "…美国大兵风动性人前他…美国大兵风动性")
    _emit(fixed, true);
    _settled.write(fixed);
  }

  /// 报出去的**永远是全文**(已定稿的几段 + 正在说的这一段).
  ///
  /// 只报当前段的话, 界面那边要自己拼 —— 而拼这件事有状态,
  /// 状态放两处必然对不上(界面以为定稿了三段, 这边以为两段)
  void _emit(String seg, bool done) {
    final all = '$_settled$seg';
    assert(() {
      // ignore: avoid_print
      print('[ASR] ${done ? "定稿" : "……"} $all');
      return true;
    }());
    _out.add(AsrText(all, done));
  }

  /// 从头开始记 —— **发送之后必须调**.
  ///
  /// ── 不调会怎样 ──
  ///
  /// 麦克风开着的时候, 已定稿的几段一直攒在 _settled 里。发送完成后
  /// 点发送、输入框清空了, 但**这边一个字没清** —— 于是他说下一句
  /// "你在吗", 屏幕上出来的是之前测试的所有话再加上"你在吗"。
  ///
  /// 清空输入框是界面的事, 而累积在这儿,
  /// 两处状态必须一起清
  void resetText() {
    _settled.clear();
    _utterance.clear();
    _lastRolling = 0;
    _spokeThisRound = false;
    _last = '';
    final s = _stream;
    // 流也要重置: 不重置的话流式那边还挂着上一句的解码状态,
    // 下一个字会接在它后面出来
    if (s != null) _recognizer.reset(s);
  }

  Future<void> stop() async {
    if (!_on) return;
    _on = false;
    await _sub?.cancel();
    _sub = null;
    try {
      await _rec.stop();
    } catch (_) {}
    _level = 0;
    if (!_lv.isClosed) _lv.add(0);
    _stream?.free();
    _stream = null;
  }
}

class AsrText {
  const AsrText(this.text, this.done);
  final String text;

  /// done 这一句说完了(检测到断句). 界面据此决定要不要另起一句
  final bool done;
}
