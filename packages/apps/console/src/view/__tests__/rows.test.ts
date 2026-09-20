import { describe, expect, it } from "vitest";

import { RowProjector } from "../rows.js";

let seq = 0;
function out(pid: string, payload: Record<string, unknown>) {
  return { pid, seq: seq++, at: Date.now(), kind: "proc.output", payload } as never;
}

describe("同一件事只占一行", () => {
  it("重试的那几次报错并成一张卡, 次数标出来", () => {
    // 模型出错时上层连着重试三次, 每次发一条 model_err —— 屏幕上原来是
    // 三张一模一样的红卡, 而信息只有一份. 用户截图里就是这样.
    const p = new RowProjector("p1");
    const err = "供应商拒绝: The `reasoning_content` in the thinking mode must be passed back to the API.";
    p.consume([
      out("p1", { phase: "model_err", n: 0, err }),
      out("p1", { phase: "model_err", n: 1, err }),
      out("p1", { phase: "model_err", n: 2, err }),
    ]);
    const alerts = p.rows.filter((r) => r.kind === "alert" && r.merged !== true);
    expect(alerts).toHaveLength(1);
    // **次数必须标出来**: 不标的话看起来像只发生了一次, 而"连着三次"
    // 本身就是判断"偶发还是真坏了"的依据
    expect(alerts[0]?.repeat).toBe(3);
  });

  it("不一样的报错各占一张 —— 合并只针对重复", () => {
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "model_err", err: "供应商拒绝: A" }),
      out("p1", { phase: "model_err", err: "供应商拒绝: B" }),
    ]);
    expect(p.rows.filter((r) => r.kind === "alert" && r.merged !== true)).toHaveLength(2);
  });

  it("下一轮的同样报错另起一张 —— 两轮的失败不能看着像一次", () => {
    const p = new RowProjector("p1");
    const err = "同一个错";
    // **一次喂进去**: consume 带游标, 分几次传不同的数组会被跳过
    p.consume([
      out("p1", { phase: "model_err", err }),
      out("p1", { phase: "reply", text: "我先停一下" }), // 说了句话 = 这一轮结束
      out("p1", { phase: "model_err", err }),
    ]);
    const alerts = p.rows.filter((r) => r.kind === "alert" && r.merged !== true);
    expect(alerts).toHaveLength(2);
    expect(alerts[0]?.repeat ?? 1).toBe(1);
  });

  it("一轮里的思考也收成一行", () => {
    // 一轮包含 16 步、8 段思考时, 屏幕上若出现八行"想了一会儿"
    // 摞在一起, 把它真正说的那句话挤到屏幕外面
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "step", thought: "先看看工作区", tool: "list_dir", args: {} }),
      out("p1", { phase: "step", thought: "建 PLAN.md", tool: "write_file", args: {} }),
      out("p1", { phase: "step", thought: "跑一下", tool: "run", args: {} }),
    ]);
    const thoughts = p.rows.filter((r) => r.kind === "thought" && r.merged !== true);
    expect(thoughts).toHaveLength(1);
    // 三段一段不少, 展开时按顺序全在
    expect(thoughts[0]?.text).toContain("先看看工作区");
    expect(thoughts[0]?.text).toContain("跑一下");
  });

  it("轮次收口后, 思考并进工具行 —— 折着时只剩一行过程", () => {
    // 用户截图: "想了一会儿" 和 "用了 10 个工具" 各占一行, 间距还不一样.
    // 说的都是"过程收好了"这一件事, 该是一行.
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "step", thought: "先看看工作区", tool: "list_dir", args: {} }),
      out("p1", { phase: "tool_ok", tool: "list_dir", result: "…" }),
      out("p1", { phase: "reply", text: "看完了" }),
    ]);
    const thought = p.rows.find((r) => r.kind === "thought");
    const group = p.rows.find((r) => r.kind === "tools");
    expect(thought?.merged).toBe(true);
    // 想的内容一个字不丢, 挂在工具行上, 展开时想的在前
    expect(group?.thought).toContain("先看看工作区");
  });

  it("跑的时候不并 —— 那是进度, 不是占位", () => {
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "step", thought: "先看看工作区", tool: "list_dir", args: {} }),
    ]);
    // 轮次没结束: 思考行活着, 工具行也活着, 各自显示进度
    expect(p.rows.find((r) => r.kind === "thought")?.merged).not.toBe(true);
    expect(p.rows.find((r) => r.kind === "tools")?.thought).toBeUndefined();
  });

  it("光想没动手的轮次, 思考行留在原地", () => {
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "step", thought: "这个不用动手" }),
      out("p1", { phase: "reply", text: "不用改" }),
    ]);
    const thought = p.rows.find((r) => r.kind === "thought");
    expect(thought?.merged).not.toBe(true);
  });
});

