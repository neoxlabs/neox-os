/**
 * pageTable — 上下文页表.
 *
 *   把上下文当虚拟内存来管:
 *     页        = 一条内容 (输入 / 工具结果 / 文档片段), 内容寻址, 不可变
 *     地址空间  = 一个执行体"记得"的全部, 由 base 链 + 自己的页组成
 *     fork      = O(1). 子只持有对父的引用, 不复制任何内容
 *     换出/换入 = 内容进 swap, 页仍在地址空间里. **无损**
 *
 *   为什么 base 链而不是数组拷贝:
 *     子往自己的 own 里追加, **永远不会动到父的任何字节**.
 *     于是父的序列化结果永远是子的字节前缀 —— 提供方的前缀缓存必然命中.
 *     用数组拷贝也能做到, 但 fork 就是 O(n) 且没有结构性保证.
 *
 *   换出不等于丢弃:
 *     内容进入 swap, 页号仍在地址空间里. 再次引用时 fault 回来,
 *     因而压缩不会删除用户原话.
 */

import { createHash } from 'node:crypto';

export type PageId = string;

export type PageKind = 'input' | 'output' | 'tool_result' | 'doc' | 'note';

export interface Page {
  id: PageId;
  kind: PageKind;
  /** 结构化内容. 不存渲染文案 */
  content: unknown;
  /** 序列化后的字节数 —— 传输成本的真实单位 */
  bytes: number;
}

/** 页不在内存里 —— 需要先 fault */
export class PageFault extends Error {
  constructor(public readonly pageId: PageId) {
    super(`page fault: ${pageId}`);
    this.name = 'PageFault';
  }
}

/**
 * 内容寻址页存储 · 全引擎唯一.
 *   相同内容只存一份 —— 这是"多个执行体共享同一份理解"的物理基础.
 */
export class PageStore {
  private readonly residentMap = new Map<PageId, Page>();
  /** 换出区. 真实现里这是磁盘/事件日志, 这里用内存模拟以便测试 */
  private readonly swapMap = new Map<PageId, Page>();
  /**
   * 每页被 append 过多少次.
   *   **这不是引用计数** —— 没有 release, 也就没有 GC.
   *   页目前只增不减: 一个长跑引擎最终会把所有历史内容都留在内存里.
   *   真正的回收要等地址空间生命周期管理.
   */
  private readonly appendCount = new Map<PageId, number>();

  put(kind: PageKind, content: unknown): Page {
    const bytes = Buffer.byteLength(stableStringify(content), 'utf8');
    const id = createHash('sha256')
      .update(kind)
      .update('\0')
      .update(stableStringify(content))
      .digest('hex')
      .slice(0, 32);

    const existing = this.residentMap.get(id) ?? this.swapMap.get(id);
    if (existing) {
      /* 内容相同 → 复用同一页. 这是去重发生的地方 */
      this.appendCount.set(id, (this.appendCount.get(id) ?? 0) + 1);
      return existing;
    }

    const page: Page = { id, kind, content, bytes };
    this.residentMap.set(id, page);
    this.appendCount.set(id, 1);
    return page;
  }

  /** 取页. 已换出 → 抛 PageFault, 由调用方决定要不要 fault 回来 */
  get(id: PageId): Page {
    const p = this.residentMap.get(id);
    if (p) return p;
    if (this.swapMap.has(id)) throw new PageFault(id);
    throw new Error(`unknown page: ${id}`);
  }

  has(id: PageId): boolean {
    return this.residentMap.has(id) || this.swapMap.has(id);
  }

  isResident(id: PageId): boolean {
    return this.residentMap.has(id);
  }

  /** 这一页被 append 过几次. 名字刻意不叫 refs —— 它不参与回收 */
  appends(id: PageId): number {
    return this.appendCount.get(id) ?? 0;
  }

  /** 页的字节数, 不管它在不在物理内存里 —— 换出不改变它的逻辑大小 */
  bytesOf(id: PageId): number {
    return (this.residentMap.get(id) ?? this.swapMap.get(id))?.bytes ?? 0;
  }

  /**
   * 换出 —— **无损**. 内容进 swap, 页号仍然有效, 地址空间不变.
   * 这就是"压缩"的正确形态: 不是删除, 是移出物理内存.
   */
  evict(id: PageId): boolean {
    const p = this.residentMap.get(id);
    if (!p) return false;
    this.residentMap.delete(id);
    this.swapMap.set(id, p);
    return true;
  }

