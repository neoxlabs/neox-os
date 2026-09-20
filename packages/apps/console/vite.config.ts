import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  // Electron 用 file:// 直接开 dist/index.html —— 绝对 base 会让
  // 打包出来的 /assets/… 解析成 file:///assets/…, 脚本一个都加载不到,
  // 界面全黑而**控制台一条报错都没有**(加载失败不抛异常).
  // 平时跑 dev server 时看不出来, 只有打完包才现形.
  base: "./",
  plugins: [react()],
  define: { __CLOUD__: false },
  server: { host: "127.0.0.1", port: 5310, strictPort: true }
});
