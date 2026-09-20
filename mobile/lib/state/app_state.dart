import 'dart:async';

import 'package:flutter/widgets.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/events.dart';
import '../api/inline_cards.dart';
import '../api/os_client.dart';
import '../audio/hotwords.dart';
import '../sense/keepalive.dart';

/// 一条聊天消息.
///
/// **它不是事件, 是事件拼出来的.** 一次模型输出在账本里是几千个
/// proc.output 块, 在这儿是一条消息 —— 折叠发生在这一层,
/// 而不是让界面去画几千行.
class Msg {
  Msg({
    required this.id,
    required this.who,
    required this.pid,
    required this.at,
    required this.kind,
    String text = '',
  }) : _text = StringBuffer(text);

  final String id;

  /// who 显示出来的名字. 用户是"你", bot 是它的名字
  final String who;
  final ProcessID pid;
  final DateTime at;
  final MsgKind kind;

  final StringBuffer _text;
  String get text => _text.toString();

  /// streaming 还在长. 界面据此画那个跳动的光标

  bool streaming = true;

  /// sending 这条是**先画上去的**, 账本还没确认.
  ///
  /// 上一版不画: 理由是"发失败时屏幕上会留着一条谁也没收到的消息".
  /// 那个顾虑是真的, 但**代价选错了** —— 结果是点了发送之后整整一个
  /// 来回什么都不发生(手机的网上这一下经常是好几百毫秒), 用户看到的
  /// 是一个卡住的界面, 于是再点一次.
  ///
  /// 正确的做法是先画上去、标成"在发", 失败了再把它拿掉(话同时还回
  /// 输入框) —— 每个 IM 都是这么干的
  bool sending = false;

  /// card 这条消息是一张卡片, 不是一段话. 见 [Card]
  Card? card;

  /// why 主动消息才有 —— 它凭哪一条判据打扰你.
  ///
  /// **必须显示**: 一次打扰值不值得, 只有看见理由才判得出来;
  /// 而判不出来的话, 用户能做的只有把整个通道关掉.
  String? why;

  /// alert 这是一条"出事了"的系统行(一轮没走完). 画成警示色 ——
  /// 跟"连上了""采集端回来了"那种灰字长得一样的话, 等于没画
  bool alert = false;

  void append(String s) => _text.write(s);

  /// replace 拿最终那份盖掉流式攒的那份.
  ///
  /// **增量可能漏**: 断线重连的那几秒里吐的字没人收得到, 而最终那条
  /// 是全的 —— 不盖的话屏幕上留着一句缺了一截的话, 而它看起来是完整的
  void replace(String s) {
    _text.clear();
    _text.write(s);
  }
  void seal() => streaming = false;
}

enum MsgKind {
  /// 你说的
  user,

  /// bot 说的
  bot,

  /// 主动 —— 没有人问, 它自己开的口. 界面上必须一眼看得出跟上面那条不同
  proactive,

  /// 同屋 bot 之间的交办. 画成 bot 说的话会变成"我说的"
  relay,

  /// 系统自己的一行(连上了、断了、采集端掉线了)
  system,
}

/// 连接状态 —— 界面上那个点.
enum Conn { off, connecting, live, badToken, error }

/// 全 App 的唯一一份状态.
///
/// ── 为什么不做成三个页面各管各的 ──
///
/// 三个页面看的是**同一条流**: 群聊画它说的话, 核心页画它在不在想,
/// 设置页画这条线通没通. 各自订阅一遍的话, OS 那边就是三条 SSE,
/// 而手机上多一条长连接就是多一份电 —— 那正好是保活最贵的东西.
class AppState extends ChangeNotifier with WidgetsBindingObserver {
  AppState() {
    WidgetsBinding.instance.addObserver(this);
  }

  // ── 设置 ──
  String base = '';
  String token = '';

  // ── 我是谁 ──
  //
  //	**登录不是进门的条件**: 进这台 OS 靠的是它的 token. 账号回答的是
  //	另一个问题 —— 屋里不止一个人, 而"我现在在哪"要有唯一答案.
  //
  //	不登录照样能用, 只是这台 OS 会把你和屋里其他人的事混在一起.
  String meId = '';
  String meName = '';
  String meEmail = '';

  bool get signedIn => meId.isNotEmpty;

  /// loaded 本地那份配置读完了没有.
  ///
  /// **门要等它**: 没读完就判"没登录"的话, 每次冷启动都会闪一下登录页,
  /// 而用户明明是登着的 —— 那种闪比慢半拍难受得多
  bool loaded = false;

  /// collect 这台手机要不要把看到的报给 OS(位置、WiFi、电量).
  ///
  /// **默认关**: 位置是最敏感的一类数据, 不该因为装了个 app 就开始收.
  /// 打开这件事必须是用户自己做的一个动作
  bool collect = false;

  /// hasLocation 这会儿真的拿得到位置吗 —— 跟 collect 是两件事:
  /// 开关开着但权限被拒, 界面得说得出来
  bool hasLocation = false;

  /// deviceName 这台设备在 OS 那边叫什么. 用户可以改 —— 屋里
  /// 两台手机的话, "手机"和"手机"是分不出来的
  String deviceName = '';

  /// 只跟这几个 bot 说话. 空 = 屋里所有活着的
  final Set<ProcessID> muted = {};

  /// 用户自己加的语音热词.
  ///
  /// ── 为什么光有 bot 名字不够 ──
  ///
  /// 热词只从进程表生成的话, 就只有 12 条(而且 6 条是英文,
  /// 拼音匹配永远碰不到) —— 用户不说那 6 个中文词里的任何一个,
  /// 就永远看不到纠错。而数字人那边是 100+ 条领域专名, 所以"有感觉"。
  ///
  /// 专名是**只有用户自己知道**的: 他的项目叫什么、同事叫什么、
  /// 他常说的行话。这份表只能他来填
  List<String> hotwords = const [];

  Conn conn = Conn.off;
  String? lastError;

  List<ProcInfo> bots = const [];

  /// onBotSaid 某个 bot 刚说完一句话.
  ///
  /// **给语音那一页用**: 它要把这句念出来。让那一页自己去 diff 消息
  /// 列表的话, 得记住"上次看到哪儿" —— 而"上次"在换 bot、补课、
  /// 重连之后各有各的意思。
  void Function(ProcessID pid, String text)? onBotSaid;

