# 启动与内核 —— Neox OS 跟 Linux 是什么关系

> 回答两个问题：AgentOS 怎么"启动"？要不要自己写内核？

---

## 0. 短答

**不写内核。写 personality 层。**

先例是 Android：Android 是 OS，它用 Linux 内核。它的贡献是 init / zygote / binder /
SystemServer / PackageManager —— 也就是 personality。
没人因为 Android 没写内核就说它不是 OS。

Linux 提供**机制**（namespace / cgroup v2 / landlock / seccomp），
Neox OS 提供**策略**（谁能拿到什么、什么时候换出、事件怎么流、人怎么被找到）。

---

## 1. 分层

```
┌──────────────────────────────────────────────┐
│ 应用      Chat / Life / Target / 第三方        │
├──────────────────────────────────────────────┤
│ NeoxABI   系统调用契约（有版本、有承诺）         │
├──────────────────────────────────────────────┤
│ neox-init 进程表 · 事件日志 · 决策 · 预算       │  ← personality
│ neox-boot PID 1：自检 / 信号 / 有序关机         │
│ confine   能力 → 内核规则的翻译 + fail-closed   │
├──────────────────────────────────────────────┤
│ Linux 内核 namespace · cgroup2 · landlock ·   │  ← 复用，不重写
│           seccomp · netns                     │
├──────────────────────────────────────────────┤
│ Ubuntu base image + 工具链 + 浏览器            │
└──────────────────────────────────────────────┘
```

---

## 2. "启动"具体是什么

镜像的 `ENTRYPOINT` 是 `neox-init`，它在容器里是 **PID 1**。

PID 1 有三件普通进程不用管、不管就出事故的事：

| 职责 | 不做的后果 | 实现 |
|---|---|---|
| 收僵尸 | PID 1 是孤儿进程的父亲，不 reap 会攒僵尸直到 pid 耗尽 | `init.ts::reapZombies` —— **目前只留了接缝并大声记录**，在补上 native waitpid 之前镜像必须用 tini 包一层 |
| 转发信号 | `docker stop` 发 SIGTERM，PID 1 默认行为是**直接死**，进程来不及落盘 | 捕获 SIGTERM/SIGINT → 有序关机（幂等，连按两次 Ctrl-C 只走一遍） |
| 启动自检 | 内核不支持强制约束却照样启动 = 一个自称能隔离而实际没有的 OS | confined 模式自检不过 → **不启动**，抛错退出 |

自检顺序是刻意的：**自检 → 建 OS → 装信号 → 收僵尸**。自检不过在建 OS 之前就抛，
所以"半启动状态"不存在。

---

## 3. 能力怎么变成强制

这是全工程最关键的一处，也是"OS"和"一个说话像 OS 的库"的分界线。

```
Capability { axis: 'read', scope: '/in' }        ← 声明
        ↓ planFor()  （纯函数，任何平台可测）
ConfinementPlan { fs: [{path:'/work/in', access:['read']}], net: [], ... }
        ↓ probeEnforcement()  ← 内核真的能执行吗？不能就 fail-closed
        ↓ buildLaunch()  （纯函数）
unshare --mount --pid --fork --net
  └─ cgexec -g memory,cpu,pids:neox-os/p1
       └─ landlock-run --ro /work/in --rw /run/neox-os/p1.sock --
            └─ 用户程序
```

landlock ruleset **跨 `execve` 继承**，所以用户程序再 fork 出来的任何子进程也逃不掉。
这就是"约束"跟"配置"的区别。

### 三条不显然但要命的设计

1. **没有出网授权时更需要 netns。** 断网要靠隔离（namespace 里一张网卡都没有），
   不能靠"白名单里没有"。把限制当配置是最常见的假隔离。
2. **可写必然可读。** landlock 里 write 不隐含 read，分开授会踩坑。
3. **宿主环境一律不继承。** 继承 env 等于把凭据漏进沙盒 —— 这是 Neox 踩过的账
   （已知陷阱 E3：长驻进程继承了 env 就摘不掉）。

---

## 4. 四种模式组合，只有两种合法

| 模式 | 进程体 | 结果 |
|---|---|---|
| confined | exec | ✅ 唯一的生产路径。内核必须真能强制，否则拒绝启动 |
| dev | inproc | ✅ 开发路径。约束退化为记账，只能跑同堆闭包 |
| confined | inproc | ❌ 拒。闭包在 OS 自己的堆里，内核管不着它 |
| dev | exec | ❌ 拒。那会是一个**完全不受约束的真进程**，比什么都不做更危险 |

**缺省是 `confined`** —— 缺省必须是安全的那个。dev 模式要显式声明，
而且启动时会大声打印"不受内核约束，不要用于生产"。

---

## 5. 现在的诚实状态

| 项 | 状态 |
|---|---|
| 能力 → 内核规则翻译 | ✅ 纯函数 + 17 项测试 |
| 内核探测 + fail-closed | ✅ 有测试。**在 macOS 上跑 confined 模式会拒绝启动，这是正确行为** |
| 启动命令行分层构造 | ✅ 有测试锁住形状 |
| PID 1 自检 / 信号 / 幂等关机 | ✅ 有测试 |
| **真的在 Linux 上跑一次** | ⬜ **没有**。本机没有 Docker，全部 Linux 路径尚未真机验证 |
| 收孤儿僵尸 | ⬜ 需要 native waitpid；在此之前镜像用 tini 兜 |
| ABI 跨进程 IPC | ⬜ 还是同进程函数调用。真隔离要求它走 unix socket |

最后三项是下一步。**第 5 项尤其要认**：所有 Linux 强制路径目前只有纯函数测试，
没有一次真机验证 —— 按"真机验证要真点界面"那条铁律，它现在不算数。
