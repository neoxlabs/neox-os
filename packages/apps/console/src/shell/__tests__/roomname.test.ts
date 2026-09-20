import { describe, expect, it } from "vitest";

import { roomLabel, roomLabels } from "../roomname.js";

describe("房间在界面上叫什么", () => {
  it("井号是给机器看的 —— 界面上不出现", () => {
    // 用户没输入过它, 也没地方学过它是什么意思
    expect(roomLabel("#oa")).toBe("oa");
    expect(roomLabel("#finance")).toBe("finance");
  });

  it("没有井号的原样 —— 用户自己起的名字不许被啃掉一个字", () => {
    expect(roomLabel("这次发布")).toBe("这次发布");
  });

  it("只剥开头那一个: 名字里本来就有井号的不算", () => {
    expect(roomLabel("#c#2")).toBe("c#2");
  });

  it("一列房间连着写", () => {
    expect(roomLabels(["#oa", "#finance"])).toBe("oa、finance");
    expect(roomLabels([])).toBe("");
  });
});
