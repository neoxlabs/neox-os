import { describe, expect, it } from "vitest";

import { unwrapSaid } from "../envelope.js";

describe("信封拆掉再显示", () => {
  it("旧账本里存着的那种, 原样是一屏 \\n 字面量", () => {
    // 真机截图里的原文(截短): 结尾挂着 "} , 中间全是 \n 字面量
    const raw = '{"said":"复现完成。\\n\\n**本轮改了三处**\\n按定稿行为重读即可。"}';
    expect(unwrapSaid(raw)).toBe("复现完成。\n\n**本轮改了三处**\n按定稿行为重读即可。");
  });

  it("多个字段就别动 —— 它可能真的想给你看一段 JSON", () => {
    const raw = '{"said":"好了","tool_calls":[]}';
    expect(unwrapSaid(raw)).toBe(raw);
  });

  it("解不开就原样, 宁可漏一条也不能把正常内容切坏", () => {
    const raw = '{"said":"引号没转义 " 就在这儿"}';
    expect(unwrapSaid(raw)).toBe(raw);
  });

  it("不是信封的键不拆", () => {
    const raw = '{"version":"3.4.0"}';
    expect(unwrapSaid(raw)).toBe(raw);
  });

  it("普通一句话原样过", () => {
    expect(unwrapSaid("改完了，已确认生效。")).toBe("改完了，已确认生效。");
  });

  it("一段真的 JSON 内容不该被当成信封", () => {
    const raw = '{"name":"导出","role":"负责导出"}';
    expect(unwrapSaid(raw)).toBe(raw);
  });

  it("数组不是信封", () => {
    expect(unwrapSaid('["a","b"]')).toBe('["a","b"]');
  });
});

describe("信封后面还跟着一段话", () => {
  it("拆开信封, 后面那段原样接回去", () => {
    // 真机截图里的原样: 那串 JSON 摆在眼前, 而下面那句话看着像另一个人说的
    const raw = '{"said":"已把这句话原样转给 OA助手。"}\n\n他说会再转给屋里另一个人。';
    expect(unwrapSaid(raw)).toBe("已把这句话原样转给 OA助手。\n\n他说会再转给屋里另一个人。");
  });

  it("正文里带 } 也不能断错 —— 只数括号会在那儿停住", () => {
    const raw = '{"said":"改完了}"}\n\n后面这句还在。';
    expect(unwrapSaid(raw)).toBe("改完了}\n\n后面这句还在。");
  });

  it("转义的引号不算字符串结束", () => {
    const raw = '{"said":"他说\\"好\\"就走了"}\n\n完。';
    expect(unwrapSaid(raw)).toBe('他说"好"就走了\n\n完。');
  });

  it("没闭合的就原样 —— 宁可漏一条", () => {
    const raw = '{"said":"写到一半就断了';
    expect(unwrapSaid(raw)).toBe(raw);
  });

  it("后面跟着的是真 JSON 内容也不动 —— 多字段本来就不拆", () => {
    const raw = '{"name":"导出","role":"做导出"}\n\n上面是我要建的人。';
    expect(unwrapSaid(raw)).toBe(raw);
  });
});