  /// onBotFailed 某个 bot 这一轮没走完(turn_failed), brief 是一行原因.
  ///
  /// **给语音那一页用**: 它发完一句就在等回话, 而失败的那一轮永远不会
  /// 有回话 —— 不告诉它的话, 那一页一直停在"在想", 他后面说的话也全被
  /// 当成"还在等"扔掉(见 VoicePage)
  void Function(ProcessID pid, String brief)? onBotFailed;

  /// 每个 bot 一条线 —— **不是一个合并的大群**.
  ///
  /// 客户端左栏就是一列 bot, 点谁进谁的会话. 手机上照做:
  /// 主页是会话列表, 点进去才是对话. 一开始我做成了"一屋子人共用
  /// 一条时间线", 那既不是客户端的样子, 也不是手机上任何人认得的样子.
  final Map<ProcessID, List<Msg>> threads = {};

  /// 每个 bot 干过什么 —— **跟"说过什么"是两件事**.
  ///
  /// 会话页只画 say(说给人听的那句)。而它真正干活的痕迹在别处:
  /// 调了哪个工具、用了哪条能力、被哪条能力拦了、烧了多少 token、
  /// 状态怎么变的。
  ///
  /// 这些不能混进会话: 一次干活能产生几十条工具流水, 摊进对话里
  /// 真正说给人听的那句会被淹掉(这正是 channel != 'say' 一律不画
  /// 的理由)。但它们**必须有个地方能看** —— 出了事只有这里能对上号。
  ///
  /// 每个 bot 只留最近 200 条: 不封顶的话跑一晚上就是几万条,
  /// 而更老的在 OS 的账本里(那份才是完整的)
  final Map<ProcessID, List<Act>> acts = {};

  static const _actCap = 200;

  /// 没读的条数. **只记数不显示数字** —— 一队 bot 的消息本来就没有
  /// "读完"这回事, 数字只会变成一个永远清不掉的焦虑源(客户端那条).
  /// 列表上画的是一个点
  final Map<ProcessID, int> unread = {};

  /// 正开着哪一条. 开着的那条不算未读
  ProcessID? open;

  /// 送到你跟前的那些 —— 闹钟 / 主动判断 / 日报.
  ///
  /// **单独存一份, 不只是塞进某个 bot 的会话里**: 主动消息埋在
  /// 各自的对话里的话, 你想回头看"今天它都提醒过我什么"要一个个
  /// 点进去翻 —— 而那正是这类消息最需要的一个视图。
  ///
  /// 最新的在前 —— 这一页是用来看"刚才/今天"的
  final List<Delivery> deliveries = [];

  /// 已经看过的那几条 id. 看过的不再算"新的"
  final Set<String> _seenDeliveries = {};

  /// 名字 → pid. 主动消息里带的是**给人看的名字**(OS 那边就该给这个),
  /// 而头像是按 pid 生成的 —— 拿名字当 id 的话, 同一个 bot 在
  /// 「它跟我说过什么」和消息列表里会出现**两张不同的脸**。
  ProcessID? pidOfName(String name) {
    for (final b in bots) {
      if (b.name == name) return b.pid;
    }
    return null;
  }

  int get unseenDeliveries =>
      deliveries.where((d) => !_seenDeliveries.contains(d.id)).length;

  void markDeliveriesSeen() {
    for (final d in deliveries) {
      _seenDeliveries.add(d.id);
    }
    notifyListeners();
  }

  /// 谁在想 —— 核心页那颗球转不转看它.
  ///
  /// 用 count 而不是 bool: 屋里三个 bot 同时在跑, 一个跑完了
  /// 不该让球停下来
  int get thinkingCount => bots.where((b) => b.state.busy).length;

  OsClient? _client;

  /// 给二级页用的 —— 它们要直接调 /spend /provider 这些一次性的接口.
  /// **不给它们各自建一个 client**: 建一个就多一份 base/token 的副本,
  /// 而用户改地址的时候只会改到这一份
  OsClient? get client => _client;
  StreamSubscription<OsEvent>? _sub;
  Timer? _retry;
  Timer? _botRetry;

  Duration _backoff = const Duration(seconds: 3);

  /// 最后一次听见 OS 说话(事件或心跳)是什么时候
  DateTime _lastBeat = DateTime.fromMillisecondsSinceEpoch(0);
  bool _disposed = false;

  /// 每进程一个游标 —— 重连时只补缺的那一截
  final Map<ProcessID, int> _cursors = {};

  /// 本地先画那几条的编号 —— 只要在这次会话里不重就行
  int _echoSeq = 0;

  /// 已经画过的用户发言. 一句话发给屋里 N 个人 = N 条 input.recv,
  /// **不去重的话用户会看见自己说了 N 遍**
  final Set<String> _saidKeys = {};

  /// pid → bot 标签.
  ///
  /// ── 为什么必须有这张表 ──
  ///
  /// **进程会死, 对话不死**: OS 每重启一次, 所有 bot 的 pid 全变.
  /// 而消息是按 pid 存的话, 重启之后昨天那些话就成了孤儿 —— 它们还在
  /// 账本里, 也补到手机上了, 只是挂在一个已经不存在的 pid 下面,
  /// 界面上一条都看不见.
  ///
  /// 典型表现是: 每次重装(或者 OS 重启)聊天记录就空了.
  ///
  /// 标签从两处学: /processes 的 app 字段, 和 proc.state 事件里的
  /// labels.bot —— 后者要紧, 因为**已经退出的进程不在进程表里**
  final Map<ProcessID, String> _botOf = {};

  /// 正在长的那几条 bot 消息, 键是 pid + stream
  final Map<String, Msg> _openStreams = {};

  bool get configured => base.isNotEmpty && token.isNotEmpty;

  // ── 设置读写 ──

  Future<void> load() async {
    final p = await SharedPreferences.getInstance();
    base = p.getString('base') ?? '';
    token = p.getString('token') ?? '';
    muted
      ..clear()
      ..addAll(p.getStringList('muted') ?? const []);
    hotwords = p.getStringList('hotwords') ?? const [];
    deviceName = p.getString('deviceName') ?? '';
    meId = p.getString('meId') ?? '';
    meName = p.getString('meName') ?? '';
    meEmail = p.getString('meEmail') ?? '';
    collect = p.getBool('collect') ?? false;
    _reloadHotwords();
    loaded = true;
    notifyListeners();
    if (configured) connect();
  }

