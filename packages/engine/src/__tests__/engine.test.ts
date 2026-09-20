/**
 * 引擎验收 —— 三条性质, 每条都对应一个具体收益.
 *
 *   1. 父的组装结果是子的字节前缀   → 前缀缓存必然命中 (结构保证, 非启发式)
 *   2. fork 是 O(1) 且不复制内容    → 起子执行体接近零成本
 *   3. 换出无损                     → "压缩"不再丢东西
 */

import { describe, it, expect } from 'vitest';
import { PageStore, ContextSpace, measureSharing, PageFault, stableStringify } from '../pageTable.js';
import { assemble, planBreakpoint } from '../assembler.js';

const SYSTEM = 'NEOX-OS/1'; /* 真实系统段必须字节稳定, 这里用常量代表 */

function rootWithPrefix(store: PageStore, n: number): ContextSpace {
  const root = new ContextSpace(store, null, 'root');
  for (let i = 0; i < n; i++) {
    root.append('doc', { file: `src/mod${i}.ts`, summary: `模块 ${i} 的理解`.repeat(20) });
  }
  return root;
}

describe('性质 1 · 父的字节永远是子的前缀', () => {
  it('子追加再多, 也不动父的一个字节', () => {
    const store = new PageStore();
    const parent = rootWithPrefix(store, 5);
    const before = assemble(parent, { systemSegment: SYSTEM }).bytes;

    const child = parent.fork('child');
    child.append('input', { ask: '把 mod3 重构了' });
    child.append('tool_result', { ok: true, changed: ['src/mod3.ts'] });

    const after = assemble(parent, { systemSegment: SYSTEM }).bytes;
    const childBytes = assemble(child, { systemSegment: SYSTEM }).bytes;

    /* 父自己没变 */
    expect(after).toBe(before);
    /* 而且父是子的严格字节前缀 —— 提供方的前缀缓存必然命中 */
    expect(childBytes.startsWith(before)).toBe(true);
    expect(childBytes.length).toBeGreaterThan(before.length);
  });

  it('多个兄弟共享同一段前缀, 互不影响', () => {
    const store = new PageStore();
    const parent = rootWithPrefix(store, 4);
    const base = assemble(parent, { systemSegment: SYSTEM }).bytes;

    const kids = ['a', 'b', 'c'].map((n) => {
      const k = parent.fork(n);
      k.append('input', { branch: n });
      return k;
    });

    for (const k of kids) {
      expect(assemble(k, { systemSegment: SYSTEM }).bytes.startsWith(base)).toBe(true);
      expect(k.sharedPrefixWith(parent)).toBe(4);
    }
    /* 断点应落在共享前缀末尾 */
    expect(planBreakpoint(parent, kids)).toBe(4);
  });

  it('组装是确定性的 —— 同样输入两次得到同样字节', () => {
    const store = new PageStore();
    const s = new ContextSpace(store);
    /* 故意用不同键序构造同一份内容 */
    s.append('note', { b: 2, a: 1, c: { z: 1, y: 2 } });
    const once = assemble(s).bytes;
    const twice = assemble(s).bytes;
    expect(once).toBe(twice);

    const t = new ContextSpace(store);
    t.append('note', { c: { y: 2, z: 1 }, a: 1, b: 2 });
    expect(assemble(t).bytes).toBe(once);
  });
});

describe('性质 2 · fork O(1), 内容零复制', () => {
  it('fork 不产生新页, 内容在存储里只有一份', () => {
    const store = new PageStore();
    const parent = rootWithPrefix(store, 10);
    const residentBefore = store.stats().resident;

    const kids = Array.from({ length: 50 }, (_, i) => parent.fork(`k${i}`));

    /* 50 个子空间, 一个新页都没产生 */
    expect(store.stats().resident).toBe(residentBefore);
    for (const k of kids) {
      expect(k.length).toBe(10);
      expect(k.ownLength).toBe(0); /* 自己一页都不持有 */
    }
  });

  it('50 个子执行体的传输量: 朴素做法 vs 共享前缀', () => {
    const store = new PageStore();
    const parent = rootWithPrefix(store, 20); /* 20 页共享理解 */
    const kids = Array.from({ length: 50 }, (_, i) => {
      const k = parent.fork(`k${i}`);
      k.append('input', { task: `子任务 ${i}` }); /* 各自一点点尾巴 */
      return k;
    });

    const m = measureSharing(kids);
    expect(m.spaces).toBe(50);
    /* 朴素做法每个子都重传 20 页共享内容 */
    expect(m.naiveBytes).toBeGreaterThan(m.sharedBytes * 10);
    /* 节省应当在 90% 以上 */
    expect(m.savedRatio).toBeGreaterThan(0.9);
  });

  it('相同内容自动去重 —— 两个执行体各自读同一个文件只存一份', () => {
    const store = new PageStore();
    const a = new ContextSpace(store, null, 'a');
    const b = new ContextSpace(store, null, 'b');
    const doc = { file: 'README.md', text: '同一份文档' };

    const pa = a.append('doc', doc);
    const pb = b.append('doc', doc);

    expect(pa.id).toBe(pb.id);
    expect(store.stats().resident).toBe(1);
    expect(store.appends(pa.id)).toBe(2);
  });
});

