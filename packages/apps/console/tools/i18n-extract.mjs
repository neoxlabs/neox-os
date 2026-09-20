/**
 * 用 TypeScript 的 AST 把控制台里**用户看得见的中文**逐条列出来 —— 位置、类型、原文。
 * 输出 JSON, 给本地化用: 字典的键就是这里的原文, 替换按这里的位置做。
 * 只看 src/shell 与 src/view 里的 .ts/.tsx, 跳过测试、注释、import 路径。
 */
import ts from "typescript";
import { readFileSync, writeFileSync } from "node:fs";
import { globSync } from "node:fs";
const CJK = /[一-鿿]/;
const files = [...globSync("src/shell/**/*.{ts,tsx}"), ...globSync("src/view/**/*.{ts,tsx}"), ...globSync("src/os/**/*.ts")]
  .filter((f) => !/\.test\.|__tests__/.test(f));
const out = [];
for (const file of files) {
  const src = readFileSync(file, "utf8");
  const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true, file.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const push = (node, kind, text) => {
    if (!CJK.test(text)) return;
    const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
    out.push({ file, line: line + 1, kind, text });
  };
  const walk = (n) => {
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) push(n, "str", n.text);
    else if (ts.isTemplateExpression(n)) push(n, "tpl", n.getText());
    else if (ts.isJsxText(n)) { const t = n.getText().trim(); if (t) push(n, "jsx", t); }
    ts.forEachChild(n, walk);
  };
  walk(sf);
}
writeFileSync("tools/i18n-inventory.json", JSON.stringify(out, null, 1));
const byFile = {}; for (const o of out) byFile[o.file] = (byFile[o.file] ?? 0) + 1;
console.log("条目", out.length, "文件", Object.keys(byFile).length);
console.log(Object.entries(byFile).sort((a, b) => b[1] - a[1]).slice(0, 8).map(([f, n]) => `${n}\t${f}`).join("\n"));
console.log("按类型:", JSON.stringify(out.reduce((a, o) => (a[o.kind] = (a[o.kind] ?? 0) + 1, a), {})));
