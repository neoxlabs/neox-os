import { beforeAll, describe, expect, it } from "vitest";

import { EventStore } from "../store.js";
import type { ProcessID } from "../events.js";

// 帧合并要 requestAnimationFrame —— node 环境下没有, 给一个立刻执行的
beforeAll(() => {
  const g = globalThis as { requestAnimationFrame?: unknown; cancelAnimationFrame?: unknown };
  g.requestAnimationFrame ??= (fn: () => void) => { fn(); return 0; };
  g.cancelAnimationFrame ??= () => {};
});

const ev = (pid: string, seq: number) =>
  ({ pid, seq, at: 0, kind: "proc.output", payload: { text: `#${seq}` } }) as never;

/**
 * 断线重连的游标必须指在**第一个洞**上, 不是见过的最大值.
 *
 *   丢事件是真会发生的: 服务端 outbox 满了会丢中间那条、后面的照发,
 *   然后推一条 gap 让界面重连补齐. 补齐的前提正是游标得指在洞上 ——
 *   拿高水位当游标, 等于跟服务端说"洞那一段我有了".
 *
 *   而渲染那侧是"停在洞前面, 绝不跳过". 两件事合起来: 这一段对话
 *   从此不再更新, **一声不吭**, 别的会话全是好的.
 */
describe("重连游标", () => {
  it("中间丢了一条, 游标停在洞上", () => {
    const store = new EventStore();
    store.append([ev("p1", 0), ev("p1", 1), ev("p1", 3), ev("p1", 4)]);
    expect(store.highWater("p1")).toBe(5);   // 见过最大的是 4
    expect(store.haveThrough("p1")).toBe(2); // 但只拿全了 0..1
  });

  it("洞补上之后游标才往前走", () => {
    const store = new EventStore();
    store.append([ev("p1", 0), ev("p1", 1), ev("p1", 3)]);
    expect(store.haveThrough("p1")).toBe(2);
    store.append([ev("p1", 2)]);
    expect(store.haveThrough("p1")).toBe(4);
  });

  it("没有洞的时候跟高水位一致 —— 不能让正常情况每次重连都全量重放", () => {
    const store = new EventStore();
    store.append([ev("p1", 0), ev("p1", 1), ev("p1", 2)]);
    expect(store.haveThrough("p1")).toBe(3);
    expect(store.haveThrough("p1")).toBe(store.highWater("p1"));
  });

  it("第一条就丢了也要从 0 要起", () => {
    const store = new EventStore();
    store.append([ev("p1", 2)]);
    expect(store.haveThrough("p1")).toBe(0);
  });

  it("没见过的 pid 从 0 要起", () => {
    expect(new EventStore().haveThrough("p9")).toBe(0);
  });
});

describe("侧栏预览那句话是什么时候说的", () => {
  const said = (pid: string, seq: number, at: number, text: string) =>
    ({ pid, seq, at, kind: "proc.output", payload: { stream: "s" + seq, channel: "say", text } }) as never;

  it("给出那句话的时间 —— 房间摘要要按时间挑人, 不是按谁干得多", () => {
    // 原来比的是事件条数: 做完整个模块的 bot 事件量远超刚上线的新人,
    // 于是它永远赢 —— 房间里来了新人说了话, 侧栏那一行一个字都不变.
    const store = new EventStore();
    store.append([said("p1", 0, 100, "老人说的"), said("p2", 0, 900, "新人说的")]);
    expect(store.previewAt("p1")).toBe(100);
    expect(store.previewAt("p2")).toBe(900);
  });

  it("取的是最后一句的时间, 不是第一句", () => {
    const store = new EventStore();
    store.append([said("p1", 0, 100, "早"), said("p1", 1, 500, "晚")]);
    expect(store.preview("p1")).toBe("晚");
    expect(store.previewAt("p1")).toBe(500);
  });

  it("一句话都没有就是 0 —— 不能当成'很久以前说过'", () => {
    expect(new EventStore().previewAt("p9")).toBe(0);
  });
});

/**
 * 窗口被盖住时 Chromium 会停掉 rAF —— 只挂 rAF 的话事件照收、界面不动,
 * 等窗口重新可见才一次性补上. SSE 0.0s 收到话、4.0s 收到回复时,
 * 界面可能到 21s 后才动, 因此还需要定时器兜底.
 */
describe("rAF 停了也得通知", () => {
  it("rAF 不来就走定时器, 而且只通知一次", async () => {
    const g = globalThis as { requestAnimationFrame: (fn: () => void) => number };
    const real = g.requestAnimationFrame;
    g.requestAnimationFrame = () => 0; // 装成"窗口被盖住": 排了队但永远不执行
    try {
      const store = new EventStore();
      let woke = 0;
      store.subscribe(() => { woke += 1; });
      store.append([ev("p9", 0)]);
      store.append([ev("p9", 1)]);
      expect(woke).toBe(0);
      await new Promise((done) => setTimeout(done, 160));
      expect(woke).toBe(1);            // 两批并成一次, 不是两次
      expect(store.version).toBe(1);
    } finally {
      g.requestAnimationFrame = real;
    }
  });

  it("rAF 正常时定时器不会再多通知一遍", async () => {
    const store = new EventStore();
    let woke = 0;
    store.subscribe(() => { woke += 1; });
    store.append([ev("p8", 0)]);       // 测试里的 rAF 是同步执行的
    expect(woke).toBe(1);
    await new Promise((done) => setTimeout(done, 160));
    expect(woke).toBe(1);
  });
});

