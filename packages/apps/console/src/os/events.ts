/**
 * events — 跟 go/abi/types.go 的 Event 一字不差的镜像.
 *
 *   这一份**不许自己发明字段**. UI 是订阅者, 不是真相源;
 *   凡是这里有而内核没有的字段, 迟早会变成"只有界面知道的状态",
 *   那正是旧世界观里 Timeline* 渗进内核的第一步.
 */

export type ProcessID = string;

export type ProcessState =
  | "created" | "running" | "waiting" | "suspended" | "exited" | "failed";

/** go/abi/types.go 的 EventKind 全集 */
export type EventKind =
  | "proc.state" | "proc.output" | "proc.outcome"
  | "decide.requested" | "decide.resolved"
  | "cap.used" | "cap.denied"
  | "budget.spent"
  | "input.recv"
  | "user.correction"
  | "signal.in" | "signal.digest" | "signal.late" | "signal.muted" | "signal.unmuted"
  | "wake.set" | "wake.fired" | "wake.cancelled"
  | "place.named"
  | "watch.set" | "watch.removed"
  | "daily.report"
  | "interrupt.verdict"
  | "collector.down" | "collector.up";

export interface OsEvent {
  /** 进程内单调递增, 从 0 开始. **归位靠它, 不靠到达顺序** */
  readonly seq: number;
  readonly pid: ProcessID;
  readonly at: number;
  readonly kind: EventKind;
  readonly payload: unknown;
}

export type CapAxis = "read" | "write" | "net" | "proc" | "secret";

/* ── payload 形状 (只声明我们真的要渲染的那些) ─────────────── */

export interface ProcStatePayload { state: ProcessState; reason?: string }
/**
 * 输出块. `stream` 把同一条流的连续块折成一行 ——
 * **没有它, 一次 8000 块的模型输出就是 8000 个 DOM 行**.
 */
export interface ProcOutputPayload {
  stream: string; text: string; channel?: "say" | "think" | "tool";
  /** 这一块就是全部, 后面不会再有 —— 界面据此当场封口, 不用等进程状态变 */
  done?: boolean;
}
/**
 * 决策请求.
 *
 *   **`present` 是 OS 定义的即时 UI 词汇**, 不是界面自己发明的:
 *   go/abi/types.go 的 PresentSpec.Kind 已经写死了
 *   choice|approve|form|progress|table|doc|media 七种.
 *   界面要做的是**认领这套词汇**, 而不是另起一套自己的卡片类型 ——
 *   另起一套就等于把"该长什么样"从 OS 挪进了客户端, 下一个客户端就对不上了.
 */
export interface PresentSpec {
  kind: "choice" | "approve" | "form" | "progress" | "table" | "doc" | "media" | string;
  title: string;
  detail?: string;
  options?: { value: string; label: string; detail?: string }[];
  fields?: { name: string; label?: string; placeholder?: string }[];
  diff?: string;
  ratio?: number;
  note?: string;
  columns?: string[];
  rows?: string[][];
  body?: string;
}
export interface DecideRequestPayload { did: string; present: PresentSpec; urgency?: string }
/** choice === "yes" 才算批准 —— 这是 OS 侧 grantFromDecision 的判据 */
export interface DecideResolvedPayload { did: string; choice: string; by: string; values?: Record<string, unknown> }
export interface CapPayload { axis: CapAxis; scope: string; mechanism?: string }
export interface BudgetPayload { tokensIn?: number; tokensOut?: number; bytes?: number; wallMs?: number }
export interface OutcomePayload { ok: boolean; summary: string; evidence?: string }
/** 跟这句话一起发来的图 —— 图本身在磁盘上, 这里只有 id(见 go/osinit/blobs.go) */
export interface ShotRef { id: string; name?: string; mime?: string }
/** 附件(非图): 账本里只有名字 —— 字节归 bot 读, 界面画一枚标签就够 */
export interface FileRef { name: string }
export interface InputPayload {
  text: string;
  images?: readonly ShotRef[];
  files?: readonly FileRef[];
  /** 谁说的 */
  from?: string;
  /** true = 同屋 bot 交办的, 不是当前用户发出的 —— 画错了会把非用户消息显示成用户消息 */
  relay?: boolean;
}
export interface WakePayload { at: number; why: string }
export interface SignalPayload { source: string; summary: string }

export function payloadOf<T>(event: OsEvent): T {
  return event.payload as T;
}
