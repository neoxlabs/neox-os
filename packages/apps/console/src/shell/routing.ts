import { t } from "../i18n/index.js";
/**
 * routing — 一句话在房间里投给谁.
 *
 *   ── 为什么不能"没点名就投给所有人" ──
 *
 *   广播能避免总投给固定的第一个人, 让"文案在吗"由不合适的成员回答.
 *   但代价是**每多一个 bot, 一句话就多
 *   一次推理**: 三个人的房间说一句烧三轮, 一百个 bot 的房间烧一百轮.
 *   而且屏幕上三个人抢着回同一件事 —— 人在群里说话本来就不是这样的.
 *
 *   ── 现在的规矩 ──
 *
 *	@某人        只投给他 —— 你点名了, 别人不必被打扰
 *	@所有人      全屋 —— **爆炸只在你明说的时候发生**
 *	没点名        有群主投给**群主** —— 群主是开这个房间的领头, 分工归它管,
 *	             默认由它接话
 *	没群主        投给**最近说话的那个人** —— 接着刚才的话头
 *	没人说过话    投给第一个 —— 房间刚建好时总得有人接
 */

/** 明说要全屋的几种写法 */
const EVERYONE = [t("@所有人"), t("@全体"), "@all", "@All", "@ALL"];

export interface Addressee {
  readonly name: string;
}

export function pickTargets<T extends Addressee>(args: {
  readonly isRoom: boolean;
  /** 房间里可以收话的人, 顺序稳定 */
  readonly pool: readonly T[];
  readonly text: string;
  /** 最近说话的那个人的名字. 没人说过就是 undefined */
  readonly lastSpeaker?: string | undefined;
  /** 群主. 在场时没点名的话默认归它接 */
  readonly owner?: string | undefined;
}): readonly T[] {
  const { isRoom, pool, text, lastSpeaker, owner } = args;
  if (pool.length === 0) return [];
  if (!isRoom) return [pool[0]!];

  if (EVERYONE.some((word) => text.includes(word))) return pool;

  const mentioned = pool.filter((m) => text.includes(`@${m.name}`));
  if (mentioned.length > 0) return mentioned;

  // 群主在场就归群主 —— 它是开房的领头, 分工由它派;
  // 群主不在(被删了/没记录)才退回"接着刚才的话头"
  const boss = pool.find((m) => m.name === owner);
  if (boss !== undefined) return [boss];

  const last = pool.find((m) => m.name === lastSpeaker);
  return [last ?? pool[0]!];
}

/** 这句话点了谁的名 —— 界面据此高亮 */
export function mentionsIn(text: string, names: readonly string[]): readonly string[] {
  return names.filter((name) => text.includes(`@${name}`));
}
