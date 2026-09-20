import { tt } from "../i18n/index.js";
/**
 * roomcard —— **一个群到底是什么, 用一屏说清**.
 *
 *   ── 为什么群比人更需要这张卡 ──
 *
 *	点一个 bot 的头像能看到他是谁、在哪儿干活、产物在哪条分支上.
 *	点一个群, 什么都没有 —— 而群恰恰是**信息更多**的那一个:
 *	里面几个人、他们是不是在同一摊活上、各自压着几条没合的分支.
 *
 *	日常 IM 里这张卡是右上角那三个点: 成员头像墙、群名、群公告.
 *	这里的"群"是一个项目组, 所以那几栏要换成这摊活自己的事实.
 *
 *   ── 一条硬规矩: 只说事实, 不猜 ──
 *
 *	"这个群在哪儿干活"有两种答案: 大家在同一摊活上, 或者各干各的.
 *	后者是**真实存在而且要紧**的情况(拉人进来时忘了给工作区, 他就在
 *	自己的空目录里), 不能拿第一个人的路径糊弄过去 —— 那正好把问题藏了.
 */

/** 判"这个群什么样"要的那一点点 */
export interface Face {
  readonly name: string;
  readonly state: string;
  readonly work?: string | undefined;
  readonly branch?: string | undefined;
  readonly project?: string | undefined;
}

export interface RoomFacts {
  /** 这摊活在哪儿. undefined = 各干各的 */
  readonly work: string | undefined;
  /** 各干各的 —— 那是**要说出来**的毛病, 不是一句"多个工作区"能带过的 */
  readonly split: boolean;
  /** 几个人正在干活 */
  readonly busy: number;
  /** 几个人不在了 —— 对着不在的人说话, 话会石沉大海 */
  readonly gone: number;
  /** 各自那条分支, 没分支的不列 */
  readonly branches: readonly { readonly name: string; readonly branch: string }[];
}

const LIVE = new Set(["running", "waiting", "created"]);

export function roomFacts(people: readonly Face[]): RoomFacts {
  const works = [...new Set(people.map((who) => who.work ?? "").filter((w) => w !== ""))];
  const projects = [...new Set(people.map((who) => who.project ?? "").filter((w) => w !== ""))];
  /**
   * 走分支隔离时每个人有**自己的 worktree**, work 天然各不相同 ——
   * 那不叫各干各的, 那正是说好的样子. 这时候"在哪儿干活"的答案是项目根.
   */
  const work = projects.length === 1 ? projects[0]
    : projects.length === 0 && works.length === 1 ? works[0]
      : undefined;
  return {
    work,
    split: work === undefined && (works.length > 1 || projects.length > 1),
    busy: people.filter((who) => who.state === "running").length,
    gone: people.filter((who) => !LIVE.has(who.state)).length,
    branches: people
      .filter((who) => (who.branch ?? "") !== "")
      .map((who) => ({ name: who.name, branch: who.branch! }))
  };
}

/** 一句话说清这屋里此刻什么情况 —— 侧栏那个绿点回答不了"几个人在动" */
export function roomPulse(facts: RoomFacts, total: number): string {
  const parts: string[] = [];
  if (facts.busy > 0) parts.push(tt("{a} 个在干活", { a: facts.busy }));
  if (facts.gone > 0) parts.push(tt("{a} 个不在了", { a: facts.gone }));
  const idle = total - facts.busy - facts.gone;
  if (idle > 0) parts.push(tt("{a} 个闲着", { a: idle }));
  return parts.join(" · ");
}
