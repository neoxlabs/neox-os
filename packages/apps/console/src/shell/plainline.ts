/**
 * plainline — 把一段回话压成**侧栏那一行**.
 *
 *   ── 为什么不能直接截 ──
 *
 *   bot 说的话是 markdown, 里面有换行、标题、列表、代码引号. 直接截到
 *   侧栏那一行, 用户看到的是:
 *
 *	完成了：\n\n- `g.txt` 写入 `...
 *	小勤: 考勤模块做完并跑通了。交付内容：\n\n**
 *
 *   —— 换行符和 markdown 记号原样露在外面. 那不是"摘要", 那是把源码
 *   贴到了列表里.
 *
 *   ── 只做减法, 不做改写 ──
 *
 *   这里**不重写句子、不提取要点**: 那需要判断, 而判断会出错, 出错的
 *   摘要比原文更糟(它看起来像 bot 说过这句话). 只把记号去掉、把空白压平,
 *   剩下的仍然是它自己说的字.
 */

/** 侧栏一行最多留多少字 —— 再多也被省略号吃掉, 白算 */
const MAX = 80;

export function plainLine(text: string): string {
  let out = text;
  // 代码块整段去掉: 它在一行里没有任何可读性
  out = out.replace(/```[\s\S]*?```/g, " ");
  // 表格行整行去掉 —— 一行 | a | b | 在侧栏里就是一串竖线
  out = out.replace(/^\s*\|.*\|\s*$/gm, " ");
  // 标题记号、列表记号、引用记号: 去掉记号**留下文字**
  out = out.replace(/^\s{0,3}#{1,6}\s+/gm, "");
  out = out.replace(/^\s{0,3}[-*+]\s+/gm, "");
  out = out.replace(/^\s{0,3}\d+[.)]\s+/gm, "");
  out = out.replace(/^\s{0,3}>\s?/gm, "");
  // 粗体/行内代码: 只脱掉记号
  out = out.replace(/\*\*([^*]+)\*\*/g, "$1");
  out = out.replace(/`([^`]+)`/g, "$1");
  // 所有空白(含换行)压成一个空格 —— 这一行本来就只有一行
  out = out.replace(/\s+/g, " ").trim();
  return out.length > MAX ? out.slice(0, MAX) : out;
}
