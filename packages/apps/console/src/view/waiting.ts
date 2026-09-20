/**
 * waiting —— **它到底还在不在干活**.
 *
 *   ── 原来那条判据只对了三秒 ──
 *
 *	原来的规矩是"最后一行是我说的话 → 显示『正在想…』". 于是真实的
 *	时间线长这样:
 *
 *	    我: @OA助手 你在干嘛
 *	    正在想…                  ← 有
 *	    OA助手 › 用了 2 个工具    ← 从这一刻起, 占位消失
 *	    (然后是一分半钟的空白)
 *
 *	它一动手, 最后一行就不再是我说的话了, 占位跟着没了 —— 而那一分半
 *	钟正是**最需要有个东西告诉我"它还在"**的时候. 用户的原话:
 *	"要不然我都不知道他在干什么".
 *
 *   ── 判据换成进程状态 ──
 *
 *	running 就是"这一轮还没走完", 由内核说了算, 不猜.
 *	它停在 Recv 上(waiting)才是真的没人在动 —— 那时候不该有占位,
 *	不然占位就一直亮着, 也就不再有信息量(跟 presence 那个点一个道理).
 *
 *   ── 顺带说清楚它在干什么 ──
 *
 *	"正在想…"和"正在跑命令…"对人是两回事: 前者该等, 后者可能要很久.
 *	最后那行工具知道是哪一种, 白拿的信息, 不用白不用.
 */

import { t, tt } from "../i18n/index.js";
import type { Row } from "./rows.js";

/** 判"谁还在干活"要的那一点点 —— 不绑 Party, 这样测试不用造整个进程表 */
export interface Runner {
  readonly pid: string;
  readonly name: string;
  readonly state: string;
}

/** 干活的人正在干的那一类事 → 人话 */
const DOING: Record<string, string> = {
  run: t("正在跑命令"),
  write: t("正在改文件"),
  read: t("正在翻文件"),
  search: t("正在找东西")
};

/** 忙碌清单里的一个人 —— 点开状态条的脸时, 每人一行 */
export interface BusyOne {
  readonly name: string;
  readonly doing: string;
  readonly steps?: number;
  readonly startedAt?: number;
  readonly tokens?: number;
}

/** 底下那条占位: 谁在动, 他在干什么 */
export interface Waiting {
  /** 谁 —— **占位也要有脸**: 群里三个人, 光一句"正在想…"看不出是谁在想.
   *  认不出是谁(比如刚发完话还没人接手)就不给, 宁可空着也不摆错一张脸 */
  readonly who?: string;
  readonly label: string;
  /**
   * 此刻在干活的都有谁 —— **刹车要按得着他们**.
   *
   *   屋里三个人同时干活时 who 是空的(摆谁的脸都是错的), 而那恰恰是
   *   最需要一颗"全停"的时候: 三条命令一起跑飞, 原来一个都停不下来.
   */
  readonly busy: readonly string[];
  /**
   * 这一轮干到哪儿了 —— **它不限轮次也不限步数**, 所以给不出百分比:
   * 一个百分比要么是编的, 要么要它先承诺一个总数, 而那个承诺也是猜的.
   * 能说的是已经发生的事实: 干了几步、从什么时候开始、花了多少.
   */
  readonly steps?: number;
  readonly startedAt?: number;
  readonly tokens?: number;
  /**
   * 每个忙着的人各自在干什么 —— 状态条点开那张清单用.
   *
   *	几个人同时跑时, 条上只有"N 个人正在干活"; 想停**其中一个**
   *	得知道谁在干什么. 因此清单按人列出正在执行的工作, 让暂停操作
   *	能够对应到具体对象.
   */
  readonly crew?: readonly BusyOne[];
}

/**
 * waitingHint 底下那条占位 —— 没人在等就 undefined(不给占位).
 *
 * @param people  这个会话里的人(去重之后的, 一个 bot 只算一次)
 * @param rows    已经投影出来的行 —— 拿来认"他正在干哪一类事"
 */
