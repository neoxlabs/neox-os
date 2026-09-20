/**
 * Timeline — 长期运行不卡的那一版.
 *
 *   四件事必须同时成立:
 *
 *   ① **窗口化**: 只渲染视口 ± overscan. 十万条和一百条渲染成本一样.
 *   ② **真实高度**: 行高不定, 量了缓存, 没量过按 kind 估. 量之前布局会跳.
 *   ③ **钉底是几何判断, 不是标志位**.
 *      programmatic 一次性标志会忽略"自己引起的滚动": 已在底部时滚到底,
 *      浏览器不发 scroll 事件，标志无人消费，**下一次真正的上翻就会被它吃掉** —
 *      新内容一到又被拽回底部. 所以只认几何:
 *      距底 <= SLACK 就是钉着, 否则不是. 程序滚到底 → 距底 0 → 依然钉着, 自洽.
 *   ④ **流式只改那一个文本节点**, 且一帧只写一次.
 *      querySelector + indexOf 找行会把 60fps 降到 22fps;
 *      注册表 O(1) 配合帧内合并才能维持增量更新.
 */

import { t, tt } from "../i18n/index.js";
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { Row, RowKind, RowProjector, ToolCall } from "./rows.js";
import { RowRegistry } from "./registry.js";
import type { Shots } from "../shell/blobs.js";
import { Avatar, type Liveness } from "./Avatar.js";
import { renderMarkdown, type MarkdownMedia } from "./markdown.js";
import { stampBetween } from "./stamp.js";
import { CallCard } from "./CallCard.js";

const ESTIMATE: Record<RowKind, number> = {
  said: 92, heard: 78, thought: 60, did: 28, tools: 30, ask: 96, widget: 150, alert: 96,
  note: 26, joined: 26, wake: 28, outcome: 34
};

const OVERSCAN = 6;
/** 距底多少像素算"还在底部". 太小会被亚像素误差判错 */
const BOTTOM_SLACK = 32;
/** 高度变化小于这个不算变 —— 否则会量-渲染-再量地自激 */
const HEIGHT_EPSILON = 0.5;

export interface TimelineProps {
  projector: RowProjector;
  version: number;
  renderWidget(row: Row): ReactNode;
  /** 有人正在想但还没吐第一个字 —— 给一条带脸的 shimmer 占位, 不给空白 */
  /** 图从哪儿取 —— 事件里只有 id/路径, 字节在 OS 那一侧 */
  shotSrc?: Shots | undefined;
  /** 点了一张图 —— 交给上面开大图 */
  onOpenImage?(src: string, alt: string): void;
  /**
   * **刹车**: 停掉这一轮.
   *
   *   按钮就放在"正在干活"那一行上 —— 那正是你想按它的时候盯着的地方.
   */
  onStop?(who: string): void;
  /** 点了某个人的头像 */
  /** 点了某个人的头像 —— **连头像在哪儿一起给**: 资料卡是贴着它弹的 */
  onWho?(name: string, anchor: DOMRect): void;
  /**
   * 我叫什么 —— **我说的话右边也该有一张脸**.
   *
   *   一屋子人的对话里, 每个人说话都带头像, 只有我不带 —— 那一列
   *   看下来像是"有人在自言自语, 中间夹着几条没有主人的话".
   *   头像同时是这一行的落点: 眼睛顺着右边那一列脸就能找到我说过什么.
   */
  me?: string;
  /**
   * 这是不是房间(要不要标名字).
   *
   *   **必须是 prop**: 投影器上那个可变字段改了不会触发重渲染 ——
   *   Timeline 只在 version 变时重画, 而房间身份在成员从 SSE 增量到达后
   *   才确定，那一刻往往没有新事件；名字已应显示却不会出现在屏幕上.
   */
  showNames?: boolean;
  /**
   * 房间里都有谁 —— @点名靠它精确匹配.
   *
   *   **不按"@ 后面跟着什么"去猜**: 猜的话邮箱 a@b.com、"@2x 的图"
   *   都会被点亮, 而点亮一个不存在的人比不点亮更糟 —— 它看起来
   *   像"这里真有个人".
   */
  mentions?: readonly string[];
  onMetrics?(m: { rendered: number; total: number }): void;
  /**
   * 谁现在什么状态 —— 时间线里的头像也带右下角那个点.
   *
   *   侧栏有点、时间线没有的话, 人在长对话里根本不知道说话的这位
   *   此刻是在忙还是已经说完了 —— 而这正是"运行中/结束"的标志.
   */
  presenceFor?(who: string): Liveness | undefined;
}

