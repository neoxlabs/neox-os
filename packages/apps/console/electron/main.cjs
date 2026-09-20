/**
 * Electron 主进程 —— 这个客户端的宿主.
 *
 *   它做三件事, 一件不多:
 *     1. spawn 一台 OS (neox-console), 从 stdout 认观察口地址和 token
 *     2. 把地址注入给渲染层 (通过 preload, 不是 nodeIntegration)
 *     3. 退出时把 OS 一起收掉
 *
 *   **凭据不进渲染层的全局环境**: token 只通过 contextBridge 暴露一个只读值,
 *   渲染层拿不到 process / require / fs.
 *
 *   OS 是子进程不是内嵌: 将来要接的是**远端**那台 OS, 现在这条本地路径
 *   只是同一个接口的一个实现. 所以地址是发现出来的, 不是写死的.
 */
const { app, BrowserWindow, dialog, ipcMain, shell } = require("electron");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const DEV_URL = process.env.NEOX_CONSOLE_DEV_URL || "";
const isDev = DEV_URL.length > 0;

let osProcess = null;
let window_ = null;
let endpoint = null;
let shuttingDown = false;
let restartTimer = null;
let restartAttempt = 0;

/**
 * 连接配置 —— 客户端的两种形态就差在这一个文件上.
 *
 *   local  (缺省): 客户端自己 spawn 一台 OS, 退出时一起收掉 —— 今天的样子
 *   remote        : OS 跑在别处(Docker/云主机/另一台 Mac), 客户端只连接渲染
 *
 *   存在 userData 下, 不进仓库也不进渲染层的 localStorage ——
 *   它带着 token, 归主进程管.
 */
function connectionPath() { return path.join(app.getPath("userData"), "connection.json"); }

function readConnection() {
  try {
    const parsed = JSON.parse(fs.readFileSync(connectionPath(), "utf8"));
    if (parsed !== null && parsed.mode === "remote"
      && typeof parsed.url === "string" && parsed.url !== ""
      && typeof parsed.token === "string" && parsed.token !== "") {
      return { mode: "remote", url: parsed.url.replace(/\/+$/, ""), token: parsed.token };
    }
  } catch { /* 没配过 = 本机模式 */ }
  return { mode: "local" };
}

function writeConnection(config) {
  const clean = config !== null && config.mode === "remote"
    && typeof config.url === "string" && config.url !== ""
    && typeof config.token === "string" && config.token !== ""
    ? { mode: "remote", url: config.url.replace(/\/+$/, ""), token: config.token }
    : { mode: "local" };
  fs.mkdirSync(path.dirname(connectionPath()), { recursive: true });
  fs.writeFileSync(connectionPath(), JSON.stringify(clean, null, 2), { mode: 0o600 });
  return clean;
}

/**
 * 界面语言 —— 存在 userData 下的 lang.json, 跟 connection.json 一个待遇.
 *
 *   为什么主进程要知道语言: OS 是这儿 spawn 的, 而开场那几个 bot 的名字和
 *   台词是 Go 侧种的 (cmd/neox-console/main.go 的 personas). 界面是英文、
 *   开场白是中文, 那就是假英文版. 所以起 OS 时把 NEOX_LANG 带过去.
 *   渲染层切换语言时通过 neox:lang-set 写到这儿, 下次起 OS 就生效.
 */
function langPath() { return path.join(app.getPath("userData"), "lang.json"); }
function readLang() {
  try {
    const parsed = JSON.parse(fs.readFileSync(langPath(), "utf8"));
    if (parsed && (parsed.lang === "zh" || parsed.lang === "en")) return parsed.lang;
  } catch { /* 没存过 = 跟系统 */ }
  return null;
}
function writeLang(lang) {
  if (lang !== "zh" && lang !== "en") return;
  fs.mkdirSync(path.dirname(langPath()), { recursive: true });
  fs.writeFileSync(langPath(), JSON.stringify({ lang }), { mode: 0o600 });
}