describe("被闸拦下 ≠ 干活失败", () => {
  it("闸拦下的那一次带 blocked 标, 不算故障", () => {
    // write_file 拦住"盖掉你没读过的那一版"是**按设计工作**. 界面把它
    // 写成"1 个没成", 用户就会去查一个不存在的故障 —— 更糟的是,
    // 看多了会开始怀疑这些闸本身有毛病.
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "batch", tools: ["write_file"] }),
      out("p1", { phase: "tool_err", tool: "write_file", err: "你没读过这个文件", blocked: true }),
    ]);
    const call = p.rows.find((r) => r.kind === "tools")?.calls?.[0];
    expect(call?.blocked).toBe(true);
  });

  it("真故障不带这个标 —— 否则故障会被当成正常", () => {
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "batch", tools: ["run"] }),
      out("p1", { phase: "tool_err", tool: "run", err: "退出码 1" }),
    ]);
    const call = p.rows.find((r) => r.kind === "tools")?.calls?.[0];
    expect(call?.failed).toBe(true);
    expect(call?.blocked).toBeUndefined();
  });
});

describe("同一个人连着说话, 头像名字只摆一遍", () => {
  it("中间夹一组工具调用, 不算换人", () => {
    // 真机截图: [小勤]想了一会儿 → 用了 21 个工具 → [小勤]想了一会儿 …
    // 工具行不带身份, 于是被当成"换人了", 头像和名字又摆了一遍.
    // 那是同一个人在同一轮里动手, 不是别人插话.
    const p = new RowProjector("p1", "小勤", true);
    p.consume([
      out("p1", { phase: "step", thought: "先看看现在什么样", tool: "read_file" }),
      out("p1", { phase: "batch", tools: ["read_file"] }),
      out("p1", { phase: "tool_ok", tool: "read_file", result: "…" }),
      out("p1", { phase: "reply", text: "改完了" }),
    ]);
    const faces = p.rows.filter((r) => r.showWho === true);
    expect(faces).toHaveLength(1);
    // 轮次收口后思考并进了工具行 —— 脸跟着搬到工具行上, 这一簇不能没有主人
    expect(faces[0]?.kind).toBe("tools");
  });

  it("用户插了话, 后面那句要重新露脸 —— 那才**真的**是换人", () => {
    const p = new RowProjector("p1", "小勤", true);
    p.consume([
      out("p1", { phase: "reply", text: "第一句" }),
      { pid: "p1", seq: 90, at: Date.now(), kind: "input.recv", payload: { text: "再改一下" } } as never,
      out("p1", { phase: "reply", text: "第二句" }),
    ]);
    expect(p.rows.filter((r) => r.showWho === true)).toHaveLength(2);
  });
});

describe("工具行的路径: 工作区内写相对", () => {
  const ROOT = "/Users/you/projects/oa";
  function pathsOf(root: string | undefined, paths: readonly string[]) {
    const p = new RowProjector("p1");
    const evs = [out("p1", { phase: "batch", tools: paths.map(() => "read_file") })];
    for (const path of paths) evs.push(out("p1", { phase: "tool_ok", tool: "read_file", args: { path }, result: "…" }));
    p.consumeFrom("p1", "小勤", evs as never, root);
    return p.rows.find((r) => r.kind === "tools")?.calls?.map((c) => c.arg) ?? [];
  }

  it("八成字符逐字相同的那截收起来", () => {
    // 一组展开的路径只有末尾一小截不同, 却被推到行中间:
    //   read_file  /Users/you/projects/oa/README.md
    //   list_dir   /Users/you/projects/oa/app/routes
    expect(pathsOf(ROOT, [`${ROOT}/README.md`, `${ROOT}/app/routes`]))
      .toEqual(["README.md", "app/routes"]);
  });

  it("工作区本身写成点, 不是一整条绝对路径", () => {
    expect(pathsOf(ROOT, [ROOT])).toEqual(["."]);
  });

  it("出了工作区的照旧 —— 那才是要看清的那种", () => {
    expect(pathsOf(ROOT, ["/etc/hosts"])).toEqual(["/etc/hosts"]);
  });

  it("不知道工作区就别动它", () => {
    expect(pathsOf(undefined, [`${ROOT}/README.md`])).toEqual([`${ROOT}/README.md`]);
  });

  // 前缀相同但不是同一个目录 —— 收错了会指向另一个地方
  it("只按目录边界收, 不按字符串前缀", () => {
    expect(pathsOf(ROOT, [`${ROOT}-backup/README.md`])).toEqual([`${ROOT}-backup/README.md`]);
  });
});