  /** 换入 —— 缺页中断的处理. 由 OS 触发, 不需要模型自己想起来 */
  fault(id: PageId): Page {
    const p = this.swapMap.get(id);
    if (!p) throw new Error(`page not in swap: ${id}`);
    this.swapMap.delete(id);
    this.residentMap.set(id, p);
    return p;
  }

  stats(): { resident: number; swapped: number; residentBytes: number } {
    let residentBytes = 0;
    for (const p of this.residentMap.values()) residentBytes += p.bytes;
    return { resident: this.residentMap.size, swapped: this.swapMap.size, residentBytes };
  }
}

/**
 * 上下文地址空间 · 一个执行体一个.
 *
 *   结构是 base 链: [根的页...] ++ [父的页...] ++ [自己的页...]
 *   追加只动自己那截, 所以祖先的字节永远稳定.
 */
export class ContextSpace {
  private readonly own: PageId[] = [];

  constructor(
    readonly store: PageStore,
    private readonly base: ContextSpace | null = null,
    readonly label = 'root',
  ) {}

  /** fork 一个子空间 —— O(1), 不复制任何内容 */
  fork(label: string): ContextSpace {
    return new ContextSpace(this.store, this, label);
  }

  append(kind: PageKind, content: unknown): Page {
    const page = this.store.put(kind, content);
    this.own.push(page.id);
    return page;
  }

  /** 本空间独有的页数 (不含继承来的) */
  get ownLength(): number {
    return this.own.length;
  }

  get length(): number {
    return (this.base?.length ?? 0) + this.own.length;
  }

  /** 展开成有序页号列表 */
  pageIds(): PageId[] {
    return this.base ? [...this.base.pageIds(), ...this.own] : [...this.own];
  }

  /**
   * 与另一个空间的共享前缀长度 —— 这就是能命中提供方前缀缓存的部分.
   * 靠 base 链求最近公共祖先, 不做逐页比对.
   */
  sharedPrefixWith(other: ContextSpace): number {
    const mine = new Map<ContextSpace, number>();
    for (let s: ContextSpace | null = this; s; s = s.baseOf()) mine.set(s, s.length);
    for (let s: ContextSpace | null = other; s; s = s.baseOf()) {
      const n = mine.get(s);
      if (n !== undefined) return n;
    }
    return 0;
  }

  private baseOf(): ContextSpace | null {
    return this.base;
  }

}

/**
 * 量一组空间的传输节省 —— 这是这套设计要证明的那个数.
 *
 *   naiveBytes:  每个执行体各传各的完整上下文 (现在的做法)
 *   sharedBytes: 唯一页只算一次 (前缀命中缓存后实际要传的)
 */
export function measureSharing(spaces: readonly ContextSpace[]): {
  spaces: number;
  naiveBytes: number;
  sharedBytes: number;
  savedRatio: number;
} {
  let naiveBytes = 0;
  const unique = new Map<PageId, number>();

  for (const s of spaces) {
    for (const id of s.pageIds()) {
      const bytes = s.store.bytesOf(id);
      naiveBytes += bytes;
      unique.set(id, bytes);
    }
  }

  let sharedBytes = 0;
  for (const b of unique.values()) sharedBytes += b;

  return {
    spaces: spaces.length,
    naiveBytes,
    sharedBytes,
    savedRatio: naiveBytes === 0 ? 0 : 1 - sharedBytes / naiveBytes,
  };
}

/**
 * 确定性序列化 —— 键序稳定.
 *   字节不稳定 = 缓存全废. 这是引擎必须统一组装的根本原因:
 *   多处各自组装, 键序早晚会漂.
 */
export function stableStringify(v: unknown): string {
  if (v === null || typeof v !== 'object') return JSON.stringify(v) ?? 'null';
  if (Array.isArray(v)) return `[${v.map(stableStringify).join(',')}]`;
  const keys = Object.keys(v as Record<string, unknown>).sort();
  return `{${keys
    .map((k) => `${JSON.stringify(k)}:${stableStringify((v as Record<string, unknown>)[k])}`)
    .join(',')}}`;
}
