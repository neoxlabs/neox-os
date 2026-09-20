import { t, tt } from "../i18n/index.js";
import { useEffect, useRef, useState } from "react";
import { randomBotName } from "./botname.js";
import { roomLabel } from "./roomname.js";

import { workForRoom } from "./roomwork.js";

/**
 * NewBot — 新建一个 bot.
 *
 *   界面只填**身份和地盘**: 名字、放进哪个房间、在哪儿干活.
 *   **不填能力** —— 能给什么由 OS 那边的策略决定, 界面能填 caps 的话
 *   授权模型就被绕过去了. 不够用的时候它会自己发审批卡来要.
 */
/**
 * 一格点一下就填进"负责什么" —— 常见岗位, 不用每次都想措辞.
 *
 *	前端那格**自带样式基底的指引**: "默认出来的 UI 太丑"的根源是模型
 *	每个 demo 现编一套设计系统 —— 岗位说明走 persona, 不占全局提示词
 *	预算, 是给单个 bot 塞规矩最便宜的通道.
 */
const ROLE_CHIPS: readonly { label: string; role: string }[] = [
  { label: t("前端页面"), role: t("前端页面。样式基底用 ~/.neox-os/kit/neox-kit.css：拷进项目引入、用 nx-* 类、改主题只动 --nx-accent，别自己发明设计系统") },
  { label: t("后端接口"), role: t("后端接口") },
  { label: t("测试与回归"), role: t("测试与回归") },
  { label: t("文档与文案"), role: t("文档与文案") },
  { label: t("盯 CI 和线上"), role: t("盯 CI 和线上") }
] as const;

