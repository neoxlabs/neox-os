/**
 * eventLog — 内核对象 ②: 只追加事件日志.
 *
 *   设计要点 (这些是跟 agent IDE 的 timeline 的本质区别):
 *
 *   1. **没有特权观察者**. UI / 推送 / IM / 审计都是平等订阅者.
 *      日志不知道有没有人在看, 也不因为没人看而改变行为.
 *
 *   2. **subscribe 自带补齐**. 订阅者给一个 fromSeq, 先同步收到历史,
 *      再接上实时流. 订阅者永远看不到断层 —— 这是"任意时刻接入都能
 *      完整重建过程"的实现点, 也是旧架构做不到的地方 (旧的 timeline
 *      是渲染态, 断线重连靠前端自己对齐, 于是有了"切会话丢滚动位置"
 *      "发消息视口被偷"那一类问题).
 *
 *   3. **补齐期间到达的实时事件必须排队**, 不能插队发给订阅者 —— 否则
 *      订阅者会先看到 seq=9 再看到 seq=5. 见 subscribe() 里的 buffering.
 *
 *   4. seq 由日志分配, 不由调用方传. 进程内单调递增, 从 0 开始.
 */

import type { OsEvent, EventKind, ProcessId, Subscription } from '@neox-os/abi';

type Listener = (e: OsEvent) => void;

export class EventLog {
  /** pid → 该进程的事件序列 (下标即 seq) */
  private readonly logs = new Map<ProcessId, OsEvent[]>();
  private readonly perProcess = new Map<ProcessId, Set<Listener>>();
  private readonly global = new Set<Listener>();

  /** 时钟可注入 — 测试要确定性, 生产用 Date.now */
  constructor(private readonly now: () => number = () => Date.now()) {}

  append(pid: ProcessId, kind: EventKind, payload: unknown): OsEvent {
    let log = this.logs.get(pid);
    if (!log) {
      log = [];
      this.logs.set(pid, log);
    }
    const event: OsEvent = { seq: log.length, pid, at: this.now(), kind, payload };
    log.push(event);

    /* 派发. 单个订阅者抛错不能影响其他订阅者, 也不能影响写入 ——
     * 日志已经落了, 派发是尽力而为. */
    for (const l of this.perProcess.get(pid) ?? []) safeCall(l, event);
    for (const l of this.global) safeCall(l, event);
    return event;
  }

  replay(pid: ProcessId, fromSeq = 0): OsEvent[] {
    const log = this.logs.get(pid);
    if (!log) return [];
    return fromSeq <= 0 ? log.slice() : log.slice(fromSeq);
  }

  length(pid: ProcessId): number {
    return this.logs.get(pid)?.length ?? 0;
  }

  subscribe(pid: ProcessId, onEvent: Listener, fromSeq = 0): Subscription {
    /* 补齐期间到达的实时事件先入队, 补齐完再按序放出.
     * 不这么做就会乱序 —— 而乱序的事件流是不可重建的. */
    let backfilling = true;
    const queued: OsEvent[] = [];

    const listener: Listener = (e) => {
      if (backfilling) queued.push(e);
      else onEvent(e);
    };

    const set = this.perProcess.get(pid) ?? new Set<Listener>();
    set.add(listener);
    this.perProcess.set(pid, set);

    /* 先注册再读历史 —— 反过来会漏掉两步之间产生的事件 */
    const history = this.replay(pid, fromSeq);
    for (const e of history) safeCall(onEvent, e);

    backfilling = false;
    /* 队列里可能有跟历史重叠的 (注册后、读历史前写入的), 按 seq 去重 */
    const lastSeq = history.length ? history[history.length - 1]!.seq : fromSeq - 1;
    for (const e of queued) {
      if (e.seq > lastSeq) safeCall(onEvent, e);
    }

    return {
      close: () => {
        set.delete(listener);
        if (set.size === 0) this.perProcess.delete(pid);
      },
    };
  }

  subscribeAll(onEvent: Listener): Subscription {
    this.global.add(onEvent);
    return { close: () => void this.global.delete(onEvent) };
  }

  /** 落盘用 — 整个日志的可序列化快照 */
  snapshot(): Record<ProcessId, OsEvent[]> {
    const out: Record<ProcessId, OsEvent[]> = {};
    for (const [pid, log] of this.logs) out[pid] = log.slice();
    return out;
  }

  /** 从快照恢复 — 唤醒被换出的进程时用 */
  restore(snap: Record<ProcessId, OsEvent[]>): void {
    for (const [pid, log] of Object.entries(snap)) this.logs.set(pid, log.slice());
  }
}

function safeCall(listener: Listener, e: OsEvent): void {
  try {
    listener(e);
  } catch {
    /* 订阅者自己的问题不能传染给日志或其他订阅者 */
  }
}
