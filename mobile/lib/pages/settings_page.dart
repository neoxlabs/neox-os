import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/os_client.dart';
import '../sense/keepalive.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/motion.dart';
import '../widgets/panel.dart';
import '../widgets/toggle.dart';
import 'spend_page.dart';
import 'devices_page.dart';
import 'system_page.dart';

/// 设置 —— **照客户端 .sec / .panel / .rw 那套**.
///
/// 客户端的原话:
///
///	分区标题是小字灰, 内容收在一张圆角面板里, 每行左边标题 + 一句说明、
///	右边控件. **说明只写一句** —— 设置页的字一多就没人看了,
///	而没人看的说明等于没写.
///
/// 三条尺寸固定为: 面板使用 fill-ghost 底和 radius 11(**不描边**),
/// 行 padding 13/15, 行与行之间一条通到边的 line, 开关 38×22 白钮.
///
/// 我前两版在这儿栽了两次: 第一版是发丝线 + 纯黑, 第二版是描边卡片 ——
/// 两次都是我自己想的, 而客户端里这套本来就是定好的.
class SettingsPage extends StatefulWidget {
  const SettingsPage({
    super.key,
    required this.state,
    required this.dark,
    required this.onTheme,
  });

  final AppState state;
  final bool dark;
  final ValueChanged<bool> onTheme;

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage>
    with WidgetsBindingObserver {
  bool _keepAlive = false;
  /// _exempt 在不在系统的电池白名单里. **每次回到这一页都重问**:
  /// 允不允许是他在系统弹框里点的, 我们这边没有回调
  bool _exempt = true;
  String _keepAliveNote = '';
  Timezone? _tz;
  /// _speak 念不念出来: off / urgent / all. 见 KeepAliveService.setSpeak
  String _speak = 'urgent';
  /// _readNotices 我们这边的开关; _noticeAccess 系统那边给权限了没有.
  /// **两件事** —— 见 KeepAliveService.setReadNotices
  bool _readNotices = false;
  bool _noticeAccess = false;
  /// 同上: 开关归我们, 权限归系统
  bool _readCalendar = false;
  bool _calAccess = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _refresh();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  /// 从系统设置那一页回来 —— 电池白名单、定位、通知权限都可能变了.
  /// 不重问的话, 他刚点了"允许", 回来看到的还是"没加进白名单"
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) _refresh();
  }

  Future<void> _refresh() async {
    // 定位权限可能在系统设置里被改过 —— 每次回到这一页问一次真的,
    // 而不是相信我们自己上次记下的那个值
    await widget.state.refreshLocationPermission();
    final st = await KeepAliveService.status();
    if (!mounted) return;
    setState(() {
      _keepAlive = st.wanted;
      _keepAliveNote = st.note;
    });
    final sp = await SharedPreferences.getInstance();
    final access = await KeepAliveService.hasNoticeAccess();
    final cal = await KeepAliveService.hasCalendar();
    final exempt = await KeepAliveService.batteryExempt();
    if (mounted) {
      setState(() {
        _exempt = exempt;
        _speak = sp.getString('speak') ?? 'urgent';
        _readNotices = sp.getBool('readNotices') ?? false;
        _noticeAccess = access;
        _readCalendar = sp.getBool('readCalendar') ?? false;
        _calAccess = cal;
      });
    }
    // 时区读一眼 —— 老版本 OS 上这个口子是 404, 那不是错误,
    // 只是它还没有这个概念
    try {
      final tz = await widget.state.client?.timezone();
      if (mounted) setState(() => _tz = tz);
    } catch (_) {}
  }

  /// 换时区.
  ///
  ///	**它进的是判断不是显示**: 日报几点发、「晚上七点提醒我」算哪一段。
  ///	Docker 里默认 UTC，全都差 8 小时，而一处都不会报错。
  ///
  ///	绝大多数时候没人需要点这一下 —— 手机报到时就带着时区。
  ///	这一页是给「按另一个地方的时间过日子」留的
  /// 挑一档「念出来」.
  ///
  ///	**三档不是两档**: 只有开关的话, 他为了躲开日报会把它整个关掉,
  ///	而那一关, 真正要紧的那次也到不了他
  Future<void> _pickSpeak() async {
    const opts = {
      'urgent': ('仅重要消息', '默认。日报和普通提醒不播报'),
      'all': ('全部播报', '所有主动消息'),
      'off': ('关闭', '仅发送通知'),
    };
    final picked = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: NX.bgElevated,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c) => SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(NX.s5, NX.s5, NX.s5, NX.s2),
            child: Align(
              alignment: Alignment.centerLeft,
              child: Text('语音播报', style: NX.heading),
            ),
          ),
          for (final e in opts.entries)
            ListTile(
              leading: Icon(
                  e.key == _speak
                      ? Icons.radio_button_checked_rounded
                      : Icons.radio_button_off_rounded,
                  size: 20,
                  color: e.key == _speak ? NX.accent : NX.text4),
              title: Text(e.value.$1, style: NX.body),
              subtitle:
                  Text(e.value.$2, style: NX.caption.copyWith(color: NX.text4)),
              onTap: () => Navigator.pop(c, e.key),
            ),
        ]),
      ),
    );
    if (picked == null || !mounted) return;
    setState(() => _speak = picked);
    await KeepAliveService.setSpeak(picked);
    // **两处都要存**: 界面读 SharedPreferences, 而念这件事发生在
    // 保活服务里(它读的是原生那份 prefs)
    (await SharedPreferences.getInstance()).setString('speak', picked);
  }

  Future<void> _pickZone() async {
    final tz = _tz;
    if (tz == null) return;
    final picked = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: NX.bgElevated,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c) => SafeArea(
        child: ListView(
          shrinkWrap: true,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(NX.s5, NX.s5, NX.s5, NX.s3),
              child: Align(
                alignment: Alignment.centerLeft,
                child: Text('按哪儿的时间', style: NX.heading),
              ),
            ),
            // ── 跟着手机 ──
            //
            //	**定死了要能松开**: 没有这一条, 点过一次就永远回不到
            //	自动 —— 他搬了城市, 手机报的新时区被自己两个月前那
            //	一下挡着, 而界面上看不出是被什么挡着的
            ListTile(
              leading: Icon(
                  tz.fixed
                      ? Icons.radio_button_off_rounded
                      : Icons.radio_button_checked_rounded,
                  size: 20,
                  color: tz.fixed ? NX.text4 : NX.accent),
              title: Text('跟着这台手机', style: NX.body),
              onTap: () => Navigator.pop(c, '\u0000auto'),
            ),
            Divider(height: 1, color: NX.line),
            for (final z in tz.pick)
              ListTile(
                leading: Icon(
                    tz.fixed && z == tz.zone
                        ? Icons.radio_button_checked_rounded
                        : Icons.radio_button_off_rounded,
                    size: 20,
                    color: tz.fixed && z == tz.zone ? NX.accent : NX.text4),
                title: Text(z, style: NX.body),
                trailing: z == tz.zone && !tz.fixed
                    ? Text('现在', style: NX.caption.copyWith(color: NX.text4))
                    : null,
                onTap: () => Navigator.pop(c, z),
              ),
          ],
        ),
      ),
    );
    if (picked == null || !mounted) return;
    if (picked != '\u0000auto' && picked == tz.zone && tz.fixed) return;
    try {
      if (picked == '\u0000auto') {
        await widget.state.client!.followTimezone();
      } else {
        await widget.state.client!.setTimezone(picked);
      }
      await _refresh();
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context)
          ..hideCurrentSnackBar()
          ..showSnackBar(SnackBar(
            content: Text('$e', style: NX.body.copyWith(color: Colors.white)),
            backgroundColor: NX.bad,
            behavior: SnackBarBehavior.floating,
          ));
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final s = widget.state;
    return ListenableBuilder(
      listenable: s,
      builder: (context, _) => ListView(
        padding: EdgeInsets.fromLTRB(
            NX.gutter, MediaQuery.of(context).padding.top + NX.s5,
            NX.gutter, NX.s7),
        children: [
          Text('设置', style: NX.display),
          const SizedBox(height: NX.s6),

          // ── 我是谁 ──
          //
          Sec(label: '连接', children: [
            Cell(
              icon: Icons.dns_rounded,
              title: '这台 OS',
              // 短值(还没填)走右边那一列跟隔壁对齐; 只有一条完整地址
              // 才摞到下面 —— 由 _Cell 自己按长度决定, 不在这儿拍
              // **一行**. 把 http:// 摘掉 —— host:port 才是有用的那半,
              // 而 scheme 在这儿永远是那两个之一, 占掉的宽度
              // 正好是那条地址放不下的原因
              note: s.base.isEmpty
                  ? '还没填'
                  : s.base.replaceFirst(RegExp(r'^https?://'), ''),
              mono: s.base.isNotEmpty,
              onTap: () => _editServer(context, s),
            ),
            Cell(
              icon: Icons.key_rounded,
              title: 'Token',
              // **不回显 token**. 它是一把钥匙, 而这一页会在
              // 别人也看得见屏幕的地方被打开
              note: s.token.isEmpty ? '还没填' : '已设置 · ${s.token.length} 位',
            ),
            Cell(
              icon: Icons.wifi_tethering_rounded,
              // **图标一律中性**: 状态本来就由右边那个字说了,
              // 图标再变一次色是同一件事说两遍, 而两块绿在浅色底上
              // 是整屏唯一的高饱和
              title: '状态',
              note: switch (s.conn) {
                Conn.live => '在线',
                Conn.connecting => '连接中',
                Conn.badToken => 'token 不对',
                Conn.error => s.lastError ?? '连不上',
                Conn.off => '未配置',
              },
              noteColor: switch (s.conn) {
                Conn.live => NX.ok,
                Conn.badToken || Conn.error => NX.bad,
                _ => null,
              },
              ctl: _Btn(
                  label: '重连', onTap: s.configured ? s.reconnect : null),
            ),
          ]),

          Sec(
            label: '主动',
            // 这一组是"早上它能不能喊醒你"的全部依赖.
            // **一个只有开关没有说明的后台服务, 用户只会把它关掉**
            foot: _keepAliveNote.isNotEmpty
                ? _keepAliveNote
                : '人不动的时候位置只用粗定位（基站和 WiFi），没挪超过 120 米就一条都不报；'
                    '在路上（走路、开车、连着车载蓝牙）才开 GPS，二三十秒一条。\n'
                    '常驻开着的时候它一直连着那台 OS；手机睡着之后系统会掐网，'
                    '所以它定时自己醒一次补课、报到 —— 加进电池白名单的话 8 分钟一次，'
                    '不加 15 分钟。',
            children: [
              Cell(
                icon: Icons.my_location_rounded,
                title: '上报位置',
                // **开关开着 ≠ 拿得到位置**: 系统那个框弹出来之后
                // 用户可能点了拒绝, 而那时候开关已经是开的了.
                // 说不出这件事的话, 他看到的是一个开着却什么都不干的开关
                sub: !s.collect
                    ? '已关闭'
                    : (s.hasLocation ? '不动时粗定位，在路上开 GPS' : '已开启，但缺少定位权限'),
                noteColor: s.collect && !s.hasLocation ? NX.warn : null,
                note: s.collect && !s.hasLocation ? '需在系统设置中授权' : null,
                ctl: NxToggle(
                  on: s.collect,
                  onChanged: (v) async {
                    await s.setCollect(v);
                    if (mounted) setState(() {});
                  },
                ),
              ),
              // ── 念出来 ──
              //
              //	**一条只能看的通知，在他开车、做饭、手上有东西的时候
              //	等于没有** —— 而那恰恰是最需要它开口的几个时刻。
              //
              //	缺省只念要紧的：全念的话，一天几条日报和提醒念下来，
              //	他会把整个通道关掉，而那一关，真正要紧的那次也到不了他。
              // ── 读通知 ──
              //
              //	短信验证码、快递、外卖、银行扣款、群里谁 @ 了你 ——
              //	这些事全都以通知的形式经过这台手机，而它们**没有一个
              //	有 API**。读通知是唯一一条能把它们收进来的路。
              //
              //	**权限最大的一件事**，所以默认关着，而且系统那一页
              //	要他亲手点。
              Cell(
                icon: Icons.notifications_active_outlined,
                title: '读取通知',
                sub: !_readNotices
                    ? '已关闭'
                    : (_noticeAccess ? '短信、快递、外卖等' : '已开启，但缺少权限'),
                noteColor: _readNotices && !_noticeAccess ? NX.warn : null,
                note: _readNotices && !_noticeAccess ? '需在系统设置中授权' : null,
                ctl: NxToggle(
                  on: _readNotices,
                  onChanged: (v) async {
                    setState(() => _readNotices = v);
                    (await SharedPreferences.getInstance())
                        .setBool('readNotices', v);
                    final ok = await KeepAliveService.setReadNotices(v);
                    if (mounted) setState(() => _noticeAccess = ok);
                  },
                ),
              ),
              // ── 读日历 ──
              //
              //	他已经有的日程不该让他再说一遍：公司发的会议邀请、
              //	订的机票酒店，本来就在手机日历里。不接的话它算
              //	「今天该几点出门」时把整个下午当空的。
              //
              //	**只读**。往他的日历里写东西是他没同意过的事。
              Cell(
                icon: Icons.event_note_rounded,
                title: '读取日历',
                sub: !_readCalendar
                    ? '已关闭'
                    : (_calAccess ? '未来 7 天日程' : '已开启，但缺少权限'),
                noteColor: _readCalendar && !_calAccess ? NX.warn : null,
                note: _readCalendar && !_calAccess ? '需在系统设置中授权' : null,
                ctl: NxToggle(
                  on: _readCalendar,
                  onChanged: (v) async {
                    setState(() => _readCalendar = v);
                    (await SharedPreferences.getInstance())
                        .setBool('readCalendar', v);
                    final ok = await KeepAliveService.setReadCalendar(v);
                    if (mounted) setState(() => _calAccess = ok);
                  },
                ),
              ),
              Cell(
                icon: Icons.record_voice_over_rounded,
                title: '语音播报',
                sub: const {
                  'off': '不念',
                  'urgent': '只念要紧的',
                  'all': '每条都念',
                }[_speak],
                onTap: _pickSpeak,
              ),
              Cell(
                icon: Icons.bolt_rounded,
                title: '后台常驻',
                ctl: NxToggle(
                  on: _keepAlive,
                  onChanged: (v) async {
                    if (v && !s.configured) {
                      _toast(context, '先填地址和 token');
                      return;
                    }
                    v
                        ? await KeepAliveService.start(
                            base: s.base, token: s.token)
                        : await KeepAliveService.stop();
                    await _refresh();
                  },
                ),
              ),
              // ── 睡着了也别聋 ──
              //
              //	**不在电池白名单里, 手机一睡着它就聋了**: 心跳停、网被掐.
              //	2026-09-11 那一夜 00:30 到 08:04 一次报到都没有 —— 开关开着,
              //	服务也"在", 而它什么都听不见.
              //
              //	白名单查得到, 所以这里是一个真的状态 + 一个真的按钮
              //	(系统弹框, 他点"允许"才算). 厂商那层查不到, 只能送他过去
              if (_keepAlive)
                Cell(
                  icon: Icons.battery_saver_rounded,
                  title: '电池白名单',
                  sub: _exempt ? '已加入，睡着了也照常报到' : '未加入，手机睡着后会掉线',
                  noteColor: _exempt ? null : NX.warn,
                  note: _exempt ? null : '点「允许」后回到这一页',
                  ctl: _exempt
                      ? null
                      : _Btn(
                          label: '去允许',
                          onTap: () async {
                            final ok = await KeepAliveService.askBatteryExempt();
                            if (mounted) setState(() => _exempt = ok);
                          },
                        ),
                ),
              if (_keepAlive)
                Cell(
                  icon: Icons.rocket_launch_outlined,
                  title: '自启动与后台运行',
                  sub: '小米、华为、OPPO、vivo 另有一处，要在系统里手动打开',
                  onTap: () async {
                    final ok = await KeepAliveService.openAutoStart();
                    if (!ok && context.mounted) {
                      _toast(context, '没找到那一页，去系统设置 → 应用 → NeoxPilot 里找「自启动」');
                    }
                  },
                ),
            ],
          ),

          // ── 这台机器 ──
          //
          // 网页端那几页(推理服务 / 权限 / 花了多少)在手机上收成两页:
          // 手机的一屏放不下网页端那种并排布局, 而分太细的话
          // 用户要点很多次才拼得出一个完整印象
          Sec(
            label: '这台 OS',
            foot: s.configured ? null : '先填地址和 token',
            children: [
              Cell(
                icon: Icons.tune_rounded,
                title: '这台机器',
                onTap: s.configured
                    ? () => Navigator.of(context).push(SlideRoute(builder: (_) => SystemPage(state: s)))
                    : null,
              ),
              Cell(
                icon: Icons.devices_other_rounded,
                title: '接进来的设备',
                sub: '手机、家里的桥接、以后的耳机',
                onTap: s.configured
                    ? () => Navigator.of(context).push(SlideRoute(
                        builder: (_) => DevicesPage(state: s)))
                    : null,
              ),
              // ── 时区 ──
              //
              //	Docker 里默认 UTC，而它进的是**判断**: 日报几点发、
              //	「晚上七点提醒我」算哪一段。全差 8 小时，一处不报错。
              //
              //	**副标题给的是那台机器此刻的钟点** —— 那是唯一能看出
              //	设对没设对的东西；只显示时区名的话，用户得自己去换算
              if (_tz != null)
                Cell(
                  icon: Icons.schedule_rounded,
                  title: '时区',
                  sub: '${_tz!.zone} · ${_tz!.now}',
                  note: _tz!.fixed ? null : '跟着手机',
                  onTap: _pickZone,
                ),
              Cell(
                icon: Icons.receipt_long_rounded,
                title: '花了多少',
                onTap: s.configured
                    ? () => Navigator.of(context).push(SlideRoute(builder: (_) => SpendPage(state: s)))
                    : null,
              ),
            ],
          ),

          Sec(label: '外观', children: [
            Cell(
              icon: widget.dark
                  ? Icons.dark_mode_rounded
                  : Icons.light_mode_rounded,
              title: '主题',
              sub: null,
              ctl: _Seg(
                options: const ['浅色', '深色'],
                index: widget.dark ? 1 : 0,
                onPick: (i) => widget.onTheme(i == 1),
              ),
            ),
          ]),

          // 「屋里的人」那一段删掉了: **消息列表页就是它** ——
          // 同一份东西在两个地方各画一遍, 改一次要改两处,
          // 而且用户会问"这两个列表有什么区别"(没有区别).
          // 静音也挪回了会话页那边: 在谁的会话里静音谁, 才是它该在的地方

          Sec(label: '关于', children: [
            const Cell(
                icon: Icons.all_inclusive_rounded,
                title: 'NeoX',
                note: '0.1.0'),
            Cell(
                icon: Icons.receipt_long_rounded,
                title: '收到的事件',
                note: s.threads.values.fold(0, (a, b) => a + b.length) == 0
                    ? '还没有'
                    : '${s.threads.values.fold(0, (a, b) => a + b.length)} 条'),
          ]),
        ],
      ),
    );
  }

  void _toast(BuildContext c, String m) => ScaffoldMessenger.of(c)
    ..hideCurrentSnackBar()
    ..showSnackBar(SnackBar(
      content: Text(m, style: NX.body.copyWith(color: Colors.white)),
      backgroundColor: NX.isDark ? NX.bgElevated : const Color(0xFF2A2A2E),
      behavior: SnackBarBehavior.floating,
    ));

  Future<void> _editServer(BuildContext context, AppState s) async {
    final base = TextEditingController(text: s.base);
    final token = TextEditingController(text: s.token);
    final ok = await showModalBottomSheet<bool>(
      context: context,
      backgroundColor: NX.bgElevated,
      isScrollControlled: true,
      showDragHandle: true,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c) => Padding(
        padding: EdgeInsets.only(
            left: NX.gutter,
            right: NX.gutter,
            bottom: MediaQuery.of(c).viewInsets.bottom + NX.s6),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('连到哪台 OS', style: NX.title),
            const SizedBox(height: NX.s2),
            Text(
              // 手机与容器不在同一网络时, localhost 会指向手机本身,
              // 因此这里必须填写运行容器的机器地址
              '容器跑在哪台机器上就填哪台的地址。手机上的 localhost 是手机自己。',
              style: NX.caption,
            ),
            const SizedBox(height: NX.s5),
            _Field(
                controller: base,
                label: '地址',
                hint: 'http://192.168.1.10:7717',
                keyboard: TextInputType.url),
            const SizedBox(height: NX.s4),
            _Field(controller: token, label: 'Token', hint: 'NEOX_OBSERVE_TOKEN'),
            const SizedBox(height: NX.s5),
            SizedBox(
              width: double.infinity,
              child: FilledButton(
                onPressed: () => Navigator.pop(c, true),
                style: FilledButton.styleFrom(
                  backgroundColor: NX.accent,
                  foregroundColor: Colors.white,
                  padding: const EdgeInsets.symmetric(vertical: NX.s4),
                  shape: RoundedRectangleBorder(
                      borderRadius: BorderRadius.circular(NX.rMd)),
                ),
                child: const Text('保存并连接',
                    style: TextStyle(
                        fontSize: NX.fBody, fontWeight: NX.wMed)),
              ),
            ),
          ],
        ),
      ),
    );
    if (ok == true) {
      await s.saveServer(base.text, token.text);
      // 服务开着的话要拿新地址重起一遍 —— 不然它还连着旧地址,
      // 而设置页显示的是新的
      if (_keepAlive) {
        await KeepAliveService.start(base: s.base, token: s.token);
        await _refresh();
      }
    }
  }
}

