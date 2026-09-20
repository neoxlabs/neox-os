/**
 * rows — 事件 → 对话行的**增量**投影.
 *
 *   这是"IM 形状"落地的地方: 内核吐的是 proc.output / cap.denied 这种
 *   系统事件, 人要看的是"谁说了什么". 折叠规则全在这里, 内核一个字不用改.
 *
 *   三条硬规则:
 *   1. **增量**. 每帧从头 fold 一遍 = 跑一小时后一帧几十毫秒.
 *   2. **同一条流的连续输出折成一行**. 一次回答几千块, 一块一行 DOM 就爆了.
 *   3. **正在长的行是可变对象, 不换引用** —— 换引用整列表 diff, 不换只动它自己.
 */

import { t } from "../i18n/index.js";
import { outcomeOf } from "./outcome.js";
import type { OsEvent, ProcessID } from "../os/events.js";
import { ROOM_CONTEXT_TAIL } from "../shell/roomContext.js";
import { unwrapSaid } from "./envelope.js";
import type {
  CapPayload, DecideRequestPayload, DecideResolvedPayload, FileRef, InputPayload, ShotRef,
  OutcomePayload, ProcOutputPayload, ProcStatePayload, SignalPayload, WakePayload
} from "../os/events.js";

/** 归并时一个成员的那一份 —— 见 consumeMerged */
export interface MergeSource {
  readonly pid: ProcessID;
  readonly speaker: string;
  readonly events: readonly OsEvent[];
  readonly root?: string;
}

export type RowKind =
  /** 收到的输入 */      | "heard"
  /** bot 说的 */      | "said"
  /** bot 在想 */      | "thought"
  /** bot 干了什么(一组) */ | "tools"
  /** bot 要许可 */     | "ask"
  /** 即时 UI */       | "widget"
  | "did" | "outcome" | "note" | "joined" | "wake"
  /** 这一轮没走完 —— 必须看得见 */ | "alert";

/**
 * 一条"过程"记录 —— 工具调用, 或者一次已经拍过板的审批.
 *
 *   **审批也算过程**: 一轮活里 bot 真正开口只有一次, 其余全是
 *   "它动了什么、你放行了什么". 那些各占一行的话, 一个跑几小时的任务
 *   会把对话整个冲掉 —— 而人回头找的是那句回复, 不是第 37 次授权.
 */
export interface ToolCall {
  name: string;
  arg: string;
  result?: string;
  failed?: boolean;
  /** 闸按设计拦下的 —— 不是故障, 不能跟 failed 混着显示 */
  blocked?: boolean;
  /** 探查的阴性结果("还没有这个东西") —— 也不是故障, 刷红会吓到人 */
  missing?: boolean;
  running?: boolean;
  /** 这条是审批不是工具 */
  approval?: boolean;
  /** 展开时该用哪种卡片渲染 */
  kind?: CallKind;
}

/**
 * 一条过程记录展开之后长什么样.
 *
 *   **按"它做了什么"分, 不按工具名分**: 工具会增删改名, 而"写了个文件"
 *   "跑了条命令"这几类不会. 认不出来的一律走 text —— 摆一个错的卡片
 *   比摆一段纯文本糟得多.
 */
export type CallKind = "write" | "read" | "run" | "search" | "image" | "text";

export interface Row {
  readonly id: string;
  /** 哪个进程说的 —— 判"这张卡还有没有人在等"要用它 */
  readonly pid: ProcessID;
  readonly kind: RowKind;
  readonly at: number;
  readonly seq: number;
  /** 在 rows 里的下标 —— 高度失效时要从这里往后重排, 不能靠 indexOf 扫 */
  readonly index: number;
  who?: string;
  /**
   * 这一行要不要露脸.
   *
   *   连着说的话只在第一行标身份 —— 每行都贴一次作者名, 是"看着像日志"
   *   而不是"看着像对话"的头号原因. (Grok Bot 那边专门有个
   *   transcript-adjacency 做同一件事.)
   */
  showWho?: boolean;
  /** 露脸时要不要连名字一起标 —— 群聊才标 */
  showName?: boolean;
  title?: string;
  text: string;
  revision: number;
  open: boolean;
  tone?: "ok" | "warn" | "bad";
  /** widget 行: 解析好的即时 UI 规格 */
  widget?: WidgetSpec;
  /** did 行: 工具跑完的结果 */
  result?: string;
  /** tools 行: 这一轮的过程(工具 + 已完成的审批) */
  calls?: ToolCall[];
  /**
   * 这一行带的图.
   *
   *   **图不进事件正文**: 事件里只有 id, 图本身是磁盘上的文件,
   *   界面按 id 去 /blob 取(见 go/osinit/blobs.go).
   */
  shots?: readonly ShotRef[];
  /** 这一行带的文件(非图) —— 只画名字标签, 字节归 bot 读 */
  files?: readonly FileRef[];
  /** 已经并进别的行了 —— 不渲染, 也不占高度 */
  merged?: boolean;
  /** tools 行: 并进来的这一轮思考. 折着时"想了一会儿"跟工具数同一行, 展开时想的在前 */
  thought?: string;
  /** 同一件事连着发生了几次 —— 重试的那几次并进一张卡, 不各占一张 */
  repeat?: number;
}

/** 即时 UI 规格. bot 通过 proc.output channel="ui" 吐一段 JSON, 就长出可交互卡片 */
export interface WidgetSpec {
  readonly type: string;
  readonly id: string;
  readonly [key: string]: unknown;
}

/** 算"连续发言"的行类型. 中间夹一条系统行不打断分组 —— 那条本来就该淡得看不见 */
/**
 * 一个洞等多久才认定它填不上.
 *
 *   乱序到达是允许的(store.ts 的规矩), 一两百毫秒内补上是常态 ——
 *   跳早了就是把一条真实发生过的事画丢了, 那比卡住更糟.
 */
const HOLE_PATIENCE_MS = 3000;

/** 一条是不是把另一条包在里面 —— 同一件事换了个说法 */
function wraps(a: string, b: string): boolean {
  return a !== "" && (a.includes(b) || b.includes(a));
}

/** 这一行要不要露脸: 有身份, 且上一个开口的不是同一个人 */
function facing(row: { who?: string }, previous: Row | undefined): boolean {
  return row.who !== undefined
    && (previous === undefined || previous.who !== row.who || !SPEECH.has(previous.kind));
}

/**
 * 算"这个人还在说话"的行 —— 紧跟着的同一个人不再重复露脸.
 *
 *   tools 也在里面: 工具行是带身份的(见 #openToolGroup), 它就是这个人
 *   在动手, 不是别人插话. 不算进来的话, "动手 → 说话"之间会多摆一次
 *   头像和名字.
 */
const SPEECH: ReadonlySet<RowKind> = new Set<RowKind>(["said", "heard", "thought", "widget", "tools"]);

/**
 * 这些行**不带身份, 但也不是别人在说话** —— 是同一个 bot 自己在动手.
 *
 *   判"该不该再摆一次头像"时要**看穿**它们: 拿一行工具调用当"换人了",
 *   同一个人连着说话就会重复摆两遍头像和名字. 真机截图里就是这样:
 *
 *     [小勤] 想了一会儿
 *     用了 21 个工具          ← 这一行把链子打断了
 *     [小勤] 想了一会儿        ← 于是又摆了一遍
 *
 *   用户插话(heard)不在里面: 那**真的**是换人了, 后面那句该重新露脸.
 */
