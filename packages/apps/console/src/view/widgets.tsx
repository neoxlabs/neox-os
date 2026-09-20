/**
 * widgets — 即时 UI.
 *
 *   bot 在对话里吐一段 JSON, 长出一张可交互的卡片, 用户点完回一条输入.
 *   这是"比 IM 更强"的那部分: 消息不只是文本, 可以是一个待办的动作.
 *
 *   **必须是注册表驱动**. 加一种卡片 = 注册一个渲染器, 不改渲染层一行.
 *   直接在渲染层写 if/else 或者正则匹配 kind, 容易出现
 *   "新 kind 被吃掉、静默塌成裸 JSON"；注册表让新增类型有明确的渲染入口.
 *
 *   不认识的 type **不许静默丢弃**: 显式显示"这个客户端还不认识它",
 *   因为对用户来说, 一张没显示的卡片和一次失约是一回事.
 */

import { t, tt } from "../i18n/index.js";
import type { ReactNode } from "react";
import type { WidgetSpec } from "./rows.js";
import { diffLines } from "./diffline.js";

export interface WidgetContext {
  /** 用户对这张卡片做出了回应 —— 回一条输入给进程 */
  respond(widgetId: string, answer: string): void;
  /** 这张卡片是否已经被回应过 */
  answerOf(widgetId: string): string | undefined;
  /**
   * 发起这张卡的进程还在不在.
   *
   *   **不在就不该给按钮**. 重启之后旧卡片还留在历史里, 而等它的那个
   *   进程早没了 —— 点下去 OS 找不到 waiter, 回一个 ok:false,
   *   界面上什么都不会发生. 一个按不动的按钮比没有按钮糟得多.
   */
  stale?: boolean;
  /**
   * 图从哪儿取 —— bot 吐一张图卡时给的是**它工作区里的路径**,
   * 而浏览器打不开本地路径(file:// 在这里也走不通). 由 OS 那侧代取.
   */
  shotSrc?: { byPath(path: string): string } | undefined;
  /** 点开看大的 */
  onOpenImage?(src: string, alt: string): void;
}

export type WidgetRenderer = (spec: WidgetSpec, ctx: WidgetContext) => ReactNode;

/**
 * 已经拍过板的卡 —— **收成一行**.
 *
 *   一次审批本来要占四行. 事情办完之后那四行就是历史噪音,
 *   而一个跑几小时的任务会问几十次 —— 不收起来的话对话被冲没了.
 *   收起来但不删掉: 事后要能翻出"这一步是谁放的行".
 */
function Settled({ title, choice }: { title: unknown; choice: string }) {
  const ok = choice === "yes";
  return (
    <div className={`settled${ok ? "" : " settled--no"}`}>
      <span className="settled__mark">{ok ? "✓" : "✕"}</span>
      <span className="settled__text">{titleText(title)}</span>
    </div>
  );
}

/** 已经没人在等了 —— 说清楚, 不给按钮 */
function Expired({ title }: { title: unknown }) {
  return (
    <div className="w w--expired">
      <Head text={title} />
      <div className="w__why">{t("这次请求已经过期：发起它的进程不在了。要它接着做就再说一句。")}</div>
    </div>
  );
}

/**
 * 卡片标题.
 *
 *   **没有标题就没有这一行** —— 早先写的是 `String(spec.title)`,
 *   而 String(undefined) 是 "undefined" 这五个字, 于是一张没标题的
 *   表格卡头上顶着一个大写的 undefined. 这类"照实转字符串"的写法,
 *   在缺字段时不会报错, 只会把内部状态直接印到用户脸上.
 */
function titleText(text: unknown): string {
  if (typeof text === "string") return text.trim();
  return text === undefined || text === null ? "" : String(text);
}

function Head({ text }: { text: unknown }) {
  const title = titleText(text);
  if (title === "") return null;
  return <div className="w__head">{title}</div>;
}

import { OpenCard } from "./opencard.js";

const REGISTRY = new Map<string, WidgetRenderer>();

