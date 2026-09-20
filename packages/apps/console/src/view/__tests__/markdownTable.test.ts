import { describe, expect, it } from "vitest";

import { takeTable } from "../markdownTable.js";

/**
 * 这一组的输入取自真机: 一个房间里三个 bot 协作时, 63 条回话里有 4 条
 * 含表格 —— 而渲染器不认表格, 用户看到的是一屏 |---|---| 的字面量,
 * 偏偏那正是最该看清楚的地方(逐条核对的期望/实际对照表).
 */
describe("takeTable", () => {
  it("认得真机上漏出来的那张对照表", () => {
    const lines = [
      "逐条核对：",
      "",
      "| 草稿原句 | release.sh 实际 | 相符 |",
      "|---|---|---|",
      "| 软链接判失败 | `[ -L \"$VERSION_FILE\" ]` 各自判失败并 exit 1 | ✓ |",
      "| 版本号格式 X.Y.Z | `grep -qE '^[0-9]+'` | ✓ |",
      "",
      "另两处非校验行为也顺带核了：",
    ];
    const table = takeTable(lines, 2);
    expect(table).not.toBeNull();
    expect(table?.head).toEqual(["草稿原句", "release.sh 实际", "相符"]);
    expect(table?.body).toHaveLength(2);
    expect(table?.body[0]?.[2]).toBe("✓");
    // 表到空行为止, 后面那句话不许被吃进来
    expect(table?.next).toBe(6);
  });

  it("对齐语法要认", () => {
    const table = takeTable(["| a | b | c |", "|:---|:---:|---:|", "| 1 | 2 | 3 |"], 0);
    expect(table?.align).toEqual(["left", "center", "right"]);
  });

  it("判据是分隔线, 不是竖线", () => {
    // 正文里带一根竖线的句子太常见了. 拿"这行有 |"当判据,
    // 普通段落会被吃成表格
    expect(takeTable(["用 a | b 这种写法", "下一句话"], 0)).toBeNull();
    expect(takeTable(["| 只有表头 |", "后面没有分隔线"], 0)).toBeNull();
  });

  it("表头和分隔线列数对不上就当它不是表", () => {
    // 渲染出一张错位的表, 比原样显示字面量更难看懂
    expect(takeTable(["| a | b | c |", "|---|---|", "| 1 | 2 |"], 0)).toBeNull();
  });

  it("正文行少一根竖线就补空, 不为它把整张表退回字面量", () => {
    const table = takeTable(["| a | b | c |", "|---|---|---|", "| 1 | 2 |"], 0);
    expect(table?.body[0]).toEqual(["1", "2", ""]);
  });

  it("正文行多出来的列截掉, 不让表错位", () => {
    const table = takeTable(["| a | b |", "|---|---|", "| 1 | 2 | 3 |"], 0);
    expect(table?.body[0]).toEqual(["1", "2"]);
  });
});