export function Timeline({ projector, version, renderWidget, onWho, showNames = false, mentions = [], onMetrics, me, shotSrc, onOpenImage, onStop, presenceFor }: TimelineProps) {
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const registry = useMemo(() => new RowRegistry(), []);
  const heights = useRef(new Map<string, number>());
  const offsets = useRef<number[]>([]);
  const dirtyFrom = useRef(0);
  const pinned = useRef(true);
  const [, bump] = useState(0);
  /**
   * 展开的思考行.
   *
   *   思考**默认折叠**. 不折叠的话一段推理就能占满整个视口,
   *   而推理本来就是"想看的时候才看"的东西 —— 它不参与判定,
   *   却因为字多而抢走了全部注意力.
   */
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const toggle = useCallback((id: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }, []);
  const [range, setRange] = useState({ start: 0, end: 0 });
  const rows = projector.rows;

  /**
   * **换了一个会话 = 换了一份坐标系**.
   *
   *   Timeline 在会话之间是同一个实例(没有 key), 而 offsets/dirtyFrom
   *   是 ref —— 它们跟着上一个会话走. dirtyFrom 停在上一个会话的行数上,
   *   于是新会话前面那几十行**根本没重排过**, 用的是上一个房间的 top:
   *   两条消息叠在同一个 y 上, 而且不会自己好, 手动滚一下才恢复.
   *   在别处滚过进度条再回群聊时，整屏会叠在一起.
   *
   *   在 render 里就地清, 不放 effect: effect 跑在这一次画完之后,
   *   而叠着的那一帧已经画出去了.
   */
  const shownFor = useRef(projector);
  const switched = shownFor.current !== projector;
  if (switched) {
    shownFor.current = projector;
    offsets.current = [];
    dirtyFrom.current = 0;
    // 换个会话默认看最新的一条 —— 上一个会话钉不钉底跟这个没关系
    pinned.current = true;
  }

  /** 高度变更攒到帧末统一提交, 不一条一次 setState */
  const heightFrame = useRef(0);
  const scheduleRelayout = useCallback((fromIndex: number) => {
    if (fromIndex < dirtyFrom.current) dirtyFrom.current = fromIndex;
    if (heightFrame.current !== 0) return;
    heightFrame.current = requestAnimationFrame(() => { heightFrame.current = 0; bump((n) => n + 1); });
  }, []);

  const rebuildOffsets = useCallback(() => {
    const list = offsets.current;
    let index = Math.min(dirtyFrom.current, list.length);
    let top = index === 0 ? 0 : (list[index - 1] ?? 0) + heightOf(rows[index - 1], heights.current);
    for (; index < rows.length; index += 1) {
      list[index] = top;
      top += heightOf(rows[index], heights.current);
    }
    list.length = rows.length;
    dirtyFrom.current = rows.length;
    return top;
  }, [rows]);

  const totalHeight = rebuildOffsets();

  /**
   * 进入动效只给**挂载之后新来的**行.
   *
   *   虚拟列表里旧行也会因为滚进视口而 mount —— 不区分的话,
   *   往回滚会看见一整屏历史消息挨个"飞进来", 那不是动效是故障.
   */
  const mountedAt = useRef(0);
  if (mountedAt.current === 0 || switched) mountedAt.current = Date.now();

  /** 二分定位首行. 线性扫在几万行时每帧要几毫秒 */
  const computeRange = useCallback(() => {
    const scroller = scrollerRef.current;
    if (scroller == null || rows.length === 0) return { start: 0, end: 0 };
    const list = offsets.current;
    const top = scroller.scrollTop;
    let lo = 0, hi = rows.length - 1, start = 0;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if ((list[mid] ?? 0) <= top) { start = mid; lo = mid + 1; } else { hi = mid - 1; }
    }
    const bottom = top + scroller.clientHeight;
    let end = start;
    while (end < rows.length && (list[end] ?? 0) < bottom) end += 1;
    return { start: Math.max(0, start - OVERSCAN), end: Math.min(rows.length, end + OVERSCAN) };
    // projector 进依赖: 两个会话行数正好一样时, 只看 rows.length 是看不出换过的
  }, [rows.length, projector]);

  const syncPinned = useCallback(() => {
    const scroller = scrollerRef.current;
    if (scroller == null) return;
    pinned.current = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <= BOTTOM_SLACK;
  }, []);

  const stickIfPinned = useCallback(() => {
    const scroller = scrollerRef.current;
    if (scroller == null || !pinned.current) return;
    scroller.scrollTop = scroller.scrollHeight;
  }, []);

  useEffect(() => {
    const scroller = scrollerRef.current;
    if (scroller == null) return;
    let frame = 0;
    const onScroll = () => {
      if (frame !== 0) return;
      frame = requestAnimationFrame(() => { frame = 0; syncPinned(); setRange(computeRange()); });
    };
    scroller.addEventListener("scroll", onScroll, { passive: true });
    syncPinned();
    setRange(computeRange());
    /**
     * **视口自己变矮时, 钉着的要保持钉着**.
     *
     *	输入框长到第二行, 时间线的底边跟着上移 21px —— 滚动位置却一动不动,
     *	最后一条消息就被输入框盖住了; 而且钉底判据(距底 ≤32px)恰好被这一涨
     *	顶破, 从此再也不自动回底. 用户看到的是"每次打字 timeline 都下沉".
     *
     *	改法只有一条: 尺寸变化时, **按变化前的钉底状态**补滚动.
     *	pinned.current 正是变化前的那份(它只在 scroll 事件里更新).
     */
    const keep = new ResizeObserver(() => { stickIfPinned(); setRange(computeRange()); });
    keep.observe(scroller);
    // 内容层也要看住: 行高校正后 spacer 会长个几十像素(估高 → 实际高度),
    // 钉着的人不该因为这种内部重排离开底部
    if (scroller.firstElementChild !== null) keep.observe(scroller.firstElementChild);
    return () => {
      cancelAnimationFrame(frame);
      scroller.removeEventListener("scroll", onScroll);
      keep.disconnect();
    };
  }, [computeRange, syncPinned, stickIfPinned]);

  useLayoutEffect(() => {
    setRange(computeRange());
    stickIfPinned();
  }, [projector, version, rows.length, totalHeight, computeRange, stickIfPinned]);

  // 展开/收起会改行高 —— 从第一行起全部重排, 让 ResizeObserver 量到的新高度生效
  useLayoutEffect(() => { scheduleRelayout(0); }, [expanded, scheduleRelayout]);

  /**
   * 行换过位置 —— **从第一行起全部重排**.
   *
   *   offsets 是按位置索引的, 而归位(rows.ts 的 #resettle)会把整个数组
   *   重排. 行数没变、内容没变, 只是顺序变了 —— 这边看不出来, 于是接着
   *   用旧的 offsets: **行叠在一起, 而且不会自己好**, 滚一下才恢复.
   */
  useLayoutEffect(() => { scheduleRelayout(0); }, [projector.reordered, scheduleRelayout]);

  /** 流式: 文本写入排进注册表, 帧末统一落 DOM, 落完再统一量高 */
  useEffect(() => {
    registry.onFlush(() => {
      let earliest = Number.POSITIVE_INFINITY;
      for (const row of projector.openRows()) {
        const nodes = registry.get(row.id);
        if (nodes === undefined) continue;
        const measured = nodes.host.offsetHeight;
        const known = heights.current.get(row.id) ?? 0;
        if (Math.abs(known - measured) <= HEIGHT_EPSILON) continue;
        heights.current.set(row.id, measured);
        if (row.index < earliest) earliest = row.index;
      }
      if (earliest !== Number.POSITIVE_INFINITY) scheduleRelayout(earliest);
      stickIfPinned();
    });
    /**
     * **只有还在流的行才走这条快路**.
     *
     *	queueText 绕过 React 直接写 DOM(一秒几千条事件, 每条都过 React
     *	就是必卡), 但它写的是**纯文本** —— 往一行已经画好 markdown 的话上
     *	写一次, 反引号和星号就全变回字面量了.
     *
     *	行写完之后归 React 管: open 翻成 false 会让 memo 放行, 那一次
     *	重画正好把 markdown 解析出来.
     */
    projector.onRowChanged((row) => { if (row.open) registry.queueText(row.id, row.text); });
    return () => registry.dispose();
  }, [projector, registry, scheduleRelayout, stickIfPinned]);

  useEffect(() => { onMetrics?.({ rendered: range.end - range.start, total: rows.length }); }, [range, rows.length, onMetrics]);

  const visible: ReactNode[] = [];
  const now = Date.now();
  for (let index = range.start; index < range.end; index += 1) {
    const row = rows[index];
    if (row === undefined || row.merged === true) continue;
    /**
     * 跟**上一个看得见的行**比.
     *
     *	被合并掉的行(拍完板的审批卡)还在数组里但不显示 —— 拿它当上一行,
     *	算出来的间隔是错的, 而且它就在隔壁, 几乎永远算不出该插时间.
     */
    let previousAt: number | undefined;
    for (let back = index - 1; back >= 0; back -= 1) {
      const earlier = rows[back];
      if (earlier === undefined || earlier.merged === true) continue;
      previousAt = earlier.at;
      break;
    }
    const stamp = stampBetween(previousAt, row.at, now);
    visible.push(
      <RowView
        key={row.id}
        mentions={mentions}
        row={row}
        top={offsets.current[index] ?? 0}
        registry={registry}
        onHeight={(height) => {
          const known = heights.current.get(row.id);
          if (known !== undefined && Math.abs(known - height) <= HEIGHT_EPSILON) return;
          heights.current.set(row.id, height);
          /**
           * **从 row.index 重排, 不是从渲染时捕获的那个 index**.
           *
           *	两个原因都会让捕获的那个过期:
           *	  · memo 挡住重渲染时, 这个闭包整个不会更新;
           *	  · 行会**换位置** —— 历史是分批到的, 后到的旧事件要按时间
           *	    归位(见 rows.ts 的 #resettle).
           *
           *	用过期的下标去重排, 排在它前面的那几行就一直用着旧高度 ——
           *	表现为**行叠在一起**, 而且不会自己好, 滚一下才恢复.
           *	row.index 是 projector 一直在维护的那个, 归位时会一起更新.
           */
          scheduleRelayout(row.index);
        }}
        // **就地改的东西要单独当 prop 传**: 比较函数里读 a.row.x 是没用的 ——
        // 行是同一个对象, a.row.x 和 b.row.x 永远相等, 那个判据从来没生效过.
        open={row.open}
        revision={row.revision}
        stamp={stamp}
        widget={row.kind === "widget" ? renderWidget(row) : null}
        isLast={index === rows.length - 1}
        fresh={row.at > mountedAt.current && index >= rows.length - 3}
        showName={row.showWho === true && showNames}
        {...(me === undefined || me === "" ? {} : { me })}
        expanded={expanded.has(row.id)}
        onToggle={toggle}
        {...(row.who !== undefined && presenceFor?.(row.who) !== undefined ? { liveness: presenceFor(row.who)! } : {})}
        {...(onWho === undefined ? {} : { onWho })}
        {...(shotSrc === undefined ? {} : { shotSrc })}
        {...(onOpenImage === undefined ? {} : { onOpenImage })}
      />
    );
  }

  /**
   * "正在干活"不再是时间线里的一行.
   *
   *	它原来钉在最底下 —— 小规长跑一分多钟时, 用户跟 OA 的新对话只能
   *	排在它上面, 看着像消息串错了位(真机原话: "以为是搞错了").
   *	状态归状态条(BusyStrip, 在输入框上方), 消息归时间线:
   *	时间线的最后一条**永远是真消息**.
   */
  return (
    <div className="timeline" ref={scrollerRef}>
      <div className="timeline__spacer" style={{ height: totalHeight }}>
        {visible}
      </div>
    </div>
  );
}