const SELF: ReadonlySet<RowKind> = new Set<RowKind>(["did", "note", "outcome", "wake"]);

const CHANNEL_KIND: Record<string, RowKind> = { say: "said", think: "thought", tool: "did", ui: "widget" };

export class RowProjector {
  readonly rows: Row[] = [];
  /**
   * 已经拍过板的决策 did → 用户选了什么.
   *
   *   **真相在事件日志里, 不在 React state 里**.
   *   早先"这张卡回答过没有"存在组件的 useState 里, 刷新一次就没了 ——
   *   卡片退回可点状态, 而 OS 那边早就 resolved 了, 再点一次只会拿到
   *   一句 ok:false. 同一件事有两个不同步的状态源, 一定以界面那份先失真.
   */
  readonly resolutions = new Map<string, string>();
  #cursor = 0;
  /** 房间模式下每个成员各自的游标 */
  readonly #cursors = new Map<ProcessID, number>();
  #speaker: string | null = null;
  /** 每个 pid 卡在哪个洞上、从什么时候开始卡的 —— 见 #hopeless */
  readonly #stuck = new Map<ProcessID, { seq: number; since: number }>();
  /**
   * 行**换过多少次位置**.
   *
   *   归位(#resettle)会把整个数组重排. 渲染那侧的 offsets 是**按位置**
   *   索引的 —— 一重排就全对不上, 而它自己看不出来: 行数没变, 内容没变,
   *   只是顺序变了. 表现为**行叠在一起**, 而且不会自己好.
   *
   *   所以由这边说出来, 让那边知道该从头重排.
   */
  reordered = 0;
  /** 取现在几点. 单独拎出来是为了测试能把时钟拨快, 不用真等 */
  now: () => number = () => Date.now();
  /** 正在消费哪个 pid 的事件, 它在哪儿干活 —— 用来把路径写成相对的 */
  #root: string | undefined;
  readonly #open = new Map<string, Row>();
  /**
   * 这一轮的工具行 —— 后面的调用都追加进它. **按 pid 分开.**
   *
   *   房间里两个 bot 会同时动手. 一个投影器一份的话, 后动手那个的调用
   *   会落进先动手那个的组里 —— 界面上是 A 的名字底下摆着 B 干的活.
   *   一屋子人一起干活正是房间存在的理由, 所以这不是边角情况.
   */
  readonly #toolGroups = new Map<ProcessID, Row>();
  /**
   * 这一轮的思考行 —— **一轮只有一行**.
   *
   *   跟"一轮里用过的工具收成一行"是同一条规矩: 工具和思考都按回合归并.
   *   不归并思考时, 一次干活十几步, 屏幕上就是十几行
   *   "想了一会儿" 摞在一起, 把它真正说的那句话挤到屏幕外面 ——
   *   而那十几行本身一个字的信息都没有(内容是折叠着的).
   *
   *   这类重复行可连续出现八行, 却没有新增信息.
   */
  readonly #thoughtRows = new Map<ProcessID, Row>();
  /**
   * 这一轮最近的那张报错卡 —— 重试的那几次并进它, 不各占一张.
   *
   *   模型出错会连着重试三次, 每次一条 model_err. 三张一模一样的红卡
   *   摞在一起, 而信息只有一份.
   */
  readonly #lastAlerts = new Map<ProcessID, Row>();
  /**
   * 上一轮结束时**最后一行的 id** —— 找组不能翻过这条界. 按 pid 分开.
   *
   *   存 id 不存下标: 行会按时间重新归位(见 #resettle), 下标一排就失效,
   *   而失效的边界会让这一轮的结果并进上一轮的组 —— "少算比多算更坏".
   */
  readonly #turnEnds = new Map<ProcessID, string>();
  /** 已经显示过的发言 —— 同一句投给多个进程只算一次 */
  readonly #heard = new Set<string>();
  #onRowChanged: ((row: Row) => void) | undefined;

  /**
   * showNames 只在群聊房间里标名字.
   *
   *   一对一时对面只有一个人, 每轮贴一次名字是纯噪音 —— 头像已经说明了是谁.
   *
   *   **不能在构造时冻住**: 事件从 SSE 增量到达, 房间的第一个成员
   *   先到的那一瞬间它还只有一个人, 那时建的投影器会按"单聊"定死,
   *   而投影器按会话 id 缓存 —— 于是那个房间永远不标名字,
   *   即使其中已有 27 行、4 个头像、0 个名字.
   */
  showNames: boolean;

  constructor(readonly pid: ProcessID, readonly speaker = "bot", showNames = false) {
    this.showNames = showNames;
  }

  /** 房间人数变了要跟着改 —— 连已经画出来的行一起补 */
  setShowNames(next: boolean): void {
    if (this.showNames === next) return;
    this.showNames = next;
    for (const row of this.rows) {
      const want = row.showWho === true && next;
      if (row.showName === want) continue;
      row.showName = want;
      row.revision += 1;
    }
  }

