package confine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 类型别名 —— 让本包读起来不必到处写 abi.
type (
	Capability = abi.Capability
	Plan       = abi.ConfinementPlan
)

type PlanOptions struct {
	// VolumeRoot 卷根 — 所有 read/write scope 都相对它解析
	VolumeRoot string
	// RuntimeFS 执行底座的实际路径列表.
	//
	// **必须由调用方按本机实际情况给**, 因为它随平台变:
	// arm64 上没有 /lib64 (那是 x86-64 的布局), 而 landlock-run 对
	// 不存在的路径是硬错误 —— 真机第一次跑就撞上.
	// 纯函数不该猜环境, 所以这里只收不查. 用 ResolveRuntimeFS 生成.
	RuntimeFS []string
	// RuntimeDevices 必须可写的设备节点. 同样按本机实际情况给 ——
	// 容器里可能连 /dev/tty 都没有. nil = 用 DefaultRuntimeDevices.
	RuntimeDevices []string
	MemoryMaxBytes *int64
	CPUWeight      *int64
	PidsMax        *int64
}

// DefaultRuntimeFS 执行底座 —— 任何进程都要能读到的路径, 否则 exec 都做不到.
//
// 这是**最小可执行集**, 不是方便集:
//
//	/usr /bin /sbin /lib /lib64  可执行文件与动态库
//	/etc                         解析器/时区/证书
//	/proc                        node/python/java 都要读它
//
// 关于 /proc 的安全判断: 可接受, 因为我们同时开了 PID namespace ——
// 进程在 /proc 里**只看得到自己**. 两者是配套的, 不能只留一个.
//
// 诚实的暴露面: 授 /etc 只读意味着进程能读 /etc/passwd (用户名列表).
// 收窄它的正确做法是给进程一个最小 rootfs, 而不是继续加白名单.
var DefaultRuntimeFS = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/proc"}

// DefaultRuntimeDevices 必须可写的设备节点.
//
// ── 为什么这几个是必需的 ──
//
//	/dev/null     `> /dev/null` 是**写**操作. shell 的作业控制、
//	              几乎每个构建脚本都要它
//	/dev/zero     mmap 匿名内存的老写法, 一些运行时还在用
//	/dev/urandom  node/python/go 起来就要读它做种子
//	/dev/random   同上, 少数程序坚持用它
//	/dev/tty      交互式程序探测"有没有终端"时会打开它
//
// ── 为什么不是整个 /dev ──
//
// 整个 /dev 授可写等于把裸磁盘 (/dev/vda) 和物理内存 (/dev/mem)
// 一起给出去 —— 那比不设约束好不了多少.
//
// 真机验过: 逐个授之后 20 个并发子进程正常跑完、`> /dev/null` 写得进,
// 而 `head -c 1 /dev/vda` 仍然被拒.
//
// ── 为什么以前没有 ──
//
// 以前 agent 只有文件工具, 从来不起子进程, 也就永远碰不到 /dev.
// 一放开命令, 第一条真命令就撞上了: 20 个后台进程全挂在
// `cannot open /dev/null: Permission denied`.
var DefaultRuntimeDevices = []string{
	"/dev/null", "/dev/zero", "/dev/urandom", "/dev/random", "/dev/tty",
}

// requiredRequirements 需要满足哪些强制需求 —— 恒定全套, 跟能力集无关.
//
// 刻意不按能力裁剪: 没有出网授权时**更**需要 net-isolate, 因为断网要靠隔离,
// 不能靠"白名单里没有"(那只是配置不是隔离).
//
// **只列我们真正会施加的**. 曾经这里列了 seccomp, 但 launcher 从没施加过
// 任何 seccomp 过滤 —— 为一个自己不用的东西拒绝开机是假严格.
var requiredRequirements = []abi.Requirement{
	abi.ReqFSEnforce, abi.ReqResourceLimit, abi.ReqNetIsolate,
}

// ResolveRuntimeFS 按本机实际情况筛出存在的底座路径.
//
// 这是唯一需要碰文件系统的一步, 刻意从 PlanFor 里分出来 ——
// 纯函数保持可测, 环境探测集中在一处.
func ResolveRuntimeFS(exists func(string) bool) []string {
	out := make([]string, 0, len(DefaultRuntimeFS))
	for _, p := range DefaultRuntimeFS {
		if exists(p) {
			out = append(out, p)
		}
	}
	return out
}

