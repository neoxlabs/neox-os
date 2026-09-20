package confine

import (
	"errors"
	"fmt"
	"strings"
)

// 启动命令行 · 由外到内, 每层收紧一点:
//
//	sh -c 'echo $$ > cgroup.procs; exec ..'      ← 限额: 自己加入 cgroup 再 exec
//	  └─ [ip netns exec NS]                      ← 出网: 只在有授权时才有这一层
//	       └─ unshare --mount --pid --fork [--net] ← 隔离: 独立 pid/挂载 (+断网)
//	            └─ landlock-run --ro X --rw Y --  ← 文件系统白名单, 自缚后 exec
//	                 └─ 用户程序                  ← 继承以上全部约束
//
// 为什么 cgroup 在最外层:
//
//	它原来在 unshare 里面, 靠的是"unshare --mount 只复制挂载树,
//	/sys/fs/cgroup 照样看得见" —— 一个**没写出来的依赖**.
//	`ip netns exec` 正好破坏它: 为了让 /sys/class/net 反映新的网络
//	命名空间, 它会重新挂一个干净的 sysfs, 于是 /sys/fs/cgroup 是空的.
//	真机报 `cannot create .../cgroup.procs: Directory nonexistent`.
//
//	提到最外层就没有这个依赖了: cgroup 归属是进程属性, 跨 fork/exec
//	和所有 namespace 继承, 后面怎么隔离都带得走.
//
// 为什么不用 cgexec:
//
//	cgexec 是 cgroup **v1** 时代的工具, 在 v2 统一层级下直接报
//	"cgroup change of group failed" —— 真机第一次跑就撞上.
//	正确做法是 OS 自己管 cgroup: 建好目录写好限额, 进程用
//	`echo $$ > cgroup.procs` 把自己放进去再 exec. cgroup 归属跨 execve
//	继承, 且不依赖任何外部二进制.
//
// 为什么 cgroup 必须在 landlock 之前:
//
//	加入 cgroup 要写 /sys/fs/cgroup/..., landlock 一旦生效就写不了了.
//
// 关键性质: landlock ruleset 跨 execve 继承, 所以用户程序再 fork 出来的
// 任何子进程也逃不掉. 这是"约束"跟"配置"的区别.

type LauncherPaths struct {
	Unshare string
	// Sh POSIX shell —— 只用来把自己放进 cgroup 然后 exec, 不跑任何用户输入
	Sh          string
	LandlockRun string
	CgroupRoot  string
	// IP iproute2. 只在有出网授权时才用到 —— 没授权就是 unshare --net,
	// 一张网卡都不需要建
	IP string
}

var DefaultPaths = LauncherPaths{
	Unshare:     "/usr/bin/unshare",
	Sh:          "/bin/sh",
	LandlockRun: "/usr/local/bin/landlock-run",
	CgroupRoot:  "/sys/fs/cgroup",
	IP:          "/sbin/ip",
}

type Body struct {
	Argv []string
	Cwd  string
	Env  map[string]string
}

type LaunchContext struct {
	PID         string
	CgroupSlice string
	ABISocket   string
	// ABIToken 一次性凭据.
	// **走 env 不走命令行** —— 命令行在 /proc/<pid>/cmdline 里对同机进程可见.
	ABIToken string
	// NetNS 出网用的具名网络命名空间. 空 = 完全断网 (unshare --net).
	//
	// 有出网授权时**必须**给, 给不出来就拒绝启动 —— 见 ErrNetUnwired.
	NetNS string
	// ProxyAddr OS 侧代理地址. 进程的 http_proxy 指向它.
	//
	// 注意它**不是强制手段**: 强制来自拓扑 (netns 里除了它没有别的路可走).
	// 这个环境变量只是告诉程序"往哪走"能省一次失败.
	ProxyAddr string
}

type Launch struct {
	Argv []string
	// Env 剥干净的环境变量. 宿主环境不继承 —— 继承等于把凭据漏进沙盒
	Env map[string]string
}

var ErrEmptyArgv = errors.New("exec 进程必须给出 argv")

// ErrNetUnwired 授了出网能力, 但没人把网配出来.
//
// **这是必须拒绝启动的情况, 不是警告.**
//
// 这个洞真实存在过: 能力模型里有 net 轴、PlanFor 老老实实把它翻译成
// NetRule、plan JSON 里也躺着 —— 而 launcher 从头到尾没读过它一眼.
// 于是授权 `{net: registry.npmjs.org}` 什么都不会发生, 而且一声不吭:
// 用户以为授权了, agent 以为自己该能连, 实际连不出去.
//
// 这跟"只支持不接入不算数"是同一条规矩的镜像:
// **授权了却不生效, 也必须说出来.**
var ErrNetUnwired = errors.New(
	"能力集里有出网授权, 但没有给出网络命名空间 —— 拒绝启动。" +
		"要么把出网真的配出来, 要么别授这条能力: 静默失效比拒绝启动糟得多")