function heightOf(row: Row | undefined, heights: Map<string, number>): number {
  if (row === undefined) return 0;
  // 并进别处的行不占高度 —— 留着它只是为了 index 不错位
  if (row.merged === true) return 0;
  return heights.get(row.id) ?? ESTIMATE[row.kind];
}

interface RowViewProps {
  row: Row; top: number; registry: RowRegistry;
  /** 行是就地改的 —— 这两个必须走 prop, 比较器里读 row.x 永远相等 */
  open: boolean; revision: number;
  /** 这一行上面要不要插一个时间. null = 紧挨着上一行, 不用插 */
  stamp: string | null;
  onHeight(height: number): void; widget: ReactNode; isLast: boolean; fresh: boolean;
  expanded: boolean; onToggle(id: string): void; onWho?(name: string, anchor: DOMRect): void;
  /**
   * 露不露名字 —— **必须走 prop, 不能在比较器里读 row.showName**.
   *
   *   行对象是可变的(流式那条路要求它可变), 于是比较器里
   *   `a.row.showName === b.row.showName` 拿的是同一个对象的同一个字段,
   *   永远相等 —— 改了也不会重渲染. prop 是每次 render 捕获的快照,
   *   它才比得出来.
   */
  showName: boolean;
  /** 房间成员名 —— @点名按真名匹配, 不猜 */
  mentions: readonly string[];
  /** 我叫什么 —— 我说的那几行右边挂我的脸 */
  me?: string;
  /** 这一行主人的状态 —— 头像右下角那个点. undefined = 不摆点 */
  liveness?: Liveness;
  shotSrc?: Shots;
  onOpenImage?(src: string, alt: string): void;
}