  /**
   * 房间: 把同一个 thread 里几个进程的事件并进一条流.
   *
   *   **每个成员各自一个游标**(#cursors), 不是一个全局游标 ——
   *   pid A 的 seq 跟 pid B 的 seq 没有可比性.
   *
   *   **顺序按 at 归并**, 见 consumeMerged. 单个成员喂进来时不做归并 ——
   *   那条路径只有测试和单聊在用, 本来就只有一个人.
   */
  consumeFrom(pid: ProcessID, speaker: string, events: readonly OsEvent[], root?: string): boolean {
    const from = this.#cursors.get(pid) ?? 0;
    let structural = false;
    const previousSpeaker = this.#speaker;
    const previousRoot = this.#root;
    this.#speaker = speaker;
    // 房间里各人的工作区可能不同 —— 按 pid 跟着走, 不能一个房间一个根
    this.#root = root;
    for (let seq = from; seq < events.length; seq += 1) {
      const event = events[seq];
      if (event === undefined) {
        if (!this.#hopeless(pid, seq, events)) break; // 见 #hopeless
        this.#cursors.set(pid, seq + 1);
        continue;
      }
      this.#cursors.set(pid, seq + 1);
      if (this.#apply(event)) structural = true;
    }
    this.#speaker = previousSpeaker;
    this.#root = previousRoot;
    return structural;
  }

  /**
   * 房间: 把几个成员的事件**按时间归并**着喂进来.
   *
   *   ── 为什么必须归并 ──
   *
   *   原来是一个成员一个成员地灌: 先把 A 的全部历史追加完, 再追加 B 的.
   *   一屋子人聊过一阵之后刷新一次, 界面上就是 A 的一整段, 然后 B 的
   *   一整段 —— **最新的回答排在最上面, 更早的话排在下面**. 真机截图里
   *   就是这样, 而聊天的顺序错了, 这个界面就不成立了.
   *
   *   ── 为什么 at 够用 ──
   *
   *   pid A 的 seq 跟 pid B 的 seq 没有可比性, 但 at 有: 它是**同一个 OS
   *   进程盖的时间戳**, 一个时钟. 早先这里写着"要等 OS 给一条带全局序的
   *   流", 那句话不对 —— 全局序一直在事件里.
   *
   *   at 相同就按成员顺序定, 保证同样的输入画出同样的东西.
   */
  consumeMerged(members: readonly MergeSource[]): boolean {
    let structural = false;
    for (;;) {
      let pick = -1;
      let earliest = Number.POSITIVE_INFINITY;
      let skipped = false;
      for (let index = 0; index < members.length; index += 1) {
        const member = members[index]!;
        const seq = this.#cursors.get(member.pid) ?? 0;
        if (seq >= member.events.length) continue;
        const event = member.events[seq];
        if (event === undefined) {
          // 洞: 填不上就跳过接着走, 否则这个人先卡着, 别人照常画
          if (this.#hopeless(member.pid, seq, member.events)) {
            this.#cursors.set(member.pid, seq + 1);
            skipped = true;
            break;
          }
          continue;
        }
        if (event.at < earliest) { earliest = event.at; pick = index; }
      }
      if (skipped) continue;
      if (pick < 0) break;
      const member = members[pick]!;
      const seq = this.#cursors.get(member.pid) ?? 0;
      const event = member.events[seq]!;
      this.#cursors.set(member.pid, seq + 1);
      const previousSpeaker = this.#speaker;
      const previousRoot = this.#root;
      this.#speaker = member.speaker;
      this.#root = member.root;
      if (this.#apply(event)) structural = true;
      this.#speaker = previousSpeaker;
      this.#root = previousRoot;
    }
    if (structural) this.#resettle();
    return structural;
  }

  /**
   * 行按 at 归位.
   *
   *   ── 为什么一次归并不够 ──
   *
   *   consumeMerged 只能把**这一次拿到的**事件排好. 而历史是分批到的:
   *   服务端按 pid 补齐, 一个进程一个进程地发, 顺序跟时间无关. 后到的
   *   旧事件只能追加在末尾 —— 于是刷新一次, 界面上是"最新的在上面,
   *   更早的在下面", 一段对话读起来是断的.
   *
   *   已经排好就一个字节都不动(实时那条路本来就是有序的, 不该每帧都排).
   *   排完要重算"谁露脸": 顺序变了, 谁挨着谁就变了.
   */
  #resettle(): void {
    for (let index = 1; index < this.rows.length; index += 1) {
      if (this.rows[index]!.at >= this.rows[index - 1]!.at) continue;
      this.rows.sort((a, b) => a.at - b.at || (a.pid < b.pid ? -1 : a.pid > b.pid ? 1 : 0) || a.seq - b.seq);
      this.reordered += 1;
      let previous: Row | undefined;
      for (let i = 0; i < this.rows.length; i += 1) {
        const row = this.rows[i]!;
        (row as { index: number }).index = i;
        const want = facing(row, previous);
        if (row.showWho !== want) {
          (row as { showWho: boolean }).showWho = want;
          (row as { showName: boolean }).showName = want && this.showNames;
          row.revision += 1;
          this.#onRowChanged?.(row);
        }
        if (row.who === undefined && SELF.has(row.kind)) continue;
        previous = row;
      }
      return;
    }
  }

  onRowChanged(fn: (row: Row) => void): void { this.#onRowChanged = fn; }
  #voice(): string { return this.#speaker ?? this.speaker; }
  openRows(): readonly Row[] { return [...this.#open.values()]; }

  /** true = 行数变了(要重排); false = 只是某行在长 */
  consume(events: readonly OsEvent[]): boolean {
    let structural = false;
    for (let seq = this.#cursor; seq < events.length; seq += 1) {
      const event = events[seq];
      if (event === undefined) {
        // 补齐还没到 —— 停在洞前面, 绝不跳过. 除非这个洞是**填不上的**
        if (!this.#hopeless(this.pid, seq, events)) break;
        this.#cursor = seq + 1;
        continue;
      }
      this.#cursor = seq + 1;
      if (this.#apply(event)) structural = true;
    }
    return structural;
  }

  /**
   * 这个洞是不是**填不上的**.
   *
   *   ── 为什么要分这一下 ──
   *
   *   "停在洞前面, 绝不跳过"对**补齐还没到**是对的: 跳过去就是把一条
   *   真实发生过的事画丢了. 但账本里真的会**永久缺一条**:
   *
   *   账本可出现 45 个进程各缺一条 seq —— 缺的都是 bot 的开场白.
   *   账本只增不改, 那条永远补不回来. 于是这些会话在界面上**停在第一句话**,
   *   重连多少次都一样, 而且一声不吭: 侧栏预览是新的, 点进去是旧的.
   *
   *   ── 判据 ──
   *
   *   在同一个洞上卡了一会儿, **而且后面的事件已经到了** —— 后面都到了,
   *   说明服务端把它有的都发过来了, 这一条它自己也没有.
   *
   *   时间闸不能太短: 乱序到达是允许的(store.ts 的规矩), 一两百毫秒内
   *   补上是常态. 跳早了就是把正常内容画丢.
   */
  #hopeless(pid: ProcessID, seq: number, events: readonly OsEvent[]): boolean {
    // 后面一条都没到 —— 那就是普通的"还没轮到", 不是缺
    let laterArrived = false;
    for (let ahead = seq + 1; ahead < events.length; ahead += 1) {
      if (events[ahead] !== undefined) { laterArrived = true; break; }
    }
    if (!laterArrived) { this.#stuck.delete(pid); return false; }

    const now = this.now();
    const at = this.#stuck.get(pid);
    if (at === undefined || at.seq !== seq) {
      this.#stuck.set(pid, { seq, since: now });
      return false;
    }
    return now - at.since >= HOLE_PATIENCE_MS;
  }

  /**
   * 开一个工具组 —— 紧挨着的那个能接着用就接着用.
   *
   *   一次审批会把一轮活劈成两半: 决策完成后进程从 waiting 回到 running,
   *   而 running 会 #closeAll() 关掉当前的组, 于是后面的工具只能另起一行.
   *   界面上就是两条贴在一起、中间什么都没有的折叠行:
   *
   *       用了 2 个工具 · 1 次授权 · 1 个没成
   *       用了 2 个工具
   *
   *   那是**同一个人同一轮**在干同一件事. 拆成两行不是多了一行的事 ——
   *   它让"这一轮到底动了几下"要靠人自己加.
   *
   *   **只并紧挨着的**: 中间只要隔了一句话(reply/heard), 上一行就不是
   *   工具组了, 自然不并. 也只并**同一个 pid** —— 房间里两个 bot 同时
   *   动手时, 那是两个人各自的动作, 并了就分不清谁干的.
   *
   *   返回 true = 真的多了一行(要重排).
   */
  #openToolGroup(base: Omit<Row, "index" | "kind" | "text">, calls: ToolCall[]): boolean {
    // 往回翻过**看不见的行**: 拍完板的审批卡会标成 merged 留在数组里
    // (它已经并进上面那个组了), 但它挡在中间, 于是下一组就并不上去;
    // 一次 recruit 之后, 一轮活会显示成两条折叠行.
    let previous: Row | undefined;
    for (let index = this.rows.length - 1; index >= 0; index -= 1) {
      const row = this.rows[index];
      if (row === undefined || row.merged === true) continue;
      previous = row;
      break;
    }
    if (previous !== undefined && previous.kind === "tools" && previous.pid === base.pid) {
      previous.open = true;
      previous.calls?.push(...calls);
      previous.revision += 1;
      this.#toolGroups.set(base.pid, previous);
      this.#onRowChanged?.(previous);
      return false;
    }
    /**
     * **工具行要带身份**.
     *
     *	房间里两个 bot 同时动手, 界面上是两条挨着的"用了 N 个工具",
     *	谁都不挂名 —— 看不出哪组是谁干的. 而那正是房间最常见的样子.
     *
     *	带上之后, 一轮活的头像落在**它开始动手**那一行(工具行), 后面
     *	那句话不再重复露脸(见 #push 的连续发言判定) —— 顺序也更对:
     *	先动手, 后说话.
     */
    this.#push({ ...base, kind: "tools", text: "", open: true, calls, who: this.#voice() });
    this.#toolGroups.set(base.pid, this.rows[this.rows.length - 1]!);
    return true;
  }

  #push(row: Omit<Row, "index">): true {
    const target = row as Row & { index: number; showWho: boolean; showName: boolean };
    target.index = this.rows.length;
    // 往回找**上一个真正开口的人** —— 看穿自己动手那几行, 见 SELF
    let previous: Row | undefined;
    for (let i = this.rows.length - 1; i >= 0; i -= 1) {
      const r = this.rows[i];
      if (r === undefined) continue;
      if (r.who === undefined && SELF.has(r.kind)) continue;
      previous = r;
      break;
    }
    target.showWho = facing(row, previous);
    target.showName = target.showWho && this.showNames;
    this.rows.push(target);
    return true;
  }

  #apply(event: OsEvent): boolean {
    const id = `${event.pid}#${event.seq}`;
    const base = { id, pid: event.pid, at: event.at, seq: event.seq, revision: 0, open: false };
    switch (event.kind) {
      case "proc.output": {
        // 会干活的 bot 吐的是 ReAct 的**阶段**, 不是文本块.
        // 那份结构化 payload 留在事件日志里(它就是审计线索),
        // 渲染这一侧把它翻成人能读的行 —— 翻译归界面, 不该让内核迁就界面.
        const phased = event.payload as { phase?: string };
        if (typeof phased.phase === "string") return this.#applyPhase(event, phased.phase);
        const p = event.payload as ProcOutputPayload;
        const kind = CHANNEL_KIND[p.channel ?? "say"] ?? "said";
        if (kind === "widget") {
          const widget = parseWidget(p.text);
          if (widget === undefined) return false;
          return this.#push({ ...base, kind: "widget", text: "", widget, who: this.#voice() });
        }
        /**
         * 正文一开始吐, 就把"在想"关掉.
         *
         *   早先只在下一句用户输入时才 closeAll —— 于是答案都出来了,
         *   上面那行还写着"正在想…"闪个不停. 用户看到的是一个
         *   **永远在想的 bot**, 而它其实早就答完了.
         *
         *   这两家模型都是先出推理再出正文, 所以"正文来了 = 想完了"
         *   是可靠的判据; 真出现交错的模型, 症状是提前收起, 不是不收.
         */
        /**
         * 正文开始吐 = 想完了、这一段跑完了, 但**这一轮还没完**.
         *
         *	agent 中间会先 reply 再接着干活, 一轮里能来好几次.
         *	正文一来就断开工具组的话, 同一轮会冒出好几行"用了 N 个工具" ——
         *	而人要的是"这一轮它一共干了什么", 一行.
         *	真正的分界是**你下一次说话**.
         */
        if (kind === "said") { this.#closeThoughts(base.pid); this.#restTools(base.pid); }

        // **键里必须带 pid**: 流名是进程自己起的, 两个 bot 的开场白都走 "s0" ——
        // 不带 pid 的话, 后说话那个的文字会**接在前一个的段落后面**, 挂在
        // 前一个的名字底下. 两个 bot 的开场消息会连接到同一段文本中.
        const key = `${event.pid}:${p.stream}:${kind}`;
        const existing = this.#open.get(key);
        if (existing !== undefined) {
          existing.text += p.text;          // ← 热路径: 只改文本, 不动数组
          existing.revision += 1;
          if (p.done === true) this.#seal(key, existing);
          this.#onRowChanged?.(existing);
          return false;
        }
        // think 也带身份: 否则"在想"那一行会孤零零地悬在用户气泡下面,
        // 而它其实是这一轮回答的开头. 带上之后整轮共用一个头像.
        // done = 发的人自己说了"没有下一块" —— 当场封口, 不用等进程状态变.
        // 闹钟就是这种: 整条发出去, 而且发的时候进程正停在 waiting.
        this.#push({ ...base, kind, text: p.text, open: p.done !== true,
          ...(kind === "said" || kind === "thought" ? { who: this.#voice() } : {}) });
        if (p.done !== true) this.#open.set(key, this.rows[this.rows.length - 1]!);
        return true;
      }
      case "input.recv":
        /**
         * **一句话进来不等于上一轮结束**.
         *
         *	在这里调用 #closeAll 会关掉当前工具组和仍在流动的行. 对"说完一句
         *	再说下一句"是对的, 但**中途插话**时该回合还在运行 —— 它已经发出去
         *	的调用, 结果随后才回来. 组一关, 那个结果就落进了新的一组:
         *
         *	  用了 3 个工具        ← 第 1/2/3 条 sleep(已发起)
         *	  [打住, 别跑了]
         *	  用了 1 个工具        ← 第 3 条 sleep 的**结果**
         *
         *	一次共运行 3 次时, 界面会数出 4 次. "多算"跟
         *	"少算"一样坏 —— 人据此判断它到底动了多少下.
         *
         *	轮次边界本来就有一个更准的: proc.state running. 每一轮开始
         *	都会发它, 而它已经在关了. 这里不用抢这一下.
         */
        // 用户不给头像也不标名字 —— 屏幕前只有一个人, 标了是废话
        //
        //	群里发言时会给收话的人捎一段"他没听到的几句", 那段是给模型看的,
        //	**不能显示给用户** —— 用户会看到自己的一句话变成一大坨转述.
        {
          /**
           * 同一句话投给屋里每个人 —— **只显示一遍**.
           *
           *   OS 那边一次投递就是一条 input.recv, 群里三个人就是三条。
           *   照实渲染的话你说一句, 屏幕上出现三个一模一样的气泡。
           */
          const utterance = (event.payload as { utterance?: unknown }).utterance;
          if (typeof utterance === "string" && utterance !== "") {
            if (this.#heard.has(utterance)) return false;
            this.#heard.add(utterance);
          }
        }
        {
          /**
           * **机器自己叫醒的那一轮, 一个字都不画**.
           *
           *   定时任务到点时, OS 把题目当成一条"收到的话"投给 bot ——
           *   于是它长得跟人输入的文字一模一样，会被误认为是本人发送。
           *
           *   它的回执("（非窗口期，不动。）")也不画, 见下面 reply 那条。
           *   真有要紧的事要说, 走的是投递通道, 那条照样到人跟前。
           */
          if ((event.payload as { quiet?: unknown }).quiet === true) return false;
        }
        {
          const said = event.payload as InputPayload;
          const shots = said.images ?? [];
          const files = said.files ?? [];
          /**
           * 交办不是人输入的.
           *
           *	同屋 bot 交代的活也走 input.recv, 照"收到的话"画就成了
           *	**输入者的气泡**, 混淆了实际说话人.
           *	按说话人画: 左侧、带交办那个 bot 的脸.
           */
          if (said.relay === true && said.from !== undefined && said.from !== "") {
            return this.#push({
              ...base, kind: "said", who: said.from,
              title: t("交办"), text: stripRoomContext(said.text)
            });
          }
          return this.#push({
            ...base, kind: "heard", text: stripRoomContext(said.text),
            ...(shots.length > 0 ? { shots } : {}),
            ...(files.length > 0 ? { files } : {})
          });
        }
      case "proc.state": {
        const p = event.payload as ProcStatePayload;
        if (p.state === "running") this.#closeAll(base.pid);
        /**
         * **进程停止后, 正在流的那几行就到此为止**.
         *
         *	流式输出的行是开着的, 开着的行按纯文本画(markdown 要等它写完
         *	才敢解析, 否则半个 ** 会被当成加粗). 若只靠"下一次
         *	running"关闭, **已经退出的进程最后那段话会永远开着**,
         *	界面上就是一堆字面量的反引号和星号.
         *
         *	#closeAll 若无差别地关掉所有开着的行, 屋里任意 bot 开始干活
         *	都会顺手关闭别人的残行. 按 pid 分开后必须显式关闭该进程的流.
         *
         *	waiting 也算停: 一轮说完就该定稿, 不必等下一轮开始.
         *	但**只关流, 不动轮次边界** —— 中途等待决策也会转 waiting,
         *	那时候这一轮还没完.
         */
        if (p.state !== "running" && p.state !== "created") this.#closeStreams(base.pid);
        // "上线"不占一行: 进程起来是常态, 每个都报一次就是满屏噪音.
        // 谁在线看侧栏的点, 不看对话.
        if (p.state === "created") return false;
        return false; // running/waiting 不占一行 —— 状态给头部看, 不塞进对话
      }
      case "decide.requested": {
        const p = event.payload as DecideRequestPayload;
        // 缺 present 的事件(老账本/走样的源)不值得让整个界面陪葬 ——
        // 画一张说不清细节的审批卡, 至少让人知道这里曾经要过一次主意
        if (p.present === undefined) {
          return this.#push({ ...base, kind: "widget", text: "", who: this.#voice(),
            widget: { kind: "approve", title: t("有一次审批(这条记录缺细节)"), type: "approve", id: p.did } });
        }
        // 直接把 OS 的 PresentSpec 当卡片规格用 —— 界面不另起一套词汇
        return this.#push({ ...base, kind: "widget", text: "", who: this.#voice(),
          widget: { ...p.present, type: p.present.kind, id: p.did } });
      }
      case "decide.resolved": {
        const p = event.payload as DecideResolvedPayload;
        this.resolutions.set(p.did, p.choice);
        /**
         * 拍完板, 那张卡就**并进过程行**, 自己不再占位置.
         *
         *	待批的时候它必须显眼(进程停在那儿等你); 拍完之后它就是
         *	一条历史记录, 跟工具调用同级. 合并之后一轮活只剩两样东西:
         *	一行"用了 N 个工具", 和它真正说的那句话.
         */
        const card = this.rows.find((row) => row.kind === "widget" && row.widget?.id === p.did);
        if (card === undefined) return false;
        card.merged = true;
        card.revision += 1;
        this.#onRowChanged?.(card);
        const label = String(card.widget?.title ?? "").split("\n")[0]!.trim();
        // 三档, 不是二分 —— 过期是自己一档, 见 outcome.ts
        const outcome = outcomeOf(p.choice);
        const entry: ToolCall = {
          name: outcome.label, arg: label, approval: true, failed: outcome.failed
        };
        const group = this.#toolGroups.get(base.pid) ?? this.#lastToolGroup(base.pid);
        if (group !== undefined) {
          group.calls?.push(entry);
          group.revision += 1;
          this.#onRowChanged?.(group);
          return false;
        }
        return this.#push({ ...base, kind: "tools", text: "", calls: [entry] });
      }
      case "cap.denied":
        /**
         * 不占一行.
         *
         *	紧跟着就会弹一张审批卡, 而卡上写的就是"要什么权限做什么" ——
         *	同一件事说两遍. 一次审批本来要占四行(需要授权 / 卡片 /
         *	你批准了 / 结果), 长任务一屏能刷十几次, 对话就被冲没了.
         */
        return false;
      case "cap.used":
        // 内核的审计线索, 不是给人看的一行.
        //
        //	早先它也占一行, 于是**一次工具调用刷三行**:
        //	step(要调什么) + cap.used(用了哪条能力) + tool_ok(结果).
        //	三行说的是同一件事, 而人只关心"动了什么、成没成".
        return false;
      case "proc.outcome": {
        this.#closeAll(base.pid);
        const p = event.payload as OutcomePayload;
        // **成功不报**. "跑了测试, 全绿" 每轮来一次, 就是噪音的主要来源;
        // 干成了本来就该从结果里看出来. 只有出事才值得占一行.
        if (p.ok) return false;
        return this.#push({ ...base, kind: "outcome", title: t("没干成"), text: p.summary, tone: "bad" });
      }
      case "signal.in": {
        const p = event.payload as SignalPayload;
        return this.#push({ ...base, kind: "note", title: p.source, text: p.summary });
      }
      case "wake.fired": case "wake.set":
        return this.#push({ ...base, kind: "wake", title: event.kind === "wake.set" ? t("定了闹钟") : t("醒了"), text: (event.payload as WakePayload).why });
      case "budget.spent":
        return false; // 记账不进对话, 进状态条
      case "user.correction":
        /**
         * 纠正**不占一行**.
         *
         *   这句话本身已经作为 input.recv 显示过了; 再来一行只印一个
         *   事件名、正文是空的 —— 用户看到的是三行光秃秃的
         *   "user.correction"。它是给模型和审计用的标记, 不是对话内容。
         */
        return false;
      default:
        return this.#push({ ...base, kind: "note", title: event.kind, text: "" });
    }
  }

