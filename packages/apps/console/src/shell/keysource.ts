import { t } from "../i18n/index.js";
/**
 * keysource — 那把 key 现在到底在哪儿.
 *
 *   ── 为什么不能只看"有没有" ──
 *
 *   hasKey 只能说明当前有 key, 不能说明它已保存. 环境变量里的 key
 *   **不落盘**, 生命周期限于当前进程; 自动抄到磁盘上会把临时凭据
 *   变成持久凭据, 持久化必须由用户选择.
 *
 *   若只据 hasKey 提示"已经存着一把", 用户会误以为重启后仍可使用;
 *   重启控制台时不再传入环境变量, key 就不可用, 推理也随之中断.
 *
 *   说清楚它是临时的, 用户才有机会选 —— 要么每次带着环境变量起,
 *   要么在这儿填一把长期存着.
 */
export function keyHint(keyFrom: string | undefined, hasKey: boolean): string {
  // 一行小字只答一个问题: **重启之后它还在不在**. 其余都是废话.
  if (keyFrom === "env") return t("环境变量 · 重启即失效");
  if (keyFrom === "file") return t("存在本机 · 明文");
  if (hasKey) return t("已存，留空不改");
  return t("未设置");
}
