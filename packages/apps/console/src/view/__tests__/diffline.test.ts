import { describe, expect, it } from "vitest";

import { diffKind, diffLines } from "../diffline.js";

describe("diff 里每一行是什么", () => {
  it("加和减各自认出来 —— 那是这张卡存在的唯一理由", () => {
    expect(diffKind("+    return 400")).toBe("add");
    expect(diffKind("-    return 400")).toBe("del");
  });

  it("文件头不是加减 —— 它也以 --- / +++ 开头", () => {
    // 染成"删了一行/加了一行"等于在最显眼的位置说了两句假话
    expect(diffKind("--- a/app/routes/attendance.py")).toBe("meta");
    expect(diffKind("+++ b/app/routes/attendance.py")).toBe("meta");
  });

  it("位置头也是 meta", () => {
    expect(diffKind("@@ -87,3 +87,3 @@")).toBe("meta");
    expect(diffKind("diff --git a/x b/x")).toBe("meta");
    expect(diffKind("index 1234..5678 100644")).toBe("meta");
  });

  it("上下文行原样", () => {
    expect(diffKind("     if late:")).toBe("same");
    expect(diffKind("")).toBe("same");
  });

  it("整段拆出来行数不变 —— 少一行就是把内容吃了", () => {
    const text = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n context";
    const rows = diffLines(text);
    expect(rows).toHaveLength(6);
    expect(rows.map((r) => r.kind)).toEqual(["meta", "meta", "meta", "del", "add", "same"]);
  });
});
