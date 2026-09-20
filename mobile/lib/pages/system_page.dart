import 'package:flutter/material.dart';

import '../api/os_client.dart';
import '../state/app_state.dart';
import '../theme.dart';
import '../widgets/panel.dart';

/// 这台机器 —— 推理服务 · 审批口径 · 工具链自检.
///
/// ── 为什么合成一页 ──
///
/// 这三件事回答的是同一个问题: **这台 OS 现在能干什么**.
/// 分成三页的话用户要点三次才拼得出一个完整印象, 而它们本来就是
/// 一起看的: 没接推理 = 不会说话; 没有 git = 干不了活;
/// 审批口径太严 = 什么都要问你.
class SystemPage extends StatefulWidget {
  const SystemPage({super.key, required this.state});
  final AppState state;

  @override
  State<SystemPage> createState() => _SystemPageState();
}

class _SystemPageState extends State<SystemPage> {
  Health? _health;
  Provider? _provider;
  List<ToolInfo>? _tools;
  String? _policy;
  String? _err;
  /// _busy 正在写设置 —— 写着的时候把开关按住, 不然连点两下会有两笔覆盖
  bool _busy = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final c = widget.state.client;
    if (c == null) {
      setState(() => _err = '还没连上');
      return;
    }
    // 四个接口各查各的, **一个失败不该拖垮其余三个** ——
    // 有的 OS 不开放某一项(501), 那一格显示"不支持"就行
    Future<T?> soft<T>(Future<T> f) async {
      try {
        return await f;
      } catch (_) {
        return null;
      }
    }

