# 出一个客户端并铺出去

macOS Apple Silicon 的 DMG，签名 + 公证，传到 R2，官网给直链。
**全程在本机做**，没有 CI —— 一条命令打包，一条命令公证，一条命令上传。

## 客户端是什么

一个 Electron 壳，里面装着完整的 OS：

| 部分 | 大小 |
|---|---|
| Electron（Chromium + Node） | 258MB |
| neox-console（OS 本体，界面打在里面） | 12MB |
| **DMG 成品** | **122MB** |

那 258MB 是 Chromium，带不带 OS 都得有它 —— 所以**没有"瘦客户端"这回事**，
只连远程的版本也是 120MB 左右。真要小得换壳（Tauri 用系统 WebKit，5–10MB）。

客户端**默认本机模式**：自己 spawn 一台 OS，双击就用，用户完全不必知道 Docker。
设置 → 连接 里可以切远程，连一台跑在服务器上的 Docker —— 那时干活环境
就是容器里那套，客户端只管界面。

## 一次完整的发布

```bash
cd packages/apps/console

# 1. 打包(前端 → Go 二进制 → 签名 → DMG). 约 4 分钟
npm run dmg

# 2. 公证. 约 5–15 分钟, 看 Apple 排队
cd release
xcrun notarytool submit NeoX-OS-1.0.0-arm64.dmg --keychain-profile neox --wait

# 3. 把公证结果钉进 DMG —— **别漏这步**
xcrun stapler staple NeoX-OS-1.0.0-arm64.dmg
xcrun stapler validate NeoX-OS-1.0.0-arm64.dmg

# 4. 传 R2
../../../../tools/publish-dmg.sh NeoX-OS-1.0.0-arm64.dmg
```

改版本号改 `packages/apps/console/package.json` 的 `version`，DMG 文件名跟着走。

### 钉章那一步为什么不能省

公证结果默认存在 Apple 服务器上，Gatekeeper 每次打开都要联网去问。
`stapler staple` 把那张票据钉进 DMG 本身 —— **用户断网也能正常打开**。
不钉的话，第一次打开时网络不好就会卡在"正在验证"，甚至直接说打不开。

### 签名和公证是怎么配好的

- 证书：`Developer ID Application: mingkang liu (24D88Q3K3S)`，在钥匙串里，
  electron-builder 自动认（`CSC_IDENTITY_AUTO_DISCOVERY`，缺省就是开的）。
- 公证：钥匙串里存了一份叫 `neox` 的 notarytool profile。
  重建它：`xcrun notarytool store-credentials neox --apple-id … --team-id 24D88Q3K3S --password <app 专用密码>`。

打包时**故意不让 electron-builder 自动公证**（`build.mac.notarize: false`）：
122MB 上传一次要几分钟，混在打包里失败了得从头再来一遍。分成两步，
打包永远是几分钟的事，公证失败只重公证。

### asar 那个坑

OS 二进制走 `extraResources` 放在 `app.asar` **外面**（`Contents/Resources/os/`）。
asar 是个归档，里面的文件 `spawn` 不了。`electron/main.cjs` 里按 `app.isPackaged`
分两条路取这个二进制 —— 开发时在 `electron/bin/`，打包后在 `process.resourcesPath`。

## R2 和域名

> 下面这一节是**通用结论**，留着备查。当前发布走的是 Neox 那个桶，
> 域名和 bucket 本来就同账号，不受影响。

### 域名必须和 bucket 在同一个 Cloudflare 账号

这是 Cloudflare 的硬规矩：R2 自定义域要求"该域名作为 zone 添加在与 R2 bucket
**同一个账号**下"。所以 A 账号的 bucket **接不上** B 账号的 `neox.dev`。

三条路，推荐第一条：

1. **把 bucket 建在持有 neox.dev 的那个账号里**。R2 开通是免费的，
   建个新 bucket 比搬域名简单得多。**建议就这么办。**
2. 把 neox.dev 这个 zone 迁到有 R2 的账号（Cloudflare 支持，但要改 NS 或走 zone 转移，
   期间域名解析要小心）。
3. 在 neox.dev 那个账号里放一个 Worker 反代 —— **不推荐**：Worker 绑不了
   别的账号的 R2，只能 HTTP 去拉 `r2.dev`，而那个地址是限速的、
   官方明说只给开发用，还专门说了别 CNAME 过去。

### 别拿 r2.dev 当下载地址

`*.r2.dev` 是限速的开发地址，没有缓存、没有防护。挂上去哪天上了一次量就废。
一定要配自定义域（比如 `dl.neox.dev`），走 Cloudflare 的 CDN 和缓存。

### 配好之后

```
R2 → 建 bucket(比如 neox-downloads)
   → Settings → Custom Domains → 加 dl.neox.dev
   → R2 → API 令牌 → 建一个"对象读写"的 token, 记下三样:
        账号 ID / Access Key ID / Secret Access Key
```

### 现在用的就是 Neox 那个桶

凭据已经在 `~/.neox-secrets/r2.env`（Neox 桌面版发版用的同一份），直接 source：

```bash
set -a && . ~/.neox-secrets/r2.env && set +a
tools/publish-dmg.sh packages/apps/console/release/NeoX-OS-1.0.0-arm64.dmg
```

桶是 `neox-downloads`，域名 `dl.neox-dev.com`（已经在同一个账号下，
所以上面那个跨账号的问题**这条路上不存在**）。

**桶是共用的**：Neox 桌面版发在 `desktop/` 下，NeoX OS 发在 `os/` 下。
脚本默认加 `os/` 前缀，改前缀设 `R2_PREFIX`（设成空字符串就是桶根目录）。

## 用户拿到 DMG 之后

双击 → 拖进 Applications → 打开。**不会有任何 Gatekeeper 警告**（签名 + 公证 + 钉章）。

第一次打开会走一遍环境自检（见 `Checkup.tsx`）：查这台 Mac 上有没有
git / python3 / node / rg / curl，缺的给一句能直接复制的安装命令。
**一件不缺就不弹**，也**随时可以跳过** —— 只让 bot 写文档聊天的人一件都不需要。
之后想再看：设置 → 连接 → 干活环境 → 看清单。
