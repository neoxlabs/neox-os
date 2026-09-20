import 'package:flutter/material.dart';

import '../theme.dart';

/// 一个分区 —— 小字标题 + 一张 ghost 底的面板.
///
/// ── 为什么抽出来 ──
///
/// 设置页、bot 资料页、花费页、推理服务页……**每一页都要这个形状**.
/// 各写各的话必然漂 —— 这个仓库已经栽过一次了(设置行 _Row 和 _Tap
/// 两个组件画同一种行, 于是内边距、图标对齐、值放哪儿全都不一样).
///
/// 一处定义, 所有页面引它
class Sec extends StatelessWidget {
  const Sec({
    super.key,
    required this.label,
    required this.children,
    this.foot,
    this.trailing,
  });

  final String label;
  final List<Widget> children;

  /// foot 面板底下那句说明. **只写一句** —— 设置页的字一多就没人看了,
  /// 而没人看的说明等于没写
  final String? foot;

  /// trailing 标题右边那个东西(通常是一颗小按钮)
  final Widget? trailing;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.only(bottom: NX.s6),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.only(bottom: NX.s2, left: NX.s1),
              child: Row(children: [
                Expanded(child: Text(label, style: NX.section)),
                if (trailing != null) trailing!,
              ]),
            ),
            if (children.isNotEmpty)
              Container(
                decoration: BoxDecoration(
                  color: NX.fill,
                  borderRadius: BorderRadius.circular(NX.rMd),
                ),
                clipBehavior: Clip.antiAlias,
                child: Column(children: [
                  for (var i = 0; i < children.length; i++) ...[
                    // 行与行之间一条**通到边**的线 —— 内缩的分隔线是
                    // iOS 列表的语言, 这里是面板
                    if (i > 0)
                      Divider(height: 1, thickness: 1, color: NX.line),
                    children[i],
                  ],
                ]),
              ),
            if (foot != null) ...[
              const SizedBox(height: NX.s2),
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: NX.s1),
                child: Text(foot!,
                    style: NX.caption.copyWith(color: NX.text4)),
              ),
            ],
          ],
        ),
      );
}

/// 面板里的一行.
///
/// 能不能点、右边挂什么、值长不长, 都是同一个组件的参数 ——
/// **两个组件画同一种行, 迟早会漂**
class Cell extends StatelessWidget {
  const Cell({
    super.key,
    required this.title,
    this.sub,
    this.note,
    this.noteColor,
    this.ctl,
    this.icon,
    this.iconColor,
    this.lead,
    this.onTap,
    this.mono = false,
    this.danger = false,
    this.acts = false,
  });

  final String title;
  final String? sub, note;
  final Color? noteColor, iconColor;
  final Widget? ctl, lead;

  /// icon 行首那枚图标. **不许有自己的底** ——
  /// 卡片里再套一层色块就是卡里的卡
  final IconData? icon;
  final VoidCallback? onTap;
  final bool mono;

  /// danger 会造成不可逆后果的那一行(删除). 整行变红, 而不是只有
  /// 一个红按钮 —— 红按钮容易被当成"主要动作"点下去
  final bool danger;

  /// acts 这一行点下去是**干一件事**, 不是进一页.
  ///
  ///	不挂箭头 —— 箭头的意思是"进去看看", 而它会让人以为点进去还有
  ///	一页可以反悔. danger 的行天然属于这一类
  final bool acts;

  @override
  Widget build(BuildContext context) {
    final tone = danger ? NX.bad : NX.text;
    final acts = this.acts || danger;
    final row = Padding(
      padding: const EdgeInsets.fromLTRB(NX.s4, NX.s3, NX.s4, NX.s3),
      child: Row(
        children: [
          if (lead != null) ...[lead!, const SizedBox(width: NX.s3)],
          if (icon != null) ...[
            // 定宽的图标位: 不定宽的话不同图标实际宽度不一样,
            // 一列文字的左边缘会参差
            SizedBox(
                width: 24,
                child: Icon(icon,
                    size: 20, color: iconColor ?? (danger ? NX.bad : NX.text3))),
            const SizedBox(width: NX.s3),
          ],
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                // ── 两行封顶 ──
                //
                //	提醒正文由模型生成, 可能是一大段文字; 这里不封顶的话
                //	整张卡会被撑成半屏, 一列事项翻不到底。
                //
                //	**要看全文点进去** —— 列表回答的是"有哪几件",
                //	不是"每件写了什么"
                Text(title,
                    style: NX.body.copyWith(color: tone),
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis),
                if (sub != null) ...[
                  const SizedBox(height: NX.s1),
                  // **一行封顶**: 折成两行会把行撑高, 右边控件跟着掉,
                  // 一列开关就参差了 —— 写不下是话太长
                  Text(sub!,
                      style: NX.caption,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis),
                ],
              ],
            ),
          ),
          if (note != null) ...[
            const SizedBox(width: NX.s4),
            // **不能用 Flexible**: 它默认 flex:1, 会跟标题那个 Expanded
            // 把整行对半分, 于是每行的值各停在各的地方
            ConstrainedBox(
              constraints: BoxConstraints(
                  maxWidth: MediaQuery.sizeOf(context).width * .42),
              child: Text(note!,
                  style: (mono ? NX.caption : NX.label).copyWith(
                      color: noteColor ?? NX.text3,
                      fontFamily: mono ? 'monospace' : null),
                  textAlign: TextAlign.right,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis),
            ),
          ],
          if (ctl != null) ...[const SizedBox(width: NX.s3), ctl!],
          // ── 箭头是"进去看看", 不是"点了会发生事" ──
          //
          //	所以**动手的那种行不挂箭头**: 退出、删掉、停掉 —— 它们不是
          //	导航, 而箭头会让人以为点进去还有一页可以反悔.
          //	退出这一行如果挂着箭头, 用户会以为点进去还有一页可以反悔,
          //	但点下去会直接登出.
          if (onTap != null && !acts) ...[
            const SizedBox(width: NX.s1),
            Icon(Icons.chevron_right_rounded, size: 20, color: NX.text4),
          ],
        ],
      ),
    );
    if (onTap == null) return row;
    return Material(
        color: Colors.transparent,
        child: InkWell(onTap: onTap, child: row));
  }
}

/// 一页的标题栏 —— 返回 + 大标题.
///
/// 二级页统一用它: 各写各的话, 返回键的位置会一页一个样
class PageBar extends StatelessWidget {
  const PageBar({super.key, required this.title, this.action});
  final String title;
  final Widget? action;

  @override
  Widget build(BuildContext context) => Padding(
        padding: EdgeInsets.only(top: MediaQuery.of(context).padding.top),
        child: SizedBox(
          height: NX.headH,
          child: Row(children: [
            IconButton(
              onPressed: () => Navigator.of(context).pop(),
              icon: const Icon(Icons.arrow_back_ios_new_rounded, size: 20),
              color: NX.text2,
            ),
            Expanded(child: Text(title, style: NX.heading)),
            if (action != null) action!,
            const SizedBox(width: NX.s2),
          ]),
        ),
      );
}