  /**
   * ReAct 阶段 → 行.
   *
   *   显示什么、不显示什么, 判据是**这一条对人有没有用**:
   *   step/tool_ok 有用(它在动什么), budget/reclaim 没用(那是记账),
   *   所以后者只留在日志里不占一行.
   */
  #applyPhase(event: OsEvent, phase: string): boolean {
    const id = `${event.pid}#${event.seq}`;
    const base = { id, pid: event.pid, at: event.at, seq: event.seq, revision: 0, open: false };
    const body = event.payload as Record<string, unknown>;
    const text = (key: string): string => {
      const value = body[key];
      return typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value);
    };
    switch (phase) {
      /**
       * 合进主干了 —— **这条要单独占一行**.
       *
       *   主干是所有人共用的那一份, 它变了是这间屋子里最该被看见的一件事.
       *   混在"用了 3 个工具"里折起来的话, 事后翻这段对话根本看不出
       *   这一轮到底有没有交付.
       */
      case "merged":
        return this.#push({ ...base, kind: "note", who: this.#voice(),
          title: t("合进主干"), text: text("text"), tone: "ok" });
      case "reply": case "done": {
        this.#closeAll(base.pid);
        // 机器自己叫醒那一轮的回话是**任务回执**, 不是对人说的话
        if ((event.payload as { quiet?: unknown }).quiet === true) return false;
        const raw = unwrapSaid(text(phase === "reply" ? "text" : "detail") || text("text"));
        /**
         * 正文里手写的卡片标记 —— **抠出来画成真卡片**.
         *
         *	模型可能生成 `[[card: progress {…}]]` 这类写法并直接写进
         *	回话。卡片的唯一出口是 show 工具(走 ui 通道), 正文里的标记
         *	不会变成卡片 —— 屏幕上留着的是一段 JSON.
         *
         *	源头已经在 show 的工具说明里堵了, 但堵不干净: 换个模型、
         *	换个供应商, 随时可能又发明一次. 而这时候三条路里 ——
         *	原样显示(一段 JSON)、悄悄删掉(它说着"如上"而什么都没有)、
         *	照着画 —— 只有第三条不丢信息.
         */
        const lifted = liftInlineCards(raw);
        let changed = false;
        for (const spec of lifted.specs) {
          changed = this.#push({ ...base, id: `${base.id}~${spec.id}`,
            kind: "widget", text: "", widget: spec, who: this.#voice() }) || changed;
        }
        if (lifted.text === "") return changed;
        return this.#push({ ...base, kind: "said", who: this.#voice(), text: lifted.text }) || changed;
      }
      case "step": {
        const thought = text("thought");
        let structural = false;
        if (thought !== "") {
          // 一轮只有一行: 后面几步的思考**并进同一行**, 展开时按顺序全在
          const open = this.#thoughtRows.get(base.pid);
          if (open !== undefined) {
            open.text = `${open.text}\n\n${thought}`;
            open.revision += 1;
            this.#onRowChanged?.(open);
          } else {
            structural = this.#push({ ...base, kind: "thought", who: this.#voice(), text: thought });
            this.#thoughtRows.set(base.pid, this.rows[this.rows.length - 1]!);
          }
        }
        const tool = text("tool");
        if (tool === "") return structural;
        /**
         * 一次工具调用 = **一行**, 结果回来就地写进去.
         *
         *   不这么做的话一次调用刷两行(要调什么 / 调完了), 一次干活
         *   十几个工具就是三十行, 人从里面读不出"它到底动了什么".
         */
        /**
         * 一轮里用过的工具**收成一行**.
         *
         *   这是 bot 聊天不是 IDE: 用户让它干活, 不是让它汇报每一步.
         *   一次干活十几个工具, 一个一行就把对话冲掉了 ——
         *   人再也看不出"我说了什么、它答了什么".
         *
         *   跑的时候这一行是展开的(看得见进度), 这一轮说完话就折起来,
         *   变成"用了 N 个工具", 想看点开.
         */
        const call: ToolCall = {
          name: tool, arg: shortArgs(body.args, this.#root), running: true, kind: callKind(tool, body.args)
        };
        const group = this.#toolGroups.get(base.pid);
        if (group !== undefined) {
          // 又开始动手了 —— 组重新变成"在跑"
          if (!group.open) group.open = true;
          group.calls?.push(call);
          group.revision += 1;
          this.#onRowChanged?.(group);
          return false; // 行数没变
        }
        return this.#openToolGroup({ ...base, id: `${id}:tools` }, [call]);
      }
      /**
       * 并行批次开跑 —— **先把工具行占上位置**.
       *
       *   批次不发 step, 组要等到第一个结果回来才建. 而工具在跑的过程中
       *   自己会往对话里发东西(比如 show 发的卡片), 那些行就落在了
       *   工具行**上面** —— 界面上看起来是"先出了一张卡, 然后才开始用工具".
       *   顺序错了, 因果就反了.
       *
       *   占位的组是空的, 那一刻显示"正在动手…" —— 它确实正在动手.
       */
      case "batch": {
        if (this.#toolGroups.get(base.pid) !== undefined) return false;
        return this.#openToolGroup(base, []);
      }
      case "tool_ok": case "tool_err": {
        const failed = phase === "tool_err";
        const blocked = failed && body.blocked === true;
        const missing = failed && body.missing === true;
        const detail = clip(failed ? text("err") : toolSummary(text("result")), failed ? 200 : 400);
        /**
         * 结果**永远进组**, 不再单独起一行.
         *
         *	找不到组的路径是真实存在的: agent 的并行批次(batch)不发 step,
         *	于是没有组被建出来, 每个结果各自成行 —— 那是"没缩成一行"的
         *	最后一条漏网路径, 而且它的 payload 里连 tool 名都没有.
         *	所以: 有组填进去, 没有就现建一个.
         */
        const toolName = text("tool") || guessTool(body.args);
        const group = this.#toolGroups.get(base.pid) ?? this.#lastToolGroup(base.pid);
        const calls = group?.calls ?? [];
        let call: ToolCall | undefined;
        for (let index = calls.length - 1; index >= 0; index -= 1) {
          const candidate = calls[index];
          if (candidate?.running === true) { call = candidate; break; }
        }
        const made: ToolCall = {
          name: toolName, arg: shortArgs(body.args, this.#root), result: detail,
          kind: callKind(toolName, body.args),
          ...(failed ? { failed: true } : {}), ...(blocked ? { blocked: true } : {}),
          ...(missing ? { missing: true } : {})
        };
        if (group === undefined) return this.#openToolGroup(base, [made]);
        if (call === undefined) group.calls?.push(made);
        else {
          call.running = false;
          call.result = detail;
          call.kind = callKind(call.name, body.args);
          if (failed) call.failed = true;
          if (blocked) call.blocked = true;
          if (missing) call.missing = true;
        }
        if (group.open && (group.calls ?? []).every((c) => c.running !== true)) group.open = false;
        group.revision += 1;
        this.#onRowChanged?.(group);
        return false;
      }
      case "turn_failed": case "stall": case "step_limit": case "model_err": {
        /**
         * 这一轮没走完, **必须看得见**.
         *
         *   早先它渲染成一行 11px 的灰字, 于是用户看到的是"它不理我" ——
         *   而真相是 OS 的停滞检测把它拦下了(连着三次找不到同一个东西,
         *   说明它对现实的判断是错的). 一个被拦下的 turn 跟一个
         *   没反应的 bot 在界面上必须长得完全不一样.
         */
        // **先取再关**: #closeAll 会把 #lastAlert 清掉(那是轮次边界该做的),
        // 顺序反了的话每一条报错都看不到上一条, 合并永远不发生
        const same = this.#lastAlerts.get(base.pid);
        this.#closeAll(base.pid);
        /**
         * **detail 才是那句有用的话**.
         *
         *   撞上止损线时 why 只是"止损线"三个字, 而 detail 里写着
         *   "这一轮已经花掉 15k token, 超过你在设置里定的每轮上限 3k"——
         *   只显示前者时, 那张卡等于什么都没说: 人既不知道花了多少,
         *   也不知道那道线来自设置.
         */
        const why = clip(text("err") || text("detail") || text("why") || text("msg"), 300);
        /**
         * **重试的那几次不各占一张卡**.
         *
         *   模型出错时上层会连着重试三次, 每次都发一条 model_err ——
         *   于是同一件事在屏幕上是三张一模一样的红卡, 而最后那条
         *   turn_failed 里已经写着"模型连续 3 次出错". 用户看到的是
         *   刷屏, 读到的信息只有一份.
         *
         *   跟"一轮里用过的工具收成一行""一轮里的思考收成一行"是同一条规矩:
         *   **同一件事只占一行**, 重复的次数标在旁边.
         */
        /**
         * **同一件事被报两遍, 只算一遍**.
         *
         *	停滞检测先发一条 stall, 这一轮随即中止又发一条 turn_failed,
         *	而后者的正文就是前者**加了个前缀**:
         *
         *	  stall        同一类错误连着出现 3 次了。…
         *	  turn_failed  检测到停滞: 同一类错误连着出现 3 次了。…
         *
         *	屏幕上是两张几乎一模一样的黄卡, 而那个前缀说的事标题上
         *	已经写着了("这一轮我停下来了"). 真机截图里就是这样.
         *
         *	跟"重试三次"要分开: 一模一样 = 真的又发生了一次, 次数要标;
         *	一条包着另一条 = 同一件事换了个说法, 不该标次数(标了就成了
         *	"发生过两次", 那是假的).
         */
        if (same !== undefined && same.text === why) {
          same.repeat = (same.repeat ?? 1) + 1;
          same.revision += 1;
          this.#onRowChanged?.(same);
          // **合并完要把它放回去**: 上面那次 #closeAll 已经清掉了追踪,
          // 不放回去的话第三次重试又会另起一张 —— 三次重试变成两张卡
          this.#lastAlerts.set(base.pid, same);
          return false;
        }
        if (same !== undefined && why !== "" && wraps(same.text, why)) {
          // 留先到的那条: 后到的那个前缀跟标题重复, 留它反而更啰嗦
          this.#lastAlerts.set(base.pid, same);
          return false;
        }
        const pushed = this.#push({ ...base, kind: "alert", who: this.#voice(),
          // step_limit 这个名字是历史遗留 —— 真正撞上的多半是**花费**那条线,
          // 而"步数到上限了"会让人去找一个根本不存在的步数设置
          title: phase === "stall" || phase === "turn_failed" ? t("这一轮我停下来了")
            : phase === "step_limit" ? (text("why") === "止损线" ? t("花到上限了") : t("到上限了"))
              : t("模型出错了"),
          text: why });
        this.#lastAlerts.set(base.pid, this.rows[this.rows.length - 1]!);
        return pushed;
      }
      /**
       * **插话被半路收到了, 得看得见**.
       *
       *	中途插话是"劝"不是"停": OS 只保证把话送到步与步之间, 停不停
       *	由它自己判断. 而界面上原来一个字都没有 —— 你说完"打住", 它
       *	接着又跑了两步, 你分不清是**没听见**还是**听见了在收尾**.
       *
       *	这两种情况该做的事完全相反: 前者要再说一遍, 后者要等一下.
       *
       *	一轮里最多一条, 而且是你自己按出来的 —— 不是刷屏.
       */
      case "interrupt":
        return this.#push({ ...base, kind: "note",
          title: t("半路收到了"), text: t("它会把手上这步做完再决定停不停") });
      case "budget_done": case "budget_starved":
        return this.#push({ ...base, kind: "note", title: t("预算到线"), text: clip(text("detail"), 160), tone: "warn" });
      case "empty_done":
        // 空的 done 曾经被当成"完成", 用户一个字没拿到而系统认为成功 ——
        // 这条必须看得见
        return this.#push({ ...base, kind: "note", title: t("它说完成了但什么都没给"), text: "", tone: "bad" });
      default:
        // start / batch / budget / reclaim / context_fold / approved / denied…
        // 都只留在日志里. 每一条都占一行的话, 一次干活会刷出几十行记账
        return false;
    }
  }

  /** 这一段跑完了: 折起来, 但**还留在手上** —— 同一轮后面还能往里加 */
  #restTools(pid: ProcessID): void {
    const group = this.#toolGroups.get(pid);
    if (group === undefined || !group.open) return;
    group.open = false;
    group.revision += 1;
    this.#onRowChanged?.(group);
  }

  /** 这一轮结束: 真正断开, 下一轮另起一组 */
  #closeTools(pid: ProcessID): void {
    this.#restTools(pid);
    /**
     * 思考并进过程行 —— 跟拍完板的审批卡同一个待遇.
     *
     *	折着的时候"想了一会儿"和"用了 N 个工具"各占一行, 说的是同一件事
     *	("过程收好了"), 间距还不一样 —— 用户截图点名的正是这两行.
     *	并成一行, 展开时想的在前、动的在后, 顺序就是它真实的顺序.
     *	只在轮次收口时并: 跑的时候两行都要活着 —— 那是进度, 不是占位.
     */
    const thought = this.#thoughtRows.get(pid);
    const group = this.#toolGroups.get(pid);
    if (thought !== undefined && group !== undefined && thought.merged !== true) {
      thought.merged = true;
      // 头像挂在思考行上(它是这一簇的第一句) —— 行隐了, 脸得跟着搬家,
      // 否则这一轮在屏幕上一张脸都没有, 看着像没主人的动作
      if (thought.showWho === true) {
        group.showWho = true;
        if (thought.who !== undefined) group.who = thought.who;
        thought.showWho = false;
      }
      thought.revision += 1;
      this.#onRowChanged?.(thought);
      group.thought = thought.text;
      group.revision += 1;
      this.#onRowChanged?.(group);
    }
    this.#toolGroups.delete(pid);
    // 思考跟工具同一个轮次边界 —— 下一轮另起一行, 否则两轮的想法会粘在一起
    this.#thoughtRows.delete(pid);
    // 报错也一样: 下一轮的同样报错该另起一张卡, 否则两轮的失败看着像一次
    this.#lastAlerts.delete(pid);
    this.#turnEnds.set(pid, this.rows[this.rows.length - 1]?.id ?? "");
  }

  /**
   * 本轮的过程行.
   *
   *   **只在本轮里找**. 早先是"往回翻 6 行", 结果并行批次的结果
   *   被塞进了上一轮的组 —— 这一轮显示"用了 1 个工具", 而它实际调了 3 个.
   *   少算比多算更坏: 人以为它没干那些事.
   */
  #lastToolGroup(pid: ProcessID): Row | undefined {
    const stop = this.#turnEnds.get(pid);
    for (let index = this.rows.length - 1; index >= 0; index -= 1) {
      const row = this.rows[index];
      if (row === undefined) continue;
      if (row.id === stop) return undefined; // 翻到上一轮的边界了
      // **同一个人的组才算**: 房间里翻回去可能撞上同屋另一个人的组
      if (row.kind === "tools" && row.pid === pid) return row;
    }
    return undefined;
  }

  /** 一行封口: 关掉, 并且不再往里接后面的块 */
  #seal(key: string, row: Row): void {
    row.open = false;
    this.#open.delete(key);
  }

  /** 这个进程正在流的那几行封口 —— 不碰轮次边界 */
  #closeStreams(pid: ProcessID): void {
    for (const [key, row] of this.#open) {
      if (row.pid !== pid) continue;
      row.open = false; row.revision += 1;
      this.#onRowChanged?.(row);
      this.#open.delete(key);
    }
  }

  #closeThoughts(pid: ProcessID): void {
    for (const [key, row] of this.#open) {
      if (row.kind !== "thought" || row.pid !== pid) continue;
      row.open = false; row.revision += 1;
      this.#onRowChanged?.(row);
      this.#open.delete(key);
    }
  }

  #closeAll(pid: ProcessID): void {
    for (const [key, row] of this.#open) {
      if (row.pid !== pid) continue; // 别人的行不归这一轮管
      row.open = false; row.revision += 1;
      this.#onRowChanged?.(row);
      this.#open.delete(key);
    }
    this.#closeTools(pid);
  }
}