function startOs() {
  return new Promise((resolve, reject) => {
    // 打包之后代码住在 app.asar 里, **asar 里的东西是 spawn 不了的** ——
    // 所以 OS 二进制走 extraResources 放在 asar 外面(见 package.json 的
    // build.extraResources). 开发时它还在 electron/bin/ 下.
    const name = process.platform === "win32" ? "neox-console.exe" : "neox-console";
    const binary = process.env.NEOX_CONSOLE_OS
      || (app.isPackaged ? path.join(process.resourcesPath, "os", name) : path.join(__dirname, "bin", name));
    // 语言跟着界面走: 存过就用存的, 没存过跟系统 —— 与渲染层 detectLang 同一口径
    const lang = readLang() ?? (String(app.getLocale() || "").toLowerCase().startsWith("zh") ? "zh" : "en");
    const child = spawn(binary, [], { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, NEOX_LANG: lang } });
    osProcess = child;

    let settled = false;
    const found = {};
    // 起不来要**说清楚是哪一步**: 二进制没有 / 端口占了 / 没打出地址,
    // 三种都会表现成"窗口空着", 不区分的话每次都要重查一遍
    const timer = setTimeout(() => {
      if (!settled) { settled = true; reject(new Error("OS 起来了但没在 8 秒内打出观察口地址")); }
    }, 8000);

    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      for (const line of chunk.split("\n")) {
        const [key, value] = line.trim().split(/\s+/);
        if (key === "NEOX_OBSERVE_URL") found.url = value;
        if (key === "NEOX_OBSERVE_TOKEN") found.token = value;
      }
      if (!settled && found.url && found.token) {
        settled = true;
        clearTimeout(timer);
        resolve(found);
      }
    });
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk) => process.stderr.write(`[os] ${chunk}`));
    child.on("error", (error) => {
      if (!settled) { settled = true; clearTimeout(timer); reject(new Error(`起不了 OS (${binary}): ${error.message}`)); }
    });
    child.on("exit", (code) => {
      osProcess = null;
      if (!settled) { settled = true; clearTimeout(timer); reject(new Error(`OS 退出了, 退出码 ${code}`)); }
      else if (!shuttingDown) scheduleOsRestart();
    });
  });
}

