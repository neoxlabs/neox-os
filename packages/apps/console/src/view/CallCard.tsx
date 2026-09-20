/**
 * CallCard — 一条工具调用展开之后的样子.
 *
 *   ── 三层折叠, 每一层都要有理由 ──
 *
 *	 一行     "用了 3 个工具 · 2 次授权"   缺省. 人回头找的是那句回复,
 *	                                       不是第 37 次授权
 *	 一条一行  write_file  a.txt          点开组之后
 *	 详情卡    真正的 diff / 终端输出       再点那一条
 *
 *   为什么不一上来就摆卡片: 一个跑几小时的任务会调几十次工具,
 *   每次一张卡的话对话被冲没了 —— 而卡片的价值在"我想看这一次"的时候,
 *   不在"它又干了一件事"的时候.
 */

import { t, tt } from "../i18n/index.js";
import type { CallKind, ToolCall } from "./rows.js";
import type { Shots } from "../shell/blobs.js";

export function CallCard({ call, shotSrc, onOpenImage }: {
  call: ToolCall;
  shotSrc?: Shots | undefined;
  onOpenImage?(src: string, alt: string): void;
}) {
  const body = (call.result ?? call.arg ?? "").trim();
  if (call.kind === "image" && shotSrc !== undefined) {
    return <SawCard call={call} body={body} shotSrc={shotSrc} {...(onOpenImage === undefined ? {} : { onOpenImage })} />;
  }
  if (body === "") return null;
  switch (call.kind ?? "text") {
    case "run": return <RunCard call={call} body={body} />;
    case "write": return <WriteCard call={call} body={body} />;
    default: return <pre className="cc cc--text">{body}</pre>;
  }
}

/**
 * 它看了一张图 —— **把那张图画出来**.
 *
 *	view_image 拿回来的是一段转述(视觉模型看图, 写一段文字). 光看那段
 *	文字, 人判断不了最要紧的一件事: **它看的是不是我说的那张**.
 *	把图摆在转述旁边, 这个问题一眼就有答案.
 */
function SawCard({ call, body, shotSrc, onOpenImage }: {
  call: ToolCall; body: string; shotSrc: Shots; onOpenImage?(src: string, alt: string): void;
}) {
  const path = call.arg.trim();
  const src = shotSrc.byPath(path);
  return (
    <div className="cc cc--saw">
      <button className="cc__shot" type="button" onClick={() => onOpenImage?.(src, path)} title={t("点开看大的")}>
        {/* 取不到就把这块收掉, 不留一个破图标 —— 图没了不该盖过那段转述 */}
        <img src={src} alt={path} onError={(event) => { event.currentTarget.closest("button")?.remove(); }} />
      </button>
      <div className="cc__sawtext">
        <code className="cc__cmd">{path}</code>
        {body === "" ? null : <pre className="cc__out">{body}</pre>}
      </div>
    </div>
  );
}

/** 跑命令: 退出码单独拎出来 —— 那是人第一眼要看的东西 */
function RunCard({ call, body }: { call: ToolCall; body: string }) {
  const exit = /\[退出码\s*(-?\d+)/.exec(body);
  const output = body.replace(/^\[退出码[^\]]*\]\s*/, "");
  const ok = exit === null ? !call.failed : exit[1] === "0";
  return (
    <div className="cc cc--run">
      <div className="cc__head">
        <code className="cc__cmd">{call.arg}</code>
        <span className={`cc__exit${ok ? "" : " cc__exit--bad"}`}>{exit === null ? (ok ? t("成功") : t("失败")) : tt("退出码 {a}", { a: exit[1] })}</span>
      </div>
      {output.length > 0 ? <pre className="cc__out">{output}</pre> : null}
    </div>
  );
}

/** 写文件: 有 diff 就画 diff, 没有就说清写了什么 */
function WriteCard({ call, body }: { call: ToolCall; body: string }) {
  const lines = body.split("\n");
  const isDiff = lines.some((line) => line.startsWith("+") || line.startsWith("-"));
  return (
    <div className="cc cc--write">
      <div className="cc__head"><code className="cc__cmd">{call.arg}</code></div>
      {isDiff
        ? <pre className="cc__diff">{lines.map((line, index) => (
            <span key={index} className={diffClass(line)}>{line}{"\n"}</span>
          ))}</pre>
        : <pre className="cc__out">{body}</pre>}
    </div>
  );
}

function diffClass(line: string): string {
  if (line.startsWith("+")) return "cc__add";
  if (line.startsWith("-")) return "cc__del";
  return "";
}

export function kindLabel(kind: CallKind | undefined): string {
  switch (kind) {
    case "write": return t("写");
    case "read": return t("读");
    case "run": return t("跑");
    case "search": return t("找");
    case "image": return t("看");
    default: return "";
  }
}
