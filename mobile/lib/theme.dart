import 'package:flutter/material.dart';

/// 设计系统 —— **这个 App 里所有的数字都从这儿取**.
///
/// ── 为什么要有这一层 ──
///
/// 上一版没有. 后果是数出来的:
///
///	字号 11 种   11 / 11.5 / 12 / 12.5 / 13 / 13.5 / 15 / 15.5 / 16 / 19 / 28
///	字重 4 种    w400 / w500 / w600 / w700 混着用
///	圆角 8 种    5 / 6 / 8 / 10 / 13 / 16 / 19 / 999
///	间距         几乎每一个整数都有
///
/// **12 和 12.5 和 13 谁也分不出来**, 但它们保证了没有一处能对齐 ——
/// 而"没有一处能对齐"人是看得出来的, 只是说不上来哪儿不对.
/// 用户的原话: 每个组件都没什么关系.
///
/// 所以这一版立三条规矩:
///
///	① 页面里**不许出现字面量**的字号 / 圆角 / 颜色, 只能引这里的常量
///	② 每个刻度之间要**明显有别** —— 分不出来的两档等于噪声
///	③ 档位宁少勿多. 缺一档的代价是某处稍微将就, 多一档的代价是
///	   从此以后所有人都得猜该用哪一档
///
/// 颜色那部分照 packages/apps/console/src/shell/console.css 抄,
/// 不是新定的(见 ink / accent 那几段).
class NX {
  NX._();

  // ══════════════════════════════════════════════════════════
  // 颜色 —— 照客户端 console.css
  // ══════════════════════════════════════════════════════════

  /// 墨色. 深色下 #fcfcfc, 浅色下 #141414 —— **只有这一个开关**.
  /// 客户端两套主题看着是同一个东西, 靠的就是这个:
  /// "深色一套灰、浅色再调一套灰"永远对不齐
  static const _inkDark = Color(0xFFFCFCFC);
  static const _inkLight = Color(0xFF141414);

  static bool _dark = true;
  static void setDark(bool v) => _dark = v;
  static bool get isDark => _dark;
  static Color get _ink => _dark ? _inkDark : _inkLight;

  /// ink(a) 墨色叠 alpha —— 界面里所有的灰都从这儿来
  static Color ink(double a) => _ink.withValues(alpha: a);

  static Color get bg => _dark ? const Color(0xFF070707) : const Color(0xFFFCFCFC);
  static Color get bgSubtle => _dark ? const Color(0xFF111111) : const Color(0xFFF7F7F7);
  static Color get bgElevated => _dark ? const Color(0xFF181818) : const Color(0xFFFFFFFF);

  /// 四档字色. **只有四档** —— 第五档灰的存在只会让人纠结用哪个
  static Color get text => ink(1);
  static Color get text2 => ink(.6);
  static Color get text3 => ink(.4);
  static Color get text4 => ink(.26);

  static Color get line => ink(.06);
  static Color get line2 => ink(.11);
  static Color get fill => ink(.06);
  static Color get fill2 => ink(.10);

  /// accent 只在两处出现: 发送键、"在忙"那个点.
  /// 到处都是的强调色等于没有强调色
  static Color get accent => const Color(0xFF1084FE);
  /// ok 在线.
  ///
  /// 浅色下用更深的一档: #009957 在浅灰卡片上是**整屏唯一一块高饱和**,
  /// 而这套界面除了它只有青和一点蓝 —— 一个亮绿点上去,
  /// 眼睛先看见的是那块绿
  static Color get ok => _dark ? const Color(0xFF00C972) : const Color(0xFF0B7A4C);
  static Color get warn => _dark ? const Color(0xFFFF9800) : const Color(0xFFC27400);
  static Color get bad => _dark ? const Color(0xFFFF5667) : const Color(0xFFC21D2E);