export function registerWidget(type: string, renderer: WidgetRenderer): void {
  REGISTRY.set(type, renderer);
}

export function renderWidget(spec: WidgetSpec | undefined, ctx: WidgetContext): ReactNode {
  if (spec === undefined) return null;
  const renderer = REGISTRY.get(spec.type);
  /**
   * **不认识的种类不丢弃, 照字段画一张通用卡** —— 见 opencard.tsx.
   *
   *   卡片的种类是开的, 而客户端是另一个发版节奏. 原来这里只画一句
   *   "还不认识 xxx 卡片": 模型说的东西整个没了, 而它收到的是"已展示",
   *   于是接着说"链接见上". 用户面前是一句道歉加一片空白.
   */
  if (renderer === undefined) return <OpenCard spec={spec} ctx={ctx} />;
  return renderer(spec, ctx);
}

/* ── 内置卡片 ─────────────────────────────────────────────── */

registerWidget("approve", (spec, ctx) => {
  const answered = ctx.answerOf(spec.id);
  if (answered !== undefined) return <Settled title={spec.title} choice={answered} />;
  if (ctx.stale === true) return <Expired title={spec.title} />;
  return (
    <div className={`w w--approve${answered ? " w--done" : ""}`}>
      <Head text={spec.title} />
      {spec.detail ? <div className="w__why">{String(spec.detail)}</div> : null}
      {answered
        ? <div className="w__answered">{answered === "yes" ? t("已批准") : t("已拒绝")}</div>
        : <div className="w__actions">
            {/* "yes" 是 OS 那边 grantFromDecision 的判据, 不能改成别的词 */}
            <button className="w__btn w__btn--primary" onClick={() => ctx.respond(spec.id, "yes")}>{t("批准")}</button>
            <button className="w__btn" onClick={() => ctx.respond(spec.id, "no")}>{t("拒绝")}</button>
          </div>}
    </div>
  );
});

registerWidget("choice", (spec, ctx) => {
  const answered = ctx.answerOf(spec.id);
  const heading = String(spec.title ?? spec.question ?? t("选一个"));
  if (answered !== undefined) {
    const picked = (Array.isArray(spec.options) ? spec.options as { id?: string; label?: string }[] : [])
      .find((option) => option.id === answered);
    return <Settled title={`${heading.split("\n")[0]}`} choice={answered === "yes" || picked?.label === "允许" ? "yes" : answered} />;
  }
  if (ctx.stale === true) return <Expired title={heading} />;
  /**
   * **选项的值在 `id` 字段, 不是 `value`**.
   *
   *   abi.PresentOption 是 {id, label, detail, destructive}. 早先这里读
   *   `option.value` —— 永远是 undefined, 于是点「允许」送出去的是空字符串,
   *   而 agent 判的是 `choice != "yes"` ⇒ **每一次批准都被当成拒绝**.
   *
   *   界面上完全看不出来: 卡片有按钮、点得动、没有报错,
   *   只是那件事永远做不成. 事件日志里才看得见 choice:"".
   */
  const options = (Array.isArray(spec.options) ? spec.options as { id?: string; value?: string; label: string }[] : [])
    .map((option) => ({ id: option.id ?? option.value ?? "", label: option.label }))
    .filter((option) => option.id !== "");
  return (
    <div className={`w w--choice${answered ? " w--done" : ""}`}>
      <Head text={spec.title ?? spec.question ?? t("选一个")} />
      {/**
        * **理由必须显示出来**.
        *
        *   这一行原来根本不存在(只有 approve 卡渲染 detail), 于是
        *   每一张 choice 审批卡都只剩一个标题和两个按钮:
        *
        *	"允许连 pypi.org 吗?"        —— 为什么要连? 看不到
        *	"拉「测仔」进来一起干吗?"     —— 他干什么? 为什么要多一个人? 看不到
        *
        *   而模型写那句话的**全部理由**就是让用户据此决定. 界面把它丢了,
        *   等于让人在没有依据的情况下点批准 —— 那不是审批, 那是走过场.
        */}
      {spec.detail ? <div className="w__why">{String(spec.detail)}</div> : null}
      <div className="w__actions w__actions--wrap">
        {options.map((option) => (
          <button
            key={option.id}
            className={`w__btn${answered === option.id ? " w__btn--picked" : ""}${option.id === "yes" ? " w__btn--primary" : ""}`}
            disabled={answered !== undefined}
            onClick={() => ctx.respond(spec.id, option.id)}
          >{option.label}</button>
        ))}
      </div>
    </div>
  );
});

