/**
 * markdown — 够用的那一点点.
 *
 *   做六件事: 粗体、行内代码、代码块、有序/无序列表、**表格**、**标题**.
 *
 *   ── 表格和标题是由消息数据决定的必要语法 ──
 *
 *   63 条有正文的回话里, **4 条含表格
 *   (6.3%)**, 2 条含标题(3.2%), 而引用、链接、分隔线**一次都没出现过**.
 *   表格漏出来的样子最难看 —— 一屏 |---|---|---| 的字面量, 而它恰恰
 *   用在最该看清楚的地方(逐条核对的期望/实际对照表).
 *
 *   所以补这两样, 别的不补: 没出现过的语法, 补了也只是代码.
 *   不引 markdown 库, 因为:
 *     · 库要把字符串变成 HTML, 而 HTML 要么 dangerouslySetInnerHTML
 *       (模型输出直接进 DOM, 一个 <img onerror> 就够了), 要么再套一层
 *       sanitizer —— 两条都比这三十行贵.
 *     · **只在行渲染完之后才用它**: 正在流式的那一行走的是直写
 *       textContent 那条路, 拆成多个节点下一次增量写会把结构冲掉.
 */

import { t } from "../i18n/index.js";
import type { ReactNode } from "react";

import { takeTable } from "./markdownTable.js";

/**
 * 图从哪儿取、点开怎么放大 —— 由调用方提供.
 *
 *	回复里写 ![](绝对路径) 就该**就地看到图**, 但字节在 OS 那一侧,
 *	渲染器自己够不着 /blob 的 token, 所以路径解析和取图凭证必须由调用方提供.
 */
export interface MarkdownMedia {
  /** 把本地绝对路径换成能取字节的 URL. null = 这个环境取不了 */
  src(path: string): string | null;
  onOpen?(url: string, alt: string): void;
}

export function renderMarkdown(text: string, mentions: readonly string[] = [], media?: MarkdownMedia): ReactNode {
  const blocks: ReactNode[] = [];
  const lines = text.split("\n");
  let index = 0;
  let key = 0;

  while (index < lines.length) {
    const line = lines[index] ?? "";

    // 代码块
    if (line.trimStart().startsWith("```")) {
      const body: string[] = [];
      index += 1;
      while (index < lines.length && !(lines[index] ?? "").trimStart().startsWith("```")) {
        body.push(lines[index] ?? "");
        index += 1;
      }
      index += 1; // 吃掉结尾的 ```
      blocks.push(<pre className="md__code" key={key++}>{body.join("\n")}</pre>);
      continue;
    }

    // 标题
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading !== null) {
      // 聊天气泡里不该出现整屏大的 h1 —— 层级只用来分轻重, 不用来撑字号
      blocks.push(
        <p className="md__h" data-level={heading[1]?.length ?? 1} key={key++}>
          {inline(heading[2] ?? "", mentions, media)}
        </p>
      );
      index += 1;
      continue;
    }

    // 表格: 一行 | 开头的表头 + 一行 |---|---| 的分隔线
    const table = takeTable(lines, index);
    if (table !== null) {
      blocks.push(
        // **横向滚动由外层管**: 一个五列的对照表在气泡里放不下,
        // 不给它自己的滚动容器的话, 整条消息会被撑宽, 旁边的头像
        // 和时间戳跟着错位
        <div className="md__tablewrap" key={key++}>
          <table className="md__table">
            <thead>
              <tr>{table.head.map((cell, i) => (
                <th key={i} style={alignOf(table.align[i])}>{inline(cell, mentions, media)}</th>
              ))}</tr>
            </thead>
            <tbody>{table.body.map((row, r) => (
              <tr key={r}>{row.map((cell, i) => (
                <td key={i} style={alignOf(table.align[i])}>{inline(cell, mentions, media)}</td>
              ))}</tr>
            ))}</tbody>
          </table>
        </div>
      );
      index = table.next;
      continue;
    }

    // 列表: 连续的 - / * / 1. 收成一段
    if (/^\s*([-*]|\d+[.)])\s+/.test(line)) {
      const ordered = /^\s*\d+[.)]\s+/.test(line);
      const items: ReactNode[] = [];
      while (index < lines.length && /^\s*([-*]|\d+[.)])\s+/.test(lines[index] ?? "")) {
        items.push(<li key={items.length}>{inline((lines[index] ?? "").replace(/^\s*([-*]|\d+[.)])\s+/, ""), mentions, media)}</li>);
        index += 1;
      }
      blocks.push(ordered
        ? <ol className="md__list" key={key++}>{items}</ol>
        : <ul className="md__list" key={key++}>{items}</ul>);
      continue;
    }

    // 段落: 到下一个空行为止
    const paragraph: string[] = [];
    while (index < lines.length && (lines[index] ?? "").trim() !== ""
      && !/^\s*([-*]|\d+[.)])\s+/.test(lines[index] ?? "")
      && !/^#{1,6}\s+/.test(lines[index] ?? "")
      && takeTable(lines, index) === null
      && !(lines[index] ?? "").trimStart().startsWith("```")) {
      paragraph.push(lines[index] ?? "");
      index += 1;
    }
    if (paragraph.length > 0) blocks.push(<p className="md__p" key={key++}>{inline(paragraph.join("\n"), mentions, media)}</p>);
    else index += 1; // 空行
  }
  return blocks.length === 1 ? blocks[0] : blocks;
}

