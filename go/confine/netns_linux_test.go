//go:build linux

package confine

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 真机验网络编排. 要 root 和 iproute2 —— 没有就跳过, 不假装通过.
func TestNetNamespaceTopology(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("要 root 才能建 netns")
	}
	ipBin, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("没有 iproute2")
	}
	ns, err := SetupNetNamespace("test-topo", ipBin)
	if err != nil {
		t.Fatalf("建网失败: %v", err)
	}
	defer ns.Teardown(ipBin)

	in := func(args ...string) string {
		out, _ := exec.Command(ipBin,
			append([]string{"netns", "exec", ns.Name}, args...)...).CombinedOutput()
		return string(out)
	}

	// ① 对端可达 —— 否则代理根本用不上
	if out := in("ping", "-c1", "-W2", ns.HostAddr); !strings.Contains(out, "1 received") {
		t.Fatalf("到 OS 那一端不通, 代理就是个摆设:\n%s", out)
	}
	// ② **没有默认路由** —— 这是整个设计的强制来源.
	//    有了它就等于放它直接出去, 代理白搭.
	if out := in("ip", "route", "show", "default"); strings.TrimSpace(out) != "" {
		t.Fatalf("不该有默认路由, 有了就等于放它出去:\n%s", out)
	}
	// ③ 外网地址不可达 —— 不是被规则拒, 是没有路
	if out := in("ping", "-c1", "-W2", "1.1.1.1"); strings.Contains(out, "1 received") {
		t.Fatalf("居然能直接出网, 拓扑没关住:\n%s", out)
	}
}

// 拆干净 —— 拆一半的网络会让下次建立直接失败
func TestNetNamespaceTeardownIsIdempotent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("要 root")
	}
	ipBin, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("没有 iproute2")
	}
	for i := 0; i < 3; i++ {
		ns, err := SetupNetNamespace("test-idem", ipBin)
		if err != nil {
			t.Fatalf("第 %d 次建网失败 (说明上次没拆干净): %v", i+1, err)
		}
		ns.Teardown(ipBin)
		ns.Teardown(ipBin) // 拆两次不该炸
	}
}

// 启动清扫必须真的收掉残留.
//
// Teardown 挂在 defer 上, 而 defer 只在正常退出时跑 —— 进程被 kill、
// 机器断电都会留下 netns. cgroup 曾积累过 18 个空叶子, 说明退出时清理
// 不能作为唯一保障. 退出时清理只是优化, 启动时清扫才是保证.
func TestSweepCollectsOrphans(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("要 root")
	}
	ipBin, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("没有 iproute2")
	}
	ns, err := SetupNetNamespace("sweep-victim", ipBin)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟"进程被 kill, defer 没跑": 建好之后什么都不做就去扫
	if n := SweepOrphanNetNamespaces(ipBin); n == 0 {
		ns.Teardown(ipBin)
		t.Fatal("清扫一个都没收 —— 残留会永远留着")
	}
	out, _ := exec.Command(ipBin, "netns", "list").Output()
	if strings.Contains(string(out), ns.Name) {
		ns.Teardown(ipBin)
		t.Fatalf("清扫之后还在: %s", ns.Name)
	}
}

// 下标必须**整台机器唯一**, 不只是本进程内唯一.
//
// 网卡名和 /30 地址是机器级资源. 只查进程内的表, 两个 OS 实例会同时
// 分到 0 号 —— 撞同一个网卡名、同一对 IP.
// 这跟当年 socket 路径撞车是同一个错.
func TestAllocSkipsIndexesTakenOnTheMachine(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("要 root")
	}
	ipBin, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("没有 iproute2")
	}
	a, err := SetupNetNamespace("uniq-a", ipBin)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Teardown(ipBin)

	// 模拟"另一个 OS 实例": 清掉进程内的账, 但机器上的网卡还在
	netnsPool.Lock()
	netnsPool.used = map[int]bool{}
	netnsPool.Unlock()

	b, err := SetupNetNamespace("uniq-b", ipBin)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Teardown(ipBin)

	if a.HostAddr == b.HostAddr {
		t.Fatalf("两个实例分到了同一对地址 %s —— 网卡名和 IP 都会撞", a.HostAddr)
	}
}

// 只有回环的那种没建过 veth, 也没占过池子.
//
// 拆的时候去删一根不存在的网卡是白费, 把 -1 还进池子会踩坏下标 0 的记账 ——
// 那种错的症状是**另一个进程的出网地址被顶掉**, 查起来毫无头绪.
func TestLoopbackOnlyTeardownTouchesNothingItDidNotCreate(t *testing.T) {
	before := allocatedIdxCount()
	ns := &NetNamespace{Name: "neox-test-lo", idx: -1}
	ns.Teardown("/nonexistent/ip") // 不该 panic, 也不该动池子
	if got := allocatedIdxCount(); got != before {
		t.Fatalf("动了地址池: %d → %d", before, got)
	}
}

func allocatedIdxCount() int {
	netnsPool.Lock()
	defer netnsPool.Unlock()
	n := 0
	for _, used := range netnsPool.used {
		if used {
			n++
		}
	}
	return n
}
