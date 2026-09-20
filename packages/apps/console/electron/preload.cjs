/**
 * preload — 唯一一条从宿主通到界面的路.
 *
 *   只暴露"OS 在哪", 不暴露任何能力.
 *   渲染层要做的一切(订阅/说话/拍板)都走 OS 的 HTTP 口, 有 token 有边界;
 *   把 fs / child_process 之类透进去, 这条边界就白设了.
 */
const { contextBridge, ipcRenderer } = require("electron");

const params = new URLSearchParams(location.search);
const url = params.get("os");
const token = params.get("token");

contextBridge.exposeInMainWorld("neoxos", Object.freeze({
  url: url || undefined,
  token: token || undefined,
  /**
   * 让人**点一个目录**, 而不是手敲一条绝对路径.
   *
   *   这不是"把 fs 透进去": 界面还是不能列目录、不能读文件 ——
   *   它只能请宿主开一个系统对话框, 而选哪个目录是人当场点的.
   */
  pickFolder: (current) => ipcRenderer.invoke("neox:pick-folder", current),
  /** 连接配置 —— 本机(spawn) / 远程(只连). 改完要重启客户端才生效 */
  connection: Object.freeze({
    get: () => ipcRenderer.invoke("neox:connection-get"),
    set: (config) => ipcRenderer.invoke("neox:connection-set", config),
    relaunch: () => ipcRenderer.invoke("neox:relaunch")
  }),
  /** 界面语言 —— 渲染层存好之后告诉宿主, 下次起 OS 用 NEOX_LANG 带过去 (见 main.cjs) */
  lang: Object.freeze({
    set: (lang) => ipcRenderer.invoke("neox:lang-set", lang)
  })
}));
