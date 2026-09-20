import { describe, expect, it } from "vitest";

import { plainLine } from "../plainline.js";

describe("侧栏那一行", () => {
  it("换行符不许露在外面", () => {
    // 侧栏接收的内容可能包含换行和 Markdown 列表；压平后应只保留可读文字。
    const got = plainLine("完成了：\n\n- `g.txt` 写入 `hello`\n- 跑过了");
    expect(got).not.toContain("\n");
    expect(got).toBe("完成了： g.txt 写入 hello 跑过了");
  });

  it("markdown 记号脱掉, 文字留下", () => {
    expect(plainLine("## 交付内容\n\n**考勤模块**做完了")).toBe("交付内容 考勤模块做完了");
  });

  it("代码块整段去掉 —— 它在一行里没有任何可读性", () => {
    expect(plainLine("用法：\n```sh\n./run.sh --port 8080\n```\n跑起来了")).toBe("用法： 跑起来了");
  });

  it("表格整行去掉 —— 一行竖线在侧栏里什么都不是", () => {
    const got = plainLine("结果：\n| 功能 | 状态 |\n|---|---|\n| 打卡 | 完成 |\n全过了");
    expect(got).toBe("结果： 全过了");
  });

  it("只做减法, 不改写句子", () => {
    // 重写会出错, 而出错的摘要看起来像 bot 说过这句话
    const said = "考勤模块做完并跑通了。";
    expect(plainLine(said)).toBe(said);
  });

  it("超长的截断, 但不在这儿加省略号 —— 那是 CSS 的活", () => {
    const long = "很长".repeat(100);
    expect(plainLine(long).length).toBeLessThanOrEqual(80);
    expect(plainLine(long)).not.toContain("…");
  });
});

describe("拼接顺序", () => {
// **先压平再拼名字**: 反过来的话第一行的记号就不在行首, 脱不掉.
//
// 如果先加前缀，标题记号就不在行首：第二行的 ### 会被脱掉，
// 第一行的 ## 会因为前面多了 "回归: " 而保留下来。
it("发言人前缀要在压平之后加", () => {
  const raw = "## 渲染验收\n\n### 格式清单\n\n表格、标题都验了";
  const wrong = plainLine(`回归: ${raw}`);
  const right = `回归: ${plainLine(raw)}`;
  expect(wrong).toContain("##");   // 反过来做就是这样
  expect(right).not.toContain("#"); // 正确的顺序
});
});
