import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';

/// 后台常驻的开关 —— Dart 这边只是一个把手, 活在原生那边干.
///
/// ── 为什么保活必须是原生的 ──
///
/// Flutter 的 Dart 引擎跟着 Activity 走: 用户一划掉任务卡,
/// 或者系统一回收后台, 这边的定时器和长连接**当场就没了** ——
/// 而且是静默的. 那之后"它不再提醒我了", 查不到根.
///
/// 而"早上主动喊你"这件事的全部前提, 就是**手机睡着的时候那条线还在**.
/// 所以它是一个前台服务(安卓明确支持的那条路), 不是一个 Dart 定时器.
///
/// ── 常驻通知不是代价, 是应该的 ──
///
/// 一个持续替你听着、还常驻不死的东西, 本来就不该藏起来.
/// 用户看得见它在跑, 也随时能掐掉.
class KeepAliveService {
  KeepAliveService._();

  static const _ch = MethodChannel('neox/keepalive');

  static Future<void> start({required String base, required String token}) async {
    if (!Platform.isAndroid) return;
    try {
      // **先要通知权限, 再起服务.**
      //
      // 反过来的话服务会照常跑、事件照常收、判断照常做, 而那句话
      // 谁也看不见 —— 用户只会觉得"它从来不提醒我", 查不到根.
      // 这就是整套主动智能断在最后一米的那种坏法
      await _ch.invokeMethod('askNotify');
      await _ch.invokeMethod('start', {'base': base, 'token': token});
    } on PlatformException {
      // 起不来不该把设置页崩掉 —— status() 下一次会如实报
    }
  }

  static Future<void> stop() async {
    if (!Platform.isAndroid) return;
    try {
      await _ch.invokeMethod('stop');
    } on PlatformException {
      // 同上
    }
  }

  /// 告诉原生那一侧"我是谁" —— 它据此决定一条通知响不响.
  ///
  /// **不能只存在 Dart 里**: 判"这条是不是给我的"发生在保活服务里,
  /// 而那个进程可能是被闹钟叫醒的 —— 那时候 Dart 侧根本没起来
  static Future<void> whoAmI(String id) async {
    if (!Platform.isAndroid) return;
    try {
      await _ch.invokeMethod('whoAmI', {'id': id});
    } on PlatformException {
      // 同上
    } on MissingPluginException {
      // 老版本没这个方法 —— 那就是所有通知都响, 跟以前一样
    }
  }

  /// 念不念出来 —— `off` / `urgent`(缺省) / `all`.
  ///
  /// ── 为什么缺省是「只念要紧的」 ──
  ///
  /// 一条只能看的通知，在他开车、做饭、手上有东西的时候等于没有 ——
  /// 而那恰恰是最需要它开口的几个时刻。
  ///
  /// 但缺省也不能是「全念」：一天几条日报和提醒全念出来，他会把整个
  /// 通道关掉，而那一关，真正要紧的那次也到不了他。
  static Future<void> setSpeak(String mode) async {
    if (!Platform.isAndroid) return;
    try {
      await _ch.invokeMethod('speakMode', {'mode': mode});
    } on PlatformException {
      // 同上
    } on MissingPluginException {
      // 老版本没这个方法 —— 那就是照旧不念
    }
  }

