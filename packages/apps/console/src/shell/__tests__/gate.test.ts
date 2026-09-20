import { describe, expect, it } from "vitest";

import { tokenFromPaste } from "../Gate.js";

/**
 * 贴进门缝的东西长什么样, 由启动日志决定 —— 用户是从那儿复制的.
 * 三种都得认: 整条 URL、带引号/空格的整条 URL、裸 token.
 */
describe("钥匙的各种贴法", () => {
  it("整条启动日志里的 URL", () => {
    expect(tokenFromPaste("http://localhost:7717/?token=abc123")).toBe("abc123");
  });

  it("带空格的(终端里双击选中常带)", () => {
    expect(tokenFromPaste("  http://127.0.0.1:7718/?token=deadbeef  ")).toBe("deadbeef");
  });

  it("裸 token", () => {
    expect(tokenFromPaste("88013f3b960a3e71ebc34d5bf27fb838")).toBe("88013f3b960a3e71ebc34d5bf27fb838");
  });

  it("URL 上没有 token 参数 —— 原样当 token 用, 让服务端说不对", () => {
    expect(tokenFromPaste("http://localhost:7717/")).toBe("http://localhost:7717/");
  });

  it("空的就是空的", () => {
    expect(tokenFromPaste("   ")).toBe("");
  });
});
