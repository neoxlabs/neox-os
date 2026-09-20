import { tt } from "../i18n/index.js";
/**
 * stamp — 什么时候该在两行之间插一个时间.
 *
 *   ── 为什么需要 ──
 *
 *   一问一答挨着时, 时间通常是多余的. 但这个账本跨天跨会话:
 *   若完全不标时间, 翻上去那一段是昨天的还是上周的, 就无法分辨.
 *
 *   即使是一条两分钟后才响的提醒, 紧贴在前一句回复下面也会看着像
 *   同一口气说的. 逐行标注能揭示这种间隔, 但也会增加下面所述的噪声.
 *
 *   ── 为什么不是每行都标 ──
 *
 *   每行都标就是每行多一坨灰字, 而绝大多数行紧挨着上一行, 那个时间
 *   一个字的信息都没有. 只在**隔开了**的时候标 —— 隔开本身才是要说的事.
 */

/** 隔这么久才值得标一次. 一问一答之间不该有时间 */
const GAP_MS = 30 * 60 * 1000;

/**
 * 这一行上面要不要插时间, 插什么.
 *
 *   previousAt 为 undefined = 这是第一行, 总是标(那段历史是哪天的,
 *   得有个交代).
 */
export function stampBetween(previousAt: number | undefined, at: number, now: number): string | null {
  if (previousAt !== undefined && at - previousAt < GAP_MS && sameDay(previousAt, at)) return null;
  return label(at, now);
}

function sameDay(a: number, b: number): boolean {
  const x = new Date(a);
  const y = new Date(b);
  return x.getFullYear() === y.getFullYear() && x.getMonth() === y.getMonth() && x.getDate() === y.getDate();
}

function label(at: number, now: number): string {
  const d = new Date(at);
  const clock = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (sameDay(at, now)) return clock;
  const yesterday = new Date(now);
  yesterday.setDate(yesterday.getDate() - 1);
  if (sameDay(at, yesterday.getTime())) return tt("昨天 {a}", { a: clock });
  const year = d.getFullYear() === new Date(now).getFullYear() ? "" : tt("{a}年", { a: d.getFullYear() });
  return tt("{a}{b}月{c}日 {d}", { a: year, b: d.getMonth() + 1, c: d.getDate(), d: clock });
}

function pad(n: number): string { return n < 10 ? `0${n}` : `${n}`; }