describe("账本里永久缺一条时, 对话不能就此停住", () => {
  // 45 个进程各缺一条 seq 时, 缺的都是 bot 那句开场白.
  // 账本只增不改, 那条永远补不回来. 于是这些会话在界面上停在第一句话,
  // 重连多少次都一样, 而且一声不吭: 侧栏预览是新的, 点进去是旧的.
  function withHole() {
    const p = new RowProjector("p1", "小勤", false);
    let clock = 1000;
    p.now = () => clock;
    const events: unknown[] = [];
    events[0] = out("p1", { phase: "reply", text: "第一句" });
    // seq 1 是那个洞
    events[2] = out("p1", { phase: "reply", text: "第三句" });
    return { p, events: events as never, tick: (ms: number) => { clock += ms; } };
  }

  it("刚发现洞时**不跳** —— 乱序到达是允许的, 跳早了就是把内容画丢", () => {
    const { p, events } = withHole();
    p.consume(events);
    expect(p.rows.map((r) => r.text)).toEqual(["第一句"]);
  });

  it("卡够久, 而且后面的已经到了, 就跳过去接着画", () => {
    const { p, events, tick } = withHole();
    p.consume(events);
    tick(4000);
    p.consume(events);
    expect(p.rows.map((r) => r.text)).toEqual(["第一句", "第三句"]);
  });

  it("后面一条都没到就一直等 —— 那是'还没轮到', 不是'缺了'", () => {
    const p = new RowProjector("p1", "小勤", false);
    let clock = 1000;
    p.now = () => clock;
    const events: unknown[] = [];
    events[0] = out("p1", { phase: "reply", text: "第一句" });
    events.length = 3; // 1 和 2 都还没到
    p.consume(events as never);
    clock += 10000;
    p.consume(events as never);
    expect(p.rows.map((r) => r.text)).toEqual(["第一句"]);
  });

  it("洞在等待期内补上了, 照常画, 不留痕", () => {
    const { p, events, tick } = withHole();
    p.consume(events);
    tick(500);
    (events as unknown[])[1] = out("p1", { phase: "reply", text: "第二句" });
    p.consume(events);
    expect(p.rows.map((r) => r.text)).toEqual(["第一句", "第二句", "第三句"]);
  });
});

describe("紧挨着的工具组并成一行", () => {
  it("一次审批把一轮活劈成两半, 不该在界面上变成两行", () => {
    // 决策解决后进程从 waiting 回到 running, 而 running 会关掉当前的组,
    // 于是后面的工具只能另起一行 —— 界面上两条贴在一起、中间什么都没有.
    // 那是同一个人同一轮干同一件事, 拆开会让"这一轮动了几下"要靠人自己加.
    const p = new RowProjector("p1", "报表", false);
    p.consume([
      out("p1", { phase: "batch", tools: ["recruit"] }),
      out("p1", { phase: "tool_ok", tool: "recruit", args: {}, result: "拉进来了" }),
      // 决策卡解决后标成 merged 留在数组里, 它不显示, 也就不该算隔开
      { pid: "p1", seq: 88, at: Date.now(), kind: "decide.requested",
        payload: { did: "d1", present: { kind: "choice", title: "拉人吗" } } } as never,
      { pid: "p1", seq: 89, at: Date.now(), kind: "decide.resolved",
        payload: { did: "d1", choice: "yes" } } as never,
      { pid: "p1", seq: 90, at: Date.now(), kind: "proc.state", payload: { state: "running" } } as never,
      out("p1", { phase: "step", tool: "read_file", args: { path: "PLAN.md" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "PLAN.md" }, result: "…" }),
    ]);
    const groups = p.rows.filter((r) => r.kind === "tools");
    expect(groups).toHaveLength(1);
    // 两个工具 + 那次授权, 全在同一行里
    const calls = groups[0]?.calls ?? [];
    expect(calls.filter((c) => c.approval !== true).map((c) => c.name)).toEqual(["recruit", "read_file"]);
    expect(calls.filter((c) => c.approval === true)).toHaveLength(1);
  });

  it("中间隔了一句话就不并 —— 那是两轮", () => {
    const p = new RowProjector("p1", "报表", false);
    p.consume([
      out("p1", { phase: "step", tool: "read_file", args: { path: "a" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "a" }, result: "…" }),
      out("p1", { phase: "reply", text: "看完了" }),
      out("p1", { phase: "step", tool: "read_file", args: { path: "b" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "b" }, result: "…" }),
    ]);
    expect(p.rows.filter((r) => r.kind === "tools")).toHaveLength(2);
  });

  it("房间里两个人各自动手, 不能并到一起 —— 并了就分不清谁干的", () => {
    const p = new RowProjector("room", "报表", true);
    p.consumeFrom("p1", "报表", [
      out("p1", { phase: "step", tool: "read_file", args: { path: "a" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "a" }, result: "…" }),
    ] as never);
    p.consumeFrom("p2", "阿导", [
      out("p2", { phase: "step", tool: "read_file", args: { path: "b" } }),
      out("p2", { phase: "tool_ok", tool: "read_file", args: { path: "b" }, result: "…" }),
    ] as never);
    expect(p.rows.filter((r) => r.kind === "tools")).toHaveLength(2);
  });
});

