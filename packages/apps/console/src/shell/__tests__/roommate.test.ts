import { describe, expect, it } from "vitest";

import { ROOMMATE_HINT, roommateNote } from "../roommate.js";

const OA = "/Users/x/AI/oa";

describe("拉进来之前先说清他碰不碰得到这摊活", () => {
  it("在别处干活的要说 —— 拉进房间只改房间标签, 不动工作区", () => {
    // 真机: 把「值守」拉进 #oa, 它的工作区还是 ~/.neox-os/work/ops,
    // 人在屋里说得上话, 却看不到 OA 的任何文件.
    expect(roommateNote(OA, "/Users/x/.neox-os/work/ops")).toBe("在别处");
    // 解释在上面说一次 —— 每行都摊开一整句就是刷屏
    expect(ROOMMATE_HINT).toContain("碰不到这摊活的文件");
  });

  it("已经在一处就别啰嗦 —— 不该有的提示比没提示更吵", () => {
    expect(roommateNote(OA, OA)).toBeNull();
  });

  it("压根没固定工作区的也算不在一处", () => {
    expect(roommateNote(OA, undefined)).toBe("在别处");
  });

  it("这房间自己都没有共同工作区, 就没什么可对的", () => {
    expect(roommateNote(undefined, "/Users/x/别处")).toBeNull();
  });
});