  Future<void> saveServer(String newBase, String newToken) async {
    // 末尾的斜杠会让每个 URL 变成 //processes —— 有的反代会 404,
    // 而那时候用户只看得到"连不上"
    base = newBase.trim().replaceAll(RegExp(r'/+$'), '');
    token = newToken.trim();
    final p = await SharedPreferences.getInstance();
    await p.setString('base', base);
    await p.setString('token', token);
    reconnect();
  }

  /// 登录之后记下"我是谁", 并且告诉这台 OS.
  ///
  /// **两步都要**: 存在本地是为了下次不用再登; 告诉 OS 是为了它能把
  /// 信号归到人头上 —— 少了后一步, 登录只是一个好看的空动作
  Future<void> signIn(String id, String name, String email) async {
    meId = id;
    meName = name;
    meEmail = email;
    final p = await SharedPreferences.getInstance();
    await p.setString('meId', id);
    await p.setString('meName', name);
    await p.setString('meEmail', email);
    await _tellWhoIAm();
    // 原生那一侧也要知道 —— 它才是真正决定"这条通知响不响"的地方
    await KeepAliveService.whoAmI(id);
    // 设备的主人跟着换 —— 不重报的话, 这台手机上的位置还挂在
    // 上一个人名下
    await _declareSelf();
    notifyListeners();
  }

  /// 退出 —— **只忘掉这台手机上的身份**.
  ///
  /// OS 那边认得的人不删: 他昨天的位置、他立的规矩都还在, 而那些
  /// 不该因为在一台手机上点了退出就没了
  Future<void> signOut() async {
    meId = meName = meEmail = '';
    final p = await SharedPreferences.getInstance();
    await p.remove('meId');
    await p.remove('meName');
    await p.remove('meEmail');
    await KeepAliveService.whoAmI('');
    await _declareSelf();
    notifyListeners();
  }

  Future<void> _tellWhoIAm() async {
    if (meId.isEmpty) return;
    try {
      await _client?.knowPerson(meId, meName);
    } catch (_) {
      // 这台 OS 可能还没有"人"这个概念(/person 404)——那不是错误
    }
  }

  /// 开/关位置上报.
  ///
  /// 三件事一起做, 少一件这个开关就是假的:
  ///	① 记住用户的选择(它要活过重启)
  ///	② 告诉原生那一侧(真正干活的是它, 而且它可能在 Dart 没起来时被唤醒)
  ///	③ **重报一次设备**: senses 变了 —— 一台声称能感知却从来不报的
  ///	   设备是最难查的一类, OS 会一直等一个永远不来的信号
  Future<void> setCollect(bool on) async {
    collect = on;
    final p = await SharedPreferences.getInstance();
    await p.setBool('collect', on);
    hasLocation = await KeepAliveService.setCollect(on,
        deviceId: await _deviceId());
    await _declareSelf();
    notifyListeners();
  }

  Future<void> refreshLocationPermission() async {
    final got = await KeepAliveService.hasLocation();
    if (got != hasLocation) {
      hasLocation = got;
      notifyListeners();
    }
  }

  /// 给这台设备起个名 —— 屋里两台手机的话, "手机"和"手机"分不出来
  Future<void> saveDeviceName(String name) async {
    deviceName = name.trim();
    final p = await SharedPreferences.getInstance();
    await p.setString('deviceName', deviceName);
    // 立刻重报一次, 别等下次连接 —— 否则用户改完名字, 设置页上
    // 还是旧的, 而他不知道到底存没存上
    await _declareSelf();
    notifyListeners();
  }

  Future<void> saveHotwords(List<String> words) async {
    hotwords = words;
    final p = await SharedPreferences.getInstance();
    await p.setStringList('hotwords', words);
    _reloadHotwords();
    notifyListeners();
  }

  /// bot 名字 + 用户自己加的, 合成一份.
  ///
  /// 两个来源合并而不是二选一: bot 名字是自动跟着变的(新建一个
  /// 就自动进表), 用户那份是他自己的行话 —— 少哪一半都不够用
  void _reloadHotwords() {
    Hotwords.load([
      for (final b in bots) ...[b.name, b.app],
      ...hotwords,
    ]);
  }

  Future<void> toggleMute(ProcessID pid) async {
    muted.contains(pid) ? muted.remove(pid) : muted.add(pid);
    final p = await SharedPreferences.getInstance();
    await p.setStringList('muted', muted.toList());
    notifyListeners();
  }

  /// 屋里还活着的几个
  List<ProcInfo> get live => bots.where((b) => b.state.alive).toList();

  // ── 连接 ──

  /// 这个 pid 属于哪条会话 —— **bot 标签优先, pid 兜底**.
  ///
  /// 认不出标签的(老账本、没打标签的进程)照旧按 pid 走: 那时候至少
  /// 这一次运行里是连贯的
  String keyOf(ProcessID pid) => _botOf[pid] ?? pid;

  List<Msg> thread(ProcessID pid) => threads[keyOf(pid)] ?? const [];
  List<Act> activity(ProcessID pid) => acts[keyOf(pid)] ?? const [];
  Msg? lastOf(ProcessID pid) {
    final t = threads[keyOf(pid)];
    return (t == null || t.isEmpty) ? null : t.last;
  }

  /// 它这会儿是不是正在把话往外吐.
  ///
  /// **跟"进程在跑"不是一回事**: 进程 running 的那一段里, 它可能在想、
  /// 在调工具、也可能已经在说了 —— 而这三件事对等着的人是不同的消息.
  /// 有一条还没封口的流 = 字正在出来
  bool replying(ProcessID pid) {
    for (final m in threads[pid] ?? const <Msg>[]) {
      if (m.streaming && m.kind != MsgKind.user) return true;
    }
    return false;
  }

  int get totalUnread => unread.values.fold(0, (a, b) => a + b);

  void openThread(ProcessID pid) {
    open = keyOf(pid);
    unread.remove(keyOf(pid));
    notifyListeners();
  }

  void closeThread() {
    open = null;
    notifyListeners();
  }

  void reconnect() {
    _cursors.clear();
    _saidKeys.clear();
    _openStreams.clear();
    threads.clear();
    acts.clear();
    deliveries.clear();
    _seenDeliveries.clear();
    unread.clear();
    connect();
  }