describe("房间里两个人的话不能粘在一起", () => {
  it("流名一样(都叫 s0)也要各归各的人", () => {
    // 流名是进程自己起的, 每个 bot 的第一条输出都走 "s0".
    // 键里不带 pid 的话, 后说话那个的文字会接在前一个的段落后面,
    // 挂在前一个的名字底下, 造成两个进程的输出串成一段.
    const p = new RowProjector("room", "报表", true);
    p.consumeFrom("p1", "报表", [
      { pid: "p1", seq: 0, at: 1, kind: "proc.output", payload: { stream: "s0", channel: "say", text: "报表在。" } }
    ] as never);
    p.consumeFrom("p2", "阿导", [
      { pid: "p2", seq: 0, at: 2, kind: "proc.output", payload: { stream: "s0", channel: "say", text: "阿导报到。" } }
    ] as never);
    const said = p.rows.filter((r) => r.kind === "said");
    expect(said.map((r) => r.text)).toEqual(["报表在。", "阿导报到。"]);
    expect(said.map((r) => r.who)).toEqual(["报表", "阿导"]);
  });
});

describe("房间里看得出哪组工具是谁干的", () => {
  it("工具行带身份, 两个人同时动手时各挂各的名字", () => {
    // 两个人同时干活时, 界面上会出现两条挨着的"用了 N 个工具", 谁都不挂名 —
    // 而那正是房间最常见的样子.
    const p = new RowProjector("room", "报表", true);
    p.consumeFrom("p1", "报表", [
      out("p1", { phase: "step", tool: "read_file", args: { path: "a" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "a" }, result: "…" }),
    ] as never);
    p.consumeFrom("p2", "阿导", [
      out("p2", { phase: "step", tool: "read_file", args: { path: "b" } }),
      out("p2", { phase: "tool_ok", tool: "read_file", args: { path: "b" }, result: "…" }),
    ] as never);
    const groups = p.rows.filter((r) => r.kind === "tools");
    expect(groups.map((r) => r.who)).toEqual(["报表", "阿导"]);
    // 两组各自露脸 —— 不露的话读者只能猜
    expect(groups.every((r) => r.showWho === true && r.showName === true)).toBe(true);
  });

  it("动手完接着说话, 不再重复露脸 —— 先动手后说话是同一个人的同一轮", () => {
    const p = new RowProjector("p1", "报表", true);
    p.consume([
      out("p1", { phase: "step", tool: "read_file", args: { path: "a" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "a" }, result: "…" }),
      out("p1", { phase: "reply", text: "看完了" }),
    ]);
    expect(p.rows.filter((r) => r.showWho === true)).toHaveLength(1);
  });
});

