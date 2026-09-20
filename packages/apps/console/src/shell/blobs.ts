/**
 * blobs —— 图片从哪儿取.
 *
 *   图片不进事件流(见 go/osinit/blobs.go): 事件里只有一个 id 或者一条
 *   路径, 真正的字节在 OS 那一侧, 界面按 URL 取, 浏览器自己会缓存.
 *
 *   **token 走 query**: <img src> 跟 EventSource 一样发不了自定义头.
 *   这个让步的边界跟 /stream 那条一样 —— 口子只绑回环, token 一次性生成.
 */

import type { Endpoint } from "./endpoint.js";

export interface Shots {
  /** 用户发进来的那些: 内容寻址, 取到的永远是同一张 */
  byId(id: string): string;
  /** bot 在自己工作区里看的那张: 按路径取 */
  byPath(path: string): string;
}

export function shotsFrom(endpoint: Endpoint | null): Shots | null {
  if (endpoint === null) return null;
  const base = endpoint.url.replace(/\/$/, "");
  const token = encodeURIComponent(endpoint.token);
  return {
    byId: (id) => `${base}/blob?id=${encodeURIComponent(id)}&token=${token}`,
    byPath: (path) => `${base}/blob?path=${encodeURIComponent(path)}&token=${token}`
  };
}