  void connect() {
    if (_disposed || !configured) return;
    _retry?.cancel();
    _botRetry?.cancel();
    _sub?.cancel();
    _client?.close();
    _client = OsClient(base: base, token: token);
    _set(Conn.connecting);
    _pullBots();
    _declareSelf();
    _tellWhoIAm();

    _sub = _client!
        .stream(
          cursors: _cursors,
          onOpen: () {
            _backoff = const Duration(seconds: 3); // 接上了, 退避归零
            _set(Conn.live);
          },
          onBeat: () => _lastBeat = DateTime.now(),
        )
        .listen(
      _onEvent,
      onError: _onStreamDown,
      // 流自己结束(OS 重启、反代掐了)跟出错是同一件事:
      // **静默地不再收事件, 跟"今天很安静"长得一模一样**
      onDone: () => _onStreamDown(OsError('流结束了')),
      cancelOnError: true,
    );
  }

  void _onStreamDown(Object e, [StackTrace? _]) {
    if (_disposed) return;
    if (e is OsAuthError) {
      // **重试一万次也没用** —— 该做的是把用户送去设置页
      _set(Conn.badToken, 'token 不对');
      return;
    }
    _set(Conn.error, e.toString());
    // 退避重连. 手机的网是断续的, 断线是常态不是故障 ——
    // 所以不弹窗、不报错, 悄悄接回来就行.
    //
    // 会退避是因为**够不着的时候多半要够不着一阵子**(进电梯、OS 那头
    // 在重启): 固定 3 秒会在没信号的地铁里空转半小时. 但第一次要快,
    // 因为最常见的断线就是一瞬间的.
    _retry = Timer(_backoff, connect);
    _backoff = _backoff * 2 > const Duration(seconds: 30)
        ? const Duration(seconds: 30)
        : _backoff * 2;
  }

  /// 跟 OS 报到 —— **我是谁, 我能怎么告诉你**.
  ///
  /// ── 为什么每次连上都报一次 ──
  ///
  /// 能力**会变**: 用户在系统设置里关掉了通知权限, 这台手机就不再
  /// presents alert 了。只在第一次报的话, OS 会一直以为它吵得醒人 ——
  /// 而那种错的表现是"重要的事它只是静悄悄摆在那儿"。
  ///
  /// 报到失败不影响别的: 它只是让 OS 少认识一台设备, 而推送那条路
  /// 走的是事件流, 跟这里没关系
  Future<void> _declareSelf() async {
    final id = await _deviceId();
    // 每次报到之前先问一次真的 —— 权限可能在系统设置里被改过,
    // 而我们上次记下的那个值不作数
    hasLocation = await KeepAliveService.hasLocation();
    try {
      await _client?.declareDevice(
        id: id,
        name: deviceName.isEmpty
            ? (meName.isEmpty ? '我的手机' : '$meName的手机')
            : deviceName,
        kind: 'phone',
        // **这台设备是谁的** —— 一条位置信号归谁, 全靠这一个字段.
        // 没登录就留空: 那时候它是"屋子的手机", 而不是某个人的
        owner: meId,
        // ── 照实说, 一样都不许多说 ──
        //
        //	**开关开着 ≠ 报得出来**: 系统那个定位权限框可以被拒, 而那时候
        //	开关已经是开的了. 照开关声明的话, OS 会一直等一个永远不来的
        //	位置信号 —— 而这是最难查的一类故障: 没有报错, 只是某件事
        //	不发生.
        //
        //	电量不用任何权限, 所以它跟着开关走; 位置和 WiFi 都要定位权限
        //	(安卓 10 起拿 SSID 也要), 所以它们跟着**权限**走
        senses: [
          if (collect) 'battery',
          if (collect && hasLocation) ...['location', 'network'],
        ],
        presents: const ['notify', 'alert'],
      );
    } catch (_) {
      // 这台 OS 可能还没接感知层(/device 是 404)——那不是错误,
      // 只是它还没有设备这个概念
    }
  }

  /// 这台设备的稳定标识 —— **换了就是另一台**.
  ///
  /// 存在本地: 拿会变的东西(IP、安装时间)当 id 的话, 每次重装
  /// 都会在 OS 那边多出一台幽灵设备
  Future<String> _deviceId() async {
    final p = await SharedPreferences.getInstance();
    var id = p.getString('deviceId') ?? '';
    if (id.isEmpty) {
      id = 'phone-${DateTime.now().microsecondsSinceEpoch.toRadixString(36)}';
      await p.setString('deviceId', id);
    }
    return id;
  }

  Future<void> _pullBots() async {
    try {
      final list = await _client!.processes();
      if (_disposed) return;
      // ── 感知判断不是一个能聊天的对象 ──
      //
      // 它是常驻的主动进程: 摘要投给它, 它判断值不值得打扰你 ——
      // **没有可调用能力, 也不接收对话输入**. 摆进消息列表会变成一个
      // 打不开的空会话, 发出去的每一句也都会石沉大海.
      //
      // 藏的是"能不能跟它说话", 不是"它存不存在": 它照样在
      // /processes 里, 电脑上看得到, 它花的 token 也照样进「花了多少」
      bots = list.where((b) => b.app != 'sense').toList();
      // 学一遍 pid → bot 标签 —— 进程会死, 对话不死
      for (final b in list) {
        if (b.app.isNotEmpty) _botOf[b.pid] = b.app;
      }
      // ── 热词跟着进程表走 ──
      //
      // 用户最可能说到、也最容易被写错的词, 就是**屋里这几个 bot 的
      // 名字和项目名**: 「找值守看一眼」「让文案起个草」.
      // 流式模型不认得这些专名, 但它们的拼音是对的 —— 正好能修.
      //
      // 每次拉进程表就重建一次. 写死一份 txt 的话, 新建一个 bot
      // 之后它的名字就永远识别不对
      _reloadHotwords();
      notifyListeners();
    } on OsAuthError {
      _set(Conn.badToken, 'token 不对');
    } catch (_) {
      // ── 拉不到要**再拉** ──
      //
      // 原来这里是空的, 理由是"进程表拉不到不算断线, 流还好好的".
      // 前半句对, 后半句是错的结论: 拉不到的后果不是少一点信息, 是
      // 消息列表整个空着 —— 而空列表跟"这台 OS 上真没有 bot"长得
      // 一模一样。冷启动那一下最容易撞上(网还没就绪), 于是你看到的是
      // 服务器上明明六个 bot, 手机上说一个都没有, 而且**永远不会自己
      // 好起来**, 因为没人再拉第二次。
      if (bots.isEmpty) {
        _botRetry?.cancel();
        _botRetry = Timer(const Duration(seconds: 3), () {
          if (!_disposed && _client != null) _pullBots();
        });
      }
    }
  }

