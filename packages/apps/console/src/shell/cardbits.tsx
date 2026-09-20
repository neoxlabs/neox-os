/**
 * cardbits —— 弹出卡片共用的几块料.
 *
 *   资料卡(点一个人)和群卡(点一个群)是同一种东西的两种内容:
 *   贴着头像弹、点空白关掉、卡右边能拉出一扇副窗. **摆在哪儿**这件事
 *   尤其不能各写一份 —— 它要量尺寸、要翻边、要夹进窗口里, 写两遍就是
 *   将来有一张卡在窗口边上被切掉半截, 而另一张好好的.
 */

import { useLayoutEffect, useState } from "react";
import type { ReactNode, RefObject } from "react";

/** 贴着头像弹时留的缝, 和离窗口边最少留的空 */
const GAP = 10;
const EDGE = 12;

export interface Place { readonly left: number; readonly top: number; readonly flip: boolean }

/**
 * useAnchored 算卡片的落点: **先贴头像的右边, 放不下就翻到左边**,
 * 最后夹进窗口里.
 *
 *   夹这一步不能省: 头像可能就在窗口最底下那一行, 而卡有四百多高 ——
 *   不夹的话它一半在窗口外, 被客户端的边界切掉, 那一半就再也看不见了
 *   (卡本身是 fixed, 盖得住任何东西, 但盖不住"窗口外").
 *
 *   量完再画: 卡多宽多高要等副窗开没开才知道, 所以先渲染一次量出来,
 *   再把它放到算好的地方 —— 用 layout effect, 中间那一帧人看不见.
 *
 *   null = 还没量出来, 别画在错的地方.
 */
export function useAnchored(
  ref: RefObject<HTMLElement | null>,
  anchor: DOMRect | null | undefined,
  open: boolean,
  deps: readonly unknown[]
): Place | null {
  const [place, setPlace] = useState<Place | null>(null);
  useLayoutEffect(() => {
    const dock = ref.current;
    if (!open || dock === null) return;
    const box = dock.getBoundingClientRect();
    const view = { w: window.innerWidth, h: window.innerHeight };
    if (anchor === null || anchor === undefined) {
      setPlace({ left: Math.round((view.w - box.width) / 2), top: Math.round((view.h - box.height) / 2), flip: false });
      return;
    }
    /**
     * 右边放不下就**整个翻到左边, 并且左右对调**(row-reverse):
     * 翻过去之后挨着头像的应该还是那张卡, 而不是副窗 —— 副窗是从卡里
     * 拉出来的一扇窗, 它离头像更近的话, 这层关系就反了.
     */
    const right = anchor.right + GAP;
    const flipped = anchor.left - GAP - box.width;
    const flip = right + box.width > view.w - EDGE && flipped >= EDGE;
    const left = right + box.width <= view.w - EDGE ? right : flip ? flipped : right;
    const top = anchor.top + anchor.height / 2 - box.height / 2;
    setPlace({
      left: Math.round(clamp(left, EDGE, view.w - box.width - EDGE)),
      top: Math.round(clamp(top, EDGE, view.h - box.height - EDGE)),
      flip
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, anchor, ...deps]);
  return place;
}

/**
 * Tap —— **一整行就是那颗按钮**.
 *
 *   值本身就是入口: 点路径就是换地方, 点房间名就是换房间,
 *   点那个条数就是去看他说过什么. 旁边再挂一颗"换个地方"是把同一件事
 *   说两遍, 而那颗按钮换来的只是一整行的高.
 *
 *   能点这件事靠**右边那个尖角**和整行的悬停底色说清楚, 不靠一句文案;
 *   副窗开着的那一行会一直亮着 —— 右边那扇窗说的是这一行的事.
 */
export function Tap({ label, children, onTap, on }: { label: string; children: ReactNode; onTap(): void; on: boolean }) {
  return (
    <button className={`rw rw--tap${on ? " rw--on" : ""}`} onClick={onTap} aria-expanded={on}>
      <span className="rw__copy">{label}</span>
      <span className="rw__ctl rw__ctl--tap">
        {children}
        <svg className="rw__chev" viewBox="0 0 12 12" width="11" height="11" fill="none"
             stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M4.5 2.5l4 3.5-4 3.5" />
        </svg>
      </span>
    </button>
  );
}

// 判据只有一份 —— 见 view/presence.ts. 这里原来有一份自己的副本,
// 而副本迟早会跟本体分叉: 同一个 bot 在侧栏写着"在线", 点开写着别的.

/** 历史那一列只给到秒: 日期在这条列表里是废话, 它们几乎都是今天 */
export function clamp(value: number, low: number, high: number): number {
  // high 有可能比 low 还小(卡比窗口还高), 那时贴上边 —— 至少头是全的
  return high < low ? low : Math.min(high, Math.max(low, value));
}

export function clock(at: number): string {
  const time = new Date(at);
  const two = (n: number) => String(n).padStart(2, "0");
  return `${two(time.getHours())}:${two(time.getMinutes())}:${two(time.getSeconds())}`;
}

/**
 * pathParts 让路径**在斜杠处换行**.
 *
 *   纯 CSS 做不到这件事: word-break: break-all 会在任意字符处断,
 *   于是 /Users/x/AI/test-projects/oa 断成 "…/test-project" + "s/oa" ——
 *   看得懂, 但读起来要在脑子里把词再接回去.
 *
 *   人读路径是按段读的, 所以断点也该在段与段之间. <wbr> 是"可以在这儿断"
 *   的标记, 放不下才会用到它.
 */
export function pathParts(path: string): ReactNode {
  const parts = homeShort(path).split("/");
  return parts.map((part, index) => (
    <span key={index}>
      {index === 0 ? part : `/${part}`}
      {index < parts.length - 1 ? <wbr /> : null}
    </span>
  ));
}

/**
 * homeShort 把家目录写成 `~`.
 *
 *   /Users/you/projects/oa 有 38 个字符, 而前 17 个
 *   ("我的家在哪儿")对读的人来说一个信息量都没有. `~` 是所有人都认的
 *   写法, 换过去之后整条路径一行读得完.
 */
export function homeShort(path: string): string {
  const match = /^\/(?:Users|home)\/[^/]+(?=\/|$)/.exec(path);
  return match === null ? path : `~${path.slice(match[0].length)}`;
}
