/**
 * history — 把一个人的事件流翻成**一列能读的行**.
 *
 *   资料边栏里"说过的话"要的不是 payload 的 JSON, 是一句人话:
 *   什么时候、干了什么、说了什么. 翻译归界面这一侧 —— 内核只管
 *   把事件原样流出来, 不该为了好看而迁就界面(见 rows.ts 同一条判据).
 *
 *   **只翻要显示的那几条**: 一个人几百条事件里, budget/记账那类不占行,
 *   它们进日志不进这一列 —— 摊出来只会把真正发生的事挤下去.
 */

import { t, tt } from "../i18n/index.js";
import type { OsEvent, ProcessID } from "../os/events.js";
import type { HistoryLine, HistoryPage } from "./Profile.js";

export interface EventSource {
  /** 某个进程的事件. **稀疏数组**: 下标 == seq, 没到的位置是洞 */
  eventsOf(pid: ProcessID): readonly OsEvent[];
}

/**
 * lines 取这个人**最近的 limit 条**, 最新的在最前.
 *
 *   跨 pid: 一个 bot 重启过 N 次就有 N 个 pid, 而他还是一个人 ——
 *   历史散在这些 pid 上, 按时间合起来才是"他说过的话".
 */
export function historyLines(
  source: EventSource, pids: readonly string[], limit: number
): HistoryPage {
  const all: HistoryLine[] = [];
  for (const pid of pids) {
    for (const event of source.eventsOf(pid)) {
      // 稀疏数组里的洞: for...of 会把它们当 undefined 走一遍
      if (event === undefined) continue;
      const line = translate(event);
      if (line !== null) all.push(line);
    }
  }
  // 最新的在最前. at 相同(同一毫秒里连着发的)时按 seq 兜底 ——
  // 不兜的话同一轮里的几条顺序是随机的, 而"先说了什么"正是要看的
  all.sort((a, b) => (b.at - a.at) || b.key.localeCompare(a.key));
  // total 是**翻出来的条数**, 不是事件总数: 一个人几百条事件里大半是
  // 起停和记账, 那些不占行. 拿事件总数当标题会写着"共 393 条"却只列出
  // 八十条 —— 剩下的去哪了没人知道, 而那看起来就是丢了.
  return { lines: all.slice(0, limit), total: all.length };
}

/** 这条事件占不占一行 —— 占就翻成人话, 不占返回 null */
function translate(event: OsEvent): HistoryLine | null {
  const key = `${event.pid}#${event.seq}`;
  const body = (event.payload ?? {}) as Record<string, unknown>;
  const str = (name: string): string => {
    const value = body[name];
    return typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value);
  };
  const line = (tag: string, text: string, bad = false): HistoryLine | null =>
    text.trim() === "" ? null : { key, at: event.at, tag, text: squeeze(text), ...(bad ? { bad } : {}) };

  switch (event.kind) {
    case "proc.output": {
      // 会干活的 bot 吐的是 ReAct 的**阶段**, 不是文本块 —— 见 rows.ts
      const phase = str("phase");
      if (phase !== "") return fromPhase(phase, str, line);
      const channel = str("channel");
      if (channel === "think") return line(t("想"), str("text"));
      return line(t("说"), str("text"));
    }
    case "input.recv": return line(t("收到"), str("text"));
    case "user.correction": return line(t("你说"), str("text"));
    case "proc.state": {
      /**
       * **起停不占行**.
       *
       *   一个 bot 每被叫醒一次就是 created→running→waiting 三条,
       *   跑了一天就是几百条 —— 摊在"说过的话"里, 真正说的那几句
       *   会被它们淹没, 满屏 waiting 反而让人找不到对话内容.
       *   只有**挂了**要留: 那条是要找的东西.
       */
      const state = str("state");
      if (state !== "failed") return null;
      const why = str("reason");
      return line(t("挂了"), why === "" ? state : why, true);
    }
    case "proc.outcome": return line(t("这一轮"), str("summary") || str("outcome"));
    case "decide.requested": return line(t("等拍板"), str("title") || str("summary"));
    case "decide.resolved": return line(t("拍了板"), `${str("choice")} · ${str("title")}`.trim());
    case "cap.denied": return line(t("拦住了"), `${str("axis")} ${str("scope")}`, true);
    case "signal.in": return line(t("有人插话"), str("text"));
    case "wake.fired": return line(t("叫醒"), str("why") || str("reason"));
    case "interrupt.verdict": return line(t("插话"), str("verdict") || str("why"));
    // budget/cap.used/watch/collector 那些是记账和自检 —— 进日志, 不占一行
    default: return null;
  }
}

/** ReAct 的阶段 —— 判据跟 rows.ts 那份是同一套, 只是这里压成一行 */
function fromPhase(
  phase: string,
  str: (name: string) => string,
  line: (tag: string, text: string, bad?: boolean) => HistoryLine | null
): HistoryLine | null {
  switch (phase) {
    case "reply": case "done": return line(t("说"), str("text") || str("detail"));
    case "think": case "thought": return line(t("想"), str("text") || str("detail"));
    case "step": return line(t("在干"), str("detail") || str("text"));
    case "tool_ok": return line(t("用了工具"), `${str("tool")} ${str("detail")}`.trim());
    case "tool_err": return line(t("工具没成"), `${str("tool")} ${str("error") || str("detail")}`.trim(), true);
    case "turn_failed": case "stall": case "model_err": case "step_limit":
      return line(t("停下了"), str("detail") || str("error") || phase, true);
    default: return line(phase, str("detail") || str("text"));
  }
}

/**
 * 一行就是一行: 换行压成空格, 太长的截掉 —— 这一列是索引, 不是正文.
 *
 *   **插卡那一条要翻成人话**: show 出来的卡在事件里是一整坨 JSON,
 *   原样摆进来就是 `{"caption":"架构图","id":"image-o42zh…","src":…}` ——
 *   占三行, 一个字都读不出来. 人要知道的只是"他插了一张图".
 */
function squeeze(text: string): string {
  const flat = text.replace(/\s+/g, " ").trim();
  const card = asCard(flat);
  if (card !== null) return card;
  return flat.length <= 160 ? flat : `${flat.slice(0, 160)}…`;
}

/** 这坨字是不是一张卡的 JSON —— 是就说清是哪种卡, 不是返回 null */
function asCard(text: string): string | null {
  if (!text.startsWith("{")) return null;
  try {
    const body = JSON.parse(text) as Record<string, unknown>;
    const kind = typeof body["type"] === "string" ? body["type"] : "";
    if (kind === "") return null;
    const title = ["title", "caption", "label", "path"]
      .map((name) => body[name]).find((value) => typeof value === "string" && value !== "");
    return title === undefined ? tt("插了一张 {a} 卡", { a: kind }) : tt("插了一张 {a} 卡 · {b}", { a: kind, b: String(title) });
  } catch { return null; }
}
