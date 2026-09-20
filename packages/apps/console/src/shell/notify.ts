/**
 * notify — 系统通知.
 *
 *   只推两种:
 *     · **有一条决策等处理** —— 进程停在那儿了, 不推等于让它一直等
 *     · 不在前台时的新消息, 每个会话最多 30 秒一条
 *
 *   不推的: 当前正在看的会话, 以及所有系统行(读了什么文件/内核挡下).
 *   把这些也推出去, 三分钟内就会把通知权限关掉, 于是**真正要紧的那条
 *   也送不到了** —— 通知的预算是用户的耐心, 不是技术上限.
 */

const COOLDOWN_MS = 30_000;
const lastAt = new Map<string, number>();

export function canNotify(): boolean {
  return typeof Notification !== "undefined" && Notification.permission === "granted";
}

export async function askPermission(): Promise<void> {
  if (typeof Notification === "undefined" || Notification.permission !== "default") return;
  try { await Notification.requestPermission(); } catch { /* 用户拒了就算了, 不重复问 */ }
}

export function notify(args: { key: string; title: string; body: string; urgent: boolean; onClick(): void }): void {
  if (!canNotify()) return;
  const now = Date.now();
  if (!args.urgent) {
    const previous = lastAt.get(args.key) ?? 0;
    if (now - previous < COOLDOWN_MS) return;
  }
  lastAt.set(args.key, now);
  const handle = new Notification(args.title, { body: args.body, silent: !args.urgent });
  handle.onclick = () => { window.focus(); args.onClick(); };
}
