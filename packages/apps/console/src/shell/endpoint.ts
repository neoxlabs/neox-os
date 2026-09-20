/**
 * endpoint — OS 观察口在哪.
 *
 *   四个来源, 优先级从高到低:
 *     1. Electron 主进程注入 (window.neoxos) —— 本机模式它 spawn 了 OS,
 *        远程模式它从连接配置里读; 两种都走这一条
 *     2. URL 参数 (?os=…&token=… 或只有 ?token=) —— 自托管的入口:
 *        OS 首启打印"浏览器打开 http://…/?token=…", 只带 token 时
 *        地址就是当前页面的源(界面是 OS 自己 serve 的)
 *     3. 记住的连接 (localStorage) —— 2 成功过一次之后就不用再带参数
 *     4. 都没有 → null, 退回合成源
 *
 *   **没有硬编码的默认端点**. 猜一个地址连上去, 连的可能是别人的 OS.
 *
 *   **token 不留在地址栏**: 认出参数之后立刻记下并把它从 URL 上抹掉 ——
 *   历史记录、截图、分享链接都会带上地址栏里的每一个字.
 */

export interface Endpoint { readonly url: string; readonly token: string }

/** Electron 里存着的连接配置 —— 见 electron/main.cjs */
export interface ConnectionConfig {
  readonly mode: "local" | "remote";
  readonly url?: string;
  readonly token?: string;
}

declare global {
  interface Window {
    neoxos?: {
      url?: string;
      token?: string;
      /** 开一个系统目录选择器, 返回选中的路径. 取消 = null. 浏览器里没有这一项 */
      pickFolder?(current?: string): Promise<string | null>;
      /** 连接配置的读写 —— 只在 Electron 里有; 浏览器模式用 URL token 进来 */
      connection?: {
        get(): Promise<ConnectionConfig>;
        set(config: ConnectionConfig): Promise<boolean>;
        /** 保存之后要重启才生效 —— OS 是主进程起的, 换连接得从头来 */
        relaunch(): Promise<void>;
      };
      /** 界面语言 —— 告诉宿主, 下次起 OS 用 NEOX_LANG 带过去 (见 i18n/index.ts). 浏览器里没有这一项 */
      lang?: {
        set(lang: "zh" | "en"): Promise<boolean>;
      };
    };
  }
}

const SAVED = "neox.endpoint";

export function resolveEndpoint(): Endpoint | null {
  const injected = window.neoxos;
  if (injected?.url && injected.token) return { url: injected.url, token: injected.token };
  const params = new URLSearchParams(location.search);
  const token = params.get("token");
  // 只带 token = 界面就是这台 OS serve 的, 地址即当前源
  const url = params.get("os") ?? (token !== null && token !== "" ? location.origin : null);
  if (url !== null && url !== "" && token !== null && token !== "") {
    remember({ url, token });
    scrub();
    return { url, token };
  }
  return recall();
}

/** 记住的钥匙被 OS 拒了(容器重建 = 新 token)就得忘掉 —— 揣着旧钥匙反复撞门没有意义 */
export function forgetEndpoint(): void {
  try { localStorage.removeItem(SAVED); } catch { /* 存不了的地方也没什么可忘 */ }
}

function remember(endpoint: Endpoint): void {
  try { localStorage.setItem(SAVED, JSON.stringify(endpoint)); } catch { /* 隐私模式存不了 —— 这次能用就行 */ }
}

function recall(): Endpoint | null {
  try {
    const raw = localStorage.getItem(SAVED);
    if (raw === null) return null;
    const parsed = JSON.parse(raw) as Partial<Endpoint>;
    return typeof parsed.url === "string" && parsed.url !== "" && typeof parsed.token === "string" && parsed.token !== ""
      ? { url: parsed.url, token: parsed.token }
      : null;
  } catch {
    return null;
  }
}

/** 把凭据从地址栏上抹掉 —— 页面已经记住了, 地址栏上那份只剩泄露的用处 */
function scrub(): void {
  const clean = new URL(location.href);
  clean.searchParams.delete("token");
  clean.searchParams.delete("os");
  history.replaceState(null, "", `${clean.pathname}${clean.search}${clean.hash}`);
}