  // ── 切回前台先验活 ──
  //
  // 后台里 Timer 是被冻住的: Doze 一压, 那个 3 秒退避可能几十分钟才跑
  // 一次, 甚至根本不跑. 于是你切回来看到的是一个卡在"连不上"的界面,
  // 而它其实什么都没在做 —— 这就是**只有手点重连才在线**的那半个原因
  // (另外半个是半死的 socket, 见 OsClient.stream 里那个 idle 超时).
  //
  // 所以回到前台就当场判一次: 连接没在 live, 或者超过一个心跳周期没
  // 听见动静, 立刻重连. 带着游标, 补的是缺的那一截, 不会重来一遍.
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state != AppLifecycleState.resumed || _disposed || !configured) return;
    final quiet = DateTime.now().difference(_lastBeat);
    if (conn != Conn.live || quiet > const Duration(seconds: 30)) connect();
  }

  // ── 一帧最多重建一次 ──
  //
  // 一条流式回答是**几十上百个 output 事件**, 每个都 notifyListeners
  // 的话界面就按事件的节奏重建 —— 那个节奏比屏幕还快, 多出来的每一次
  // 都是白烧的帧. 攒 16ms 合成一次, 上限正好是 60Hz.
  //
  // 代价是所有更新晚最多 16ms —— 一帧, 看不见
  Timer? _coalesce;

  @override
  void notifyListeners() {
    if (_disposed || _coalesce != null) return;
    _coalesce = Timer(const Duration(milliseconds: 16), () {
      _coalesce = null;
      if (!_disposed) super.notifyListeners();
    });
  }

  void _set(Conn c, [String? err]) {
    conn = c;
    lastError = err;
    notifyListeners();
  }

  // ── 事件 → 消息 ──

  /// debugFeed 直接喂一条事件进来 —— **只给测试用**.
  ///
  ///	不开这个口子的话, "封口时有没有吆喝"这件事只能靠真机跑一遍,
  ///	而它已经悄悄坏过一次: 流式那条路上漏了, 生产上语音模式全哑,
  ///	而账本里一切正常。
  @visibleForTesting
  void debugFeed(Map<String, dynamic> raw) => _onEvent(OsEvent.fromJson(raw));

  void _onEvent(OsEvent e) {
    if (conn != Conn.live) _set(Conn.live);
    // 游标先记. **哪怕这条我们不画** —— 不记的话重连会把它再补一遍
    final next = e.seq + 1;
    if ((_cursors[e.pid] ?? 0) < next) _cursors[e.pid] = next;

    switch (e.kind) {
      case Ev.inputRecv:
        _onSaid(e);
      case Ev.procOutput:
        _onOutput(e);
      case Ev.procDelta:
        // 边生成边吐的那一段 —— 追加到同一条流上. 见 Ev.procDelta
        final d = OutputChunk.of(e.payload);
        _stream(e, d.stream, d.text, false);
      case Ev.procState:
        _onState(e);
        _act(e, ActKind.state, _stateWord(e), '');
      case Ev.interruptVerdict:
        _onProactive(e);
      case Ev.dailyReport:
        _onDaily(e);
      case Ev.collectorDown:
        _sys('采集端掉线了: ${e.payload['source'] ?? '?'}', e);
      case Ev.collectorUp:
        _sys('采集端回来了: ${e.payload['source'] ?? '?'}', e);
      case Ev.delivery:
        final d = Delivery.of(e);
        // 空的丢掉 —— OS 那边也拦了一道, 两边都拦是有意的:
        // 一条空通知在列表里是个点不开的空行
        //
        // 不是给我的也丢掉: **她的提醒不该出现在他的列表里**.
        // 空的 to = 屋里所有人; 没登录(meId 为空)的时候只收公共的 ——
        // 那时候这台手机不代表任何人
        if (d.text.isNotEmpty && (d.to.isEmpty || d.to == meId)) {
          deliveries.insert(0, d);
        }
      case Ev.capUsed:
        _act(e, ActKind.cap, '用了能力',
            '${e.payload["axis"] ?? ""} ${e.payload["scope"] ?? ""}');
      case Ev.capDenied:
        // **被拦下来的比用上的更值得看**: 它说明这个 bot 想干一件
        // 它没权限干的事 —— 那通常是"为什么它没做成"的答案
        _act(e, ActKind.denied, '被能力拦了',
            '${e.payload["axis"] ?? ""} ${e.payload["scope"] ?? ""}');
      case Ev.budgetSpent:
        final i = (e.payload['tokensIn'] as num?)?.toInt() ?? 0;
        final o = (e.payload['tokensOut'] as num?)?.toInt() ?? 0;
        if (i + o > 0) _act(e, ActKind.spend, '烧了 token', '进 $i · 出 $o');
      case Ev.procOutcome:
        final ok = e.payload['ok'] == true;
        _act(e, ok ? ActKind.done : ActKind.failed,
            ok ? '干完了' : '失败了', '${e.payload["summary"] ?? ""}');
      default:
        // 不认得的种类原样忽略 —— 内核加一种而手机还没认的时候,
        // 应该安静地跳过, 而不是让整条流解析失败
        return;
    }
    notifyListeners();
  }

  void _onSaid(OsEvent e) {
    final said = InputSaid.of(e.payload);
    if (said.text.trim().isEmpty) return;

    if (said.relay) {
      // 这条消息来自同屋 bot 的交办, 不是当前用户发出的; 否则界面会把
      // 非用户消息显示成用户消息.
      _put(e.pid, Msg(
        id: '${e.pid}#${e.seq}',
        who: said.from,
        pid: e.pid,
        at: e.time,
        kind: MsgKind.relay,
        text: said.text,
      )..seal());
      return;
    }
    // ── 机器自己叫醒的那一轮, 一个字都不画 ──
    //
    //	定时任务的题目不是用户打的字, 它的回执也不是对人说的话.
    //	一个 bot 的会话 58 行里可能有 16 行是"（非窗口期，不动。）"
    if (said.quiet) return;
    // 一句话投给屋里 N 个人 = N 条 input.recv, 只画第一条
    if (!_saidKeys.add(said.key)) return;

    // 先画上去的那条在这儿被认领: 同一个 pid、同样的字、还标着"在发".
    // **认领而不是替换**, 是为了不让它跳位置 —— 屏幕上那条已经在那儿了,
    // 删一条再插一条在视觉上是一次闪烁
    final t = threads[keyOf(e.pid)];
    for (var i = (t?.length ?? 0) - 1; i >= 0; i--) {
      final m = t![i];
      if (m.sending && m.kind == MsgKind.user && m.text == said.text) {
        m.sending = false;
        return;
      }
    }

    _put(e.pid, Msg(
      id: said.key,
      who: said.from,
      pid: e.pid,
      at: e.time,
      kind: MsgKind.user,
      text: said.text,
    )..seal());
  }

  /// 工具流水也进活动 —— **它是"它干了什么"最直接的记录**
  void _actOutput(OsEvent e, OutputChunk out) {
    if (out.channel == 'tool') {
      // 一次工具调用会分成很多块, 只在收尾那块记一条 ——
      // 不然一次调用在活动里是几十行
      if (out.done && out.text.trim().isNotEmpty) {
        _act(e, ActKind.tool, '调了工具', out.text.trim());
      }
    } else if (out.channel == 'think' && out.done) {
      _act(e, ActKind.think, '想了一下', out.text.trim());
    }
  }

  void _act(OsEvent e, ActKind kind, String what, String detail) {
    final list = acts[keyOf(e.pid)] ??= [];
    list.add(Act(kind, what, detail, e.time));
    // 只留最近的: 从头删而不是不加 —— 新的比老的有用
    if (list.length > _actCap) list.removeRange(0, list.length - _actCap);
  }

  String _stateWord(OsEvent e) => switch (e.payload['state']) {
        'created' => '起来了',
        'running' => '开始跑',
        'waiting' => '等着',
        'suspended' => '挂起',
        'exited' => '退出了',
        'failed' => '崩了',
        _ => '状态变了',
      };

  void _onOutput(OsEvent e) {
    final out = OutputChunk.of(e.payload);
    _actOutput(e, out);
    // ── 卡片 ──
    //
    //	bot 的多模态出口: 地图、图、表格这些用一段文字讲不清的东西,
    //	走 ui 这个通道发一段 JSON(见 go/agent/show.go).
    //
    //	**这条路一直在发, 而手机端一直在丢** —— 卡片发出去了, 用户
    //	什么都没看见, 而 bot 那边收到的是"已经展示了", 于是它接着说
    //	"如上图". 没有任何一处报错.
    if (out.channel == 'ui') {
      final card = Card.parse(out.text);
      if (card != null) {
        _put(
            e.pid,
            Msg(
              id: '${e.pid}/${out.stream}/card',
              who: _nameOf(e.pid),
              pid: e.pid,
              at: e.time,
              kind: MsgKind.bot,
            )..card = card);
      }
      return;
    }
    // ── 最终那一条 ──
    //
    //	phase:"reply" 是**账本里的真相**: 重启后重建对话窗口、事后召回
    //	读的都是它. 而边生成边吐的那些增量走的是另一条**不落账**的路
    //	(Ev.procDelta) —— 一次回复上百段, 落账的话账本每条回复膨胀
    //	一百倍, 而它们加起来跟这一条一模一样.
    //
    //	它**带着流水号**: 这一轮如果吐过增量, 屏幕上已经有一条正在长的
    //	消息了, 按流水号认出来就行 —— **不用猜**. 猜的结果是同一句话
    //	否则同一句话可能在屏幕上出现两遍.
    //
    //	没有流水号 = 这一轮没流式(老 OS、或者供应商不支持), 那它自己
    //	就是那条消息.
    final phase = '${e.payload['phase'] ?? ''}';
    // 机器自己叫醒那一轮的回话是**任务回执**, 不是对人说的话 ——
    // 见 InputSaid.quiet
    if (e.payload['quiet'] == true) return;
    if (phase == 'turn_failed') {
      _turnFailed(e);
      return;
    }
    // model_err 是**重试途中**的那几次 —— 上层连着重试三次, 每次一条,
    // 最后兜底的那条 turn_failed 里已经写着"模型连续 3 次出错". 这里
    // 再各画一条就是刷屏(控制台那边为这个专门做了合并). 手机上只画最后那条
    if (phase == 'model_err') return;
    if (phase == 'reply') {
      final key = '${e.pid}/${out.stream}';
      final live = _openStreams[key];
      if (out.stream.isNotEmpty && live != null) {
        // **拿最终那份为准**: 增量可能因为断线漏了几段, 而这一条是全的
        live.replace(out.text);
        live.seal();
        _openStreams.remove(key);
        // ── 这一句原来没有, 而它让整个语音模式是哑的 ──
        //
        //	封口有两条路: 流式那条在这儿(收过增量), 非流式那条在
        //	_stream 里. 而"它说完了"这个吆喝只挂在后一条上 ——
        //	于是生产上(流式开着的)语音页永远等不到回话, 球一直转,
        //	而账本里明明 1 秒就回了。
        //
        //	**同一件事有两个出口的时候, 挂在其中一个上就是挂错了。**
        _botSaid(m: live);
        notifyListeners();
        return;
      }
      _stream(e, out.stream.isEmpty ? 'r${e.seq}' : out.stream, out.text, true);
      return;
    }
    // **群聊只画 say.** think 和 tool 是它干活的流水,
    // 摊进群聊的话真正说给人听的那句会被淹掉
    if (out.channel != 'say') return;
    if (out.text.isEmpty && !out.done) return;

    final key = '${e.pid}/${out.stream}';
    var m = _openStreams[key];
    if (m == null) {
      m = Msg(
        id: key,
        who: _nameOf(e.pid),
        pid: e.pid,
        at: e.time,
        kind: MsgKind.bot,
      );
      _openStreams[key] = m;
      _put(e.pid, m);
    }
    m.append(out.text);
    if (out.done) {
      m.seal();
      _openStreams.remove(key);
    }
  }

  /// 这一轮没走完 —— **必须看得见**.
  ///
  ///	三件事:
  ///	① 这个 bot 还在长的那几条先封口 —— 不封的话尾巴上那根光标一直闪,
  ///	   看着像它还在说, 而这一轮已经死了
  ///	② 在它的会话里落一行"这一轮没做完：原因"(警示色)
  ///	③ 吆喝语音那一页 —— 它在等回话(见 [onBotFailed])
  ///
  ///	**同一件事只占一行**: 停滞检测和中止会前后脚各报一次, 两条正文一样
  ///	(或者一条包着另一条)的话, 屏幕上只留先到的那条 —— 跟控制台 rows.ts
  ///	同一条规矩
  void _turnFailed(OsEvent e) {
    _openStreams.removeWhere((k, m) {
      if (m.pid != e.pid) return false;
      m.seal();
      return true;
    });
    const head = '这一轮没做完：';
    final brief = TurnFailed.of(e.payload);
    final t = threads[keyOf(e.pid)];
    final last = (t == null || t.isEmpty) ? null : t.last;
    if (last != null && last.alert && last.text.startsWith(head)) {
      final was = last.text.substring(head.length);
      // 一模一样, 或者新的这条只是给上一条加了个前缀 —— 同一件事
      if (was == brief || brief.contains(was)) return;
    }
    _put(e.pid, Msg(
      id: '${e.pid}#${e.seq}',
      who: '',
      pid: e.pid,
      at: e.time,
      kind: MsgKind.system,
      text: '$head$brief',
    )
      ..alert = true
      ..seal());
    onBotFailed?.call(e.pid, brief);
  }

  /// 往一条流里追加. 收口之后那条消息就不再长了.
  ///
  ///	**每一轮一个 stream id**: 共用一个的话, 第二次回答会接在第一次
  ///	那条后面 —— 屏幕上是一个越来越长的气泡, 而它其实是两次对话
  void _stream(OsEvent e, String stream, String text, bool done) {
    final key = '${e.pid}/$stream';
    var m = _openStreams[key];
    if (m == null) {
      if (text.isEmpty && done) return;
      m = Msg(
        id: key,
        who: _nameOf(e.pid),
        pid: e.pid,
        at: e.time,
        kind: MsgKind.bot,
      );
      _openStreams[key] = m;
      _put(e.pid, m);
    }
    if (text.isNotEmpty) m.append(text);
    if (done) {
      _liftCards(e, key, m);
      m.seal();
      _openStreams.remove(key);
      _botSaid(m: m);
    }
  }

  /// 它说完一句了 —— 吆喝一声。
  ///
  /// **封口有两条路**：流式那条(收过增量)和非流式那条。这个吆喝
  /// 原来只挂在后一条上，于是生产上语音页永远等不到回话——球一直转，
  /// 而账本里明明 1 秒就回了。
  ///
  /// 同一件事有两个出口的时候，挂在其中一个上就是挂错了。
  void _botSaid({required Msg m}) {
    final say = onBotSaid;
    if (say != null && m.text.trim().isNotEmpty) say(m.pid, m.text);
  }

  /// 把正文里手写的卡片标记抠出来, 变成真的卡片.
  ///
  ///	**只在封口那一下做**: 流式过程中标记是半截的, 那时候解析出来的
  ///	是垃圾. 而封口之后这一句已经定了.
  ///
  ///	卡片的唯一出口是 show 工具(走 ui 通道). 正文里写标记是模型自己
  ///	发明的, 不会变成卡片 —— 而屏幕上留着的是一段 JSON. 见
  ///	api/inline_cards.dart 那段: 原样显示、悄悄删掉、照着画,
  ///	三条路里只有第三条不丢信息.
  void _liftCards(OsEvent e, String key, Msg m) {
    final got = InlineCards.take(m.text);
    if (got.cards.isEmpty) return;
    m.replace(got.text);
    var rest = got.cards;
    // 剩下的话是空的 = 这条消息本身就是一张卡. 别留一个空气泡
    if (got.text.isEmpty && m.card == null) {
      m.card = rest.first;
      rest = rest.sublist(1);
    }
    for (var i = 0; i < rest.length; i++) {
      _put(
          e.pid,
          Msg(
            id: '$key/inline$i',
            who: _nameOf(e.pid),
            pid: e.pid,
            at: e.time,
            kind: MsgKind.bot,
          )
            ..card = rest[i]
            ..seal());
    }
  }

  void _onState(OsEvent e) {
    // **从事件里学标签**: 已经退出的进程不在进程表里, 而它说过的话
    // 照样要归到那条会话下面 —— 否则每次 OS 重启, 昨天的聊天记录
    // 就成了一堆挂在死 pid 上的孤儿
    final labels = e.payload['labels'];
    if (labels is Map) {
      final bot = '${labels['bot'] ?? ''}';
      if (bot.isNotEmpty) _botOf[e.pid] = bot;
    }
    final st = parseState(e.payload['state'] as String?);
    final i = bots.indexWhere((b) => b.pid == e.pid);
    if (i < 0) {
      // 新进程. 名字要去进程表拿, 事件里没有
      _pullBots();
      return;
    }
    final b = bots[i];
    // **copyWith, 不是逐字段重建** —— 重建那种写法在加字段的
    // 那一天必然漏掉新字段, 而且是静默的(见 ProcInfo.copyWith)
    bots = List.of(bots)..[i] = b.copyWith(state: st);
    // ── 不在跑了就把它那几条还在长的消息封口 ──
    //
    // **判据是"没在跑", 不是"进程死了"**. 内核的 waiting 意思是
    // "停在 Recv 上等下一句话" —— 也就是这一轮已经说完了.
    //
    // 只在进程死掉时封口的话, 一条正常答完的消息尾巴上会永远挂着
    // 那根光标: 补齐历史时尤其明显(账本里的 done 不一定跟着那一块),
    // 于是一屏老消息全在闪, 看着像它们都还没说完.
    if (!st.busy) {
      _openStreams.removeWhere((k, m) {
        if (m.pid != e.pid) return false;
        m.seal();
        return true;
      });
    }
  }

  /// 主动消息 —— 没有人问, 它自己开的口.
  ///
  /// 只画真的说出去了的那两种. defer(攒着)和 duplicate(重复)照样进账本,
  /// 但**它们的意思恰恰是"这次不打扰你"** —— 画出来就等于打扰了,
  /// 那整套打扰预算就白做了.
  void _onProactive(OsEvent e) {
    final v = e.payload['verdict'] as String? ?? '';
    if (v != 'deliver' && v != 'breakthrough') return;
    final text = (e.payload['text'] as String? ?? '').trim();
    if (text.isEmpty) return;
    _put(e.pid, Msg(
      id: '${e.pid}#${e.seq}',
      who: v == 'breakthrough' ? '主动 · 破例' : '主动',
      pid: e.pid,
      at: e.time,
      kind: MsgKind.proactive,
      text: text,
    )
      ..why = e.payload['why'] as String?
      ..seal());
  }

  void _onDaily(OsEvent e) {
    final text = (e.payload['text'] as String? ?? '').trim();
    if (text.isEmpty) return;
    _put(e.pid, Msg(
      id: '${e.pid}#${e.seq}',
      who: '日报',
      pid: e.pid,
      at: e.time,
      kind: MsgKind.proactive,
      text: text,
    )..seal());
  }

  void _sys(String text, OsEvent e) {
    _put(e.pid, Msg(
      id: '${e.pid}#${e.seq}',
      who: '',
      pid: e.pid,
      at: e.time,
      kind: MsgKind.system,
      text: text,
    )..seal());
  }

  /// 落一条 —— **未读只在这一处记**, 免得漏掉某一类消息
  /// **所有按会话存的东西都从这儿归一化** —— 一处收口, 别处照旧传 pid.
  ///
  /// 散着写的话, 加一处新的存储时必然漏掉归一化那一步, 而症状是
  /// "有些东西重启后还在, 有些没了"
  void _put(ProcessID pid, Msg m) {
    final k = keyOf(pid);
    (threads[k] ??= []).add(m);
    // 自己说的那条不算未读; 正开着的那条也不算
    if (m.kind != MsgKind.user && k != open) {
      unread[k] = (unread[k] ?? 0) + 1;
    }
  }

  String _nameOf(ProcessID pid) {
    for (final b in bots) {
      if (b.pid == pid) return b.name;
    }
    return pid;
  }

  /// 停一个 bot —— 它还在, 只是不跑了.
  ///
  /// **停不是删**: 停了还能在电脑上拉起来, 账本和工作区都在
  Future<void> stopBot(String name) async {
    await _client!.stopBot(name);
    await _pullBots();
  }

  /// 改岗位 / 换工作区 / 换房间.
  ///
  /// **三个都会换一个进程**: 这几样都是启动参数, 不是能中途改的状态
  /// (能力更是 —— landlock 只能收紧不能放宽). 对话不断, 但它手上
  /// 正干的那一轮会断
  Future<void> setRole(String name, String role) async {
    await _client!.setRole(name, role);
    await _pullBots();
  }

  Future<void> rebind(String name, String work) async {
    await _client!.rebind(name, work);
    await _pullBots();
  }

  Future<void> moveBot(String name, String thread) async {
    await _client!.move(name, thread);
    await _pullBots();
  }

  /// 删掉一个 bot —— **不可逆**.
  ///
  /// 进程没了, 界面上再也够不着它. 账本里它说过的话还在(append-only),
  /// 但那要在电脑上翻. 所以界面必须先问一次
  Future<void> forgetBot(ProcessID pid) async {
    await _client!.forget([pid]);
    threads.remove(pid);
    acts.remove(pid);
    unread.remove(pid);
    await _pullBots();
  }

  /// 新建一个 bot. 建完把进程表拉一遍, 它就出现在列表里
  Future<ProcessID> createBot(String name, {String role = ''}) async {
    final pid = await _client!.create(name: name.trim(), role: role.trim());
    await _pullBots();
    return pid;
  }

  // ── 说话 ──

  /// 只发给一个人.
  ///
  /// **一句话发给一屋子**那条路(pids 一次给全)留在 OsClient 里没动 ——
  /// 它是协议的一部分. 只是手机上的入口是"进某个 bot 的会话再说话",
  /// 跟客户端一样: 你先选人, 再说话.
  Future<void> send(
    String text,
    ProcessID pid, {
    List<Attach> images = const [],
    /// voice 他在用耳朵听这一轮的回答 —— 见 [OsClient.say]
    bool voice = false,
  }) async {
    final t = text.trim();
    // 只发图不打字是常见的 —— 那时候文本给一个默认的,
    // 否则 OS 那边收到一句空话
    if (t.isEmpty && images.isEmpty) return;
    final shown = t.isEmpty ? '（图片）' : t;

    // ── 先画上去 ──
    //
    // 这条是本地的, 账本还没确认(见 [Msg.sending]). 账本那条 input.recv
    // 回来的时候在 [_onSaid] 里认领它, 而不是再画一条
    final echo = Msg(
      id: 'local:${_echoSeq++}',
      who: '你',
      pid: pid,
      at: DateTime.now(),
      kind: MsgKind.user,
      text: shown,
    )
      ..seal()
      ..sending = true;
    _put(pid, echo);
    notifyListeners();

    try {
      await _client!.say(
          text: shown, pids: [pid], images: images, voice: voice);
    } catch (err) {
      // 发不出去就**把它拿掉** —— 话会被还回输入框(chat_page 那边做的),
      // 留一条灰着的假消息在那儿只会让人以为说过了
      threads[keyOf(pid)]?.remove(echo);
      lastError = err.toString();
      notifyListeners();
      rethrow;
    }
  }

  @override
  void dispose() {
    _disposed = true;
    WidgetsBinding.instance.removeObserver(this);
    _retry?.cancel();
    _botRetry?.cancel();
    _coalesce?.cancel();
    _sub?.cancel();
    _client?.close();
    super.dispose();
  }
}


/// 一条活动 —— 它干了什么, 不是它说了什么
class Act {
  const Act(this.kind, this.what, this.detail, this.at);
  final ActKind kind;

  /// what 一句话说清是哪类事
  final String what;

  /// detail 具体是什么(工具名、能力轴、token 数)
  final String detail;
  final DateTime at;
}

enum ActKind {
  /// 状态变了(起来/开始跑/退出/崩了)
  state,

  /// 调了工具
  tool,

  /// 它的思考
  think,

  /// 用了一条能力
  cap,

  /// **被能力拦了** —— 这一类最值得看: 它是"为什么没做成"的答案
  denied,

  /// 烧了 token
  spend,
  done,
  failed,
}