/**
 * 把正文里手写的 `[[card: kind {…}]]` 抠出来.
 *
 *   **数括号, 不找 ']]'**: JSON 里 "rows":[["a","b"]] 这样的嵌套会把
 *   找结尾那种写法骗到半路, 于是后半段话跟着一起消失.
 *
 *   解不开的原样留着 —— 半个标记也比无声吞掉一段话强.
 */
export function liftInlineCards(raw: string): { text: string; specs: WidgetSpec[] } {
  const OPEN = "[[card:";
  if (!raw.includes(OPEN)) return { text: raw, specs: [] };
  const specs: WidgetSpec[] = [];
  let out = "";
  let i = 0;
  while (i < raw.length) {
    const at = raw.indexOf(OPEN, i);
    if (at < 0) { out += raw.slice(i); break; }
    out += raw.slice(i, at);
    const brace = raw.indexOf("{", at + OPEN.length);
    const close = brace < 0 ? -1 : endOfObject(raw, brace);
    if (close < 0) { out += OPEN; i = at + OPEN.length; continue; }
    const kind = raw.slice(at + OPEN.length, brace).trim();
    let end = close + 1;
    const tail = raw.indexOf("]]", end);
    if (tail >= 0 && raw.slice(end, tail).trim() === "") end = tail + 2;
    try {
      const parsed: unknown = JSON.parse(raw.slice(brace, close + 1));
      if (typeof parsed === "object" && parsed !== null) {
        const spec = parsed as Record<string, unknown>;
        const type = typeof spec.type === "string" && spec.type !== "" ? spec.type : kind;
        // id 是渲染端认卡片用的, 手写的标记里没有 —— 自己编一个稳定的
        if (type !== "") specs.push({ ...spec, type, id: `inline-${specs.length}-${type}` } as WidgetSpec);
      }
    } catch { /* 解不开就当没有这张卡, 话已经收进 out 了 */ }
    i = end;
  }
  return { text: tidyBlanks(out), specs };
}

