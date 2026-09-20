import { t } from "../i18n/index.js";
/**
 * presence — 头像上那个点表达的状态.
 *
 *   ── 状态映射需要独立维护 ──
 *
 *   这个点是整个界面上信息密度最高的一个像素: 扫过一列 bot,
 *   就能知道"谁能接收指令、谁在忙、谁需要处理".
 *
 *   只有两种颜色时, 判据会把内核里的 waiting 误解为
 *   "停在 Recv 上等下一句话"(bot 闲着的常态), 界面却把它映成橙色的
 *   --warn 并写着"等待决定" —— 一屋子闲着的 bot 就全是警告色,
 *   那个点永远亮着, 也就不再有任何信息量.
 *
 *   ── 五档, 每一档都对应一个不同的操作 ──
 *
 *	在线   进程活着, 停在 Recv 上   → 现在就能使唤它
 *	忙碌   正在跑一轮              → 说话它也听得见(插话), 但不会马上回
 *	待决   有一条决策尚未处理        → 它卡在那儿, 只有人能让它继续
 *	出错   进程失败了              → 需要查看发生了什么
 *	不在   进程退出了              → 说话前要先把它拉起来
 *
 *   永远给一个点, 包括"不在"那种(灰的). 不给点等于"不知道",
 *   而不知道和离线是两件事.
 */

export type Presence = "online" | "busy" | "needs-you" | "failed" | "offline";

export interface PresenceView {
  readonly kind: Presence;
  /** 一句话, 鼠标停上去和侧栏没有摘要时都用它 */
  readonly label: string;
}

/**
 * presenceOf 把内核的进程状态翻成"人看得懂的在不在".
 *
 *   pending = 有一条 decide.requested 还没等到 resolved, 而且它的主人
 *   还活着(死进程上的卡按了也没人接, 见 Console.tsx 的 anyPending).
 */
export function presenceOf(state: string, pending: boolean): PresenceView {
  if (state === "failed") return { kind: "failed", label: t("出错了") };
  if (state === "exited") return { kind: "offline", label: t("不在") };
  if (state === "running") return { kind: "busy", label: t("在干活") };
  if (state === "waiting" || state === "created") {
    return pending
      ? { kind: "needs-you", label: t("等你拍板") }
      : { kind: "online", label: t("在线") };
  }
  // 认不得的状态**不当成在线** —— 说它在线而其实不在, 用户会对着一个
  // 死进程说话; 反过来最多是多点一下
  return { kind: "offline", label: t("不在") };
}
