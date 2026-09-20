import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'pages/list_page.dart';
import 'pages/mind_page.dart';
import 'pages/settings_page.dart';
import 'state/app_state.dart';
import 'theme.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const NeoxApp());
}

class NeoxApp extends StatefulWidget {
  const NeoxApp({super.key});

  @override
  State<NeoxApp> createState() => _NeoxAppState();
}

class _NeoxAppState extends State<NeoxApp> {
  final _state = AppState();

  /// 深浅. 客户端里这是一个 data-theme 属性, 这边是一个 bool ——
  /// **只有这一个开关**, 别的令牌一个都不动(见 theme.dart).
  ///
  /// 默认浅色. 客户端默认深色是因为它整天开在一块大屏上,
  /// 手机是白天在外面掏出来看的 —— 而且这是用户点名要的那个
  bool _dark = false;

  @override
  void initState() {
    super.initState();
    _state.load();
    SharedPreferences.getInstance().then((p) {
      final v = p.getBool('dark') ?? false;
      if (mounted && v != _dark) setState(() => _dark = v);
    });
  }

  Future<void> _setDark(bool v) async {
    setState(() => _dark = v);
    (await SharedPreferences.getInstance()).setBool('dark', v);
  }

  @override
  void dispose() {
    _state.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final theme = NX.theme(_dark);
    // 状态栏图标跟着主题走. 不跟的话浅色主题下顶上是一排看不见的白图标
    SystemChrome.setSystemUIOverlayStyle(SystemUiOverlayStyle(
      statusBarColor: Colors.transparent,
      statusBarIconBrightness: _dark ? Brightness.light : Brightness.dark,
      statusBarBrightness: _dark ? Brightness.dark : Brightness.light,
      systemNavigationBarColor: NX.bg,
      systemNavigationBarIconBrightness:
          _dark ? Brightness.light : Brightness.dark,
    ));
    return MaterialApp(
      title: 'NeoxPilot',
      debugShowCheckedModeBanner: false,
      theme: theme,
      // ── 门 ──
      //
      //	不登录不给进. 这是产品上的决定, 而代码里要说清楚它到底
      //	挡住了什么:
      //
      //	**它挡的是"这台手机代表谁"**, 不是"能不能访问那台 OS" ——
      //	进 OS 靠的是它的 token(见 go/osinit/observe.go 的 guard).
      //	把这道门当成安全边界的话, 会得到一种最坏的东西: 一道看起来
      //	存在、实际一推就倒的墙, 而人会依据那道墙去放东西.
      //
      //	它真正解决的两件事: 屋里不止一个人的时候分得清谁是谁;
      //	以及额度和套餐要挂在一个账号上.
      home: ListenableBuilder(
        listenable: _state,
        builder: (context, _) {
          // 本地配置还没读完 —— **先什么都不画**.
          //
          //	判成"没登录"的话, 每次冷启动都会闪一下登录页, 而用户明明
          //	是登着的. 那种闪比慢半拍难受得多
          if (!_state.loaded) {
            return Scaffold(backgroundColor: NX.bg, body: const SizedBox());
          }
          return Home(state: _state, dark: _dark, onTheme: _setDark);
        },
      ),
    );
  }
}

class Home extends StatefulWidget {
  const Home({
    super.key,
    required this.state,
    required this.dark,
    required this.onTheme,
  });

  final AppState state;
  final bool dark;
  final ValueChanged<bool> onTheme;

  @override
  State<Home> createState() => _HomeState();
}

class _HomeState extends State<Home> {
  /// 开在消息列表.
  ///
  /// **这是一个 IM**, 而 IM 打开就该是消息 —— 开在核心页的话,
  /// 每次打开都要多点一下才能看见谁找过你, 而那是绝大多数时候
  /// 打开这个 App 的理由
  int _tab = 0;

  @override
  Widget build(BuildContext context) => Scaffold(
        backgroundColor: NX.bg,
        body: IndexedStack(index: _tab, children: [
          ListPage(
              state: widget.state,
              // 空态里那颗"去连接"要能真的把人送到设置页 ——
              // 只说"去设置页填地址"而不给路, 等于让他自己找
              onGoSettings: () => setState(() => _tab = 2)),
          // ── 语音不在这儿 ──
          //
          //	独立的 tab **不知道该跟谁说话** —— 如果把话发给
          //	bots.first(感知判断那个, 它压根不回聊天),
          //	用户就会对着屏幕说半天却得不到回应。
          //
          //	它在每个 bot 的聊天里: 点一下就从打字切成对话,
          //	而"跟谁"这件事那一页本来就知道。见 pages/voice_page.dart
          MindPage(state: widget.state),
          SettingsPage(
              state: widget.state,
              dark: widget.dark,
              onTheme: widget.onTheme),
        ]),
        bottomNavigationBar: Container(
          decoration: BoxDecoration(
            color: NX.bg,
            border: Border(top: BorderSide(color: NX.line)),
          ),
          child: NavigationBarTheme(
            data: NavigationBarThemeData(
              backgroundColor: Colors.transparent,
              indicatorColor: NX.fill2,
              surfaceTintColor: Colors.transparent,
              labelTextStyle: WidgetStateProperty.resolveWith((st) =>
                  NX.caption.copyWith(
                    fontWeight: NX.wMed,
                    color:
                        st.contains(WidgetState.selected) ? NX.text : NX.text3,
                  )),
              iconTheme: WidgetStateProperty.resolveWith((st) => IconThemeData(
                    size: 24,
                    color: st.contains(WidgetState.selected)
                        ? NX.text
                        : NX.text3,
                  )),
            ),
            child: NavigationBar(
              selectedIndex: _tab,
              height: 64,
              onDestinationSelected: (i) => setState(() => _tab = i),
              destinations: const [
                NavigationDestination(
                    icon: Icon(Icons.chat_bubble_outline_rounded),
                    selectedIcon: Icon(Icons.chat_bubble_rounded),
                    label: '消息'),
                NavigationDestination(
                    icon: Icon(Icons.checklist_rounded),
                    selectedIcon: Icon(Icons.checklist_rounded),
                    label: '事项'),
                NavigationDestination(
                    icon: Icon(Icons.tune_outlined),
                    selectedIcon: Icon(Icons.tune_rounded),
                    label: '设置'),
              ],
            ),
          ),
        ),
      );
}