    final r = await Future.wait([
      soft(c.health()),
      soft(c.provider()),
      soft(c.toolchain()),
      soft(c.policy()),
    ]);
    if (!mounted) return;
    setState(() {
      _health = r[0] as Health?;
      _provider = r[1] as Provider?;
      _tools = r[2] as List<ToolInfo>?;
      _policy = r[3] as String?;
      _err = null;
    });
  }

  @override
  Widget build(BuildContext context) {
    final tools = _tools ?? const <ToolInfo>[];
    final missing = tools.where((t) => t.core && !t.have).toList();
    return Scaffold(
      backgroundColor: NX.bg,
      body: Column(children: [
        PageBar(
          title: '这台机器',
          action: IconButton(
            onPressed: _load,
            icon: const Icon(Icons.refresh_rounded, size: 20),
            color: NX.text3,
          ),
        ),
        Expanded(
          child: _err != null
              ? Center(child: Text(_err!, style: NX.bodyDim))
              : ListView(
                  padding: const EdgeInsets.fromLTRB(
                      NX.gutter, NX.s3, NX.gutter, NX.s7),
                  children: [
                    // ── 推理服务 ──
                    //
                    // 放最前面, 因为**没接的话别的都是空谈**:
                    // bot 会直说"这台机器没接推理服务"
                    Sec(
                      label: '推理服务',
                      foot: 'key 只在这台 OS 手里，手机这边拿不到',
                      children: _provider == null
                          ? [const Cell(title: '问不到', note: '这台 OS 不开放')]
                          : [
                              Cell(
                                icon: Icons.link_rounded,
                                title: '地址',
                                note: _provider!.baseUrl
                                    .replaceFirst(RegExp(r'^https?://'), ''),
                                mono: true,
                              ),
                              Cell(
                                icon: Icons.memory_rounded,
                                title: '模型',
                                note: _provider!.model,
                                mono: true,
                              ),
                              Cell(
                                icon: Icons.vpn_key_rounded,
                                title: 'API Key',
                                note: _provider!.hasKey
                                    ? '已配置 · 来自${_provider!.keyFrom == "env" ? "环境变量" : "文件"}'
                                    : '没配',
                                noteColor:
                                    _provider!.hasKey ? NX.ok : NX.bad,
                              ),
                              // ── 先想再答 ──
                              //
                              //	**缺省关 —— 要的是快**。量过：同一句
                              //	「1+1=?」关掉 completion 是 1 个 token，
                              //	开着 34–40，那三十几个 token 是要等的时间。
                              //	而助理绝大多数时候在做的是「提醒我 5:30
                              //	打卡」这种事，想不想都是同一个答案。
                              // ── 搜网 ──
                              //
                              //	**先看供应商自己带不带**：DeepSeek 是带的，
                              //	但只在 Anthropic Messages 那条口上
                              //	（/chat/completions 传 web_search 直接 400，
                              //	/responses 接受但一次都不搜）。带的话这里
                              //	不用配，用的是推理那把 key。
                              //
                              //	都没有的话 web_search 这个工具根本不挂出来，
                              //	它会照实说搜不了 —— 那比调一个必然失败的
                              //	工具然后编一个答案强。
                              Cell(
                                icon: Icons.travel_explore_rounded,
                                title: '搜网',
                                sub: _provider!.searchOn
                                    ? (_provider!.searchApi.isEmpty
                                        ? '自定义接口'
                                        : _provider!.searchApi)
                                    : _provider!.searchNative
                                        ? '这家自己会搜，用推理那把 key'
                                        : '没配 —— 它查不到的东西会照实说搜不了',
                                noteColor: _provider!.searchOn ||
                                        _provider!.searchNative
                                    ? NX.ok
                                    : NX.text4,
                                note: _provider!.searchOn
                                    ? '已配'
                                    : _provider!.searchNative
                                        ? '自带'
                                        : '去配',
                                onTap: _setSearch,
                              ),
                              Cell(
                                icon: Icons.psychology_outlined,
                                title: '先想再答',
                                sub: '关着更快',
                                ctl: Switch.adaptive(
                                  value: _provider!.think,
                                  onChanged: _busy ? null : _setThink,
                                ),
                              ),
                            ],
                    ),

                    // ── 审批口径 ──
                    Sec(
                      label: '越界了要不要问你',
                      foot: '越界是内核不让，不是它不肯 —— 批了才接着干',
                      children: [
                        for (final m in const [
                          ('never', '从不问', '越界直接失败。最省事也最容易卡住'),
                          ('ask', '问我', '每次都弹给你拍板'),
                          ('always', '一律放行', '危险 —— 等于没有边界'),
                        ])
                          Cell(
                            icon: _policy == m.$1
                                ? Icons.radio_button_checked_rounded
                                : Icons.radio_button_unchecked_rounded,
                            iconColor: _policy == m.$1 ? NX.accent : NX.text4,
                            title: m.$2,
                            sub: m.$3,
                            onTap: _policy == null || _busy
                                ? null
                                : () => _setPolicy(m.$1),
                          ),
                      ],
                    ),

                    // ── 工具链 ──
                    //
                    // **这台 OS 派得动多少活, 取决于它脚下有什么**.
                    // 缺哪个要点名 —— 只说"工具不全"的话, 用户不知道
                    // 该装什么
                    Sec(
                      label: '它手上有什么',
                      trailing: missing.isEmpty
                          ? null
                          : Text('缺 ${missing.length} 件要紧的',
                              style: NX.caption.copyWith(color: NX.warn)),
                      foot: null,
                      children: tools.isEmpty
                          ? [const Cell(title: '问不到', note: '这台 OS 不开放')]
                          : [
                              for (final t in tools)
                                Cell(
                                  icon: t.have
                                      ? Icons.check_circle_rounded
                                      : (t.core
                                          ? Icons.error_rounded
                                          : Icons.remove_circle_outline_rounded),
                                  iconColor: t.have
                                      ? NX.ok
                                      : (t.core ? NX.warn : NX.text4),
                                  title: t.name,
                                  // **有的时候不说话**: 一列十几件工具,
                                  // 每件都跟一句"拿它干什么"就成了一篇说明书。
                                  // 缺的那几件才需要解释, 因为你要决定装不装
                                  sub: t.have ? null : t.what,
                                  note: t.have ? '' : (t.core ? '要紧' : '可选'),
                                  noteColor: t.core ? NX.warn : NX.text4,
                                ),
                            ],
                    ),

                    // ── 运行态 ──
                    Sec(label: '运行态', children: [
                      Cell(
                        icon: Icons.bolt_rounded,
                        title: '推理',
                        note: _health?.inference == true ? '接上了' : '没接',
                        noteColor:
                            _health?.inference == true ? NX.ok : NX.bad,
                      ),
                      Cell(
                        icon: Icons.shield_rounded,
                        title: '边界强制',
                        // enforced=false 要如实说 —— macOS 上会退化成
                        // 只拦写, 而"以为拦住了其实没拦"是最坏的一种
                        sub: _health?.enforced == true
                            ? '内核在管'
                            : '没在内核层强制（开发模式）',
                        note: _health?.mode ?? '?',
                        noteColor:
                            _health?.enforced == true ? NX.ok : NX.warn,
                      ),
                    ]),
                  ],
                ),
        ),
      ]),
    );
  }

  Future<void> _setPolicy(String mode) async {
    setState(() => _busy = true);
    try {
      await widget.state.client!.setPolicy(mode);
      if (mounted) setState(() => _policy = mode);
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
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// 配搜网 —— 挑一家，或者填一个自定义地址。
  ///
  ///	**key 空着就是不改**：界面永远拿不到它（只进不出），而空不该
  ///	被理解成"删掉"。
  Future<void> _setSearch() async {
    final p = _provider;
    final c = widget.state.client;
    if (p == null || c == null) return;
    const known = {
      'bocha': '博查（国内）',
      'tavily': 'Tavily',
      'serper': 'Serper（Google）',
      'brave': 'Brave',
      '': '自定义接口',
    };
    final api = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: NX.bgElevated,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(NX.rLg))),
      builder: (c2) => SafeArea(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(NX.s5, NX.s5, NX.s5, NX.s2),
            child: Align(
              alignment: Alignment.centerLeft,
              child: Text('用哪家搜', style: NX.heading),
            ),
          ),
          for (final e in known.entries)
            ListTile(
              leading: Icon(
                  e.key == p.searchApi
                      ? Icons.radio_button_checked_rounded
                      : Icons.radio_button_off_rounded,
                  size: 20,
                  color: e.key == p.searchApi ? NX.accent : NX.text4),
              title: Text(e.value, style: NX.body),
              onTap: () => Navigator.pop(c2, e.key),
            ),
        ]),
      ),
    );
    if (api == null || !mounted) return;

    var url = p.searchUrl;
    if (api.isEmpty) {
      url = await _ask('搜索接口地址',
              'https://api.example.com/search', p.searchUrl) ??
          p.searchUrl;
      if (!mounted) return;
    }
    final key = await _ask(
        '搜索 key', p.searchOn ? '已设置 —— 留空就不改' : '粘进来', '') ?? '';
    if (!mounted) return;

    final next = p.copyWith(searchApi: api, searchUrl: url);
    setState(() {
      _provider = next;
      _busy = true;
    });
    try {
      await c.setProvider(next, searchKey: key);
      final got = await c.provider();
      if (mounted) setState(() => _provider = got);
    } catch (e) {
      if (mounted) {
        setState(() => _provider = p);
        ScaffoldMessenger.of(context)
          ..hideCurrentSnackBar()
          ..showSnackBar(SnackBar(
            content: Text('$e', style: NX.body.copyWith(color: Colors.white)),
            backgroundColor: NX.bad,
            behavior: SnackBarBehavior.floating,
          ));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  /// 问一句. 取消返回 null —— **跟"填了空"分得开**
  Future<String?> _ask(String title, String hint, String initial) async {
    final ctl = TextEditingController(text: initial);
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        backgroundColor: NX.bgElevated,
        shape:
            RoundedRectangleBorder(borderRadius: BorderRadius.circular(NX.rLg)),
        title: Text(title, style: NX.heading),
        content: TextField(
          controller: ctl,
          autofocus: true,
          style: NX.body,
          decoration: InputDecoration(
            hintText: hint,
            hintStyle: NX.body.copyWith(color: NX.text3),
            filled: true,
            fillColor: NX.fill,
            border: OutlineInputBorder(
              borderRadius: BorderRadius.circular(NX.rMd),
              borderSide: BorderSide.none,
            ),
          ),
        ),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(c, false),
              child: Text('取消', style: NX.label.copyWith(color: NX.text2))),
          TextButton(
              onPressed: () => Navigator.pop(c, true),
              child: Text('好',
                  style:
                      NX.label.copyWith(color: NX.accent, fontWeight: NX.wMed))),
        ],
      ),
    );
    return ok == true ? ctl.text.trim() : null;
  }

  /// 开关「先想再答」.
  ///
  ///	**先画上去再发**：手机的网这一下经常好几百毫秒，等回来才动的话
  ///	用户会以为没点上，然后再点一次。失败了再翻回去。
  Future<void> _setThink(bool on) async {
    final p = _provider;
    final c = widget.state.client;
    if (p == null || c == null) return;
    setState(() {
      _provider = p.copyWith(think: on);
      _busy = true;
    });
    try {
      await c.setProvider(p.copyWith(think: on));
    } catch (e) {
      if (mounted) {
        setState(() => _provider = p);
        ScaffoldMessenger.of(context)
          ..hideCurrentSnackBar()
          ..showSnackBar(SnackBar(
            content: Text('$e', style: NX.body.copyWith(color: Colors.white)),
            backgroundColor: NX.bad,
            behavior: SnackBarBehavior.floating,
          ));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }
}