/// 分段器 —— 客户端 .sg. 两三个互斥的选项用它, 不用下拉:
/// 下拉要点两次才看得见有什么可选
class _Seg extends StatelessWidget {
  const _Seg({
    required this.options,
    required this.index,
    required this.onPick,
  });

  final List<String> options;
  final int index;
  final ValueChanged<int> onPick;

  @override
  Widget build(BuildContext context) => Container(
        padding: const EdgeInsets.all(2),
        decoration: BoxDecoration(
          color: NX.fill2,
          borderRadius: BorderRadius.circular(NX.rSm + 2),
        ),
        child: Row(mainAxisSize: MainAxisSize.min, children: [
          for (var i = 0; i < options.length; i++)
            GestureDetector(
              onTap: () => onPick(i),
              behavior: HitTestBehavior.opaque,
              child: Container(
                padding:
                    const EdgeInsets.symmetric(horizontal: NX.s3, vertical: NX.s2),
                decoration: BoxDecoration(
                  color: i == index ? NX.bgElevated : Colors.transparent,
                  borderRadius: BorderRadius.circular(NX.rSm),
                  boxShadow: i == index
                      ? [
                          BoxShadow(
                              color: Colors.black.withValues(alpha: .18),
                              blurRadius: 2,
                              offset: const Offset(0, 1))
                        ]
                      : null,
                ),
                child: Text(options[i],
                    style: NX.label.copyWith(
                        color: i == index ? NX.text : NX.text3,
                        fontWeight: i == index ? NX.wMed : NX.wReg)),
              ),
            ),
        ]),
      );
}