/**
 * 进度条画多长 —— **单拎出来是为了钉得住**.
 *
 *   埋在渲染器里的话, 只有把整张卡画出来才验得到; 而这条判据出过错:
 *   模型给 done/total, 卡上写 0%.
 */
export function progressPercent(spec: Readonly<Record<string, unknown>>): number {
  const total = Number(spec.total ?? 0);
  const raw = spec.ratio !== undefined ? Number(spec.ratio) * 100
    : spec.percent !== undefined ? Number(spec.percent)
    : spec.done !== undefined && total > 0 ? (Number(spec.done) / total) * 100
    : 0;
  return Math.max(0, Math.min(100, Number.isFinite(raw) ? raw : 0));
}

registerWidget("progress", (spec) => {
  /**
   * **done/total 也认**.
   *
   *	"5 之 8"是表达进度最自然的写法, 模型十有八九这么给. 只认
   *	ratio/percent 的话, 它说着"进度 5/8", 卡上画着**空条 + 0%** ——
   *	不报错, 只是画了个假数；进度值缺少兼容解析时就会出现这种显示.
   *
   *	(产出侧也会拦住"一个进度值都没给"的卡：
   *	一头不让发假的, 一头把真的读懂.)
   */
  const total = Number(spec.total ?? 0);
  const percent = progressPercent(spec);
  return (
    <div className="w w--progress">
      <div className="w__line">{String(spec.title ?? spec.label ?? t("进行中"))}</div>
      <div className="w__bar"><span style={{ width: `${percent}%` }} /></div>
      {/* 给了 done/total 就把原数也摆出来 —— "5/8" 比 "63%" 好对账 */}
      <div className="w__why">
        {spec.done !== undefined && total > 0 ? `${Number(spec.done)}/${total} · ` : ""}
        {Math.round(percent)}%{spec.detail ? ` · ${String(spec.detail)}` : ""}
      </div>
    </div>
  );
});

registerWidget("diff", (spec) => (
  <div className="w w--diff">
    <Head text={spec.title ?? spec.path} />
    {/* 加的减的要分开看得见 —— 那是这张卡存在的唯一理由, 见 diffline.ts */}
    <pre className="w__diff">
      {diffLines(String(spec.diff ?? spec.patch ?? "")).map((line, index) => (
        <span className={`dl dl--${line.kind}`} key={index}>{line.text || " "}</span>
      ))}
    </pre>
  </div>
));

registerWidget("doc", (spec) => (
  <div className="w w--doc">
    <Head text={spec.title} />
    <div className="w__why">{String(spec.body ?? "")}</div>
  </div>
));

/** 这一列是不是数字 —— 是就右对齐并用等宽, 数位才对得齐 */
function numeric(rows: string[][], column: number): boolean {
  const cells = rows.map((row) => row[column]).filter((cell): cell is string => typeof cell === "string");
  return cells.length > 0 && cells.every((cell) => /^[\d.,+\-%$¥]+\s*\w{0,3}$/.test(cell.trim()));
}

