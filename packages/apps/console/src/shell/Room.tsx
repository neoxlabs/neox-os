import { t, tt } from "../i18n/index.js";
import { useEffect, useMemo, useRef, useState } from "react";
import { Avatar, GroupAvatar } from "../view/Avatar.js";
import { presenceOf } from "../view/presence.js";
import { Tap, clock, pathParts, useAnchored } from "./cardbits.js";
import { roomFacts, roomPulse } from "./roomcard.js";
import { roomLabel } from "./roomname.js";
import type { Member } from "./parties.js";
import type { HistoryPage } from "./Profile.js";

/**
 * Room —— **点一个群, 看这摊活是怎么回事**.
 *
 *   点一个 bot 的头像早就能看到他是谁; 点一个群什么都没有 —— 而群恰恰
 *   信息更多: 几个人、在不在同一摊活上、各自压着哪条分支.
 *
 *   形状照资料卡: 贴着头像弹、点空白关掉、右边能拉出一扇副窗.
 *   内容不照 —— IM 里那张卡的下半截是"消息免打扰/置顶/清空聊天记录",
 *   这里的群是个**项目组**, 那几栏换成这摊活自己的事实.
 *
 *   头像墙排在最前, 而且**每张脸都能点**: 群里最常问的一句就是
 *   "这个谁来着", 而答案在他自己那张资料卡上, 不该在这里再抄一遍.
 */

/** 副窗里现在开着哪一件事 */
type Pane = "log" | "branch";

const PANE_TITLE: Record<Pane, string> = { log: t("屋里说过的话"), branch: t("各自的分支") };
const PAGE = 60;

export interface RoomTarget {
  readonly title: string;
  readonly people: readonly Member[];
  /** 屋里所有进程 —— 历史按它取 */
  readonly pids: readonly string[];
  readonly events: number;
}

