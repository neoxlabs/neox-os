#!/usr/bin/env node
/**
 * 把控制台里用户看得见的中文字面量包成 t("…") / tt("…", {…}) —— 用 TypeScript 的
 * AST 定位, 不用正则: 正则分不清 "这是一句话" 和 case "已批准" 的区别, 而后者
 * 一包就把 switch 改坏了。
 *
 *   跑法:  node tools/i18n-codemod.mjs [--dry-run] [文件…]
 *   幂等:  已经在 t()/tt() 里的字面量不再包; 重跑不会包两层。
 *
 * 规则 (每一条都有一个不这么做就会翻车的理由):
 *   · 只碰含 CJK 的字面量; 只碰 src/shell src/view src/os; 跳过测试、glyphs.tsx
 *     (那是卡片种类的识别词库, 翻了就认不出中文卡了) 和 i18n/ 自己。
 *   · 跳过 import/export 的模块名、对象键、JSX 属性名、case 标签、=== / !== 两侧 ——
 *     这些是程序在比对的值, 不是给人看的话。
 *   · JSX 属性值 title="…" 要变成 title={t("…")}, 不然引号里只是一串字符。
 *   · JSX 文本 (标签之间的字) 变成 {t("…")}; 键按**压成单空格**的文本取 ——
 *     源码里为了排版换的行不该进字典。
 *   · 带 ${} 的模板变成 tt("… {a} …", { a: 表达式 }), 占位符按出现顺序叫 a/b/c。
 *     嵌套模板从里往外, 多跑几遍直到没有可改的 (每遍重新解析, 位置才对)。
 */
import ts from "typescript";
import { readFileSync, writeFileSync, globSync } from "node:fs";
import path from "node:path";

