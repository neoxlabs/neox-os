import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { checkupSeen, markCheckupSeen, fetchToolchain, type Toolchain } from "../Checkup.js";

/**
 * 自检这张卡最要紧的两件事都不在渲染上:
 *   ① **够不着就当没这回事** —— 老版本 OS 没有 /toolchain, 合成源更没有.
 *      这条路上任何一种失败都不该让首启卡住或弹一张空卡.
 *   ② **看过就不再拦路** —— 它是首启问一次的东西, 不是每次开机的关卡.
 */

const store = new Map<string, string>();

beforeEach(() => {
  store.clear();
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => { store.set(k, v); },
    removeItem: (k: string) => { store.delete(k); },
  });
});
afterEach(() => { vi.unstubAllGlobals(); });

describe("看过就不再拦路", () => {
  it("没看过时是 false", () => {
    expect(checkupSeen()).toBe(false);
  });

  it("记下之后就是 true", () => {
    markCheckupSeen();
    expect(checkupSeen()).toBe(true);
  });

  it("localStorage 抛错(隐私模式)也不该炸 —— 大不了每次都问", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => { throw new Error("拒绝"); },
      setItem: () => { throw new Error("拒绝"); },
    });
    expect(() => markCheckupSeen()).not.toThrow();
    expect(checkupSeen()).toBe(false);
  });
});

describe("问 OS 脚下有什么", () => {
  const endpoint = { url: "http://127.0.0.1:7717", token: "k" };

  it("问到了就原样带回来", async () => {
    const body: Toolchain = {
      platform: "darwin",
      tools: [{ name: "git", have: false, core: true, what: "工件以它为准", fix: "xcode-select --install" }],
      missing: 1,
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => body }));
    expect(await fetchToolchain(endpoint)).toEqual(body);
  });

  it("带上 token —— 这个口子也在 guard 后面", async () => {
    const spy = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ platform: "linux", tools: [], missing: 0 }) });
    vi.stubGlobal("fetch", spy);
    await fetchToolchain(endpoint);
    const [url, init] = spy.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("http://127.0.0.1:7717/toolchain");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer k");
  });

  it("老版本 OS 不认这个口(404) → null, 不是抛错", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 404 }));
    expect(await fetchToolchain(endpoint)).toBeNull();
  });

  it("根本够不着 → null", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("连不上")));
    expect(await fetchToolchain(endpoint)).toBeNull();
  });

  it("没 endpoint(界面由 OS 自己 serve)走相对路径, 不带头", async () => {
    const spy = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ platform: "linux", tools: [], missing: 0 }) });
    vi.stubGlobal("fetch", spy);
    await fetchToolchain(null);
    const [url] = spy.mock.calls[0] as [string];
    expect(url).toBe("/toolchain");
  });
});
