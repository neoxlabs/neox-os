/**
 * i18n — 界面文案的两种语言.
 *
 *   **中文原文就是键.** 这个界面是用中文写出来的, 六百多处字面量散在三十个
 *   文件里; 给每一条发明一个 ID 再把中文挪进字典, 是两倍的改动、零收益 ——
 *   而且改完之后源码里再也看不见这句话在说什么. 所以 t("说点什么") 在中文下
 *   原样返回, 在英文下查 en.ts 里同一个键. 字典里查不到的键**原样返回中文**,
 *   永远不会因为漏译而显示成空的或 undefined.
 *
 *   **语言存在宿主, 不存在进程里.** 切换语言要整页重来: 六百处文案不是 React
 *   state, 它们在渲染时被 t() 读一次. 让每个组件订阅一个语言 store 的改动量
 *   等于把全部文案变成 hook —— 为了一个一年切不了两次的开关, 不值. 存好之后
 *   reload, 一秒钟的事.
 *
 *   桌面客户端里还要把语言带给 OS (NEOX_LANG): 开场那几个 bot 的名字和台词是
 *   Go 侧种的, 得跟界面一个语言 —— 见 electron/main.cjs 与 cmd/neox-console/main.go.
 */
import { EN } from "./en.js";

export type Lang = "zh" | "en";

const SAVED = "neoxos.lang";

/** 猜一个缺省: 浏览器/系统是中文就中文, 否则英文. 存过就用存的. */
export function detectLang(): Lang {
  try {
    const saved = localStorage.getItem(SAVED);
    if (saved === "zh" || saved === "en") return saved;
  } catch { /* 隐私模式读不了, 那就猜 */ }
  // 显式指定 (宿主注入或测试里钉死) 优先于猜
  const forced = (globalThis as { __NEOX_LANG__?: unknown }).__NEOX_LANG__;
  if (forced === "zh" || forced === "en") return forced;
  // **没有浏览器就是中文**: 这套界面是用中文写的, 测试里几百条断言比的都是中文原文,
  // node 里落到英文会让它们全红 —— 而那不是产品的问题。
  // 判 document 而不是 navigator: Node 21 起自带一个 navigator (language = "en-US"),
  // 拿它当"有浏览器"的证据会把测试全带偏; 有 document 才是真的在页面里。
  if (typeof document === "undefined") return "zh";
  const tag = (typeof navigator !== "undefined" ? navigator.language : "") || "";
  return tag.toLowerCase().startsWith("zh") ? "zh" : "en";
}

/** 当前语言 —— 模块级, 整页生命周期内不变 (见文件头: 切换 = reload) */
export const lang: Lang = detectLang();

/** 存下来并重载. 桌面壳会顺手把它带给 OS (NEOX_LANG). */
export function setLang(next: Lang): void {
  try { localStorage.setItem(SAVED, next); } catch { /* 存不了就只对这一次页面生效 */ }
  // 桌面壳有 lang 桥 (见 shell/endpoint.ts 的 Window 声明); 浏览器里没有, 那就只存本地
  const done: Promise<unknown> = window.neoxos?.lang !== undefined ? window.neoxos.lang.set(next) : Promise.resolve();
  void done.finally(() => location.reload());
}

/**
 * 查一句话. 中文下原样; 英文下查字典, 查不到原样 —— 漏译只会显示中文,
 * 不会显示空的.
 */
export function t(zh: string): string {
  if (lang === "zh") return zh;
  return EN[zh] ?? zh;
}

/**
 * 带插值的句子. 模板里用 {name} 占位, 键是**带占位符的中文原文**:
 *   tt("第 {n} 件事", { n: 3 })
 * 中英文的占位符名字必须一致 —— en.ts 里写错一个名字, 那个位置会原样显示 {n}.
 */
export function tt(zh: string, vars: Record<string, string | number | undefined | null>): string {
  const template = lang === "zh" ? zh : (EN[zh] ?? zh);
  // 没给的占位符原样留着 (一眼能看出漏了哪个); 给了但是 undefined/null 的当空串 ——
  // 源码里本来就有 `${x ?? ""}` 这种写法, 包成 tt 之后语义不该变
  return template.replace(/\{(\w+)\}/g, (_, k: string) => (k in vars ? String(vars[k] ?? "") : `{${k}}`));
}
