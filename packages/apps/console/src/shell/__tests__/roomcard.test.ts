import { describe, expect, it } from "vitest";

import { roomFacts, roomPulse, type Face } from "../roomcard.js";

const who = (name: string, part: Partial<Face> = {}): Face => ({ name, state: "waiting", ...part });

describe("这个群在哪儿干活", () => {
  it("走分支隔离时各人一个 worktree —— 那不叫各干各的, 那正是说好的样子", () => {
    const facts = roomFacts([
      who("小登", { work: "/wt/oa/小登", project: "/AI/oa", branch: "neox/小登" }),
      who("接口", { work: "/wt/oa/接口", project: "/AI/oa", branch: "neox/接口" }),
    ]);
    expect(facts.work).toBe("/AI/oa");
    expect(facts.split).toBe(false);
    expect(facts.branches).toEqual([
      { name: "小登", branch: "neox/小登" },
      { name: "接口", branch: "neox/接口" },
    ]);
  });

  it("**真的各干各的要说出来**: 拉人时忘了给工作区, 他就在自己的空目录里", () => {
    // 这是真实会发生的毛病, 拿第一个人的路径糊弄过去正好把它藏了
    const facts = roomFacts([who("小登", { work: "/AI/oa" }), who("新来的", { work: "/tmp/空" })]);
    expect(facts.work).toBeUndefined();
    expect(facts.split).toBe(true);
  });

  it("一个人都没派工作区就是没有, 不是各干各的", () => {
    const facts = roomFacts([who("小登"), who("接口")]);
    expect(facts.work).toBeUndefined();
    expect(facts.split).toBe(false);
  });
});

describe("这屋里此刻什么情况", () => {
  it("在干活的、不在的、闲着的分开说 —— 侧栏那个点答不了这个", () => {
    const facts = roomFacts([
      who("小登", { state: "running" }), who("接口", { state: "waiting" }), who("走了的", { state: "exited" }),
    ]);
    expect(roomPulse(facts, 3)).toBe("1 个在干活 · 1 个不在了 · 1 个闲着");
  });

  it("没有的那几档不占字 —— 全闲着时就一句话", () => {
    expect(roomPulse(roomFacts([who("a"), who("b")]), 2)).toBe("2 个闲着");
  });
});