func BuildLaunch(plan Plan, body Body, ctx LaunchContext, paths LauncherPaths) (Launch, error) {
	if len(body.Argv) == 0 {
		return Launch{}, ErrEmptyArgv
	}
	if len(plan.Net) > 0 && ctx.NetNS == "" {
		return Launch{}, ErrNetUnwired
	}

	var argv []string

	// ── 层 1: 自己加入 cgroup 再 exec ──
	//
	// **必须在最外层**, 也就是在任何 namespace 之前.
	//
	// 它原来在 unshare 里面, 靠的是"unshare --mount 只是复制挂载树,
	// /sys/fs/cgroup 照样看得见". 那是一个**没写出来的依赖**,
	// 而 `ip netns exec` 正好破坏它: 它会重新挂一个干净的 sysfs
	// (为了让 /sys/class/net 反映新的网络命名空间), 于是
	// /sys/fs/cgroup 在里面是空的 —— 进程报的是
	// `cannot create .../cgroup.procs: Directory nonexistent`.
	//
	// 提到最外层之后这个依赖就不存在了: cgroup 归属是进程属性,
	// 跨 fork/exec 和所有 namespace 继承, 后面怎么隔离都带得走.
	//
	// 只有确实设了限额时才套这一层; 没限额就不必多一次 exec.
	hasLimit := plan.Cgroup.MemoryMaxBytes != nil ||
		plan.Cgroup.CPUWeight != nil || plan.Cgroup.PidsMax != nil
	if hasLimit {
		procs := fmt.Sprintf("%s/%s/cgroup.procs", paths.CgroupRoot, ctx.CgroupSlice)
		argv = append(argv, paths.Sh, "-c",
			fmt.Sprintf(`echo $$ > %s && exec "$@"`, shQuote(procs)), "neox-os")
	}

	// ── 层 2: namespace 隔离 ──
	//
	// 没有出网授权: --net 建一个匿名网络命名空间, 里面**一张网卡都没有** ——
	// 这是"真的连不上", 不是"白名单里没有".
	//
	// 有出网授权: 进程进入 OS 预先配好的具名 ns. 那里面只有一根 veth,
	// 对端是 OS, **没有默认路由也没有 DNS** —— 唯一能到的地方就是 OS
	// 的代理. 强制来自拓扑: 不走代理的程序不是被规则拒绝, 是没有路可走.
	//
	// 这时不能再 --net: 那会把刚进来的 ns 换成一个新的空 ns, 网又没了.
	if ctx.NetNS != "" {
		argv = append(argv, paths.IP, "netns", "exec", ctx.NetNS)
	}
	// ── --mount-proc: 新的 pid 命名空间必须配一个新的 /proc ──
	//
	// `--pid` 建了独立的 pid 命名空间, 但 /proc 还是宿主那个 —— 于是
	// **看到的 pid 和能发信号的 pid 不是一套**:
	//
	//	ps 报的     宿主视角的 pid (procfs 来自宿主)
	//	kill 发的   命名空间视角的 pid
	//
	// 症状是"看得见、杀不掉, 而且报的错还是误导性的 No such process".
	// 真机 A/B (同一段脚本, 只差这一个开关):
	//
	//	不挂 proc   真实子进程 $!=2, ps 报 29739 → kill 失败 → 进程还活着
	//	--mount-proc 真实子进程 $!=2, ps 报 2    → kill 成功 → 进程被杀掉
	//
	// agent 撞上的样子: 第一轮起了个 HTTP 服务, 第二轮要改它的行为,
	// 重启时端口被旧进程占着 —— 它 kill/pkill 全试了都杀不掉, 最后只能
	// 换到 8001 端口, 并且在交付说明里写"旧进程我没法清掉, 你那边方便的话
	// 手动 kill 29560". **一个连自己起的进程都停不掉的 OS 是不成立的**,
	// 而且这种残留会一直占着端口累积下去.
	//
	// 挂上之后它看到的就只是自己命名空间里的进程 —— 既是一致的, 也更干净:
	// 它本来就不该看见宿主上别人的进程.
	argv = append(argv, paths.Unshare, "--mount", "--pid", "--fork", "--mount-proc")
	if ctx.NetNS == "" {
		argv = append(argv, "--net")
	}

	// ── 层 3 的命令行先拼好, 因为层 2 要把它整个塞进 sh -c ──
	inner := []string{paths.LandlockRun}
	// 先下执行底座 —— 没有它进程连自己的可执行文件都读不到
	for _, p := range plan.RuntimeFS {
		inner = append(inner, "--ro", p)
	}
	// 设备节点必须 --rw: `> /dev/null` 是写操作.
	// 下成 --ro 的话现象是"起 20 个后台进程, 20 个全挂在
	// cannot open /dev/null" —— 而且只在真跑命令时才暴露.
	for _, p := range plan.RuntimeDevices {
		inner = append(inner, "--rw", p)
	}
	for _, r := range plan.FS {
		flag := "--ro"
		for _, a := range r.Access {
			if a == "write" {
				flag = "--rw"
			}
		}
		inner = append(inner, flag, r.Path)
	}
	// 进程**自己的可执行文件**必须可读, 否则 exec 都做不到.
	//
	// 这是第三次撞上同一类问题了 (前两次: 缺 /usr 动态库、缺 /lib64),
	// 规律已经清楚:
	//
	//	**"进程存在所需的一切" 都是强制机制的前置, 不是它要申请的能力.**
	//	包括: 执行底座 + ABI socket + 它自己的二进制.
	//
	// 这些不该出现在能力集里 —— 用户授权的是"它能碰什么业务数据",
	// 不该操心"它怎么才能跑起来".
	inner = append(inner, "--ro", body.Argv[0])

	// ABI socket 必须可读写, 否则进程连 OS 都调不到
	inner = append(inner, "--rw", ctx.ABISocket, "--")
	// ── 层 4: 用户程序 ──
	inner = append(inner, body.Argv...)

	argv = append(argv, inner...)

	return Launch{Argv: argv, Env: buildEnv(body.Env, ctx)}, nil
}

