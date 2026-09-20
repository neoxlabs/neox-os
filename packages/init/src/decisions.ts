/**
 * decisions — 待决策登记处：把需要人工判断的事项保存为可跨进程、跨设备解决的状态.
 *
 *   直接阻塞在当前 UI 上等待回答会在无人连接时卡死或超时, 形成必须守在电脑边
 *   的使用约束. 登记待决策后, 进程转为 waiting (不占计算, 可被换出), OS 将事项
 *   路由到可用通道, 任意时刻、任意设备上的解决结果都能唤醒进程继续.
 *
 *   关键性质:
 *     · 决策活得比进程的任何一次运行都长 (进程可以在等待期间被换出重启)
 *     · 决策不属于任何客户端. 谁先解决算谁的, 后到的收到 already-resolved
 *     · 超时有 OS 兜底, 不是无限等
 */

import type {
  DecisionId,
  DecisionRequest,
  DecisionResolution,
  PendingDecision,
  ProcessId,
} from '@neox-os/abi';

interface Waiter {
  pending: PendingDecision;
  resolve: (r: DecisionResolution) => void;
  timer?: ReturnType<typeof setTimeout>;
}

export class DecisionRegistry {
  private readonly waiters = new Map<DecisionId, Waiter>();
  /**
   * 已解决的保留最近 N 条, 让重复 resolve 拿到明确答复而不是"不存在".
   * 有上限是刻意的 —— 长跑进程会解决成千上万个决策, 无上限就是内存泄漏.
   */
  private readonly settled = new Map<DecisionId, DecisionResolution>();
  private static readonly SETTLED_MAX = 256;
  private counter = 0;

  constructor(
    private readonly now: () => number = () => Date.now(),
    /** 决策产生/解决时通知 OS — OS 负责写日志、改进程状态、路由触达 */
    private readonly hooks: {
      onRequested?: (p: PendingDecision) => void;
      onResolved?: (r: DecisionResolution) => void;
    } = {},
  ) {}

  request(pid: ProcessId, req: DecisionRequest): Promise<DecisionResolution> {
    const did: DecisionId = `d${++this.counter}`;
    const pending: PendingDecision = { did, pid, request: req, requestedAt: this.now() };

    return new Promise<DecisionResolution>((resolve) => {
      const waiter: Waiter = { pending, resolve };

      if (req.onTimeout) {
        waiter.timer = setTimeout(() => {
          /* 超时兜底也走同一条 resolve 路径 —— 于是日志里看得见
           * "这个决定是超时替你做的", by='timeout'. 不许静默默认. */
          this.resolve(did, req.onTimeout!.choose, 'timeout');
        }, req.onTimeout.afterMs);
        /* 别让一个待决策把进程钉在事件循环上 */
        waiter.timer.unref?.();
      }

      this.waiters.set(did, waiter);
      this.hooks.onRequested?.(pending);
    });
  }

  pending(): PendingDecision[] {
    return [...this.waiters.values()].map((w) => w.pending);
  }

  get(did: DecisionId): PendingDecision | undefined {
    return this.waiters.get(did)?.pending;
  }

  /**
   * 解决一个决策. 返回 false 表示它已经被解决过了 (或从不存在) ——
   * 调用方 (客户端) 据此告诉用户"这条已经处理过了", 而不是静默吞掉.
   */
  resolve(
    did: DecisionId,
    choice: string,
    by: string,
    values?: Record<string, unknown>,
  ): boolean {
    const waiter = this.waiters.get(did);
    if (!waiter) return false;

    this.waiters.delete(did);
    if (waiter.timer) clearTimeout(waiter.timer);

    const resolution: DecisionResolution = { did, choice, by, at: this.now(), values };
    this.settled.set(did, resolution);
    if (this.settled.size > DecisionRegistry.SETTLED_MAX) {
      /* Map 保持插入序, 删最旧的那条 */
      const oldest = this.settled.keys().next().value;
      if (oldest !== undefined) this.settled.delete(oldest);
    }
    this.hooks.onResolved?.(resolution);
    waiter.resolve(resolution);
    return true;
  }

  settledResult(did: DecisionId): DecisionResolution | undefined {
    return this.settled.get(did);
  }

  /** 关机时清掉所有定时器, 否则进程退不干净 */
  dispose(): void {
    for (const w of this.waiters.values()) if (w.timer) clearTimeout(w.timer);
  }
}