  /// 核心那一族的青 —— 粒子球、空态插画、核心页的状态字.
  /// **它跟 accent 是两个世界**: accent 是"能操作的东西",
  /// 青是"它自己的生命迹象". 混用的话两个意思就都没了
  static Color get cyan =>
      _dark ? const Color(0xFF2FE0DA) : const Color(0xFF128C93);

  // ══════════════════════════════════════════════════════════
  // 字阶 —— 六档, 整数, 比例约 1.2
  // ══════════════════════════════════════════════════════════
  //
  //	28  display  页面大标题(一屏一个)
  //	20  title    弹层标题
  //	17  heading  会话名 · 列表项名字
  //	15  body     正文 · 消息 · 设置行的标题
  //	13  label    右值 · 摘要 · 按钮
  //	12  caption  分组标题 · 时间 · 说明
  //
  // **相邻两档差 2–3pt, 一眼分得出**.
  //
  // ── 为什么使用 28/20/17/15 而不是 32/22/18/16 ──
  //
  // 整体放大字号来增强可读性会破坏页面比例: display 32
  // 配 s6/s7 的留白, 一个"设置"两个字占掉屏幕上沿一大块, 而下面
  // 那些卡片显得挤, 使页面比例不协调.
  //
  // 不协调的来源不是正文, 是**大标题跟内容的落差**: 32 对 16 是
  // 两倍, 眼睛会把标题当成一个独立的区块而不是这一页的名字.
  // 28 对 15 差不到一倍, 才读得像同一页上的东西.
  static const fDisplay = 28.0;
  static const fTitle = 20.0;
  static const fHeading = 17.0;
  static const fBody = 15.0;
  static const fLabel = 13.0;
  static const fCaption = 12.0;

  /// 字重也只有三档.
  ///
  /// 中文字面本来就密, w700 在小字号上会糊成一团;
  /// 而 w500 和 w600 并存是上一版的病 —— 那两档在屏幕上分不出来
  static const wReg = FontWeight.w400;
  static const wMed = FontWeight.w500;
  static const wBold = FontWeight.w600;

  // ── 成品字样 ──
  //
  // 页面里只引这些, 不自己拼 TextStyle: 拼的那一刻就多了一档

  // ── 一条中文排版的硬规矩: 不许有字距 ──
  //
  // 上一版给大标题设了 -.5 的 letterSpacing, 给分组标题设了 +.3.
  // 那是**拉丁字体的规矩**: 西文大字号要收紧、小字号标签要放开.
  //
  // 汉字是等宽的方块, 本来就自带字面留白:
  //	负字距 → 笔画贴到一起, 「设置」两个字会粘住
  //	正字距 → 一行字散架, 看着像被强行拉开
  //
  // 中英混排时更糟: 同一行里英文按 -.5 收、汉字也跟着收,
  // 于是汉字先垮. 所以一律 0 —— 要调节奏就调字号和行高.
  static TextStyle get display => TextStyle(
      fontSize: fDisplay, height: 1.2, color: text, fontWeight: wBold);
  static TextStyle get title => TextStyle(
      fontSize: fTitle, height: 1.3, color: text, fontWeight: wBold);
  static TextStyle get heading => TextStyle(
      fontSize: fHeading, height: 1.3, color: text, fontWeight: wMed);
  /// headingOn 未读那种 —— 同一档字号, 靠字重变实
  static TextStyle get headingOn => TextStyle(
      fontSize: fHeading, height: 1.3, color: text, fontWeight: wBold);
  static TextStyle get body =>
      TextStyle(fontSize: fBody, height: 1.5, color: text);
  static TextStyle get bodyDim =>
      TextStyle(fontSize: fBody, height: 1.5, color: text2);
  static TextStyle get label =>
      TextStyle(fontSize: fLabel, height: 1.35, color: text2);
  static TextStyle get labelDim =>
      TextStyle(fontSize: fLabel, height: 1.35, color: text3);
  /// section 分组标题 —— 字号最小但字重最实, 它是标签不是句子
  static TextStyle get section => TextStyle(
      fontSize: fCaption, height: 1.2, color: text3, fontWeight: wMed);

