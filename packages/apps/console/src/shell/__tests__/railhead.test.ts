import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

const css = readFileSync(join(__dirname, "../console.css"), "utf8");
const main = readFileSync(join(__dirname, "../../../electron/main.cjs"), "utf8");

/**
 * 侧栏头的高度是**跨进程的约定**, 所以它不能被挤.
 *
 *   红绿灯不归页面管: Electron 按窗口坐标把它摆在 trafficLightPosition,
 *   跟 DOM 里发生什么无关. 页面这边只有一次机会把品牌名摆到同一条水平线上 ——
 *   就是让那个头保持它声明的高度.
 *
 *   .rail 是 flex column, 缺省 flex-shrink:1. 会话一多、列表撑不下,
 *   两个头就按比例缩水 —— 高度可从 52 缩到 34.7, 于是字标中心跑到 17,
 *   而灯还在 25. **会话越多越歪**, 而且页面内截图根本看不见(不含窗口边框),
 *   检查这种错位需要包含窗口边框的截图, 因此测试同时约束高度和窗口坐标.
 */
describe("侧栏头不许被挤扁", () => {
  it(".rail__top 和 .rail__view 都关掉了 flex-shrink", () => {
    for (const sel of [".rail__top", ".rail__view"]) {
      const i = css.indexOf(sel + " ");
      expect(i, `${sel} 这条规则不见了`).toBeGreaterThan(-1);
      const rule = css.slice(i, css.indexOf("}", i));
      expect(rule, `${sel} 会被列表挤扁, 品牌名就跟红绿灯错位`).toMatch(/flex-shrink:\s*0/);
    }
  });

  // 高度和灯的位置必须一起看 —— 只改一边就是把错位换个方向
  it("头的高度跟红绿灯的落点仍然对得上", () => {
    const h = Number(/\.rail__top\s*\{[^}]*height:\s*(\d+)px/.exec(css)?.[1]);
    const y = Number(/trafficLightPosition:\s*\{[^}]*y:\s*(\d+)/.exec(main)?.[1]);
    expect(h, "读不到侧栏头的高度").toBeGreaterThan(0);
    expect(y, "读不到红绿灯的位置").toBeGreaterThan(0);
    // 灯本身约 14px 高, 中心 ≈ y + 7; 头的中心是 h/2. 差 2px 以内算齐
    expect(Math.abs(h / 2 - (y + 7))).toBeLessThanOrEqual(2);
  });
});
