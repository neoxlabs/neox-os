import { describe, expect, it } from "vitest";

import { EXPIRED, outcomeOf } from "../outcome.js";

describe("outcomeOf", () => {
  it("过期不许说成拒绝", () => {
    // 原来是二分(yes 之外一律"已拒绝"), 于是 OS 判的过期被显示成
    // "已拒绝" —— 把"问这件事的进程已经不在了"说成"人拒绝了他".
    expect(outcomeOf(EXPIRED).label).toBe("已过期");
    expect(outcomeOf(EXPIRED).label).not.toBe("已拒绝");
  });

  it("批准和拒绝照旧", () => {
    expect(outcomeOf("yes")).toEqual({ label: "已批准", failed: false });
    expect(outcomeOf("no").label).toBe("已拒绝");
  });

  it("没见过的结局按没做成算, 不当成批准", () => {
    // 判错的代价不对称: 把未知当批准, 界面会显示"已批准"而其实没有
    expect(outcomeOf("什么鬼").failed).toBe(true);
  });
});