/**
 * Doing —— "第 12 步 · 2 分 10 秒 · 花了 43k".
 *
 *   **自己走秒**: 一条命令跑三分钟, 那三分钟里一条事件都没有 ——
 *   靠事件驱动重渲染的话, 时间会停在开跑那一刻, 看着像卡死了.
 */
export function Doing({ at, steps, tokens }: { at?: number | undefined; steps?: number | undefined; tokens?: number | undefined }) {
  const [, tick] = useState(0);
  useEffect(() => {
    if (at === undefined) return;
    const timer = window.setInterval(() => tick((n) => n + 1), 1000);
    return () => window.clearInterval(timer);
  }, [at]);
  if (at === undefined) return null;
  const parts: string[] = [];
  if (steps !== undefined && steps > 0) parts.push(tt("第 {a} 步", { a: steps }));
  parts.push(spell(Date.now() - at));
  if (tokens !== undefined && tokens > 0) parts.push(tt("花了 {a}", { a: short(tokens) }));
  return <span className="row__doing">{parts.join(" · ")}</span>;
}

/** 多久了 —— 秒以下不显示, 那个精度对人没意义 */
function spell(ms: number): string {
  const secs = Math.max(0, Math.round(ms / 1000));
  if (secs < 60) return tt("{a} 秒", { a: secs });
  const mins = Math.floor(secs / 60);
  if (mins < 60) return tt("{a} 分 {b} 秒", { a: mins, b: secs % 60 });
  return tt("{a} 小时 {b} 分", { a: Math.floor(mins / 60), b: mins % 60 });
}

