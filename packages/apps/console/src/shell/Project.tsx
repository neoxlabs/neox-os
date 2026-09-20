import { t, tt } from "../i18n/index.js";
import { useEffect, useMemo, useRef, useState } from "react";
import { Avatar } from "../view/Avatar.js";
import { Glyph, glyphOf } from "../view/glyphs.js";
import { clock, pathParts, useAnchored } from "./cardbits.js";
import type { ProjectReport } from "../os/source-live.js";

/**
 * Project —— **这个项目现在什么状态**.
 *
 * ── 为什么非要有这一页 ──
 *
 *	三个 bot 并行工作一晚上后, 若状态只按 bot 展示, 了解项目进展就得
 *	逐个点头像拼凑信息. 项目才是工作的单位, 因此状态也按项目汇总.
 *
 *	这一页只答四个问题, 一屏之内:
 *	  主干现在是什么样(谁最后改的、改了什么)
 *	  谁手上还压着没合的活          ← **最要紧的一格**
 *	  最近合进去的是哪几条
 *	  合错了怎么退回去
 *
 *	"谁手上还压着没合的"要紧, 是因为一条干完却没合上去的分支, 在别的
 *	地方看跟"什么都没干"长得一模一样 —— 那是最容易丢活的地方.
 */
export function Project({ name, path, report, onClose, onWho, onRevert, anchor, onHire, onRename, onDisband, bots }: {
  name: string;
  path: string;
  /** undefined = 还在问; null = 问不出来 */
  report: ProjectReport | null | undefined;
  onClose(): void;
  onWho(who: string, anchor: DOMRect): void;
  /** 撤一条. 返回回执 */
  onRevert(hash: string): Promise<{ ok: boolean; message: string }>;
  anchor?: DOMRect | null;
  /** 往这个项目里拉一个人 —— 像群里加人: 点一下就进来, 名字随机 */
  onHire?(): Promise<void> | void;
  /** 改显示名. 空 = 回到目录名. 返回错误原因, null = 成了 */
  onRename?(next: string): Promise<string | null>;
  /** 解散: 项目里的 bot 全删, **目录和代码留在磁盘上** */
  onDisband?(): Promise<void>;
  /** 项目里有几段会话 —— 解散确认时要说清删的是几个 */
  bots?: number;
}) {
  const dockRef = useRef<HTMLDivElement | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [said, setSaid] = useState<{ ok: boolean; text: string } | null>(null);
  /** 撤之前再问一次 —— 这是唯一一个会改所有人主干的按钮 */
  const [asking, setAsking] = useState<string | null>(null);
  /** 改名中: null = 没在改 */
  const [renaming, setRenaming] = useState<string | null>(null);
  /** 解散要再问一次 —— 它删的是人和对话, 虽然目录留着 */
  const [askDisband, setAskDisband] = useState(false);
  const [disbanding, setDisbanding] = useState(false);
  /** 拉人中 —— 点两下不能进来俩 */
  const [hiring, setHiring] = useState(false);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => { setSaid(null); setAsking(null); setRenaming(null); setAskDisband(false); }, [path]);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setAsking((current) => { if (current === null) closeRef.current(); return null; });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const place = useAnchored(dockRef, anchor, true, [path, report, said, asking]);
  const glyph = useMemo(() => glyphOf(name, ["tool"]), [name]);
  const waiting = (report?.members ?? []).filter((one) => one.unmerged > 0 || one.dirty > 0);

  const take = async (hash: string) => {
    setBusy(hash); setAsking(null);
    const got = await onRevert(hash);
    setBusy(null);
    setSaid({ ok: got.ok, text: got.message });
  };

  return (
    <div className="scrim scrim--bare" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="dock" ref={dockRef}
        onMouseDown={(event) => { if (event.target === dockRef.current) onClose(); }}
        style={place === null ? { visibility: "hidden" } : { left: place.left, top: place.top }}>
        <div className="prof proj" role="dialog" aria-label={tt("{a} 这个项目", { a: name })}>
          <div className="prof__head">
            <span className="proj__glyph"><Glyph glyph={glyph} size={20} /></span>
            <div className="prof__id">
              {renaming === null ? (
                <div className="prof__name">
                  {name}
                  {onRename === undefined ? null : (
                    <button className="proj__edit" title={t("改名（只改显示，目录不动）")} aria-label={t("改名")}
                      onClick={() => setRenaming(name)}>
                      <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor"
                        strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <path d="M11.2 2.3l2.5 2.5L5.5 13 2.5 13.5 3 10.5z" />
                      </svg>
                    </button>
                  )}
                </div>
              ) : (
                <span className="proj__renaming">
                  <input className="field field--inline" value={renaming} maxLength={24} autoFocus
                    onChange={(event) => setRenaming(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Escape") setRenaming(null);
                      if (event.key !== "Enter" || onRename === undefined) return;
                      void onRename(renaming.trim()).then((err) => {
                        if (err === null) setRenaming(null);
                        else setSaid({ ok: false, text: err });
                      });
                    }} />
                </span>
              )}
              <div className="prof__state">
                {report === undefined ? t("看着…")
                  : report === null || report.error !== undefined ? (report?.error ?? t("读不出状态"))
                    : tt("{a} 人 · {b} 个文件 · 主干 {c}", { a: report.members.length, b: report.files, c: report.main })}
              </div>
            </div>
            <button className="prof__x" onClick={onClose} aria-label={t("关掉")} title={t("关掉")}>
              <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
                <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" />
              </svg>
            </button>
          </div>

          <div className="rw rw--stack">
            <span className="rw__copy">{t("这摊活在哪儿")}</span>
            <span className="rw__path">{pathParts(path)}</span>
          </div>

          {report === undefined || report === null ? null : (
            <>
              {/* ① 主干现在是什么样 */}
              {report.head === undefined ? null : (
                <div className="proj__sec">
                  <div className="proj__label">{t("主干最近")}</div>
                  <div className="proj__head">
                    <Avatar id={report.head.who} size={22} />
                    <span className="proj__subject">{report.head.subject}</span>
                    <span className="proj__at">{clock(report.head.at * 1000)}</span>
                  </div>
                </div>
              )}

              {/* ② 谁手上还压着没合的 —— 这一格空着才是好消息 */}
              <div className="proj__sec">
                <div className="proj__label">
                  {t("手上还有活")}
                  {waiting.length === 0 ? <span className="proj__clean">{t("都合上了")}</span> : null}
                </div>
                {waiting.map((one) => (
                  <button key={one.bot} className="proj__row"
                    onClick={(event) => onWho(one.bot, event.currentTarget.getBoundingClientRect())}>
                    <Avatar id={one.bot} size={22} />
                    <span className="proj__who">{one.bot}</span>
                    {one.unmerged > 0 ? <span className="proj__tag proj__tag--wait">{one.unmerged} {t("条没合")}</span> : null}
                    {/* 没提交的比没合的更早一步: 那些东西对别人**等于不存在** */}
                    {one.dirty > 0 ? <span className="proj__tag">{one.dirty} {t("个文件没提交")}</span> : null}
                  </button>
                ))}
              </div>

              {/* ③ 交接: 这块活现在归谁 —— 三天后没人记得清是哪句话里说的 */}
              {(report.handoffs ?? []).length === 0 ? null : (
                <div className="proj__sec">
                  <div className="proj__label">{t("交接过")}</div>
                  {(report.handoffs ?? []).slice(0, 4).map((one) => (
                    <div key={`${one.from}-${one.to}-${one.at}`} className="proj__row proj__row--flat">
                      <Avatar id={one.from} size={20} />
                      <span className="proj__who">{one.from}</span>
                      <span className="proj__arrow">→</span>
                      <Avatar id={one.to} size={20} />
                      <span className="proj__who">{one.to}</span>
                      <span className="proj__subject">{one.files > 0 ? tt("{a} 个文件", { a: one.files }) : ""}</span>
                      <span className="proj__at">{clock(one.at)}</span>
                    </div>
                  ))}
                </div>
              )}

              {/* ④ 最近合进去的, 每一条都能撤 */}
              <div className="proj__sec">
                <div className="proj__label">{t("主干上最近这几条")}</div>
                <div className="proj__log">
                  {report.merges.slice(0, 8).map((one) => (
                    <div key={one.hash} className={`proj__commit${asking === one.hash ? " proj__commit--asking" : ""}`}>
                      <span className="proj__hash">{one.hash.slice(0, 7)}</span>
                      <span className="proj__subject">{one.subject}</span>
                      <span className="proj__by">{one.who}</span>
                      {asking === one.hash ? (
                        <span className="proj__ask">
                          {/* **这是唯一会改所有人主干的按钮**, 所以要再问一次 */}
                          <button className="w__btn w__btn--danger" disabled={busy !== null}
                            onClick={() => void take(one.hash)}>{busy === one.hash ? t("撤着…") : t("确认撤掉")}</button>
                          <button className="w__btn" onClick={() => setAsking(null)}>{t("算了")}</button>
                        </span>
                      ) : (
                        <button className="proj__undo" title={t("撤掉这一条")} aria-label={t("撤掉这一条")}
                          onClick={() => { setSaid(null); setAsking(one.hash); }}>
                          <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor"
                            strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                            <path d="M3 7.4h7a3 3 0 0 1 0 6H6" /><path d="M5.6 4.6 3 7.4l2.6 2.8" />
                          </svg>
                        </button>
                      )}
                    </div>
                  ))}
                </div>
              </div>

              {said === null ? null : (
                <div className={`probe ${said.ok ? "probe--ok" : "probe--bad"} proj__said`}>{said.text}</div>
              )}
              {report.dirty === 0 ? null : (
                /* 项目根上的改动是**你自己**的: bot 都在各自的 worktree 里, 碰不到这儿 */
                <div className="proj__mine">{t("主干目录里有")} {report.dirty} {t("个你自己没提交的改动")}</div>
              )}
            </>
          )}

          {/* 项目上的动作: 拉人 / 解散. 改名在名字旁边那支笔上 */}
          {onHire === undefined && onDisband === undefined ? null : (
            <div className="proj__actions">
              {onHire === undefined ? null : (
                <button className="w__btn w__btn--primary" disabled={hiring}
                  onClick={() => { setHiring(true); void Promise.resolve(onHire()).finally(() => setHiring(false)); }}>
                  {hiring ? t("拉着…") : t("拉个人进来")}
                </button>
              )}
              <span className="proj__grow" />
              {onDisband === undefined ? null : askDisband ? (
                <span className="proj__ask">
                  {/* 删的是人和对话 —— 目录留着这件事要说在按钮上 */}
                  <button className="w__btn w__btn--danger" disabled={disbanding}
                    onClick={() => { setDisbanding(true); void onDisband().finally(() => setDisbanding(false)); }}>
                    {disbanding ? t("散着…") : tt("删掉 {a} 段会话，目录留着", { a: bots ?? 0 })}
                  </button>
                  <button className="w__btn" onClick={() => setAskDisband(false)}>{t("算了")}</button>
                </span>
              ) : (
                <button className="w__btn" onClick={() => setAskDisband(true)}>{t("解散项目")}</button>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
