import type { Presence } from "./presence.js";
/**
 * Avatar — 由 id 确定性生成的一张脸.
 *
 *   三条:
 *
 *   ① **圆的**. 不是圆角方. 会话列表里圆形读起来是"一个人",
 *      圆角方读起来是"一个应用图标".
 *
 *   ② **不放文字**. 拿名字第一个汉字当头像是省事不是设计 ——
 *      一排看过去全是字, 认不出谁是谁. 这里按 id 生成一只小家伙:
 *      耳朵/眼睛/嘴/天线各几种, 组合起来足够区分, 而且同一个 id
 *      永远是同一只, 不用存不用配.
 *
 *   ③ **群聊跟单聊是同一个形状**. 一样的圆, 只是里面塞进去几张脸.
 *      给群另起一种造型, 会让人以为那是另一类东西.
 */

const HUES = [206, 158, 266, 22, 322, 186, 240, 96, 44, 288];

function hash(value: string): number {
  let h = 2166136261;
  for (let i = 0; i < value.length; i += 1) { h ^= value.charCodeAt(i); h = Math.imul(h, 16777619); }
  return Math.abs(h);
}

interface Traits { hue: number; ears: number; eyes: number; mouth: number; }

function traits(id: string): Traits {
  const h = hash(id);
  return {
    hue: HUES[h % HUES.length]!,
    ears: (h >> 4) % 3,   // 0 无 · 1 猫耳 · 2 天线
    eyes: (h >> 8) % 3,   // 0 圆 · 1 竖椭圆 · 2 面罩
    mouth: (h >> 12) % 3  // 0 点 · 1 微笑 · 2 一横
  };
}

/** 画一只. viewBox 恒为 32×32, 缩放交给外面 */
function Creature({ t }: { t: Traits }) {
  const skin = `hsl(${t.hue} 42% 62%)`;
  const deep = `hsl(${t.hue} 44% 34%)`;
  const ink = `hsl(${t.hue} 46% 17%)`;
  return (
    <>
      {t.ears === 1 ? (
        <>
          <path d="M8 11 L9.5 4.5 L15 9 Z" fill={deep} />
          <path d="M24 11 L22.5 4.5 L17 9 Z" fill={deep} />
        </>
      ) : null}
      {t.ears === 2 ? (
        <>
          <rect x="15.2" y="3.2" width="1.6" height="5" rx=".8" fill={deep} />
          <circle cx="16" cy="3" r="2" fill={deep} />
        </>
      ) : null}
      {/* 头 */}
      <rect x="6.5" y="8.5" width="19" height="17" rx={t.ears === 2 ? 5.5 : 8.5} fill={skin} />
      {t.eyes === 2 ? (
        <>
          <rect x="9.5" y="13.5" width="13" height="5.5" rx="2.75" fill={ink} />
          <circle cx="13" cy="16.2" r="1.05" fill={skin} />
          <circle cx="19" cy="16.2" r="1.05" fill={skin} />
        </>
      ) : (
        <>
          <ellipse cx="12.6" cy="16.4" rx={t.eyes === 1 ? 1.5 : 1.9} ry="2.1" fill={ink} />
          <ellipse cx="19.4" cy="16.4" rx={t.eyes === 1 ? 1.5 : 1.9} ry="2.1" fill={ink} />
        </>
      )}
      {t.mouth === 0 ? <circle cx="16" cy="21" r="1.15" fill={ink} opacity=".75" /> : null}
      {t.mouth === 1 ? <path d="M13.4 20.4 Q16 22.8 18.6 20.4" fill="none" stroke={ink} strokeWidth="1.4" strokeLinecap="round" opacity=".8" /> : null}
      {t.mouth === 2 ? <rect x="13.6" y="20.5" width="4.8" height="1.4" rx=".7" fill={ink} opacity=".7" /> : null}
    </>
  );
}

/** 兼容旧名字 —— 语义在 presence.ts 里 */
export type Liveness = Presence;

function ring(t: Traits): string {
  return `linear-gradient(155deg, hsl(${t.hue} 34% 30%), hsl(${(t.hue + 24) % 360} 32% 20%))`;
}

export function Avatar({ id, size = 34, liveness }: { id: string; size?: number; liveness?: Liveness }) {
  const t = traits(id);
  return (
    <span className="ava" style={{ width: size, height: size }}>
      <span className="ava__disc" style={{ background: ring(t) }}>
        <svg viewBox="0 0 32 32" aria-hidden="true"><Creature t={t} /></svg>
      </span>
      {liveness === undefined ? null : <span className={`ava__dot ava__dot--${liveness}`} />}
    </span>
  );
}

/**
 * 群头像 —— **同一个圆**, 里面按人数切格子塞脸.
 *   2 人左右对半, 3 人上一下二, 4 人四宫格, 再多只画前四张.
 */
export function GroupAvatar({ ids, size = 34, liveness }: { ids: readonly string[]; size?: number; liveness?: Liveness }) {
  const faces = ids.slice(0, 4);
  const t0 = traits(faces[0] ?? "room");
  return (
    <span className="ava" style={{ width: size, height: size }}>
      <span className="ava__disc" style={{ background: ring(t0) }}>
        <svg viewBox="0 0 32 32" aria-hidden="true">
          {faces.map((id, index) => {
            const box = cell(index, faces.length);
            return (
              <g key={id} transform={`translate(${box.x} ${box.y}) scale(${box.s})`}>
                <Creature t={traits(id)} />
              </g>
            );
          })}
        </svg>
      </span>
      {liveness === undefined ? null : <span className={`ava__dot ava__dot--${liveness}`} />}
    </span>
  );
}

/** 32×32 里第 index 张脸放哪 —— 数值是照着圆形裁切调过的, 别按网格直觉改 */
function cell(index: number, total: number): { x: number; y: number; s: number } {
  if (total <= 1) return { x: 0, y: 0, s: 1 };
  if (total === 2) {
    return index === 0 ? { x: -2.5, y: 3, s: .62 } : { x: 14.5, y: 3, s: .62 };
  }
  if (total === 3) {
    if (index === 0) return { x: 6, y: -1, s: .56 };
    return index === 1 ? { x: -1.5, y: 12, s: .56 } : { x: 13.5, y: 12, s: .56 };
  }
  const column = index % 2, rowIndex = Math.floor(index / 2);
  return { x: -1 + column * 16.5, y: -1 + rowIndex * 16.5, s: .55 };
}
