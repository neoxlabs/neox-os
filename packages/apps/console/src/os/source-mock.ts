/**
 * source-mock — 合成事件源.
 *
 *   OS 还没有给观察者的端口 (go/osinit 只有给受约束进程的 unix socket),
 *   所以先用它把界面压到**比真实负载更狠**的地步 —— 否则"不卡"证明不了什么.
 */

import { t, tt } from "../i18n/index.js";
import type { EventKind, OsEvent, ProcessID, ProcessState } from "./events.js";

const WORDS = t("把这条链路从头到尾走一遍 先确认约束在内核而不是在工具表 再看预算 止损线躲不掉 因为记账在 OS 这一侧 供应商回报多少就是多少 进程报不报都一样 我先跑一遍测试 再决定要不要动那个文件").split(" ");

export interface Bot { pid: ProcessID; name: string; state: ProcessState }

export class MockSource {
  readonly bots: Bot[] = [
    { pid: "p-research", name: t("研究"), state: "running" },
    { pid: "p-ops", name: t("值守"), state: "waiting" },
    { pid: "p-build", name: t("构建"), state: "exited" }
  ];
  readonly #seq = new Map<ProcessID, number>();
  #timer = 0;
  #word = 0;
  #stream = 0;

  constructor(private readonly sink: (events: OsEvent[]) => void) {}

  #event(pid: ProcessID, kind: EventKind, payload: unknown): OsEvent {
    const seq = this.#seq.get(pid) ?? 0;
    this.#seq.set(pid, seq + 1);
    return { seq, pid, at: Date.now(), kind, payload };
  }

  seed(rounds = 300): void {
    const batch: OsEvent[] = [];
    for (const bot of this.bots) {
      batch.push(this.#event(bot.pid, "proc.state", { state: "created" }));
      for (let index = 0; index < rounds; index += 1) batch.push(...this.#round(bot.pid, index));
    }
    this.sink(batch);
  }

  #round(pid: ProcessID, index: number): OsEvent[] {
    const stream = `s${index}`;
    const out: OsEvent[] = [
      this.#event(pid, "input.recv", { text: tt("第 {a} 件事: {b}{c}", { a: index + 1, b: WORDS[index % WORDS.length], c: WORDS[(index + 3) % WORDS.length] }) }),
      this.#event(pid, "proc.state", { state: "running" })
    ];
    for (let n = 0; n < 4 + (index % 7); n += 1) {
      out.push(this.#event(pid, "proc.output", { stream, channel: n < 2 ? "think" : "say", text: `${WORDS[(index + n) % WORDS.length]} ` }));
    }
    if (index % 9 === 4) out.push(this.#event(pid, "cap.used", { axis: "read", scope: `/workspace/src/mod${index}.ts` }));
    if (index % 17 === 7) out.push(this.#event(pid, "cap.denied", { axis: "write", scope: "/etc", mechanism: "landlock" }));
    // 跟真 OS 一个格式(DecideRequestPayload): did + PresentSpec —— 旧格式(axis/scope/why)
    // 已经没有任何一侧在认了, mock 发它只会把界面带崩
    if (index % 23 === 11) out.push(this.#event(pid, "decide.requested", { did: `d${pid}${index}`, present: { kind: "approve", title: t("要访问 api.github.com"), detail: t("要读 PR 列表才能接着往下走") } }));
    out.push(this.#event(pid, "proc.outcome", { ok: index % 13 !== 12, summary: index % 13 !== 12 ? t("跑了测试, 全绿") : t("编译没过, 已回滚") }));
    return out;
  }

  /** 用户发言 */
  say(pid: ProcessID, text: string): void {
    this.sink([this.#event(pid, "input.recv", { text }), this.#event(pid, "proc.state", { state: "running" })]);
    this.stream(pid, 12, 2600);
  }

  /** 用户对某张卡片做了回应 */
  resolve(pid: ProcessID, widgetId: string, answer: string): void {
    this.sink([this.#event(pid, "decide.resolved", { id: widgetId, allowed: answer === "allow", by: t("你") })]);
  }

  /** 即时 UI 演示 */
  widgets(pid: ProcessID): void {
    const ui = (spec: unknown) => this.#event(pid, "proc.output", { stream: `ui${Date.now()}${Math.random()}`, channel: "ui", text: JSON.stringify(spec) });
    this.sink([
      ui({ type: "choice", id: `c${Date.now()}`, question: t("这三台机器都要重装, 先动哪台?"), options: [{ value: "a", label: t("构建机") }, { value: "b", label: t("值守机") }, { value: "c", label: t("都等我确认") }] }),
      ui({ type: "progress", id: `p${Date.now()}`, label: t("正在重建索引"), percent: 68, detail: t("12,404 / 18,300 文件") }),
      ui({ type: "diff", id: `f${Date.now()}`, path: "go/osinit/eventlog.go", patch: "-\tnext := len(l.logs[pid])\n+\tnext := l.next[pid]" }),
      ui({ type: "seismograph", id: `x${Date.now()}` })
    ]);
  }

  /** 流式回答: 每帧几十个块 */
  stream(pid: ProcessID, chunksPerFrame = 20, durationMs = 0): void {
    this.stop();
    this.#stream += 1;
    const stream = `live${this.#stream}`;
    const startedAt = performance.now();
    const tick = () => {
      const batch: OsEvent[] = [];
      for (let n = 0; n < chunksPerFrame; n += 1) {
        batch.push(this.#event(pid, "proc.output", { stream, channel: "say", text: `${WORDS[this.#word++ % WORDS.length]} ` }));
      }
      this.sink(batch);
      if (durationMs > 0 && performance.now() - startedAt > durationMs) {
        this.sink([this.#event(pid, "proc.outcome", { ok: true, summary: t("说完了") })]);
        return;
      }
      this.#timer = requestAnimationFrame(tick);
    };
    this.#timer = requestAnimationFrame(tick);
  }

  /** 压力: 一次灌 count 条 */
  storm(pid: ProcessID, count = 20_000): void {
    const batch: OsEvent[] = [];
    const tag = `storm${Date.now()}`;
    for (let n = 0; n < count; n += 1) {
      batch.push(n % 40 === 0
        ? this.#event(pid, "cap.used", { axis: "read", scope: `/workspace/f${n}.ts` })
        : this.#event(pid, "proc.output", { stream: `${tag}-${Math.floor(n / 40)}`, channel: "say", text: `${WORDS[n % WORDS.length]} ` }));
    }
    this.sink(batch);
  }

  stop(): void { cancelAnimationFrame(this.#timer); this.#timer = 0; }
}