registerWidget("table", (spec) => {
  const columns = Array.isArray(spec.columns) ? spec.columns as string[] : [];
  const rows = Array.isArray(spec.rows) ? spec.rows as string[][] : [];
  return (
    <div className="w w--table">
      <Head text={spec.title} />
      <table className="w__grid">
        <thead>
          <tr>{columns.map((c, i) => <th key={c} className={numeric(rows, i) ? "num" : undefined}>{c}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={index}>
              {row.map((cell, i) => <td key={i} className={numeric(rows, i) ? "num" : undefined}>{cell}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
});

registerWidget("form", (spec, ctx) => {
  const answered = ctx.answerOf(spec.id);
  if (answered === undefined && ctx.stale === true) return <Expired title={spec.title} />;
  const fields = Array.isArray(spec.fields) ? spec.fields as { name: string; label?: string; placeholder?: string }[] : [];
  return (
    <form
      className={`w w--form${answered ? " w--done" : ""}`}
      onSubmit={(event) => {
        event.preventDefault();
        const data = new FormData(event.currentTarget);
        ctx.respond(spec.id, JSON.stringify(Object.fromEntries(data.entries())));
      }}
    >
      <Head text={spec.title} />
      {fields.map((field) => (
        <label className="w__field" key={field.name}>
          <span>{field.label ?? field.name}</span>
          <input name={field.name} placeholder={field.placeholder ?? ""} disabled={answered !== undefined} />
        </label>
      ))}
      {answered ? <div className="w__answered">{t("已提交")}</div> : <div className="w__actions"><button className="w__btn w__btn--primary" type="submit">{t("提交")}</button></div>}
    </form>
  );
});

/* ── 多模态: bot 回复里可以直接放的东西 ──────────────────────
 *
 *   这些跟工具卡不同层: 工具是**过程**(缺省折叠), 这些是**内容**
 *   —— 它就是在拿图/表/代码回答你, 折起来等于没回答.
 *
 *   OS 侧一行不用改: bot 走 proc.output channel="ui" 吐一段 JSON 就行.
 */

/**
 * 图卡 —— bot 拿一张图回答你.
 *
 *   src 有两种写法, **path 才是常用的那种**: bot 画完一张图存在自己的
 *   工作区里, 它知道的就是那条路径. data: URL 要它把几百 KB 的 base64
 *   写进一次输出里, 又慢又占上下文.
 */
registerWidget("image", (spec, ctx) => {
  const path = String(spec.path ?? "");
  const src = path !== "" && ctx.shotSrc !== undefined ? ctx.shotSrc.byPath(path) : String(spec.src ?? "");
  const alt = String(spec.alt ?? spec.caption ?? path);
  return (
    <figure className="w w--media">
      {/* 远程图走不了(CSP) —— 拿不到就把它收掉, 不留个破图标 */}
      <img className="w__img" src={src} alt={alt} style={{ cursor: "zoom-in" }}
        onClick={() => ctx.onOpenImage?.(src, alt)}
        onError={(event) => { event.currentTarget.style.display = "none"; }} />
      {spec.caption ? <figcaption className="w__cap">{String(spec.caption)}</figcaption> : null}
    </figure>
  );
});

registerWidget("video", (spec, ctx) => (
  <figure className="w w--media">
    <video className="w__img" src={mediaSrc(spec, ctx)} controls preload="metadata" />
    {spec.caption ? <figcaption className="w__cap">{String(spec.caption)}</figcaption> : null}
  </figure>
));

/** 声音: 一条播放条就够 —— 它没有"看"的部分, 摆个大方框是白占地方 */
registerWidget("audio", (spec, ctx) => (
  <figure className="w w--audio">
    {spec.title ? <div className="w__head">{String(spec.title)}</div> : null}
    <audio className="w__audio" src={mediaSrc(spec, ctx)} controls preload="metadata" />
    {spec.caption ? <figcaption className="w__cap">{String(spec.caption)}</figcaption> : null}
  </figure>
));

/**
 * 一组图 —— **一张一张摆是不对的**.
 *
 *   五张截图各占一张卡, 对话就被冲掉了; 而它们本来就是一件事的几个面.
 *   缩略图铺开, 点开看大的.
 */
registerWidget("gallery", (spec, ctx) => {
  const items = Array.isArray(spec.images) ? spec.images : [];
  return (
    <div className="w w--gallery">
      {spec.title ? <div className="w__head">{String(spec.title)}</div> : null}
      <div className="gal">
        {items.slice(0, 24).map((one, index) => {
          const item = typeof one === "string" ? { path: one } : one as Record<string, unknown>;
          const src = mediaSrc(item, ctx);
          const alt = String(item.alt ?? item.caption ?? tt("第 {a} 张", { a: index + 1 }));
          return (
            <button key={index} className="gal__one" type="button" title={alt}
              onClick={() => ctx.onOpenImage?.(src, alt)}>
              <img src={src} alt={alt} loading="lazy"
                onError={(event) => { event.currentTarget.closest("button")?.remove(); }} />
            </button>
          );
        })}
      </div>
    </div>
  );
});

/**
 * 网页 —— bot 甩一条链接过来.
 *
 *   **链接要能点, 而且开在系统浏览器里**: 客户端里没有地址栏, 在里面
 *   开一个网页等于把人困在一个退不出去的页面上(见 electron/main.cjs
 *   的 setWindowOpenHandler).
 *
 *   标题/摘要/站点是 bot 自己抓来填的(它有 fetch). 只给一条 url 也画得
 *   出来 —— 那时这张卡就是一条能点的链接, 不假装自己有摘要.
 */
registerWidget("link", (spec) => {
  const url = String(spec.url ?? "");
  const title = String(spec.title ?? url);
  const site = String(spec.site ?? hostOf(url));
  return (
    <a className="w w--link" href={url} target="_blank" rel="noreferrer" title={url}>
      {spec.image ? <img className="link__shot" src={String(spec.image)} alt=""
        onError={(event) => { event.currentTarget.style.display = "none"; }} /> : null}
      <div className="link__body">
        <div className="link__title">{title}</div>
        {spec.desc ? <div className="link__desc">{String(spec.desc)}</div> : null}
        <div className="link__site">
          <svg viewBox="0 0 16 16" width="11" height="11" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
            <circle cx="8" cy="8" r="6" /><path d="M2.4 6.4h11.2M2.4 9.6h11.2M8 2a12 12 0 0 0 0 12 12 12 0 0 0 0-12Z" />
          </svg>
          {site}
        </div>
      </div>
    </a>
  );
});

/**
 * 文件 —— 它做出来一份东西.
 *
 *   **不假装能打开它**: 客户端打不开一个任意文件(也不该能). 这张卡说
 *   清楚三件事: 叫什么、在哪儿、多大. 路径**可以整条复制**, 那是人拿去
 *   接着用的唯一形式.
 */
registerWidget("file", (spec) => {
  const path = String(spec.path ?? "");
  const name = String(spec.name ?? path.split("/").pop() ?? t("文件"));
  return (
    <div className="w w--file">
      <svg className="file__icon" viewBox="0 0 16 16" width="20" height="20" fill="none"
        stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" aria-hidden="true">
        <path d="M4 1.8h5l3 3v9.4H4z" /><path d="M9 1.8v3h3" />
      </svg>
      <div className="file__body">
        <div className="file__name">{name}</div>
        <div className="file__path">{path}</div>
      </div>
      {spec.size ? <span className="file__size">{String(spec.size)}</span> : null}
    </div>
  );
});

/**
 * mediaSrc 一张卡里的媒体从哪儿取.
 *
 *   **path 优先**: bot 手上有的通常是它工作区里的一条路径, 而浏览器
 *   打不开本地路径 —— 由 OS 那侧代取(见 shell/blobs.ts).
 *   给了 src(data: URL 或 http) 就原样用.
 */
function mediaSrc(spec: Record<string, unknown>, ctx: WidgetContext): string {
  const path = String(spec.path ?? "");
  if (path !== "" && ctx.shotSrc !== undefined) return ctx.shotSrc.byPath(path);
  return String(spec.src ?? path);
}

/** 域名 —— 拿不出来就算了, 不猜 */
function hostOf(url: string): string {
  try { return new URL(url).host; } catch { return ""; }
}

/**
 * 代码卡**只有一层面**.
 *
 *   早先是卡里再套一个深色框装代码 —— 两层圆角嵌在一起, 那是最显廉价的
 *   一种做法: 同一件事(这是一段代码)被两层容器各说了一遍。
 *   现在语言名只是左上角一行小字, 代码直接躺在卡面上。
 */
registerWidget("code", (spec) => {
  const tag = typeof spec.title === "string" && spec.title !== ""
    ? spec.title
    : typeof spec.lang === "string" ? spec.lang : "";
  return (
    <div className="w w--code">
      {tag !== "" ? <div className="code__tag">{tag}</div> : null}
      <pre className="code__body">{String(spec.code ?? spec.body ?? "")}</pre>
    </div>
  );
});

/**
 * 天气不是"一段带标题的内容", 是一块**自带天空的磁贴**.
 *
 *   所以它不吃通用卡面(w--bare): 底是按天气状况给的一层渐变,
 *   字永远是浅色 —— 这张卡有自己的小主题, 不跟着明暗切换走,
 *   就像手机上那块天气小组件.
 */
const SKY: { match: RegExp; sky: string; icon: "sun" | "cloud" | "rain" | "snow" }[] = [
  { match: /雪/, sky: "snow", icon: "snow" },
  { match: /雨|雷/, sky: "rain", icon: "rain" },
  { match: /阴|雾|霾/, sky: "grey", icon: "cloud" },
  { match: /多云|云/, sky: "cloudy", icon: "cloud" },
  { match: /晴|./, sky: "clear", icon: "sun" },
];

function WeatherIcon({ kind }: { kind: "sun" | "cloud" | "rain" | "snow" }) {
  return (
    <svg className="wx__icon" viewBox="0 0 24 24" aria-hidden="true">
      {kind === "sun" ? <circle cx="12" cy="11" r="5.2" /> : null}
      {kind !== "sun" ? (
        <>
          <circle cx="16.2" cy="8.2" r="3.2" opacity=".7" />
          <path d="M7 16.4h9.1a3.3 3.3 0 0 0 .2-6.6 4.9 4.9 0 0 0-9.3 1.1A2.9 2.9 0 0 0 7 16.4Z" />
        </>
      ) : null}
      {kind === "rain" || kind === "snow"
        ? <path className="wx__drops" d="M9.4 18.6l-.8 2.1M13 18.6l-.8 2.1M16.6 18.6l-.8 2.1" />
        : null}
    </svg>
  );
}

registerWidget("weather", (spec) => {
  const summary = String(spec.summary ?? "");
  const look = SKY.find((entry) => entry.match.test(summary)) ?? SKY[SKY.length - 1]!;
  const days = Array.isArray(spec.days) ? spec.days as { day: string; high: number; low: number }[] : [];
  const today = days[0];
  const range = spec.high !== undefined && spec.low !== undefined
    ? `${String(spec.high)}°/${String(spec.low)}°`
    : today !== undefined ? `${today.high}°/${today.low}°` : "";
  return (
    <div className={`w w--bare wx wx--${look.sky}`}>
      <div className="wx__top">
        <span className="wx__place">{String(spec.place ?? "")}</span>
        <WeatherIcon kind={look.icon} />
      </div>
      <div className="wx__temp">{String(spec.temp ?? "—")}<span className="wx__deg">°</span></div>
      <div className="wx__foot">
        {summary ? <span>{summary}</span> : null}
        {spec.air !== undefined ? <span>{String(spec.air)}</span> : null}
        {range ? <span className="wx__range">{range}</span> : null}
      </div>
      {days.length > 1 ? (
        <div className="wx__days">
          {days.slice(0, 5).map((entry) => (
            <span className="wx__day" key={entry.day}>
              <small>{entry.day}</small>
              <b>{entry.high}°</b>
              <small>{entry.low}°</small>
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
});