/** 配对的 '}' 在哪儿. 认字符串里的括号, -1 = 没配上 */
function endOfObject(s: string, start: number): number {
  let depth = 0, inStr = false, esc = false;
  for (let i = start; i < s.length; i++) {
    const c = s[i];
    if (inStr) {
      if (esc) esc = false;
      else if (c === "\\") esc = true;
      else if (c === "\"") inStr = false;
      continue;
    }
    if (c === "\"") inStr = true;
    else if (c === "{") depth++;
    else if (c === "}") { depth--; if (depth === 0) return i; }
  }
  return -1;
}

/** 抠掉标记之后收一下空行 —— 标记独占一行会留下两个换行 */
function tidyBlanks(s: string): string {
  const out: string[] = [];
  for (const line of s.split("\n")) {
    if (line.trim() === "" && (out.length === 0 || out[out.length - 1]!.trim() === "")) continue;
    out.push(line);
  }
  while (out.length > 0 && out[out.length - 1]!.trim() === "") out.pop();
  return out.join("\n").trim();
}

function parseWidget(text: string): WidgetSpec | undefined {
  try {
    const parsed: unknown = JSON.parse(text);
    if (typeof parsed !== "object" || parsed === null) return undefined;
    const spec = parsed as Partial<WidgetSpec>;
    return typeof spec.type === "string" && typeof spec.id === "string" ? parsed as WidgetSpec : undefined;
  } catch { return undefined; }
}