  /// speaker 消息上方那个说话人的名字.
  ///
  /// 跟 section 同字号但**更实更亮**: 它是"这句话是谁说的",
  /// 一屏里要能被扫到; 而 section 是分组标签, 该退后.
  /// 上一版两个共用一个样式, 于是名字弱得像脚注
  static TextStyle get speaker => TextStyle(
      fontSize: fLabel - 1, height: 1.2, color: text2, fontWeight: wBold);
  static TextStyle get caption =>
      TextStyle(fontSize: fCaption, height: 1.5, color: text3);
  static TextStyle get captionDim =>
      TextStyle(fontSize: fCaption, height: 1.5, color: text4);

  // ══════════════════════════════════════════════════════════
  // 间距 —— 4 的倍数, 七档
  // ══════════════════════════════════════════════════════════
  //
  // 上一版几乎用遍了每一个整数(1,2,3,4,6,7,9,10,11,12,13,14,15,16,18,20,26,32,48).
  // 那不是节奏, 是随手. 4 的倍数保证任何两块东西的边总能对上

  static const s1 = 4.0;
  static const s2 = 8.0;
  static const s3 = 12.0;
  static const s4 = 16.0;
  static const s5 = 20.0;
  // s6 从 28 收到 24: 分区之间 28 的空隙配上 12 的分组标题,
  // 看着像每一组各占一屏 —— 一页只塞得下三组
  static const s6 = 24.0;
  static const s7 = 36.0;

  /// 屏幕左右的槽. **全 App 只有这一个值** ——
  /// 各页各定的话, 切页时内容会横向跳一下
  static const gutter = s5;

  // ══════════════════════════════════════════════════════════
  // 圆角 —— 四档
  // ══════════════════════════════════════════════════════════
  //
  //	rSm   6   小标签 · 分段器里那格
  //	rMd   12  卡片 · 面板 · 按钮
  //	rLg   18  气泡 · 输入条 · 弹层
  //	rPill 999 圆的
  //
  // 上一版八种圆角, 其中 13 和 12、19 和 18 各自都分不出来 ——
  // 而卡片里套一个半径不同的小块, 眼睛立刻觉得"这两个东西没关系"
  static const rSm = 6.0;
  static const rMd = 12.0;
  static const rLg = 18.0;
  static const rPill = 999.0;

  // ══════════════════════════════════════════════════════════
  // 定死的尺寸
  // ══════════════════════════════════════════════════════════

  /// 顶栏高度 —— 跟客户端的会话头同高
  static const headH = 52.0;
  /// 列表项最矮多高. 一行名字 + 一行摘要 + 上下 s3
  static const rowH = 68.0;
  /// 设置行最矮多高.
  ///
  /// 56 → 48: 大多数行只有一行字, 56 的高度让它上下各空一截 ——
  /// 一屏本来能放八行的地方只放了六行
  static const cellH = 48.0;
  /// 圆钮 —— **同时也是输入条那颗胶囊的高度**.
  ///
  /// 一个值管三块(加号 · 输入框 · 麦克风), 它们才可能一样高;
  /// 各写各的话必然差几个像素, 而一排差几像素的东西比差很多更难看
  static const btn = 40.0;
  /// 列表里的头像
  static const avatarLg = 46.0;
  /// 会话里的头像
  static const avatarSm = 32.0;

  static ThemeData theme(bool dark) {
    setDark(dark);
    return ThemeData(
      brightness: dark ? Brightness.dark : Brightness.light,
      scaffoldBackgroundColor: bg,
      canvasColor: bg,
      colorScheme: ColorScheme.fromSeed(
        seedColor: accent,
        brightness: dark ? Brightness.dark : Brightness.light,
      ).copyWith(surface: bg, primary: accent),
      textSelectionTheme: TextSelectionThemeData(
        cursorColor: accent,
        selectionColor: accent.withValues(alpha: .3),
        selectionHandleColor: accent,
      ),
    );
  }
}
