/**
 * roomwork — 选了房间之后, "在哪儿干活"该填什么.
 *
 *   ── 为什么要自动填 ──
 *
 *   同一个房间的人**必须在同一个工作区**. recruit 那条路早就想明白了:
 *   "拉人是为了一起干这摊活, 不是各干各的. 分开目录的话他连你写的代码
 *   都改不了."
 *
 *   而手工新建这条路没学到: 选了 #oa、工作区留空, OS 就分给它一个自己的
 *   空目录 —— 它在房间里, 却碰不到这摊活的一个字节. 而"留空"恰恰是
 *   最省事、最容易发生的选择.
 *
 *   ── 为什么不直接覆盖 ──
 *
 *   用户自己敲进去的路径是**他的决定**, 不能因为他后来点了个房间就被
 *   悄悄改掉. 只在"这一格还是我们自己填的那份"时才跟着变.
 */
export function workForRoom(args: {
  /** 输入框里现在是什么 */
  readonly current: string;
  /** 上一次**我们自动填进去**的是什么. 空 = 还没自动填过 */
  readonly filled: string;
  /** 这个房间的人在哪儿干活. undefined = 这房间没有共同工作区 */
  readonly roomWork: string | undefined;
}): { readonly work: string; readonly filled: string } | null {
  const { current, filled, roomWork } = args;
  // 用户自己敲过了 —— 那是他的决定, 不动
  if (current.trim() !== "" && current !== filled) return null;
  const next = roomWork ?? "";
  if (next === current) return null;
  return { work: next, filled: next };
}
