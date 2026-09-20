import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { renderWidget } from "../widgets.js";
import type { WidgetContext } from "../widgets.js";

const ctx: WidgetContext = {
  respond() {}, answerOf() { return undefined; },
  shotSrc: { byPath: (path) => `http://os/blob?path=${encodeURIComponent(path)}` }
};
const html = (spec: Record<string, unknown>) => renderToStaticMarkup(renderWidget(spec as never, ctx) as never);

describe("多模态卡片", () => {
  /**
   * **本地路径要交给 OS 那侧代取**: 浏览器打不开 file://, 而 bot 手上
   * 有的通常正是一条工作区里的路径.
   */
  it("图/声音/视频给 path 就走 OS 代取那条", () => {
    for (const kind of ["image", "audio", "video"]) {
      expect(html({ type: kind, id: "w", path: "/工作区/a.png" }))
        .toContain("blob?path=%2F%E5%B7%A5%E4%BD%9C%E5%8C%BA%2Fa.png");
    }
    // 给了 http/data 的就原样用, 不往 OS 那边绕
    expect(html({ type: "image", id: "w", src: "data:image/png;base64,AAA" })).toContain("data:image/png");
  });

  it("网页卡: 一整张能点, 而且**开在系统浏览器里**", () => {
    // 客户端里没有地址栏 —— 在里面开网页等于把人困在一个退不出去的页面上
    const out = html({ type: "link", id: "w", url: "https://neox.dev/docs", title: "文档", desc: "怎么用" });
    expect(out).toContain('href="https://neox.dev/docs"');
    expect(out).toContain('target="_blank"');
    expect(out).toContain("文档");
    expect(out).toContain("怎么用");
    // 站点名自己从 url 里取, 不用 bot 再填一遍
    expect(out).toContain("neox.dev");
  });

  it("只给一条 url 也画得出来 —— 不假装自己有摘要", () => {
    const out = html({ type: "link", id: "w", url: "https://neox.dev/a" });
    expect(out).toContain("https://neox.dev/a");
  });

  it("一组图铺开摆, 不是一张一张各占一张卡", () => {
    // 五张截图各占一张卡的话对话就被冲掉了, 而它们本来是一件事的几个面
    const out = html({ type: "gallery", id: "w", images: ["/a.png", "/b.png", { path: "/c.png", alt: "第三张" }] });
    expect(out.match(/gal__one/g) ?? []).toHaveLength(3);
    expect(out).toContain("第三张");
  });

  it("文件卡说清叫什么、在哪儿 —— **不假装能打开它**", () => {
    const out = html({ type: "file", id: "w", path: "/工作区/报表.xlsx", size: "24KB" });
    expect(out).toContain("报表.xlsx");   // 名字没给就从路径里取
    expect(out).toContain("/工作区/报表.xlsx");
    expect(out).toContain("24KB");
  });
});
