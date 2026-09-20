#!/usr/bin/env node
/**
 * forbidden-words — 边界闸.
 *
 *   这个闸防的不是别人, 是我们自己的肌肉记忆.
 *
 *   重写一个 OS 最常见的死法是: 写着写着又长出了 turn, 又长出了 timeline,
 *   三个月后发现是同一个 agent IDE 换了包名. 靠自律防不住, 靠 CI 防得住.
 *
 *   被禁的五个词对应旧世界观的五个内核对象:
 *     turn      — 一问一答的边界. OS 里的计算没有轮次
 *     timeline  — 给人看的界面模型. 界面不能长在内核里
 *     chat      — 对话. 进程不一定跟人说话
 *     assistant — 说话人角色. 进程没有角色
 *     session   — 会话. 进程不依附于任何连接
 *
 *   要用这些概念? 可以, 但只能在应用层 (packages/apps/*), 不能在 OS 层.
 *   注释和字符串不算 —— 讨论旧架构是允许的, 把它写进类型才是问题.
 */

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const ROOT = new URL('..', import.meta.url).pathname;
const SCAN_DIRS = ['packages/abi/src', 'packages/init/src', 'packages/confine/src', 'packages/boot/src', 'packages/engine/src'];
const FORBIDDEN = ['turn', 'timeline', 'chat', 'assistant', 'session'];

const pattern = new RegExp(`\\b(${FORBIDDEN.join('|')})\\b`, 'i');

/** 去掉注释和字符串字面量 —— 只看真正的标识符 */
function stripCommentsAndStrings(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/\/\/[^\n]*/g, ' ')
    .replace(/`(?:\\[\s\S]|[^\\`])*`/g, '``')
    .replace(/'(?:\\[\s\S]|[^\\'])*'/g, "''")
    .replace(/"(?:\\[\s\S]|[^\\"])*"/g, '""');
}

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) yield* walk(p);
    else if (p.endsWith('.ts')) yield p;
  }
}

const violations = [];
for (const d of SCAN_DIRS) {
  const abs = join(ROOT, d);
  let entries;
  try {
    entries = [...walk(abs)];
  } catch {
    continue; /* 目录还不存在 */
  }
  for (const file of entries) {
    const cleaned = stripCommentsAndStrings(readFileSync(file, 'utf8'));
    cleaned.split('\n').forEach((line, i) => {
      const m = line.match(pattern);
      if (m) {
        violations.push({
          file: relative(ROOT, file),
          line: i + 1,
          word: m[1],
          text: line.trim().slice(0, 100),
        });
      }
    });
  }
}

if (violations.length) {
  console.error('\n❌ 边界闸: OS 层出现了旧世界观的词汇\n');
  for (const v of violations) {
    console.error(`  ${v.file}:${v.line}  「${v.word}」  ${v.text}`);
  }
  console.error(
    `\n这些概念属于应用层, 不属于 OS 层. 见 tools/forbidden-words.mjs 顶部说明.\n`,
  );
  process.exit(1);
}

console.log(`✅ 边界闸通过 — OS 层没有 ${FORBIDDEN.join(' / ')}`);
