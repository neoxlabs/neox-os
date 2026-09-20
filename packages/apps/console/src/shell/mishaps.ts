import { tt } from "../i18n/index.js";
/**
 * mishaps —— **渲染进程里出的错, 得留下痕迹**.
 *
 * ── 为什么 ──
 *
 *	这套东西的自动检查走的全是 HTTP 那条口子: 主干上有什么、账本里
 *	说了什么. 而人用的是这个界面 —— **渲染进程里报一个错, 那些检查
 *	一个字都看不见**, 因为数据全是对的.
 *
 *	rAF 被系统节流时, 界面可能停留在 21 秒之前的状态,
 *	而 SSE 仍一条不落地全部收到. 仅检查 API 无法发现这种渲染侧故障.
 *
 *	浏览器的控制台只在开着开发者工具的时候才留得住, 而且连上去之前
 *	的一概看不到. 自己记一份最省事, 也最可靠.
 *
 * ── 只记不改 ──
 *
 *	这里不吞异常、不弹提示、不重试 —— 只是把它记下来.
 *	改变行为是另一件事, 混在一起的话反而会盖住真正的毛病.
 */

export type Mishap = {
	/** 什么时候 */
	at: number;
	/** 哪一类: 未捕获的异常, 还是没人接的 Promise */
	kind: "error" | "unhandled";
	text: string;
};

/** 最多留这么多 —— 一个坏掉的循环能一秒钟塞几千条 */
const MOST = 50;

declare global {
	interface Window {
		__neoxErrors?: Mishap[];
	}
}

export function noteMishap(kind: Mishap["kind"], text: string): void {
	if (typeof window === "undefined") return;
	const all = (window.__neoxErrors ||= []);
	all.push({ at: Date.now(), kind, text: text.slice(0, 500) });
	if (all.length > MOST) all.splice(0, all.length - MOST);
}

/** watchMishaps 从这一刻起, 出的错都记下来. 越早叫越好 */
export function watchMishaps(): void {
	if (typeof window === "undefined") return;
	window.addEventListener("error", (e) => {
		// 图片/脚本加载失败也走这个事件, 它们没有 error 对象
		const detail = e.error?.stack || e.error?.message || e.message ||
			tt("{a} 加载失败", { a: (e.target as { src?: string } | null)?.src ?? "?" });
		noteMishap("error", String(detail));
	});
	window.addEventListener("unhandledrejection", (e) => {
		const why = e.reason;
		noteMishap("unhandled", String(why?.stack || why?.message || why));
	});
}
