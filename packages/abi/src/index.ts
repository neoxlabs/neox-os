/**
 * NeoxABI — Neox OS 的系统调用契约.
 *
 *   这个包**只有类型和常量, 没有实现**. 它是 OS 与应用之间唯一的接口面.
 *   实现在 @neox-os/init, 应用在别处. 两边都只依赖本包.
 *
 *   稳定性分级 (见 docs/ABI-v0.md):
 *     stable   — 破坏性变更需要大版本 + 12 个月弃用期
 *     unstable — 可随时改, 应用必须显式 opt-in
 *     internal — 不对外, 随时消失
 *
 *   v0 阶段**全部标 unstable**. 基础运行链路稳定之后才冻结第一批 stable.
 *   在此之前不提升任何原语到 stable — 这是成本最低、最容易发现契约问题的阶段.
 */

export const ABI_VERSION = '0.0.1-unstable';

export type Stability = 'stable' | 'unstable' | 'internal';

/* ────────────────────────────────────────────────────────────
 * 内核对象 ① Process — 有生命周期的计算
 *
 *   关键: Process 不需要观众. 它的存在不依赖任何客户端连接,
 *   不依赖"轮次", 不依赖有人在看. 这是 Neox OS 跟 agent IDE 的分界线.
 * ──────────────────────────────────────────────────────────── */

export type ProcessId = string;

export type ProcessState =
  /** 已登记, 还没开始跑 */
  | 'created'
  /** 正在占用计算 */
  | 'running'
  /** 在等外部事件 (决策 / 定时 / 外部回调). 不占计算, 但活着 */
  | 'waiting'
  /** 被 OS 换出. 状态已落盘, 计算资源已归还. 可被唤醒 */
  | 'suspended'
  /** 正常结束 */
  | 'exited'
  /** 异常结束 */
  | 'failed';

/** 终态 — 到这里就不会再变 */
export const TERMINAL_STATES: readonly ProcessState[] = ['exited', 'failed'];

export interface ProcessSpec {
  /** 哪个应用起的. OS 按应用做隔离与配额 */
  app: string;
  /** 人类可读的名字, 仅用于呈现 */
  name?: string;
  /** 这个进程被授予的能力集. OS 按此做保护, 不是"问不问用户" */
  caps: Capability[];
  /** 资源预算. 超出即由 OS 终止, 不依赖进程自觉 */
  budget?: Budget;
  /** 任意标签, 供调度与检索 */
  labels?: Record<string, string>;
}

/**
 * 进程体 — 进程"是什么".
 *
 *   `exec` 是唯一的生产路径: 一个**真正的 OS 进程**, 被内核约束
 *   (namespace + cgroup + landlock + seccomp). 它不调 can() 也做不到
 *   没授权的事 —— 能力是**强制**的, 不是问询的.
 *
 *   `inproc` 是同一个 JS 堆里的闭包. 它跑得快、好调试, 但**内核管不着它**:
 *   它可以绕过 can() 直接 import fs. 因此只允许在 dev 模式使用,
 *   confined 模式下 spawn 一个 inproc 进程会被直接拒绝 (fail-closed).
 *
 *   这条区分是"OS"和"一个说话像 OS 的库"的分界线.
 */
export type ProcessBody =
  | {
      kind: 'exec';
      /** argv[0] 是可执行文件. OS 会在前面插入约束启动器 */
      argv: string[];
      /** 卷内工作目录 */
      cwd: string;
      /** 额外环境变量. OS 会剥掉宿主环境, 只留这些 + OS 注入的 */
      env?: Record<string, string>;
    }
  | {
      kind: 'inproc';
      entry: ProcessEntry;
    };

/**
 * OS 运行模式.
 *   confined — 生产. 内核必须能强制约束, 否则拒绝启动进程
 *   dev      — 开发/测试. 允许 inproc, 约束退化为记账
 *
 * 模式是**启动时确定的**, 不能在运行中切换 —— 否则就成了后门.
 */
export type OsMode = 'confined' | 'dev';

export interface ProcessInfo {
  pid: ProcessId;
  spec: ProcessSpec;
  state: ProcessState;
  /** 创建时刻 (epoch ms) */
  createdAt: number;
  /** 最后一次状态变更时刻 */
  changedAt: number;
  /** 已写入的事件条数 = 下一条事件的 seq */
  logLength: number;
  /** 终态时的结果或错误 */
  outcome?: ProcessOutcome;
}

export interface ProcessOutcome {
  ok: boolean;
  /** 结构化结果. 不是给人看的文本 */
  value?: unknown;
  error?: { code: string; message: string };
}