export function Room({ target, onClose, onWho, onInvite, anchor, history, onStop, owner }: {
  target: RoomTarget | null;
  onClose(): void;
  /** 点了墙上某张脸 —— 交给上面去开他的资料卡, 位置也一并给 */
  onWho(name: string, anchor: DOMRect): void;
  onInvite(room: string): void;
  /** 停下屋里正在干活的那几个 —— 三条命令一起跑飞时, 一颗一颗按太慢 */
  onStop?(names: readonly string[]): void;
  anchor?: DOMRect | null;
  history?(pids: readonly string[], limit: number): HistoryPage;
  /** 群主 —— 开这个房间的那个 bot. 交办链的头也是它 */
  owner?: string;
}) {
  const dockRef = useRef<HTMLDivElement | null>(null);
  const [pane, setPane] = useState<Pane | null>(null);
  const [shown, setShown] = useState(PAGE);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => { setPane(null); setShown(PAGE); }, [target?.title]);

  useEffect(() => {
    if (target === null) return;
    const onKey = (event: KeyboardEvent) => {
      // Esc **先关副窗** —— 跟资料卡一个规矩, 不然等于把人退回两步
      if (event.key !== "Escape") return;
      setPane((current) => { if (current === null) closeRef.current(); return null; });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [target]);

  const place = useAnchored(dockRef, anchor, target !== null, [target?.title, pane, shown]);
  const facts = useMemo(() => roomFacts(target?.people ?? []), [target]);
  const page = useMemo(
    () => (target === null || history === undefined ? null : history(target.pids, shown)),
    [target, history, shown]
  );

  if (target === null) return null;
  const { title, people, events } = target;
  const said = page?.total ?? events;

  return (
    <div className="scrim scrim--bare" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div
        className={`dock${place?.flip === true ? " dock--flip" : ""}`} ref={dockRef}
        onMouseDown={(event) => { if (event.target === dockRef.current) onClose(); }}
        style={place === null ? { visibility: "hidden" } : { left: place.left, top: place.top }}
      >
        <div className="prof" role="dialog" aria-label={tt("{a} 的资料", { a: roomLabel(title) })}>
          <div className="prof__head">
            <GroupAvatar ids={people.map((who) => who.name)} size={44} />
            <div className="prof__id">
              <div className="prof__name">{roomLabel(title)}</div>
              <div className="prof__state">{people.length} {t("人 ·")} {roomPulse(facts, people.length)}</div>
            </div>
            {/* 屋里有人在干活才给 —— 按不动的按钮比没有更糟 */}
            {facts.busy > 0 && onStop !== undefined ? (
              <button className="prof__stopbtn"
                onClick={() => onStop(people.filter((who) => who.state === "running").map((who) => who.name))}
                title={t("停下屋里正在跑的那几轮，手上的改动留着")}>
                {facts.busy > 1 ? tt("全停 {a}", { a: facts.busy }) : t("停下")}
              </button>
            ) : null}
            <button className="prof__x" onClick={onClose} aria-label={t("关掉")} title={t("关掉")}>
              <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
                <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" />
              </svg>
            </button>
          </div>

          {/* 头像墙: **每张脸都能点** —— "这个谁来着"的答案在他自己那张卡上 */}
          <div className="wall">
            {people.map((who) => (
              <button key={who.name} className="wall__one"
                      onClick={(event) => onWho(who.name, event.currentTarget.getBoundingClientRect())}
                      title={presenceOf(who.state, false).label}>
                <Avatar id={who.name} size={38} liveness={presenceOf(who.state, false).kind} />
                <span className="wall__name">
                  {who.name}
                  {/* 群主 = 开这个房间的那个 —— 交办链的头, 一屋子人里得认得出 */}
                  {owner === who.name ? <span className="wall__owner">{t("群主")}</span> : null}
                </span>
              </button>
            ))}
            <button className="wall__one wall__add" onClick={() => onInvite(title)} title={t("拉人进来")} aria-label={t("拉人进来")}>
              <span className="wall__plus">＋</span>
              <span className="wall__name">{t("拉人")}</span>
            </button>
          </div>

          <div className="panel">
            {/* **各干各的要说出来**: 拉人时忘了给工作区, 他就在自己的空目录里,
                在群里却碰不到这摊活的一个字节 —— 拿第一个人的路径糊弄过去
                正好把这个毛病藏了 */}
            <div className="rw rw--stack">
              <span className="rw__copy">{t("这摊活在哪儿")}</span>
              {facts.split
                ? <span className="rw__note rw__note--role">{t("各干各的 —— 屋里的人不在同一个工作区，他们改不到彼此的东西")}</span>
                : <span className="rw__path">{facts.work === undefined ? t("还没派工作区") : pathParts(facts.work)}</span>}
            </div>
            <Tap label={t("屋里说过的话")} on={pane === "log"} onTap={() => setPane((c) => (c === "log" ? null : "log"))}>
              <span className="rw__note">{said} {t("条")}</span>
            </Tap>
            {facts.branches.length === 0 ? null : (
              <Tap label={t("各自的分支")} on={pane === "branch"} onTap={() => setPane((c) => (c === "branch" ? null : "branch"))}>
                <span className="rw__note">{facts.branches.length} {t("条")}</span>
              </Tap>
            )}
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
                  ? <div className="pick__empty">{t("这屋里还没人说过话")}</div>
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

              {/* 一人一条分支: 那是"谁的产物归谁"的全部依据, 交接就是把它交出去 */}
              {pane === "branch" ? (
                <div className="made">
                  {facts.branches.map((one) => (
                    <button key={one.name} className="made__row made__row--tap"
                            onClick={(event) => onWho(one.name, event.currentTarget.getBoundingClientRect())}>
                      <Avatar id={one.name} size={20} />
                      <span className="made__subject">{one.name}</span>
                      <span className="made__hash">{one.branch}</span>
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
          </section>
        )}
      </div>
    </div>
  );
}
