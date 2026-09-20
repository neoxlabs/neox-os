import { t } from "../i18n/index.js";
/**
 * mentions — @ 能点到谁.
 *
 *   ── 为什么要把"所有人"列出来 ──
 *
 *   投递规则里一直有全屋这一档(routing.ts 的 EVERYONE), 但 @ 的补全
 *   只列成员名 —— 于是"怎么让全屋都动起来"这件事**只有读源码才知道**.
 *   一个存在但没人找得到的功能等于不存在.
 *
 *   ── 为什么排在最后 ──
 *
 *   补全列表第一项是默认高亮的, 打个 @ 再回车就选中它. 全屋广播是
 *   这里唯一会让花费翻倍的动作 —— 它不该是手滑就能碰到的那一个.
 *   routing.ts 里那句话同理: **爆炸只在你明说的时候发生**.
 */
export const EVERYONE_NAME = t("所有人");

export function mentionChoices(people: readonly string[], isRoom: boolean): readonly string[] {
  if (!isRoom) return []; // 一对一没有"点名"这回事, 对面只有一个人
  return [...people, EVERYONE_NAME];
}