function createWindow() {
  window_ = new BrowserWindow({
    width: 1120,
    height: 780,
    minWidth: 720,
    minHeight: 480,
    // 无边框但保留红绿灯 —— 顶栏是内容的一部分, 不额外占一条系统标题栏
    titleBarStyle: "hiddenInset",
    trafficLightPosition: { x: 18, y: 18 },
    backgroundColor: "#0c0c0c",
    show: false,
    webPreferences: {
      preload: path.join(__dirname, "preload.cjs"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      // 窗口被盖住时别把它冻起来: 默认 Electron 会节流后台窗口的
      // 定时器和 requestAnimationFrame, 而这个界面是**一直在动**的 ——
      // bot 在后台干着活, 事件照收, 界面却停在十几秒前. 切回来才
      // "哗"地补上一大段, 那条"正在想…"于是说的是老早以前的事.
      backgroundThrottling: false
    }
  });
  window_.once("ready-to-show", () => window_.show());
  // 外链走系统浏览器, 不在客户端里开
  window_.webContents.setWindowOpenHandler(({ url }) => { void shell.openExternal(url); return { action: "deny" }; });
  return window_;
}

/**
 * 选一个工作区 —— **系统的目录选择器, 不是让人手敲路径**.
 *
 *   手敲一条绝对路径这件事本身就是个陷阱: 敲错一个字母不会报错,
 *   它会真的去那个不存在的目录里开工, 而人要过很久才发现活干在了别处.
 *
 *   这是这条边界上唯一一个"能力"型接口, 所以它必须是**用户亲手点的**:
 *   主进程只开一个系统对话框, 选哪个目录由人决定, 渲染层拿到的只是
 *   一个字符串 —— 它没有列目录、没有读文件, 那条边界还在.
 */
ipcMain.handle("neox:pick-folder", async (_event, current) => {
  if (window_ === null) return null;
  const result = await dialog.showOpenDialog(window_, {
    title: "选一个工作区",
    message: "他只能在这个目录里动手",
    buttonLabel: "就这儿",
    properties: ["openDirectory", "createDirectory"],
    ...(typeof current === "string" && current !== "" ? { defaultPath: current } : {})
  });
  return result.canceled || result.filePaths.length === 0 ? null : result.filePaths[0];
});

/** 连接配置的读写 —— 渲染层的设置页用. 改完要重启才生效(OS 是这儿起的) */
ipcMain.handle("neox:connection-get", () => readConnection());
ipcMain.handle("neox:connection-set", (_event, config) => { writeConnection(config); return true; });
ipcMain.handle("neox:relaunch", () => { app.relaunch(); app.exit(0); });
/** 界面语言 —— 渲染层切换时写到这儿, 下次起 OS 用 NEOX_LANG 带过去 (见 readLang) */
ipcMain.handle("neox:lang-set", (_event, lang) => { writeLang(lang); return true; });

app.whenReady().then(async () => {
  // Dock 图标: 开发时 Electron 会用它自己那个原子图标, 必须显式换掉 ——
  // 打包版走 electron-builder 的 icon 配置, 这里只管 dev
  if (process.platform === "darwin" && app.dock !== undefined) {
    try { app.dock.setIcon(path.join(__dirname, "icon.png")); } catch { /* 图标缺了不该拦住启动 */ }
  }
  const connection = readConnection();
  if (connection.mode === "remote") {
    // 远程模式: 不 spawn, 只连 —— OS 的生死归它自己的宿主管
    endpoint = { url: connection.url, token: connection.token };
  } else {
    try {
      endpoint = await startOs();
    } catch (error) {
      console.error("[console]", error.message);
      endpoint = null; // 渲染层会退回合成源, 而不是白屏
    }
  }
  const win = createWindow();
  const query = endpoint === null ? "" : `?os=${encodeURIComponent(endpoint.url)}&token=${encodeURIComponent(endpoint.token)}`;
  if (isDev) await win.loadURL(DEV_URL + query);
  else await win.loadFile(path.join(__dirname, "..", "dist", "index.html"), endpoint === null ? {} : { search: query });
});

app.on("window-all-closed", () => { if (process.platform !== "darwin") app.quit(); });
app.on("activate", () => { if (BrowserWindow.getAllWindows().length === 0) createWindow(); });
// OS 是我们起的, 就该我们收 —— 留个孤儿进程占着端口, 下次就起不来了
app.on("before-quit", () => {
  shuttingDown = true;
  if (restartTimer !== null) { clearTimeout(restartTimer); restartTimer = null; }
  osProcess?.kill("SIGTERM");
});
process.on("exit", () => { osProcess?.kill("SIGKILL"); });

/**
 * OS 自己退了要把他拉起来.
 *
 *	Electron 只在启动时 spawn 一次. 之后 neox-console 崩了 / 被系统收掉,
 *	窗口还开着, 渲染层拿着旧地址发 /say, 得到的就是"进程离线了".
 *	用户只能重启整个客户端 —— 重启之所以管用, 只是又走了一遍 startOs.
 */
function scheduleOsRestart() {
  if (shuttingDown || restartTimer !== null) return;
  restartAttempt = Math.min(restartAttempt + 1, 6);
  const delay = Math.min(400 * (2 ** (restartAttempt - 1)), 12000);
  process.stderr.write(`[os] 退了, ${delay}ms 后拉起来 (第 ${restartAttempt} 次)\n`);
  restartTimer = setTimeout(() => {
    restartTimer = null;
    startOs().then((found) => {
      endpoint = found;
      restartAttempt = 0;
      reloadWindow();
    }).catch((error) => {
      process.stderr.write(`[os] 拉不起来: ${error.message}\n`);
      scheduleOsRestart();
    });
  }, delay);
}

function reloadWindow() {
  if (window_ === null || window_.isDestroyed() || endpoint === null) return;
  const query = `?os=${encodeURIComponent(endpoint.url)}&token=${encodeURIComponent(endpoint.token)}`;
  if (isDev) void window_.loadURL(DEV_URL + query);
  else void window_.loadFile(path.join(__dirname, "..", "dist", "index.html"), { search: query });
}