  /// 打开/关掉「读通知」。返回**系统那边给权限了没有**。
  ///
  /// ── 为什么返回的是权限而不是"成功了" ──
  ///
  /// **两个开关是两件事**：系统那个是「能不能读」，我们这个是
  /// 「要不要用」。开我们这个的时候会把他送到系统那一页，但他可能
  /// 在那儿点了返回——那时候我们的开关已经是开的了。
  ///
  /// 界面必须能说出「开着，但系统没给权限」，否则他看到的是一个
  /// 开着却什么都不干的开关。跟位置那个同一条道理。
  static Future<bool> setReadNotices(bool on) async {
    if (!Platform.isAndroid) return false;
    try {
      return await _ch.invokeMethod<bool>('readNotices', {'on': on}) ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  static Future<bool> hasNoticeAccess() async {
    if (!Platform.isAndroid) return false;
    try {
      return await _ch.invokeMethod<bool>('hasNoticeAccess') ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  /// 念一句 —— 对话里的回话走这条。
  ///
  /// **打断上一句**：他又说了一句，上一句的回话就作废了。
  /// 主动消息那条是排队念的（见 KeepAliveService.speak），两回事。
  static Future<void> say(String text) async {
    if (!Platform.isAndroid) return;
    try {
      await _ch.invokeMethod('say', {'text': text});
    } on PlatformException {
      // 这台机器没装语音引擎 —— 念不出来只是少一层, 字还在屏幕上
    } on MissingPluginException {
      // 老版本没这个方法
    }
  }

  static Future<void> shutUp() async {
    if (!Platform.isAndroid) return;
    try {
      await _ch.invokeMethod('shutUp');
    } on PlatformException {
      // 同上
    } on MissingPluginException {
      // 同上
    }
  }

  static StreamController<bool>? _tts;

  /// 它开始念了(true) / 全念完了(false) —— **原生那边推过来的**.
  ///
  /// ── 为什么不能再靠轮询 ──
  ///
  /// 语音页原来每 400ms 问一次 [speaking]. 对画一颗球够了, 对"念的时候
  /// 别听"不够: 那 400ms 里, 它自己念的头几个字已经进了麦克风, 又被当成
  /// 他说的发了出去 —— 2026-09-11 车上一路都是这样.
  ///
  /// 所以 Speaker 那边一变就推(见 MainActivity 里 Speaker.listener).
  /// 这一条是 neox/keepalive 上唯一一个反方向的调用, 所以整个通道的
  /// 回调就挂在这儿
  static Stream<bool> get ttsEvents {
    var c = _tts;
    if (c == null) {
      c = _tts = StreamController<bool>.broadcast();
      if (Platform.isAndroid) {
        _ch.setMethodCallHandler((call) async {
          if (call.method == 'tts') {
            final m = call.arguments;
            _tts?.add(m is Map && m['speaking'] == true);
          }
          return null;
        });
      }
    }
    return c.stream;
  }

  /// 在不在系统的电池白名单里.
  ///
  /// **不在的话手机一睡着它就聋了**: 心跳停、网被掐, 只剩 15 分钟一次的
  /// 闹钟那十来秒. 界面要能如实说出来, 而不是一个看着开着的开关
  static Future<bool> batteryExempt() async {
    if (!Platform.isAndroid) return true;
    try {
      return await _ch.invokeMethod<bool>('batteryExempt') ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  /// 请他把这个 App 加进电池白名单 —— 系统弹框, 他点了才算.
  /// 返回**这一刻**在不在(弹框是异步的, 回到这一页时要再问一次)
  static Future<bool> askBatteryExempt() async {
    if (!Platform.isAndroid) return true;
    try {
      return await _ch.invokeMethod<bool>('askBatteryExempt') ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  /// 把他送到厂商的「自启动 / 后台运行」那一页.
  ///
  /// 小米、华为、OPPO、vivo 在系统的省电之上各加了一层, **没有接口能查**,
  /// 只能送他过去自己开. 送不到的话返回 false, 界面上照实说
  static Future<bool> openAutoStart() async {
    if (!Platform.isAndroid) return false;
    try {
      return await _ch.invokeMethod<bool>('openAutoStart') ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  /// 现在正在念吗 —— 界面据此让那颗球跟着律动
  static Future<bool> speaking() async {
    if (!Platform.isAndroid) return false;
    try {
      return await _ch.invokeMethod<bool>('speaking') ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  /// 打开/关掉「读手机日历」。返回**这会儿有没有日历权限**。
  ///
  /// **只读**：往他的日历里写东西是他没同意过的事——一条误加的日程会
  /// 出现在所属者的共享视图里，而所属者不知道是谁加的。
  static Future<bool> setReadCalendar(bool on) async {
    if (!Platform.isAndroid) return false;
    try {
      return await _ch.invokeMethod<bool>('readCalendar', {'on': on}) ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  static Future<bool> hasCalendar() async {
    if (!Platform.isAndroid) return false;
    try {
      return await _ch.invokeMethod<bool>('hasCalendar') ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  /// 打开/关掉位置上报. 返回**这会儿有没有定位权限**.
  ///
  /// ── 为什么返回的是"有没有权限"而不是"成功了" ──
  ///
  /// 打开开关和拿到权限是两件事: 系统那个框弹出来之后用户可能点拒绝,
  /// 而那时候开关已经是开的了. 界面必须能说出"开关开着, 但它拿不到
  /// 位置" —— 否则用户看到的是一个开着却什么都不干的开关.
  static Future<bool> setCollect(bool on, {required String deviceId}) async {
    if (!Platform.isAndroid) return false;
    try {
      final ok = await _ch.invokeMethod<bool>(
          'setCollect', {'on': on, 'deviceId': deviceId});
      return ok ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  /// 这会儿有没有定位权限
  static Future<bool> hasLocation() async {
    if (!Platform.isAndroid) return false;
    try {
      return await _ch.invokeMethod<bool>('hasLocation') ?? false;
    } on PlatformException {
      return false;
    } on MissingPluginException {
      return false;
    }
  }

  static Future<KeepAliveStatus> status() async {
    if (!Platform.isAndroid) {
      // **说清是"这个平台没有", 不是"关着"** —— 后者会让用户
      // 一直去点那个开关, 而它永远不会亮
      return const KeepAliveStatus(
          false, false, 'iOS 上系统不给后台常驻, 这一条只在安卓上有');
    }
    try {
      final m = await _ch.invokeMapMethod<String, dynamic>('status');
      return KeepAliveStatus(
        m?['wanted'] as bool? ?? false,
        m?['running'] as bool? ?? false,
        m?['note'] as String? ?? '',
      );
    } on PlatformException catch (e) {
      return KeepAliveStatus(false, false, '问不到服务: ${e.message}');
    } on MissingPluginException {
      return const KeepAliveStatus(false, false, '这个版本没装保活服务');
    }
  }
}

class KeepAliveStatus {
  const KeepAliveStatus(this.wanted, this.running, this.note);

  /// wanted 用户要不要 —— **开关画的是这个**.
  ///
  /// 服务活在一个随时会被端掉的进程里(装新包、划掉最近任务、系统内存紧、
  /// 厂商半夜清一遍). 拿 running 画开关的话, 每被杀一次开关就自己弹回
  /// "关" —— 而他从没关过, 只会觉得这个开关存不住
  final bool wanted;

  /// running 它此刻真的活着没有 —— 这个只影响底下那句说明
  final bool running;

  /// note 给人看的一句话 —— 它开着的时候在干什么, 或者为什么开不起来.
  /// **一个只有开关没有说明的后台服务, 用户只会把它关掉**
  final String note;
}