/** 工具参数只显示最能说明"动了什么"的那一个 */
function shortArgs(args: unknown, root?: string): string {
  if (typeof args !== "object" || args === null) return "";
  const record = args as Record<string, unknown>;
  for (const key of ["path", "cmd", "command", "query", "pattern", "url"]) {
    const value = record[key];
    if (typeof value === "string" && value.length > 0) {
      return clip(key === "path" ? inWorkspace(value, root) : value, 120);
    }
  }
  return clip(JSON.stringify(record), 120);
}

/**
 * 工作区里的路径写成相对的.
 *
 *   展开一组工具调用时可能是这样:
 *
 *     read_file  <workspace>/README.md
 *     read_file  <workspace>/PLAN.md
 *     list_dir   <workspace>/static
 *
 *   每一行八成的字符**逐字相同**, 而且相同的那截在前面 —— 真正在变的
 *   末尾那一小截被推到行中间, 眼睛得横着扫过去才能找到. 一组十几行的话,
 *   这就是一堵墙.
 *
 *   工作区**是 bot 干活的地方**, 说一遍就够了(会话头上一直写着). 出了工作区
 *   才是例外, 那种反而要看清 —— 所以只把工作区内的收起来, 外面的照旧.
 */
function inWorkspace(path: string, root?: string): string {
  if (root === undefined || root === "") return path;
  const base = root.replace(/\/+$/, "");
  // 就是工作区本身 —— 写个点比写一整条绝对路径清楚
  if (path === base) return ".";
  // **按目录边界判, 不按字符串前缀**: oa 和 oa-backup 是两个目录,
  // 按前缀收的话后者会变成 "-backup/README.md" —— 指向一个别的地方,
  // 而且看起来还挺像回事. 这条是测试当场抓出来的.
  return path.startsWith(base + "/") ? path.slice(base.length + 1) : path;
}

