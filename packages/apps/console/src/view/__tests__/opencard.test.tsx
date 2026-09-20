import { describe, expect, it } from "vitest";

import { renderToStaticMarkup } from "react-dom/server";

import { renderWidget } from "../widgets.js";
import type { WidgetContext } from "../widgets.js";

const ctx: WidgetContext = { respond() {}, answerOf() { return undefined; } };

/** 真渲染一遍 —— 断言的是**用户看得见的那段 HTML**, 不是组件树的形状 */
function textIn(node: unknown): string[] {
  return [renderToStaticMarkup(node as never)];
}

describe("认不出的卡片", () => {
  /**
   * 原来遇上不认识的种类只画一句"还不认识 xxx 卡片" —— **模型说的东西
   * 整个没了**, 而它收到的是"已展示", 于是接着说"链接见上".
   */
  it("内容一个字都不能丢", () => {
    const words = textIn(renderWidget(
      { type: "flight", id: "w1", title: "MU5100", from: "虹桥", to: "首都", url: "https://x.com/f" } as never,
      ctx
    ));
    const all = words.join(" ");
    for (const want of ["MU5100", "虹桥", "首都", "https://x.com/f"]) {
      expect(all).toContain(want);
    }
  });

  it("**种类要照实写出来**: 少的是样子, 不是内容 —— 用户得知道这一点", () => {
    const all = textIn(renderWidget({ type: "flight", id: "w1", title: "MU5100" } as never, ctx)).join(" ");
    expect(all).toContain("flight");
  });

  it("认识的那些照旧走自己的渲染", () => {
    const all = textIn(renderWidget({ type: "link", id: "w2", url: "https://neox.dev/a", title: "Neox" } as never, ctx)).join(" ");
    expect(all).toContain("Neox");
    // 通用卡会把 url 当一行文字画出来; 专门那张画成可点的标题
    expect(all).not.toContain("open__row");
  });

  it("嵌套对象压成一行, 不长成一棵树", () => {
    const all = textIn(renderWidget(
      { type: "怪东西", id: "w3", meta: { a: 1, b: "二", deep: { c: 3 } } } as never, ctx
    )).join(" ");
    expect(all).toContain("a: 1");
    expect(all).toContain("b: 二");
  });
});

describe("认不出的卡片也得像张卡", () => {
  /**
   * 只保内容不丢的话, 出来的是一摞 `键: 值` —— **那是调试输出**.
   * 用户的原话: "太粗糙, 不够动感精致, 缺少飞机火车等各种插画".
   */
  it("按词认出这是一件什么事 —— 航班给飞机, 高铁给火车", () => {
    const plane = textIn(renderWidget({ type: "flight", id: "w", title: "MU5100" } as never, ctx))[0]!;
    const train = textIn(renderWidget({ type: "高铁票", id: "w", title: "G7" } as never, ctx))[0]!;
    // 图形不一样, 色相也不一样 —— 两张卡摆一起要一眼分得开
    expect(plane).not.toBe(train);
    expect(plane).toContain("206");   // 飞机是蓝的
    expect(train).toContain("268");   // 火车是紫的
  });

  it("**种类认不出就看字段名**: 卡里写着 flight number, 那就是航班", () => {
    // 卡片是 bot 现编的, 它起的名字五花八门, 而字段名往往更诚实
    expect(textIn(renderWidget(
      { type: "itinerary", id: "w", flight_no: "MU5100", from: "虹桥", to: "首都" } as never, ctx))[0])
      .toContain("206");
  });

  it("认不出坐什么去的, 也别给方块 —— 有起点终点那至少是一段路", () => {
    // 图钉是诚实的("这是一段路"), 而方块什么都没说
    expect(textIn(renderWidget(
      { type: "itinerary", id: "w", departure: "08:20", from: "虹桥", to: "首都" } as never, ctx))[0])
      .toContain("--g:4");
  });

  it("有起点终点就画成一条行程线, 不是两行字", () => {
    const out = textIn(renderWidget(
      { type: "flight", id: "w", from: "虹桥 T2", to: "首都 T3", depart: "08:20", arrive: "11:05" } as never, ctx))[0]!;
    expect(out).toContain("trip__line");
    expect(out).toContain("虹桥 T2");
    expect(out).toContain("首都 T3");
    // 时间跟着两端走, 不另占一格
    expect(out).toContain("trip__at");
  });

  it("认不出是什么就给中性的那个, **不硬套一个错的**", () => {
    // 一张画着飞机的酒店卡比没有图形更糟
    expect(textIn(renderWidget({ type: "怪东西", id: "w", a: 1 } as never, ctx))[0]).toContain("220");
  });

  it("链接收成一行按钮, 不把整条 url 铺在卡底下", () => {
    const out = textIn(renderWidget(
      { type: "flight", id: "w", url: "https://flightaware.com/live/flight/MU5100/history" } as never, ctx))[0]!;
    expect(out).toContain("open__go");
    expect(out).toContain("flightaware.com");
    // 整条 url 还在 href 里 —— 点得到, 只是不铺在脸上
    expect(out).toContain("history");
  });
});

describe("卡面上那一大张图形", () => {
  it("有它 —— 一张小徽章说不出「这是一趟航班」", () => {
    // 用户的原话: "整个大的高铁飞机等元素都太少了"
    expect(textIn(renderWidget({ type: "flight", id: "w", title: "MU5100" } as never, ctx))[0])
      .toContain("open__art");
  });

  it("有封面图的那张不画 —— 两张图叠在一起谁都看不清", () => {
    expect(textIn(renderWidget(
      { type: "flight", id: "w", title: "MU5100", image: "https://x/cover.jpg" } as never, ctx))[0])
      .not.toContain("open__art");
  });
});