/* ────────────────────────────────────────────────────────────
 * 内核对象 ② 事件日志 — 只追加, 单调递增, 可重放
 *
 *   关键: 这是唯一的观察通道. UI 不是特权观察者, 它跟推送、IM、
 *   审计一样只是一个订阅者. 任何客户端可以在任意时刻接入,
 *   从任意 seq 重放, 得到完全一致的过程.
 * ──────────────────────────────────────────────────────────── */

export interface OsEvent<P = unknown> {
  /** 进程内单调递增, 从 0 开始. 全局有序性只在单进程内保证 */
  seq: number;
  pid: ProcessId;
  /** epoch ms */
  at: number;
  kind: EventKind;
  payload: P;
}

export type EventKind =
  /** 进程状态变更 */
  | 'proc.state'
  /** 进程产出的结构化输出 (不是"消息", 没有说话人) */
  | 'proc.output'
  /** 进程请求一个人类决策, 并转入 waiting */
  | 'decide.requested'
  /** 决策被解决 (由任意订阅者, 任意时刻, 任意通道) */
  | 'decide.resolved'
  /** 能力被使用 — 审计面 */
  | 'cap.used'
  /** 能力被拒 */
  | 'cap.denied'
  /** 预算消耗 */
  | 'budget.spent'
  /** 进程结束 */
  | 'proc.outcome';

export interface Subscription {
  close(): void;
}

/* ────────────────────────────────────────────────────────────
 * 内核对象 ③ 能力 (Capability) — 主体能不能做, 而不是要不要问人
 *
 *   四轴模型直接沿用 neox-sandbox 已验证的抽象 (读/写/网络/进程).
 *   区别是: 这里它是**授予进程的**, 不是"当前会话的设置".
 * ──────────────────────────────────────────────────────────── */

export type CapabilityAxis = 'read' | 'write' | 'net' | 'proc' | 'secret';

export interface Capability {
  axis: CapabilityAxis;
  /**
   * 作用域. 语义随 axis 变:
   *   read/write → 卷内路径前缀 (以 / 开头, 相对卷根)
   *   net        → host 或 host:port, 支持 *.example.com
   *   proc       → 允许 spawn 的 app 名
   *   secret     → 凭据名
   */
  scope: string;
}

/* ────────────────────────────────────────────────────────────
 * 内核对象 ④ 预算 — OS 强制, 不靠进程自觉
 * ──────────────────────────────────────────────────────────── */

export interface Budget {
  /** 墙钟上限 (ms). 注意: 这是**总活跃时长**, waiting/suspended 不计入 */
  activeMs?: number;
  /** 模型 token 上限 */
  tokens?: number;
  /** 出网请求次数上限 */
  netCalls?: number;
}

export interface BudgetSpent {
  activeMs: number;
  tokens: number;
  netCalls: number;
}

/* ────────────────────────────────────────────────────────────
 * 决策 — 核心原语
 *
 *   这是"不需要人在场"的关键: 进程遇到需要人决定的地方,
 *   不是阻塞等一个连着的 UI, 而是**登记一个待决策并转入 waiting**.
 *   OS 负责把它路由到能找到人的通道 (推送/IM/桌面/网页).
 *   人在任意时刻、任意设备上解决它, 进程被唤醒继续.
 *
 *   进程可以在决策未解决时被 suspend 并换出. 决策活得比进程的
 *   任何一次运行都长.
 * ──────────────────────────────────────────────────────────── */

export type DecisionId = string;

export interface DecisionRequest {
  /** 语义化的呈现意图. 各端自己决定长什么样, OS 不描述像素 */
  present: PresentSpec;
  /**
   * 这次授权针对**哪个资源** (路径/主机名).
   *
   * 光有 present 里的文案不够: 批准之后 OS 要知道到底授了什么,
   * 从标题里抠字符串只能猜. landlock 的规则在进程启动时就定死、只能
   * 收紧不能放宽; 如果没有明确的 scope, 就会出现**问了人、人说可以、
   * 还是做不到**.
   * 有了它, 批准才能记进对话的授权表, 下个进程带着它起来.
   */
  scope?: string;
  /** 超时未解决怎么办 */
  onTimeout?: { afterMs: number; choose: string };
  /** 紧急程度 — 触达层据此决定要不要打断人 */
  urgency?: 'low' | 'normal' | 'high';
}

export interface PendingDecision {
  did: DecisionId;
  pid: ProcessId;
  request: DecisionRequest;
  requestedAt: number;
}

