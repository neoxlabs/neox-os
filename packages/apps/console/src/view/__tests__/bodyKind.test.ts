import { describe, expect, it } from "vitest";

import { bodyKind } from "../Timeline.js";

const row = (kind: string, open = false) => ({ kind, open }) as never;

/**
 * 哪种行按什么画 —— 这条判据以前埋在组件里, 只有真机看得到.
 * 上一轮改完 alert 那条, 没有任何测试拦着它被改回去.
 */
describe("正文按什么画", () => {
  it("告警卡走 markdown —— 里面那句「该怎么办」带着强调", () => {
    expect(bodyKind(row("alert"))).toBe("markdown");
  });

  it("bot 说完的话走 markdown", () => {
    expect(bodyKind(row("said"))).toBe("markdown");
  });

  it("还在流的时候按纯文本 —— 半个 ** 会被当成加粗", () => {
    expect(bodyKind(row("said", true))).toBe("plain");
  });

  it("用户自己那条只点亮 @ —— 他打的字不该被当成 markdown 改样子", () => {
    expect(bodyKind(row("heard"))).toBe("mentions");
  });

  it("其余的原样 —— 工具行/提示行没有正文可解析", () => {
    for (const kind of ["tools", "thought", "did", "note", "wake", "joined", "outcome"]) {
      expect(bodyKind(row(kind))).toBe("plain");
    }
  });
});
