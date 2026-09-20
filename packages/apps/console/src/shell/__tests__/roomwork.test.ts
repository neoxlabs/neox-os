import { describe, expect, it } from "vitest";

import { workForRoom } from "../roomwork.js";

describe("选了房间, 工作区跟着走", () => {
  it("留空时自动填成这个房间的工作区 —— 不然它在房间里却碰不到这摊活", () => {
    expect(workForRoom({ current: "", filled: "", roomWork: "/Users/x/AI/oa" }))
      .toEqual({ work: "/Users/x/AI/oa", filled: "/Users/x/AI/oa" });
  });

  it("用户自己敲过的路径不动 —— 那是他的决定", () => {
    expect(workForRoom({ current: "/Users/x/别处", filled: "", roomWork: "/Users/x/AI/oa" }))
      .toBeNull();
  });

  it("上次是我们填的, 换个房间就跟着换", () => {
    expect(workForRoom({ current: "/Users/x/AI/oa", filled: "/Users/x/AI/oa", roomWork: "/Users/x/AI/finance" }))
      .toEqual({ work: "/Users/x/AI/finance", filled: "/Users/x/AI/finance" });
  });

  it("改回「不放」就清掉我们填的那份", () => {
    expect(workForRoom({ current: "/Users/x/AI/oa", filled: "/Users/x/AI/oa", roomWork: undefined }))
      .toEqual({ work: "", filled: "" });
  });

  it("这房间没有共同工作区, 而用户自己填了 —— 还是不动他的", () => {
    expect(workForRoom({ current: "/Users/x/我的", filled: "", roomWork: undefined })).toBeNull();
  });

  it("已经一样就什么都不做 —— 别白白重渲染", () => {
    expect(workForRoom({ current: "/Users/x/AI/oa", filled: "/Users/x/AI/oa", roomWork: "/Users/x/AI/oa" }))
      .toBeNull();
  });
});