function clip(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max)}…`;
}

/**
 * 工具结果里人要看的那部分.
 *
 *   工具回的是结构化 JSON(status/detail/…), 整段贴出来
 *   人读到的是一堆引号和花括号, 真正有用的那句被埋在里面.
 */
function toolSummary(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed.startsWith("{")) return trimmed;
  try {
    const parsed = JSON.parse(trimmed) as Record<string, unknown>;
    for (const key of ["detail", "output", "result", "text", "message"]) {
      const value = parsed[key];
      if (typeof value === "string" && value.length > 0) return value;
    }
    return trimmed;
  } catch { return trimmed; }
}

/** 剥掉捎带的房间上下文, 只留用户真正说的那句 */
export function stripRoomContext(text: string): string {
  const at = text.lastIndexOf(ROOM_CONTEXT_TAIL);
  return at === -1 ? text : text.slice(at + ROOM_CONTEXT_TAIL.length);
}

/** 批次结果不带工具名, 只能从参数形状认 */
function guessTool(args: unknown): string {
  if (typeof args !== "object" || args === null) return t("工具");
  const record = args as Record<string, unknown>;
  if (typeof record.content === "string") return "write_file";
  if (typeof record.cmd === "string" || typeof record.command === "string") return "run";
  if (typeof record.pattern === "string" || typeof record.query === "string") return "search";
  if (typeof record.path === "string") return "read_file";
  return t("工具");
}

function callKind(name: string, args: unknown): CallKind {
  const record = typeof args === "object" && args !== null ? args as Record<string, unknown> : {};
  // **看图那一次要把图画出来**: 拿回来的只是一段转述, 而人一眼就能
  // 判断"它看的到底是不是我说的那张" —— 那句转述做不到这件事
  if (name.includes("image") || name.includes(t("图"))) return "image";
  if (name.includes("write") || name.includes("edit") || typeof record.content === "string") return "write";
  if (name === "run" || typeof record.cmd === "string" || typeof record.command === "string") return "run";
  if (name.includes("search") || name.includes("find") || name.includes("grep")) return "search";
  if (name.includes("read") || name.includes("list") || name.includes("dir")) return "read";
  return "text";
}
