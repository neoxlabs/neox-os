import type { ReactNode } from "react";
import { Glyph, glyphOf } from "./glyphs.js";
import type { WidgetContext } from "./widgets.js";
import type { WidgetSpec } from "./rows.js";

/**
 * opencard —— **卡片种类是开的, 而每一种都得像样**.
 *
 * ── 两件事一起做, 少一件都不成立 ──
 *
 *	① 认不出的种类**内容一个字都不能丢**. 原来遇上不认识的种类只画一句
 *	   "还不认识 xxx 卡片", 而模型收到的是"已展示", 于是接着说"如上图" ——
 *	   用户面前是一句道歉加一片空白.
 *
 *	② 但只做①的话, 出来的是一摞 `键: 值` —— **那是调试输出, 不是卡片**.
 *	   用户的原话: "太粗糙, 不够动感精致, 缺少飞机火车等各种插画".
 *
 *	所以这里认的不是种类, 是**形状**: 有起点终点就画成一条行程线,
 *	有时间就摆在两端, 有图就当封面, 剩下的排成两列. 再配一张按词认出来
 *	的图形(flight/航班/飞机 → 一架飞机, 见 glyphs.tsx).
 *
 *	画得像不像那张"专门设计过的卡"不重要 —— 重要的是它一眼能看出
 *	**这是一件什么事**, 而不是"这是一个 JSON".
 */

/** 这几个是外壳不是内容 */
const SHELL = new Set(["type", "id", "kind"]);

const isTitle = (k: string) => /^(title|name|heading|label|subject|标题|名称)$/i.test(k);
const isText = (k: string) => /^(text|body|desc|description|summary|caption|note|content|摘要|说明|备注)$/i.test(k);
const isLink = (k: string) => /^(url|href|link|website|site|链接|网址)$/i.test(k);
const isImage = (k: string) => /^(src|path|image|img|cover|thumb|thumbnail|photo|封面|图)$/i.test(k);
const isFrom = (k: string) => /^(from|origin|start|source|departure|depart_from|出发|起点|始发)$/i.test(k);
const isTo = (k: string) => /^(to|dest|destination|end|target|arrival|arrive_at|到达|终点|目的地)$/i.test(k);
const isDepart = (k: string) => /^(depart|departs|departure|leave|start_at|出发时间|发车|起飞)$/i.test(k);
const isArrive = (k: string) => /^(arrive|arrives|arrival|end_at|到达时间|到站|落地)$/i.test(k);

export function OpenCard({ spec, ctx }: { spec: WidgetSpec; ctx: WidgetContext }) {
  const kind = String(spec.type);
  const keys = Object.keys(spec);
  /**
   * **有起点终点, 那至少是一趟行程** —— 认不出坐什么去的, 也别给一个
   * 中性的方块: 一个地点图钉是诚实的("这是一段路"), 而方块什么都没说.
   *   itinerary + departure 就是这种: 词里认不出飞机还是火车.
   */
  const glyph = glyphOf(kind, keys, keys.some(isFrom) && keys.some(isTo) ? "place" : undefined);

  let title: string | null = null;
  let text: string | null = null;
  let cover: string | null = null;
  let link: string | null = null;
  const trip: Record<string, string> = {};
  const rest: [string, unknown][] = [];

  for (const [key, value] of Object.entries(spec)) {
    if (SHELL.has(key) || value === null || value === undefined || value === "") continue;
    const flat = typeof value === "string" || typeof value === "number" || typeof value === "boolean";
    if (flat && title === null && isTitle(key)) { title = String(value); continue; }
    if (flat && text === null && isText(key)) { text = String(value); continue; }
    if (flat && link === null && isLink(key)) { link = String(value); continue; }
    if (flat && cover === null && isImage(key)) { cover = srcOf(String(value), ctx); continue; }
    if (flat && isFrom(key)) { trip.from = String(value); continue; }
    if (flat && isTo(key)) { trip.to = String(value); continue; }
    if (flat && isDepart(key)) { trip.depart = String(value); continue; }
    if (flat && isArrive(key)) { trip.arrive = String(value); continue; }
    rest.push([key, value]);
  }

  const hue = glyph.hue;
  return (
    <div className="w w--open" style={{ ["--g" as string]: hue }}>
      {/**
        * **卡面上那一大张图形**.
        *
        *	上一版只有卡头 17px 的小徽章 —— 用户的原话: "整个大的高铁飞机
        *	等元素都太少了". 他要的不是多一个图标, 是这张卡**看着就是一趟
        *	航班**.
        *
        *	做法是同一条矢量放到 176px 压在卡面上, 极淡、往右出血、往左
        *	渐隐. 放大之后 1.4px 的笔画变成一条舒展的长线 —— 那正是"插画"
        *	该有的样子, 而且跟界面别处是同一套笔.
        *
        *	**它是背景不是内容**: 淡到读字时注意不到, 扫一眼才看见.
        *	有封面图的那张不画 —— 两张图叠在一起谁都看不清.
        */}
      {cover !== null ? null : (
        <span className="open__art" aria-hidden="true"><Glyph glyph={glyph} size={176} /></span>
      )}
      {cover === null ? null : (
        <button className="open__cover" type="button" onClick={() => ctx.onOpenImage?.(cover, title ?? kind)}>
          <img src={cover} alt={title ?? kind} onError={(e) => { e.currentTarget.closest("button")?.remove(); }} />
        </button>
      )}
      <div className="open__head">
        <span className="open__glyph"><Glyph glyph={glyph} size={17} /></span>
        <span className="open__title">{title ?? kind}</span>
        {/* **种类照实标出来**: 认不出是哪一种, 那本身就是要说的一件事 ——
            用户看到 flight 却画成通用卡, 至少知道少的是样子不是内容 */}
        {title === null ? null : <span className="open__kind">{kind}</span>}
      </div>

      {trip.from === undefined && trip.to === undefined ? null : (
        <Trip trip={trip} glyph={glyph} />
      )}

      {text === null ? null : <p className="open__text">{text}</p>}

      {rest.length === 0 ? null : (
        <div className="open__grid">
          {rest.map(([key, value]) => <Cell key={key} name={key} value={value} ctx={ctx} />)}
        </div>
      )}

      {link === null ? null : (
        <a className="open__go" href={link} target="_blank" rel="noreferrer" title={link}>
          <span>{shortLink(link)}</span>
          <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor"
            strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M6.4 3.4h6.2v6.2M12.6 3.4 6 10" /><path d="M11 9.6v3H3.4V5h3" />
          </svg>
        </a>
      )}
    </div>
  );
}