export function waitingHint(
  people: readonly Runner[],
  rows: readonly Row[],
  /** 这一轮干到哪儿了 —— 由 store 算, 见 EventStore.turnProgress */
  progressOf?: (pid: string) => { steps: number; startedAt: number; tokens: number } | null
): Waiting | undefined {
  const busy = people.filter((who) => who.state === "running");
  const last = lastShown(rows);

  /**
   * ── **话一出来就撤** ──
   *
   *	它开口之后, 这一轮往往还没走完(收尾、自动提交、下一步工具),
   *	进程还是 running. 可这时候屏幕上已经有字了 —— 再挂一条"正在想…"
   *	就是在回答一个已经被回答了的问题, 而且会一直挂到整轮结束.
   *	用户的原话: "回复成功后立马消失", "正在想显示了很久才消失".
   *
   *	它要是接着动手, 下一条工具行会立刻把占位带回来 —— 那时候占位说的
   *	是新的一件事, 不是同一句话赖着不走.
   */
  /**
   * **只有"他自己刚说完"才算撤**.
   *
   *	只看"最后一行是 said"在群里是错的: 屋里三个人,
   *	甲刚回完话, 乙丙正在跑命令, 最后一行是甲的话, 于是占位整个消失,
   *	连那颗"停"也跟着没了. 4 条 sleep 在跑时可能出现这种结果,
   *	界面上一个字都没有.
   */
  if (last?.kind === "said" && (busy.length === 0 || busy.some((who) => who.pid === last.pid))) {
    return undefined;
  }

  if (busy.length === 0) {
    /**
     * 还没转成 running 的那一小段也要有占位.
     *
     *	按下回车到内核把进程标成 running 之间有几百毫秒 —— 可能出现
     *	两帧空白. 这段时间**恰恰是人最盯着屏幕的时候**:
     *	刚发完话, 想知道有没有发出去.
     */
    if (last?.kind === "heard") return { label: t("正在想…"), busy: [], ...face(addressee(last, people)) };
    /**
     * **手上有一条工具还在跑, 那它当然在干活** —— 哪怕进程状态还写着
     * 在线. 进程状态可能比事件慢一拍(工具行都画出来了, 状态还是
     * waiting), 只信状态的话那一拍就是一次闪烁: 有 → 没 → 有.
     */
    if (last?.kind === "tools" && last.calls?.some((call) => call.running === true) === true) {
      return { label: doingOf(last.pid, rows) + "…", busy: [], ...face(nameOf(last.pid, people)) };
    }
    return undefined;
  }
  /**
   * **他忙着的时候你说了话, 要给回执**.
   *
   *	插话其实已经进了他的收件箱(账本里有 interrupt), 这步跑完他就看到 ——
   *	但界面上只有"正在想…", 用户对着它干等, 不知道话到底送没送到.
   *	真机原话: "其实你可以快速 fork 一个他的分身来回答我" —— 分身也要
   *	走一次同样的模型往返, 不会更快; 缺的是**被听见的确认**.
   */
  const interjected = last?.kind === "heard";

  // 每个人各自在干什么 —— 点开状态条那张清单要用
  const crew: BusyOne[] = busy.map((who) => {
    const step = progressOf?.(who.pid) ?? null;
    return {
      name: who.name, doing: doingOf(who.pid, rows),
      ...(step === null ? {} : { steps: step.steps, startedAt: step.startedAt, tokens: step.tokens })
    };
  });

  // 好几个人同时在干 —— 一个个念出来只会占满一行, 报个数就够了.
  // 脸也不摆: 摆谁的都是错的
  if (busy.length > 1) {
    return {
      label: interjected ? t("都看到你的话了，手头这步跑完就回…") : tt("{a} 个人正在干活…", { a: busy.length }),
      busy: busy.map((one) => one.name),
      crew
    };
  }

  const one = busy[0]!;
  const step = progressOf?.(one.pid) ?? null;
  return {
    label: interjected ? t("看到你的话了，手头这步跑完就回…") : doingOf(one.pid, rows) + "…",
    who: one.name, busy: [one.name], crew,
    ...(step === null ? {} : { steps: step.steps, startedAt: step.startedAt, tokens: step.tokens })
  };
}

/**
 * addressee 这句话是冲谁说的 —— 拿来在他开口之前就把脸摆上.
 *
 *	一对一时没有第二种可能; 群里就看 @ 了谁. **只 @ 了一个人才算**:
 *	@ 了两个的话摆谁的脸都是偏的, 那就先空着(一秒之内他自己就动起来了).
 */
function addressee(heard: Row, people: readonly Runner[]): string | undefined {
  if (people.length === 1) return people[0]!.name;
  const called = people.filter((who) => heard.text.includes("@" + who.name));
  return called.length === 1 ? called[0]!.name : undefined;
}

function nameOf(pid: string, people: readonly Runner[]): string | undefined {
  return people.find((who) => who.pid === pid)?.name;
}

/** exactOptionalPropertyTypes: 认不出是谁就**一个字段都不给**, 不给 undefined */
function face(who: string | undefined): { who?: string } {
  return who === undefined ? {} : { who };
}

/**
 * doingOf 这个人最后一步在干什么.
 *
 *	**只看他自己的行**: 群里另一个人刚跑完一条命令, 不代表他在跑命令.
 *	往回找也只找到最近一条属于他的过程行为止 —— 再往前是上一轮的事了,
 *	拿上一轮的动作描述这一轮就是在撒谎.
 */
function doingOf(pid: string, rows: readonly Row[]): string {
  for (let index = rows.length - 1; index >= 0; index -= 1) {
    const row = rows[index];
    if (row === undefined || row.pid !== pid || row.merged === true) continue;
    if (row.kind !== "tools") return t("正在想");
    // 一组工具里正在跑的那条才说明"此刻在干什么"; 都跑完了说明它在
    // 消化结果 —— 那是在想
    const live = row.calls?.find((call) => call.running === true);
    if (live === undefined) return t("正在想");
    return DOING[live.kind ?? ""] ?? t("正在干活");
  }
  return t("正在想");
}

/** 最后一条**画出来了的**行 —— 并掉的那些不算, 它们没占地方 */
function lastShown(rows: readonly Row[]): Row | undefined {
  for (let index = rows.length - 1; index >= 0; index -= 1) {
    const row = rows[index];
    if (row !== undefined && row.merged !== true) return row;
  }
  return undefined;
}
