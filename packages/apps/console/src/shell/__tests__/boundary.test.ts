import { describe, expect, it } from "vitest";

import { boundaryNote } from "../boundary.js";

describe("边界到底由谁挡", () => {
  it("dev + 有沙箱: 说沙箱, 不说 landlock —— 这台机器上根本没有那套东西", () => {
    const n = boundaryNote({ enforced: true, mode: "dev" });
    expect(n.hint).not.toContain("landlock");
    expect(n.hint).toContain("沙箱");
  });

  it("dev + 有沙箱: 必须说清**只挡写** —— 出网照旧出得去", () => {
    // 规则是 (allow default)(deny file-write*) 加白名单.
    // 不说的话用户会以为 bot 用 run 起的命令连不出去.
    const n = boundaryNote({ enforced: true, mode: "dev" });
    expect(n.hint).toContain("出网不在这层");
    // 设置页的 hint 是纯文本 —— 写 ** 就是一串字面星号
    expect(n.hint).not.toContain("**");
    expect(n.note).toBe("只挡写");
  });

  it("confined 才说内核那一套 —— 那时候它是真的", () => {
    const n = boundaryNote({ enforced: true, mode: "confined" });
    expect(n.hint).toContain("landlock");
  });

  it("没人强制就照实说靠自觉", () => {
    const n = boundaryNote({ enforced: false, mode: "dev" });
    expect(n.enforced).toBe(false);
    expect(n.note).toBe("靠自觉");
  });

  it("健康信息还没拉到, 按最坏的说 —— 不能默认它是安全的", () => {
    expect(boundaryNote(null).enforced).toBe(false);
    expect(boundaryNote({}).enforced).toBe(false);
  });
});
