import 'package:flutter/material.dart';

import '../api/os_client.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/empty_state.dart';
import '../widgets/panel.dart';

/// 接进来的设备 —— 屋里有哪些东西，各自能感知什么、能怎么跟你说话。
///
/// ── 这一页要回答的是"它到底看得见什么" ──
///
/// 一个 24 小时的助理，能不能提醒你带伞取决于它看不看得见天气；
/// 能不能在你到家时说话，取决于有没有东西在报位置。**而这件事
/// 原来在界面上一个字都看不到** —— 用户只能从"它从来不提醒我"
/// 反推，而那反推不出任何东西。
///
/// 每台设备写清三件事：报什么、怎么说、上次什么时候见过。
/// 第三件最要紧：一台三天没报到的设备，跟一台不存在的设备，
/// 在别处长得一模一样。
class DevicesPage extends StatefulWidget {
  const DevicesPage({super.key, required this.state});
  final AppState state;

  @override
  State<DevicesPage> createState() => _DevicesPageState();
}

class _DevicesPageState extends State<DevicesPage> {
  List<Device>? _list;
  String? _err;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final d = await widget.state.client!.devices();
      if (mounted) setState(() => _list = d);
    } catch (e) {
      if (mounted) setState(() => _err = '$e');
    }
  }

  @override
  Widget build(BuildContext context) {
    final list = _list;
    return Scaffold(
      backgroundColor: NX.bg,
      body: Column(children: [
        PageBar(
          title: '接进来的设备',
          action: IconButton(
            onPressed: () {
              setState(() {
                _list = null;
                _err = null;
              });
              _load();
            },
            icon: const Icon(Icons.refresh_rounded, size: 20),
            color: NX.text3,
          ),
        ),
        Expanded(
          child: _err != null
              ? Center(
                  child: Padding(
                    padding: const EdgeInsets.all(NX.s7),
                    child: Text(
                      // **404 要说人话**: 这台 OS 可能还没接感知层,
                      // 而那跟"出错了"是两回事 —— 用户会去查网络
                      _err!.contains('404')
                          ? '这台 OS 还没接感知层，它还不认识任何设备。'
                          : _err!,
                      style: NX.bodyDim,
                      textAlign: TextAlign.center,
                    ),
                  ),
                )
              : list == null
                  ? const Center(child: CircularProgressIndicator())
                  : list.isEmpty
                      ? const EmptyState(
                          art: 'assets/empty_nobots.png',
                          line: '还没有设备报到',
                          hint: '这台手机会在连上时自己报到；'
                              '家里的桥接和别的采集端各自报各自的',
                        )
                      : ListView(
                          padding: const EdgeInsets.fromLTRB(
                              NX.gutter, NX.s3, NX.gutter, NX.s7),
                          children: [
                            Sec(
                              label: '屋里的 ${list.length} 台',
                              foot: '每台设备自己声明能干什么。'
                                  '换个硬件不用改 OS —— 耳机只报"能出声"，'
                                  '它就只会被拿来念。',
                              children: [
                                for (final d in list) _Row(d: d),
                              ],
                            ),
                          ],
                        ),
        ),
      ]),
    );
  }
}

class _Row extends StatelessWidget {
  const _Row({required this.d});
  final Device d;

  @override
  Widget build(BuildContext context) {
    final quiet = _quietFor(d.at);
    return Padding(
      padding: const EdgeInsets.fromLTRB(NX.s4, NX.s3, NX.s4, NX.s3),
      child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
        SizedBox(
          width: 24,
          child: Icon(_icon(d.kind), size: 20, color: NX.text3),
        ),
        const SizedBox(width: NX.s3),
        Expanded(
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Row(children: [
              Expanded(
                child: Text(d.name,
                    style: NX.body, maxLines: 1, overflow: TextOverflow.ellipsis),
              ),
              const SizedBox(width: NX.s2),
              // **上次见过是什么时候** —— 一台三天没报到的设备,
              // 跟一台不存在的设备在别处长得一模一样
              Text(quiet.$1, style: NX.caption.copyWith(color: quiet.$2)),
            ]),
            const SizedBox(height: 3),
            Text(_says(d), style: NX.caption.copyWith(color: NX.text4)),
          ]),
        ),
      ]),
    );
  }

  /// 一句话说清它报什么、怎么说.
  ///
  /// **只感知或只呈现都要说得出来**: 这两种设备在屋里是常态
  /// (体重秤只报数, 音箱只出声), 而"两样都有"反倒少见
  static String _says(Device d) {
    final parts = <String>[];
    if (d.senses.isNotEmpty) parts.add('报 ${d.senses.join('、')}');
    if (d.presents.isNotEmpty) {
      parts.add('能${d.presents.map(_wayWord).join('、')}');
    }
    return parts.isEmpty ? '什么都不干' : parts.join(' · ');
  }

  static String _wayWord(String way) => switch (way) {
        'notify' => '摆一条出来',
        'alert' => '吵醒你',
        'speak' => '念出来',
        'ask' => '问你一句',
        _ => way,
      };

  static IconData _icon(String kind) => switch (kind) {
        'phone' => Icons.smartphone_rounded,
        'watch' => Icons.watch_rounded,
        'earbuds' => Icons.headphones_rounded,
        'speaker' => Icons.speaker_rounded,
        'bridge' => Icons.hub_rounded,
        'mcu' => Icons.memory_rounded,
        _ => Icons.device_unknown_rounded,
      };

  /// 多久没动静了 —— 颜色跟着走.
  ///
  /// 不设阈值报警(那是 OS 那边心跳的事), 这里只是照实说:
  /// 界面上的"上次报到"跟 OS 的失联判断是两个东西, 混起来的话
  /// 用户会以为界面变灰就等于系统已经知道了
  static (String, Color) _quietFor(int at) {
    if (at == 0) return ('没报过', NX.text4);
    final d = DateTime.now()
        .difference(DateTime.fromMillisecondsSinceEpoch(at));
    if (d.inMinutes < 5) return ('刚刚', NX.ok);
    if (d.inMinutes < 60) return ('${d.inMinutes} 分钟前', NX.text3);
    if (d.inHours < 24) return ('${d.inHours} 小时前', NX.warn);
    return ('${d.inDays} 天前', NX.bad);
  }
}
