import { t } from "../i18n/index.js";
/**
 * outcome — 一条决策最后怎么了.
 *
 *   **单独一个文件是为了能测**: 这里只有三行, 但它决定了事后翻账本的人
 *   会得出什么结论, 而它一旦裹在渲染里就只能靠肉眼看.
 */

/** OS 判过期时写进账本的那个词. 跟 go 那边的 DecisionExpired 是同一个值 */
export const EXPIRED = "expired";

export interface Outcome {
  /** 一句话说清结局 */
  readonly label: string;
  /** 算不算"没做成" —— 界面据此上色 */
  readonly failed: boolean;
}

/**
 * outcomeOf 把 choice 翻成人话.
 *
 *   ── 过期必须是**自己一档** ──
 *
 *   原来是二分: yes 就是"已批准", 其余一律"已拒绝". 于是 OS 判的过期
 *   被显示成"已拒绝" —— 把"问这件事的进程已经不在了, 没人拍过板"
 *   说成"人拒绝了他". 事后翻账本的人会据此得出完全错的结论,
 *   而账本里明明写着 by=OS、choice=expired.
 */
export function outcomeOf(choice: string): Outcome {
  switch (choice) {
    case "yes":
      return { label: t("已批准"), failed: false };
    case EXPIRED:
      // 不写"失败": 没人做错什么, 只是那个进程不在了
      return { label: t("已过期"), failed: true };
    default:
      return { label: t("已拒绝"), failed: true };
  }
}
