//go:build linux

package confine

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
)

// 出网 —— OS 是它唯一的出口.
//
// ── 为什么不做 NAT + 白名单 ──
//
// 常见做法是给 netns 接上 NAT 然后用 iptables 按 IP 放行. 那个做法是坏的:
// 能力集里写的是**主机名** (registry.npmjs.org), 而 CDN 的 IP 随时在变 ——
// 按 IP 拦要么拦错要么漏, 而且漏的时候一声不吭.
//
// ── 拓扑即强制 ──
//
// 这里的做法是让 netns 里**只有一根 veth**, 对端在 OS 手上:
//
//	netns 内: veth + lo, **没有默认路由, 没有 DNS**
//	          唯一可达的地址就是对端那一个 /30 地址
//	OS 侧:    在那个地址上跑一个 CONNECT 代理, 按能力集的主机名放行
//
// 关键在于: 不走代理的程序**不是被规则拒绝, 而是根本没有路可走**.
// 想绕过代理直连一个 IP 也不行 —— 那条路不存在.
// 这跟"配了个 http_proxy 环境变量"完全是两回事: 后者只是建议.
//
// DNS 也刻意不给: 主机名由代理这一端解析. 进程连"能查什么域名"
// 都不该知道 —— 少一条侧信道.
//
// ── 为什么用具名 netns 而不是 unshare --net ──
//
// veth 要在**进程起来之前**放进 netns, 而 `unshare --net` 建的是匿名 ns,
// 拿到它得先知道子进程的 pid —— 那要一套父子握手 (子进程报 pid、
// 等父进程配好网再 exec), 多一个只在出错时才暴露的时序面.
//
// 具名 ns 让整件事变成**顺序执行**: OS 先建好网, 再 `ip netns exec` 起进程.
// 没有握手就没有时序 bug.

// NetNamespace 一个进程的网络出口
type NetNamespace struct {
	// Name netns 名字, 传给 `ip netns exec`
	Name string
	// HostAddr OS 侧地址 —— 代理监听在这里. 也是 netns 里唯一可达的地址
	HostAddr string
	// GuestAddr 进程侧地址
	GuestAddr string
	// hostLink OS 侧网卡名, 拆的时候要
	hostLink string
	// idx 分配出去的下标, 释放时要还回去. -1 = 没占过池子(只有回环的那种)
	idx int
}

// SetupLoopbackOnlyNamespace 建一个**只有回环**的命名空间: 没有 veth,
// 没有地址, 没有路由 —— 出不去, 但自己连得上自己.
//
// ── 为什么断网也要建具名 ns ──
//
// 原来没有出网授权时走的是 `unshare --net` 的匿名命名空间. 那里面 lo
// 是**存在但 DOWN 的** —— 内核默认如此, 新 ns 一律这样. 于是 127.0.0.1
// 谁也连不上, 而"起个服务再打它验证"正是 agent 验证自己工作的主要手段
// (起服务 curl、跑测试连本地库、起 dev server).
//
// 回环一个字节都出不了这个命名空间, 关着它不带来任何安全, 只是把那一整类
// 活废掉. HTTP 服务无法用 curl 验证时, 只能先诊断出 "lo state DOWN" 再
// `ip link set lo up` 才继续 —— 一件本该 OS 保证的事变成了模型的偏方.
//
// 修法不是在启动命令行里塞一条 `ip link set lo up`(那会把整条命令行包进
// sh -c, 参数不再逐个可见, 审计和三条既有用例一起废掉), 而是**把两条路
// 合成一条**: 断网也用具名 ns, 跟有授权那条同一套代码把 lo 拉起来.
// "连不出去"于是从"匿名 ns 恰好没网卡"变成一个看得见的事实:
// 这个 ns 里除了 lo 什么都没有.
func SetupLoopbackOnlyNamespace(pid string, ipBin string) (*NetNamespace, error) {
	if ipBin == "" {
		ipBin = "/sbin/ip"
	}
	ns := &NetNamespace{Name: "neox-" + sanitizeName(pid), idx: -1}
	// 残留会让 add 直接失败. 幂等的建立比"报错让人手工清"强
	_ = run(ipBin, "netns", "del", ns.Name)
	steps := loopbackOnlySteps(ns.Name, ipBin)
	for _, s := range steps {
		if err := run(ipBin, s...); err != nil {
			ns.Teardown(ipBin)
			return nil, fmt.Errorf("建回环命名空间失败 (%s): %w", strings.Join(s, " "), err)
		}
	}
	return ns, nil
}