describe('性质 3 · 换出无损', () => {
  it('换出后页仍属于地址空间, 换入拿回原内容', () => {
    const store = new PageStore();
    const s = new ContextSpace(store);
    const p = s.append('tool_result', { big: 'x'.repeat(5000) });
    const lenBefore = s.length;

    expect(store.evict(p.id)).toBe(true);
    expect(store.isResident(p.id)).toBe(false);
    /* 关键: 地址空间没变短 —— 页还在, 只是不在物理内存里 */
    expect(s.length).toBe(lenBefore);
    expect(store.has(p.id)).toBe(true);
    /* 直接取会缺页 */
    expect(() => store.get(p.id)).toThrow(PageFault);

    /* 换入后内容完全一致 —— 无损 */
    const back = store.fault(p.id);
    expect(back.content).toEqual({ big: 'x'.repeat(5000) });
  });

  it('组装时遇到换出的页会明确报出来, 不静默跳过', () => {
    const store = new PageStore();
    const s = new ContextSpace(store);
    s.append('note', { a: 1 });
    const p = s.append('note', { b: 2 });
    store.evict(p.id);

    const r = assemble(s);
    /* 悄悄跳过就是"压缩掉了但没人知道" —— 必须显式暴露 */
    expect(r.faults).toEqual([p.id]);
  });

  it('换出的是内容不是历史 —— 换入后组装结果跟从没换出过一样', () => {
    const store = new PageStore();
    const s = new ContextSpace(store);
    s.append('note', { a: 1 });
    const p = s.append('note', { b: 2 });
    const original = assemble(s).bytes;

    store.evict(p.id);
    store.fault(p.id);

    expect(assemble(s).bytes).toBe(original);
  });
});

describe('回归 · 审计发现的缺陷', () => {
  it('有系统段时缓存断点不会错一位', () => {
    const store = new PageStore();
    const s = new ContextSpace(store);
    s.append('note', { n: 1 });
    s.append('note', { n: 2 });
    s.append('note', { n: 3 });

    const withSys = assemble(s, { systemSegment: SYSTEM, breakAt: [2] });
    const noSys = assemble(s, { breakAt: [2] });

    /* 断点应当恰好落在第 2 页结束处, 且两种情况只差一个系统段长度 */
    expect(withSys.breakpoints[0]! - noSys.breakpoints[0]!).toBe(
      Buffer.byteLength(SYSTEM, 'utf8'),
    );
    /* 断点处截断, 前面必须正好是 系统段 + 前两页 */
    const head = withSys.bytes.slice(0, withSys.breakpoints[0]!);
    expect(head.startsWith(SYSTEM)).toBe(true);
    /* 第 2 页的完整序列化必须正好收在断点上 */
    expect(head.endsWith(stableStringify({ k: 'note', c: { n: 2 } }))).toBe(true);
  });

  it('换出的页仍计入传输量 —— 省了多少不能虚高', () => {
    const store = new PageStore();
    const root = new ContextSpace(store);
    const p = root.append('doc', { big: 'y'.repeat(4000) });
    const kids = [root.fork('a'), root.fork('b')];

    const before = measureSharing(kids);
    store.evict(p.id);
    const after = measureSharing(kids);

    /* 换出只改变它在不在内存, 不改变它的逻辑大小 */
    expect(after.naiveBytes).toBe(before.naiveBytes);
    expect(after.sharedBytes).toBe(before.sharedBytes);
  });
});
