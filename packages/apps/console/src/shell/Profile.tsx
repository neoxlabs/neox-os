import { t, tt } from "../i18n/index.js";
import { useEffect, useMemo, useRef, useState } from "react";
import { Tap, clock, homeShort, pathParts, useAnchored } from "./cardbits.js";
import { Avatar } from "../view/Avatar.js";
import { presenceOf } from "../view/presence.js";
import type { Member } from "./parties.js";
import type { BotOutput } from "../os/source-live.js";
import { roomLabel, roomLabels } from "./roomname.js";

/**
 * Profile — 点头像看这个人是谁.
 *
 *   **一张卡, 外加一扇随手拉开的副窗**.
 *
 *   卡本身只答一句话: 他是谁、在不在、在哪儿干活、在哪个房间.
 *   这些一屏看完就能做决定, 所以卡要小、要一眼扫完, 不许因为多了
 *   一件能干的事就把自己撑大.
 *
 *   而"改工作区""换房间""他说过的那几百条"是**另一件事**: 它们要列表、
 *   要翻页、要输入框. 早先的版本把它们就地展开在卡里 —— 卡当场膨胀,
 *   宽度却是写死的, 于是路径折三行、按钮挤成竖排, 每一处都难看.
 *   现在它们开在卡**右边贴着的一扇副窗**里: 卡不动, 副窗自己滚,
 *   两扇拼成一个左右布局 —— 左边是"他是谁", 右边是"这件事怎么办".
 */

/** 一条给人看的历史 —— 事件流翻译过一遍, 界面不该在这儿再认一次 payload */
export interface HistoryLine {
  readonly key: string;
  readonly at: number;
  /** 一个短标签: 说了、想了、用了工具、挂了… */
  readonly tag: string;
  readonly text: string;
  /** 这条是不是坏消息 —— 出事那几条要一眼看得出来 */
  readonly bad?: boolean;
}

export interface HistoryPage {
  readonly lines: readonly HistoryLine[];
  /** 翻得出来的总条数 —— 起停和记账不算, 见 history.ts */
  readonly total: number;
}

export interface ProfileTarget {
  readonly member: Member;
  /** 这个人一共有几条事件 —— 跨重启累计 */
  readonly events: number;
  /** 他在哪个房间里 */
  readonly rooms: readonly string[];
  /** 他起过的所有进程 —— 重启过几次、现在跑在哪个 pid 上, 都在这儿 */
  readonly pids: readonly string[];
  /** 能不能单独找他说话 */
  readonly canOpen: boolean;
  /** 他现在正在干活 —— **只有这时候才给那颗停** */
  readonly busy: boolean;
  /** 存在一条尚未确定的决策 —— "等待决策"只有这时候才是真的 */
  readonly pending: boolean;
}

/** 副窗里现在开着哪一件事. null = 只有卡 */
type Pane = "log" | "work" | "room" | "made";

/** 一次翻多少条历史 —— 几百条一次全画出来是白画, 没人一眼看几百条 */
const PAGE = 60;