function short(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}

const RowView = memo(function RowView({ row, top, registry, onHeight, widget, isLast, fresh, expanded, onToggle, onWho, showName, mentions, stamp, me, liveness, shotSrc, onOpenImage }: RowViewProps) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const textRef = useRef<HTMLSpanElement | null>(null);
  const heightCb = useRef(onHeight);
  heightCb.current = onHeight;

  useLayoutEffect(() => {
    const host = hostRef.current;
    if (host == null) return;
    const unmount = textRef.current == null ? undefined : registry.mount(row.id, { host, text: textRef.current });

    /**
     * 量两次: 一次立刻, 一次下一帧.
     *
     *   **layout effect 里量到的可能不是最终高度**:
     *   首屏行高可能缓存为 48, 最终高度却是 62. 若后续没有尺寸变化
     *   触发 ResizeObserver 回调来修正缓存, 错值就会一直用于布局,
     *   导致行与行叠在一起. 下一帧再量一次, 用于校正初次测量.
     *
     *   不重新测量就不会恢复: 重叠会持续存在, 而非短暂闪烁,
     *   且可能只影响某几行, 看起来像随机的渲染错乱.
     */
    const measure = () => { if (hostRef.current != null) heightCb.current(hostRef.current.offsetHeight); };
    measure();
    const frame = requestAnimationFrame(measure);

    const observer = new ResizeObserver(measure);
    observer.observe(host);
    return () => { cancelAnimationFrame(frame); observer.disconnect(); unmount?.(); };
    // **stamp 也要进依赖**: 插一个时间会让这一行高出一截, 而上面那段
    // 这正是"只靠 ResizeObserver 会漏"的情况：带时间的行
    // 带时间的行按没带时间的高度排, 于是压在上一行身上, 滚一下才好.
  }, [row.id, registry, stamp]);

  return (
    <div
      className={`row row--${row.kind}${row.tone ? ` row--${row.tone}` : ""}${row.showWho ? " row--lead" : ""}${fresh ? " row--fresh" : ""}`}
      data-row={row.id}
      ref={hostRef}
      style={{ top }}
    >
      {stamp === null ? null : <div className="row__stamp">{stamp}</div>}
      <div className="row__gutter">
        {row.showWho && row.who !== undefined
          ? (onWho === undefined
              ? <Avatar id={row.who} size={26} {...(liveness === undefined ? {} : { liveness })} />
              : <button className="who" onClick={(event) => onWho(row.who!, event.currentTarget.getBoundingClientRect())} aria-label={tt("{a} 的资料", { a: row.who })}>
                  <Avatar id={row.who} size={26} {...(liveness === undefined ? {} : { liveness })} />
                </button>)
          : null}
      </div>
      <div className="row__body">
        {/* 群里才标名字 —— 一对一时对面只有一个人, 头像已经说明了是谁 */}
        {row.showWho && row.who !== undefined && showName
          ? <div className="row__who">{row.who}</div>
          : null}
        <div className="row__line">
          {row.kind === "tools" ? (
            <ToolGroup row={row} expanded={expanded || row.open} onToggle={() => onToggle(row.id)}
              {...(shotSrc === undefined ? {} : { shotSrc })}
              {...(onOpenImage === undefined ? {} : { onOpenImage })} />
          ) : null}
          {row.kind === "thought" ? (
            <button className="think" onClick={() => onToggle(row.id)} type="button">
              <span className={`think__caret${expanded ? " think__caret--open" : ""}`} />
              <span className={row.open ? "shimmer" : "think__label"}>
                {row.open ? t("正在想…") : expanded ? t("收起想的") : t("想了一会儿")}
              </span>
            </button>
          ) : null}
          {row.title !== undefined && row.title.length > 0
            ? <span className={row.kind === "did" ? "tool__name" : "row__title"}>
                {row.title}
                {/* 重试的那几次并进了这一张卡 —— **次数要标出来**:
                    不标的话看起来像只发生了一次, 而"连着三次"本身
                    就是判断"这是偶发还是真坏了"的依据 */}
                {(row.repeat ?? 1) > 1 ? <span className="row__times">×{row.repeat}</span> : null}
              </span>
            : null}
          {row.kind === "tools" ? null : widget ?? (
            <span
              className={`row__text${row.kind === "thought" && !expanded ? " row__text--hidden" : ""}`}
              ref={textRef}
            >{body(row, mentions, shotSrc === undefined ? undefined : {
              // 回复里写 ![](绝对路径) 就地出图 —— 字节走 /blob?path=, 只放图片过
              src: (path) => shotSrc.byPath(path),
              ...(onOpenImage === undefined ? {} : { onOpen: onOpenImage })
            })}</span>
          )}
          {row.kind === "did" && row.open ? <span className="tool__spin" /> : null}
          {/* 光标只跟最后一行走. 早先每个没关掉的流都画一根,
            于是历史里散着一排竖线, 看着像渲染坏了 */}
        {row.open && isLast ? <span className="row__caret" /> : null}
        </div>
        {/* **图跟这句话是同一条消息**: 单独占一行的话, 一句"你看这个"
            和它说的那张图之间会插进别人的发言 */}
        {row.shots === undefined || shotSrc === undefined ? null : (
          <div className="shotwall">
            {row.shots.map((shot) => (
              <button key={shot.id} className="shotwall__one" type="button"
                onClick={() => onOpenImage?.(shotSrc.byId(shot.id), shot.name ?? t("图"))}
                title={shot.name ?? t("点开看大的")}>
                <img src={shotSrc.byId(shot.id)} alt={shot.name ?? t("图")} loading="lazy" />
              </button>
            ))}
          </div>
        )}
        {/* 文件附件: 一枚标签, 不取字节 —— 内容归 bot 读 */}
        {row.files === undefined || row.files.length === 0 ? null : (
          <div className="filewall">
            {row.files.map((one, index) => (
              <span key={`${one.name}-${index}`} className="filewall__one" title={one.name}>
                <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true">
                  <path d="M4 1.5h5.5L13 5v9.5H4z M9.5 1.5V5H13" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" />
                </svg>
                {one.name}
              </span>
            ))}
          </div>
        )}
        {row.kind === "did" && row.result !== undefined && row.result.length > 0
          ? <pre className="tool__out">{row.result}</pre>
          : null}
      </div>
      {/* 我说的那一行, 脸挂在**右边**: 一屋子人说话都带头像, 只有我不带的话,
          那一列看下来像"有人在自言自语, 中间夹着几条没主人的话" */}
      {row.kind === "heard" && me !== undefined
        ? <div className="row__gutter row__gutter--me"><Avatar id={me} size={26} /></div>
        : null}
    </div>
  );
}, (a, b) => a.row === b.row && a.top === b.top && a.revision === b.revision
  && a.open === b.open
  && a.showName === b.showName && a.widget === b.widget && a.isLast === b.isLast
  && a.fresh === b.fresh && a.expanded === b.expanded && a.stamp === b.stamp && a.me === b.me
  && a.liveness === b.liveness
  && a.shotSrc === b.shotSrc);