describe("房间里的话按时间排", () => {
  const say = (pid: string, seq: number, at: number, text: string) =>
    ({ pid, seq, at, kind: "proc.output", payload: { phase: "reply", text } }) as never;

  it("两个人交替说话, 不能一个人的整段排在另一个人整段前面", () => {
    // 原来是一个成员一个成员地灌: 刷新一次, 界面上就是 A 的一整段、
    // 然后 B 的一整段 —— 最新的回答排在最上面. 真机截图里就是这样.
    const p = new RowProjector("room", "报表", true);
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: [say("p1", 0, 100, "报表第一句"), say("p1", 1, 300, "报表第二句")] },
      { pid: "p2", speaker: "阿导", events: [say("p2", 0, 200, "阿导第一句"), say("p2", 1, 400, "阿导第二句")] },
    ]);
    expect(p.rows.map((r) => r.text)).toEqual(["报表第一句", "阿导第一句", "报表第二句", "阿导第二句"]);
    expect(p.rows.map((r) => r.who)).toEqual(["报表", "阿导", "报表", "阿导"]);
  });

  it("再喂一批只接新的, 已经画过的不重来", () => {
    const p = new RowProjector("room", "报表", true);
    const a = [say("p1", 0, 100, "早")];
    const b = [say("p2", 0, 200, "也早")];
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: a },
      { pid: "p2", speaker: "阿导", events: b },
    ]);
    a.push(say("p1", 1, 300, "接着说"));
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: a },
      { pid: "p2", speaker: "阿导", events: b },
    ]);
    expect(p.rows.map((r) => r.text)).toEqual(["早", "也早", "接着说"]);
  });

  it("时间一样时按成员顺序定 —— 同样的输入要画出同样的东西", () => {
    const build = () => {
      const p = new RowProjector("room", "报表", true);
      p.consumeMerged([
        { pid: "p1", speaker: "报表", events: [say("p1", 0, 100, "甲")] },
        { pid: "p2", speaker: "阿导", events: [say("p2", 0, 100, "乙")] },
      ]);
      return p.rows.map((r) => r.text);
    };
    expect(build()).toEqual(build());
    expect(build()).toEqual(["甲", "乙"]);
  });
});

describe("历史分批到达, 行也要按时间归位", () => {
  const say = (pid: string, seq: number, at: number, text: string) =>
    ({ pid, seq, at, kind: "proc.output", payload: { phase: "reply", text } }) as never;

  it("后到的旧事件不能就摞在末尾", () => {
    // 服务端按 pid 补齐, 一个进程一个进程地发, 顺序跟时间无关.
    // 刷新一次, 界面上就成了"最新的在上面, 更早的在下面".
    const p = new RowProjector("room", "报表", true);
    const late = [say("p1", 0, 900, "后说的")];
    const early: unknown[] = [];
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: late },
      { pid: "p2", speaker: "阿导", events: early as never },
    ]);
    expect(p.rows.map((r) => r.text)).toEqual(["后说的"]);
    // 现在旧进程那一批才补齐到
    (early as unknown[]).push(say("p2", 0, 100, "早说的"));
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: late },
      { pid: "p2", speaker: "阿导", events: early as never },
    ]);
    expect(p.rows.map((r) => r.text)).toEqual(["早说的", "后说的"]);
  });

  it("归位之后重算谁露脸 —— 顺序变了, 谁挨着谁就变了", () => {
    const p = new RowProjector("room", "报表", true);
    const a = [say("p1", 0, 900, "报表后一句")];
    const b: unknown[] = [];
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: a },
      { pid: "p2", speaker: "报表", events: b as never },
    ]);
    (b as unknown[]).push(say("p2", 0, 100, "报表前一句"));
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: a },
      { pid: "p2", speaker: "报表", events: b as never },
    ]);
    // 同一个人连着两句, 只有排在前面那句露脸
    expect(p.rows.map((r) => r.showWho)).toEqual([true, false]);
  });

  it("本来就有序就一个字节都不动", () => {
    const p = new RowProjector("room", "报表", true);
    p.consumeMerged([{ pid: "p1", speaker: "报表", events: [say("p1", 0, 100, "一"), say("p1", 1, 200, "二")] }]);
    const before = p.rows.map((r) => r.revision);
    p.consumeMerged([{ pid: "p1", speaker: "报表", events: [say("p1", 0, 100, "一"), say("p1", 1, 200, "二")] }]);
    expect(p.rows.map((r) => r.revision)).toEqual(before);
  });
});