describe("侧栏那一行", () => {
  it("光发一张图也是说了话 —— 留空看着像什么都没发生", () => {
    const store = new EventStore();
    store.append([{ pid: "p7", seq: 0, at: 1, kind: "input.recv",
      payload: { text: "", from: "我", images: [{ id: "a.png" }] } } as never]);
    expect(store.preview("p7")).toBe("[图片]");
    store.append([{ pid: "p7", seq: 1, at: 2, kind: "input.recv",
      payload: { text: "", from: "我", images: [{ id: "a.png" }, { id: "b.png" }] } } as never]);
    expect(store.preview("p7")).toBe("[2 张图]");
    // 有字就用字 —— 图是配着这句话发的
    store.append([{ pid: "p7", seq: 2, at: 3, kind: "input.recv",
      payload: { text: "看看这个", from: "我", images: [{ id: "a.png" }] } } as never]);
    expect(store.preview("p7")).toBe("看看这个");
  });
});

/**
 * 合进主干了要**不在这间屋子里也知道** —— 它改的是所有人共用的那一份.
 */
describe("最近一次合进主干", () => {
  const out = (pid: string, seq: number, payload: Record<string, unknown>) =>
    ({ pid, seq, at: seq, kind: "proc.output", payload }) as never;

  it("认得出那一条, 而且给的是最近的一次", () => {
    const store = new EventStore();
    store.append([
      out("p1", 0, { phase: "merged", text: "合进主干（master）了。1 file changed" }),
      out("p1", 1, { phase: "reply", text: "干完了" }),
      out("p1", 2, { phase: "merged", text: "合进主干（master）了。3 files changed" }),
    ]);
    expect(store.lastMerged("p1")?.seq).toBe(2);
    expect(store.lastMerged("p1")?.text).toContain("3 files");
  });

  it("没合过就是 null —— 不拿别的事件冒充", () => {
    const store = new EventStore();
    store.append([out("p2", 0, { phase: "reply", text: "合进主干了吗？还没" })]);
    expect(store.lastMerged("p2")).toBeNull();
  });

  /**
   * **只往回看一小段**: 通知问的是"刚刚有没有"而不是"这辈子有没有" ——
   * 翻整段历史既慢, 又会在刚连上时把几小时前的合并全都提醒一遍.
   */
  it("很久以前那次不算数", () => {
    const store = new EventStore();
    const many = [out("p3", 0, { phase: "merged", text: "上古那次" })];
    for (let i = 1; i <= 80; i += 1) many.push(out("p3", i, { phase: "step" }));
    store.append(many);
    expect(store.lastMerged("p3")).toBeNull();
  });
});

/**
 * **它不限轮次也不限步数, 所以给不出百分比** —— 一个百分比要么是编的,
 * 要么要它先承诺一个总数, 而那个承诺也是猜的.
 *
 *	能说的是已经发生的事实: 干了几步、从什么时候开始、花了多少.
 */
describe("这一轮干到哪儿了", () => {
  const st = (pid: string, seq: number, state: string) =>
    ({ pid, seq, at: seq * 1000, kind: "proc.state", payload: { state } }) as never;
  const outp = (pid: string, seq: number, payload: Record<string, unknown>) =>
    ({ pid, seq, at: seq * 1000, kind: "proc.output", payload }) as never;

  it("从这一轮开始算 —— 上一轮的步数不算在这一轮头上", () => {
    const store = new EventStore();
    store.append([
      st("q1", 0, "running"),
      outp("q1", 1, { phase: "step" }), outp("q1", 2, { phase: "step" }),
      outp("q1", 3, { phase: "usage", prompt: 900, completion: 100 }),
      st("q1", 4, "waiting"),
      st("q1", 5, "running"),                       // ← 新的一轮从这儿开始
      outp("q1", 6, { phase: "step" }),
      outp("q1", 7, { phase: "usage", prompt: 400, completion: 100 }),
    ]);
    const got = store.turnProgress("q1");
    expect(got?.steps).toBe(1);
    expect(got?.tokens).toBe(500);
    expect(got?.startedAt).toBe(5000);
  });

  it("没跑过就是 null —— 不拿一个 0 冒充", () => {
    const store = new EventStore();
    store.append([outp("q2", 0, { phase: "reply", text: "在" })]);
    expect(store.turnProgress("q2")).toBeNull();
  });
});

/**
 * **其他窗口删掉的, 这儿也要跟着删**.
 *
 *	删原来是"界面自己先删, 再告诉服务器" —— 一个窗口一条路走下来是对的.
 *	但从其他窗口删的如果没有广播, 通过接口清掉 108 个进程后、账本只剩一行,
 *	开着的那个窗口侧栏里仍有 12 间屋子 —— 点进去是空的.
 */
describe("别处删掉的会话", () => {
	it("广播一到就跟着删", () => {
		const store = new EventStore();
		store.append([
			{ pid: "p1" as ProcessID, seq: 0, at: 1, kind: "proc.state", payload: { state: "running" } },
			{ pid: "p2" as ProcessID, seq: 0, at: 2, kind: "proc.state", payload: { state: "running" } },
		]);
		expect(store.pids()).toHaveLength(2);

		store.append([{
			pid: "sense" as ProcessID, seq: 0, at: 3, kind: "proc.state",
			payload: { phase: "forgotten", pids: ["p1"] },
		}]);
		expect(store.pids()).toEqual(["p2"]);
	});

	// 广播本身不该在侧栏里长出一间"sense"屋子
	it("广播自己不留痕", () => {
		const store = new EventStore();
		store.append([{
			pid: "sense" as ProcessID, seq: 0, at: 1, kind: "proc.state",
			payload: { phase: "forgotten", pids: ["p9"] },
		}]);
		expect(store.pids()).toEqual([]);
	});
});
