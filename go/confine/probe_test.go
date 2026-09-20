package confine

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 探测要**问施加者**, 不要猜内核.
//
// 猜内核这件事错过两次: seccomp 查 /proc/self/status (问的是
// "我自己被限制了吗", 不是"内核支持吗"), landlock 查 sysfs 的
// abi_version —— 那个入口在不少内核根本不导出 (这台 6.8 就是),
// 于是在**真的有 landlock 的机器上**判成不支持, 后果是明明关得住却拒绝启动.
func TestLandlockAsksTheEnforcerNotTheKernel(t *testing.T) {
	deps := ProbeDeps{
		Platform: "linux",
		// sysfs 读不到, LSM 列表也读不到 —— 模拟没挂 securityfs 的容器
		FileExists: func(p string) bool { return p == DefaultPaths.LandlockRun },
		ReadFile:   func(string) string { return "" },
		AskEnforcer: func(string) string {
			return "landlock: fully enforced\n"
		},
		Paths: DefaultPaths,
	}
	if !detect(abi.MechLandlock, deps) {
		t.Fatal("施加者说能强制, 却因为猜内核判成了不支持")
	}
}

// 施加者说不行就是不行 —— 不许退回去猜内核把自己哄过关
func TestEnforcerSayingNoIsFinal(t *testing.T) {
	deps := ProbeDeps{
		Platform:    "linux",
		FileExists:  func(p string) bool { return true }, // sysfs 全在, 猜内核会说"支持"
		ReadFile:    func(string) string { return "landlock,capability" },
		AskEnforcer: func(string) string { return "landlock: unavailable\n" },
		Paths:       DefaultPaths,
	}
	if detect(abi.MechLandlock, deps) {
		t.Fatal("施加者说不行, 却被猜内核的结果盖过去了 —— 那是 fail-open")
	}
}

// 施加者不在就是不在, 内核支持也没用
func TestNoEnforcerBinaryMeansNoEnforcement(t *testing.T) {
	deps := ProbeDeps{
		Platform:   "linux",
		FileExists: func(p string) bool { return p != DefaultPaths.LandlockRun },
		ReadFile:   func(string) string { return "landlock" },
		Paths:      DefaultPaths,
	}
	if detect(abi.MechLandlock, deps) {
		t.Fatal("没有施加者却说能强制")
	}
}

// "只能部分强制"必须如实上报.
//
// 我们以为关住了、实际只关住一部分, 比明说"只能关住这些"危险得多 ——
// 后者用户还能自己决定要不要跑.
func TestPartialEnforcementIsReported(t *testing.T) {
	deps := ProbeDeps{
		Platform:    "linux",
		FileExists:  func(string) bool { return true },
		ReadFile:    func(string) string { return "landlock,capability" },
		AskEnforcer: func(string) string { return "landlock: partially enforced (older ABI)\n" },
		Paths:       DefaultPaths,
	}
	p := Probe([]abi.Requirement{abi.ReqFSEnforce}, deps)
	if len(p.Limits) == 0 {
		t.Fatal("部分强制没有上报 —— 我们会以为全关住了")
	}
	if !strings.Contains(p.Limits[0], "部分强制") {
		t.Fatalf("上报的话说不清: %v", p.Limits)
	}
}

// 完全强制时不该无中生有报限制
func TestFullEnforcementReportsNoLimits(t *testing.T) {
	deps := ProbeDeps{
		Platform:    "linux",
		FileExists:  func(string) bool { return true },
		ReadFile:    func(string) string { return "landlock" },
		AskEnforcer: func(string) string { return "landlock: fully enforced\n" },
		Paths:       DefaultPaths,
	}
	if p := Probe([]abi.Requirement{abi.ReqFSEnforce}, deps); len(p.Limits) != 0 {
		t.Fatalf("完全强制却报了限制: %v", p.Limits)
	}
}

// 施加者不在时**绝不能报绿**.
//
// 报绿的后果是最坏的一种: 我们以为约束在, 实际什么都没有,
// 而且没有任何信号. 宁可显式跳过(让人看见), 也不许默认通过.
func TestSelfCheckNeverGreenWithoutEnforcer(t *testing.T) {
	results := SelfCheck(LauncherPaths{LandlockRun: "/根本不存在的施加者"})
	if len(results) == 0 {
		t.Fatal("没有施加者却什么都不说")
	}
	for _, r := range results {
		if r.OK && !r.Skipped {
			t.Fatalf("没有施加者却报绿: %+v", r)
		}
	}
	if !results[0].Skipped {
		t.Fatalf("该显式标成跳过, 让人看得见: %+v", results[0])
	}
}

// 跳过要说清楚原因 —— 只说"skipped"下次没人知道为什么
func TestSkipExplainsWhy(t *testing.T) {
	r := SelfCheck(LauncherPaths{LandlockRun: "/no/such/bin"})[0]
	if !strings.Contains(r.Detail, "/no/such/bin") {
		t.Fatalf("没说清跳过的原因: %q", r.Detail)
	}
}

// seccomp 不许进 wired —— 它不可靠，而且不可靠的方向是 fail-open。
//
// 同一份代码能拦住读取 /agentwork 下的文件，却一次也拦不住读取 /etc 下的
// 文件（filter 明明装上了）。一旦把它算作“已接入的强制手段”，
// 这种情况就会变成一个静默的洞。
//
// 要接入它必须先验证拦截稳定，不能仅凭探测结果显示“支持”就算作强制手段。
func TestSeccompNotCountedAsWiredEnforcement(t *testing.T) {
	if wired[abi.MechSeccompUserNotif] {
		t.Fatal("seccomp user notification 被算作已接入的强制手段 —— " +
			"真机上它会出现'filter 在但不拦'的情况, 那是 fail-open")
	}
	// 探到了要如实报在 DetectedNotWired 里, 不能假装不存在
	deps := ProbeDeps{
		Platform:   "linux",
		FileExists: func(string) bool { return true },
		ReadFile: func(p string) string {
			if strings.Contains(p, "actions_avail") {
				return "kill_process kill_thread trap errno user_notif log allow"
			}
			return "landlock"
		},
		Paths: DefaultPaths,
	}
	p := Probe([]abi.Requirement{abi.ReqFSEnforce}, deps)
	found := false
	for _, m := range p.DetectedNotWired {
		if m == abi.MechSeccompUserNotif {
			found = true
		}
	}
	if !found {
		t.Fatalf("探到了却没报在 DetectedNotWired 里: %+v", p.DetectedNotWired)
	}
}
