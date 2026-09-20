import { describe, expect, it } from "vitest";

import { renderMarkdown } from "../markdown.js";

const json = (text: string, mentions: string[] = []) => JSON.stringify(renderMarkdown(text, mentions));

describe("行内: 加粗里面还要接着解析", () => {
  it("加粗里的 `代码` 不该变字面量", () => {
    // 真机截图: 同一条消息里别处的 `csv` 是代码块, 而
    // "**绝不拿真实的 `ledger.json` 做验证**" 里那一对反引号原样摆着.
    const out = json("**绝不拿真实的 `ledger.json` 做验证**");
    expect(out).toContain("md__inline");
    expect(out).toContain("ledger.json");
    expect(out).not.toContain("`");
  });

  it("加粗里的 @点名 也要点亮 —— 房间里点名常跟强调一起写", () => {
    const out = json("**@报表 这步归你**", ["报表", "阿导"]);
    expect(out).toContain("md__at");
  });

  it("代码里的 ** 保持原样 —— 那是内容, 不是格式", () => {
    const out = json("看 `a**b` 这个写法");
    expect(out).toContain("a**b");
    expect(out).not.toContain("strong");
  });

  it("普通的加粗和代码照旧", () => {
    const out = json("先 **看清楚** 再改 `main.go`");
    expect(out).toContain("strong");
    expect(out).toContain("md__inline");
  });
});

describe("告警卡里的话也要解析", () => {
  it("停滞检测那句里的强调不该是字面星号", () => {
    // OS 写的那段本来就是照着提示词的口气写的, 而星号恰好落在
    // **最需要看清楚**的那句上(该怎么办).
    const out = json("连着找不到 3 次。**用 find_files 搜一下**，或者停下来告诉用户。");
    expect(out).toContain("strong");
    expect(out).not.toContain("**");
  });
});

// 链接要能点 —— bot 说"地址 http://localhost:3001" 用户不该只能抄
import { describe as ld, expect as le, it as li } from "vitest";
ld("链接可点", () => {
  li("裸 URL 变链接, 句尾逗号留在话里", () => {
    const out = json("地址 http://localhost:3001，首页 200");
    le(out).toContain('"href":"http://localhost:3001"');
    le(out).not.toContain('3001，"'.replace('"', '\\"') + '···'); // 逗号不进 href
    le(out).toContain("，首页 200");
  });
  li("[文字](url) 按文字显示", () => {
    const out = json("看[这里](https://example.com/a)吧");
    le(out).toContain('"href":"https://example.com/a"');
    le(out).toContain("这里");
  });
  li("反引号里的 URL 保持代码样式", () => {
    const out = json("`http://localhost:3001` 是地址");
    le(out).not.toContain('"href"');
  });
});

// 回复里的 ![](…) 要就地出图 —— "默认能展示就展示"
import { describe as gd, expect as ge, it as gi } from "vitest";
import { renderMarkdown as rm } from "../markdown.js";
gd("行内图片", () => {
  const media = { src: (p: string) => `blob://x?path=${p}` };
  gi("本地绝对路径换成取图 URL", () => {
    const out = JSON.stringify(rm("截图在这 ![首页](/tmp/shot.png) 你看看", [], media));
    ge(out).toContain('"src":"blob://x?path=/tmp/shot.png"');
  });
  gi("http 图直接用", () => {
    const out = JSON.stringify(rm("![图](https://a.b/c.png)", [], media));
    ge(out).toContain('"src":"https://a.b/c.png"');
  });
  gi("换不出 URL 就原样当文字, 不摆裂图", () => {
    const out = JSON.stringify(rm("![图](相对路径.png)", [], media));
    ge(out).not.toContain('"src"');
    ge(out).toContain("相对路径.png");
  });
});