/**
 * 把 @名字 标出来.
 *
 *   **只在 heard 行上做**. bot 说的话走的是流式直写 textContent
 *   那条路, 把它拆成多个节点, 下一次增量写就会把整段结构冲掉.
 */
function highlightMentions(text: string, mentions: readonly string[]): ReactNode {
  if (!text.includes("@") || mentions.length === 0) return text;
  const parts: ReactNode[] = [];
  // **按真实成员名匹配, 不按"@ 后面跟着什么"猜**. 猜的那一版会点亮
  // 邮箱 a@b.com、"@2x 的图" —— 点亮一个不存在的人比不点亮更糟,
  // 它看起来像"这里真有个人".
  const pattern = new RegExp(
    "@(?:" + mentions.map((n) => n.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
      .sort((a, b) => b.length - a.length).join("|") + ")", "g");
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    const at = match.index ?? 0;
    if (at > cursor) parts.push(text.slice(cursor, at));
    parts.push(<b className="mentionTag" key={`${at}-${match[0]}`}>{match[0]}</b>);
    cursor = at + match[0].length;
  }
  if (cursor < text.length) parts.push(text.slice(cursor));
  return parts;
}

/**
 * 一行的正文.
 *
 *   **正在长的那一行只能是纯文本**: 它走的是直写 textContent 的路,
 *   拆成多个节点, 下一次增量写就会把整段结构冲掉.
 *   长完了(open=false)再按 markdown 排版.
 */
