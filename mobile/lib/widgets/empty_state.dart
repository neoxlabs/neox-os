import 'package:flutter/material.dart';

import '../theme.dart';

/// 空态 —— 一张插画 + 一句话 + 一句"接下来干什么".
///
/// ── 空屏是设计里最容易被糊弄的一块 ──
///
/// 「暂无数据」四个字什么都没说. 用户在这一刻要知道的是**接下来该
/// 干什么**, 而那取决于它到底卡在哪一步 —— 是没连上、token 不对、
/// 还是这台机器上真的一个 bot 都没有.
///
/// ── 插画为什么要有 ──
///
/// 不是为了好看. 一屏只有两行小灰字的时候, 那一屏看着像**没加载完**;
/// 而一个明确的图形告诉人"这就是它现在的样子, 不是还在转圈".
///
/// 三张图是同一族(点阵球 + 细线 + 青色), 跟核心页那颗球同一个世界 ——
/// 各画各的风格才是真正的五花八门
class EmptyState extends StatelessWidget {
  const EmptyState({
    super.key,
    required this.art,
    required this.line,
    this.hint,
    this.action,
  });

  /// art assets/empty_*.png
  final String art;
  final String line;
  final String? hint;
  final Widget? action;

  @override
  Widget build(BuildContext context) => Center(
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: NX.s7),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              // ── 深色下不许再压透明度 ──
              //
              // 透明度设为 .55 时, 那张线稿在纯黑底上几乎看不见 ——
              // 它本来就是 1px 的细线, 再打五折就只剩一层灰雾.
              //
              // "插画不该比正文响"这条是对的, 但**控制响度的手段是
              // 尺寸和颜色, 不是把它调透明**: 调透明的结果不是"轻",
              // 是"糊".
              Image.asset(art,
                  width: 168,
                  height: 168,
                  fit: BoxFit.contain,
                  // 浅色主题下那张青线稿在白底上偏浅, 压一点让它站住
                  opacity: AlwaysStoppedAnimation(NX.isDark ? 1.0 : .85),
                  filterQuality: FilterQuality.medium),
              const SizedBox(height: NX.s5),
              Text(line, style: NX.bodyDim, textAlign: TextAlign.center),
              if (hint != null) ...[
                const SizedBox(height: NX.s2),
                Text(hint!,
                    style: NX.caption.copyWith(color: NX.text4),
                    textAlign: TextAlign.center),
              ],
              if (action != null) ...[
                const SizedBox(height: NX.s5),
                action!,
              ],
            ],
          ),
        ),
      );
}

/// 空态里那颗按钮 —— 唯一的出路要看得见能点
class EmptyAction extends StatelessWidget {
  const EmptyAction({super.key, required this.label, required this.onTap});
  final String label;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => FilledButton(
        onPressed: onTap,
        style: FilledButton.styleFrom(
          backgroundColor: NX.accent,
          foregroundColor: Colors.white,
          padding: const EdgeInsets.symmetric(
              horizontal: NX.s5, vertical: NX.s3),
          shape: RoundedRectangleBorder(
              borderRadius: BorderRadius.circular(NX.rMd)),
        ),
        child: Text(label,
            style: const TextStyle(
                fontSize: NX.fLabel, fontWeight: NX.wMed)),
      );
}
