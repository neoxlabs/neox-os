/**
 * envelope — 把模型偶尔吐出来的"信封"拆掉再显示.
 *
 *   ── 为什么渲染这侧也要做一遍 ──
 *
 *   agent 那边已经拆过一次(go/agent/model_llm.go 的 unwrapEnvelope), 但那是
 *   **落盘之前**. 账本只增不改 —— 在那次修复之前存下来的那些, 原文就是:
 *
 *       {"said":"复现 + 修正完成。…\n\n**本轮改了三处**…"}
 *
 *   于是用户每次翻到那一段, 看到的都是一屏 `\n` 字面量, 结尾还挂着 `"}`.
 *   真机截图里就是这样. 那段历史不会自己变好, 只有显示这一侧能收拾.
 *
 *   ── 为什么只拆"单键字符串" ──
 *
 *   多个字段说明它真的想表达结构(比如 bot 就是要给你看一段 JSON),
 *   拆了会把别的字段吞掉. 拆错比不拆糟.
 */

const KEYS = new Set(["said", "reply", "text", "answer", "message", "content"]);

export function unwrapSaid(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed.startsWith("{")) return raw;
  /**
   * **信封后面还能跟着正文**. 真机截图里的原样:
   *
   *	   {"said":"已把这句话原样转给 OA助手：「…」"}
   *
   *	   他说会再转给屋里另一个人去建 ping.txt。
   *
   *	判据是"整段以 } 结尾"的话, 后面多一段话这条就整个失效 —— 那串
   *	JSON 原样摆在用户眼前, 而下面那句话看着像另一个人说的.
   *	(agent 那侧同一个坑修过一次, 这边当时只抄了严格的那一半.)
   */
  const head = objectEnd(trimmed);
  if (head < 0) return raw;
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed.slice(0, head));
  } catch {
    return raw; // 解不开就原样 —— 宁可漏一条, 不能把正常内容切坏
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return raw;
  const fields = Object.entries(parsed as Record<string, unknown>);
  if (fields.length !== 1) return raw;
  const [key, value] = fields[0]!;
  if (!KEYS.has(key) || typeof value !== "string" || value.trim() === "") return raw;
  const rest = trimmed.slice(head).trim();
  // 拆信封不是删内容 —— 后面跟着的原样接回去
  return rest === "" ? value : `${value}\n\n${rest}`;
}

/**
 * 开头那个 JSON 对象在哪儿结束(下标, 不含). -1 = 它就没闭合.
 *
 *   得认字符串: 正文里带 } 是常事("改完了}"), 只数括号会在那儿断错.
 */
function objectEnd(text: string): number {
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i]!;
    if (inString) {
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inString = false;
      continue;
    }
    if (c === '"') inString = true;
    else if (c === "{") depth += 1;
    else if (c === "}") {
      depth -= 1;
      if (depth === 0) return i + 1;
    }
  }
  return -1;
}
