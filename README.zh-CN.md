<div align="center">

<!-- 单张彩色字标, 不走 <picture>: prefers-color-scheme 跟随操作系统, 而 GitHub 主题是
     账号设置, 两者可以相反 —— 一相反就会把近黑色字标贴到深色页面上, 字整个看不见。
     单色的 wordmark-dark / wordmark-white 留给应用内使用。 -->
<img src="packages/apps/console/src/assets/wordmark-color.png" alt="NeoX OS" height="52">

**个人 AI Agent 操作系统**

[官网](https://os.neox-dev.com) · [English](README.md)

一个人领着一群 bot 干活:每个 bot 是一个真正的进程,边界由内核强制,产出以 git 为准。

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go/)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](packages/apps/console/)
[![Electron](https://img.shields.io/badge/Electron-40-47848F?logo=electron&logoColor=white)](packages/apps/console/electron/)
[![Platform](https://img.shields.io/badge/platform-macOS%20%C2%B7%20Linux-555555)](#快速开始)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

</div>

---

不是聊天窗口里的循环,也不是 workflow 编排器。OS 拥有机器:分配工作区、翻译能力为内核规则、记录每一件事、在你不在的时候替你把关。

## 特性

- **进程,不是会话** — bot 是有生命周期的进程。进程会死,对话不死:事件账本(append-only)可完整重建任意时刻的状态,重启后接着干。
- **边界在内核** — Linux 上用 landlock / netns / cgroup2 把能力声明翻译成内核规则,子进程继承,`nohup` 绕不过;macOS 开发环境退化为 Seatbelt 沙箱(只拦写),自检如实报告哪一层在生效。
- **工件级团队协作** — 多个 bot 共改一个项目时,每人一个 git worktree 分支,同步、合入、交接、撤回都是 git 操作。交办只许一层:一句话,屋里每人最多动一次。
- **审批下沉到系统** — 越界不是崩溃而是问人;批准后 OS 带扩权把进程重新拉起,对话无缝接续。凭据(API key)只在宿主手里,不进任何被约束的进程。
- **成本工程** — 上下文页表 O(1) fork、内容寻址去重、前缀缓存命中实测 94–99%,并有独立于命中率的缓存健康判据。
- **场景测试台** — 十几个真实场景(多人抢文件、半路喊停、换人交接、烧到上限……)对活着的系统跑真模型,判据是不变量,不是 mock。

## 架构

```
┌─────────────────────────────────────────────┐
│  客户端                                       │
│  Electron 桌面壳 / 浏览器(OS 自带 Web UI)      │
└──────────────┬──────────────────────────────┘
               │ HTTP + SSE (Bearer token)
┌──────────────┴──────────────────────────────┐
│  neox-console (Go)                           │
│  ├─ osinit    进程表 · 事件账本 · 决策登记      │
│  ├─ agent     ReAct 循环 · 工具 · 团队协作     │
│  ├─ confine   能力 → 内核规则(landlock/netns) │
│  └─ engine    上下文页表 · 缓存 · 供应商调度    │
└─────────────────────────────────────────────┘
```

| 目录 | 内容 |
|---|---|
| `go/` | 系统主体(Go):`agent` `osinit` `confine` `engine` `abi` `cmd/` |
| `packages/apps/console/` | 控制台前端(React)与 Electron 壳 |
| `tools/scenario/` | 场景测试台 |
| `docs/` | 架构、审计、陷阱清单(TRAPS)、阶段报告 |

## 快速开始

三种形态,同一个二进制。

### Docker(最快)

```bash
docker pull lmk1010/neox-os:latest

docker run -d --name neox-os --restart unless-stopped \
  -p 7717:7717 \
  -v neox-os-data:/root/.neox-os \
  -e NEOX_OBSERVE_TOKEN=my-secret-key \
  lmk1010/neox-os:latest

# 打开 http://localhost:7717/?token=my-secret-key
```

token 自己定(`NEOX_OBSERVE_TOKEN`),地址就不用去日志里翻。不设的话首启随机生成,`docker logs neox-os` 里能看到。裸地址打开也有接入引导(贴 token、配推理服务、自动拉模型列表)。API key 也能一并注入:

```bash
  -e NEOX_API_KEY=sk-… -e NEOX_API_BASE=https://api.deepseek.com -e NEOX_MODEL_ID=deepseek-chat
```

嫌 707MB 大就用 `lmk1010/neox-os:slim`(176MB, 不带 node)。更多(数据卷、权限、远程访问、自建镜像)见 [`docs/DOCKER.md`](docs/DOCKER.md)。

### 自托管(浏览器)

```bash
cd packages/apps/console
npm install
npm run os:pack        # 前端构建 → 打进 Go 二进制
./electron/bin/neox-console
# 首次启动打印: 浏览器打开 http://127.0.0.1:7717/?token=…
```

token 持久化在 `~/.neox-os/token`,重启不变。要开远程访问,显式设置监听地址(并自行保证传输安全——内网、隧道或 TLS):

```bash
NEOX_OBSERVE_ADDR=0.0.0.0:7717 ./electron/bin/neox-console
```

### 桌面客户端(macOS)

[**下载 NeoX OS 1.0.1(Apple Silicon, 122MB)**](https://dl.neox-dev.com/os/NeoX-OS-1.0.1-arm64.dmg)

拖进"应用程序",双击。已签名并经 Apple 公证,**不会有"无法验证开发者"那道拦路**。

客户端**自带一台完整的 OS**——不需要装 Docker,不需要装 Go 或 node,双击就能用。首次打开会看一眼这台机器上有什么(git / python3 / node),缺的给一句能直接复制的安装命令;一件不装也能用,随时可以跳过。

想让活跑在别处(比如服务器上那台 Docker),在 设置 → 连接 里切成远程模式,填地址和 token——这时干活环境就是容器里那套,客户端只负责连接与渲染。

从源码跑客户端:

```bash
cd packages/apps/console
npm install && npm run os:pack   # 第一次, 或改了代码之后
npm start                        # 日常启动
npm run dmg                      # 打一个签名的 DMG(见 docs/RELEASE.md)
```

### 推理服务

界面 设置 → 推理服务 里填接口地址、模型与 key(OpenAI 兼容协议),支持一键拉取模型列表与真实连通性探测(含视觉能力实测)。

## 开发

```bash
cd go && go build ./... && go vet ./... && go test ./...   # 系统层
cd packages/apps/console && npm run typecheck && npx vitest run   # 前端
python3 tools/scenario/run.py list                          # 场景测试台
```

发布客户端(打包、签名、公证、传 R2)见 [`docs/RELEASE.md`](docs/RELEASE.md)。

内核强制路径只在 Linux 成立,验收在 Linux 真机/VM 上做;macOS 用于开发,自检会如实报告当前的强制级别。

## 状态

单用户单机形态,主动开发中。已知边界与欠账如实记录在 [`docs/STATUS.md`](docs/STATUS.md) 与 [`docs/TRAPS.md`](docs/TRAPS.md)——这两份文档的准则是:写下来的每一句都要能指到一条今天还能重跑的命令。
