import { describe, expect, it, beforeEach } from "vitest";
import { noteMishap, watchMishaps } from "../mishaps.js";

/**
 * 测试跑在 node 上, 没有 window.
 *
 *	不为一条测试引 jsdom —— 这个模块要的只有两样: 挂事件、放一个数组.
 *	给它一个刚好够用的替身, 比拖进来一整个 DOM 实现诚实得多.
 */
type Listener = (e: unknown) => void;
const hooks: Record<string, Listener[]> = {};
beforeEach(() => {
	for (const k of Object.keys(hooks)) delete hooks[k];
	(globalThis as { window?: unknown }).window = {
		addEventListener(name: string, fn: Listener) {
			(hooks[name] ||= []).push(fn);
		},
	};
});
const fire = (name: string, e: unknown) => (hooks[name] ?? []).forEach((f) => f(e));

describe("渲染进程里出的错要留下痕迹", () => {

	const kept = () => (globalThis as { window?: { __neoxErrors?: unknown[] } })
		.window?.__neoxErrors ?? [];

	it("记下来的东西查得出来", () => {
		noteMishap("error", "TypeError: 读不到 undefined 的 map");
		expect(kept()).toHaveLength(1);
		expect((kept()[0] as { text: string }).text).toContain("读不到");
		expect((kept()[0] as { kind: string }).kind).toBe("error");
	});

	// **一个坏掉的循环能一秒钟塞几千条** —— 留最近的那些就够
	it("不许把内存撑爆", () => {
		for (let i = 0; i < 200; i++) noteMishap("error", `第 ${i} 条`);
		expect(kept().length).toBeLessThanOrEqual(50);
		// 留的是**最近的**: 出问题时最后几条才是现场
		expect((kept().at(-1) as { text: string }).text).toContain("199");
	});

	it("太长的一条要截断", () => {
		noteMishap("error", "x".repeat(2000));
		expect((kept()[0] as { text: string }).text.length).toBeLessThanOrEqual(500);
	});

	// 没人接的 Promise 是最容易漏的那一类: 界面上什么都不显示
	it("没人接的 Promise 也算", () => {
		watchMishaps();
		fire("unhandledrejection", { reason: new Error("接口挂了") });
		expect(kept().some((m) => (m as { kind: string; text: string }).kind === "unhandled"
			&& (m as { text: string }).text.includes("接口挂了"))).toBe(true);
	});
});