export interface DecisionResolution {
  did: DecisionId;
  /** 选中的 option id, 或 form 的字段值 */
  choice: string;
  values?: Record<string, unknown>;
  /** 谁解决的 — 审计用. 'timeout' 表示 OS 按 onTimeout 兜底 */
  by: string;
  at: number;
}

/* ────────────────────────────────────────────────────────────
 * 呈现协议 — 描述语义, 不描述像素
 *
 *   白名单封闭. 进程只能从中选, 不能生成任意代码.
 *   这既是安全边界, 也是"任意客户端都能渲染"的保证.
 * ──────────────────────────────────────────────────────────── */

export type PresentSpec =
  | { kind: 'choice'; title: string; detail?: string; options: PresentOption[] }
  | { kind: 'approve'; title: string; detail?: string; diff?: string }
  | { kind: 'form'; title: string; fields: PresentField[] }
  | { kind: 'progress'; title: string; ratio?: number; note?: string }
  | { kind: 'table'; title: string; columns: string[]; rows: string[][] }
  | { kind: 'doc'; title: string; body: string }
  | { kind: 'media'; title: string; volumePath: string; mime: string };

export interface PresentOption {
  id: string;
  label: string;
  detail?: string;
  /** 这个选项是不是破坏性的 — 客户端据此决定要不要二次确认 */
  destructive?: boolean;
}

export interface PresentField {
  id: string;
  label: string;
  type: 'text' | 'number' | 'bool' | 'select';
  options?: PresentOption[];
  required?: boolean;
}

/* ────────────────────────────────────────────────────────────
 * 系统调用面
 *
 *   应用拿到的全部东西就是这个接口. 没有别的入口.
 * ──────────────────────────────────────────────────────────── */

/** 进程自己能调的 — 由 OS 注入给进程入口函数 */
export interface ProcessContext {
  readonly pid: ProcessId;
  readonly abiVersion: string;

  /** 产出结构化输出. 会进事件日志, 所有订阅者可见 */
  emit(payload: unknown): void;

  /**
   * 请求一个人类决策. 进程在此转入 waiting 并可被换出.
   * 返回的 Promise 在决策被解决 (或超时兜底) 时 resolve.
   */
  decide(req: DecisionRequest): Promise<DecisionResolution>;

  /** 检查自己有没有某个能力. 没有就别做, 做了会被 OS 拒 */
  can(axis: CapabilityAxis, scope: string): boolean;

  /** 记一笔预算消耗. 超预算时抛 BudgetExceeded */
  spend(delta: Partial<BudgetSpent>): void;

  /** 进程被 OS 要求退出时触发 (预算耗尽 / 管理员终止 / 关机) */
  readonly signal: AbortSignal;
}

/** 进程入口 — 应用提供的用户态程序 */
export type ProcessEntry = (ctx: ProcessContext) => Promise<unknown>;

/** OS 对外的控制面 — 客户端 (shell) 和管理面用 */
export interface OsSyscalls {
  readonly abiVersion: string;

  readonly mode: OsMode;

  proc: {
    spawn(spec: ProcessSpec, body: ProcessBody): ProcessId;
    info(pid: ProcessId): ProcessInfo | undefined;
    list(filter?: { app?: string; state?: ProcessState }): ProcessInfo[];
    /** 请求终止. 进程通过 ctx.signal 感知 */
    kill(pid: ProcessId, reason: string): void;
  };

  log: {
    /** 从 fromSeq 开始重放已有事件 (含 fromSeq) */
    replay(pid: ProcessId, fromSeq?: number): OsEvent[];
    /**
     * 订阅. 先把 fromSeq 起的历史补齐, 再接实时 —— 订阅者永远看不到断层.
     * 这是"任意时刻接入都能重建完整过程"的实现点.
     */
    subscribe(
      pid: ProcessId,
      onEvent: (e: OsEvent) => void,
      fromSeq?: number,
    ): Subscription;
    /** 跨进程订阅 — 触达层用 */
    subscribeAll(onEvent: (e: OsEvent) => void): Subscription;
  };

  decide: {
    /** 当前所有待决策 — 任意客户端接入时先拉这个 */
    pending(): PendingDecision[];
    /** 解决一个决策. 可以来自任意通道: 桌面/手机/IM/网页 */
    resolve(did: DecisionId, choice: string, by: string, values?: Record<string, unknown>): void;
  };
}

/* ────────────────────────────────────────────────────────────
 * 约束 — 能力如何变成内核规则
 *
 *   Capability 是**声明**, ConfinementPlan 是它翻译成的**内核可执行规则**.
 *   翻译是纯函数, 可以在任何平台上测; 施加是 Linux-only.
 *
 *   Fail-closed 铁律: 内核不支持某一轴的强制 → 不降级、不警告后继续,
 *   直接拒绝启动进程. 这跟"不许 fallback"是同一条.
 * ──────────────────────────────────────────────────────────── */

