import { describe, expect, it } from "vitest";

import { presenceOf } from "../presence.js";

describe("presenceOf", () => {
  it("闲着的 bot 是在线, 不是等你拍板", () => {
    // 内核里的 waiting 只是"停在 Recv 上等下一句话", 那是常态.
    // 一律映成橙色 --warn + "需要处理", 会让一屋子闲着的 bot
    // 全顶着警告色, 那个点永远亮着, 也就不再有任何信息量.
    expect(presenceOf("waiting", false)).toEqual({ kind: "online", label: "在线" });
  });

  it("真有没拍板的决策才是等你", () => {
    expect(presenceOf("waiting", true).kind).toBe("needs-you");
  });

  it("在跑就是忙碌", () => {
    expect(presenceOf("running", false).kind).toBe("busy");
  });

  it("退出和失败要分开 —— 一个是干完了, 一个是得看一眼", () => {
    expect(presenceOf("exited", false).kind).toBe("offline");
    expect(presenceOf("failed", false).kind).toBe("failed");
  });

  it("认不得的状态不当成在线", () => {
    // 说它在线而其实不在, 用户会对着一个死进程说话;
    // 反过来最多是多点一下
    expect(presenceOf("什么鬼", false).kind).toBe("offline");
  });
});
