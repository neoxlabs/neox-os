import { t, tt } from "../i18n/index.js";
import { useEffect, useRef, useState } from "react";
import { Avatar } from "./Avatar.js";
import { Doing } from "./Timeline.js";
import type { Waiting } from "./waiting.js";

/**
 * BusyStrip — 谁在干活, 干到哪儿. 一条纤细的状态条, 住在输入框上方.
 *
 *   它原来是时间线最底下的一行伪消息: 小规长跑一分多钟时永远垫底,
 *   用户跟 OA 的新对话只能排在它上面 —— 看着像消息串错了位
 *   (真机原话: "我对话还以为是搞错了"). 状态归状态条, 消息归时间线.
 *
 *   **点脸弹清单**: 几个人同时跑时, 条上只报个数; 想停其中一个,
 *   点头像 —— 每人一行(在干什么/第几步/多久/花了多少), 行尾各一颗停.
 */
export function BusyStrip({ waiting, onStop }: {
  waiting?: Waiting | undefined;
  onStop?(who: string): void;
}) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const away = (event: MouseEvent) => {
      if (box.current !== null && !box.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);
  // 人都停了, 清单也该收 —— 不收的话弹着一张空单
  useEffect(() => {
    if (waiting === undefined || (waiting.crew ?? []).length === 0) setOpen(false);
  }, [waiting]);

  if (waiting === undefined) return null;
  const faces = waiting.busy.length > 0 ? waiting.busy
    : waiting.who !== undefined ? [waiting.who] : [];
  const crew = waiting.crew ?? [];

  return (
    <div className="busy" role="status" ref={box}>
      {faces.length > 0 ? (
        <button className="busy__faces" type="button" aria-expanded={open}
          title={crew.length > 0 ? t("看看各自在干什么") : undefined}
          onClick={() => { if (crew.length > 0) setOpen((now) => !now); }}>
          <span className="facepile">
            {faces.slice(0, 4).map((one) => <Avatar key={one} id={one} size={18} liveness="busy" />)}
          </span>
        </button>
      ) : null}
      <span className="shimmer busy__label">
        {/* 群里要念出是谁 —— 状态条不在他的消息旁边, 光"正在想"认不出人 */}
        {waiting.who !== undefined && waiting.busy.length <= 1 ? `${waiting.who} ${waiting.label}` : waiting.label}
      </span>
      <Doing at={waiting.startedAt} steps={waiting.steps} tokens={waiting.tokens} />
      <span className="busy__grow" />
      {/* 停: 会自己改文件、自己花钱的东西, 必须有一颗按下去就停的按钮 */}
      {onStop !== undefined && faces.length > 0 ? (
        <button className="row__stop" title={t("停下这一轮，手上的改动留着")}
          onClick={() => faces.forEach((one) => onStop(one))}>
          {faces.length > 1 ? t("全停") : t("停")}
        </button>
      ) : null}

      {/* 清单: 每人一行, 行尾各一颗停 —— 停一个不牵连别人 */}
      {open && crew.length > 0 ? (
        <div className="busy__list" role="menu">
          {crew.map((one) => (
            <div className="busy__one" key={one.name}>
              <Avatar id={one.name} size={22} liveness="busy" />
              <span className="busy__who">{one.name}</span>
              <span className="busy__doing">{one.doing}</span>
              <Doing at={one.startedAt} steps={one.steps} tokens={one.tokens} />
              <span className="busy__grow" />
              {onStop === undefined ? null : (
                <button className="row__stop" title={tt("停下 {a} 这一轮，手上的改动留着", { a: one.name })}
                  onClick={() => onStop(one.name)}>{t("停")}</button>
              )}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
