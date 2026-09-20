import { t, tt } from "../i18n/index.js";
/**
 * botname — 随机给 bot 起个名.
 *
 *   起名对大部分场景没有信息量 —— 需要的是"来个人干活",
 *   不是给孩子上户口. 名字可以随时在资料卡里改.
 *
 *   池子是单字: "小X" 两个字念得顺, 跟内置的(研究/值守/构建)和
 *   场景测试的(小甲/小乙)都不撞形.
 */

const POOL = [
  t("小岚"), t("小川"), t("小泽"), t("小枫"), t("小叶"), t("小山"), t("小星"), t("小野"),
  t("小禾"), t("小舟"), t("小石"), t("小雨"), t("小青"), t("小竹"), t("小云"), t("小田"),
  t("小谷"), t("小峰"), t("小泉"), t("小林"), t("小雪"), t("小风"), t("小海"), t("小桥")
] as const;

/** 挑一个没被用过的. 全被占了就编号兜底 —— 兜底也不能撞名 */
export function randomBotName(taken: ReadonlySet<string>): string {
  const free = POOL.filter((name) => !taken.has(name));
  if (free.length > 0) return free[Math.floor(Math.random() * free.length)]!;
  for (let n = 2; ; n += 1) {
    const name = tt("小{a}号", { a: n });
    if (!taken.has(name)) return name;
  }
}