// PlanFor 把能力集翻译成内核规则.
//
// 缺省是拒绝: 空能力集翻译出来是"什么都碰不到、完全断网、不能起子进程".
// 注意它仍然 requires 全套机制 —— **越是什么都不给, 越需要内核真的能拦住**.
func PlanFor(caps []Capability, opts PlanOptions) (Plan, error) {
	fsAccess := map[string]map[string]bool{}
	var net []abi.NetRule
	allowSub := false

	for _, c := range caps {
		switch c.Axis {
		case abi.AxisRead, abi.AxisWrite:
			abs, err := ResolveInVolume(opts.VolumeRoot, c.Scope)
			if err != nil {
				return Plan{}, err
			}
			if fsAccess[abs] == nil {
				fsAccess[abs] = map[string]bool{}
			}
			fsAccess[abs][string(c.Axis)] = true
			// 可写必然可读 —— landlock 里 write 不隐含 read, 不同时授予 read
			// 会让已批准的写入仍然无法正常使用.
			if c.Axis == abi.AxisWrite {
				fsAccess[abs]["read"] = true
			}
		case abi.AxisNet:
			host, port, hasPort := splitHostPort(c.Scope)
			r := abi.NetRule{Host: host}
			if hasPort {
				r.Port = port
			}
			net = append(net, r)
		case abi.AxisProc:
			allowSub = true
		case abi.AxisSecret:
			// 凭据不走文件系统, 由 OS 通过 ABI 通道按次发放.
			// 刻意什么都不做 —— 凭据不该以"某个路径可读"的形式存在.
		}
	}

	fs := make([]abi.FSRule, 0, len(fsAccess))
	for path, acc := range fsAccess {
		var access []string
		for a := range acc {
			access = append(access, a)
		}
		sort.Strings(access)
		fs = append(fs, abi.FSRule{Path: path, Access: access})
	}
	// 顺序稳定 —— 命令行可比对, 审计日志才有意义
	sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })

	// 内存也要有缺省上限.
	//
	// 如果 memory.max = max —— **内存限额就没有施加**,
	// 因为调用方没传, 而这里对 nil 就直接不写.
	// pids 有缺省而内存没有, 是漏了, 不是有意为之:
	// 一个跑飞的 agent 能吃光整台机器的内存, 而这台机器上还跑着别的对话.
	//
	// 额度跟"能不能起进程"绑在一起, 因为吃内存的从来不是 agent 自己:
	//
	//	没有 proc   512MB. agent 是个 Go 小程序, 峰值在读大文件那步,
	//	            而读已经按 24KB 分片了. 真吃到说明失控了
	//	有 proc     2GB. 一次 go build 或 tsc 单个进程就能上 GB,
	//	            按 512MB 卡的话命令会被 OOM killer 砍掉,
	//	            而报出来的是一句没头没尾的 "signal: killed" ——
	//	            比"不给这个能力"更难查
	memMax := opts.MemoryMaxBytes
	if memMax == nil {
		v := int64(512 << 20)
		if allowSub {
			v = 2 << 30
		}
		memMax = &v
	}
	pidsMax := opts.PidsMax
	if pidsMax == nil {
		// pids.max 是**资源上限**, 不是"禁不禁止起子进程"的开关.
		//
		// 它数的是**任务(线程)**, 不是进程. 如果在没有 proc 能力时
		// 设成 1 来表达"fork 不出来", Go/Java/Node 这些多线程
		// 运行时连启动都做不到: `failed to create new OS thread`.
		//
		// **不再试图禁止起子进程**, 这是想清楚之后的决定, 不是妥协:
		//
		// 拦 clone/execve 要靠 seccomp, 而当前 seccomp
		// 根本不可靠(装了过滤器却一个调用都没拦到). 更根本的是,
		// 拦住了也没有意义 —— landlock/netns/cgroup **全部随 fork+exec
		// 继承**, 子进程跟父进程关在同一个笼子里. 起进程不构成逃逸,
		// 它只是多几个任务在同一套约束下跑.
		//
		// 所以 proc 能力现在管的是**配额宽窄**: 授了就给它跑 make -j、
		// go test 这类东西的余量, 没授就只够自己那几个线程.
		v := int64(64)
		if allowSub {
			// 一次 go test ./... 或 npm ci 会同时拉起几十个进程,
			// 每个还带自己的线程. 4096 是"跑得动真活"的量,
			// 同时仍然拦得住 fork 炸弹 —— 上限存在本身才是重点.
			v = 4096
		}
		pidsMax = &v
	}

	runtimeFS := opts.RuntimeFS
	if runtimeFS == nil {
		runtimeFS = append([]string(nil), DefaultRuntimeFS...)
	}
	devices := opts.RuntimeDevices
	if devices == nil {
		devices = append([]string(nil), DefaultRuntimeDevices...)
	}

	if net == nil {
		net = []abi.NetRule{}
	}

	return Plan{
		FS:              fs,
		RuntimeFS:       runtimeFS,
		RuntimeDevices:  devices,
		Net:             net,
		AllowSubprocess: allowSub,
		Cgroup: abi.CgroupLimits{
			MemoryMaxBytes: memMax,
			CPUWeight:      opts.CPUWeight,
			PidsMax:        pidsMax,
		},
		Requires: append([]abi.Requirement(nil), requiredRequirements...),
	}, nil
}