/**
 * 一条行程 —— **这是"动感"该出现的地方**.
 *
 *	`from: 虹桥 T2` `to: 首都 T3` 两行字, 跟用连线和移动图标连接两端
 *	说的是同一件事, 而后者直观呈现方向, 不必逐行读字段. 时间摆在两端下面, 因为人先看
 *	"从哪到哪", 再看"几点".
 */
function Trip({ trip, glyph }: { trip: Record<string, string>; glyph: Parameters<typeof Glyph>[0]["glyph"] }) {
  return (
    <div className="trip">
      <div className="trip__end">
        <div className="trip__spot">{trip.from ?? "—"}</div>
        {trip.depart === undefined ? null : <div className="trip__at">{trip.depart}</div>}
      </div>
      <div className="trip__line" aria-hidden="true">
        <span className="trip__dot" />
        <span className="trip__rail" />
        {/* 图形沿着线走一趟 —— 只在它进入视野时走一次, 不是无限循环:
            一直动的东西会一直把眼睛拽过去 */}
        <span className="trip__move"><Glyph glyph={glyph} size={15} moving /></span>
        <span className="trip__rail" />
        <span className="trip__dot trip__dot--end" />
      </div>
      <div className="trip__end trip__end--right">
        <div className="trip__spot">{trip.to ?? "—"}</div>
        {trip.arrive === undefined ? null : <div className="trip__at">{trip.arrive}</div>}
      </div>
    </div>
  );
}

function Cell({ name, value, ctx }: { name: string; value: unknown; ctx: WidgetContext }) {
  if (Array.isArray(value)) {
    return (
      <div className="open__cell open__cell--wide">
        <span className="open__key">{name}</span>
        <ul className="open__list">
          {value.slice(0, 12).map((item, index) => (
            <li key={index}>{typeof item === "object" && item !== null ? brief(item) : String(item)}</li>
          ))}
        </ul>
      </div>
    );
  }
  if (typeof value === "object" && value !== null) {
    return (
      <div className="open__cell open__cell--wide">
        <span className="open__key">{name}</span>
        <span className="open__val">{brief(value)}</span>
      </div>
    );
  }
  const shown = String(value);
  return (
    <div className={`open__cell${shown.length > 24 ? " open__cell--wide" : ""}`}>
      <span className="open__key">{name}</span>
      <span className="open__val">{shown}</span>
    </div>
  );
}

/** 本地路径交给 OS 那侧代取 —— 浏览器打不开 file:// */
function srcOf(value: string, ctx: WidgetContext): string {
  if (value.startsWith("http") || value.startsWith("data:")) return value;
  return ctx.shotSrc?.byPath(value) ?? value;
}

/** 链接只留"站点 + 尾巴" —— 整条 url 铺在卡底下是噪音 */
function shortLink(url: string): string {
  try {
    const parsed = new URL(url);
    const tail = parsed.pathname === "/" ? "" : parsed.pathname;
    return parsed.host + (tail.length > 24 ? tail.slice(0, 24) + "…" : tail);
  } catch { return url; }
}

/** 嵌套对象压成一行 —— **不递归下去**: 一张卡不该长成一棵树 */
function brief(value: unknown): string {
  if (value === null || typeof value !== "object") return String(value);
  return Object.entries(value as Record<string, unknown>)
    .filter(([, one]) => one !== null && typeof one !== "object")
    .map(([key, one]) => `${key}: ${String(one)}`)
    .join(" · ");
}

export type { ReactNode };
