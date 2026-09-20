import { describe, expect, it } from "vitest";

import { dominantWork } from "../workspace.js";

const at = (...works: (string | undefined)[]) =>
  works.map((w) => (w === undefined ? {} : { work: w }));

describe("一个房间在哪儿干活", () => {
  it("人数多的那个说了算", () => {
    expect(dominantWork(at("/Users/x/AI/oa", "/Users/x/AI/finance", "/Users/x/AI/oa")))
      .toBe("/Users/x/AI/oa");
  });

  it("票数相同也要**每次都一样** —— 成员顺序跨重启会变", () => {
    const a = dominantWork(at("/Users/x/AI/oa", "/Users/x/AI/finance"));
    const b = dominantWork(at("/Users/x/AI/finance", "/Users/x/AI/oa"));
    expect(a).toBe(b);
  });

  it("匿名目录不投票 —— 一人一个, 从来不是大家一起干的那摊活", () => {
    expect(dominantWork(at("/Users/x/.neox-os/work/ops", "/Users/x/AI/oa")))
      .toBe("/Users/x/AI/oa");
  });

  it("全是匿名目录就是没有共同工作区", () => {
    expect(dominantWork(at("/Users/x/.neox-os/work/ops", "/Users/x/.neox-os/work/qa")))
      .toBeUndefined();
  });

  it("一个人都没派活就是没有", () => {
    expect(dominantWork(at(undefined, ""))).toBeUndefined();
  });

  it("末尾的斜杠不该算成另一个工作区", () => {
    expect(dominantWork(at("/Users/x/AI/oa/", "/Users/x/AI/oa"))).toBe("/Users/x/AI/oa");
  });
});