const DRY = process.argv.includes("--dry-run");
const ONLY = process.argv.slice(2).filter((a) => !a.startsWith("--"));
const CJK = /[一-鿿]/;
const FILES = (ONLY.length ? ONLY : [
  ...globSync("src/shell/**/*.{ts,tsx}"), ...globSync("src/view/**/*.{ts,tsx}"), ...globSync("src/os/**/*.ts"),
]).filter((f) => !/\.test\.|__tests__|glyphs\.tsx|\/i18n\//.test(f));

const q = (s) => JSON.stringify(s);
const norm = (s) => s.replace(/\s+/g, " ").trim();

/** 这个字面量是不是"程序在比对的值", 不是给人看的话 */
function isCode(node) {
  const p = node.parent;
  if (!p) return true;
  if (ts.isImportDeclaration(p) || ts.isExportDeclaration(p) || ts.isExternalModuleReference(p)) return true;
  if (ts.isPropertyAssignment(p) && p.name === node) return true;
  if (ts.isPropertySignature(p) || ts.isLiteralTypeNode(p) || ts.isEnumMember(p)) return true;
  if (ts.isCaseClause(p)) return true;
  if (ts.isBinaryExpression(p) && [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken,
      ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken].includes(p.operatorToken.kind)) return true;
  if (ts.isElementAccessExpression(p) && p.argumentExpression === node) return true;
  if (ts.isComputedPropertyName(p)) return true;
  return false;
}

/** 已经包在 t()/tt() 里了 */
function inT(node) {
  for (let p = node.parent; p; p = p.parent) {
    if (ts.isCallExpression(p) && ts.isIdentifier(p.expression) && (p.expression.text === "t" || p.expression.text === "tt")) return true;
  }
  return false;
}

/** 一个模板表达式的**表达式里**还有没有别的含中文的模板 —— 有就这遍先不动它 */
function hasInnerTemplate(node) {
  let found = false;
  const walk = (n) => {
    if (found) return;
    if (n !== node && (ts.isTemplateExpression(n) || ts.isNoSubstitutionTemplateLiteral(n)) && CJK.test(n.getText())) { found = true; return; }
    ts.forEachChild(n, walk);
  };
  node.templateSpans.forEach((s) => walk(s.expression));
  return found;
}

function pass(file, src) {
  const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true, file.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const edits = []; // { start, end, text }
  let needT = false, needTT = false;

  const walk = (n) => {
    if ((ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) && CJK.test(n.text) && !isCode(n) && !inT(n)) {
      const call = `t(${q(n.text)})`;
      needT = true;
      // JSX 属性值: title="…" → title={t("…")}
      if (n.parent && ts.isJsxAttribute(n.parent)) edits.push({ start: n.getStart(), end: n.getEnd(), text: `{${call}}` });
      else edits.push({ start: n.getStart(), end: n.getEnd(), text: call });
    } else if (ts.isJsxText(n) && CJK.test(n.getText()) && !inT(n)) {
      const raw = n.getText();
      const lead = raw.match(/^\s*/)[0], trail = raw.match(/\s*$/)[0];
      const body = raw.slice(lead.length, raw.length - trail.length);
      if (body) {
        needT = true;
        edits.push({ start: n.getStart() + lead.length, end: n.getEnd() - trail.length, text: `{t(${q(norm(body))})}` });
      }
    } else if (ts.isTemplateExpression(n) && CJK.test(n.getText()) && !isCode(n) && !inT(n) && !hasInnerTemplate(n)) {
      // `第 ${steps} 步` → tt("第 {a} 步", { a: steps })
      let key = n.head.text, vars = [];
      n.templateSpans.forEach((span, i) => {
        const name = String.fromCharCode(97 + i);           // a, b, c…
        key += `{${name}}` + span.literal.text;
        vars.push(`${name}: ${span.expression.getText()}`);
      });
      if (CJK.test(key)) {
        needTT = true;
        edits.push({ start: n.getStart(), end: n.getEnd(), text: `tt(${q(key)}, { ${vars.join(", ")} })` });
        return; // 表达式里的东西已经原样抄进去了, 不再往里走
      }
    }
    ts.forEachChild(n, walk);
  };
  walk(sf);
  if (edits.length === 0) return { src, count: 0 };

  edits.sort((a, b) => b.start - a.start);
  let out = src;
  for (const e of edits) out = out.slice(0, e.start) + e.text + out.slice(e.end);

  // 补 import —— 相对路径按文件所在目录算
  const hasT = /import\s*\{[^}]*\bt\b[^}]*\}\s*from\s*"[^"]*i18n\/index\.js"/.test(out);
  const hasTT = /import\s*\{[^}]*\btt\b[^}]*\}\s*from\s*"[^"]*i18n\/index\.js"/.test(out);
  if ((needT && !hasT) || (needTT && !hasTT)) {
    const rel = path.relative(path.dirname(file), "src/i18n/index.js").replace(/\\/g, "/");
    const spec = `import { ${[needT || hasT ? "t" : null, needTT || hasTT ? "tt" : null].filter(Boolean).join(", ")} } from "${rel.startsWith(".") ? rel : "./" + rel}";\n`;
    // 已有一条 i18n import 就替换它, 没有就插在第一条 import 前面
    const existing = out.match(/^import\s*\{[^}]*\}\s*from\s*"[^"]*i18n\/index\.js";\n/m);
    if (existing) out = out.replace(existing[0], spec);
    else {
      const firstImport = out.search(/^import\b/m);
      out = firstImport >= 0 ? out.slice(0, firstImport) + spec + out.slice(firstImport) : spec + out;
    }
  }
  return { src: out, count: edits.length };
}

let total = 0;
for (const file of FILES) {
  let src = readFileSync(file, "utf8"), count = 0;
  for (let i = 0; i < 6; i++) {            // 嵌套模板最多几层, 6 遍绰绰有余
    const r = pass(file, src);
    if (r.count === 0) break;
    src = r.src; count += r.count;
  }
  if (count > 0) {
    total += count;
    console.log(`${String(count).padStart(4)}  ${file}`);
    if (!DRY) writeFileSync(file, src);
  }
}
console.log(`${DRY ? "[dry-run] " : ""}共 ${total} 处`);
