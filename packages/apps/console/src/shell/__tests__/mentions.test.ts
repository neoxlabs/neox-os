import { describe, expect, it } from "vitest";

import { EVERYONE_NAME, mentionChoices } from "../mentions.js";

describe("@ 能点到谁", () => {
  it("房间里把「所有人」也列出来 —— 不列的话只有读源码才知道有全屋这一档", () => {
    expect(mentionChoices(["小勤", "小查"], true)).toContain(EVERYONE_NAME);
  });

  it("「所有人」排在最后 —— 第一项是默认高亮的, 手滑回车不该变成全屋广播", () => {
    const list = mentionChoices(["小勤", "小查"], true);
    expect(list[list.length - 1]).toBe(EVERYONE_NAME);
    expect(list[0]).toBe("小勤");
  });

  it("一对一不给点名 —— 对面只有一个人", () => {
    expect(mentionChoices(["小记"], false)).toEqual([]);
  });
});
