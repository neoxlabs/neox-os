import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';
import 'package:http/http.dart' as http;

import 'events.dart';

/// 连 OS 的那条线.
///
/// ── 手机跟网页控制台是同一种客户端 ──
///
/// 没有"移动端专用接口". 用的就是 /processes /stream /say ——
/// 多开一套接口的话, OS 那边就要知道"谁是手机", 而那正是
/// "UI 只是订阅者之一, 没有特权客户端"这条要防的.
///
/// ── 唯一有判断的地方是断线 ──
///
/// 手机的网是断续的(电梯、地铁、锁屏、被系统切后台). 别的都是管道.
class OsClient {
  OsClient({required this.base, required this.token});

  /// base 形如 http://192.168.1.10:7717 —— 末尾不带斜杠
  final String base;
  final String token;

  final _http = http.Client();

  Map<String, String> get _headers => {
        'Authorization': 'Bearer $token',
        'Content-Type': 'application/json',
      };

  Uri _u(String path, [Map<String, String>? q]) =>
      Uri.parse('$base$path').replace(queryParameters: q);

  /// 这台 OS 上有哪些 bot.
  Future<List<ProcInfo>> processes() async {
    final r = await _http
        .get(_u('/processes'), headers: _headers)
        .timeout(const Duration(seconds: 15));
    if (r.statusCode == 401) throw OsAuthError();
    if (r.statusCode != 200) throw OsError('processes ${r.statusCode}');
    final list = jsonDecode(utf8.decode(r.bodyBytes)) as List<dynamic>;
    return list
        .map((e) => ProcInfo.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  /// 说一句话.
  ///
  /// ── pid 和 pids 不是一回事, 传错了会静默地不送达 ──
  ///
  /// OS 侧 handleSay 的形状是:
  ///
  ///	pids 多于一个且 pid 空  →  由 OS 投给名单里每一个
  ///	否则                    →  投给 **body.pid** 那一个
  ///
  /// 也就是说**单发必须给 pid**. 我一开始只传了 pids(单元素),
  /// 于是 OS 拿着一个空 pid 去投递 —— 表现是:
  /// HTTP 200、输入框清空、界面一切正常, 而那句话谁也没收到.
  ///
  /// `pids` 在多发时照样要给: "这话还发给了谁"是协议的一部分,
  /// 由 OS 拼给每个 bot(go/osinit/observe.go 的 sayBody 注释).
  ///
  /// ── 200 不等于送到了 ──
  ///
  /// 它回的是 `{"ok": bool}`: 进程不在、已经退出、收件箱满了都是 false.
  /// **只看状态码的话, "它已经退出了"会显示成发送成功** ——
  /// 而用户要过很久才发现没人理他.
  Future<void> say({
    required String text,
    required List<ProcessID> pids,
    String from = '你',
    List<Attach> images = const [],
    List<Attach> files = const [],
    // voice 他在用耳朵听这一轮的回答 —— OS 那边据此让它说短点。
    // 见 go/abi/wire.go 的 RecvResult.Voice
    bool voice = false,
  }) async {
    if (pids.isEmpty) throw OsError('没有人可以说');
    final body = <String, dynamic>{
      'text': text,
      'said': text,
      if (voice) 'voice': true,
      'from': from,
    };
    // 图和文件都是 {name, data(base64, **不带 data: 前缀**)}.
    // 前缀是浏览器那边的事(go/osinit/observe.go 的 sayImage)
    if (images.isNotEmpty) body['images'] = images.map((a) => a.json).toList();
    if (files.isNotEmpty) body['files'] = files.map((a) => a.json).toList();
    if (pids.length == 1) {
      body['pid'] = pids.first;
    } else {
      body['pids'] = pids;
    }
    final r = await _http
        .post(_u('/say'), headers: _headers, body: jsonEncode(body))
        .timeout(const Duration(seconds: 30));
    if (r.statusCode == 401) throw OsAuthError();
    if (r.statusCode >= 300) {
      throw OsError('say ${r.statusCode}: ${utf8.decode(r.bodyBytes)}');
    }
    final res = jsonDecode(utf8.decode(r.bodyBytes));
    if (res is Map && res['ok'] == false) {
      throw OsNotDelivered();
    }
  }

  /// 新建一个 bot.
  ///
  /// OS 侧只认四样: 名字(必填, ≤24 字)、在哪段对话、在哪儿干活、岗位.
  /// **能力不在这儿给** —— 新进程按默认能力起, 要更多得走 request_access
  /// 授权必须由 OS 决定. 如果客户端能直接指定能力, 授权流程就失去意义.
  Future<ProcessID> create({
    required String name,
    String work = '',
    String role = '',
  }) async {
    final r = await _http
        .post(_u('/create'),
            headers: _headers,
            body: jsonEncode({'name': name, 'work': work, 'role': role}))
        .timeout(const Duration(seconds: 60));
    if (r.statusCode == 401) throw OsAuthError();
    final body = jsonDecode(utf8.decode(r.bodyBytes));
    if (r.statusCode >= 300) {
      // **起不来要把原因带出来**: 只说"失败了"的话, 用户会对着
      // 同一颗按钮点第二次, 而那只会多起一个失败的进程
      throw OsError(body is Map && body['error'] != null
          ? '${body['error']}'
          : '新建失败 ${r.statusCode}');
    }
    return (body as Map)['pid'] as String? ?? '';
  }

  // ══════════════════════════════════════════════════════════
  // 只读的那几个
  // ══════════════════════════════════════════════════════════

  Future<T> _get<T>(String path, T Function(dynamic) parse) async {
    final r = await _http
        .get(_u(path), headers: _headers)
        .timeout(const Duration(seconds: 20));
    if (r.statusCode == 401) throw OsAuthError();
    final body = jsonDecode(utf8.decode(r.bodyBytes));
    if (r.statusCode >= 300) {
      // **501 是"这台 OS 不开放这件事", 不是故障** —— 界面据此
      // 把那一格显示成"这台机器不支持", 而不是红色的报错
      throw OsUnsupported(body is Map && body['error'] != null
          ? '${body['error']}'
          : '$path ${r.statusCode}');
    }
    return parse(body);
  }

  Future<Health> health() => _get('/health', (j) => Health.fromJson(j));

  Future<List<Spend>> spend() => _get('/spend', (j) {
        final list = (j as Map)['bots'] as List<dynamic>? ?? const [];
        return list
            .map((e) => Spend.fromJson(e as Map<String, dynamic>))
            .toList();
      });

  Future<Provider> provider() =>
      _get('/provider', (j) => Provider.fromJson(j));

  /// 改推理配置. **整份回传** —— 见 [Provider.toJson]。
  ///
  /// [searchKey] 只在真要改的时候给：界面永远拿不到它（只进不出），
  /// 而空不该被理解成"删掉"
  Future<void> setProvider(Provider p, {String searchKey = ''}) => _post(
      '/provider',
      {...p.toJson(), if (searchKey.isNotEmpty) 'searchKey': searchKey});

  Future<List<ToolInfo>> toolchain() => _get('/toolchain', (j) {
        final list = (j as Map)['tools'] as List<dynamic>? ?? const [];
        return list
            .map((e) => ToolInfo.fromJson(e as Map<String, dynamic>))
            .toList();
      });

  /// 审批口径: never / ask / always 之类. 具体取值由 OS 定
  Future<String> policy() =>
      _get('/policy', (j) => (j as Map)['mode'] as String? ?? '');

  // ══════════════════════════════════════════════════════════
  // 会改东西的那几个
  // ══════════════════════════════════════════════════════════

  Future<void> _post(String path, Map<String, dynamic> body) async {
    final r = await _http
        .post(_u(path), headers: _headers, body: jsonEncode(body))
        .timeout(const Duration(seconds: 30));
    if (r.statusCode == 401) throw OsAuthError();
    final j = jsonDecode(utf8.decode(r.bodyBytes));
    if (r.statusCode >= 300) {
      throw (r.statusCode == 501 ? OsUnsupported.new : OsError.new)(
          j is Map && j['error'] != null ? '${j['error']}' : '$path ${r.statusCode}');
    }
    // 有些接口 200 但 ok:false —— **只看状态码会把失败显示成成功**
    if (j is Map && j['ok'] == false) throw OsError('没成功');
  }

  Future<void> setPolicy(String mode) => _post('/policy', {'mode': mode});

  /// 停一个 bot. **按名字不按 pid** —— OS 侧就是这么定的
  Future<void> stopBot(String name) => _post('/stop', {'name': name});

  /// 这台机器上有名字的项目 —— {路径: 显示名}.
  ///
  /// **只有起过名的才在里面**. 派工作区时它是候选之一, 不是全集:
  /// 另一半候选是屋里那几个 bot 已经在用的目录(手机侧就有, 见
  /// ProcInfo.work) —— 那些是真实存在、而且已经验证过能派的
  Future<Map<String, String>> projects() =>
      _get('/projects', (j) => ((j['names'] as Map<String, dynamic>?) ?? {})
          .map((k, v) => MapEntry(k, '$v')));

  /// 改岗位 —— 进系统段的那句话.
  ///
  /// **进程会换一个**: 系统段是启动参数, 不是能中途改的状态.
  /// 对话不断(历史跟着 bot 标签走), 但它手上正干的那一轮会断
  Future<void> setRole(String name, String role) =>
      _post('/role', {'name': name, 'role': role});

  /// 换工作区 —— 同上, 也是换一个进程. 能力在进程启动时定死,
  /// landlock 只能收紧不能放宽
  Future<void> rebind(String name, String work) =>
      _post('/rebind', {'name': name, 'work': work});

  /// 换房间. 空 thread = 拉出来单聊
  Future<void> move(String name, String thread) =>
      _post('/move', {'name': name, 'thread': thread});

  /// 删掉几个 bot.
  ///
  /// **这是不可逆的**: 进程没了、账本里它那一段还在但界面上够不着了.
  /// 界面必须问一次再调
  Future<void> forget(List<ProcessID> pids) => _post('/forget', {'pids': pids});

  /// 报到 —— **我是谁, 我能感知什么, 我能怎么告诉你**.
  ///
  /// ── 为什么这一步不能省 ──
  ///
  /// 不报到的话, OS 眼里这台手机只是一个匿名的 source 字符串:
  /// 它不知道这台设备有没有屏、能不能出声、报不报位置。于是"怎么
  /// 告诉用户"只能写死成推安卓通知 —— 而下一个接进来的可能是
  /// 一副耳机或者一块 ESP32。
  ///
  /// 声明的是**做得到什么**, 不是想要什么: 该用哪种由 OS 判
  /// (它手上有紧急程度和打扰预算)。
  Future<void> declareDevice({
    required String id,
    required String name,
    required List<String> senses,
    required List<String> presents,
    String kind = 'phone',
    String owner = '',
    int paceSec = 0,
  }) =>
      _post('/device', {
        'id': id,
        'name': name,
        'kind': kind,
        'senses': senses,
        'presents': presents,
        if (owner.isNotEmpty) 'owner': owner,
        if (paceSec > 0) 'paceSec': paceSec,
      });

  /// 告诉这台 OS "我是谁".
  ///
  /// **不是鉴权**: 进门靠的是 OS 的 token, 这一步只解决归属 ——
  /// 一条位置信号是谁的、一条通知该推给谁
  Future<void> knowPerson(String id, String name) =>
      _post('/person', {'id': id, 'name': name});

  /// 它现在知道什么 —— **已经过期筛选之后**的那一份.
  ///
  /// who 是谁在问: 不给就只看得到屋子的那些(天气、家里有没有人)——
  /// **不是"看全部"**: 把两个人的位置一起端出来, 问的人分不清哪条是自己的
  Future<String> world(String who) => _get(
      '/world?who=${Uri.encodeComponent(who)}',
      (j) => j['text'] as String? ?? '');

  /// 它认得哪些地方 —— 用户教的那些
  Future<List<Place>> places() => _get('/places', (j) =>
      ((j['places'] as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(Place.fromJson)
          .toList());

  /// 给一个坐标起名 —— **规律层推出来的那些, 要能当场确认**.
  ///
  /// 没有它的话, 用户看到「这儿像你上班的地方」却没法处置: 只能回聊天里
  /// 说一句，而那时候他多半不在那儿，也未必说得出门牌号。
  /// **一个看得见却按不下去的推断，比不推断更让人烦。**
  /// 给一个坐标起名。[radius] 方圆多少米算在这儿，0 = 按缺省 250。
  ///
  /// **圈要能调**：150 米的「家」可能认不出客厅里的人——
  /// 记着的点和手机报的差 190 米，刚好出圈，而一次都不报错
  Future<void> namePlace(String name, double lat, double lon,
          {double radius = 0}) =>
      _post('/place',
          {'name': name, 'lat': lat, 'lon': lon, 'radius': radius});

  /// 忘掉一个地方. **教错了要能改** —— 一个记错的「家」会让所有
  /// 跟到家有关的判断都错，而它一个都不会报错
  Future<void> forgetPlace(String name) =>
      _post('/place/forget', {'name': name});

  /// 这台 OS 算在哪个时区 —— 见 go/osinit/zone.go.
  ///
  /// **它进的是判断不是显示**: 日报几点发、「晚上七点提醒我」算哪一段、
  /// 「明天早上八点」是哪一刻。Docker 里默认 UTC，全都差 8 小时，
  /// 而一处都不会报错。
  Future<Timezone> timezone() =>
      _get('/timezone', (j) => Timezone.fromJson(j));

  Future<void> setTimezone(String zone) => _post('/timezone', {'zone': zone});

  /// 回到「跟着手机走」. **定死了就要能松开** —— 没有它的话点过一次
  /// 就永远回不到自动，而界面上看不出新报上来的时区是被什么挡着的
  Future<void> followTimezone() => _post('/timezone', {'auto': true});

  /// 它在管你哪些事 —— **一次给全**。见 go/osinit/observe.go handleUpcoming.
  ///
  /// 待办、日程、提醒、盯着的事分在四个地方存（它们的语义和生命周期
  /// 都不一样），但用户问的是同一个问题。分四次请求的话，每个客户端
  /// 都要自己排一遍序，而排出来的顺序各不相同。
  Future<Upcoming> upcoming() =>
      _get('/upcoming', (j) => Upcoming.fromJson(j));

  /// 撤一条提醒. **能设就得能撤**
  Future<void> cancelReminder(String id) =>
      _post('/reminders/cancel', {'id': id});

  /// 不盯了
  Future<void> removeWatch(String id) => _post('/watches/remove', {'id': id});

  /// 他要做的事和日程 —— 见 go/osinit/agenda.go.
  ///
  /// **待办和日程是同一份存储**：「下周三三点开会」和「记得买牛奶」
  /// 差的只是一个时间。
  Future<List<Task>> agenda({bool withDone = false}) => _get(
      '/agenda${withDone ? '?done=1' : ''}',
      (j) => ((j['tasks'] as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(Task.fromJson)
          .toList());

  Future<void> addTask(String what, {DateTime? at, String where = ''}) =>
      _post('/agenda', {
        'what': what,
        'at': at?.millisecondsSinceEpoch ?? 0,
        'where': where,
      });

  /// 划掉一件. **不删** —— 划掉的他还要能看见
  Future<void> doneTask(String id) => _post('/agenda/done', {'id': id});

  /// 扔掉一件 —— 记错了、不做了
  Future<void> dropTask(String id) => _post('/agenda/drop', {'id': id});

  /// 它记着的事 —— 生日、口味、家里的规矩. 见 go/osinit/notes.go.
  ///
  /// **必须看得见, 而且能删**: 一条记错的偏好会一直影响它的回话,
  /// 而用户唯一的处置办法本来是再说一遍 —— 而那只会多一条
  Future<List<Note>> notes() => _get('/notes', (j) =>
      ((j['notes'] as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(Note.fromJson)
          .toList());

  Future<void> forgetNote(String key, {String who = ''}) =>
      _post('/note/forget', {'who': who, 'key': key});

  Future<void> setNote(String key, String text, {String who = ''}) =>
      _post('/note', {'who': who, 'key': key, 'text': text});

  /// 它看出了什么规律 —— 常去哪几个地方
  Future<List<Habit>> routine(String who) => _get(
      '/routine?who=${Uri.encodeComponent(who)}',
      (j) => ((j['habits'] as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(Habit.fromJson)
          .toList());

  /// 屋里都接了些什么 —— 设置页照这个画
  Future<List<Device>> devices() => _get('/devices', (j) =>
      ((j['devices'] as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(Device.fromJson)
          .toList());

  /// 投一条信号进感知层 —— 手机是采集端之一.
  ///
  /// **走 _post 而不是自己拼一次 http**: 上一版是后者, 于是它既不判
  /// 状态码也不看返回 —— 而这个口子的返回里说的正是"这条去了哪儿"
  /// (收下了/紧急穿透/重复/迟到)。判不出来的话, 手机会在自己时钟慢了
  /// 半小时的时候以为一切正常
  Future<void> signal(String kind, Map<String, dynamic> body,
          {String source = 'phone'}) =>
      _post('/signal', {
        'source': source,
        'kind': kind,
        'at': DateTime.now().millisecondsSinceEpoch,
        'body': body,
      });

  /// 订阅事件流.
  ///
  /// ── 游标是每进程一个, 不是一个全局游标 ──
  ///
  /// 全局游标在多进程下必然错位: 进程 A 的 seq 跟进程 B 的没有可比性.
  /// (go/osinit/observe.go parseCursors)
  ///
  /// ── 重连必须带游标 ──
  ///
  /// 不带的话每次断线都从头补一遍整段历史; 带了的话 OS 只补缺的那一截.
  /// 而**宁可重复也不能缺**: 重复可检测可丢弃, 缺失不是 —— 拿着一段
  /// 有洞的流继续画, 界面自己不会知道少了什么.
  /// [onOpen] 连上了 —— **在拿到 200 的那一刻**, 不是收到第一条事件时.
  /// 差别在一台闲着的 OS 上是致命的: 它可能几小时不产事件, 只发心跳
  /// 注释。把"收到第一条事件"当成连上的话, 界面会一直卡在"连接中"
  /// [onBeat] 收到了任何一个字节(心跳注释也算) —— 用来记"最后一次
  /// 听见它是什么时候", 切回前台时靠这个判断这条连接是不是已经凉了
  Stream<OsEvent> stream({
    Map<ProcessID, int> cursors = const {},
    void Function()? onOpen,
    void Function()? onBeat,
  }) async* {
    final q = {'token': token};
    if (cursors.isNotEmpty) {
      q['from'] = cursors.entries.map((e) => '${e.key}:${e.value}').join(',');
    }
    final req = http.Request('GET', _u('/stream', q));
    req.headers.addAll({'Accept': 'text/event-stream'});
    final resp = await _http.send(req).timeout(const Duration(seconds: 20));
    if (resp.statusCode == 401) throw OsAuthError();
    if (resp.statusCode != 200) throw OsError('stream ${resp.statusCode}');
    onOpen?.call();

    // ── 心跳当活信号, 不是当垃圾 ──
    //
    // OS 每 20 秒发一行 `:` 注释. 原来这里直接 continue 丢掉了 ——
    // 连"这条连接还活着"这个信息也一起丢了.
    //
    // **手机上的连接不是"要么好要么报错"**: 切基站、WiFi 转 4G、
    // 反代那头静悄悄地掐掉, socket 都会挂在那儿既不出错也不结束.
    // 那时候 onError / onDone 一个都不响, 退避重连根本没被触发过 ——
    // 界面还写着"在线", 而事件早就不来了. 用户唯一的出路是手点重连.
    //
    // 所以: 超过 [idle] 一个字节都没有(心跳都断了)就当断线抛出去,
    // 交给外面重连. 心跳 20 秒一次, 连丢三次才算数.
    const idle = Duration(seconds: 70);
    var eventName = '';
    await for (final line in resp.stream
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .timeout(idle,
            onTimeout: (sink) => sink.addError(OsError('心跳停了')))) {
      onBeat?.call();
      if (line.startsWith(':')) continue; // 心跳注释: 它的价值在上面那个 timeout
      if (line.isEmpty) {
        eventName = '';
        continue;
      }
      if (line.startsWith('event: ')) {
        eventName = line.substring(7).trim();
        continue;
      }
      if (!line.startsWith('data: ')) continue;
      final raw = line.substring(6);

      // OS 显式说"这里断过一段" —— 它比我们更早知道.
      // 当场结束这条流, 让外面拿着游标重连补齐
      if (eventName == 'gap') throw OsGap();

      try {
        final j = jsonDecode(raw);
        if (j is Map<String, dynamic> && j.containsKey('kind')) {
          yield OsEvent.fromJson(j);
        }
      } catch (_) {
        // 一条画不出来不该拖垮整条流 —— 内核侧 writeEvent 也是这么做的
      }
    }
  }

  void close() => _http.close();
}

/// 跟这句话一起发出去的东西.
///
/// **图和文件走两条路**: 图进 /blob 读出口(界面能取回来画),
/// 文件只落盘给 bot 读、账本里只记名字 —— 读出口只放媒体
class Attach {
  const Attach(this.name, this.bytes);
  final String name;
  final Uint8List bytes;

  Map<String, dynamic> get json =>
      {'name': name, 'data': base64Encode(bytes)};

  /// 大小上限: OS 侧一次请求 2MB 封顶, base64 还要涨三分之一.
  /// **在这边先拦下来**, 让用户当场知道 —— 传上去再被 413 拒的话,
  /// 他只会看到"发送失败"
  static const maxBytes = 1400 * 1024;
}

/// 这台 OS 不开放这件事(HTTP 501).
///
/// **跟出错分开**: 出错该重试、该报红; 这个该显示成"这台机器不支持",
/// 而且那一格干脆别给按钮 —— 给了也是个按下去必然失败的按钮
class OsUnsupported extends OsError {
  OsUnsupported(super.message);
}

class OsError implements Exception {
  OsError(this.message);
  final String message;
  @override
  String toString() => message;
}

/// token 不对. **跟别的错分开** —— 别的错该重试, 这个错重试一万次也没用,
/// 该做的是把用户送去设置页
class OsAuthError extends OsError {
  OsAuthError() : super('token 不对');
}

/// OS 说流断过一段. 不是错误, 是一条指令: 拿着游标重连
class OsGap extends OsError {
  OsGap() : super('流断过一段, 要重连补齐');
}

/// 话没送到 —— HTTP 是 200, 但 OS 说 ok:false.
///
/// 进程不在、已经退出、收件箱满了都会走到这里. **必须跟网络错误分开**:
/// 网络错误重试有用, 这个重试一万次也没用 —— 该做的是告诉用户
/// "它不在", 而不是转圈
class OsNotDelivered extends OsError {
  OsNotDelivered() : super('没送到 —— 它可能已经不在了');
}

/// 这台 OS 现在什么状况 —— /health
class Health {
  const Health({
    required this.ok,
    required this.inference,
    required this.model,
    required this.mode,
    required this.enforced,
  });

  final bool ok;

  /// inference 有没有接上推理服务. **没接的话 bot 只会说"这台机器
  /// 没配推理服务"** —— 而那是用户第一个要知道的事
  final bool inference;
  final String model;

  /// mode dev / prod —— 决定边界是不是真的在内核里强制
  final String mode;

  /// enforced 约束层是不是真在生效.
  /// macOS 上会退化成只拦写, 这个字段是唯一如实说的地方
  final bool enforced;

  factory Health.fromJson(Map<String, dynamic> j) => Health(
        ok: j['ok'] as bool? ?? false,
        inference: j['inference'] as bool? ?? false,
        model: j['model'] as String? ?? '',
        mode: j['mode'] as String? ?? '',
        enforced: j['enforced'] as bool? ?? false,
      );
}

/// 一个 bot 花了多少 —— /spend
class Spend {
  const Spend({
    required this.bot,
    required this.prompt,
    required this.completion,
    required this.cached,
    required this.calls,
    required this.turns,
  });

  final String bot;
  final int prompt, completion, cached, calls, turns;

  /// 送进去 + 吐出来. **缓存命中的那部分算在 prompt 里** ——
  /// 它是"这次真的送了多少", 而不是"付了多少钱"
  int get total => prompt + completion;

  factory Spend.fromJson(Map<String, dynamic> j) => Spend(
        bot: j['bot'] as String? ?? '',
        prompt: (j['prompt'] as num?)?.toInt() ?? 0,
        completion: (j['completion'] as num?)?.toInt() ?? 0,
        cached: (j['cached'] as num?)?.toInt() ?? 0,
        calls: (j['calls'] as num?)?.toInt() ?? 0,
        turns: (j['turns'] as num?)?.toInt() ?? 0,
      );
}

/// 推理服务配置 —— /provider
class Provider {
  const Provider({
    required this.baseUrl,
    required this.model,
    required this.hasKey,
    required this.keyFrom,
    this.protocol = '',
    this.searchNative = false,
    this.think = false,
    this.vision = false,
    this.searchApi = '',
    this.searchUrl = '',
    this.searchOn = false,
    this.capTokens = 0,
    this.autoResume = false,
    this.autoHire = false,
  });

  final String baseUrl, model;
  final bool hasKey;

  /// keyFrom key 从哪来(env / file). **凭据只在宿主手里**,
  /// 界面永远拿不到 key 本身, 只能知道有没有、从哪来
  final String keyFrom;

  /// protocol 走哪套 wire format。**空 = 让 OS 自己认**
  /// （api.deepseek.com → Anthropic Messages）。
  ///
  /// 这不是口味问题：**DeepSeek 官方的联网搜索只挂在 Messages 那条口上**。
  /// 手机上不给改 —— 改错了整台机器不能推理，那种事该在大屏上做。
  /// 但**必须带在快照里**：`/provider` 的写是整体覆盖，不带就等于清掉它，
  /// 而这里最常按的是「先想再答」那个开关。
  final String protocol;

  /// searchNative 供应商自己就会搜网（只读）。
  /// 会的话别再催人去配第三方 —— 催了等于让人白花一笔钱
  final bool searchNative;

  /// think 让它先想再答。**缺省关 —— 要的是快**。
  ///
  /// 量过：同一句「1+1=?」，关掉 completion 是 1 个 token，开着 34–40。
  /// 那三十几个 token 是要等的时间，而助理绝大多数时候在做的是
  /// 「提醒我 5:30 打卡」这种事，想不想都是同一个答案。
  final bool think;
  final bool vision;

  /// 搜网走哪儿 —— **供应商自带的那个靠不住**。
  ///
  /// 供应商传 `web_search` 会直接返回 400。所以搜索是一个外挂服务：
  /// 认识的几家填名字加 key，别的填 URL。都空 = 不能搜网，
  /// 而那时候 `web_search` 这个工具根本不挂出来。
  final String searchApi, searchUrl;

  /// searchOn 配好了没有。key 只进不出，界面靠它说"已经存着一把"
  final bool searchOn;
  final int capTokens;
  final bool autoResume;
  final bool autoHire;

  factory Provider.fromJson(Map<String, dynamic> j) => Provider(
        baseUrl: j['baseUrl'] as String? ?? '',
        model: j['model'] as String? ?? '',
        hasKey: j['hasKey'] as bool? ?? false,
        keyFrom: j['keyFrom'] as String? ?? '',
        protocol: j['protocol'] as String? ?? '',
        searchNative: j['searchNative'] as bool? ?? false,
        think: j['think'] as bool? ?? false,
        searchApi: j['searchApi'] as String? ?? '',
        searchUrl: j['searchUrl'] as String? ?? '',
        searchOn: j['searchOn'] as bool? ?? false,
        vision: j['vision'] as bool? ?? false,
        capTokens: (j['capTokens'] as num?)?.toInt() ?? 0,
        autoResume: j['autoResume'] as bool? ?? false,
        autoHire: j['autoHire'] as bool? ?? false,
      );

  /// 回写用的整份快照.
  ///
  /// **必须带全**：`/provider` 的写是整体覆盖，只回传两个字段的话，
  /// 否则一保存就会把 capTokens/autoResume/vision 全清零。
  /// apiKey 不带 —— 凭据只进不出，宿主那边空着就是保留原来那把。
  Map<String, dynamic> toJson() => {
        'baseUrl': baseUrl,
        'model': model,
        'protocol': protocol,
        'think': think,
        'vision': vision,
        'searchApi': searchApi,
        'searchUrl': searchUrl,
        'capTokens': capTokens,
        'autoResume': autoResume,
        'autoHire': autoHire,
      };

  Provider copyWith({
    bool? think,
    String? searchApi,
    String? searchUrl,
  }) =>
      Provider(
        baseUrl: baseUrl, model: model, hasKey: hasKey, keyFrom: keyFrom,
        protocol: protocol, searchNative: searchNative,
        think: think ?? this.think, vision: vision, capTokens: capTokens,
        autoResume: autoResume, autoHire: autoHire,
        searchApi: searchApi ?? this.searchApi,
        searchUrl: searchUrl ?? this.searchUrl,
        searchOn: searchOn,
      );
}

/// 一件工具在不在 —— /toolchain
class ToolInfo {
  const ToolInfo({
    required this.name,
    required this.have,
    required this.core,
    required this.what,
    required this.fix,
  });

  final String name;
  final bool have;

  /// core 缺了会**明显影响**干活的那几件
  final bool core;

  /// what 拿它干什么 —— 用户得知道值不值得为它装一趟
  final String what;

  /// fix 一句能直接粘进终端的话
  final String fix;

  factory ToolInfo.fromJson(Map<String, dynamic> j) => ToolInfo(
        name: j['name'] as String? ?? '',
        have: j['have'] as bool? ?? false,
        core: j['core'] as bool? ?? false,
        what: j['what'] as String? ?? '',
        fix: j['fix'] as String? ?? '',
      );
}

/// 屋里一台设备 —— 见 go/osinit/devices.go.
///
/// **一台设备可以只有一半**: 只感知(体重秤)、只呈现(音箱)都是合法的。
/// 契约要能表达这件事, 否则那两种东西永远接不进来
class Device {
  const Device({
    required this.id,
    required this.name,
    required this.kind,
    required this.senses,
    required this.presents,
    required this.at,
  });

  final String id, name, kind;

  /// senses 它报哪几类信号(按 kind 的前缀)
  final List<String> senses;

  /// presents 它能怎么把话送到人跟前:
  /// notify 摆出来 / alert 现在就吵醒他 / speak 念出来 / ask 问一句并等答案
  final List<String> presents;

  /// at 最后一次报到. 判断它死没死的唯一依据
  final int at;

  factory Device.fromJson(Map<String, dynamic> j) => Device(
        id: j['id'] as String? ?? '',
        name: j['name'] as String? ?? '',
        kind: j['kind'] as String? ?? '',
        senses: ((j['senses'] as List<dynamic>?) ?? const [])
            .whereType<String>()
            .toList(),
        presents: ((j['presents'] as List<dynamic>?) ?? const [])
            .whereType<String>()
            .toList(),
        at: (j['at'] as num?)?.toInt() ?? 0,
      );
}

/// 一个它认得的地方 —— 用户教的
class Place {
  const Place({
    required this.name,
    required this.lat,
    required this.lon,
    this.radius = 250,
  });
  final String name;
  final double lat, lon;

  /// radius 方圆多少米算在这儿。
  ///
  /// **要显示出来**：圈小了他人在里面而系统说不认识，而且一次都不
  /// 报错——家记着的点和手机报的差 190 米时，位置可能刚好出圈
  final double radius;

  factory Place.fromJson(Map<String, dynamic> j) => Place(
        name: j['name'] as String? ?? '',
        lat: (j['lat'] as num?)?.toDouble() ?? 0,
        lon: (j['lon'] as num?)?.toDouble() ?? 0,
        radius: (j['radius'] as num?)?.toDouble() ?? 250,
      );
}

/// 时区 —— 见 go/osinit/zone.go
class Timezone {
  const Timezone({
    required this.zone,
    required this.fixed,
    required this.now,
    required this.pick,
  });

  /// zone 现在算哪个
  final String zone;

  /// fixed 是人明确设的(true), 还是手机报上来的(false).
  ///
  /// **要显示出来**: 「跟着手机」和「我定死了」是两种状态,
  /// 而它们的后续行为不一样 —— 定死之后手机换地方也不会再跟着变
  final bool fixed;

  /// now 那台机器上此刻的钟点 —— **唯一能看出设对没设对的东西**
  final String now;
  final List<String> pick;

  factory Timezone.fromJson(Map<String, dynamic> j) => Timezone(
        zone: j['zone'] as String? ?? 'UTC',
        fixed: j['fixed'] as bool? ?? false,
        now: j['now'] as String? ?? '',
        pick: ((j['pick'] as List<dynamic>?) ?? const [])
            .map((e) => '$e')
            .toList(),
      );
}

/// 它在管你哪些事 —— /upcoming 一次给全的那一份
class Upcoming {
  const Upcoming({
    required this.tasks,
    required this.reminders,
    required this.watches,
    required this.places,
    required this.notes,
  });

  final List<Task> tasks;
  final List<Reminder> reminders;
  final List<Watch> watches;
  final List<Place> places;
  final List<Note> notes;

  /// 一共管着几件事 —— 空态和"它什么都没管"要分得开
  int get count =>
      tasks.length + reminders.length + watches.length + notes.length;

  static List<T> _list<T>(dynamic v, T Function(Map<String, dynamic>) f) =>
      ((v as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(f)
          .toList();

  factory Upcoming.fromJson(Map<String, dynamic> j) => Upcoming(
        tasks: _list(j['tasks'], Task.fromJson),
        reminders: _list(j['reminders'], Reminder.fromJson),
        watches: _list(j['watches'], Watch.fromJson),
        places: _list(j['places'], Place.fromJson),
        notes: _list(j['notes'], Note.fromJson),
      );
}

/// 一条设着的提醒 —— 见 go/osinit/timers.go
class Reminder {
  const Reminder({
    required this.id,
    required this.at,
    required this.text,
    required this.every,
  });

  final String id, text;
  final DateTime at;

  /// every 隔多久再响一次。0 = 只响一次
  final int every;

  bool get repeats => every > 0;

  factory Reminder.fromJson(Map<String, dynamic> j) => Reminder(
        id: j['id'] as String? ?? '',
        text: j['text'] as String? ?? '',
        at: DateTime.fromMillisecondsSinceEpoch(
            (j['at'] as num?)?.toInt() ?? 0),
        every: (j['every'] as num?)?.toInt() ?? 0,
      );
}

/// 一条盯着的事 —— 见 go/osinit/watch.go
class Watch {
  const Watch({required this.id, required this.kind, required this.place,
      required this.raw});
  final String id, kind, place, raw;

  /// 给人看的一行 —— **原话优先**：他问「我让你盯什么了」时，
  /// 要照他自己说过的那句答，不是照内部的事件类型名
  String get label {
    if (raw.isNotEmpty) return raw;
    final k = switch (kind) {
      'place.arrived' => '到达',
      'place.left' => '离开',
      'door.opened' => '开门',
      'lock.opened' => '开锁',
      'call.incoming' => '来电',
      'presence.motion' => '有人活动',
      _ => kind,
    };
    return place.isEmpty ? k : '$k @$place';
  }

  factory Watch.fromJson(Map<String, dynamic> j) => Watch(
        id: j['id'] as String? ?? '',
        kind: j['kind'] as String? ?? '',
        place: j['place'] as String? ?? '',
        raw: j['raw'] as String? ?? '',
      );
}

/// 一件要做的事 / 一个日程 —— 见 go/osinit/agenda.go
class Task {
  const Task({
    required this.id,
    required this.what,
    required this.at,
    required this.where,
    required this.done,
    this.from = '',
  });

  final String id, what, where;

  /// from 哪儿来的。空 = 他自己跟 bot 说的；「手机日历」= 扫上来的。
  ///
  /// **扫上来的划不掉**：那是他日历里的事，划掉这边的不会改那边
  final String from;

  /// at 什么时候。null = 没定时间（纯待办）
  final DateTime? at;
  final bool done;

  factory Task.fromJson(Map<String, dynamic> j) => Task(
        id: j['id'] as String? ?? '',
        what: j['what'] as String? ?? '',
        where: j['where'] as String? ?? '',
        from: j['from'] as String? ?? '',
        at: (j['at'] as num?) == null || (j['at'] as num) == 0
            ? null
            : DateTime.fromMillisecondsSinceEpoch((j['at'] as num).toInt()),
        done: j['done'] as bool? ?? false,
      );

  /// overdue 该做而没做 —— **要标出来**：一件昨天该做的事混在列表里，
  /// 看起来跟还没到点的一模一样
  bool get overdue => !done && at != null && at!.isBefore(DateTime.now());
}

/// 它记着的一件事 —— 见 go/osinit/notes.go
class Note {
  const Note({required this.key, required this.text, required this.who});
  final String key, text, who;

  factory Note.fromJson(Map<String, dynamic> j) => Note(
        key: j['key'] as String? ?? '',
        text: j['text'] as String? ?? '',
        who: j['who'] as String? ?? '',
      );
}

/// 一个它看出来的规律 —— 见 go/osinit/routine.go
class Habit {
  const Habit({
    required this.name,
    required this.guess,
    required this.why,
    required this.days,
    required this.minutes,
    required this.lat,
    required this.lon,
  });

  /// name 用户已经起过名就是那个名字，否则空 ——
  /// **空的那些才是它想让你确认的**
  final String name;

  /// guess 它猜这儿是什么：家 / 上班的地方 / 常去的地方
  final String guess;

  /// why 凭什么这么猜。**必须显示** —— 说不出理由的推断，
  /// 用户没法判断值不值得信
  final String why;

  final int days, minutes;
  final double lat, lon;

  factory Habit.fromJson(Map<String, dynamic> j) => Habit(
        name: j['name'] as String? ?? '',
        guess: j['guess'] as String? ?? '',
        why: j['why'] as String? ?? '',
        days: (j['days'] as num?)?.toInt() ?? 0,
        minutes: (j['minutes'] as num?)?.toInt() ?? 0,
        lat: (j['lat'] as num?)?.toDouble() ?? 0,
        lon: (j['lon'] as num?)?.toDouble() ?? 0,
      );
}
