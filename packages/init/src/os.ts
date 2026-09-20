/**
 * os — neox-init 的核心: 进程表 + 系统调用实现.
 *
 *   这个模块**不跑 agent**, 它调度跑 agent 的东西. 进程入口是应用提供的
 *   用户态程序 (ProcessEntry), OS 只负责: 分配 pid、注入 ctx、记事件、
 *   管状态机、算预算、在需要人决策时把进程挂起并把决策交给触达层.
 *
 *   跟旧架构最重要的三处不同:
 *
 *   1. 进程不依附于任何"会话"或客户端连接. 关掉所有客户端, 进程照跑.
 *   2. 观察全部走事件日志. OS 自己不持有任何"给人看的"渲染态.
 *   3. 等人决策时进程转 waiting, 不占计算 —— 于是"等一天"和"等一秒"
 *      对资源的成本是一样的. 这是 24h 常驻能省钱的技术前提.
 */

import { spawn as spawnProcess } from 'node:child_process';
import { existsSync } from 'node:fs';

import type {
  Budget,
  BudgetSpent,
  CapabilityAxis,
  DecisionRequest,
  EnforcementRequirement,
  EnforcementProbe,
  OsEvent,
  OsMode,
  OsSyscalls,
  ProcessBody,
  ProcessContext,
  ProcessId,
  ProcessInfo,
  ProcessOutcome,
  ProcessSpec,
  ProcessState,
  Subscription,
} from '@neox-os/abi';
import { ABI_VERSION, BudgetExceeded, ConfinementUnavailable, TERMINAL_STATES } from '@neox-os/abi';
import { buildLaunch, planFor, probeEnforcement, resolveRuntimeFs } from '@neox-os/confine';

import { EventLog } from './eventLog.js';
import { DecisionRegistry } from './decisions.js';
import { capabilityAllows } from './capabilities.js';

/**
 * 子进程句柄 — 抽掉 node:child_process 的形状, 让 OS 不直接依赖它.
 * 生产用 defaultSpawner, 测试注入假的.
 */
export interface ChildHandle {
  onLine(cb: (line: string) => void): void;
  onExit(cb: (code: number | null, signal: string | null) => void): void;
  kill(signal: string): void;
}

export type Spawner = (
  argv: string[],
  opts: { cwd: string; env: Record<string, string> },
) => ChildHandle;

