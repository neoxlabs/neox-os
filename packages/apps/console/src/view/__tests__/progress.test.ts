import { describe, expect, it } from "vitest";

import { progressPercent } from "../widgets.js";

describe("进度条画多长", () => {
  it("done/total 是最自然的写法, 必须认", () => {
    // 真机: 模型说"进度 5/8", 而卡上画着空条 + 0% —— 不报错, 只是画了个假数
    expect(progressPercent({ done: 5, total: 8 })).toBeCloseTo(62.5);
  });

  it("percent 和 ratio 照旧", () => {
    expect(progressPercent({ percent: 40 })).toBe(40);
    expect(progressPercent({ ratio: 0.25 })).toBe(25);
  });

  it("ratio 优先于 done —— 明说的比算出来的准", () => {
    expect(progressPercent({ ratio: 1, done: 1, total: 8 })).toBe(100);
  });

  it("total 是 0 不能除 —— 画出 NaN 比画错更难看", () => {
    expect(progressPercent({ done: 3, total: 0 })).toBe(0);
  });

  it("超出范围掐回去", () => {
    expect(progressPercent({ percent: 250 })).toBe(100);
    expect(progressPercent({ percent: -10 })).toBe(0);
  });

  it("给了个不是数的东西也别画出 NaN", () => {
    expect(progressPercent({ percent: "很快了" })).toBe(0);
  });
});
