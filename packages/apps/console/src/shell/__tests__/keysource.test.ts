import { describe, expect, it } from "vitest";

import { keyHint } from "../keysource.js";

describe("那把 key 现在在哪儿", () => {
  it("环境变量那把要说清**重启就没了** —— 真机上撞过一次", () => {
    const hint = keyHint("env", true);
    expect(hint).toContain("重启即失效");
    expect(hint).not.toContain("已存");
  });

  it("存了盘的说清它是明文 —— 一句话, 不写地址不写解释", () => {
    expect(keyHint("file", true)).toBe("存在本机 · 明文");
  });

  it("一句话就要答完'重启后还在不在' —— 超过十来个字就没人读了", () => {
    for (const hint of [keyHint("env", true), keyHint("file", true), keyHint(undefined, false)]) {
      expect(hint.length).toBeLessThanOrEqual(14);
    }
  });

  it("一把都没有就说没有", () => {
    expect(keyHint(undefined, false)).toBe("未设置");
  });

  it("旧后端没有这个字段时按 hasKey 兜底 —— 不能因为不认识就说没有", () => {
    expect(keyHint(undefined, true)).toBe("已存，留空不改");
  });
});
