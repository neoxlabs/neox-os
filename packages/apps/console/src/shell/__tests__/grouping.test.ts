import { describe, expect, it } from "vitest";

import { OTHERS, byProject, projectOf } from "../grouping.js";
import { membersFromHistory } from "../parties.js";

const party = (title: string, works: (string | undefined)[]) => ({
  id: title, title, isRoom: works.length > 1, state: "waiting",
  members: works.map((work, i) => ({ pid: `${title}-${i}`, name: `${title}${i}`, state: "waiting", ...(work === undefined ? {} : { work }) })),
  people: works.map((work, i) => ({ pid: `${title}-${i}`, name: `${title}${i}`, state: "waiting", ...(work === undefined ? {} : { work }) })),
}) as never;

// 走分支隔离之后每个人的工作区是自己那一个 worktree —— 一个项目里六个人
// 就是六条互不相同的路径. "同一个项目"要认内核发下来的 project 标签.
const inProject = (title: string, project: string, names: string[]) => ({
  id: title, title, isRoom: names.length > 1, state: "waiting",
  members: names.map((name, i) => ({
    pid: `${title}-${i}`, name, state: "waiting",
    work: `/Users/x/.neox-os/worktrees/oa/${name}`, project, branch: `neox/${name}`
  })),
  people: names.map((name, i) => ({
    pid: `${title}-${i}`, name, state: "waiting",
    work: `/Users/x/.neox-os/worktrees/oa/${name}`, project, branch: `neox/${name}`
  })),
}) as never;

describe("按项目分堆", () => {
  it("各自 worktree 的几个人**还是一个项目** —— 照工作区分的话六个人分成六堆", () => {
    const 小登 = inProject("小登", "/Users/x/AI/test-projects/oa", ["小登"]);
    const 小查 = inProject("小查", "/Users/x/AI/test-projects/oa", ["小查"]);
    expect(projectOf(小登)).toBe("oa");
    expect(byProject([小登, 小查]).map((g) => g.name)).toEqual(["oa"]);
  });

  it("项目名取工作区目录的最后一段", () => {
    expect(projectOf(party("a", ["/Users/x/AI/oa"]))).toBe("oa");
    expect(projectOf(party("b", ["/Users/x/AI/neox-cloud/"]))).toBe("neox-cloud");
  });

  it("系统分的匿名目录不算项目 —— 那是还没派活的样子", () => {
    expect(projectOf(party("c", ["/Users/x/.neox-os/work/research"]))).toBe("");
  });

  it("没派项目的归到「其他」, 不硬塞进某个项目", () => {
    // 塞错了比不塞更难发现
    const groups = byProject([party("a", ["/Users/x/AI/oa"]), party("b", [undefined])]);
    expect(groups.map((g) => g.name)).toEqual(["oa", OTHERS]);
  });

  it("「其他」永远排最后 —— 它不是一个项目, 是剩下的", () => {
    const groups = byProject([party("b", [undefined]), party("a", ["/Users/x/AI/oa"])]);
    expect(groups[groups.length - 1]?.name).toBe(OTHERS);
  });

  it("顺序稳定 —— 每次刷新都重排的话, 人刚记住的位置就没了", () => {
    const list = [party("a", ["/Users/x/AI/oa"]), party("b", ["/Users/x/AI/fin"]), party("c", ["/Users/x/AI/oa"])];
    const once = byProject(list).map((g) => `${g.name}:${g.parties.map((p) => p.title).join(",")}`);
    const twice = byProject(list).map((g) => `${g.name}:${g.parties.map((p) => p.title).join(",")}`);
    expect(once).toEqual(twice);
    expect(once[0]).toBe("oa:a,c");
  });

  it("房间按成员的工作区归堆", () => {
    const room = party("#oa", ["/Users/x/AI/oa", "/Users/x/AI/oa"]);
    expect(projectOf(room)).toBe("oa");
  });
});

