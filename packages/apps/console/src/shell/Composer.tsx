import { t, tt } from "../i18n/index.js";
import { useEffect, useMemo, useRef, useState } from "react";
import { filesFrom, readAny, type Shot } from "./shots.js";

/**
 * Composer — 输入框.
 *
 *   结构照 Grok Bot: **按钮在壳里面, 不在旁边**. 上一行文本, 下一行动作,
 *   发送是 30×30 圆钮. 壳长高时里面自然跟着, 没有"对齐"这回事.
 *
 *   @ 只在房间里出现: 一对一时对面只有一个人, 提一个人是废话.
 *
 *   ── 图 ──
 *
 *   三条路都通: 粘贴、拖进来、点那个 +. 一张截图从剪贴板到发出去
 *   应该是 Cmd+V 加回车, 中间不该有"选择文件"这一步.
 */
export function Composer({ onSend, disabled = false, mentions = [] }: {
  onSend(text: string, shots: readonly Shot[]): void;
  disabled?: boolean;
  mentions?: readonly string[];
}) {
  const [draft, setDraft] = useState("");
  /** 待发的图. 发出去之前屏幕上就该看得见 —— 不然人不知道粘上没有 */
  const [shots, setShots] = useState<Shot[]>([]);
  /** 没收下的那些的原因 —— 静默丢是最糟的处理 */
  const [refused, setRefused] = useState<string[]>([]);
  const [over, setOver] = useState(false);
  const fileRef = useRef<HTMLInputElement | null>(null);
  const [pickAt, setPickAt] = useState<number | null>(null);
  const [cursor, setCursor] = useState(0);
  const ref = useRef<HTMLTextAreaElement | null>(null);
  /** 那个 + 的悬浮菜单 —— 附件类动作都从这儿走, 以后加功能只加一行 */
  const [plusOpen, setPlusOpen] = useState(false);
  const plusRef = useRef<HTMLSpanElement | null>(null);
  /** 任意文件的选择器 —— 跟图片分开: accept 不同, 打开的也不是同一个对话框 */
  const anyRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!plusOpen) return;
    // 点到菜单外面就收起 —— 悬浮的东西不能靠用户"再点一次加号"才走
    const away = (event: MouseEvent) => {
      if (plusRef.current !== null && !plusRef.current.contains(event.target as Node)) setPlusOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [plusOpen]);

  /**
   * 自适应高度交给 CSS(field-sizing: content), **不用 JS 测量**.
   *
   *	JS 若"先设 0px 再读 scrollHeight", 在 0px 的瞬间,
   *	输入壳矮一截、时间线高一截, 浏览器当场把 scrollTop 钳到新上限;
   *	高度还原后 scrollTop 回不来, 每次击键时间线就往下沉 22px.
   *	打字因此会带动时间线下沉, 干扰阅读位置.
   *	CSS 原生自适应只有一次布局, 不经过高度归零的瞬时状态, 避免触发钳位.
   */

  /** @ 之后打的字用来过滤候选 */
  const query = pickAt === null ? "" : draft.slice(pickAt + 1);
  const candidates = useMemo(
    () => mentions.filter((name) => query.length === 0 || name.includes(query)),
    [mentions, query]
  );
  const picking = pickAt !== null && candidates.length > 0;

  const take = async (files: readonly File[]) => {
    if (files.length === 0) return;
    // 图和文件都收 —— 拖一个 zip 进来不再被"不是图片"挡回去
    const [ok, bad] = await readAny(files);
    if (ok.length > 0) setShots((have) => [...have, ...ok]);
    setRefused(bad);
  };

  const drop = (key: string) => setShots((have) => {
    const gone = have.find((one) => one.key === key);
    // 预览用的 blob: URL 要还回去, 不然这一张的字节一直留在内存里
    if (gone !== undefined && gone.url !== "") URL.revokeObjectURL(gone.url);
    return have.filter((one) => one.key !== key);
  });

  const send = () => {
    const text = draft.trim();
    // **光发图也算一句话**: 一张报错截图本身就是全部内容
    if ((text.length === 0 && shots.length === 0) || disabled) return;
    setDraft(""); setPickAt(null); setShots([]); setRefused([]);
    onSend(text, shots);
  };

  const choose = (name: string) => {
    if (pickAt === null) return;
    const next = `${draft.slice(0, pickAt)}@${name} `;
    setDraft(next); setPickAt(null);
    requestAnimationFrame(() => { ref.current?.focus(); });
  };

  return (
    <div className={`composer${over ? " composer--over" : ""}`}
      onDragOver={(event) => { if (event.dataTransfer.types.includes("Files")) { event.preventDefault(); setOver(true); } }}
      onDragLeave={(event) => { if (event.currentTarget === event.target) setOver(false); }}
      onDrop={(event) => {
        if (!event.dataTransfer.types.includes("Files")) return;
        event.preventDefault(); setOver(false);
        void take(filesFrom(event.dataTransfer));
      }}
    >
      {picking ? (
        <div className="mention">
          {candidates.map((name, index) => (
            <button key={name} className={`mention__item${index === cursor % candidates.length ? " mention__item--on" : ""}`}
              onMouseDown={(event) => { event.preventDefault(); choose(name); }}>
              @{name}
            </button>
          ))}
        </div>
      ) : null}
      {refused.length === 0 ? null : (
        <div className="shell__refused" onClick={() => setRefused([])} title={t("点一下收起")}>
          {refused.map((why) => <div key={why}>{why}</div>)}
        </div>
      )}
      <div className="shell">
        {shots.length === 0 ? null : (
          <div className="shots">
            {shots.map((one) => (
              <div key={one.key} className={`shots__one${one.file === true ? " shots__one--file" : ""}`}>
                {one.file === true
                  ? <span className="shots__doc" title={one.name}>
                      <svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true">
                        <path d="M4 1.5h5.5L13 5v9.5H4z M9.5 1.5V5H13" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" />
                      </svg>
                      {one.name}
                    </span>
                  : <img src={one.url} alt={one.name} />}
                <button className="shots__x" onClick={() => drop(one.key)} aria-label={tt("不发 {a}", { a: one.name })} title={t("拿掉")}>×</button>
              </div>
            ))}
          </div>
        )}
        <textarea
          ref={ref}
          className="shell__input"
          placeholder={mentions.length > 0 ? t("说点什么，@ 可以点名") : t("说点什么，或者交代一件事")}
          rows={1}
          disabled={disabled}
          value={draft}
          onChange={(event) => {
            const value = event.target.value;
            setDraft(value);
            // 只在**行首或空格后**的 @ 才算点名 —— 否则邮箱地址里的 @ 也会弹面板
            const at = value.lastIndexOf("@");
            const before = at <= 0 ? "" : value[at - 1] ?? "";
            const ok = mentions.length > 0 && at >= 0 && (at === 0 || before === " " || before === "\n")
              && !value.slice(at + 1).includes(" ");
            setPickAt(ok ? at : null);
            setCursor(0);
          }}
          onPaste={(event) => {
            // **粘贴优先**: 截图从剪贴板过来时 clipboardData.files 里就是它,
            // 拦住这一次的默认行为, 免得同时又往输入框里塞一段文件名
            const files = filesFrom(event.clipboardData);
            if (files.length === 0) return;
            event.preventDefault();
            void take(files);
          }}
          onKeyDown={(event) => {
            if (picking) {
              if (event.key === "ArrowDown") { event.preventDefault(); setCursor((c) => c + 1); return; }
              if (event.key === "ArrowUp") { event.preventDefault(); setCursor((c) => c + candidates.length - 1); return; }
              if (event.key === "Escape") { event.preventDefault(); setPickAt(null); return; }
              if (event.key === "Enter" || event.key === "Tab") {
                event.preventDefault();
                choose(candidates[cursor % candidates.length]!);
                return;
              }
            }
            if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
            event.preventDefault();
            send();
          }}
        />
        <div className="shell__actions">
          <input ref={fileRef} type="file" accept="image/png,image/jpeg,image/webp,image/gif" multiple hidden
            onChange={(event) => { void take([...(event.target.files ?? [])]); event.target.value = ""; }} />
          <input ref={anyRef} type="file" multiple hidden
            onChange={(event) => { void take([...(event.target.files ?? [])]); event.target.value = ""; }} />
          {/* + 是一个菜单不是一个动作: 附件类的功能都长在这儿 */}
          <span className="plus" ref={plusRef}>
            <button className="shell__btn" type="button" title={t("附件")} aria-label={t("附件")} aria-expanded={plusOpen}
              disabled={disabled} onClick={() => setPlusOpen((now) => !now)}>+</button>
            {plusOpen ? (
              <div className="plus__menu" role="menu">
                <button className="plus__item" role="menuitem" type="button"
                  onClick={() => { setPlusOpen(false); fileRef.current?.click(); }}>
                  <svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true">
                    <rect x="1.8" y="2.8" width="12.4" height="10.4" rx="2" fill="none" stroke="currentColor" strokeWidth="1.4" />
                    <circle cx="5.6" cy="6.4" r="1.2" fill="currentColor" />
                    <path d="M3 12l3.4-3.6 2.4 2.4 2.6-2.8 2.8 3" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
                  </svg>
                  {t("图片")}<small>{t("也可以直接粘贴、拖进来")}</small>
                </button>
                <button className="plus__item" role="menuitem" type="button"
                  onClick={() => { setPlusOpen(false); anyRef.current?.click(); }}>
                  <svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true">
                    <path d="M4 1.5h5.5L13 5v9.5H4z M9.5 1.5V5H13" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" />
                  </svg>
                  {t("文件")}<small>{t("最大 20MB，他会用工具打开")}</small>
                </button>
              </div>
            ) : null}
          </span>
          <span className="shell__grow" />
          <button className="shell__btn shell__btn--send" type="button" title={t("发送")} onClick={send}
            disabled={disabled || (draft.trim().length === 0 && shots.length === 0)}>
            <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
              <path d="M8 13V3.6M8 3.2 3.6 7.6M8 3.2l4.4 4.4" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </button>
        </div>
      </div>
    </div>
  );
}
