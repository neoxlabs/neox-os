/**
 * registry — 行 id → 已挂载的 DOM 节点.
 *
 *   为什么不用 querySelector: 流式一帧要改十几行,
 *   每次 querySelector 都要在文档中匹配, 仅这项开销就能让帧率从 60fps 降到 22fps.
 *   注册表是 O(1), 而且行卸载时自动摘掉, 不会指向已经不在的节点.
 */

export interface RowNodes { host: HTMLElement; text: HTMLElement }

export class RowRegistry {
  readonly #nodes = new Map<string, RowNodes>();
  /** 这一帧要写的行 —— 同一行一帧内改十次也只写一次 DOM */
  readonly #pending = new Map<string, string>();
  #frame = 0;
  #flush: (() => void) | undefined;

  mount(id: string, nodes: RowNodes): () => void {
    this.#nodes.set(id, nodes);
    return () => { if (this.#nodes.get(id) === nodes) this.#nodes.delete(id); };
  }

  get(id: string): RowNodes | undefined { return this.#nodes.get(id); }

  onFlush(fn: () => void): void { this.#flush = fn; }

  /** 排一次文本写入. 真正落 DOM 在帧末, 合并同一行的多次修改 */
  queueText(id: string, text: string): void {
    this.#pending.set(id, text);
    if (this.#frame !== 0) return;
    this.#frame = requestAnimationFrame(() => {
      this.#frame = 0;
      // 先全部写 (write 阶段), 再统一量 (read 阶段) —— 交错读写会强制同步布局
      for (const [rowId, value] of this.#pending) {
        const nodes = this.#nodes.get(rowId);
        if (nodes !== undefined) nodes.text.textContent = value;
      }
      this.#pending.clear();
      this.#flush?.();
    });
  }

  dispose(): void { cancelAnimationFrame(this.#frame); this.#nodes.clear(); this.#pending.clear(); }
}
