import { t } from "../i18n/index.js";
import { useEffect } from "react";

/**
 * Lightbox —— 点开看大的.
 *
 *   聊天里的图必须**小着放、点开才大**: 一张手机截图按原尺寸铺开会
 *   把整屏对话顶掉, 而多数时候人只是扫一眼"哦是那张".
 *
 *   三条:
 *     · 点任何地方关掉 —— 跟资料卡一个规矩, 界面上"点空白关掉"只能有一种答案
 *     · Esc 关掉
 *     · 图**按窗口缩**, 不放大超过原始尺寸 —— 放大只会看到马赛克
 */
export function Lightbox({ shot, onClose }: {
  shot: { src: string; alt: string } | null;
  onClose(): void;
}) {
  useEffect(() => {
    if (shot === null) return;
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [shot, onClose]);

  if (shot === null) return null;
  return (
    <div className="lightbox" onMouseDown={onClose} role="dialog" aria-label={shot.alt}>
      <img className="lightbox__img" src={shot.src} alt={shot.alt}
           onMouseDown={(event) => event.stopPropagation()} />
      <div className="lightbox__bar">
        <span className="lightbox__name">{shot.alt}</span>
        {/* 在自己的窗口里打开 = 原图, 想另存/放大都在那儿做 */}
        <a className="lightbox__link" href={shot.src} target="_blank" rel="noreferrer"
           onMouseDown={(event) => event.stopPropagation()}>{t("看原图")}</a>
      </div>
    </div>
  );
}
