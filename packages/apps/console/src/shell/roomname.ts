/**
 * roomname — 房间在界面上叫什么.
 *
 *   内核里房间的身份是一个 thread 字符串, 而它现在长这样: `#oa`、`#finance`
 *   (见 go 侧的 roomNameFor: 取工作区目录最后一段, 前面缀一个井号).
 *
 *   **那个井号是给机器看的, 不是给人看的**. 用户从没输入过它, 也没地方
 *   学过它是什么意思 —— 界面上凭空多出来一个符号, 只会让人问"这是什么".
 *   "这是个房间不是一个人"这件事该由**头像和图标**去说: 房间的头像本来
 *   就是几张脸叠在一起, 名字边上再给一个小图标就够了, 一个字都不用写.
 *
 *   剥掉只发生在**显示**这一层: 传回内核的(点名、move、房间上下文)
 *   永远是原样的 thread —— 界面自作主张改过的名字, 内核那边一个都对不上.
 */

/** 房间名给人看的样子: 去掉开头那个井号 */
export function roomLabel(thread: string): string {
  return thread.startsWith("#") ? thread.slice(1) : thread;
}

/** 一列房间名连着写 —— "在的房间"那一行用 */
export function roomLabels(threads: readonly string[]): string {
  return threads.map(roomLabel).join("、");
}
