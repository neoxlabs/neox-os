/**
 * assembler — prompt 组装器 · 全引擎唯一.
 *
 *   它保证的性质只有一条, 但这一条决定了所有前缀缓存的成败:
 *
 *     **父空间的组装结果, 永远是子空间组装结果的字节前缀.**
 *
 *   有了这条, 提供方的前缀缓存在 fork 出的每个子执行体上必然命中 ——
 *   不需要任何"尽量复用"的启发式, 是结构保证.
 *
 *   反过来说, 只要有第二个地方也在组装 prompt, 这条就守不住:
 *   键序、空白、字段顺序早晚会漂. 这是"一个引擎"而不是
 *   "每个 agent 一个引擎"的根本理由.
 */

import type { ContextSpace, PageId } from './pageTable.js';
import { stableStringify } from './pageTable.js';

export interface AssembleResult {
  /** 要发出去的字节 */
  bytes: string;
  /**
   * 每一页结束时的字节偏移 —— 缓存断点只能落在这些位置上.
   * **只含页边界, 不含系统段边界** —— 混进去会让 breakAt 的下标错一位.
   */
  offsets: number[];
  /** 系统段占的字节数 (offsets 的起点) */
  prefixBytes: number;
  /** 建议的缓存断点 (字节偏移). 落在共享前缀的末尾 */
  breakpoints: number[];
  /** 换出的页 —— 组装前必须先 fault 回来 */
  faults: PageId[];
}

export interface AssembleOptions {
  /**
   * 系统段. **必须字节稳定** —— 变一个字节, 所有执行体的缓存全废.
   * 所以它不接受任何随时间变化的东西 (时间戳 / 随机 id / 机器名).
   */
  systemSegment?: string;
  /** 在这些页数位置放缓存断点. 缺省: 共享前缀末尾 */
  breakAt?: number[];
}

export function assemble(space: ContextSpace, opts: AssembleOptions = {}): AssembleResult {
  const faults: PageId[] = [];
  const offsets: number[] = [];
  const parts: string[] = [];
  let cursor = 0;

  let prefixBytes = 0;
  if (opts.systemSegment) {
    parts.push(opts.systemSegment);
    prefixBytes = Buffer.byteLength(opts.systemSegment, 'utf8');
    cursor += prefixBytes;
    /* 刻意不 push —— offsets[i] 必须恒等于"第 i+1 页结束处" */
  }

  for (const id of space.pageIds()) {
    if (!space.store.isResident(id)) {
      /* 换出的页 —— 记下来让调用方 fault, 不在这里偷偷跳过.
       * 悄悄跳过就是"压缩掉了但没人知道", 那正是旧做法的病根. */
      faults.push(id);
      continue;
    }
    const page = space.store.get(id);
    const chunk = stableStringify({ k: page.kind, c: page.content });
    parts.push(chunk);
    cursor += Buffer.byteLength(chunk, 'utf8');
    offsets.push(cursor);
  }

  const breakpoints = (opts.breakAt ?? [])
    .map((n) => offsets[n - 1])
    .filter((x): x is number => x !== undefined);

  return { bytes: parts.join(''), offsets, prefixBytes, breakpoints, faults };
}

/**
 * 给一组同源的执行体算最优缓存断点.
 *   断点放在它们的共享前缀末尾 —— 那是所有子都能命中的最长部分.
 */
export function planBreakpoint(
  parent: ContextSpace,
  children: readonly ContextSpace[],
): number {
  if (children.length === 0) return parent.length;
  let min = Infinity;
  for (const c of children) min = Math.min(min, c.sharedPrefixWith(parent));
  return Number.isFinite(min) ? min : 0;
}
