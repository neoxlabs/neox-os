/**
 * store — 客户端侧的事件日志镜像.
 *
 *   语义与 go/osinit/eventlog.go 保持一致, 因为**两边语义不一致就是不可重建**:
 *
 *     1. 只追加. 事件不可变, 不就地改.
 *     2. 按 (pid, seq) 归位, **不按到达顺序**. 乱序到达是允许的,
 *        重复到达也是允许的 (断线重连补齐必然重叠), 都要幂等.
 *     3. 订阅自带补齐: 给 fromSeq, 先历史后实时, 中间不许有断层.
 *
 *   多出来的一件事: **通知按帧合并**.
 *   内核一秒能吐几千条事件, 每条都同步通知一次 React 就是必卡.
 *   这里把"有变化"攒到下一帧再吐一次, 帧内多少条都只唤醒一次.
 *   —— 这不是优化, 这是这个界面能不能长期跑的前提.
 */

import { t, tt } from "../i18n/index.js";
import type { OsEvent, ProcessID } from "./events.js";

type Listener = () => void;

/**
 * forgotten 这条事件是不是"这几个被删掉了" —— 是就返回它们.
 *
 *	广播走的是 sense 那条系统流(见 cmd/neox-console 的 OnForget),
 *	所以它跟别的 proc.state 长得一样, 靠 phase 认.
 */
function forgotten(event: OsEvent): readonly ProcessID[] | null {
	if (event.kind !== "proc.state") return null;
	const body = event.payload as { phase?: string; pids?: readonly string[] };
	if (body.phase !== "forgotten" || !Array.isArray(body.pids)) return null;
	return body.pids as readonly ProcessID[];
}

export class EventStore {
  /** 每个进程一条有序数组, 下标 == seq (稀疏时用 holes 填) */
  readonly #byPid = new Map<ProcessID, OsEvent[]>();
  /** 下一条期望的 seq. **不能用 length 反推** —— 压紧后 length 会变小 */
  readonly #nextSeq = new Map<ProcessID, number>();
  readonly #listeners = new Set<Listener>();

  /**
   * version 是给 useSyncExternalStore 用的快照.
   *
   *   **必须是标量**. 返回对象/数组会因为每次新引用而无限重渲染 ——
   *   否则症状会是"看着没变但一直在 render".
   */
  #version = 0;
  #frame = 0;
  #timer = 0;
  #dirty = false;

  /** 统计: 收了多少条 / 通知了多少次. 用来证明合并真的在生效 */
  #ingested = 0;
  #notified = 0;

