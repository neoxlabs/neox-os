/**
 * source-live — 接入 OS 的观察口.
 *
 *   传输是 SSE 不是 WebSocket:
 *   观察本来就是**单向**的 —— 事件从 OS 流向界面, 反向只有"说话/批准"
 *   两个动作, 普通 POST 就够. 而且 EventSource 自带重连.
 *
 *   两条不能省的语义:
 *
 *   1. **重连带的是"每个 pid 各自拿全了到哪儿", 不是一个全局游标**.
 *      进程 A 的 seq 跟进程 B 的 seq 没有可比性, 拿一个全局游标去续,
 *      必然一部分跳过一部分重放, 而订阅方自己不会知道.
 *
 *   2. **重叠必然发生, 幂等丢弃**; 缺失绝不允许.
 *      EventStore 按 (pid, seq) 归位, 已有的直接丢, 所以这里放心重叠.
 *
 *   OS 那边如果发现我们跟不上, 会推一条 `gap` 事件然后断开 ——
 *   那不是错误处理, 那是**明说这里断过一段**, 我们据此带着游标重连.
 */

import { t } from "../i18n/index.js";
import type { OsEvent, ProcessID } from "./events.js";
import type { EventStore } from "./store.js";

export interface LiveProcess {
  readonly pid: ProcessID;
  readonly name: string;
  readonly state: string;
  /** 同一个 thread 的几个进程就是一个房间 —— 这是 OS 的概念, 不是界面发明的 */
  readonly thread?: string;
  /** 同一个 bot 标签的新旧进程是同一个人 */
  readonly bot?: string;
}

export type LiveState = "connecting" | "live" | "down";

/** 这台 OS 现在有什么 —— 没接推理服务要**明说**, 不能让 bot 拿套话糊过去 */
export interface OsHealth {
  inference: boolean;
  model?: string;
  /**
   * 边界是不是**真的有人挡**.
   *
   *   confined 那条路是 landlock/netns/cgroup 挡的; dev(in-proc)那条路
   *   什么都没有 —— bot 用 run 可以把文件写到工作区外面.
   *   界面据此说实话, 不写死一句"边界在内核".
   */
  enforced?: boolean;
  mode?: string;
}

export interface LiveOptions {
  readonly baseUrl: string;
  readonly token: string;
  readonly store: EventStore;
  onState(state: LiveState): void;
  onProcesses(processes: readonly LiveProcess[]): void;
  onHealth(health: OsHealth): void;
}

/** 试一把的结果 —— 成没成、多久、对面自报的模型是谁 */
export interface ProviderProbe {
  readonly ok: boolean;
  readonly latencyMs: number;
  readonly model?: string;
  readonly reply?: string;
  readonly error?: string;
  /** 勾了"能看图"的话: **真送一张图过去**之后的结果 */
  readonly vision?: string;
}

/** 一个项目现在什么状态 —— 见 go/cmd/neox-console/project.go */
export interface ProjectReport {
  readonly path: string;
  readonly name: string;
  readonly main?: string;
  readonly head?: CommitLine;
  readonly files: number;
  /** 项目根上没提交的改动 —— **那是你自己手上的东西**, bot 都在各自的 worktree 里 */
  readonly dirty: number;
  readonly members: readonly {
    readonly bot: string; readonly branch?: string;
    /** 干完了还没合上去的 —— 这一格不为零最要紧 */
    readonly unmerged: number; readonly dirty: number; readonly lastAt?: number;
  }[];
  readonly merges: readonly CommitLine[];
  /** 谁把活交给了谁 —— 交接改变的是"这个项目里谁负责什么" */
  readonly handoffs?: readonly { from: string; to: string; at: number; files: number }[];
  readonly error?: string;
}

export interface CommitLine {
  readonly hash: string; readonly who: string; readonly subject: string;
  readonly at: number; readonly merge?: boolean;
}

/** 一个 bot 的产物: 它自己那条分支上的提交 + 还没提交的改动 */
export interface BotOutput {
  readonly branch?: string;
  readonly dir?: string;
  readonly commits: readonly {
    readonly hash: string; readonly at: number; readonly subject: string;
    readonly files: number; readonly add?: number; readonly del?: number;
  }[];
  readonly dirty: readonly { readonly path: string; readonly status: string }[];
  readonly error?: string;
}