describe("进程停了, 它正在流的那几行要封口", () => {
  const stream = (pid: string, seq: number, text: string) =>
    ({ pid, seq, at: seq, kind: "proc.output", payload: { stream: "s0", channel: "say", text } }) as never;
  const state = (pid: string, seq: number, st: string) =>
    ({ pid, seq, at: seq, kind: "proc.state", payload: { state: st } }) as never;

  it("说完转 waiting 就定稿 —— 开着的行按纯文本画, 不解析 markdown", () => {
    const p = new RowProjector("p1", "报表", false);
    p.consume([stream("p1", 0, "看 `report.py`"), state("p1", 1, "waiting")]);
    const said = p.rows.find((r) => r.kind === "said");
    expect(said?.open).toBe(false);
  });

  it("退出的进程也要封口 —— 否则它最后那段话永远是字面量", () => {
    const p = new RowProjector("p1", "报表", false);
    p.consume([stream("p1", 0, "干完了 **收工**"), state("p1", 1, "exited")]);
    expect(p.rows.find((r) => r.kind === "said")?.open).toBe(false);
  });

  it("还在写的时候不能提前封 —— 半个 ** 会被当成加粗", () => {
    const p = new RowProjector("p1", "报表", false);
    p.consume([stream("p1", 0, "干完了 **收")]);
    expect(p.rows.find((r) => r.kind === "said")?.open).toBe(true);
  });

  it("只封自己的 —— 同屋另一个人还在写", () => {
    const p = new RowProjector("room", "报表", true);
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: [stream("p1", 0, "我说完了")] },
      { pid: "p2", speaker: "阿导", events: [stream("p2", 0, "我还在写")] },
    ]);
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: [stream("p1", 0, "我说完了"), state("p1", 1, "waiting")] },
      { pid: "p2", speaker: "阿导", events: [stream("p2", 0, "我还在写")] },
    ]);
    const byWho = Object.fromEntries(p.rows.filter((r) => r.kind === "said").map((r) => [r.who, r.open]));
    expect(byWho).toEqual({ 报表: false, 阿导: true });
  });
});

describe("整条发出去的那种输出, 当场就该封口", () => {
  const chunk = (seq: number, text: string, done?: boolean) =>
    ({ pid: "p1", seq, at: seq, kind: "proc.output",
       payload: { stream: "wake-w1", channel: "say", text, ...(done === undefined ? {} : { done }) } }) as never;

  it("闹钟响一句就完了 —— 后面不会有块, 也不会有状态变化", () => {
    // 真机: 提醒是整条发出去的, 而且发的时候进程正停在 waiting.
    // 靠"下一次状态变化"关它的话, 那行永远开着 ——
    // 一句早就说完的提醒, 屁股后面挂着一根不会消失的光标.
    const p = new RowProjector("p1", "小记", false);
    p.consume([chunk(0, "⏰ 该验收定时器了。", true)]);
    expect(p.rows.find((r) => r.kind === "said")?.open).toBe(false);
  });

  it("没说 done 的照旧当流 —— 后面还能接上去", () => {
    const p = new RowProjector("p1", "小记", false);
    p.consume([chunk(0, "前半"), chunk(1, "后半")]);
    const said = p.rows.filter((r) => r.kind === "said");
    expect(said).toHaveLength(1);
    expect(said[0]?.text).toBe("前半后半");
    expect(said[0]?.open).toBe(true);
  });

  it("最后一块带 done, 接完就封 —— 不能再往里接", () => {
    const p = new RowProjector("p1", "小记", false);
    p.consume([chunk(0, "前半"), chunk(1, "后半", true), chunk(2, "又来一块")]);
    const said = p.rows.filter((r) => r.kind === "said");
    expect(said.map((r) => r.text)).toEqual(["前半后半", "又来一块"]);
    expect(said[0]?.open).toBe(false);
  });
});

describe("行换过位置要说出来", () => {
  const say = (pid: string, seq: number, at: number, text: string) =>
    ({ pid, seq, at, kind: "proc.output", payload: { phase: "reply", text } }) as never;

  it("归位一次, reordered 加一 —— 渲染那侧靠它知道该从头重排", () => {
    // offsets 是按位置索引的. 行数没变、内容没变, 只是顺序变了 ——
    // 渲染那侧看不出来, 于是接着用旧的 offsets: 行叠在一起, 不会自己好.
    const p = new RowProjector("room", "报表", true);
    const late = [say("p1", 0, 900, "后说的")];
    const early: unknown[] = [];
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: late },
      { pid: "p2", speaker: "阿导", events: early as never },
    ]);
    expect(p.reordered).toBe(0);
    (early as unknown[]).push(say("p2", 0, 100, "早说的"));
    p.consumeMerged([
      { pid: "p1", speaker: "报表", events: late },
      { pid: "p2", speaker: "阿导", events: early as never },
    ]);
    expect(p.reordered).toBe(1);
  });

  it("本来就有序就不加 —— 加了等于每帧全量重排", () => {
    const p = new RowProjector("room", "报表", true);
    const evs = [say("p1", 0, 100, "一"), say("p1", 1, 200, "二")];
    p.consumeMerged([{ pid: "p1", speaker: "报表", events: evs }]);
    p.consumeMerged([{ pid: "p1", speaker: "报表", events: evs }]);
    expect(p.reordered).toBe(0);
  });
});

