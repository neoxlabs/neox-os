#!/usr/bin/env node
/**
 * 无边框闸 —— 不许给"一块面"描一圈框.
 *
 * ── 判据 ──
 *
 *   `border` 本身不是罪。折角、单选圈、转圈的那道弧, 都是**用 border 画的图形**;
 *   区域之间的一条发丝分隔线也是正当的。
 *   要挡的是另一件事: 一个已经**是一块面**的东西(有底色或有内边距),
 *   再给它描一圈框。
 *
 *   那圈框把"这是一块面"说了第二遍(底色已经说过一次), 而且深浅两套主题里
 *   那个灰总有一套是脏的 —— 出来的东西就显得廉价。分层交给实底色差,
 *   浮起来交给投影。
 *
 *   所以判据是 **"这条规则里同时有 background/padding 和一个会画线的 border"**,
 *   不是"出现了 border 这个词"。按词扫会把折角和分隔线一起误杀,
 *   然后这道闸就会被关掉。
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const targets = ["packages/apps/console/src/shell/console.css"];

/** 这个值会不会真的画出一条线 */
function draws(value) {
  const v = value.trim().toLowerCase();
  if (v === "" || v === "none" || v === "0" || v === "unset" || v === "initial") return false;
  return !/^0(px|em|rem)?\s+/.test(v);
}

let bad = 0;
for (const rel of targets) {
  const css = readFileSync(path.join(here, "..", rel), "utf8");
  // 去掉注释: 说明文字里提到 border 是在解释为什么不用它
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, (block) => block.replace(/[^\n]/g, " "));
  for (const match of clean.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = match[1].trim().replace(/\s+/g, " ");
    const body = match[2];
    const line = clean.slice(0, match.index).split("\n").length;

    const outline = [...body.matchAll(/(^|;)\s*(border)\s*:\s*([^;]*)/g)].find((hit) => draws(hit[3]));
    if (outline === undefined) continue;
    // 是不是一块"面": 有底色 或 有内边距
    const surface = /(^|;)\s*(background|background-color)\s*:/.test(body)
      || /(^|;)\s*padding(-\w+)?\s*:\s*(?!0\b)/.test(body);
    if (!surface) continue;

    bad += 1;
    console.error(`${rel}:${line}  ${selector} { border: ${outline[3].trim()} }`);
  }
}

if (bad > 0) {
  console.error(`\n❌ 无边框闸: 有 ${bad} 块面被描了框。分层用底色差, 浮起来用投影。`);
  process.exit(1);
}
console.log("✅ 无边框闸通过 — 没有一块面靠描框分层");