export interface ConfinementPlan {
  /** landlock 文件系统规则 — 路径 → 允许的访问. **来自用户授予的能力** */
  fs: { path: string; access: ('read' | 'write')[] }[];
  /**
   * 执行底座 — OS 恒定授予的只读路径, 不来自任何能力.
   *
   *   没有它进程连自己的可执行文件都读不到, 会直接 `exec failed: Permission denied`.
   *   缺少这些路径会让进程直接得到 `exec failed: Permission denied`.
   *
   *   跟 fs 分开存是刻意的: 审计时要一眼看出**哪些是用户授的、哪些是系统给的**.
   */
  runtimeFs: string[];
  /**
   * 必须**可写**的设备节点, 同样恒定授予.
   *
   *   不能并进 runtimeFs: 那些是只读的, 而 `> /dev/null` 是写操作.
   *   少了它时, 20 个后台进程都会挂在
   *   `cannot open /dev/null: Permission denied`.
   *
   *   逐个列而不是整个 /dev —— 整个 /dev 可写等于把裸磁盘和物理内存
   *   一起给出去.
   */
  runtimeDevices: string[];
  /** 网络: 允许出网的目标. 空数组 = 完全断网 */
  net: { host: string; port?: string }[];
  /**
   * 是否允许 fork/exec 子进程.
   *
   *   **这是配额宽窄的开关, 不是安全开关.** landlock/netns/cgroup 全部
   *   随 fork+exec 继承, 子进程跟父进程关在同一个笼子里 ——
   *   起进程不构成逃逸. 想真的禁止要靠 seccomp 拦 clone/execve,
   *   这里不拦, 因为在已有继承约束下拦住它也没有安全收益.
   */
  allowSubprocess: boolean;
  /** cgroup v2 限额 */
  cgroup: { memoryMaxBytes?: number; cpuWeight?: number; pidsMax?: number };
  /** 需要内核提供哪些强制机制. probe 少一条即 fail-closed */
  requires: EnforcementRequirement[];
}

/**
 * 强制**需求** — 系统要达成什么效果.
 *
 *   刻意跟具体机制解耦: 同一个需求可以由不同内核机制满足.
 *   写死机制名会让系统在一台明明能强制的机器上拒绝启动
 *   (某些 linuxkit 内核没有 landlock, 但有 BPF-LSM).
 */
export type EnforcementRequirement =
  /** 文件系统访问强制 — 可由 landlock 或 bpf-lsm 满足 */
  | 'fs-enforce'
  /** 资源限额 — cgroup v2 */
  | 'resource-limit'
  /** 网络隔离 — netns */
  | 'net-isolate'
  /** 系统调用拦截并交给用户态裁决 — seccomp user_notif. 审批下沉到 syscall 用 */
  | 'syscall-notify';

/** 具体内核机制 */
export type EnforcementMechanism =
  | 'landlock'
  | 'bpf-lsm'
  | 'cgroup2'
  | 'netns'
  | 'seccomp'
  | 'seccomp-user-notif';

export interface EnforcementProbe {
  platform: string;
  /** 内核支持、**且我们已经接入**的机制. 只支持不接入不算 —— 那会 fail-open */
  mechanisms: EnforcementMechanism[];
  /** 内核支持但我们还没接入的 — 只用于诊断, 不参与满足判定 */
  detectedNotWired: EnforcementMechanism[];
  satisfied: EnforcementRequirement[];
  missing: EnforcementRequirement[];
  /** 全部需求都被满足才为 true. false 时 confined 模式必须拒绝启动 */
  usable: boolean;
  /** 人类可读的原因, 用于启动失败时给出确定答复 */
  reason?: string;
}

/** 内核无法强制约束 — confined 模式下的启动失败 */
export class ConfinementUnavailable extends Error {
  constructor(public readonly probe: EnforcementProbe) {
    super(`confinement unavailable: ${probe.reason ?? probe.missing.join(', ')}`);
    this.name = 'ConfinementUnavailable';
  }
}

/** 预算超限 — OS 强制终止的原因之一 */
export class BudgetExceeded extends Error {
  constructor(public readonly axis: keyof BudgetSpent) {
    super(`budget exceeded: ${axis}`);
    this.name = 'BudgetExceeded';
  }
}

/* 线格式 — 跨语言边界. 见 wire.ts */
export * from './wire.js';
