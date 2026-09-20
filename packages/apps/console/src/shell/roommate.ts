import { t } from "../i18n/index.js";
/**
 * roommate — 把一个人拉进房间之前, 他跟这摊活对不对得上.
 *
 *   ── 为什么要说这一句 ──
 *
 *   拉人进房间只改**房间标签**, 不动工作区(moveBot). 于是把一个在别处
 *   干活的 bot 拉进来, 它人在屋里、说得上话, 却**碰不到这摊活的一个文件**.
 *
 *   recruit 那条路早就想明白了: "同一个房间、同一个工作区 —— 拉人是为了
 *   一起干这摊活, 不是各干各的. 分开目录的话他连你写的代码都改不了."
 *   新建 bot 那条路后来也补上了(roomwork.ts). 只剩这条还是哑的.
 *
 *   ── 为什么只说不改 ──
 *
 *   自动把他的工作区改成房间的, 等于替他放弃原来那摊活 —— 那是**用户的
 *   决定**, 不是我们的. 说清后果就够: 他要真想一起干, 进来之后在资料卡
 *   上"换个地方"一下就行.
 *
 *   ── 为什么行上只给一个短签 ──
 *
 *   十一个候选里七个都不在这摊活的工作区, 每行都摊开一整句就是七遍一样的
 *   话 —— 那不是说清楚, 那是刷屏. 行上标两个字, 解释在上面说一次.
 */

/** 上面那句解释, 只在**真有人不在一处**的时候露出来 */
export const ROOMMATE_HINT = t("标了「在别处」的, 进来也碰不到这摊活的文件");

/** 行上那个短签. null = 他跟这摊活在一处, 什么都不用标 */
export function roommateNote(roomWork: string | undefined, botWork: string | undefined): string | null {
  if (roomWork === undefined || roomWork === "") return null; // 这房间自己都没有共同工作区
  if (botWork === roomWork) return null;                      // 已经在一处, 不用说
  return t("在别处");
}