/** 一个人花了多少、干出了什么 */
export interface Spend {
  readonly bot: string;
  readonly project?: string;
  readonly branch?: string;
  readonly prompt: number;
  readonly completion: number;
  /** 命中前缀缓存的部分 —— 省下来的那些 */
  readonly cached: number;
  readonly calls: number;
  readonly turns: number;
  readonly commits: number;
  readonly starts: number;
  readonly firstAt?: number;
  readonly lastAt?: number;
}

export interface SpendReport {
  readonly bots: readonly Spend[];
  /** 合计由后端算 —— 加法散在两处迟早对不上 */
  readonly total: Spend;
}

export class LiveSource {
  #source: EventSource | null = null;
  #closed = false;
  #retry = 0;
  #timer = 0;
  #pollTimer = 0;

  constructor(private readonly options: LiveOptions) {
    this.#open();
    void this.#refreshProcesses();
    void this.#refreshHealth();
    this.#pollTimer = window.setInterval(() => void this.#refreshProcesses(), 3_000);
  }

  #url(path: string, params: Record<string, string> = {}): string {
    const url = new URL(path, this.options.baseUrl);
    for (const [key, value] of Object.entries(params)) url.searchParams.set(key, value);
    return url.toString();
  }

  #headers(): HeadersInit {
    return { authorization: `Bearer ${this.options.token}`, "content-type": "application/json" };
  }

  async #refreshHealth(): Promise<void> {
    try {
      const response = await fetch(this.#url("/health"), { headers: this.#headers() });
      if (!response.ok) return;
      const body = await response.json() as {
        inference?: boolean; model?: string; enforced?: boolean; mode?: string;
      };
      // **一个字段一个字段地接**: 这里原来只搬 inference 和 model,
      // 于是后端加的 enforced 到不了界面 —— 而设置页正是靠它决定
      // 说"边界在内核"还是"没人强制". 后端说了真话, 界面照样在说假话.
      this.options.onHealth({
        inference: body.inference === true,
        ...(body.model === undefined ? {} : { model: body.model }),
        ...(body.enforced === undefined ? {} : { enforced: body.enforced }),
        ...(body.mode === undefined ? {} : { mode: body.mode })
      });
    } catch { /* 拉不到就当没接 —— 宁可显示"没接", 不许假装接上了 */ }
  }

  async #refreshProcesses(): Promise<void> {
    try {
      const response = await fetch(this.#url("/processes"), { headers: this.#headers() });
      if (!response.ok) return;
      const raw = await response.json() as {
        pid: string; state: string;
        spec?: {
          name?: string; app?: string; labels?: Record<string, string>;
          // caps 里写轴的 scope 就是它干活的目录 —— **界面不另存一份**,
          // 存两份迟早分叉: 显示的是一个地方, 它实际在另一个地方动手
          caps?: { axis?: string; scope?: string }[];
        };
      }[];
      this.options.onProcesses(raw.map((row) => {
        const labels = row.spec?.labels ?? {};
        return {
          pid: row.pid,
          name: row.spec?.name ?? row.spec?.app ?? row.pid,
          state: row.state,
          ...(labels.thread === undefined ? {} : { thread: labels.thread }),
          ...(labels.bot === undefined ? {} : { bot: labels.bot }),
          ...(labels.role === undefined ? {} : { role: labels.role }),
          // 它的产物在哪条分支上 —— 隔离和交接都挂在这条上, 见 go 侧 gitwork.go
          ...(labels.branch === undefined ? {} : { branch: labels.branch }),
          // 项目根 —— 干活目录是各自的 worktree, "同一个项目"只有它答得出
          ...(labels.project === undefined ? {} : { project: labels.project }),
          ...(workOf(row.spec?.caps) === undefined ? {} : { work: workOf(row.spec?.caps)! })
        };
      }));
    } catch { /* 拉不到就下一轮再拉 —— 流那条才是主路径 */ }
  }

  #open(): void {
    if (this.#closed) return;
    this.options.onState("connecting");
    // 每个进程各自的游标
    const cursors = this.options.store.pids()
      // **haveThrough 不是 highWater**: 中间丢过一条时 highWater 在洞的后面,
      // 拿它当游标等于跟服务端说"那一段我有了" —— 洞永远补不上. 见 store.ts
      .map((pid) => `${pid}:${this.options.store.haveThrough(pid)}`)
      .join(",");
    // EventSource 发不了自定义头, 所以 token 走 query —— 口子只绑回环
    const source = new EventSource(this.#url("/stream", { token: this.options.token, from: cursors }));
    this.#source = source;

    source.onopen = () => { this.#retry = 0; this.options.onState("live"); };
    source.onmessage = (message) => {
      try {
        const event = JSON.parse(message.data as string) as OsEvent;
        this.options.store.append([event]);
      } catch { /* 一条解不开不该拖垮整条流 */ }
    };
    // OS 明说"这里断过一段" —— 带着游标重来, 不要假装没发生
    source.addEventListener("gap", () => { source.close(); this.#reconnect(); });
    source.onerror = () => { source.close(); this.#reconnect(); };
  }

  #reconnect(): void {
    if (this.#closed) return;
    this.options.onState("down");
    this.#retry = Math.min(this.#retry + 1, 6);
    this.#timer = window.setTimeout(() => this.#open(), 200 * 2 ** this.#retry);
  }

  /**
   * 投一句话.
   *
   *	`text` 是真正送进进程的内容(群聊时裹着上下文信封),
   *	`said` 是用户**原话**, `utterance` 是这一次发言的身份 ——
   *	同一句话投给屋里 N 个人时, 界面据此只显示一遍。
   */
  /**
   * 投一句话. **pids 给了就一次投给一屋子人** ——
   *
   *   "这话还发给了谁、谁拆活"是这套东西的协议, 它属于 OS 不属于界面:
   *   写在界面里的话, 换个客户端(命令行、测试台)协议就没了, 而 bot
   *   完全看不出少了什么; 如果没有这一层, 两个人各自搭一套脚手架时会
   *   各自执行一遍.
   */
  /** @returns 送到了 / 进程不在 / 连不上这台 OS */
  async say(pid: ProcessID, text: string, from?: string, said?: string, utterance?: string,
            images?: readonly { name: string; data: string }[],
            pids?: readonly ProcessID[],
            files?: readonly { name: string; data: string }[]): Promise<"ok" | "gone" | "down"> {
    try {
      const response = await fetch(this.#url("/say"), {
        method: "POST", headers: this.#headers(),
        body: JSON.stringify({
          pid, text,
          ...(pids === undefined || pids.length < 2 ? {} : { pids }),
          // 图**跟着这句话一起过去**: OS 那边落成文件, 再把路径写进这一轮
          // 的话里(见 go/osinit/blobs.go). 界面这边只留 id
          ...(images === undefined || images.length === 0 ? {} : { images }),
          // 文件同一套待遇, 走 files 通道 —— bot 拿到路径用 read_file 打开
          ...(files === undefined || files.length === 0 ? {} : { files }),
          ...(from === undefined || from.length === 0 ? {} : { from }),
          ...(said === undefined || said === text ? {} : { said }),
          ...(utterance === undefined ? {} : { utterance })
        })
      });
      if (!response.ok) return "down";
      const body = await response.json() as { ok?: boolean; sent?: number };
      // 单发看 ok; 群发(sayToMany)回的是 sent 数 —— 一个都没送到才算失败
      return body.ok === true || (body.sent ?? 0) > 0 ? "ok" : "gone";
    } catch {
      return "down";
    }
  }

  /**
   * **刹车**: 停掉这一轮, 人还在, 手上没提交的改动留着.
   *
   *   按名字不按 pid —— 界面上人看到的是"这个 bot", 而它可能刚重启过,
   *   pid 早换了. 返回错误原因, null = 停住了.
   */
  async stop(name: string): Promise<string | null> {
    try {
      const response = await fetch(this.#url("/stop"), {
        method: "POST", headers: this.#headers(), body: JSON.stringify({ name })
      });
      if (response.ok) return null;
      const body = await response.json().catch(() => ({})) as { error?: string };
      return body.error ?? `HTTP ${response.status}`;
    } catch (error) {
      return error instanceof Error ? error.message : t("连不上这台 OS");
    }
  }

  /** 这个项目现在什么状态 */
  async projectOf(path: string): Promise<ProjectReport | null> {
    try {
      const response = await fetch(this.#url(`/project?path=${encodeURIComponent(path)}`), { headers: this.#headers() });
      if (!response.ok) return null;
      return await response.json() as ProjectReport;
    } catch { return null; }
  }

  /** 撤掉主干上的一条改动. 返回错误原因, null = 撤掉了 */
  /** 项目显示名: path → 名字. 没起过名的不在里面, 界面回退到目录名 */
  async projectNames(): Promise<Record<string, string>> {
    try {
      const res = await fetch(this.#url("/projects"), { headers: this.#headers() });
      if (!res.ok) return {};
      const got = await res.json() as { names?: Record<string, string> };
      return got.names ?? {};
    } catch {
      return {};
    }
  }

  /** 群主: 房间名 → bot 名. 开房那一刻定的 */
  async roomOwners(): Promise<Record<string, string>> {
    try {
      const res = await fetch(this.#url("/rooms"), { headers: this.#headers() });
      if (!res.ok) return {};
      const got = await res.json() as { owners?: Record<string, string> };
      return got.owners ?? {};
    } catch {
      return {};
    }
  }

  /** 给项目起显示名. 空名字 = 回到目录名. 返回错误原因, null = 成了 */
  async nameProject(path: string, name: string): Promise<string | null> {
    const res = await fetch(this.#url("/project/name"), {
      method: "POST", headers: this.#headers(),
      body: JSON.stringify({ path, name })
    });
    if (res.ok) return null;
    const got = await res.json().catch(() => ({})) as { error?: string };
    return got.error ?? t("改不了");
  }

  async revert(project: string, hash: string): Promise<{ ok: boolean; message: string }> {
    try {
      const response = await fetch(this.#url("/revert"), {
        method: "POST", headers: this.#headers(), body: JSON.stringify({ project, hash })
      });
      const body = await response.json().catch(() => ({})) as { message?: string; error?: string };
      return response.ok
        ? { ok: true, message: body.message ?? t("撤掉了") }
        : { ok: false, message: body.error ?? `HTTP ${response.status}` };
    } catch (error) {
      return { ok: false, message: error instanceof Error ? error.message : t("连不上这台 OS") };
    }
  }

  /** 读推理配置. **key 永远不回吐** —— 界面只需要知道配没配 */
  async getProvider(): Promise<{ baseUrl: string; model: string; hasKey: boolean; keyFrom?: string; protocol?: string; searchNative?: boolean; vision?: boolean; think?: boolean; searchApi?: string; searchUrl?: string; searchKey?: string; searchOn?: boolean; capTokens?: number; autoResume?: boolean; autoHire?: boolean } | null> {
    try {
      const response = await fetch(this.#url("/provider"), { headers: this.#headers() });
      if (!response.ok) return null;
      return await response.json() as { baseUrl: string; model: string; hasKey: boolean; keyFrom?: string; protocol?: string; searchNative?: boolean; vision?: boolean; think?: boolean; searchApi?: string; searchUrl?: string; searchKey?: string; searchOn?: boolean; capTokens?: number; autoResume?: boolean; autoHire?: boolean };
    } catch { return null; }
  }

  /** 写推理配置. 不填 key = 沿用已经存着的那把 */
  async setProvider(next: { baseUrl: string; model: string; apiKey?: string; protocol?: string; vision?: boolean; think?: boolean; searchApi?: string; searchUrl?: string; searchKey?: string; searchOn?: boolean; capTokens?: number; autoResume?: boolean; autoHire?: boolean }): Promise<string | null> {
    const response = await fetch(this.#url("/provider"), {
      method: "POST", headers: this.#headers(), body: JSON.stringify(next)
    });
    if (response.ok) { void this.#refreshHealth(); return null; }
    const body = await response.json().catch(() => ({})) as { error?: string };
    return body.error ?? `HTTP ${response.status}`;
  }

  /**
   * 试一把这条配置 —— **真发一次请求, 不保存**.
   *
   *   "存好了"跟"它能用"是两件事: 地址少个 /v1、模型名拼错、key 过期,
   *   都要等到 bot 干到一半才由它回你一句"模型出错了".
   */
  async testProvider(next: { baseUrl?: string; model?: string; apiKey?: string; vision?: boolean }): Promise<ProviderProbe> {
    try {
      const response = await fetch(this.#url("/provider/test"), {
        method: "POST", headers: this.#headers(), body: JSON.stringify(next)
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({})) as { error?: string };
        return { ok: false, latencyMs: 0, error: body.error ?? `HTTP ${response.status}` };
      }
      return await response.json() as ProviderProbe;
    } catch (error) {
      return { ok: false, latencyMs: 0, error: error instanceof Error ? error.message : t("连不上这台 OS") };
    }
  }

  /**
   * 问供应商要一份模型名单 —— **不保存**.
   *
   *   模型名是抄不对的东西(deepseek-v4 不是合法名字, 只有 -pro/-flash),
   *   而拼错要等一次真请求打过去才报错.
   */
  async listModels(next: { baseUrl?: string; apiKey?: string }): Promise<{ models?: readonly string[]; error?: string }> {
    try {
      const response = await fetch(this.#url("/provider/models"), {
        method: "POST", headers: this.#headers(), body: JSON.stringify(next)
      });
      const body = await response.json().catch(() => ({})) as { models?: string[]; error?: string };
      if (!response.ok) return { error: body.error ?? `HTTP ${response.status}` };
      return { models: body.models ?? [] };
    } catch (error) {
      return { error: error instanceof Error ? error.message : t("连不上这台 OS") };
    }
  }

  /**
   * 它干出来了什么 —— **判据是 git, 不是它自己说的话**.
   *
   *   它说"改好了", 而到底改了哪几个文件、改成什么样, 原来得人去翻;
   *   翻不动的时候就只能信它.
   */
  async outputOf(bot: string): Promise<BotOutput | null> {
    try {
      const response = await fetch(this.#url(`/output?bot=${encodeURIComponent(bot)}`), { headers: this.#headers() });
      if (!response.ok) return null;
      return await response.json() as BotOutput;
    } catch { return null; }
  }

  /** 这台机器上的账: 谁花了多少、干出了什么 */
  async spend(): Promise<SpendReport | null> {
    try {
      const response = await fetch(this.#url("/spend"), { headers: this.#headers() });
      if (!response.ok) return null;
      return await response.json() as SpendReport;
    } catch { return null; }
  }

  /** 把一个 bot 换到别的房间. 空 thread = 拉出来单聊 */
  async move(name: string, thread: string): Promise<string | null> {
    const response = await fetch(this.#url("/move"), {
      method: "POST", headers: this.#headers(), body: JSON.stringify({ name, thread })
    });
    if (response.ok) { void this.#refreshProcesses(); return null; }
    const body = await response.json().catch(() => ({})) as { error?: string };
    return body.error ?? `HTTP ${response.status}`;
  }

  /** 删掉一段对话. **不可撤销** —— 内存、账本、名册、进程一起没 */
  async forget(pids: readonly string[]): Promise<string | null> {
    const response = await fetch(this.#url("/forget"), {
      method: "POST", headers: this.#headers(), body: JSON.stringify({ pids })
    });
    if (response.ok) { void this.#refreshProcesses(); return null; }
    const body = await response.json().catch(() => ({})) as { error?: string };
    return body.error ?? `HTTP ${response.status}`;
  }

  /** 审批口径 */
  async getPolicy(): Promise<"always" | "bounds" | "never" | null> {
    try {
      const response = await fetch(this.#url("/policy"), { headers: this.#headers() });
      if (!response.ok) return null;
      const body = await response.json() as { mode?: string };
      return (body.mode ?? "bounds") as "always" | "bounds" | "never";
    } catch { return null; }
  }

  async setPolicy(mode: string): Promise<string | null> {
    const response = await fetch(this.#url("/policy"), {
      method: "POST", headers: this.#headers(), body: JSON.stringify({ mode })
    });
    if (response.ok) return null;
    const body = await response.json().catch(() => ({})) as { error?: string };
    return body.error ?? `HTTP ${response.status}`;
  }

  /**
   * 这台 OS 算在哪个时区.
   *
   *	**它进的是判断不是显示**: 日报几点发、规则里"晚上七点到十点"
   *	算哪一段、"明天早上八点"是哪一刻. Docker 里默认 UTC, 全差 8 小时,
   *	而一处都不会报错 —— 日报照发, 只是在凌晨发.
   *
   *	手机连上来会自己带 tz(见 go/osinit/zone.go), 所以绝大多数时候
   *	没人需要动这一格. 它是给"只用桌面端"和"按另一个地方的时间过日子"
   *	留的.
   */
  async getTimezone(): Promise<{ zone: string; fixed: boolean; now: string; pick: string[] } | null> {
    try {
      const response = await fetch(this.#url("/timezone"), { headers: this.#headers() });
      if (!response.ok) return null;
      const body = await response.json() as {
        zone?: string; fixed?: boolean; now?: string; pick?: string[];
      };
      return {
        zone: body.zone ?? "UTC",
        fixed: body.fixed === true,
        now: body.now ?? "",
        pick: body.pick ?? []
      };
    } catch { return null; }
  }

  /**
   * 换时区. zone 为 null = **回到"跟着手机走"**.
   *
   *	定死了要能松开: 没有这一条的话, 用户点过一次就永远回不到自动 ——
   *	他搬了城市, 手机报的新时区被自己两个月前那一下挡着, 而界面上
   *	看不出是被什么挡着的.
   */
  async setTimezone(zone: string | null): Promise<string | null> {
    const response = await fetch(this.#url("/timezone"), {
      method: "POST", headers: this.#headers(),
      body: JSON.stringify(zone === null ? { auto: true } : { zone })
    });
    if (response.ok) return null;
    const body = await response.json().catch(() => ({})) as { error?: string };
    return body.error ?? `HTTP ${response.status}`;
  }

  /** 新建一个 bot. 能力**不由界面指定** —— 宿主按自己的策略给 */
  /**
   * 新建一个 bot.
   *
   *   work = 派它去哪个目录干活. 空 = 由 OS 分一个匿名目录.
   *   **这条路径会原样变成内核给它的写能力**, 所以边界由 OS 那边把关
   *   (家目录、根、账本所在一律拒), 界面只管把话带到.
   */
  async create(name: string, thread?: string, work?: string, role?: string): Promise<{ pid?: string; error?: string }> {
    const response = await fetch(this.#url("/create"), {
      method: "POST", headers: this.#headers(),
      body: JSON.stringify({
        name,
        ...(thread === undefined || thread.length === 0 ? {} : { thread }),
        ...(work === undefined || work.length === 0 ? {} : { work }),
        ...(role === undefined || role.length === 0 ? {} : { role })
      })
    });
    const body = await response.json() as { pid?: string; error?: string };
    // 失败要把原因带回去 —— 对着一个没反应的按钮点第二次只会多起一个失败的进程
    if (!response.ok) return { error: body.error ?? `HTTP ${response.status}` };
    return body.pid === undefined ? {} : { pid: body.pid };
  }

  /**
   * 批准请求. choice 用 OS 的词汇: "yes" 才算批准.
   *
   *   返回 null = 成了; 返回一句话 = 没成(多半是等它的进程已经不在了).
   *   **不能静默吞掉** —— 否则界面上那张卡片会永远等下去.
   */
  /**
   * 把一个 bot 换到另一个工作区.
   *
   *   **换个进程, 对话不断**: 能力在进程启动时定死, 所以"换个地方干活"
   *   只能是重起一个. 历史跟着 bot 标签走.
   */
  async rebind(name: string, work: string): Promise<string | null> {
    const response = await fetch(this.#url("/rebind"), {
      method: "POST", headers: this.#headers(),
      body: JSON.stringify({ name, work })
    });
    if (response.ok) return null;
    const body = await response.json().catch(() => ({})) as { error?: string };
    return body.error ?? `HTTP ${response.status}`;
  }

  async decide(did: string, choice: string): Promise<string | null> {
    const response = await fetch(this.#url("/decide"), {
      method: "POST", headers: this.#headers(), body: JSON.stringify({ did, choice })
    });
    if (!response.ok) return `HTTP ${response.status}`;
    const body = await response.json() as { ok?: boolean };
    return body.ok === true ? null : t("这条已经处理过了，或者等它的进程已经不在了");
  }

  close(): void {
    this.#closed = true;
    window.clearTimeout(this.#timer);
    window.clearInterval(this.#pollTimer);
    this.#source?.close();
  }
}

/**
 * workOf 它在哪儿干活 —— 就是写轴那条能力的 scope.
 *
 *   **不另存一份**: 工作区的真相源是内核发给它的写能力. 界面另记一份的话
 *   迟早分叉 —— 显示的是一个地方, 它实际在另一个地方动手, 而这种错
 *   只有等用户发现"我的文件没变"才会暴露.
 */
function workOf(caps: { axis?: string; scope?: string }[] | undefined): string | undefined {
  return caps?.find((c) => c.axis === "write")?.scope;
}
