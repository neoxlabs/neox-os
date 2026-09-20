package confine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 探测这台机器到底能不能强制约束.
//
// 两条铁律:
//
//  1. **Fail-closed**. 探测不过 → confined 模式拒绝启动, 不降级不 warning.
//     一个自称能隔离而实际没隔离的 OS, 比明确不能隔离的 OS 危险得多.
//
//  2. **只支持不接入 = 不算数**. 内核有某个机制但我们没写对接代码,
//     那它对我们就是不存在的. 算进"可用"就是 fail-open —— 我们会以为
//     自己在强制, 实际什么都没做. 这类机制只进 DetectedNotWired 供诊断.
//
// 不同内核环境的探测结果:
//
//	Docker Desktop linuxkit 6.10.14: landlock 未编译, bpf-lsm 未接入
//	Ubuntu 24.04 kernel 6.8:         landlock 可用 (LSM 列表可见, 但
//	                                 /sys/kernel/security/landlock/abi_version 读不到)
type ProbeDeps struct {
	Platform   string
	FileExists func(string) bool
	ReadFile   func(string) string
	// AskEnforcer 问施加者本人能不能强制, 返回它的自述.
	//
	// **问施加者, 不要猜内核.** 猜内核这件事我已经错过两次:
	// 一次是 seccomp 查 /proc/self/status (问的是"我自己被限制了吗",
	// 不是"内核支持吗"), 一次是 landlock 查 sysfs 里的 abi_version ——
	// 那个入口在不少内核根本不导出, 于是在**有 landlock 的机器上**
	// 判成不支持, 直接拒绝启动.
	//
	// landlock-run --probe 会真去调 landlock_create_ruleset,
	// 那是唯一不会撒谎的答案.
	AskEnforcer func(bin string) string
	Paths       LauncherPaths
}

func RealDeps() ProbeDeps {
	return ProbeDeps{
		Platform:   runtime.GOOS,
		FileExists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		ReadFile: func(p string) string {
			b, err := os.ReadFile(p)
			if err != nil {
				return ""
			}
			return string(b)
		},
		AskEnforcer: func(bin string) string {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, bin, "--probe").CombinedOutput()
			if err != nil && len(out) == 0 {
				return ""
			}
			return string(out)
		},
		Paths: DefaultPaths,
	}
}

// satisfiedBy 需求 → 能满足它的机制 (任一即可).
//
// bpf-lsm 列在 fs-enforce 下是**未来**的事 —— 目前它进不了 Mechanisms,
// 因为 BuildLaunch 还没有对应的下发路径.
var satisfiedBy = map[abi.Requirement][]abi.Mechanism{
	abi.ReqFSEnforce:     {abi.MechLandlock, abi.MechBPFLSM},
	abi.ReqResourceLimit: {abi.MechCgroup2},
	abi.ReqNetIsolate:    {abi.MechNetns},
	abi.ReqSyscallNotify: {abi.MechSeccompUserNotif},
}

// wired 我们已经写了下发代码的机制. 不在这里 = 探到了也不算数.
var wired = map[abi.Mechanism]bool{
	abi.MechLandlock: true, abi.MechCgroup2: true, abi.MechNetns: true,
}

var allMechanisms = []abi.Mechanism{
	abi.MechLandlock, abi.MechBPFLSM, abi.MechCgroup2,
	abi.MechNetns, abi.MechSeccomp, abi.MechSeccompUserNotif,
}

// AllRequirements 我们会施加的全部需求 —— 启动自检时用.
//
// 单列一个函数而不是让调用方各写各的: 需求清单变了, 自检要跟着变,
// 两处各写一份必然会漂.
func AllRequirements() []abi.Requirement {
	return []abi.Requirement{abi.ReqFSEnforce, abi.ReqResourceLimit}
}

func Probe(required []abi.Requirement, deps ProbeDeps) abi.EnforcementProbe {
	if deps.Platform != "linux" {
		return abi.EnforcementProbe{
			Platform:         deps.Platform,
			Mechanisms:       []abi.Mechanism{},
			DetectedNotWired: []abi.Mechanism{},
			Satisfied:        []abi.Requirement{},
			Missing:          append([]abi.Requirement(nil), required...),
			Usable:           false,
			Reason:           fmt.Sprintf("强制约束只在 Linux 上可用, 当前是 %s", deps.Platform),
		}
	}

	mechanisms := []abi.Mechanism{}
	notWired := []abi.Mechanism{}
	limits := []string{}
	for _, m := range allMechanisms {
		if !detect(m, deps) {
			continue
		}
		if wired[m] {
			mechanisms = append(mechanisms, m)
		} else {
			notWired = append(notWired, m)
		}
		if lim := limitOf(m, deps); lim != "" {
			limits = append(limits, lim)
		}
	}

	satisfied := []abi.Requirement{}
	missing := []abi.Requirement{}
	for _, r := range required {
		ok := false
		for _, m := range satisfiedBy[r] {
			for _, have := range mechanisms {
				if have == m {
					ok = true
				}
			}
		}
		if ok {
			satisfied = append(satisfied, r)
		} else {
			missing = append(missing, r)
		}
	}

	p := abi.EnforcementProbe{
		Platform:         deps.Platform,
		Mechanisms:       mechanisms,
		DetectedNotWired: notWired,
		Satisfied:        satisfied,
		Missing:          missing,
		Limits:           limits,
		Usable:           len(missing) == 0,
	}
	if !p.Usable {
		p.Reason = explain(missing, notWired)
	}
	return p
}