describe("死掉的进程也要能答出它当时在哪儿干活", () => {
  it("工作区从 created 事件里认 —— 能力集随进程一起没了", () => {
    // 工具行把工作区内的路径写成相对的, 靠的就是"这条事件属于哪个工作区".
    // 历史里没有它, 一整段旧对话就只能摊着一堆逐字相同的绝对路径.
    const store = {
      pids: () => ["p1"],
      eventsOf: () => [{
        kind: "proc.state",
        payload: { state: "created", work: "/Users/x/AI/oa", labels: { bot: "oa", name: "小勤" } }
      }]
    };
    expect(membersFromHistory(store)[0]?.work).toBe("/Users/x/AI/oa");
  });

  it("旧账本里没有这个字段, 不能因此就崩或者编一个出来", () => {
    const store = {
      pids: () => ["p1"],
      eventsOf: () => [{ kind: "proc.state", payload: { state: "created", labels: { bot: "oa", name: "小勤" } } }]
    };
    const m = membersFromHistory(store)[0];
    expect(m?.name).toBe("小勤");
    expect(m?.work).toBeUndefined();
  });
});

describe("成员分散在不同工作区时, 房间归哪个项目", () => {
  const room = (works: (string | undefined)[]) => ({
    id: "#r", title: "#r", isRoom: true, state: "waiting",
    people: works.map((w, i) => ({ pid: "p" + i, name: "b" + i, state: "waiting", ...(w === undefined ? {} : { work: w }) })),
    members: []
  }) as never;

  it("成员顺序一换就跳到另一个项目 —— 不行, 顺序跨重启会变", () => {
    // 原来取的是"第一个有真实工作区的成员". 而 pid 一变成员顺序就变,
    // 于是同一个房间在项目之间来回跳 —— 人刚记住的位置就没了.
    expect(projectOf(room(["/Users/x/AI/oa", "/Users/x/AI/finance"])))
      .toBe(projectOf(room(["/Users/x/AI/finance", "/Users/x/AI/oa"])));
  });

  it("按人数多的那个定", () => {
    expect(projectOf(room(["/Users/x/AI/oa", "/Users/x/AI/finance", "/Users/x/AI/oa"]))).toBe("oa");
  });

  it("票数相同按名字定 —— 谁赢不重要, 每次都一样才重要", () => {
    expect(projectOf(room(["/Users/x/AI/oa", "/Users/x/AI/finance"]))).toBe("finance");
  });

  it("匿名目录不投票 —— 那是'还没派活', 不是一个项目", () => {
    expect(projectOf(room(["/Users/x/.neox-os/work/ops", "/Users/x/AI/oa"]))).toBe("oa");
  });

  it("一个人都没派活就是没项目", () => {
    expect(projectOf(room(["/Users/x/.neox-os/work/ops", undefined]))).toBe("");
  });
});

/**
 * 组头要能点开项目卡, 所以每一堆得带上**它在磁盘上的哪儿**.
 *
 *   要整条路径不要短名字: 项目卡拿它去问 git, 而两个不同目录完全可能
 *   叫同一个名字(~/a/oa 和 ~/b/oa).
 */
describe("每一堆带上项目根", () => {
  const one = (name: string, project?: string, work?: string) => ({
    id: name, title: name, isRoom: false, state: "waiting",
    members: [], people: [{ pid: "p" + name, name, state: "waiting", ...(project === undefined ? {} : { project }), ...(work === undefined ? {} : { work }) }]
  }) as never;

  it("走分支隔离时认项目根, 不认各自的 worktree", () => {
    const groups = byProject([one("小登", "/AI/oa", "/wt/oa/小登"), one("小勤", "/AI/oa", "/wt/oa/小勤")]);
    expect(groups).toHaveLength(1);
    expect(groups[0]?.path).toBe("/AI/oa");
  });

  it("没派项目的那一堆不给路径 —— 它不是一个项目, 没有状态可看", () => {
    const groups = byProject([one("值守")]);
    expect(groups[0]?.name).toBe(OTHERS);
    expect(groups[0]?.path).toBeUndefined();
  });
});
