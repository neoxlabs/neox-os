import { describe, expect, it } from "vitest";

import { waitingHint, type Runner } from "../waiting.js";
import type { Row } from "../rows.js";

function row(part: Partial<Row> & { pid: string; kind: Row["kind"] }): Row {
  return { id: "r", at: 0, seq: 0, index: 0, text: "", revision: 0, open: false, ...part } as Row;
}
const on = (name: string, state: string): Runner => ({ pid: name, name, state });
const running = (kind?: string): NonNullable<Row["calls"]> =>
  [{ name: "t", arg: "", running: true, ...(kind === undefined ? {} : { kind }) }] as NonNullable<Row["calls"]>;

describe("底下那条占位", () => {
  it("它一动手占位就不能消失 —— 那正是最需要占位的一分半钟", () => {
    // 真机截图: 我 @ 了它, 它回了一行"用了 2 个工具", 然后界面一片空白.
    // 原来的判据("最后一行是我说的话")在它动手的那一刻就失效了.
    const rows = [row({ pid: "OA助手", kind: "tools", calls: running("run") })];
    expect(waitingHint([on("OA助手", "running")], rows)?.label).toBe("正在跑命令…");
  });

  it("**话一出来就撤** —— 收尾还要跑很久, 但屏幕上已经有字了", () => {
    // 它开口之后这一轮往往还没走完(自动提交、下一步工具), 进程还是 running.
    // 这时候再挂一条"正在想…"就是在回答一个已经被回答了的问题,
    // 而且会一直挂到整轮结束. 用户: "正在想显示了很久才消失".
    const rows = [
      row({ pid: "小登", kind: "tools", calls: running("run") }),
      row({ pid: "小登", kind: "said", text: "跑完了，退出码 0" }),
    ];
    expect(waitingHint([on("小登", "running")], rows)).toBeUndefined();
  });

  /**
   * 群里: 甲刚回完话, 乙丙正在跑命令 —— **占位不能跟着甲一起撤**.
   *	否则 4 条 sleep 在跑时, 界面上一个字都没有, 连那颗"停"也没了.
   */
  it("别人刚说完话, 不影响正在干活的那几个", () => {
    const rows = [
      row({ pid: "甲", kind: "said", text: "我这块做完了" }),
    ];
    const got = waitingHint([on("甲", "waiting"), on("乙", "running"), on("丙", "running")], rows);
    expect(got?.label).toBe("2 个人正在干活…");
    expect(got?.busy).toEqual(["乙", "丙"]);
  });

  it("说完又动手, 占位立刻回来 —— 那说的是新的一件事", () => {
    const rows = [
      row({ pid: "小登", kind: "said", text: "我先看一眼" }),
      row({ pid: "小登", kind: "tools", calls: running("read") }),
    ];
    expect(waitingHint([on("小登", "running")], rows)?.label).toBe("正在翻文件…");
  });

  it("占位也摆脸 —— 群里三个人时光一句「正在想…」等于没说", () => {
    const rows = [row({ pid: "小登", kind: "tools", calls: running("run") })];
    expect(waitingHint([on("小登", "running"), on("接口", "waiting")], rows)?.who).toBe("小登");
  });

  it("按下回车到进程转 running 之间也要有占位 —— 那几百毫秒人正盯着屏幕", () => {
    // 消息已经发出但进程尚未转为 running 时, 界面不能出现两帧空白
    const rows = [row({ pid: "我", kind: "heard", text: "你在干嘛" })];
    const got = waitingHint([on("小登", "waiting")], rows);
    expect(got?.label).toBe("正在想…");
    // 一对一时对面只有一个人, 那张脸不会认错
    expect(got?.who).toBe("小登");
  });

  it("认不出是谁就不摆脸 —— 宁可空着也不摆错一张", () => {
    const rows = [row({ pid: "我", kind: "heard", text: "谁来接一下" })];
    // 群里没点名, 还没人接手: 摆谁的脸都是猜的
    expect(waitingHint([on("小登", "waiting"), on("接口", "waiting")], rows)?.who).toBeUndefined();
    // 点了两个人也一样偏 —— 一秒之内他们自己就动起来了, 不急这一下
    const two = [row({ pid: "我", kind: "heard", text: "@小登 @接口 你们看看" })];
    expect(waitingHint([on("小登", "waiting"), on("接口", "waiting")], two)?.who).toBeUndefined();
    // 好几个人同时在干也一样, 摆谁的都是错的
    expect(waitingHint([on("小登", "running"), on("接口", "running")], [])?.who).toBeUndefined();
  });

  it("群里点了谁的名, 就先摆谁的脸 —— 不然那一秒是张没主人的空位", () => {
    const rows = [row({ pid: "我", kind: "heard", text: "@OA助手 你在干嘛" })];
    expect(waitingHint([on("OA助手", "waiting"), on("小勤", "waiting")], rows)?.who).toBe("OA助手");
  });

  it("手上有工具还在跑就算在干活 —— 进程状态比事件慢一拍", () => {
    // 工具行都画出来了, 进程状态还写着"在线". 只信状态的话
    // 那一拍就是一次闪烁: 有 → 没 → 有
    const rows = [row({ pid: "小登", kind: "tools", calls: running("run") })];
    expect(waitingHint([on("小登", "waiting")], rows)?.label).toBe("正在跑命令…");
  });

  it("停在 Recv 上不给占位 —— 一直亮着的占位等于没有占位", () => {
    // waiting 是 bot 闲着的常态, 不是"在忙"
    const done = [row({ pid: "OA助手", kind: "tools", calls: [{ name: "t", arg: "" }] })];
    expect(waitingHint([on("OA助手", "waiting")], done)).toBeUndefined();
    expect(waitingHint([on("OA助手", "exited")], [])).toBeUndefined();
    expect(waitingHint([], [])).toBeUndefined();
  });

  it("说清在干哪一类事: 该等一会儿还是该等很久", () => {
    const kinds: [NonNullable<Row["calls"]>, string][] = [
      [running("write"), "正在改文件…"],
      [running("read"), "正在翻文件…"],
      [running("search"), "正在找东西…"],
      // 认不出是哪一类的不硬编一个名字 —— 说错比说笼统糟
      [running(), "正在干活…"],
      // 都跑完了却还没回话 = 它在消化结果, 那就是在想
      [[{ name: "run", arg: "", kind: "run" }], "正在想…"],
    ];
    for (const [calls, want] of kinds) {
      expect(waitingHint([on("小登", "running")], [row({ pid: "小登", kind: "tools", calls })])?.label).toBe(want);
    }
  });

  it("好几个人同时在干就报个数 —— 一个个念出来占满一行", () => {
    const many = [on("小登", "running"), on("接口", "running"), on("新来的", "running")];
    expect(waitingHint(many, [])?.label).toBe("3 个人正在干活…");
  });

  /**
   * **几个人同时跑飞的时候最需要刹车**, 而那正是 who 为空的时候 ——
   * 原来那颗"停"就跟着一起没了.
   */
  it("在干活的都要报上名 —— 刹车得按得着他们", () => {
    const many = [on("小登", "running"), on("接口", "running"), on("闲着的", "waiting")];
    expect(waitingHint(many, [])?.busy).toEqual(["小登", "接口"]);
    // 一个人的时候也在名单里, 按钮走同一条路
    expect(waitingHint([on("小登", "running")], [])?.busy).toEqual(["小登"]);
  });

  it("只看他自己的行: 别人在跑命令不代表他在跑命令", () => {
    const rows = [row({ pid: "接口", kind: "tools", calls: running("run") })];
    // 小登在干活, 但界面上最后那条过程行是接口的 —— 拿它描述小登就是撒谎
    expect(waitingHint([on("小登", "running"), on("接口", "waiting")], rows)?.label).toBe("正在想…");
  });

  it("并掉的行不算数 —— 它根本没画出来", () => {
    const rows = [row({ pid: "小登", kind: "tools", merged: true, calls: running("run") })];
    expect(waitingHint([on("小登", "running")], rows)?.label).toBe("正在想…");
  });
});

// 他忙着的时候你插了话 —— 占位要变成回执, 不能让人对着"正在想"干等
import { describe as d2, expect as e2, it as i2 } from "vitest";
d2("插话要有回执", () => {
  i2("单人忙 + 最后一行是用户的话 → 看到你的话了", () => {
    const got = waitingHint(
      [{ pid: "p1", name: "小海", state: "running" }],
      [{ id: "a", pid: "p1", at: 1, seq: 0, revision: 0, open: false, kind: "heard", text: "你怎么没拉他进群?" } as never]
    );
    e2(got?.label).toContain("看到你的话了");
    e2(got?.busy).toEqual(["小海"]);
  });
  i2("没插话时照旧说在干什么", () => {
    const got = waitingHint(
      [{ pid: "p1", name: "小海", state: "running" }],
      []
    );
    e2(got?.label).not.toContain("看到你的话了");
  });
});
