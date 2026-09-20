/**
 * grouping — 会话怎么分堆.
 *
 *   ── 默认不分堆 ──
 *
 *   用户的原话: "微信他跟谁聊天也没有说分组吧…现在这种简约我都非常喜欢的。
 *   但是我刚刚发现，确实不同的助手太多，我自己都分不清。但是我又不想搞
 *   高复杂度。"
 *
 *   所以: **默认还是那一条平的列表**(跟微信一样), 分组是一个可以切过去的
 *   视图(跟 QQ 一样). 不是默认行为, 也不是设置项里藏着的开关 ——
 *   列表顶上一个两格的切换, 一眼看得见、一下切得回来.
 *
 *   ── 按什么分 ──
 *
 *   **按工作区**, 不按房间. 房间是"谁跟谁在一起说话", 工作区是"这摊活
 *   在哪儿" —— 用户脑子里的"项目"是后者: 他会说"你去干这个 OA 项目",
 *   不会说"你进那个房间".
 *
 *   一个人可以没有工作区(系统分的匿名目录), 那种归到"其他" ——
 *   **不硬塞进某个项目**: 塞错了比不塞更难发现.
 */

import { t } from "../i18n/index.js";
import type { Party } from "./parties.js";
import { dominantWork } from "./workspace.js";

/** 一堆会话 */
export interface Group {
  /** 这一堆叫什么 —— 项目目录的最后一段 */
  readonly name: string;
  /**
   * 这个项目在磁盘上的哪儿. undefined = 这一堆不是一个项目("其他").
   *
   *   **要整条路径不要短名字**: 项目卡要拿它去问 git, 而两个不同目录
   *   完全可能叫同一个名字(~/a/oa 和 ~/b/oa).
   */
  readonly path?: string;
  readonly parties: readonly Party[];
}

/** 没派项目的那些归到这儿 */
export const OTHERS = t("其他");

/**
 * projectOf 这段会话属于哪个项目. 没有就是空.
 *
 *   哪个工作区算"这个房间的", 由 workspace.ts 说了算 —— 这里只把它
 *   变成一个短名字.
 */
export function projectRootOf(party: Party): string {
  const people = party.people.length > 0 ? party.people : party.members;
  const project = people.map((m) => m.project).find((p) => p !== undefined && p !== "");
  return project ?? dominantWork(people) ?? "";
}

export function projectOf(party: Party): string {
  const people = party.people.length > 0 ? party.people : party.members;
  /**
   * **先认项目根, 再退回工作区**.
   *
   *   走分支隔离之后, 每个人的工作区是自己那一个 worktree —— 一个项目
   *   里六个人就是六条互不相同的路径, 照着它分堆的话六个人分成六堆,
   *   而他们干的是同一摊活. 项目根是内核发下来的(见 go 侧的 project 标签),
   *   它才是"同一个项目"的判据.
   */
  const project = people.map((m) => m.project).find((p) => p !== undefined && p !== "");
  const work = project ?? dominantWork(people);
  return work?.split("/").pop() ?? "";
}

/**
 * byProject 把会话分堆. **顺序不变** —— 组内保持原来的先后,
 * 组之间按"组里最靠前的那个"排.
 *
 *   稳定的顺序是列表能用的前提: 每次刷新都重排的话, 人刚记住的位置就没了.
 */
export function byProject(parties: readonly Party[]): readonly Group[] {
  const order: string[] = [];
  const buckets = new Map<string, Party[]>();
  const roots = new Map<string, string>();
  for (const party of parties) {
    const name = projectOf(party) || OTHERS;
    if (name !== OTHERS && !roots.has(name)) {
      const root = projectRootOf(party);
      if (root !== "") roots.set(name, root);
    }
    let bucket = buckets.get(name);
    if (bucket === undefined) {
      bucket = [];
      buckets.set(name, bucket);
      order.push(name);
    }
    bucket.push(party);
  }
  // "其他"永远排最后 —— 它不是一个项目, 是"剩下的"
  const named = order.filter((n) => n !== OTHERS);
  if (buckets.has(OTHERS)) named.push(OTHERS);
  return named.map((name) => {
    const root = roots.get(name);
    return { name, parties: buckets.get(name) ?? [], ...(root === undefined ? {} : { path: root }) };
  });
}
