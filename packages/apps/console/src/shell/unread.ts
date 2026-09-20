/**
 * unread — 未读.
 *
 *   判据是**时间**, 不是水位.
 *
 *   如果使用"这个会话的事件总数涨了没有"作为判据, 补齐历史时水位会一直涨,
 *   而那些是**旧话**不是新话, 页面一加载就会全部亮起.
 *   刷新一次全部标未读, 未读这个功能就等于没有了.
 *
 *   现在: **最后一句话的时间 > 我上次看这个会话的时间**.
 *   补齐历史带的是旧时间戳, 不会误报; 而且时间戳落 localStorage,
 *   刷新之后还认得出哪些是真没看过的.
 */

import type { EventStore } from "../os/store.js";
import type { OsEvent } from "../os/events.js";
import type { Party } from "./parties.js";

const KEY = "neoxos.seenAt";

export type SeenAt = ReadonlyMap<string, number>;

export function loadSeenAt(): Map<string, number> {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw === null) return new Map();
    return new Map(Object.entries(JSON.parse(raw) as Record<string, number>));
  } catch { return new Map(); }
}

function persist(seen: SeenAt): void {
  try { localStorage.setItem(KEY, JSON.stringify(Object.fromEntries(seen))); }
  catch { /* 无痕窗口写不了, 不影响这次会话内的判断 */ }
}

/**
 * 会话里最后一句**话**的时间.
 *
 *   只认人看得见的那几种 —— 工具调用、能力记账、状态变更都不算,
 *   否则一个在后台干活的 bot 会一直闪红点, 而它根本没跟你说话.
 */
export function lastSpokeAt(party: Party, store: EventStore): number {
  let latest = 0;
  for (const member of party.members) {
    const events = store.eventsOf(member.pid);
    for (let index = events.length - 1; index >= 0 && index > events.length - 200; index -= 1) {
      const event = events[index];
      if (event === undefined || !isSpeech(event)) continue;
      if (event.at > latest) latest = event.at;
      break;
    }
  }
  return latest;
}

function isSpeech(event: OsEvent): boolean {
  if (event.kind === "decide.requested") return true; // 待处理的决策也需要当前人的注意
  if (event.kind !== "proc.output") return false;
  const body = event.payload as { channel?: string; phase?: string };
  if (typeof body.phase === "string") return body.phase === "reply" || body.phase === "done";
  return (body.channel ?? "say") === "say";
}

export function unreadOf(party: Party, store: EventStore, seen: SeenAt): boolean {
  const at = seen.get(party.id);
  if (at === undefined) return false; // 头一回见到这个会话, 不算未读
  return lastSpokeAt(party, store) > at;
}

export function markSeen(party: Party, store: EventStore, seen: SeenAt): Map<string, number> {
  const next = new Map(seen);
  next.set(party.id, Math.max(Date.now(), lastSpokeAt(party, store)));
  persist(next);
  return next;
}

/** 头一回见到的会话按"刚看过"记 —— 补齐历史不该炸出一屏红点 */
export function seedSeen(parties: readonly Party[], store: EventStore, seen: SeenAt): Map<string, number> {
  let changed = false;
  const next = new Map(seen);
  for (const party of parties) {
    if (next.has(party.id)) continue;
    next.set(party.id, Math.max(Date.now(), lastSpokeAt(party, store)));
    changed = true;
  }
  if (changed) persist(next);
  return next;
}
