import { describe, expect, it } from "vitest";

import { liftInlineCards } from "../rows.js";

/**
 * 正文里手写的卡片标记.
 *
 *   模型可能在正文里手写卡片标记; 若只接收 show 工具输出的卡片,
 *   屏幕上就只剩一段 JSON. 因此测试正文标记也能提取成卡片, 且保留其余文字.
 */
describe("liftInlineCards", () => {
  it("抠出卡片, 话留着", () => {
    const got = liftInlineCards(
      '[[card: progress {"title":"提醒已设","done":1,"total":1}]]\n\n提醒设好了。'
    );
    expect(got.specs).toHaveLength(1);
    expect(got.specs[0]!.type).toBe("progress");
    expect(got.text).toBe("提醒设好了。");
  });

  it("没标记就原样", () => {
    expect(liftInlineCards("就一句话。")).toEqual({ text: "就一句话。", specs: [] });
  });

  // 找 ']]' 那种写法会在这儿断在半路, 后半段话跟着消失
  it("JSON 里带 ]] 也认得出边界", () => {
    const got = liftInlineCards('[[card: table {"rows":[["a","b"]]}]]后面还有话');
    expect(got.specs).toHaveLength(1);
    expect(got.text).toBe("后面还有话");
  });

  // 解不开的**原样留着**: 半个标记也比无声吞掉一段话强
  it("解不开就不动它", () => {
    const got = liftInlineCards("[[card: progress {坏的]] 后面的话");
    expect(got.specs).toHaveLength(0);
    expect(got.text).toContain("后面的话");
  });

  it("每张卡都有 id —— 渲染端靠它认卡片", () => {
    const got = liftInlineCards('[[card: doc {"body":"一"}]][[card: doc {"body":"二"}]]');
    expect(got.specs.map((s) => s.id)).toEqual(["inline-0-doc", "inline-1-doc"]);
  });
});