export function NewBot({ open, onClose, onCreate, rooms, works = [], taken = [], initialWork = "", project = false }: {
  open: boolean;
  onClose(): void;
  onCreate(name: string, thread: string, work: string, role: string): Promise<string | null>;
  /** 房间, 以及这个房间的人在哪儿干活 —— 选了房间工作区要跟着走 */
  rooms: readonly { readonly title: string; readonly work?: string }[];
  /** 已有项目的目录 —— 工作区一格的下拉候选, 不用整条路径重敲一遍 */
  works?: readonly string[];
  /** 已经叫掉的名字 —— 随机起名不能撞车 */
  taken?: readonly string[];
  /** 预填的工作区 —— 从项目卡"拉个人进来"进来时带着项目路径 */
  initialWork?: string;
  /**
   * 以"新建项目"的身份打开.
   *
   *	项目没有独立实体: 它 = 一个目录 + 在里面干活的人.
   *	表单只填**项目名** —— 第一个人的名字随机给(可以事后改),
   *	跟别的项目没有任何关系, 所以也不给已有目录的下拉.
   */
  project?: boolean;
}) {
  const [name, setName] = useState("");
  const [thread, setThread] = useState("");
  const [work, setWork] = useState("");
  /** 上一次**我们自动填进去**的工作区 —— 用户自己敲过的不能覆盖 */
  const filled = useRef("");
  const [role, setRole] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const input = useRef<HTMLInputElement | null>(null);

  /**
   * 选房间 —— **工作区跟着走**.
   *
   *	同一个房间的人必须在同一个工作区, 否则他在房间里却碰不到这摊活.
   *	见 roomwork.ts.
   */
  const pickRoom = (title: string) => {
    setThread(title);
    const next = workForRoom({
      current: work, filled: filled.current,
      roomWork: rooms.find((r) => r.title === title)?.work
    });
    if (next === null) return;
    setWork(next.work);
    filled.current = next.filled;
  };
  const scrim = useRef<HTMLDivElement | null>(null);

  /**
   * 打开时清一次表单.
   *
   *   **只能依赖 open**. 早先这里还依赖了 onClose —— 而 onClose 是父组件
   *   内联写的箭头函数, 每次父组件渲染身份都变, 于是这个 effect 每次都重跑,
   *   把用户刚敲进去的字清空. 症状是"输入框打不进字", 看着像输入法或
   *   受控组件的问题, 其实是依赖数组多写了一个函数.
   */
  useEffect(() => {
    if (!open) return;
    setName(""); setThread(""); setWork(initialWork); setRole(""); setError(null); setBusy(false);
    filled.current = initialWork;
    const timer = setTimeout(() => input.current?.focus(), 40);
    return () => clearTimeout(timer);
  }, [open, initialWork]);

  // Esc 关闭: 回调放 ref, 免得它的身份变化牵动上面那个 effect
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!open) return;
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") closeRef.current(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  /**
   * 工作区候选: 自绘下拉, 不用 datalist ——
   * 原生那个长什么样、弹在哪儿由系统定, 跟这套界面完全不是一副皮.
   *
   * **钩子必须在 `if (!open) return null` 之前**: 放在早退后面,
   * 开对话框那一刻钩子数量就变了 —— React #310, 整棵树当场黑屏.
   * 因此钩子数量必须在对话框开关状态变化时保持一致.
   */
  const [workOpen, setWorkOpen] = useState(false);
  const workBox = useRef<HTMLSpanElement | null>(null);
  useEffect(() => {
    if (!workOpen) return;
    const away = (event: MouseEvent) => {
      if (workBox.current !== null && !workBox.current.contains(event.target as Node)) setWorkOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [workOpen]);

  if (!open) return null;

  /**
   * 只写了个名字(没有 / )就当成 ~/AI/ 底下的项目 ——
   * 项目名是常见输入, 而 OS 只收绝对路径或 ~/ 开头.
   * 与其报错教育人, 不如按他显然的意思办, 并把会建在哪当场写出来.
   */
  const resolveWork = (raw: string): string => {
    const t = raw.trim();
    return t === "" || t.includes("/") ? t : `~/AI/${t}`;
  };

  const submit = async () => {
    if (busy) return;
    // **点了要有话**: 缺什么当场说, 不靠灰按钮让人猜
    if (project && work.trim().length === 0) { setError(t("先给项目起个名（或填个目录）")); return; }
    // 名字不强求 —— 空着就随机给一个, 事后随时能改
    const finalName = name.trim() !== "" ? name.trim() : randomBotName(new Set(taken));
    setBusy(true); setError(null);
    const failure = await onCreate(finalName, project ? "" : thread, resolveWork(work), role.trim());
    setBusy(false);
    if (failure === null) onClose();
    else setError(failure);
  };

  /** Electron 里有系统目录选择器就摆出来 —— 手敲整条路径是个陷阱 */
  const pickable = window.neoxos?.pickFolder !== undefined;
  const pickDir = async () => {
    const got = await window.neoxos?.pickFolder?.(work || undefined);
    if (got !== null && got !== undefined) { setWork(got); filled.current = ""; setWorkOpen(false); }
  };

  const workHits = works.filter((one) => work.trim() === "" || one.toLowerCase().includes(work.trim().toLowerCase()));

  return (
    /**
     * **点空白不关这扇窗**: 表单里有敲了一半的字, 手一滑点到边上
     * 就全扔掉太狠, 也会让未完成的输入在无明确意图时丢失.
     * 关它的路只有三条, 每条都是明确的意图: × / 取消 / Esc.
     */
    <div className="scrim" ref={scrim}>
      <div className="sheet sheet--narrow" role="dialog" aria-label={project ? t("新建项目") : t("新建 bot")}>
        <header className="sheet__head"><span>{project ? t("新建项目") : t("新建 bot")}</span><button className="sheet__x" onClick={onClose} aria-label={t("关闭")}>×</button></header>

        {/* 一列对齐的行: 标签在左、控件在右 —— 眼睛只扫一条竖线 */}
        <div className="frm">
          {/* 项目模式第一格是**项目名**: 这是他脑子里的第一件事.
              第一个人随机起名(可事后改), 不在这儿问 */}
          {project ? (
            <label className="frm__row">
              <span className="frm__key">{t("项目名")}</span>
              <input
                ref={input} className="field frm__val" value={work} placeholder={t("OA 项目（或填一条目录路径）")}
                onChange={(event) => { setWork(event.target.value); filled.current = ""; }}
                onKeyDown={(event) => { if (event.key === "Enter") void submit(); }}
              />
            </label>
          ) : (
            <label className="frm__row">
              <span className="frm__key">{t("叫什么")}</span>
              <input
                ref={input} className="field frm__val" value={name} maxLength={24} placeholder={t("不填就随机起一个")}
                onChange={(event) => setName(event.target.value)}
                onKeyDown={(event) => { if (event.key === "Enter") void submit(); }}
              />
            </label>
          )}

          {/* 岗位是"他是谁": 上线就照这句话干. 常见的点一下就填 */}
          <label className="frm__row">
            <span className="frm__key">{t("负责什么")}</span>
            <input
              className="field frm__val" value={role} placeholder={t("不填就是通用助手")}
              onChange={(event) => setRole(event.target.value)}
              onKeyDown={(event) => { if (event.key === "Enter") void submit(); }}
            />
          </label>
          <div className="frm__row">
            <span className="frm__key" aria-hidden="true" />
            <span className="frm__chips">
              {ROLE_CHIPS.map((chip) => (
                <button key={chip.label} type="button" className={`chip${role === chip.role ? " chip--on" : ""}`}
                  onClick={() => setRole(role === chip.role ? "" : chip.role)}>{chip.label}</button>
              ))}
            </span>
          </div>

          {!project && rooms.length > 0 ? (
            <div className="frm__row">
              <span className="frm__key">{t("房间")}</span>
              <div className="seg frm__val">
                <button className={`seg__btn${thread === "" ? " seg__btn--on" : ""}`} onClick={() => pickRoom("")}>{t("不放")}</button>
                {rooms.map((room) => (
                  <button key={room.title} className={`seg__btn${thread === room.title ? " seg__btn--on" : ""}`} onClick={() => pickRoom(room.title)}>{roomLabel(room.title)}</button>
                ))}
              </div>
            </div>
          ) : null}

          {/* 工作区只在"加 bot"里出现 —— 新项目跟别的项目没关系,
              目录就是 ~/AI/项目名, 不给下拉不给选择器 */}
          {project ? null : (
            <div className="frm__row">
              <span className="frm__key">{t("工作区")}</span>
              <span className="frm__val frm__pick" ref={workBox}>
                <input
                  className="field" value={work}
                  placeholder={t("不填就分一个它自己的目录")}
                  onFocus={() => { if (works.length > 0) setWorkOpen(true); }}
                  onChange={(event) => { setWork(event.target.value); filled.current = ""; if (works.length > 0) setWorkOpen(true); }}
                  onKeyDown={(event) => {
                    if (event.key === "Escape" && workOpen) { event.stopPropagation(); setWorkOpen(false); return; }
                    if (event.key === "Enter") { setWorkOpen(false); void submit(); }
                  }}
                />
                {works.length === 0 ? null : (
                  <button type="button" className="frm__dir" title={t("已有的项目")} aria-label={t("已有的项目")}
                    onClick={() => setWorkOpen((now) => !now)}>
                    <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                      <path d="M4 6.5l4 4 4-4" />
                    </svg>
                  </button>
                )}
                {pickable ? (
                  <button type="button" className="frm__dir" title={t("选个目录")} aria-label={t("选个目录")} onClick={() => void pickDir()}>
                    <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" aria-hidden="true">
                      <path d="M1.8 4.2a1.2 1.2 0 0 1 1.2-1.2h3.2l1.5 1.8h5.3a1.2 1.2 0 0 1 1.2 1.2v6.2a1.2 1.2 0 0 1-1.2 1.2H3a1.2 1.2 0 0 1-1.2-1.2z" />
                    </svg>
                  </button>
                ) : null}
                {/* 自绘下拉: 短名加粗, 整条路径淡一号 —— 认路看短名, 分辨靠路径 */}
                {workOpen && workHits.length > 0 ? (
                  <div className="drop" role="listbox">
                    {workHits.map((one) => (
                      <button key={one} type="button" className="drop__item" role="option"
                        onMouseDown={(event) => { event.preventDefault(); setWork(one); filled.current = ""; setWorkOpen(false); }}>
                        <b>{one.split("/").pop()}</b>
                        <small>{one}</small>
                      </button>
                    ))}
                  </div>
                ) : null}
              </span>
            </div>
          )}

          {/* 这条路径会原样变成内核给它的写能力 —— 说在脸上.
              项目名/短名时把补全后的落点当场写出来, 别让人猜 */}
          <div className="frm__row">
            <span className="frm__key" aria-hidden="true" />
            <span className="frm__hint">
              {work.trim() !== "" && !work.includes("/")
                ? tt("会建在 ~/AI/{a} —— 里面的人只能改这个目录。", { a: work.trim() })
                : project
                  ? t("第一个成员会随机起名，之后随时能改、随时能再拉人。")
                  : t("它只能改工作区里的东西，别处一个字节都动不了。")}
            </span>
          </div>
        </div>

        {error !== null ? <div className="sheet__error">{error}</div> : null}

        <footer className="sheet__foot">
          <button className="w__btn" onClick={onClose}>{t("取消")}</button>
          {/* 不靠灰按钮让人猜缺什么 —— 点了会说话(见 submit) */}
          <button className="w__btn w__btn--primary" onClick={() => void submit()} disabled={busy}>
            {busy ? t("起着…") : project ? t("建项目") : t("起一个")}
          </button>
        </footer>
      </div>
    </div>
  );
}
