import { describe, expect, it } from "vitest";

import { isShot, MAX_SHOT_BYTES, readShots } from "../shots.js";

function fakeFile(name: string, type: string, size: number): File {
  return { name, type, size, lastModified: 0 } as File;
}

describe("挑图", () => {
  it("只认那几种 —— 拖一个 zip 进来不该变成发不出去的附件", () => {
    expect(isShot({ type: "image/png" })).toBe(true);
    expect(isShot({ type: "image/jpeg" })).toBe(true);
    expect(isShot({ type: "application/zip" })).toBe(false);
    // heic 认不了: 存下来 bot 也打不开(go/osinit/blobs.go 只认那四种)
    expect(isShot({ type: "image/heic" })).toBe(false);
  });

  it("**没收下的要说出来**, 不能静默丢", async () => {
    const [ok, bad] = await readShots([
      fakeFile("笔记.zip", "application/zip", 10),
      fakeFile("大图.png", "image/png", MAX_SHOT_BYTES + 1),
    ]);
    expect(ok).toHaveLength(0);
    expect(bad).toHaveLength(2);
    expect(bad[0]).toContain("不是图片");
    // 说清是多大、上限是多少 —— "失败了"这三个字帮不了任何人
    expect(bad[1]).toContain("6MB");
  });
});
