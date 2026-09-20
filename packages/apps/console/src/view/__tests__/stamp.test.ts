import { describe, expect, it } from "vitest";

import { stampBetween } from "../stamp.js";

const at = (s: string) => new Date(s).getTime();
const NOW = at("2026-08-25T14:00:00");

describe("什么时候插一个时间", () => {
  it("一问一答挨着就不插 —— 那个时间一个字的信息都没有", () => {
    expect(stampBetween(at("2026-08-25T13:59:00"), at("2026-08-25T13:59:20"), NOW)).toBeNull();
  });

  it("隔了半小时以上要插", () => {
    expect(stampBetween(at("2026-08-25T12:00:00"), at("2026-08-25T13:00:00"), NOW)).toBe("13:00");
  });

  it("跨天了就得插, 哪怕只隔了一分钟 —— 换了一天是要说的事", () => {
    expect(stampBetween(at("2026-08-24T23:59:30"), at("2026-08-25T00:00:10"), NOW)).toBe("00:00");
  });

  it("第一行总是标 —— 那段历史是哪天的得有个交代", () => {
    expect(stampBetween(undefined, at("2026-08-25T09:05:00"), NOW)).toBe("09:05");
  });

  it("昨天说人话", () => {
    expect(stampBetween(undefined, at("2026-08-24T09:05:00"), NOW)).toBe("昨天 09:05");
  });

  it("更早的带上日期", () => {
    expect(stampBetween(undefined, at("2026-08-20T09:05:00"), NOW)).toBe("8月20日 09:05");
  });

  it("跨年的把年份也带上 —— 不带的话去年的和今年的长得一样", () => {
    expect(stampBetween(undefined, at("2025-12-31T23:05:00"), NOW)).toBe("2025年12月31日 23:05");
  });
});