// limitOf 这个机制自述的强制上限. 空 = 没有已知限制.
//
// 探到"支持"不等于"全都拦得住". landlock-run --probe 可能报告
// "partially enforced (older ABI)" —— 老 ABI 缺 refer/truncate 那些访问位,
// 也就是说文件系统约束在, 但某些操作拦不住.
//
// 这件事必须往上报. **我们以为关住了、实际只关住一部分, 比明说
// "只能关住这些"危险得多** —— 后者用户还能自己决定要不要跑.
func limitOf(m abi.Mechanism, deps ProbeDeps) string {
	if m != abi.MechLandlock || deps.AskEnforcer == nil {
		return ""
	}
	said := deps.AskEnforcer(deps.Paths.LandlockRun)
	if strings.Contains(said, "partial") {
		return "landlock: 内核 ABI 较老, 只能部分强制 (缺少较新的访问位)"
	}
	return ""
}

func detect(m abi.Mechanism, deps ProbeDeps) bool {
	switch m {
	case abi.MechLandlock:
		// **先问施加者本人.** landlock-run --probe 真去调
		// landlock_create_ruleset, 那是唯一不会撒谎的答案.
		//
		// 之前是猜内核: 查 sysfs 的 abi_version, 读不到就翻 LSM 列表.
		// 而 abi_version 这个入口在不少内核压根不导出 (这台 6.8 就是),
		// 于是在**真的有 landlock 的机器上**判成不支持 —— 假阴性,
		// 后果是明明关得住却拒绝启动.
		if !deps.FileExists(deps.Paths.LandlockRun) {
			return false // 施加者不在, 内核支持也没用
		}
		if deps.AskEnforcer != nil {
			if said := deps.AskEnforcer(deps.Paths.LandlockRun); said != "" {
				// 判据是它说**自己在强制**, 不是它提到了 landlock 这个词.
				// 单测抓到过: "landlock: unavailable" 也含 "landlock:",
				// 于是施加者说不行被读成了行 —— 那正是 fail-open.
				return strings.Contains(said, "enforced")
			}
		}
		// 问不到才退回猜内核 (测试里没接 AskEnforcer 就走这条)
		return deps.FileExists("/sys/kernel/security/landlock/abi_version") ||
			strings.Contains(lsm(deps), "landlock")
	case abi.MechBPFLSM:
		return strings.Contains(lsm(deps), "bpf")
	case abi.MechCgroup2:
		// 只需要 v2 统一层级 + 一个 POSIX shell.
		// 曾经还要求 cgexec —— 那是 cgroup v1 的工具, v2 下根本不工作.
		return deps.FileExists(deps.Paths.CgroupRoot+"/cgroup.controllers") &&
			deps.FileExists(deps.Paths.Sh)
	case abi.MechNetns:
		return deps.FileExists("/proc/self/ns/net") && deps.FileExists(deps.Paths.Unshare)
	case abi.MechSeccomp:
		return deps.FileExists("/proc/sys/kernel/seccomp/actions_avail")
	case abi.MechSeccompUserNotif:
		// 判据是内核**支持**哪些 action, 不是"我自己有没有被限制".
		// 原来查 /proc/self/status 的 Seccomp 位, 那问的是后者 —— 真机才发现.
		return strings.Contains(
			deps.ReadFile("/proc/sys/kernel/seccomp/actions_avail"), "user_notif")
	}
	return false
}

func explain(missing []abi.Requirement, notWired []abi.Mechanism) string {
	parts := make([]string, len(missing))
	for i, m := range missing {
		parts[i] = string(m)
	}
	s := "无法满足: " + strings.Join(parts, ", ")
	if len(notWired) > 0 {
		names := make([]string, len(notWired))
		for i, m := range notWired {
			names[i] = string(m)
		}
		s += fmt.Sprintf(" (内核有 %s 但我们尚未接入, 不能算数)", strings.Join(names, "/"))
	}
	return s
}

func lsm(deps ProbeDeps) string { return deps.ReadFile("/sys/kernel/security/lsm") }