/**
 * 这一行的正文按什么画.
 *
 *   **单独拎出来是为了钉得住**: 判据留在 body() 里的话, 只有把整个
 *   组件渲染出来才验得到；alert 的渲染规则会缺少直接的测试保护.
 */
export function bodyKind(row: { kind: RowKind; open: boolean }): "mentions" | "markdown" | "plain" {
  if (row.kind === "heard") return "mentions";
  // **bot 说的话原来一个 @ 都不点亮** —— 只有用户自己那条走了高亮.
  // 而房间里互相点名恰恰是 bot 之间最常干的事.
  //
  // 还在流的时候按纯文本画: markdown 要等它写完才敢解析,
  // 否则半个 ** 会被当成加粗.
  if (row.kind === "said") return row.open ? "plain" : "markdown";
  /**
   * **告警卡里的话也是写给人看的**.
   *
   *	那段文字是 OS 写的(停滞检测、步数上限…), 而它本来就是照着提示词
   *	的口气写的, 里面带着 `**用 find_files 搜一下**` 这种强调. 不解析
   *	的话, 用户看到的是一串字面的星号 —— 真机截图里就是这样, 而且它
   *	恰好落在**最需要看清楚**的那句上(该怎么办).
   *
   *	告警卡不会流式生长(一次写完), 所以不用像 said 那样等 open 关掉.
   */
  if (row.kind === "alert") return "markdown";
  return "plain";
}

function body(row: Row, mentions: readonly string[], media?: MarkdownMedia): ReactNode {
  switch (bodyKind(row)) {
    case "mentions": return highlightMentions(row.text, mentions);
    case "markdown": return renderMarkdown(row.text, mentions, media);
    default: return row.text;
  }
}