// ProxyAddr 代理的监听地址 —— 进程的 http_proxy 指向它
func (n *NetNamespace) ProxyAddr() string {
	return net.JoinHostPort(n.HostAddr, fmt.Sprint(ProxyPort))
}

// ProxyPort OS 侧代理端口. 固定值 —— 每个进程有自己的地址, 不必再区分端口
const ProxyPort = 3128

// netnsPool 下标分配. 每个进程一个 /30, 源地址即身份 ——
// 代理据此知道**是谁在连**, 从而套用那个进程的能力集.
//
// 不用一个大网段加查表: 一个 /30 一个进程, 源 IP 直接就是身份,
// 没有"查错了会串权限"这种失败模式.
var netnsPool = struct {
	sync.Mutex
	used map[int]bool
}{used: map[int]bool{}}

// allocIdx 分配一个没人用的下标.
//
// **必须整台机器唯一, 不只是本进程内唯一.**
//
// 网卡名 (nxh<idx>) 和 /30 地址都是**机器级**的资源. 只查进程内的
// used 表, 两个 OS 实例会同时分到 0 号 —— 撞同一个网卡名、
// 同一对 IP. 这跟当年 socket 路径撞车是同一个错:
// 单实例内唯一 ≠ 机器上唯一.
//
// 所以还要问一句内核: 这个名字的网卡在不在. 内核是唯一的真相源,
// 而且它天然覆盖"别的实例建的"和"上次没拆干净的"两种情况.
func allocIdx(ipBin string) (int, error) {
	netnsPool.Lock()
	defer netnsPool.Unlock()
	// 10.77.a.b/30 —— a 是 idx/64, b 是 (idx%64)*4
	for i := 0; i < 64*256; i++ {
		if netnsPool.used[i] {
			continue
		}
		if linkExists(ipBin, fmt.Sprintf("nxh%d", i)) {
			continue // 机器上已经有了 —— 别的实例的, 或者上次的残留
		}
		netnsPool.used[i] = true
		return i, nil
	}
	return 0, fmt.Errorf("出网命名空间用满了 (%d 个)", 64*256)
}

func linkExists(ipBin, name string) bool {
	return run(ipBin, "link", "show", name) == nil
}

func freeIdx(i int) {
	netnsPool.Lock()
	netnsPool.used[i] = false
	netnsPool.Unlock()
}

func addrsFor(idx int) (host, guest string) {
	a := idx / 64
	b := (idx % 64) * 4
	return fmt.Sprintf("10.77.%d.%d", a, b+1), fmt.Sprintf("10.77.%d.%d", a, b+2)
}

// SetupNetNamespace 建一个只通向 OS 的网络命名空间.
//
// 全部做完才返回 —— 进程起来时网已经是好的, 不存在"起来了但网还没配好"
// 这个窗口. 任何一步失败都会把已经做的拆干净再报错:
// **半配好的网络比没有网络危险** (可能默认路由已经在了而过滤还没上).
func SetupNetNamespace(pid string, ipBin string) (*NetNamespace, error) {
	if ipBin == "" {
		ipBin = "/sbin/ip"
	}
	idx, err := allocIdx(ipBin)
	if err != nil {
		return nil, err
	}
	host, guest := addrsFor(idx)
	ns := &NetNamespace{
		Name:      "neox-" + sanitizeName(pid),
		HostAddr:  host,
		GuestAddr: guest,
		// 网卡名上限 15 字符, 所以用下标不用 pid
		hostLink: fmt.Sprintf("nxh%d", idx),
		idx:      idx,
	}
	guestLink := fmt.Sprintf("nxg%d", idx)

	// 上一次没拆干净的残留会让 `ip netns add` 直接失败.
	// 先清一遍 —— 幂等的建立比"报错让人手工清"强.
	_ = run(ipBin, "netns", "del", ns.Name)
	_ = run(ipBin, "link", "del", ns.hostLink)

	steps := [][]string{
		{"netns", "add", ns.Name},
		{"link", "add", ns.hostLink, "type", "veth", "peer", "name", guestLink},
		{"link", "set", guestLink, "netns", ns.Name},
		{"addr", "add", host + "/30", "dev", ns.hostLink},
		{"link", "set", ns.hostLink, "up"},
		{"netns", "exec", ns.Name, ipBin, "addr", "add", guest + "/30", "dev", guestLink},
		{"netns", "exec", ns.Name, ipBin, "link", "set", guestLink, "up"},
		{"netns", "exec", ns.Name, ipBin, "link", "set", "lo", "up"},
		// **刻意不加默认路由**: 加了就等于放它出去.
		// 唯一可达的是 /30 对端, 也就是 OS 自己.
	}
	for _, s := range steps {
		if err := run(ipBin, s...); err != nil {
			ns.Teardown(ipBin)
			return nil, fmt.Errorf("配网络失败 (%s): %w", strings.Join(s, " "), err)
		}
	}
	return ns, nil
}

