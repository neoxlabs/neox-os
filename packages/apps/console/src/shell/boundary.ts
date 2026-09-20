import { t } from "../i18n/index.js";
/**
 * boundary — 这台机器上, 边界到底由谁挡.
 *
 *   ── 为什么要分这么细 ──
 *
 *   用户看这一句是为了决定**敢不敢把一个没验过的 bot 放进真实项目目录**.
 *   说错的后果不是"少一层保护", 是他以为有一层而其实没有.
 *
 *   只分两档(有人强制 / 靠自觉)不足以说明保护范围.
 *   "landlock / netns / cgroup" 对应 confined 模式; console 的
 *   dev 模式由 macOS 沙箱(sandbox-exec)限制写入, 规则是
 *   `(allow default)(deny file-write*)` 加一份白名单 —— **只管写, 不管出网**.
 *
 *   因此提示同时区分强制机制与覆盖面, 不能把写入限制说成网络隔离,
 *   否则用户会以为 bot 用 run 起的命令连不出去.
 *
 *   注意这几句是**纯文本**: 设置页的 hint 不走 markdown, 写了 ** 就是
 *   一串字面星号. 该重的地方靠用词, 不靠记号.
 */
export interface BoundaryNote {
  readonly title: string;
  readonly hint: string;
  readonly note: string;
  /** true = 有人强制. 界面据此决定这句话是灰的还是暗的 */
  readonly enforced: boolean;
}

export function boundaryNote(health: { enforced?: boolean; mode?: string } | null): BoundaryNote {
  if (health?.enforced !== true) {
    return {
      title: t("这台机器上边界没人强制"),
      hint: t("文件工具拦得住工作区外的路径，但 bot 用 run 起的命令不受约束——派它去真实项目目录之前先想清楚"),
      note: t("靠自觉"),
      enforced: false
    };
  }
  if (health.mode === "confined") {
    return {
      title: t("边界在内核，不在这里"),
      hint: t("bot 跑的命令继承同一套 landlock / netns / cgroup"),
      note: t("改这个只改问不问"),
      enforced: true
    };
  }
  return {
    title: t("写入边界由系统沙箱挡着"),
    hint: t("bot 跑的命令只能写进它的工作区（macOS 沙箱，子孙进程一起受约束）。出网不在这层挡——那由能力集和审批管"),
    note: t("只挡写"),
    enforced: true
  };
}
