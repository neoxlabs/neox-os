import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Console } from "./shell/Console.js";
import { watchMishaps } from "./shell/mishaps.js";
import "./cloud/noop.js";
import "./shell/console.css";

// 出的错要留下痕迹 —— 越早开越好, 见 mishaps.ts
watchMishaps();

/**
 * 门在最外面 —— **Console 里一个 hook 都不该为它跑**.
 *
 *	放在 Console 内部的话, 一个还没登录的人也会把整套状态机启起来:
 *	连 SSE、拉进程表、跑自检. 那些请求本来就该在门后面.
 */
function App() {
  return <Console />;
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