// Teardown 拆干净. 幂等 —— 拆一半的网络下次会让建立直接失败.
func (n *NetNamespace) Teardown(ipBin string) {
	if n == nil {
		return
	}
	if ipBin == "" {
		ipBin = "/sbin/ip"
	}
	// 删 netns 会连带删掉里面那半根 veth; 宿主这半根单独删.
	// 两个都忽略错误: 可能本来就没建成
	_ = run(ipBin, "netns", "del", n.Name)
	// 只有回环的那种没建过 veth, 也没占过池子 —— 别去删一根不存在的网卡,
	// 更别把 -1 还进池子(那会踩坏下标 0 的记账)
	if n.hostLink != "" {
		_ = run(ipBin, "link", "del", n.hostLink)
	}
	if n.idx >= 0 {
		freeIdx(n.idx)
	}
}

func run(bin string, args ...string) error {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sanitizeName netns 名字只留安全字符 —— 它会进命令行
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// SweepOrphanNetNamespaces 扫掉上次留下的空网络命名空间和断头网卡.
//
// ── 为什么必须有 ──
//
// Teardown 挂在 defer 上, 而 defer **只在正常退出时跑**. 进程被 kill、
// 宿主崩了、机器断电 —— 每一种都会留下一个 netns 和一根 veth,
// 而且**永远不会有人再来收**.
//
// 这跟 cgroup 叶子是同一个错误: 曾出现 18 个空叶子、零个活进程.
// 结论是"退出时清理必须配启动时清扫"; 只做退出清理时, 还会留下 3 个
// 残留 netns.
//
// **退出时清理只是优化, 启动时清扫才是保证.**
//
// ── 只收真正没人用的 ──
//
// 里面还有进程的不动 (可能是另一个 OS 实例正在用的). 判断依据是内核:
// `ip netns pids` 为空才删. 猜不得 —— 删掉别人正在用的网络,
// 那个进程会在毫无征兆的情况下断网.
func SweepOrphanNetNamespaces(ipBin string) int {
	if ipBin == "" {
		ipBin = "/sbin/ip"
	}
	out, err := exec.Command(ipBin, "netns", "list").Output()
	if err != nil {
		return 0
	}
	swept := 0
	for _, line := range strings.Split(string(out), "\n") {
		// 形如 "neox-p123_1_2 (id: 0)"
		name := strings.TrimSpace(strings.SplitN(strings.TrimSpace(line), " ", 2)[0])
		if !strings.HasPrefix(name, "neox-") {
			continue
		}
		pids, perr := exec.Command(ipBin, "netns", "pids", name).Output()
		if perr != nil || strings.TrimSpace(string(pids)) != "" {
			continue // 还有人在里面 —— 可能是另一个实例的, 不许动
		}
		if run(ipBin, "netns", "del", name) == nil {
			swept++
		}
	}
	// 断头的宿主侧网卡: 对端没了之后它会掉到 DOWN.
	// 活着的那些是 UP (对端在 ns 里也起着), 所以这个判据分得开.
	if links, lerr := exec.Command(ipBin, "-o", "link", "show").Output(); lerr == nil {
		for _, line := range strings.Split(string(links), "\n") {
			i := strings.Index(line, "nxh")
			if i < 0 || !strings.Contains(line, "state DOWN") {
				continue
			}
			name := line[i:]
			if j := strings.IndexAny(name, "@: "); j > 0 {
				name = name[:j]
			}
			if run(ipBin, "link", "del", name) == nil {
				swept++
			}
		}
	}
	return swept
}