/// 小按钮 —— 客户端 .icon 那一档
class _Btn extends StatelessWidget {
  const _Btn({required this.label, this.onTap});
  final String label;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => GestureDetector(
        onTap: onTap,
        behavior: HitTestBehavior.opaque,
        child: Container(
          padding: const EdgeInsets.symmetric(
              horizontal: NX.s3, vertical: NX.s2),
          decoration: BoxDecoration(
            color: NX.fill2,
            borderRadius: BorderRadius.circular(NX.rSm + 2),
          ),
          child: Text(label,
              style: NX.label.copyWith(
                  color: onTap == null ? NX.text4 : NX.text2,
                  fontWeight: NX.wMed)),
        ),
      );
}

class _Field extends StatelessWidget {
  const _Field({
    required this.controller,
    required this.label,
    required this.hint,
    this.keyboard,
  });
  final TextEditingController controller;
  final String label, hint;
  final TextInputType? keyboard;

  @override
  Widget build(BuildContext context) => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(label, style: NX.section),
          const SizedBox(height: NX.s2),
          TextField(
            controller: controller,
            style: NX.body,
            keyboardType: keyboard,
            keyboardAppearance: NX.isDark ? Brightness.dark : Brightness.light,
            autocorrect: false,
            enableSuggestions: false,
            // 地址和 token 里出现空格必然是粘贴带进来的,
            // 而它会让连接以一个查不出原因的失败收场
            inputFormatters: [FilteringTextInputFormatter.deny(RegExp(r'\s'))],
            decoration: InputDecoration(
              hintText: hint,
              hintStyle: NX.body.copyWith(color: NX.text4),
              filled: true,
              fillColor: NX.fill,
              isDense: true,
              contentPadding:
                  const EdgeInsets.symmetric(
                      horizontal: NX.s4, vertical: NX.s4),
              border: OutlineInputBorder(
                borderRadius: BorderRadius.circular(NX.rMd),
                borderSide: BorderSide.none,
              ),
              enabledBorder: OutlineInputBorder(
                borderRadius: BorderRadius.circular(NX.rMd),
                borderSide: BorderSide.none,
              ),
              focusedBorder: OutlineInputBorder(
                borderRadius: BorderRadius.circular(NX.rMd),
                borderSide: BorderSide(color: NX.accent, width: 1.5),
              ),
            ),
          ),
        ],
      );
}
