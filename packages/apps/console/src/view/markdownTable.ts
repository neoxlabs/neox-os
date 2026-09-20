/**
 * markdownTable — 「这几行是不是一张表」这个判断.
 *
 *   **单独一个文件是为了能测**: 判据(什么算表、列数对不上怎么办)
 *   是这块唯一会出错的地方, 而它一旦裹在 JSX 里就只能靠肉眼看.
 *   渲染那一侧不做判断, 只把这里的结果摆出来.
 */

/** 一张表: 表头、对齐、正文, 以及它到哪一行为止 */
export interface TableBlock {
  readonly head: readonly string[];
  readonly align: readonly ("left" | "center" | "right")[];
  readonly body: readonly (readonly string[])[];
  readonly next: number;
}

/**
 * takeTable 从 at 这一行开始是不是一张表. 不是就返回 null.
 *
 *   **判据是分隔线, 不是竖线**: 正文里带一根竖线的句子太常见了
 *   (比如 `a | b`), 拿"这行有 |"当判据会把普通段落吃成表格.
 *   分隔线那一行几乎不可能是别的东西.
 */
export function takeTable(lines: readonly string[], at: number): TableBlock | null {
  const header = lines[at];
  const divider = lines[at + 1];
  if (header === undefined || divider === undefined) return null;
  if (!header.includes("|")) return null;
  const align = dividerAlign(divider);
  if (align === null) return null;

  const head = splitRow(header);
  // 表头和分隔线的列数对不上 = 多半不是表. 宁可当段落原样显示,
  // 也不要渲染出一张错位的表 —— 错位的表比字面量更难看懂
  if (head.length !== align.length) return null;

  const body: string[][] = [];
  let index = at + 2;
  while (index < lines.length) {
    const line = lines[index] ?? "";
    if (line.trim() === "" || !line.includes("|")) break;
    const cells = splitRow(line);
    // 少了的补空, 多了的截掉 —— 模型偶尔会漏一根竖线,
    // 为这个把整张表退回字面量不值得
    while (cells.length < head.length) cells.push("");
    body.push(cells.slice(0, head.length));
    index += 1;
  }
  return { head, align, body, next: index };
}

function splitRow(line: string): string[] {
  let text = line.trim();
  if (text.startsWith("|")) text = text.slice(1);
  if (text.endsWith("|")) text = text.slice(0, -1);
  return text.split("|").map((cell) => cell.trim());
}

function dividerAlign(line: string): ("left" | "center" | "right")[] | null {
  const cells = splitRow(line);
  if (cells.length === 0) return null;
  const out: ("left" | "center" | "right")[] = [];
  for (const cell of cells) {
    if (!/^:?-{1,}:?$/.test(cell)) return null;
    const left = cell.startsWith(":");
    const right = cell.endsWith(":");
    out.push(left && right ? "center" : right ? "right" : "left");
  }
  return out;
}
