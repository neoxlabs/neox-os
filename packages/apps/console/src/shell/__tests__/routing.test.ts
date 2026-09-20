import { describe, expect, it } from "vitest";

import { pickTargets } from "../routing.js";

const pool = [{ name: "发版" }, { name: "回归" }, { name: "文案" }];

describe("pickTargets", () => {
  it("没点名不再投给所有人", () => {
    // 上一版的规矩: 没点名 = 说给所有人听. 代价是每多一个 bot,
    // 你说一句就多一次推理 —— 一百个 bot 的房间烧一百轮.
    const got = pickTargets({ isRoom: true, pool, text: "这个怎么样", lastSpeaker: "回归" });
    expect(got).toHaveLength(1);
    expect(got[0]?.name).toBe("回归");
  });

  it("点了名只投给他", () => {
    const got = pickTargets({ isRoom: true, pool, text: "@文案 你来写", lastSpeaker: "回归" });
    expect(got.map((m) => m.name)).toEqual(["文案"]);
  });

  it("点几个就投几个", () => {
    const got = pickTargets({ isRoom: true, pool, text: "@发版 @回归 对一下", lastSpeaker: "文案" });
    expect(got.map((m) => m.name)).toEqual(["发版", "回归"]);
  });

  it("要全屋必须明说 —— 爆炸只在你要的时候发生", () => {
    expect(pickTargets({ isRoom: true, pool, text: "@所有人 停一下", lastSpeaker: "回归" }))
      .toHaveLength(3);
  });

  it("还没人说过话时投给第一个 —— 房间刚建好总得有人接", () => {
    const got = pickTargets({ isRoom: true, pool, text: "在吗" });
    expect(got.map((m) => m.name)).toEqual(["发版"]);
  });

  it("最近说话的人已经不在了, 退回第一个而不是谁都不投", () => {
    const got = pickTargets({ isRoom: true, pool, text: "接着说", lastSpeaker: "已经走了的人" });
    expect(got).toHaveLength(1);
  });

  it("单聊不受影响", () => {
    expect(pickTargets({ isRoom: false, pool: [{ name: "小记" }], text: "在吗" }).map((m) => m.name))
      .toEqual(["小记"]);
  });
});