// shQuote 单引号包裹 —— 路径由 OS 生成不含用户输入, 但仍然不留注入面
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildEnv 环境变量白名单.
//
// 宿主环境**一律不继承**: 长驻进程继承了 env 就摘不掉, 临时约束会变成永久约束.
func buildEnv(extra map[string]string, ctx LaunchContext) map[string]string {
	env := map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin",
		"HOME": "/work",
		"LANG": "C.UTF-8",
		// 进程通过这个 socket 调 ABI. 这是它跟 OS 之间**唯一**的通道
		"NEOX_ABI_SOCKET": ctx.ABISocket,
		"NEOX_ABI_TOKEN":  ctx.ABIToken,
		"NEOX_PID":        ctx.PID,
	}
	// 出网走 OS 的代理. 大小写两套都给 —— curl 认小写、Java/.NET 认大写,
	// 只给一套的话总有一半程序连不出去, 而报出来的是"连接超时",
	// 排查的人根本想不到是环境变量的大小写.
	if ctx.ProxyAddr != "" {
		u := "http://" + ctx.ProxyAddr
		for _, k := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
			env[k] = u
		}
		// npm 不认通用的 proxy 变量, 要自己那一套
		env["npm_config_proxy"] = u
		env["npm_config_https_proxy"] = u
		// **本机回环必须直连, 其余一律走代理.**
		//
		// 这里原来是清空 no_proxy, 理由是"镜像里带着的 NO_PROXY 一旦命中
		// 就是直连, 而直连在这里等于连不上(没有路由)". 那条理由对**外部主机**
		// 成立, 对 127.0.0.1 不成立 —— 回环在 netns 里是通的(见 netns_linux.go
		// 把 lo 拉起来那一段), 直连才是对的, 走代理反而是错的.
		//
		// 少了这一条, agent 一验证自己的工作就撞墙, 而且**只在拿到出网授权
		// 之后才撞**:
		//
		//	没授权   没有代理变量 → 直连 127.0.0.1 → 正常
		//	批过一次 http_proxy 全局生效 → 连自己起的服务也发给代理
		//	         → 代理回 502 空 body → "我明明起了服务却连不上"
		//
		// 在核一个 agent 交付的 HTTP 服务时会出现: 7 个用例全红,
		// 报的是 JSONDecodeError(代理回的 502 没有 body), 跟真实原因
		// 隔着两层. 而"起个服务再 curl 自己"恰恰是提示词里鼓励的验证方式 ——
		// 我们一边让它这么验, 一边在环境里把这条路堵上.
		//
		// 只放回环. 别的一律走代理, 因为**授权是按主机名授的**,
		// 绕过代理就等于绕过授权.
		const loopbackDirect = "127.0.0.1,localhost,::1"
		env["no_proxy"] = loopbackDirect
		env["NO_PROXY"] = loopbackDirect
	}
	for k, v := range extra {
		// 只保护**具体的保留名**, 不是整个 NEOX_ 前缀.
		//
		// 早先按前缀一刀切, 结果应用自己的 NEOX_MODEL / NEOX_TASK 也被剥掉了 ——
		// 真跑第一个 agent 时症状是"模型开关不生效, 跑的还是老脚本",
		// 而且完全没有报错. **管得太宽跟管得太松一样是缺陷.**
		if reservedEnv[k] {
			continue
		}
		env[k] = v
	}
	return env
}

// reservedEnv OS 注入的凭据与身份, 应用不许覆盖 ——
// 否则它能把自己的 pid 改成别人的, 或者指向别人的 socket.
var reservedEnv = map[string]bool{
	"NEOX_ABI_SOCKET": true,
	"NEOX_ABI_TOKEN":  true,
	"NEOX_PID":        true,
}