describe("同一件事被报两遍, 只算一遍", () => {
  const CORE = "同一类错误连着出现 3 次了。用 find_files 搜一下。";

  it("stall 之后紧跟着的 turn_failed 只是换了个说法, 不该再来一张卡", () => {
    // 真机截图: 两张几乎一模一样的黄卡, 后一张只多了个"检测到停滞: "前缀,
    // 而那件事标题上已经写着了("这一轮我停下来了").
    const p = new RowProjector("p1", "值守", false);
    p.consume([
      out("p1", { phase: "stall", msg: CORE }),
      out("p1", { phase: "turn_failed", err: `检测到停滞: ${CORE}` }),
    ]);
    const alerts = p.rows.filter((r) => r.kind === "alert" && r.merged !== true);
    expect(alerts).toHaveLength(1);
    // 不是"发生过两次" —— 标次数就是在说假话
    expect(alerts[0]?.repeat ?? 1).toBe(1);
    // 留先到的那条: 后到的前缀跟标题重复
    expect(alerts[0]?.text).toBe(CORE);
  });

  it("一模一样的还是要标次数 —— 那是真的又发生了一次", () => {
    const p = new RowProjector("p1", "值守", false);
    p.consume([
      out("p1", { phase: "model_err", err: "供应商拒绝" }),
      out("p1", { phase: "model_err", err: "供应商拒绝" }),
      out("p1", { phase: "model_err", err: "供应商拒绝" }),
    ]);
    const alerts = p.rows.filter((r) => r.kind === "alert" && r.merged !== true);
    expect(alerts).toHaveLength(1);
    expect(alerts[0]?.repeat).toBe(3);
  });

  it("不相干的两条各占一张 —— 合并只针对同一件事", () => {
    const p = new RowProjector("p1", "值守", false);
    p.consume([
      out("p1", { phase: "model_err", err: "供应商拒绝: A" }),
      out("p1", { phase: "turn_failed", err: "步数用完了" }),
    ]);
    expect(p.rows.filter((r) => r.kind === "alert" && r.merged !== true)).toHaveLength(2);
  });
});

describe("中途插话不该把这一轮劈开", () => {
  it("插话之后才回来的结果, 要落回它自己那一次调用", () => {
    // 真机: 让它依次跑 5 条 sleep, 第 3 条跑到一半插话"打住".
    // 结果是第 3 条的结果在插话之后才回来 —— 组一关, 它就单独成了一组,
    // 界面上一共数出 4 次调用, 而它实际只跑了 3 次.
    const p = new RowProjector("p1", "小查", false);
    p.consume([
      out("p1", { phase: "step", tool: "run", args: { cmd: "sleep 3 && echo 1" } }),
      out("p1", { phase: "tool_ok", tool: "run", args: { cmd: "sleep 3 && echo 1" }, result: "1" }),
      out("p1", { phase: "step", tool: "run", args: { cmd: "sleep 3 && echo 2" } }),
      out("p1", { phase: "tool_ok", tool: "run", args: { cmd: "sleep 3 && echo 2" }, result: "2" }),
      out("p1", { phase: "step", tool: "run", args: { cmd: "sleep 3 && echo 3" } }),
      // ← 第 3 条还没回来, 用户插话
      { pid: "p1", seq: 90, at: Date.now(), kind: "input.recv", payload: { text: "打住，别跑了" } } as never,
      out("p1", { phase: "tool_ok", tool: "run", args: { cmd: "sleep 3 && echo 3" }, result: "3" }),
      out("p1", { phase: "reply", text: "跑到第 3 条" }),
    ]);
    const groups = p.rows.filter((r) => r.kind === "tools");
    expect(groups).toHaveLength(1);
    expect(groups[0]?.calls).toHaveLength(3);
  });

  it("说完一句再说下一句, 照旧是两轮 —— 轮次边界靠 running", () => {
    const p = new RowProjector("p1", "小查", false);
    p.consume([
      out("p1", { phase: "step", tool: "read_file", args: { path: "a" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "a" }, result: "…" }),
      out("p1", { phase: "reply", text: "看完了" }),
      { pid: "p1", seq: 91, at: Date.now(), kind: "input.recv", payload: { text: "再看一个" } } as never,
      { pid: "p1", seq: 92, at: Date.now(), kind: "proc.state", payload: { state: "running" } } as never,
      out("p1", { phase: "step", tool: "read_file", args: { path: "b" } }),
      out("p1", { phase: "tool_ok", tool: "read_file", args: { path: "b" }, result: "…" }),
    ]);
    expect(p.rows.filter((r) => r.kind === "tools")).toHaveLength(2);
  });
});

