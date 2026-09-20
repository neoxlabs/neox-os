/// events —— 跟 go/abi/types.go 的 Event 一字不差的镜像.
///
/// 这一份**不许自己发明字段**. 手机跟网页控制台一样, 只是订阅者之一,
/// 不是真相源; 凡是这里有而内核没有的字段, 迟早会变成"只有手机知道的
/// 状态" —— 而那种状态换个客户端就对不上了.
///
/// packages/apps/console/src/os/events.ts 是同一份东西的 TS 版.
/// **两边要一起改**, 只改一边的话, 手机会安静地少画一类事件.
library;

import 'dart:convert';

typedef ProcessID = String;

enum ProcState { created, running, waiting, suspended, exited, failed }

ProcState parseState(String? s) => switch (s) {
      'running' => ProcState.running,
      'waiting' => ProcState.waiting,
      'suspended' => ProcState.suspended,
      'exited' => ProcState.exited,
      'failed' => ProcState.failed,
      _ => ProcState.created,
    };

extension ProcStateX on ProcState {
  bool get alive => this != ProcState.exited && this != ProcState.failed;
  /// 忙 —— 核心动画据此转起来
  bool get busy => this == ProcState.running;
}

/// 事件种类与内核使用相同的字符串, 不转成枚举:
/// 内核加一种而手机还没认的时候, 应该原样收下并忽略,
/// 而不是解析失败把整条流打断.
class Ev {
  static const procState = 'proc.state';
  static const procOutput = 'proc.output';

  /// procDelta 边生成边吐的那一小段字 —— **不在账本里**.
  ///
  /// 一次回复上百段, 落账的话账本每条回复膨胀一百倍, 而它们加起来的
  /// 信息量跟最后那条 proc.output(phase:reply) 一模一样。
  /// 所以它只发给此刻正连着的人：早两秒看见字，仅此而已。
  ///
  /// 断线重连的人收到的是完整那条，不缺任何东西
  static const procDelta = 'proc.delta';
  static const procOutcome = 'proc.outcome';
  static const capUsed = 'cap.used';
  static const capDenied = 'cap.denied';
  static const budgetSpent = 'budget.spent';

  /// delivery 主动消息的**唯一出口**.
  ///
  /// OS 侧把闹钟到点、主动判断、日报三条来源全汇进这一种 ——
  /// 手机端只认它。上一版认三种, 于是漏掉了闹钟(它走的是
  /// proc.output, 三种里一个都不是): 闹钟响了、账本里有、
  /// 桌面上有, 而手机上一条通知都没有
  static const delivery = 'delivery';
  static const decideRequested = 'decide.requested';
  static const decideResolved = 'decide.resolved';
  static const inputRecv = 'input.recv';
  static const correction = 'user.correction';
  static const signalIn = 'signal.in';
  static const signalDigest = 'signal.digest';
  static const wakeSet = 'wake.set';
  static const wakeFired = 'wake.fired';
  static const dailyReport = 'daily.report';
  static const interruptVerdict = 'interrupt.verdict';
  static const collectorDown = 'collector.down';
  static const collectorUp = 'collector.up';
}

class OsEvent {
  /// seq 进程内单调递增. **归位靠它, 不靠到达顺序**
  final int seq;
  final ProcessID pid;
  final int at;
  final String kind;
  final Map<String, dynamic> payload;

  const OsEvent({
    required this.seq,
    required this.pid,
    required this.at,
    required this.kind,
    required this.payload,
  });

  factory OsEvent.fromJson(Map<String, dynamic> j) => OsEvent(
        seq: (j['seq'] as num?)?.toInt() ?? 0,
        pid: j['pid'] as String? ?? '',
        at: (j['at'] as num?)?.toInt() ?? 0,
        kind: j['kind'] as String? ?? '',
        // payload 可能是任何东西(内核那边是 any). 不是对象就装进一个盒子,
        // 免得后面每处都要判一次类型
        payload: j['payload'] is Map<String, dynamic>
            ? j['payload'] as Map<String, dynamic>
            : {'value': j['payload']},
      );