const defaultSpawner: Spawner = (argv, opts) => {
  const child = spawnProcess(argv[0]!, argv.slice(1), {
    cwd: opts.cwd,
    env: opts.env,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  return {
    onLine: (cb) => {
      child.stdout?.on('data', (b: Buffer) => {
        for (const line of b.toString('utf8').split('\n')) cb(line);
      });
    },
    onExit: (cb) => {
      child.on('error', () => cb(null, 'spawn_error'));
      child.on('exit', (code, signal) => cb(code, signal));
    },
    kill: (sig) => void child.kill(sig as NodeJS.Signals),
  };
};

interface ProcRecord {
  info: ProcessInfo;
  abort: AbortController;
  spent: BudgetSpent;
  /** 进入 running 的时刻, 用于累计 activeMs. 非 running 时为 null */
  runningSince: number | null;
}

export interface OsOptions {
  now?: () => number;
  /**
   * 运行模式. **缺省是 confined** —— 缺省必须是安全的那个.
   * dev 模式必须显式要, 而且它只能跑 inproc 进程 (跑不了真程序).
   */
  mode?: OsMode;
  /** 卷根. confined 模式下所有能力 scope 相对它解析 */
  volumeRoot?: string;
  /** 执行底座. 缺省按本机实际存在的路径解析 */
  runtimeFs?: string[];
  /** 探测内核能力的实现 — 测试可注入 */
  probe?: (required: readonly EnforcementRequirement[]) => EnforcementProbe;
  /**
   * 起子进程的实现 — 测试可注入.
   * 测试**绝不能真的 spawn 系统二进制**: 慢、不可移植、而且在受限环境里
   * 会直接把测试进程树打死. 命令行的正确性由 buildLaunch 的纯函数测试锁.
   */
  spawner?: Spawner;
}

export class NeoxOs implements OsSyscalls {
  readonly abiVersion = ABI_VERSION;
  readonly mode: OsMode;

  private readonly volumeRoot: string;
  /** 本机实际存在的执行底座 —— 启动时解析一次, 之后不再碰文件系统 */
  private readonly runtimeFs: string[];
  private readonly probeFn: (r: readonly EnforcementRequirement[]) => EnforcementProbe;
  private readonly spawner: Spawner;
  private readonly procs = new Map<ProcessId, ProcRecord>();
  /**
   * did → pid 索引.
   *   纯派生数据: 完全可以从事件日志重放得出 (decide.requested 带 did).
   *   放这里只是为了 O(1) 查找 —— 原来的实现是遍历所有进程的全部日志,
   *   O(进程数 × 日志长度), 长跑之后会明显变慢.
   *   恢复场景下必须由 rebuildIndex() 重建, 不能落盘当真相源.
   */
  private readonly decisionOwner = new Map<string, ProcessId>();
  private readonly events: EventLog;
  private readonly decisions: DecisionRegistry;
  private readonly now: () => number;
  private counter = 0;

  constructor(opts: OsOptions = {}) {
    this.now = opts.now ?? (() => Date.now());
    this.mode = opts.mode ?? 'confined';
    this.volumeRoot = opts.volumeRoot ?? '/work';
    this.runtimeFs = opts.runtimeFs ?? resolveRuntimeFs(existsSync);
    this.probeFn = opts.probe ?? probeEnforcement;
    this.spawner = opts.spawner ?? defaultSpawner;
    this.events = new EventLog(this.now);
    this.decisions = new DecisionRegistry(this.now, {
      onRequested: (p) => {
        this.events.append(p.pid, 'decide.requested', {
          did: p.did,
          present: p.request.present,
          urgency: p.request.urgency ?? 'normal',
        });
        this.decisionOwner.set(p.did, p.pid);
        /* 进程在等人 → 不占计算. 触达层从事件流里看到这条, 决定要不要打断人 */
        this.setState(p.pid, 'waiting');
      },
      onResolved: (r) => {
        /* r 已经带了 values —— 不需要再去 settled 表里查一遍 */
        const pid = this.decisionOwner.get(r.did);
        if (pid) {
          this.events.append(pid, 'decide.resolved', {
            did: r.did,
            choice: r.choice,
            by: r.by,
            values: r.values,
          });
          this.setState(pid, 'running');
        }
      },
    });
  }

  /* ── 进程 ─────────────────────────────────────────────── */

  proc = {
    spawn: (spec: ProcessSpec, body: ProcessBody): ProcessId => {
      /* ── Fail-closed 闸. 在分配 pid 之前拦, 不合法的组合根本不存在 ──
       *
       *   confined + exec   → 唯一的生产路径. 内核必须真能强制, 否则拒绝
       *   dev      + inproc → 开发路径. 约束退化为记账, 只能跑闭包
       *   confined + inproc → 拒. 闭包在 OS 自己的堆里, 内核管不着它
       *   dev      + exec   → 拒. 那会是一个**没有任何约束的真进程**,
       *                       比什么都不做更危险. 不给这条路. */
      if (this.mode === 'confined' && body.kind === 'inproc') {
        throw new Error(
          'confined 模式不接受 inproc 进程: 同堆闭包不受内核约束, 能力检查会形同虚设',
        );
      }
      if (this.mode === 'dev' && body.kind === 'exec') {
        throw new Error(
          'dev 模式不接受 exec 进程: 那会起一个完全不受约束的真进程. 要跑真程序请用 confined 模式',
        );
      }
      if (body.kind === 'exec') {
        const plan = planFor(spec.caps, { volumeRoot: this.volumeRoot, runtimeFs: this.runtimeFs });
        const probe = this.probeFn(plan.requires);
        /* 内核拦不住 → 不降级、不 warning 后继续, 直接拒绝启动 */
        if (!probe.usable) throw new ConfinementUnavailable(probe);
      }

      const pid: ProcessId = `p${++this.counter}`;
      const at = this.now();
      const rec: ProcRecord = {
        info: {
          pid,
          spec,
          state: 'created',
          createdAt: at,
          changedAt: at,
          logLength: 0,
        },
        abort: new AbortController(),
        spent: { activeMs: 0, tokens: 0, netCalls: 0 },
        runningSince: null,
      };
      this.procs.set(pid, rec);
      this.events.append(pid, 'proc.state', { state: 'created' });

      /* 立刻转 running 并异步执行. spawn 不等进程跑完 —— 调用方拿到 pid
       * 就可以走人, 这正是"不需要观众"的入口. */
      this.setState(pid, 'running');
      void this.run(pid, body);
      return pid;
    },

    info: (pid: ProcessId): ProcessInfo | undefined => {
      const rec = this.procs.get(pid);
      if (!rec) return undefined;
      return { ...rec.info, logLength: this.events.length(pid) };
    },

    list: (filter?: { app?: string; state?: ProcessState }): ProcessInfo[] => {
      const out: ProcessInfo[] = [];
      for (const rec of this.procs.values()) {
        if (filter?.app && rec.info.spec.app !== filter.app) continue;
        if (filter?.state && rec.info.state !== filter.state) continue;
        out.push({ ...rec.info, logLength: this.events.length(rec.info.pid) });
      }
      return out;
    },

    kill: (pid: ProcessId, reason: string): void => {
      const rec = this.procs.get(pid);
      if (!rec || TERMINAL_STATES.includes(rec.info.state)) return;
      rec.abort.abort(new Error(reason));
    },
  };

  /* ── 事件日志 ─────────────────────────────────────────── */

  /* 公开面. 内部实现持有的 EventLog 叫 this.events —— 名字分开是刻意的:
   * 应用只能看到这三个方法, 拿不到日志实例, 就写不进别的进程的日志. */
  log = {
    replay: (pid: ProcessId, fromSeq?: number): OsEvent[] => this.events.replay(pid, fromSeq),
    subscribe: (pid: ProcessId, on: (e: OsEvent) => void, fromSeq?: number): Subscription =>
      this.events.subscribe(pid, on, fromSeq),
    subscribeAll: (on: (e: OsEvent) => void): Subscription => this.events.subscribeAll(on),
  };

  /* ── 决策 ─────────────────────────────────────────────── */

  decide = {
    pending: () => this.decisions.pending(),
    resolve: (
      did: string,
      choice: string,
      by: string,
      values?: Record<string, unknown>,
    ): void => {
      this.decisions.resolve(did, choice, by, values);
    },
  };

  /* ── 内部 ─────────────────────────────────────────────── */

  /** 从事件日志重建派生索引 —— 恢复/重挂时调用. 索引永远不是真相源 */
  rebuildIndex(): void {
    this.decisionOwner.clear();
    for (const rec of this.procs.values()) {
      for (const e of this.events.replay(rec.info.pid)) {
        if (e.kind === 'decide.requested') {
          this.decisionOwner.set((e.payload as { did: string }).did, rec.info.pid);
        }
      }
    }
  }

  private setState(pid: ProcessId, state: ProcessState): void {
    const rec = this.procs.get(pid);
    if (!rec || rec.info.state === state) return;

    /* 离开 running 时结算活跃时长 —— waiting/suspended 不计入预算,
     * 这是"等一天和等一秒成本相同"的实现点 */
    if (rec.runningSince !== null) {
      rec.spent.activeMs += this.now() - rec.runningSince;
      rec.runningSince = null;
    }
    if (state === 'running') rec.runningSince = this.now();

    rec.info.state = state;
    rec.info.changedAt = this.now();
    this.events.append(pid, 'proc.state', { state });
  }

  private makeContext(pid: ProcessId): ProcessContext {
    const rec = this.procs.get(pid)!;
    const self = this;

    return {
      pid,
      abiVersion: ABI_VERSION,
      signal: rec.abort.signal,

      emit(payload: unknown): void {
        self.events.append(pid, 'proc.output', payload);
      },

      decide(req: DecisionRequest) {
        return self.decisions.request(pid, req);
      },

      can(axis: CapabilityAxis, scope: string): boolean {
        const ok = capabilityAllows(rec.info.spec.caps, axis, scope);
        self.events.append(pid, ok ? 'cap.used' : 'cap.denied', { axis, scope });
        return ok;
      },

      spend(delta: Partial<BudgetSpent>): void {
        const s = rec.spent;
        s.tokens += delta.tokens ?? 0;
        s.netCalls += delta.netCalls ?? 0;
        if (delta.activeMs) s.activeMs += delta.activeMs;
        self.events.append(pid, 'budget.spent', { ...delta });

        const b: Budget | undefined = rec.info.spec.budget;
        if (!b) return;
        /* 预算由 OS 强制, 不靠进程自觉 —— 超了直接抛, 进程无处可躲 */
        if (b.tokens !== undefined && s.tokens > b.tokens) throw new BudgetExceeded('tokens');
        if (b.netCalls !== undefined && s.netCalls > b.netCalls) throw new BudgetExceeded('netCalls');
        if (b.activeMs !== undefined && self.activeMsOf(rec) > b.activeMs) {
          throw new BudgetExceeded('activeMs');
        }
      },
    };
  }

  private activeMsOf(rec: ProcRecord): number {
    return rec.spent.activeMs + (rec.runningSince !== null ? this.now() - rec.runningSince : 0);
  }

  private async run(pid: ProcessId, body: ProcessBody): Promise<void> {
    const rec = this.procs.get(pid)!;
    let outcome: ProcessOutcome;

    try {
      const value =
        body.kind === 'inproc'
          ? await body.entry(this.makeContext(pid))
          : await this.runExec(pid, rec, body);
      outcome = { ok: true, value };
    } catch (err) {
      const e = err as Error;
      outcome = {
        ok: false,
        error: {
          code: e instanceof BudgetExceeded ? 'budget_exceeded' : e?.name || 'error',
          message: e?.message ?? String(err),
        },
      };
    }

    rec.info.outcome = outcome;
    this.setState(pid, outcome.ok ? 'exited' : 'failed');
    this.events.append(pid, 'proc.outcome', outcome);
  }

  /**
   * 起一个真正被内核约束的 OS 进程.
   *
   *   能力已经在 spawn 时翻译成 landlock/cgroup/netns 规则并验证过内核支持,
   *   这里只负责拼命令行、起进程、等退出码. 进程调不调 can() 无关紧要 ——
   *   它**做不到**没授权的事.
   */
  private runExec(
    pid: ProcessId,
    rec: ProcRecord,
    body: Extract<ProcessBody, { kind: 'exec' }>,
  ): Promise<unknown> {
    const plan = planFor(rec.info.spec.caps, {
      volumeRoot: this.volumeRoot,
      runtimeFs: this.runtimeFs,
    });
    const launch = buildLaunch(plan, body, {
      pid,
      cgroupSlice: `neox-os/${pid}`,
      abiSocket: `/run/neox-os/${pid}.sock`,
    });

    this.events.append(pid, 'cap.used', { confinement: plan, argv: launch.argv });

    return new Promise((resolve, reject) => {
      const child = this.spawner(launch.argv, { cwd: body.cwd, env: launch.env });

      /* 进程的 stdout 是结构化输出通道, 一行一条 —— 不是"给人看的日志" */
      child.onLine((line) => {
        if (line.trim()) this.events.append(pid, 'proc.output', { line });
      });

      rec.abort.signal.addEventListener('abort', () => child.kill('SIGTERM'));

      child.onExit((code, signal) => {
        if (code === 0) resolve({ exitCode: 0 });
        else reject(new Error(`进程退出: code=${code} signal=${signal}`));
      });
    });
  }

  /** 关机 — 清定时器, 停所有进程 */
  shutdown(reason = 'shutdown'): void {
    for (const rec of this.procs.values()) {
      if (!TERMINAL_STATES.includes(rec.info.state)) rec.abort.abort(new Error(reason));
    }
    this.decisions.dispose();
  }
}