  get version(): number { return this.#version; }
  get ingested(): number { return this.#ingested; }
  get notified(): number { return this.#notified; }

  /** 追加一批. 重复 seq 直接丢弃 —— 补齐必然重叠 */
  append(events: readonly OsEvent[]): void {
    let changed = false;
    for (const event of events) {
      /**
       * **别处删掉的, 这儿也要跟着删**.
       *
       *	删如果只由界面自己先做, 再告诉服务器 —— 一个窗口一条路走下来
       *	是对的. 但从别处删的没人知道: 按接口清掉了 108 个进程、
       *	账本只剩一行, 而开着的那个窗口侧栏里 12 间屋子一间不少 ——
       *	点进去是空的.
       *
       *	删是**世界变了**, 不是这个窗口的私事.
       */
      const gone = forgotten(event);
      if (gone !== null) { this.forget(gone); continue; }
      let log = this.#byPid.get(event.pid);
      if (log === undefined) { log = []; this.#byPid.set(event.pid, log); }
      const next = this.#nextSeq.get(event.pid) ?? 0;
      if (event.seq < next && log[event.seq] !== undefined) continue; // 幂等
      log[event.seq] = event;
      if (event.seq >= next) this.#nextSeq.set(event.pid, event.seq + 1);
      this.#ingested += 1;
      changed = true;
    }
    if (changed) this.#scheduleNotify();
  }

  eventsOf(pid: ProcessID): readonly OsEvent[] {
    const log = this.#byPid.get(pid);
    if (log === undefined) return EMPTY;
    // 稀疏洞在补齐到达前是暂时的; 渲染时跳过, 不要当成"没有"
    return log;
  }

  pids(): readonly ProcessID[] { return [...this.#byPid.keys()]; }

  /**
   * 本地也删掉.
   *
   *   **服务端删了不等于这边删了**: 客户端这份是镜像, 不清的话
   *   界面上那条会话原地不动 —— 用户点了删除、什么都没发生.
   *   (第一次做这个功能时就是这样, 账本从 376 行掉到 347,
   *   而侧栏一条没少.)
   */
  forget(pids: readonly ProcessID[]): void {
    let changed = false;
    for (const pid of pids) {
      if (this.#byPid.delete(pid)) { this.#nextSeq.delete(pid); changed = true; }
    }
    if (changed) this.#scheduleNotify();
  }

  /**
   * 侧栏预览: 从末尾往回找最近一句话.
   *
   *   **有上限**. 不设上限的话, 一个刚灌了两万条 cap.used 的进程
   *   会让侧栏每帧倒扫两万条 —— 侧栏是最不该拖慢主线程的地方.
   *   上限要够拼回一整句(流式一块三个字), 所以是 400 不是 80.
   */
  preview(pid: ProcessID, limit = 400): string {
    return this.#lastSaid(pid, limit)?.text ?? "";
  }

  /**
   * 侧栏预览那句话是**什么时候**说的.
   *
   *   ── 为什么要它 ──
   *
   *   房间的摘要要取"最近说话的那个人"的最后一句. 原来比的是 highWater
   *   (事件条数) —— 那是**谁干得多**, 不是**谁刚说过**. 一个做完整个模块的
   *   bot 事件量远超刚上线的新人, 于是它永远赢: 房间里来了新人、说了话,
   *   侧栏那一行**一个字都不变**.
   *
   *   一对一同理: 一个 bot 跨多次重启有多个 pid, 旧 pid 事件多, 照样压住新的.
   */
  previewAt(pid: ProcessID, limit = 400): number {
    return this.#lastSaid(pid, limit)?.at ?? 0;
  }

  #lastSaid(pid: ProcessID, limit: number): { text: string; at: number } | null {
    const log = this.#byPid.get(pid);
    if (log === undefined) return null;
    const stop = Math.max(0, log.length - limit);
    for (let index = log.length - 1; index >= stop; index -= 1) {
      const event = log[index];
      if (event === undefined) continue;
      if (event.kind === "input.recv") {
        const said = event.payload as { text?: string; images?: readonly unknown[] };
        const text = String(said.text ?? "").trim();
        // **光发一张图也是说了话**: 侧栏那一行留空的话, 看着像这条会话
        // 什么都没发生
        const shots = said.images?.length ?? 0;
        return {
          text: text !== "" ? text : shots > 0 ? (shots > 1 ? tt("[{a} 张图]", { a: shots }) : t("[图片]")) : "",
          at: event.at
        };
      }
      if (event.kind !== "proc.output") continue;
      const tail = event.payload as { channel?: string; stream?: string; text?: string };
      if ((tail.channel ?? "say") !== "say" || typeof tail.text !== "string") continue;
      // 找到流的末块之后, **往回把同一条流拼回整句**.
      // 流式输出是一块三个字, 只取末块拿到的是"。" —— 摘要位上
      // 显示一个句号, 看着像渲染坏了.
      const parts = [tail.text];
      for (let back = index - 1; back >= stop; back -= 1) {
        const previous = log[back];
        if (previous === undefined || previous.kind !== "proc.output") break;
        const chunk = previous.payload as { channel?: string; stream?: string; text?: string };
        if (chunk.stream !== tail.stream || (chunk.channel ?? "say") !== "say") break;
        parts.unshift(chunk.text ?? "");
      }
      return { text: parts.join("").trim(), at: event.at };
    }
    return null;
  }

  /**
   * 这一轮干到哪儿了 —— **给"正在干活"那一行配个进度**.
   *
   *   ── 为什么不是百分比 ──
   *
   *   它不限轮次也不限步数, 干到哪儿算完由它自己判 —— 一个百分比要么
   *   是编的, 要么要它先承诺一个总数, 而那个承诺本身就是猜的.
   *
   *   能说的是**已经发生的事实**: 干了几步、跑了多久、花了多少.
   *   三样都是真的, 而且都在往上走 —— 人看的正是"它还在往前"这件事.
   *
   *   从**这一轮开始**算起: 轮的边界是 proc.state=running(见 rows.ts).
   */
  turnProgress(pid: ProcessID, limit = 400): { steps: number; startedAt: number; tokens: number } | null {
    const log = this.#byPid.get(pid);
    if (log === undefined) return null;
    const stop = Math.max(0, log.length - limit);
    let steps = 0;
    let tokens = 0;
    let startedAt = 0;
    for (let index = log.length - 1; index >= stop; index -= 1) {
      const event = log[index];
      if (event === undefined) continue;
      if (event.kind === "proc.state") {
        const state = (event.payload as { state?: string }).state;
        if (state === "running") { startedAt = event.at; break; }
        continue;
      }
      if (event.kind !== "proc.output") continue;
      const body = event.payload as { phase?: string; prompt?: number; completion?: number };
      if (body.phase === "step") steps += 1;
      if (body.phase === "usage") tokens += num(body.prompt) + num(body.completion);
    }
    return startedAt === 0 ? null : { steps, startedAt, tokens };
  }

  /**
   * 这个进程最近一次**合进主干**是什么时候、说了什么.
   *
   *   null = 从来没合过. 只往回看一小段: 通知问的是"刚刚有没有",
   *   而不是"这辈子有没有" —— 翻整段历史既慢又会在刚连上时把
   *   几小时前的合并全都提醒一遍.
   */
  lastMerged(pid: ProcessID, limit = 60): { seq: number; text: string } | null {
    const log = this.#byPid.get(pid);
    if (log === undefined) return null;
    const stop = Math.max(0, log.length - limit);
    for (let index = log.length - 1; index >= stop; index -= 1) {
      const event = log[index];
      if (event === undefined || event.kind !== "proc.output") continue;
      const body = event.payload as { phase?: string; text?: string };
      if (body.phase !== "merged") continue;
      return { seq: event.seq, text: String(body.text ?? t("合进主干了")) };
    }
    return null;
  }

  /** 高水位 —— 见过的最大 seq + 1. **不是**能拿来重连的游标, 见 haveThrough */
  highWater(pid: ProcessID): number { return this.#nextSeq.get(pid) ?? 0; }

  /**
   * 事件已经**连续收齐**到哪里 —— 第一个洞的位置, 也是断线重连时的 fromSeq.
   *
   *   ── 为什么不能用 highWater ──
   *
   *   highWater 是"见过的最大 seq + 1". 中间丢过一条时它在**洞的后面**:
   *   重连拿它当游标, 等于跟服务端说"洞那一段我有了", 于是那个洞
   *   **永远补不上**.
   *
   *   而渲染那侧是"停在洞前面, 绝不跳过"(rows.ts) —— 两件事合起来的
   *   后果是: 这一段对话从此不再更新, **一声不吭**, 而别的会话全是好的.
   *   典型表现是: 一个 bot 明明在等待决策, 点进去看到的却是
   *   上一次会话的旧消息, 刷新一下才对.
   *
   *   丢事件是真会发生的: 服务端 outbox 满了会丢**中间**那条, 后面的照发
   *   (observe.go 里那个 dropped), 然后推一条 gap 让界面重连补齐 ——
   *   补齐的前提正是游标得指在洞上.
   */
  haveThrough(pid: ProcessID): number {
    const log = this.#byPid.get(pid);
    if (log === undefined) return 0;
    const next = this.#nextSeq.get(pid) ?? 0;
    for (let seq = 0; seq < next; seq += 1) {
      if (log[seq] === undefined) return seq;
    }
    return next;
  }

  /**
   * 这个进程有没有**尚未解决**的人工决策.
   *
   *   ── 为什么要它 ──
   *
   *   界面把 OS 的 waiting 直接翻成"等待决策", 而 waiting 在内核里的意思是
   *   "停在 Recv 上等下一句话" —— 那是 bot 闲着的常态. 于是一屋子闲着的 bot
   *   全都顶着"等待人工决策"和一个橙色(warn)的点, **每一句都是假的**.
   *
   *   真正需要人工决策, 只有一种情况: 有一条 decide.requested 还没等到
   *   对应的 decide.resolved. 那件事账本里写着, 不用猜.
   */
  pendingDecision(pid: ProcessID): boolean {
    const open = new Set<string>();
    for (const event of this.eventsOf(pid)) {
      // **eventsOf 是稀疏的**(下标 == seq, 补齐到达前中间是洞), for...of
      // 会把洞当成 undefined 吐出来. 不挡这一下, 整个界面白屏 ——
      // 而渲染必须跳过这些洞, 否则整个界面会白屏.
      if (event === undefined) continue;
      if (event.kind === "decide.requested") {
        const did = (event.payload as { did?: unknown })?.did;
        if (typeof did === "string") open.add(did);
      } else if (event.kind === "decide.resolved") {
        const did = (event.payload as { did?: unknown })?.did;
        if (typeof did === "string") open.delete(did);
      }
    }
    return open.size > 0;
  }

  subscribe(listener: Listener): () => void {
    this.#listeners.add(listener);
    return () => { this.#listeners.delete(listener); };
  }

  /**
   * 帧合并.
   *   同一帧内来一万条事件, 只在帧末通知一次.
   *   注意 **version 也只加一次** —— 每条都加会让下游以为变了一万次.
   *
   * ── **不能只挂在 requestAnimationFrame 上** ──
   *
   *	窗口被别的窗口盖住时, Chromium 会把 rAF 停掉. 只挂 rAF 的话,
   *	事件照收、界面一动不动 —— 等你切回来才"哗"地一次性补上.
   *	典型时序是: SSE 里 0.0s 收到输入、4.0s 收到回复,
   *	界面 21s 之后才动. 那条"正在想…"于是说的是十几秒前的事 ——
   *	用户看到的就是"显示了很久才消失".
   *
   *	所以再挂一个定时器兜底, 谁先到算谁的: 平时是 rAF(跟着刷新率),
   *	rAF 被停掉时是定时器. 两条路都只让 version 加一次.
   */
  #scheduleNotify(): void {
    if (this.#dirty) return;
    this.#dirty = true;
    const fire = () => {
      if (!this.#dirty) return;
      this.#dirty = false;
      cancelAnimationFrame(this.#frame);
      clearTimeout(this.#timer);
      this.#version += 1;
      this.#notified += 1;
      for (const listener of [...this.#listeners]) listener();
    };
    this.#frame = requestAnimationFrame(fire);
    // 100ms: 比一帧慢得多(平时永远轮不到它), 又比人察觉得到的延迟短得多
    this.#timer = setTimeout(fire, 100) as unknown as number;
  }

  dispose(): void {
    cancelAnimationFrame(this.#frame);
    clearTimeout(this.#timer);
    this.#listeners.clear();
  }
}

const EMPTY: readonly OsEvent[] = Object.freeze([]);

/** 账本装回来的数字过了一趟 JSON, 可能是任何东西 —— 认不出就当 0 */
function num(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}