// ResolveInVolume 把卷内路径解析成绝对路径.
//
// 拒绝路径穿越, 不做 ".." 解析 —— 解析等于给自己开后门.
// 连续斜杠不压缩 (UNC 会被压没).
func ResolveInVolume(volumeRoot, scope string) (string, error) {
	root := strings.ReplaceAll(volumeRoot, "\\", "/")
	if len(root) > 1 && strings.HasSuffix(root, "/") {
		root = root[:len(root)-1]
	}
	rel := strings.ReplaceAll(scope, "\\", "/")
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("capability scope 含路径穿越, 拒绝: %s", scope)
		}
	}
	// '*' 在 fs 轴上等于"整个卷" —— 跟 ScopeMatches 的处理必须一致.
	// 裸 '*' 和 '/*' 都要认: 前者是能力集里最常见的写法.
	if rel == "*" || rel == "/*" || rel == "/" || rel == "" {
		if root == "" {
			return "/", nil
		}
		return root, nil
	}
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return root + rel, nil
}

// PrepareVolume 在 spawn 之前把能力集涉及的路径落实到文件系统上.
//
// 为什么必须由 OS 做:
//
//	landlock 装规则时要求路径**已经存在**, 不存在直接报错退出.
//	而"授了写权限的目录"往往还没被创建 —— 进程又没有能力去创建它
//	(它连起都起不来). 这是个先有鸡还是先有蛋的问题,
//	只能由拥有这个卷的 OS 来打破.
//
// 处理方式区分读写, 刻意不一致:
//
//	写路径: 建出来. 授了写就意味着允许它存在
//	读路径: 不建. 不存在就从规则里摘掉, 但**把摘掉的报回去**,
//	        由调用方写进审计 —— 静默摘除会让人以为授权生效了
func PrepareVolume(plan *Plan, mkdirAll func(string) error, exists func(string) bool) []string {
	var dropped []string
	kept := plan.FS[:0]
	for _, r := range plan.FS {
		writable := false
		for _, a := range r.Access {
			if a == "write" {
				writable = true
			}
		}
		if writable {
			// **已经存在的东西不要去 mkdir.**
			//
			// 授权的目标可能是一个**文件**而不是目录: 用户批准
			// "允许写 bench/src/config.js" 时授的就是那个文件.
			// 对一个已存在的文件调 mkdirAll 必然失败(它不是目录),
			// 于是这条规则被当成"建不出来"摘掉 —— **用户的批准静默失效**.
			//
			// 授权记上了、并进了 plan、标签也对, 但如果对已存在的文件调用
			// mkdirAll, 仍会在这一行失败并让用户的批准静默失效.
			if exists(r.Path) {
				kept = append(kept, r)
				continue
			}
			if err := mkdirAll(r.Path); err != nil {
				// 建不出来就摘掉并报告 —— 总比让整个进程起不来强
				dropped = append(dropped, r.Path)
				continue
			}
			kept = append(kept, r)
			continue
		}
		if exists(r.Path) {
			kept = append(kept, r)
		} else {
			dropped = append(dropped, r.Path)
		}
	}
	plan.FS = kept

	// 底座同理: 不存在的直接摘, 否则 landlock 会因为一条无关路径拒绝启动
	var base []string
	for _, p := range plan.RuntimeFS {
		if exists(p) {
			base = append(base, p)
		} else {
			dropped = append(dropped, p)
		}
	}
	plan.RuntimeFS = base

	// 设备节点也要摘 —— 精简的容器镜像里没有 /dev/tty 是常事,
	// 而 landlock-run 对不存在的路径是硬错误: 一个没人用的节点
	// 会让整个进程起不来.
	var devs []string
	for _, p := range plan.RuntimeDevices {
		if exists(p) {
			devs = append(devs, p)
		} else {
			dropped = append(dropped, p)
		}
	}
	plan.RuntimeDevices = devs
	return dropped
}