  DateTime get time => DateTime.fromMillisecondsSinceEpoch(at);
}

/// 一个 bot.
class ProcInfo {
  final ProcessID pid;
  final String app;
  final String name;
  final ProcState state;
  final int createdAt;

  /// caps 这个 bot 手上有哪几条能力.
  ///
  /// **资料页最要紧的一块**: 一个 bot 能干什么, 不看它的自我介绍,
  /// 看它手上有哪几条能力 —— 那是内核强制的, 不是它自己说的
  final List<Cap> caps;

  /// role 它的岗位(标签里的那一句)
  final String role;

  /// thread 在哪个房间. 空 = 一对一单聊
  final String thread;

  const ProcInfo({
    required this.pid,
    required this.app,
    required this.name,
    required this.state,
    required this.createdAt,
    this.caps = const [],
    this.role = '',
    this.thread = '',
  });

  /// work 它能写的那个目录 —— **从能力里读, 不另存一份**.
  ///
  /// 工作区就是那条 write 能力的 scope: 另存一份的话, 就会有
  /// "标签说 A、内核拦的是 B"的那天, 而那时候只有内核说的算
  String get work {
    for (final c in caps) {
      if (c.axis == 'write') return c.scope;
    }
    return '';
  }

  factory ProcInfo.fromJson(Map<String, dynamic> j) {
    final spec = (j['spec'] as Map<String, dynamic>?) ?? const {};
    final name = (spec['name'] as String?) ?? '';
    final labels = (spec['labels'] as Map<String, dynamic>?) ?? const {};
    return ProcInfo(
      pid: j['pid'] as String? ?? '',
      app: (spec['app'] as String?) ?? '',
      // 没名字的进程用 pid 顶上 —— 一个没有名字的头像
      // 比一个丑名字更让人不知道那是谁
      name: name.isEmpty ? (j['pid'] as String? ?? '?') : name,
      state: parseState(j['state'] as String?),
      createdAt: (j['createdAt'] as num?)?.toInt() ?? 0,
      caps: ((spec['caps'] as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(Cap.fromJson)
          .toList(),
      role: (labels['role'] as String?) ?? '',
      thread: (labels['thread'] as String?) ?? '',
    );
  }

  /// 只更新变化的字段, 其余字段保持原值.
  ///
  /// ── 为什么必须有这个 ──
  ///
  /// 收到 proc.state 时要更新状态, 而原来的写法是**逐字段重建**一个
  /// ProcInfo. 那种写法在加字段的那一天必然出事: 我给 ProcInfo 加了
  /// caps 和 role, 而重建那处没跟着加 —— 于是进程状态一变,
  /// 能力和岗位就**静默地变成空的**.
  ///
  /// 典型表现是: 资料页写着"一条能力都没有 —— 它只能说话",
  /// 而 OS 明明给了三条. 而且它只在状态变过一次之后才错,
  /// 刚拉到进程表的那一瞬是对的.
  ///
  /// copyWith 让"忘了抄某个字段"这件事**在语法上做不到**.
  ProcInfo copyWith({ProcState? state}) => ProcInfo(
        pid: pid,
        app: app,
        name: name,
        state: state ?? this.state,
        createdAt: createdAt,
        caps: caps,
        role: role,
        thread: thread,
      );
}

/// proc.output 的 payload —— bot 说的话.
///
/// `stream` 把同一条流的连续块折成一段: 没有它, 一次 8000 块的模型输出
/// 就是 8000 条消息.
class OutputChunk {
  final String stream;
  final String text;
  /// channel say = 说给人听的; think = 它的思考; tool = 工具流水.
  /// **群聊只画 say**, 另外两种归核心页
  final String channel;
  final bool done;

  const OutputChunk(this.stream, this.text, this.channel, this.done);

  factory OutputChunk.of(Map<String, dynamic> p) => OutputChunk(
        p['stream'] as String? ?? '',
        p['text'] as String? ?? '',
        p['channel'] as String? ?? 'say',
        p['done'] as bool? ?? false,
      );
}

/// proc.output 里 `phase: "turn_failed"` 那一条 —— 这一轮没走完.
///
/// ── 为什么非画不可 ──
///
/// 手机上原来一个字都不画: 模型出错、这一轮中止, OS 发了一条带着原因的
/// turn_failed, 而手机把它当成"不是 say"丢了. 他看到的是**它不理我** ——
/// 2026-09-11 车上连着几轮都是这样, 他以为是网不好, 一遍遍重说.
///
/// 网页控制台那边画成一张"这一轮我停下来了"的卡(rows.ts), 手机上照着来,
/// 只是收成一行.
class TurnFailed {
  TurnFailed._();

  /// 一行能读的原因, 最多 [max] 个字.
  ///
  ///	err 常常是供应商原样吐回来的一坨 JSON:
  ///	`400 {"type":"error","error":{"type":"invalid_request_error","message":"…"}}`
  ///	—— 屏幕上摆一坨 JSON 他什么也读不出来. 里面有 "message" 的话只取它;
  ///	没有就取第一行. **不编**: 什么都没有就照实说"没说原因"
  static String brief(Object? err, {int max = 160}) {
    var s = '${err ?? ''}'.trim();
    final m = RegExp(r'"message"\s*:\s*"((?:[^"\\]|\\.)*)"').firstMatch(s);
    if (m != null) {
      try {
        s = jsonDecode('"${m.group(1)}"') as String;
      } catch (_) {
        s = m.group(1)!;
      }
    }
    s = s.split('\n').map((l) => l.trim()).firstWhere(
          (l) => l.isNotEmpty,
          orElse: () => '',
        );
    if (s.isEmpty) return '没说原因';
    if (s.length > max) s = '${s.substring(0, max - 1)}…';
    return s;
  }

  /// 从 payload 里取原因 —— 跟控制台同一个先后: err > detail > why > msg
  static String of(Map<String, dynamic> p) {
    for (final k in const ['err', 'detail', 'why', 'msg']) {
      final v = p[k];
      if (v != null && '$v'.trim().isNotEmpty) return brief(v);
    }
    return brief(null);
  }
}

/// input.recv 的 payload —— 谁说了一句话.
class InputSaid {
  final String text;
  final String from;

  /// relay = 同屋 bot 交办的, 不是用户输入.
  /// **画错了会变成"我说的"**
  final bool relay;

  /// utterance 同一句话投给屋里 N 个人时共用的身份.
  ///
  /// **群聊必须靠它去重**: 一句话发给 3 个 bot, 账本里就是 3 条
  /// input.recv(每个进程各一条) —— 照着画的话用户会看见自己
  /// 说了三遍. 而 OS 早就把这个 id 放进 payload 了
  /// (go/osinit/inbox.go: recv["utterance"] = in.ID).
  ///
  /// 空的时候退回按 内容+说话人 去重: 老账本里没有这个字段
  final String utterance;

  /// quiet = **机器自己叫醒的**，没有人在等回话。
  ///
  /// 定时任务到点时，OS 把题目当成一条「收到的话」投给 bot ——
  /// 于是它在界面上长得跟用户打的字一模一样，容易被误认为是用户消息。
  ///
  /// **这一行和它的回话都不画。** 它真有要紧的事要说，走的是投递
  /// 通道（主动消息），那条照样到人跟前。
  final bool quiet;

  const InputSaid(this.text, this.from, this.relay, this.utterance, this.quiet);

  factory InputSaid.of(Map<String, dynamic> p) => InputSaid(
        p['text'] as String? ?? '',
        p['from'] as String? ?? '你',
        p['relay'] as bool? ?? false,
        p['utterance'] as String? ?? '',
        p['quiet'] as bool? ?? false,
      );

  /// 去重的钥匙
  String get key => utterance.isNotEmpty ? utterance : '$from\u0000$text';
}

/// 头像上那个点到底在说什么 —— **移植自
/// packages/apps/console/src/view/presence.ts**.
///
/// 五档, 每一档都对应一个你会做的不同决定:
///
///	在线   进程活着, 停在 Recv 上   → 现在就能使唤它
///	忙碌   正在跑一轮              → 说话它也听得见(插话), 但别指望马上回
///	等你   有一条决策未解决        → 它卡在那儿, 只有你能让它继续
///	出错   进程失败了              → 得看一眼发生了什么
///	不在   进程退出了              → 说话要先把它拉起来
enum Presence { online, busy, needsYou, failed, offline }

/// presenceOf 把内核的进程状态翻成"人看得懂的在不在".
///
/// **内核的 waiting 不是"等你"**: 它的意思是"停在 Recv 上等下一句话",
/// 也就是 bot 闲着的常态. 客户端早先把它画成橙色警告, 于是一屋子
/// 闲着的 bot 全是警告色 —— 那个点永远亮着, 也就不再有任何信息量.
///
/// pending = 有一条 decide.requested 还没等到 resolved.
PresenceInfo presenceOf(ProcState state, {bool pending = false}) =>
    switch (state) {
      ProcState.failed => const PresenceInfo(Presence.failed, '出错了'),
      ProcState.exited => const PresenceInfo(Presence.offline, '不在'),
      ProcState.running => const PresenceInfo(Presence.busy, '在干活'),
      ProcState.waiting || ProcState.created => pending
          ? const PresenceInfo(Presence.needsYou, '等你拍板')
          : const PresenceInfo(Presence.online, '在线'),
      // 认不得的状态**不当成在线** —— 说它在线而其实不在, 用户会对着
      // 一个死进程说话; 反过来最多是多点一下
      _ => const PresenceInfo(Presence.offline, '不在'),
    };

class PresenceInfo {
  const PresenceInfo(this.kind, this.label);
  final Presence kind;
  final String label;
}


/// 一条能力 —— 内核强制的那种, 不是它自己声称的.
///
/// axis: read / write / net / proc / secret
class Cap {
  const Cap(this.axis, this.scope);
  final String axis, scope;

  factory Cap.fromJson(Map<String, dynamic> j) =>
      Cap(j['axis'] as String? ?? '', j['scope'] as String? ?? '');

  /// 给人看的那句话. **说"能干什么", 不说轴的名字** ——
  /// "read /" 对用户是零信息, "能读这台机器上的文件"才是
  String get label => switch (axis) {
        'read' => scope == '/' ? '能读这台机器上的文件' : '能读 ${_short(scope)}',
        'write' => '能写 ${_short(scope)}',
        'net' => scope == '*' ? '能出网（不限）' : '能连 $scope',
        'proc' => '能起别的进程',
        'secret' => '能用凭据 $scope',
        _ => '$axis $scope',
      };

  /// 长路径只留尾巴那两段.
  ///
  /// **一条工作区路径可能撑满六行**, 把整块能力都淹没 —— 而那六行里
  /// 有五行是临时目录的前缀, 对用户零信息.
  ///
  /// 留尾巴不留头: 一条路径里**最后那两段才说明它在哪儿干活**
  /// (…/work/research), 前面那一长串只说明这台机器怎么摆的.
  /// 从中间截会两头都不成句
  static String _short(String p) {
    if (p.length <= 28) return p;
    final segs = p.split('/').where((e) => e.isNotEmpty).toList();
    if (segs.length <= 2) return p;
    return '…/${segs[segs.length - 2]}/${segs.last}';
  }

  /// 越界的那几条要显眼: 出网和写盘是能造成外部后果的两条
  bool get heavy => axis == 'net' || axis == 'write' || axis == 'secret';
}


/// 一条送到你跟前的东西 —— 闹钟 / 主动判断 / 日报, 同一个形状
class Delivery {
  const Delivery({
    required this.id,
    required this.from,
    required this.kind,
    required this.text,
    required this.why,
    required this.urgent,
    required this.at,
    this.to = '',
  });

  final String id;

  /// from 哪个 bot 说的. **可以是空** —— 感知层判出来的那些
  /// 不属于任何一个 bot, 界面上那一列要留位不留脸
  final String from;
  final DeliveryKind kind;
  final String text;

  /// why 凭哪条判据. **必须显示** —— 一次打扰值不值得,
  /// 只有看见它凭什么说才判得出来
  final String why;

  /// to 给谁的. 空 = 屋里所有人.
  ///
  /// **一台家用的 OS 上不止一个人**: 她的「到家提醒」在他手机上响一次,
  /// 他就会把整个通道关掉 —— 而那一关, 真正要紧的那次也到不了他
  final String to;

  /// urgent 要不要现在就吵醒你. **这个判断在 OS 侧做的**:
  /// 它手上有打扰预算和判据, 手机端只知道"来了一条"
  final bool urgent;
  final DateTime at;

  factory Delivery.of(OsEvent e) => Delivery(
        id: e.payload['id'] as String? ?? '',
        from: e.payload['from'] as String? ?? '',
        kind: switch (e.payload['kind']) {
          'remind' => DeliveryKind.remind,
          'daily' => DeliveryKind.daily,
          'needs_you' => DeliveryKind.needsYou,
          _ => DeliveryKind.proactive,
        },
        text: (e.payload['text'] as String? ?? '').trim(),
        why: e.payload['why'] as String? ?? '',
        urgent: e.payload['urgent'] as bool? ?? false,
        to: e.payload['to'] as String? ?? '',
        at: e.time,
      );
}

enum DeliveryKind {
  /// 你自己设的闹钟 —— 不占打扰额度
  remind,

  /// 它判断出该告诉你的 —— 已经过了打扰预算那道闸
  proactive,

  /// 每天一次
  daily,

  /// 它卡住了, 只有你能让它继续
  needsYou,
}

/// 一张卡片 —— bot 的多模态出口.
///
/// ── 为什么是 JSON 不是图片 ──
///
/// 一张位置卡上有标题、地址、一张地图缩略图，还要能点开。传一张图片的话
/// 这些全糊在像素里：点不了、选不了、暗色模式下还得配两套。
///
/// 所以 OS 那边发的是结构（go/agent/show.go 的 ui 通道），**渲染端照着画** ——
/// 换个客户端只要各自画一遍，协议一个字不用改。
///
/// ── 认不出的种类也要画 ──
///
/// 这张表是开的：OS 那边允许 bot 发表外的种类，界面照字段排一张通用卡。
/// 丢掉的话，用户什么都看不见，而 bot 收到的是「已经展示了」，
/// 于是它接着说「如上图」——没有任何一处报错。
class Card {
  const Card({required this.type, required this.fields});

  /// type 哪一种: map / image / link / table / progress …
  final String type;

  /// fields 剩下的字段，原样收着 —— **界面认不出的种类靠它排通用卡**
  final Map<String, dynamic> fields;

  String str(String k) => '${fields[k] ?? ''}';
  double num_(String k) {
    final v = fields[k];
    if (v is num) return v.toDouble();
    return double.tryParse('$v') ?? 0;
  }

  /// parse 解不开就返回 null —— **不抛**: 一条画不出来的卡片
  /// 不该把整条事件流打断（跟 OS 侧 writeEvent 同一条规矩）
  static Card? parse(String raw) {
    try {
      final v = jsonDecode(raw);
      if (v is! Map<String, dynamic>) return null;
      final t = '${v['type'] ?? ''}'.trim();
      if (t.isEmpty) return null;
      return Card(type: t, fields: v);
    } catch (_) {
      return null;
    }
  }
}
