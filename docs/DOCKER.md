# Docker 里跑一台 NeoX OS

镜像: [`lmk1010/neox-os`](https://hub.docker.com/r/lmk1010/neox-os)(amd64 + arm64), 两个口味:

| 标签 | 解压大小 | 里面有什么 |
|---|---|---|
| `slim` | 176MB | Alpine 底座 + git + python3/pip/pytest + bash/curl/jq/ripgrep(GNU 工具). bot 要 node 时自己 `apk add nodejs npm` |
| `latest` | 707MB | Ubuntu 底座, 上面那些再加 node/npm 全套 |

一个容器就是一台完整的 OS: 服务端 + 内嵌 Web 界面, 浏览器打开就能用。
OS 本体只有 8MB(静态 Go 二进制, 界面打在里面), 其余全是给 bot 干活的工具链。

## 最快的一条路

```bash
docker pull lmk1010/neox-os:latest

docker run -d --name neox-os --restart unless-stopped -p 7717:7717 \
  -v neox-os-data:/root/.neox-os \
  -e NEOX_OBSERVE_TOKEN=my-secret-key \
  lmk1010/neox-os:latest

# 打开 http://localhost:7717/?token=my-secret-key
```

**token 建议自己定**(`NEOX_OBSERVE_TOKEN`): 地址是确定的, 不用去日志里翻。
不设的话首启随机生成并存进数据卷, `docker logs neox-os` 最后一行能看到。

带 token 的地址打开一次, 浏览器就记住了, 以后开裸地址也行。

**直接开裸地址(比如点 Docker Desktop 里的端口链接)也不再是死路**:
页面会展示接入引导 —— 把日志里那行地址(或只有 token)贴进去,
顺手把推理服务也配了, 然后进门。

## 一条命令配齐(推荐给不想点界面的人)

所有配置都能用环境变量在启动时注入:

```bash
docker run -d --name neox-os --restart unless-stopped -p 7717:7717 \
  -v neox-os-data:/root/.neox-os \
  -e NEOX_OBSERVE_TOKEN=my-secret-key \
  -e NEOX_API_KEY=sk-… \
  -e NEOX_API_BASE=https://api.deepseek.com \
  -e NEOX_MODEL_ID=deepseek-chat \
  lmk1010/neox-os:latest
# 直接打开 http://localhost:7717/?token=my-secret-key —— 开箱即聊
```

| 变量 | 作用 | 不设的话 |
|---|---|---|
| `NEOX_OBSERVE_TOKEN` | 访问口令(界面和 API 的钥匙) | 首启随机生成, 存进数据卷, 重启不变 |
| `NEOX_API_KEY` | 推理服务的 API key | 界面里配(引导页或 设置 → 推理服务) |
| `NEOX_API_BASE` | 推理接口地址(OpenAI 兼容) | `https://api.deepseek.com` |
| `NEOX_MODEL_ID` | 模型名 | `deepseek-v4-flash` |
| `NEOX_OBSERVE_ADDR` | 监听地址 | 镜像里已设 `0.0.0.0:7717`(容器必须) |
| `NEOX_ASK_MODE` | 审批口径: `never` 权限给足一次不问(含出网) / `bounds` 越界才问 / `always` 每步都问 | 镜像里已设 `never` —— 容器本身是隔离环境。设置页三档随时切, 界面里改过之后以界面为准 |

## 权限怎么管

三档, 在 设置 → 什么时候问你, 随时切、**立刻生效**(包括已经在干活的 bot):

- **都不问**(镜像缺省): 权限给足, 连出网都不批。bot 该干活干活。
- **要改东西时问**: 在自己工作区里动手不用问, 越界(出网、写别处)才来问你。
- **每步都问**: 每一个副作用都要你点头, 适合第一次跑不熟悉的活。

不管哪一档, bot 的写权限都锁在它自己的工作目录里 —— 那道墙不受这个开关影响。

注意: 环境变量注入的 API key **只活在进程里, 不落盘** —— 这是有意的。
要落盘就在界面里填。

## 镜像里带什么

bot 的工具链是现成的, 派活不用先等它装环境:
git · python3 / pip / pytest · curl / jq / ripgrep / unzip(`latest` 另带 node/npm)。
pip 可直接装包(容器即隔离环境, 不设 PEP 668 那道闸)。
`slim` 用 musl(Alpine): 个别带 C 扩展的 Python 包没有现成轮子时,
让 bot `apk add py3-包名` 装系统版即可。

## 打开之后干什么

1. **配推理服务**(没用环境变量注入的话): 接入引导会带你配;
   进门之后也可以随时在 设置 → 推理服务 改, 支持一键拉模型列表和真实连通性探测。
2. **加 bot**: 左上角 **+** → 加 bot(一个人)或加项目(一个目录 + 第一个人),
   起个名字就开工。
3. **交代事情**: 底部输入框直接说, `@` 可以点名。bot 是真进程,
   产出以 git 为准, 关掉浏览器它们照样干活。

## 数据在哪

全部在 `/root/.neox-os`(上面挂的 `neox-os-data` 卷):
事件账本、token、推理配置、bot 的 worktree。
删容器不丢数据; 删了卷才是真忘干净。

## 远程/团队访问

容器天然监听 `0.0.0.0`, 把端口映到内网或套一层 TLS/隧道即可 ——
传输安全自己负责, token 只是门锁不是加密。
桌面客户端(Electron)也能在 设置 → 连接 里切到远程模式连这台容器。

## 给 AI 的安装提示词

不想自己敲命令的话, 把下面整段发给任何能执行终端命令的 AI 助手
(Cursor / Claude / ChatGPT 等), 它会替你装好并把入口地址交给你:

```text
帮我在这台机器上启动 NeoX OS（一个自托管的 AI Agent 操作系统，跑在 Docker 里）。按下面步骤做，每步出错就先解决再继续：

1. 确认 Docker 可用：跑 `docker version`。没装就指导我装 Docker Desktop（macOS/Windows）或 docker-ce（Linux）；装了但连不上就帮我把它启动起来。

2. 拉镜像：`docker pull lmk1010/neox-os:latest`。如果在中国大陆拉不动，改用加速器拉：`docker pull docker.m.daocloud.io/lmk1010/neox-os:latest`，然后 `docker tag docker.m.daocloud.io/lmk1010/neox-os:latest lmk1010/neox-os:latest`。

3. 生成一个随机访问口令（比如 `openssl rand -hex 12` 的结果），记为 TOKEN。

4. 启动容器（7717 被占用就换成 7719 并在后面所有地方同步替换）：
   docker run -d --name neox-os --restart unless-stopped -p 7717:7717 \
     -v neox-os-data:/root/.neox-os \
     -e NEOX_OBSERVE_TOKEN=TOKEN \
     lmk1010/neox-os:latest

5. 验证：`curl -s -o /dev/null -w "%{http_code}" http://localhost:7717/` 应返回 200；`docker logs neox-os` 里应有"浏览器打开"字样。

6. 把这个地址完整地交给我，让我在浏览器打开：
   http://localhost:7717/?token=TOKEN

7. 最后告诉我：打开后如果提示"还没配推理服务"，在引导页或 设置 → 推理服务 里填一个 OpenAI 兼容接口的地址、模型名和 API key（比如 DeepSeek：https://api.deepseek.com + deepseek-chat），填完点"测一把"确认连通；然后左上角 + 号加 bot 或加项目，就可以直接给 bot 派活了。数据都在 neox-os-data 卷里，删容器不丢。

注意：不要把口令发到任何外部服务；所有命令都在本机执行。
```

## 自己构建镜像

```bash
docker build -f Dockerfile.console -t neox-os .        # 本机架构
docker buildx build --platform linux/amd64,linux/arm64 \
  -f Dockerfile.console -t you/neox-os --push .        # 双架构直推
```

`Dockerfile.console` 是完整 OS 镜像; 仓库根目录的 `Dockerfile` 是
`neox-init` 约束层(bot 的运行环境), 是另一回事。