export function Profile({ target, onClose, onOpen, onRebind, rooms: allRooms = [], works = [], onMove, history, output, anchor, onStop }: {
  target: ProfileTarget | null;
  onClose(): void;
  onOpen(member: Member): void;
  /**
   * **停下他这一轮**.
   *
   *   一个会自己改文件、自己花钱的东西, 必须有一颗按下去就停的按钮 ——
   *   而且要在**你想起他的时候找得到**: 时间线上那颗只在他正干活的那一
   *   瞬间露面, 而人常常是先点开他的卡, 才决定"算了别干了".
   */
  onStop?(name: string): void;
  /** 把他派到另一个工作区. 返回错误原因, null = 成了 */
  onRebind(name: string, work: string): Promise<string | null>;
  /** OS 上现有的房间 —— 换房间只能换进已经开着的房间 */
  rooms?: readonly string[];
  /**
   * 别人正在用的工作区 —— 换地方时**点一下就选中**.
   *
   *   最常见的那次换地方是"把他派到我另一个 bot 正在干的那摊活上",
   *   而那条路径此刻就在屏幕上别的地方. 让人重新敲一遍是白敲.
   */
  works?: readonly string[];
  /** 把他挪进另一个房间. 空 = 拉出来单独待着. 返回错误原因, null = 成了 */
  onMove(name: string, room: string): Promise<string | null>;
  /** 他说过的话 —— 最新的在前. 副窗自己按页取, 不一次要全部 */
  history?(pids: readonly string[], limit: number): HistoryPage;
  /**
   * 他干出来了什么 —— **判据是 git**.
   *
   *   它说"改好了"和它真的改了哪几个文件, 是两件事. 前者在对话里,
   *   后者在它自己那条分支上.
   */
  output?(bot: string): Promise<BotOutput | null>;
  /**
   * 点的那个头像在哪儿 —— **卡贴着它弹**.
   *
   *   弹在屏幕正中的话, 人得先在满屏里找一遍"我刚点的是谁";
   *   贴着头像出来, 那个问题根本不存在.
   *
   *   null = 不知道锚在哪(键盘打开的), 那就回到正中.
   */
  anchor?: DOMRect | null;
}) {
  const scrim = useRef<HTMLDivElement | null>(null);
  const dockRef = useRef<HTMLDivElement | null>(null);
  const [pane, setPane] = useState<Pane | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  /** 历史翻到第几页 —— 只往下翻, 换个人看就归零 */
  const [shown, setShown] = useState(PAGE);
  /** 它的产物. undefined = 还没问过 */
  const [made, setMade] = useState<BotOutput | null | undefined>(undefined);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  // 换个人看就把上一个人的状态清掉 —— 不清的话上一个人的路径会留在框里,
  // 而它看起来就像"这个人的工作区是那个"
  useEffect(() => {
    setPane(null); setDraft(""); setError(null); setBusy(false); setShown(PAGE);
    setMade(undefined);
  }, [target?.member.name]);

  // 产物**打开那一栏才去问**: 它要跑几条 git, 而多数时候人只是想看一眼
  // 这个人是谁
  useEffect(() => {
    if (pane !== "made" || target === null || output === undefined) return;
    let alive = true;
    void output(target.member.name).then((got) => { if (alive) setMade(got); });
    return () => { alive = false; };
  }, [pane, target?.member.name, output]);

  useEffect(() => {
    if (target === null) return;
    const onKey = (event: KeyboardEvent) => {
      // Esc **先关副窗**: 副窗开着时它是你正在看的东西, 一下把整张卡
      // 也关掉, 等于把你退回两步
      if (event.key !== "Escape") return;
      setPane((current) => { if (current === null) closeRef.current(); return null; });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [target]);

  // 落点算在 cardbits 里 —— 资料卡和群卡共用同一套翻边/夹边规则
  const place = useAnchored(dockRef, anchor, target !== null, [target?.member.name, pane, shown]);

  const page = useMemo(
    () => (target === null || history === undefined ? null : history(target.pids, shown)),
    [target, history, shown]
  );

  if (target === null) return null;

  const { member, events, rooms, pids, canOpen, pending } = target;
  // target.busy 是"他正在干活"; 上面那个 busy 是"这张卡正在存东西" —— 两回事
  const working = target.busy;
  const work = member.work ?? "";
  const live = member.state === "running" || member.state === "waiting" || member.state === "created";
  const said = page?.total ?? events;

  const open = (which: Pane) => {
    setError(null);
    // 改工作区时给他看的是**项目根**, 不是 worktree 副本的内部路径 ——
    // 他要换的是"这摊活在哪儿", 副本是系统自己的布局
    if (which === "work") setDraft(member.project !== undefined && member.project !== "" ? member.project : work);
    setPane((current) => (current === which ? null : which));
  };

  const save = async () => {
    if (busy) return;
    setBusy(true); setError(null);
    const failure = await onRebind(member.name, draft.trim());
    setBusy(false);
    if (failure === null) setPane(null);
    else setError(failure);
  };

  /** 请宿主开一个系统目录选择器. 浏览器里没有这条路 —— 那时只能敲 */
  const picker = window.neoxos?.pickFolder;
  const pick = async () => {
    if (picker === undefined) return;
    const picked = await picker(draft === "" ? work : draft);
    if (picked !== null && picked !== undefined) setDraft(picked);
  };

  const moveTo = async (room: string) => {
    if (busy) return;
    setBusy(true); setError(null);
    const failure = await onMove(member.name, room);
    setBusy(false);
    if (failure === null) setPane(null);
    else setError(failure);
  };

  return (
    /* **不压暗底下那一屏**: 这是贴着头像弹出来的一张卡, 不是一道要你
       先处理完的关卡. 这一层只做两件事: 接住外面的点击(点空白关掉),
       以及把卡托在所有东西之上 —— 客户端里任何面板都盖不住它. */
    <div className="scrim scrim--bare" ref={scrim} onMouseDown={(event) => { if (event.target === scrim.current) onClose(); }}>
      {/* 卡和副窗拼成一个左右布局, 中间留一条缝 */}
      <div
        className={`dock${place?.flip === true ? " dock--flip" : ""}`} ref={dockRef}
        /* 卡和副窗**中间那条缝**也是外面: 它看着是空白, 点下去却什么都
           不发生 —— "点空白关掉"这条规矩一旦有例外, 人就不信它了 */
        onMouseDown={(event) => { if (event.target === dockRef.current) onClose(); }}
        style={place === null
          ? { visibility: "hidden" }
          : { left: place.left, top: place.top }}
      >
        <div className="prof" role="dialog" aria-label={tt("{a} 的资料", { a: member.name })}>
          <div className="prof__head">
            <Avatar id={member.name} size={44} />
            <div className="prof__id">
              <div className="prof__name">{member.name}</div>
              <div className="prof__state">{presenceOf(member.state, pending).label}</div>
            </div>
            {/* 停: 只在他正干活时出现 —— 一颗按不动的按钮比没有更糟 */}
            {working && onStop !== undefined ? (
              <button className="prof__stopbtn" onClick={() => onStop(member.name)}
                title={t("停下这一轮，手上的改动留着")}>{t("停下")}</button>
            ) : null}
            {/* 动作在角上, 不占一整行 */}
            {canOpen ? (
              <button className="prof__x" onClick={() => onOpen(member)} aria-label={t("单独找他")} title={t("单独找他")}>
                <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
                  <path d="M3 3.8h10a1 1 0 0 1 1 1v5.2a1 1 0 0 1-1 1H7.2L4.4 13.4V11H3a1 1 0 0 1-1-1V4.8a1 1 0 0 1 1-1Z" />
                </svg>
              </button>
            ) : null}
            <button className="prof__x" onClick={onClose} aria-label={t("关掉")} title={t("关掉")}>
              <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
                <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" />
              </svg>
            </button>
          </div>

          <div className="panel">
            <div className="rw">
              <span className="rw__copy">{t("在不在")}</span>
              <span className="rw__ctl"><span className={`pill${live ? " pill--on" : ""}`}>{live ? t("在线") : t("不在")}</span></span>
            </div>
            {/* **负责什么**排在最前: 一列名字长得都差不多的 bot 里,
                这一句才是"他是谁" */}
            {member.role === undefined || member.role === "" ? null : (
              <div className="rw rw--stack">
                <span className="rw__copy">{t("负责什么")}</span>
                <span className="rw__note rw__note--role">{member.role}</span>
              </div>
            )}
            {/* 下面三行**都是入口**: 值本身就是那颗按钮, 点了在右边开一扇副窗.
                卡自己一行都不长 —— 撑大的是副窗, 不是它. */}
            <Tap label={t("说过的话")} on={pane === "log"} onTap={() => open("log")}>
              <span className="rw__note">{said} {t("条")}</span>
            </Tap>
            {/**
              * 显示**项目**, 不显示 worktree 内部路径.
              *
              *	分支隔离下它真正写的是 ~/.neox-os/worktrees/... 那份副本,
              *	但直接把那条甩出来, 用户第一反应是"这工作区不对吧?"
              *	(真机原话). 项目在哪儿才是他问的事; 副本机制一行小字说清.
              */}
            <Tap label={t("在哪儿干活")} on={pane === "work"} onTap={() => open("work")}>
              <span className="rw__path" title={work}>
                {pathParts((member.project !== undefined && member.project !== "" ? member.project : work) || t("系统分的目录"))}
              </span>
            </Tap>
            {member.project !== undefined && member.project !== "" && member.project !== work ? (
              <div className="rw rw--sub">
                <span className="rw__copy" aria-hidden="true" />
                <span className="rw__note">{t("改在它自己的分支副本里，合格才进主干")}</span>
              </div>
            ) : null}
            {/* **产物在哪条分支上**: 它在一个只属于自己的 git worktree 里干活,
                别人的改动不会出现在它脚底下. 交接就是把这条分支交出去. */}
            {member.branch === undefined || member.branch === "" ? null : (
              <Tap label={t("干出来的")} on={pane === "made"} onTap={() => open("made")}>
                <span className="rw__note rw__note--mono">{member.branch}</span>
              </Tap>
            )}
            <Tap label={t("在的房间")} on={pane === "room"} onTap={() => open("room")}>
              <span className="rw__note">{rooms.length > 0 ? roomLabels(rooms) : t("没在房间里")}</span>
            </Tap>
            <div className="rw">
              <span className="rw__copy">{t("哪个进程")}</span>
              <span className="rw__ctl">
                <span className="rw__note rw__note--mono">{member.pid}</span>
                {pids.length > 1 ? <span className="rw__note"> {t("· 起过")} {pids.length} {t("次")}</span> : null}
              </span>
            </div>
          </div>
        </div>

        {pane === null ? null : (
          <section className="pane" aria-label={PANE_TITLE[pane]}>
            <header className="pane__head">
              <span className="pane__title">{PANE_TITLE[pane]}</span>
              {pane === "log" && page !== null
                ? <span className="pane__count">
                    {page.lines.length < page.total ? tt("最近 {a} · 共 {b}", { a: page.lines.length, b: page.total }) : tt("共 {a} 条", { a: page.total })}
                  </span>
                : null}
              <button className="prof__x" onClick={() => setPane(null)} aria-label={t("收起")} title={t("收起")}>
                <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
                  <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" />
                </svg>
              </button>
            </header>

            <div className="pane__body">
              {pane === "log" ? (
                page === null || page.lines.length === 0
                  ? <div className="pick__empty">{t("还没说过话")}</div>
                  : (
                    <>
                      <ol className="log">
                        {page.lines.map((line) => (
                          <li key={line.key} className={`log__row${line.bad === true ? " log__row--bad" : ""}`}>
                            <span className="log__at">{clock(line.at)}</span>
                            <span className="log__tag">{line.tag}</span>
                            <span className="log__text">{line.text}</span>
                          </li>
                        ))}
                      </ol>
                      {page.lines.length < page.total ? (
                        <button className="w__btn pane__more" onClick={() => setShown((n) => n + PAGE)}>
                          {t("再看")} {Math.min(PAGE, page.total - page.lines.length)} {t("条")}
                        </button>
                      ) : null}
                    </>
                  )
              ) : null}

              {pane === "made" ? (
                made === undefined
                  ? <div className="pick__empty">{t("读着…")}</div>
                  : made === null || made.error !== undefined
                    ? <div className="probe probe--bad">{made?.error ?? t("读不出它的产物")}</div>
                    : (
                      <>
                        {/* **没提交的排在最前**: 那是"活干了一半"的样子 ——
                            没提交的东西对别人等于不存在, 交接一个字都传不过去 */}
                        {made.dirty.length === 0 ? null : (
                          <div className="made">
                            <div className="pane__label">{t("还没提交（")}{made.dirty.length}）</div>
                            {made.dirty.map((change) => (
                              <div key={change.path} className="made__row">
                                <span className={`made__st made__st--${statusKind(change.status)}`}>{change.status}</span>
                                <span className="made__path">{change.path}</span>
                              </div>
                            ))}
                          </div>
                        )}
                        {made.commits.length === 0
                          ? <div className="pick__empty">{t("这条分支上还没有提交")}</div>
                          : (
                            <div className="made">
                              <div className="pane__label">{t("提交（")}{made.commits.length}）</div>
                              {made.commits.map((commit) => (
                                <div key={commit.hash} className="made__row made__row--commit">
                                  <span className="made__hash">{commit.hash}</span>
                                  <span className="made__subject">{commit.subject}</span>
                                  <span className="made__stat">
                                    {commit.files} {t("个文件")}
                                    {commit.add ? <b className="made__add"> +{commit.add}</b> : null}
                                    {commit.del ? <b className="made__del"> −{commit.del}</b> : null}
                                  </span>
                                  <span className="made__at">{clock(commit.at)}</span>
                                </div>
                              ))}
                            </div>
                          )}
                      </>
                    )
              ) : null}

              {pane === "work" ? (
                <>
                  {/* **先给选, 再给敲**: 手敲一条绝对路径敲错了不会报错,
                      他会真的去那个不存在的地方开工. 所以第一顺位是系统的
                      目录选择器, 输入框留着 —— 粘一条路径进来也是常事. */}
                  <div className="rw__pickline">
                    <input
                      className="field" value={draft} autoFocus placeholder={t("选一个目录，或把路径粘进来")}
                      onChange={(event) => setDraft(event.target.value)}
                      onKeyDown={(event) => { if (event.key === "Enter") void save(); }}
                    />
                    {picker === undefined ? null : (
                      <button className="icon--field" onClick={() => void pick()} disabled={busy}
                              title={t("从系统里选一个目录")} aria-label={t("选一个目录")}>
                        <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round">
                          <path d="M2 4.2a1 1 0 0 1 1-1h3l1.4 1.6H13a1 1 0 0 1 1 1v6.4a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V4.2Z" />
                        </svg>
                      </button>
                    )}
                  </div>
                  {/* 别人正在用的那几个: 最常见的一次换地方就是"派到那摊活上" */}
                  {works.filter((path) => path !== work).length > 0 ? (
                    <div className="pane__pick">
                      <div className="pane__label">{t("别人正在用的")}</div>
                      {works.filter((path) => path !== work).map((path) => (
                        <button key={path} className="pick__row" onClick={() => setDraft(path)} title={path}>
                          <span className="pick__path">{homeShort(path)}</span>
                        </button>
                      ))}
                    </div>
                  ) : null}
                  <div className="rw__foot">
                    <span className="sheet__hint">{t("换地方 = 重起一个进程，历史跟着他走。")}</span>
                    <button className="w__btn w__btn--primary" onClick={() => void save()} disabled={busy || draft.trim() === "" || draft.trim() === work}>
                      {busy ? t("换着…") : t("换过去")}
                    </button>
                  </div>
                </>
              ) : null}

              {pane === "room" ? (
                <>
                  <div className="pane__pick">
                    {allRooms.map((room) => {
                      const here = rooms.includes(room);
                      return (
                        <button key={room} className="pick__row" disabled={busy || here} onClick={() => void moveTo(room)}>
                          <span>{roomLabel(room)}</span>
                          {here ? <span className="pick__note">{t("就在这儿")}</span> : null}
                        </button>
                      );
                    })}
                    {rooms.length > 0 ? (
                      <button className="pick__row" disabled={busy} onClick={() => void moveTo("")}>
                        <span>{t("单独待着")}</span>
                        <span className="pick__out">{t("从房间里出来")}</span>
                      </button>
                    ) : null}
                    {allRooms.length === 0 && rooms.length === 0 ? <div className="pick__empty">{t("还没有房间")}</div> : null}
                  </div>
                  <div className="rw__foot">
                    <span className="sheet__hint">{t("换房间 = 重起他的进程，历史跟着他走。")}</span>
                  </div>
                </>
              ) : null}

              {error !== null ? <div className="sheet__error sheet__error--inline">{error}</div> : null}
            </div>
          </section>
        )}
      </div>
    </div>
  );
}

const PANE_TITLE: Record<Pane, string> = {
  log: t("说过的话"), work: t("换个地方"), room: t("换个房间"), made: t("干出来的")
};

/** git 的状态字母翻成"这是好事还是坏事" —— 删掉的那种要看得出来 */
function statusKind(status: string): string {
  if (status.startsWith("D")) return "del";
  if (status.startsWith("??") || status.startsWith("A")) return "add";
  return "mod";
}