/**
 * 一轮用过的工具.
 *
 *   跑的时候展开(看得见进度), 跑完折成一行"用了 N 个工具".
 *   **默认收起**是因为这是 bot 聊天不是 IDE —— 你让它干活,
 *   不是让它汇报每一步; 要看的时候点开就是了.
 */
function ToolGroup({ row, expanded, onToggle, shotSrc, onOpenImage }: {
  row: Row; expanded: boolean; onToggle(): void;
  shotSrc?: Shots | undefined; onOpenImage?(src: string, alt: string): void;
}) {
  const calls = row.calls ?? [];
  const [openCall, setOpenCall] = useState<number | null>(null);
  return (
    <div className="tg">
      <button className="tg__head" onClick={onToggle} type="button">
        <span className={`think__caret${expanded ? " think__caret--open" : ""}`} />
        {row.open
          ? <span className="shimmer">{calls[calls.length - 1]?.name ?? t("正在动手")}…</span>
          : <span className="tg__label">{(row.thought !== undefined && row.thought !== "" ? t("想了一会儿 · ") : "") + summarize(calls)}</span>}
      </button>
      {expanded ? (
        <div className="tg__list">
          {/* 想的在前, 动的在后 —— 顺序就是它真实的顺序 */}
          {row.thought !== undefined && row.thought !== ""
            ? <div className="tg__thought">{row.thought}</div>
            : null}
          {calls.map((call, index) => {
            const shown = openCall === index;
            const detailed = call.approval !== true && (call.result ?? "").trim().length > 0;
            return (
                <div className={`tg__row${call.failed && call.blocked !== true && call.missing !== true ? " tg__row--bad" : ""}${call.blocked === true ? " tg__row--held" : ""}${call.approval === true ? " tg__row--approval" : ""}`} key={`${call.name}-${index}`}>
                <button
                  className="tg__line"
                  type="button"
                  disabled={!detailed}
                  onClick={() => setOpenCall(shown ? null : index)}
                >
                  <span className="tool__name">{call.name}</span>
                  <span className="tg__arg">{call.arg}</span>
                  {/* 出事要说出来，不能只靠一层淡色底：
                      拦下 ≠ 没成 ≠ 没有: 闸挡危险动作是正常工作;
                      探查发现"还没有这个东西"是阴性结果, 刷红会吓到人 */}
                  {call.blocked === true
                    ? <span className="tg__flag tg__flag--held">{t("拦下")}</span>
                    : call.missing === true
                      ? <span className="tg__flag tg__flag--dim">{t("没有")}</span>
                      : call.failed && call.approval !== true
                        ? <span className="tg__flag tg__flag--bad">{t("没成")}</span>
                        : null}
                  {call.running ? <span className="tool__spin" /> : null}
                  {detailed ? <span className={`tg__more${shown ? " tg__more--open" : ""}`} /> : null}
                </button>
                {shown ? <CallCard call={call} {...(shotSrc === undefined ? {} : { shotSrc })}
                                   {...(onOpenImage === undefined ? {} : { onOpenImage })} /> : null}
              </div>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}

/**
 * 一行说清这一轮干了什么: 几个工具、几次授权、几个没成、几个被拦下.
 *
 *	"被拦下"跟"没成"分开数. 闸挡住一次危险动作是系统**正常工作**的样子,
 *	写成"没成"会让人去查一个不存在的故障 —— 更糟的是, 看多了会开始
 *	怀疑这些闸有毛病.
 */
function summarize(calls: readonly ToolCall[]): string {
  const tools = calls.filter((call) => call.approval !== true);
  const approvals = calls.filter((call) => call.approval === true);
  const blocked = tools.filter((call) => call.blocked === true).length;
  const missing = tools.filter((call) => call.missing === true).length;
  const failed = tools.filter((call) => call.failed && call.blocked !== true && call.missing !== true).length;
  const parts: string[] = [];
  if (tools.length > 0) parts.push(tt("用了 {a} 个工具", { a: tools.length }));
  if (approvals.length > 0) parts.push(tt("{a} 次授权", { a: approvals.length }));
  if (failed > 0) parts.push(tt("{a} 个没成", { a: failed }));
  if (blocked > 0) parts.push(tt("{a} 个被拦下", { a: blocked }));
  if (missing > 0) parts.push(tt("{a} 个没找到", { a: missing }));
  return parts.length > 0 ? parts.join(" · ") : t("没动手");
}
