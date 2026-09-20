/**
 * diffline — diff 卡里每一行是加、是减、还是别的.
 *
 *   ── 为什么要分 ──
 *
 *   diff 卡原来是一整个 <pre>, 加的减的一个颜色 —— 而"哪几行变了"
 *   正是这张卡存在的**唯一理由**. 一眼看不出来的话, 它跟一段普通代码
 *   没有区别, 而且比代码更难读(多了一列 +/- 干扰).
 *
 *   ── 为什么不能只看第一个字符 ──
 *
 *   统一 diff 的**文件头**也以 - 和 + 开头:
 *
 *       --- a/app/routes/attendance.py
 *       +++ b/app/routes/attendance.py
 *       @@ -87,3 +87,3 @@
 *
 *   把它们染成"删了一行 / 加了一行", 等于在最显眼的位置说了两句假话.
 */
export type DiffKind = "add" | "del" | "meta" | "same";

export function diffKind(line: string): DiffKind {
  if (line.startsWith("+++") || line.startsWith("---")) return "meta"; // 文件头, 不是加减
  if (line.startsWith("@@")) return "meta";                            // 位置头
  if (line.startsWith("diff ") || line.startsWith("index ")) return "meta";
  if (line.startsWith("+")) return "add";
  if (line.startsWith("-")) return "del";
  return "same";
}

export function diffLines(text: string): readonly { readonly kind: DiffKind; readonly text: string }[] {
  return text.split("\n").map((line) => ({ kind: diffKind(line), text: line }));
}