describe("插话被半路收到了要看得见", () => {
  it("收到就留一行 —— 分得清'没听见'和'听见了在收尾'", () => {
    // 中途插话是"劝"不是"停": OS 只保证送到步与步之间, 停不停它自己判断.
    // 界面上一个字都没有的话, 你说完"打住"它又跑了两步, 分不清哪种.
    // 而这两种情况该做的事完全相反: 前者再说一遍, 后者等一下.
    const p = new RowProjector("p1", "小查", false);
    p.consume([
      out("p1", { phase: "step", tool: "run", args: { cmd: "sleep 3" } }),
      { pid: "p1", seq: 90, at: Date.now(), kind: "input.recv", payload: { text: "打住" } } as never,
      out("p1", { phase: "tool_ok", tool: "run", args: { cmd: "sleep 3" }, result: "ok" }),
      out("p1", { phase: "interrupt", text: "打住", n: 3 }),
      out("p1", { phase: "reply", text: "停了" }),
    ]);
    const note = p.rows.find((r) => r.kind === "note");
    expect(note?.title).toBe("半路收到了");
    // **不能说成"已经停了"** —— 停不停是它的判断, 我们只保证话送到了
    expect(note?.text).toContain("再决定");
  });

  it("没插话就不该有这一行", () => {
    const p = new RowProjector("p1", "小查", false);
    p.consume([
      out("p1", { phase: "step", tool: "run", args: { cmd: "x" } }),
      out("p1", { phase: "tool_ok", tool: "run", args: { cmd: "x" }, result: "ok" }),
      out("p1", { phase: "reply", text: "好了" }),
    ]);
    expect(p.rows.find((r) => r.kind === "note")).toBeUndefined();
  });
});

describe("图", () => {
  it("我发的图跟这句话是同一条消息 —— 不另起一行", () => {
    const p = new RowProjector("p1");
    p.consume([{
      pid: "p1", seq: 0, at: 1, kind: "input.recv",
      payload: { text: "这个报错什么意思", from: "我", images: [{ id: "abc.png", name: "截图.png" }] }
    } as never]);
    const heard = p.rows.filter((r) => r.kind === "heard");
    expect(heard).toHaveLength(1);
    expect(heard[0]?.text).toBe("这个报错什么意思");
    expect(heard[0]?.shots).toEqual([{ id: "abc.png", name: "截图.png" }]);
  });

  it("没图的行不带这个字段 —— 免得每一行都拖着一个空数组", () => {
    const p = new RowProjector("p1");
    p.consume([{ pid: "p1", seq: 0, at: 1, kind: "input.recv", payload: { text: "在吗", from: "我" } } as never]);
    expect(p.rows[0]?.shots).toBeUndefined();
  });

  it("它看图那一次单独算一类 —— 那一条要把图画出来", () => {
    // 拿回来的只是一段转述, 而人一眼要判断的是"它看的是不是我说的那张"
    const p = new RowProjector("p1");
    p.consume([
      out("p1", { phase: "step", tool: "view_image", args: { path: "/tmp/a.png", question: "报错是什么" } }),
      out("p1", { phase: "tool_ok", tool: "view_image", result: "写着 ECONNREFUSED" }),
    ]);
    const tools = p.rows.find((r) => r.kind === "tools");
    expect(tools?.calls?.[0]?.kind).toBe("image");
  });
});

describe("撞上上限那张卡", () => {
  /**
   * 只显示"步数到上限了 · 止损线"等于什么都没说:
   * 用户既不知道花了多少, 也不知道那道线是他自己设的.
   */
  it("说清是花到上限了, 花了多少、上限多少", () => {
    const p = new RowProjector("p1");
    p.consume([out("p1", {
      phase: "step_limit", why: "止损线",
      detail: "这一轮已经花掉 15k token，超过你在设置里定的每轮上限 3k。先收尾说清做到哪儿了",
      msg: "到止损线了"
    })]);
    const alert = p.rows.find((r) => r.kind === "alert");
    expect(alert?.title).toBe("花到上限了");
    expect(alert?.text).toContain("15k");
    expect(alert?.text).toContain("上限 3k");
  });
});