/**
 * 行内: **粗**、`码`, 和 @点名.
 *
 *   ── @点名必须看得出来 ──
 *
 *   房间里 bot 之间互相点名是常事("@发版 这步是你的活"), 而它原来跟
 *   正文一个颜色 —— 用户扫过去根本注意不到自己或者别人被点了名,
 *   截图里就是这样: 一句话里的 @发版 埋在文字中间.
 *
 *   **按真实成员名匹配, 不按 @ 后面跟着什么猜**: 猜的话
 *   "邮箱 a@b.com"、"@2x 的图" 都会被点亮, 而点亮一个不存在的人
 *   比不点亮更糟 —— 它看起来像"这里真有个人".
 */
function inline(text: string, mentions: readonly string[] = [], media?: MarkdownMedia): ReactNode {
  const parts: ReactNode[] = [];
  const at = mentions.length === 0 ? "" :
    "|@(?:" + mentions.map(escapeForRegex).sort((a, b) => b.length - a.length).join("|") + ")";
  /**
   * 链接也要认: bot 说"地址 http://localhost:3001" 用户只能抄下来 ——
   * 真机原话: "都不能直接点击打开". 认两种: [文字](url) 和裸 URL.
   * 反引号排在前面, 所以 `code` 里的 URL 保持代码样式不变链接.
   */
  // 裸 URL 撞到中日韩文字或全角标点就停 —— 没编码过的 URL 里本来就没有它们,
  // 不停的话 "http://…3001，首页" 整段被吞进链接.
  // 图片(![alt](src))排在链接前面: ![ 的 [ 不能被当成链接的开头
  const pattern = new RegExp(
    "(\\*\\*[^*]+\\*\\*|`[^`]+`|!\\[[^\\]]*\\]\\([^\\s)]+\\)|\\[[^\\]]+\\]\\(https?://[^\\s)]+\\)|https?://[^\\s<>\"'\\u3000-\\u303f\\u4e00-\\u9fff\\uff00-\\uffef]+" + at + ")", "g");
  let cursor = 0;
  let key = 0;
  for (const match of text.matchAll(pattern)) {
    const at = match.index ?? 0;
    if (at > cursor) parts.push(text.slice(cursor, at));
    const token = match[0];
    // **加粗里面还要接着解析**: 匹配到 ** 就把整段吞掉的话, 里面的
    // `代码` 和 @点名 全变成字面量 —— 真机截图里就是这样, 同一条消息里
    // 别处的 `csv` 是代码块, 而"**绝不拿真实的 `ledger.json` 做验证**"
    // 里那一对反引号原样摆着.
    // (不会无限递归: 外层的 [^*]+ 保证里面没有第二对 **)
    if (token.startsWith("**")) { parts.push(<strong key={key++}>{inline(token.slice(2, -2), mentions, media)}</strong>); cursor = at + token.length; continue; }
    if (token.startsWith("@")) { parts.push(<span className="md__at" key={key++}>{token}</span>); cursor = at + token.length; continue; }
    if (token.startsWith("`")) { parts.push(<code className="md__inline" key={key++}>{token.slice(1, -1)}</code>); cursor = at + token.length; continue; }
    if (token.startsWith("![")) {
      /**
       * ![alt](src) —— **就地把图摆出来**, 让消息中的图片不必退化成路径文本.
       *
       *	http(s) 直接用; 本地绝对路径换成 /blob 的取图 URL(带 token,
       *	且那个口子只放图片字节过). 换不出来就原样当文字 —— 摆一个
       *	裂图比一行文本糟.
       */
      const close = token.indexOf("](");
      const alt = token.slice(2, close);
      const raw = token.slice(close + 2, -1).replace(/^file:\/\//, "");
      const url = /^https?:\/\//.test(raw) ? raw : raw.startsWith("/") ? media?.src(raw) ?? null : null;
      if (url === null) parts.push(token);
      else parts.push(
        <button className="md__imgbtn" key={key++} type="button" title={alt || t("点开看大的")}
          onClick={() => media?.onOpen?.(url, alt || t("图"))}>
          <img className="md__img" src={url} alt={alt} loading="lazy"
            onError={(event) => { event.currentTarget.closest("button")?.replaceWith(document.createTextNode(raw)); }} />
        </button>
      );
      cursor = at + token.length;
      continue;
    }
    if (token.startsWith("[")) {
      // [文字](url)
      const close = token.indexOf("](");
      const label = token.slice(1, close);
      const url = token.slice(close + 2, -1);
      parts.push(<a className="md__link" key={key++} href={url} target="_blank" rel="noreferrer">{label}</a>);
      cursor = at + token.length;
      continue;
    }
    // 裸 URL: 句尾标点是话的一部分, 不是地址的 —— "http://…3001，" 那个
    // 逗号进了链接就打不开了
    const tail = /[),.;!?，。；！？、]+$/.exec(token);
    const url = tail === null ? token : token.slice(0, -tail[0].length);
    parts.push(<a className="md__link" key={key++} href={url} target="_blank" rel="noreferrer">{url}</a>);
    if (tail !== null) parts.push(tail[0]);
    cursor = at + token.length;
  }
  if (cursor < text.length) parts.push(text.slice(cursor));
  return parts.length === 0 ? text : parts;
}

function alignOf(align: "left" | "center" | "right" | undefined): { textAlign: "left" | "center" | "right" } {
  return { textAlign: align ?? "left" };
}

/** 名字里可能有正则元字符 —— 不转义的话一个名字就能把整条正则搞坏 */
function escapeForRegex(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
