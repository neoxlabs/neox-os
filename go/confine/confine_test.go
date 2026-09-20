package confine

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

var V = PlanOptions{VolumeRoot: "/work", RuntimeFS: []string{"/usr", "/bin"}}

func mustPlan(t *testing.T, caps []Capability, opts PlanOptions) Plan {
	t.Helper()
	p, err := PlanFor(caps, opts)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	return p
}

// ── 翻译 ────────────────────────────────────────────────────

func TestEmptyCapsDeniesEverything(t *testing.T) {
	p := mustPlan(t, nil, V)
	if len(p.FS) != 0 || len(p.Net) != 0 || p.AllowSubprocess {
		t.Fatalf("空能力集应该什么都不给: %+v", p)
	}
	// pids.max 数的是线程不是进程 —— 设成 1 会让任何多线程运行时起不来.
	// 它是资源上限, 不是"禁止子进程"的开关 (那个还没真正强制, 见 plan.go).
	if p.Cgroup.PidsMax == nil || *p.Cgroup.PidsMax != 64 {
		t.Fatalf("缺省 pidsMax 应为 64, got %v", p.Cgroup.PidsMax)
	}
}

// 没有出网授权时**更**需要网络隔离 —— 断网要靠隔离, 不能靠"白名单里没有"
func TestRequiresAreConstant(t *testing.T) {
	p := mustPlan(t, nil, V)
	want := []abi.Requirement{abi.ReqFSEnforce, abi.ReqResourceLimit, abi.ReqNetIsolate}
	if !reflect.DeepEqual(p.Requires, want) {
		t.Fatalf("requires = %v, want %v", p.Requires, want)
	}
	// 只要求我们真正会施加的 —— 不要求自己都不用的 seccomp
	for _, r := range p.Requires {
		if r == abi.ReqSyscallNotify {
			t.Fatal("不该要求 syscall-notify: launcher 还没施加过任何 seccomp 过滤")
		}
	}
}

// 可写必然可读 —— landlock 里 write 不隐含 read
func TestWriteImpliesRead(t *testing.T) {
	p := mustPlan(t, []Capability{{Axis: abi.AxisWrite, Scope: "/out"}}, V)
	if len(p.FS) != 1 || p.FS[0].Path != "/work/out" ||
		!reflect.DeepEqual(p.FS[0].Access, []string{"read", "write"}) {
		t.Fatalf("got %+v", p.FS)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	if _, err := PlanFor([]Capability{{Axis: abi.AxisRead, Scope: "/../../etc"}}, V); err == nil {
		t.Fatal("路径穿越必须直接拒绝, 不做解析")
	}
}

// 有 proc 能力时配额放宽 —— 但仍然有上限.
//
// 两头都要验: 只验"放宽了"的话, 把上限去掉也能过, 而没有上限
// 等于一个 fork 炸弹就能拖垮整台机器 (上面还跑着别的对话).
func TestProcCapRaisesQuota(t *testing.T) {
	p := mustPlan(t, []Capability{{Axis: abi.AxisProc, Scope: "demo"}}, V)
	if p.Cgroup.PidsMax == nil || *p.Cgroup.PidsMax != 4096 {
		t.Fatalf("有 proc 能力时任务数应放宽到 4096 (够跑 go test/npm ci), got %v",
			p.Cgroup.PidsMax)
	}
	if p.Cgroup.MemoryMaxBytes == nil || *p.Cgroup.MemoryMaxBytes != 2<<30 {
		t.Fatalf("有 proc 能力时内存应放宽到 2GB —— 512MB 会让 go build 被 "+
			"OOM killer 砍掉, 报出来只有一句 signal: killed, got %v",
			p.Cgroup.MemoryMaxBytes)
	}
}

// 没有 proc 能力时配额收窄, 而且**必须有上限** —— 缺省不安全是最糟的
func TestNoProcCapKeepsQuotaTight(t *testing.T) {
	p := mustPlan(t, nil, V)
	if p.Cgroup.PidsMax == nil || *p.Cgroup.PidsMax != 64 {
		t.Fatalf("没有 proc 能力时任务数应是 64, got %v", p.Cgroup.PidsMax)
	}
	if p.Cgroup.MemoryMaxBytes == nil || *p.Cgroup.MemoryMaxBytes != 512<<20 {
		t.Fatalf("内存缺省上限没了 —— 一个跑飞的 agent 能吃光整台机器, got %v",
			p.Cgroup.MemoryMaxBytes)
	}
}

func TestSecretNotAFilePath(t *testing.T) {
	// 凭据不该以"某路径可读"的形式存在
	p := mustPlan(t, []Capability{{Axis: abi.AxisSecret, Scope: "tok"}}, V)
	if len(p.FS) != 0 {
		t.Fatalf("secret 不该翻译成文件系统规则: %+v", p.FS)
	}
}

func TestRuleOrderStable(t *testing.T) {
	p := mustPlan(t, []Capability{
		{Axis: abi.AxisRead, Scope: "/z"}, {Axis: abi.AxisRead, Scope: "/a"},
	}, V)
	if p.FS[0].Path != "/work/a" || p.FS[1].Path != "/work/z" {
		t.Fatalf("顺序必须稳定, 否则命令行没法比对审计: %+v", p.FS)
	}
}

// ── 启动命令行 ──────────────────────────────────────────────

func TestLaunchLayering(t *testing.T) {
	mem := int64(1 << 30)
	opts := V
	opts.MemoryMaxBytes = &mem
	p := mustPlan(t, []Capability{{Axis: abi.AxisWrite, Scope: "/out"}}, opts)
	l, err := BuildLaunch(p, Body{Argv: []string{"/usr/bin/node", "app.js"}, Cwd: "/work"},
		LaunchContext{PID: "p1", CgroupSlice: "neox-os/p1", ABISocket: "/run/neox-os/p1.sock"},
		DefaultPaths)
	if err != nil {
		t.Fatal(err)
	}
	// **cgroup 那层必须在最外面, 在任何 namespace 之前.**
	//
	// 它原来在 unshare 里面, 靠的是"unshare --mount 只复制挂载树,
	// /sys/fs/cgroup 照样看得见" —— 一个没写出来的依赖.
	// `ip netns exec` 会重新挂一个干净的 sysfs 从而破坏它, 结果会报
	// `cannot create .../cgroup.procs: Directory nonexistent`.
	if l.Argv[0] != DefaultPaths.Sh {
		t.Fatalf("最外层必须是加入 cgroup 那一层, got %v", l.Argv[0])
	}
	iSh := indexOf(l.Argv, DefaultPaths.Sh)
	iUn := indexOf(l.Argv, DefaultPaths.Unshare)
	iLl := indexOf(l.Argv, DefaultPaths.LandlockRun)
	iSep := indexOf(l.Argv, "--")
	// cgroup → 隔离 → landlock → 程序.
	// cgroup 必须在 landlock 之前(之后写不了 /sys), 也必须在隔离之前(见上).
	if !(iSh == 0 && iUn > iSh && iLl > iUn && iSep > iLl) {
		t.Fatalf("分层顺序不对: sh=%d unshare=%d landlock=%d sep=%d",
			iSh, iUn, iLl, iSep)
	}
	if !reflect.DeepEqual(l.Argv[iSep+1:], []string{"/usr/bin/node", "app.js"}) {
		t.Fatalf("用户程序必须在最里层: %v", l.Argv[iSep+1:])
	}
	if !strings.Contains(l.Argv[iSh+2], "cgroup.procs") ||
		!strings.Contains(l.Argv[iSh+2], `exec "$@"`) {
		t.Fatalf("sh 那层必须是 自己加入 cgroup 再 exec: %q", l.Argv[iSh+2])
	}
	if strings.Contains(strings.Join(l.Argv, " "), "cgexec") {
		t.Fatal("不该用 cgexec —— 它是 cgroup v1 的工具, v2 下直接失败")
	}
}

// 执行底座必须在规则里 —— 否则进程连自己的可执行文件都读不到
func TestRuntimeBaseGranted(t *testing.T) {
	p := mustPlan(t, nil, V)
	l, _ := BuildLaunch(p, Body{Argv: []string{"/bin/true"}, Cwd: "/work"},
		LaunchContext{PID: "p1", CgroupSlice: "s", ABISocket: "/run/s.sock"}, DefaultPaths)
	for _, base := range V.RuntimeFS {
		i := indexOf(l.Argv, base)
		if i <= 0 || l.Argv[i-1] != "--ro" {
			t.Fatalf("底座缺 %s", base)
		}
	}
}

func TestEnvIsScrubbed(t *testing.T) {
	p := mustPlan(t, nil, V)
	l, _ := BuildLaunch(p,
		Body{Argv: []string{"/bin/true"}, Cwd: "/work", Env: map[string]string{"MY": "1", "NEOX_PID": "冒充"}},
		LaunchContext{PID: "p1", CgroupSlice: "s", ABISocket: "/run/s.sock"}, DefaultPaths)
	// 宿主环境一律不继承; 进程不能覆盖 OS 注入的 NEOX_*
	if l.Env["NEOX_PID"] != "p1" {
		t.Fatalf("进程覆盖了 OS 注入的 pid: %v", l.Env["NEOX_PID"])
	}
	if l.Env["MY"] != "1" {
		t.Fatal("自定义 env 应当保留")
	}
	// PATH HOME LANG NEOX_ABI_SOCKET NEOX_ABI_TOKEN NEOX_PID + 自定义的 MY
	if len(l.Env) != 7 {
		t.Fatalf("env 白名单应恰好 7 项, got %d: %v", len(l.Env), l.Env)
	}
}

// 只保护具体保留名, 不按前缀一刀切 ——
// 否则应用自己的 NEOX_XXX 会被静默剥掉.
func TestAppMayUseNeoxPrefixedEnv(t *testing.T) {
	p := mustPlan(t, nil, V)
	l, _ := BuildLaunch(p,
		Body{Argv: []string{"/bin/true"}, Cwd: "/work",
			Env: map[string]string{"NEOX_MODEL": "llm", "NEOX_TASK": "干活"}},
		LaunchContext{PID: "p1", CgroupSlice: "s", ABISocket: "/run/s.sock", ABIToken: "t"},
		DefaultPaths)
	if l.Env["NEOX_MODEL"] != "llm" || l.Env["NEOX_TASK"] != "干活" {
		t.Fatalf("应用自己的 NEOX_* 不该被剥掉: %v", l.Env)
	}
	if l.Env["NEOX_ABI_TOKEN"] != "t" || l.Env["NEOX_PID"] != "p1" {
		t.Fatal("保留名仍然必须由 OS 说了算")
	}
}

func TestEmptyArgvRejected(t *testing.T) {
	p := mustPlan(t, nil, V)
	if _, err := BuildLaunch(p, Body{Cwd: "/work"}, LaunchContext{}, DefaultPaths); err == nil {
		t.Fatal("空 argv 必须拒绝")
	}
}

// ── 探测 ────────────────────────────────────────────────────

func linuxDeps() ProbeDeps {
	return ProbeDeps{
		Platform:   "linux",
		FileExists: func(string) bool { return true },
		ReadFile: func(p string) string {
			if strings.Contains(p, "actions_avail") {
				return "kill_process errno user_notif trace log allow"
			}
			return "landlock,capability,bpf"
		},
		Paths: DefaultPaths,
	}
}

func TestProbeNonLinux(t *testing.T) {
	d := linuxDeps()
	d.Platform = "darwin"
	p := Probe([]abi.Requirement{abi.ReqFSEnforce}, d)
	if p.Usable || !strings.Contains(p.Reason, "只在 Linux") {
		t.Fatalf("非 Linux 必须不可用: %+v", p)
	}
}

// 只支持不接入不算数 —— 例如 linuxkit 可能无 landlock, 但有 bpf-lsm
func TestProbeDetectedNotWired(t *testing.T) {
	d := ProbeDeps{
		Platform:   "linux",
		FileExists: func(p string) bool { return !strings.Contains(p, "landlock") },
		ReadFile: func(p string) string {
			if strings.Contains(p, "actions_avail") {
				return "errno user_notif allow"
			}
			return "capability,bpf"
		},
		Paths: DefaultPaths,
	}
	p := Probe([]abi.Requirement{abi.ReqFSEnforce, abi.ReqNetIsolate}, d)
	if p.Usable {
		t.Fatal("缺 fs-enforce 时不该 usable")
	}
	if !reflect.DeepEqual(p.Missing, []abi.Requirement{abi.ReqFSEnforce}) {
		t.Fatalf("missing = %v", p.Missing)
	}
	if !contains(p.DetectedNotWired, abi.MechBPFLSM) || !strings.Contains(p.Reason, "尚未接入") {
		t.Fatalf("探到但没接入的必须进诊断: %+v", p)
	}
}

// 内核有 landlock 但启动器二进制不在 → 不算数
func TestProbeMissingBinary(t *testing.T) {
	d := linuxDeps()
	d.FileExists = func(p string) bool { return p != DefaultPaths.LandlockRun }
	if Probe([]abi.Requirement{abi.ReqFSEnforce}, d).Usable {
		t.Fatal("启动器不在也算 usable = fail-open")
	}
}

// ── 跨语言对拍: Go 生成的命令行必须跟 TS 逐字节一致 ─────────

type tsFixture struct {
	RuntimeFS []string      `json:"runtimeFs"`
	Paths     tsPaths       `json:"paths"`
	Cases     []tsCase      `json:"cases"`
	Probes    []string      `json:"probes"`
	Hosts     []string      `json:"hosts"`
	Allows    []tsAllowCase `json:"allows"`
}
type tsPaths struct {
	Unshare, Sh, LandlockRun, CgroupRoot, IP string
}
type tsCase struct {
	Name string            `json:"name"`
	Caps []Capability      `json:"caps"`
	Plan json.RawMessage   `json:"plan"`
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
	// NetNS/ProxyAddr 有出网授权的用例才有 —— 没有出网就该是完全断网那条路
	NetNS     string `json:"netNS"`
	ProxyAddr string `json:"proxyAddr"`
}
type tsAllowCase struct {
	Name  string `json:"name"`
	Read  []bool `json:"read"`
	Write []bool `json:"write"`
	Net   []bool `json:"net"`
}

// 样本是**契约的显式声明**, 手工维护.
//
// 改了 plan 的契约(比如给 cgroup 加一个缺省), 两边都要改, 样本也要跟着改 ——
// 这次就是这么红的, 而且红得对: 它正是这个测试存在的理由.
//
// 试过写一个从 TS 实时生成样本的脚本, 放弃了: 生成出来的东西依赖
// **跑生成器那台机器**有没有 /lib64、路径怎么解析, 于是样本从
// 契约的声明变成了环境的函数 —— 比手工维护更脆.
func loadTS(t *testing.T) tsFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/ts_plans.json")
	if err != nil {
		t.Fatalf("读样本失败: %v", err)
	}
	var f tsFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParityArgvAndEnv(t *testing.T) {
	f := loadTS(t)
	paths := LauncherPaths{Unshare: f.Paths.Unshare, Sh: f.Paths.Sh,
		LandlockRun: f.Paths.LandlockRun, CgroupRoot: f.Paths.CgroupRoot,
		IP: f.Paths.IP}

	for _, c := range f.Cases {
		opts := PlanOptions{VolumeRoot: "/work", RuntimeFS: f.RuntimeFS}
		// TS 样本里"带内存限额"那组设了 256MB
		var tsPlan struct {
			Cgroup struct {
				MemoryMaxBytes *int64 `json:"memoryMaxBytes"`
			} `json:"cgroup"`
		}
		_ = json.Unmarshal(c.Plan, &tsPlan)
		opts.MemoryMaxBytes = tsPlan.Cgroup.MemoryMaxBytes

		p, err := PlanFor(c.Caps, opts)
		if err != nil {
			t.Fatalf("[%s] PlanFor: %v", c.Name, err)
		}
		l, err := BuildLaunch(p,
			Body{Argv: []string{"/usr/bin/node", "app.js"}, Cwd: "/work",
				Env: map[string]string{"MY": "1", "NEOX_PID": "冒充"}},
			LaunchContext{PID: "p1", CgroupSlice: "neox-os/p1", ABISocket: "/run/neox-os/p1.sock",
				NetNS: c.NetNS, ProxyAddr: c.ProxyAddr},
			paths)
		if err != nil {
			t.Fatalf("[%s] BuildLaunch: %v", c.Name, err)
		}
		if !reflect.DeepEqual(l.Argv, c.Argv) {
			t.Fatalf("[%s] 命令行不一致\nGo: %v\nTS: %v", c.Name, l.Argv, c.Argv)
		}
		if !reflect.DeepEqual(l.Env, c.Env) {
			t.Fatalf("[%s] env 不一致\nGo: %v\nTS: %v", c.Name, l.Env, c.Env)
		}
	}
}

func TestParityPlanJSON(t *testing.T) {
	f := loadTS(t)
	for _, c := range f.Cases {
		var tsPlan struct {
			Cgroup struct {
				MemoryMaxBytes *int64 `json:"memoryMaxBytes"`
			} `json:"cgroup"`
		}
		_ = json.Unmarshal(c.Plan, &tsPlan)
		p, _ := PlanFor(c.Caps, PlanOptions{VolumeRoot: "/work", RuntimeFS: f.RuntimeFS,
			MemoryMaxBytes: tsPlan.Cgroup.MemoryMaxBytes})
		goJSON, _ := json.Marshal(p)
		if !jsonSemanticEqual(goJSON, c.Plan) {
			t.Fatalf("[%s] plan JSON 不一致\nGo: %s\nTS: %s", c.Name, goJSON, c.Plan)
		}
	}
}

// 预检答案也必须逐条一致 —— 这是"双真相源"的防线
func TestParityCapabilityAllows(t *testing.T) {
	f := loadTS(t)
	for i, a := range f.Allows {
		caps := f.Cases[i].Caps
		for j, p := range f.Probes {
			if got := CapabilityAllows(caps, "read", p); got != a.Read[j] {
				t.Fatalf("[%s] read %s: go=%v ts=%v", a.Name, p, got, a.Read[j])
			}
			if got := CapabilityAllows(caps, "write", p); got != a.Write[j] {
				t.Fatalf("[%s] write %s: go=%v ts=%v", a.Name, p, got, a.Write[j])
			}
		}
		for j, h := range f.Hosts {
			if got := CapabilityAllows(caps, "net", h); got != a.Net[j] {
				t.Fatalf("[%s] net %s: go=%v ts=%v", a.Name, h, got, a.Net[j])
			}
		}
	}
}

// ── 工具 ────────────────────────────────────────────────────

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

func contains[T comparable](ss []T, s T) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func jsonSemanticEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return string(ax) == string(by)
}

// 已经存在的**文件**不能被当成"建不出来的目录"摘掉.
//
// 授权的目标可能是一个文件: 用户批准"允许写 bench/src/config.js"
// 授的就是那个文件. 对已存在的文件调 mkdirAll 必然失败,
// 于是规则被摘掉 —— **用户的批准静默失效**.
// 授权会被记上、并进入 plan、标签也正确, 但最后会死在这一行.
func TestPrepareVolumeKeepsExistingFile(t *testing.T) {
	plan := &Plan{FS: []abi.FSRule{
		{Path: "/vol/config.js", Access: []string{"read", "write"}},
	}}
	dropped := PrepareVolume(plan,
		func(p string) error { return fmt.Errorf("mkdir %s: 不是目录", p) },
		func(p string) bool { return p == "/vol/config.js" })

	if len(dropped) != 0 {
		t.Fatalf("已存在的文件被摘掉了: %v", dropped)
	}
	if len(plan.FS) != 1 {
		t.Fatalf("规则没保住: %+v", plan.FS)
	}
}

// 不存在的可写目录仍然要建出来 —— landlock 装规则要求路径存在
func TestPrepareVolumeStillCreatesMissingDirs(t *testing.T) {
	made := ""
	plan := &Plan{FS: []abi.FSRule{
		{Path: "/vol/newdir", Access: []string{"read", "write"}},
	}}
	dropped := PrepareVolume(plan,
		func(p string) error { made = p; return nil },
		func(p string) bool { return false })
	if made != "/vol/newdir" {
		t.Fatalf("没去建缺失的目录: made=%q", made)
	}
	if len(dropped) != 0 {
		t.Fatalf("建成功了却报摘掉: %v", dropped)
	}
}

// 真建不出来的还是要摘掉并报告 —— 不许静默
func TestPrepareVolumeStillDropsUncreatable(t *testing.T) {
	plan := &Plan{FS: []abi.FSRule{
		{Path: "/vol/nope", Access: []string{"read", "write"}},
	}}
	dropped := PrepareVolume(plan,
		func(p string) error { return fmt.Errorf("只读文件系统") },
		func(p string) bool { return false })
	if len(dropped) != 1 {
		t.Fatalf("建不出来却没报: %v", dropped)
	}
}

// 有出网授权时: 进 OS 预先配好的具名 ns, 而且**不能再 --net**
// (那会换成一个新的空 ns, 网又没了). cgroup 仍然在最外面.
func TestNetGrantEntersNamedNamespace(t *testing.T) {
	p := mustPlan(t, []Capability{{Axis: abi.AxisNet, Scope: "registry.npmjs.org"}}, V)
	l, err := BuildLaunch(p, Body{Argv: []string{"/bin/true"}, Cwd: "/work"},
		LaunchContext{PID: "p1", CgroupSlice: "s", ABISocket: "/run/s.sock",
			NetNS: "neox-p1", ProxyAddr: "10.77.0.1:3128"}, DefaultPaths)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	if !strings.Contains(joined, "netns exec neox-p1") {
		t.Fatalf("没进具名 ns: %v", l.Argv)
	}
	if strings.Contains(joined, "--net") {
		t.Fatal("进了具名 ns 还带 --net —— 那会换成一个新的空 ns, 网又没了")
	}
	if l.Argv[0] != DefaultPaths.Sh {
		t.Fatalf("cgroup 那层仍然必须在最外面 (ip netns exec 会重挂 /sys): %v", l.Argv[0])
	}
	if indexOf(l.Argv, DefaultPaths.IP) < indexOf(l.Argv, DefaultPaths.Unshare) == false {
		t.Fatal("ip netns exec 必须在 unshare 之外")
	}
	// 代理地址要进环境, 大小写两套都得有
	for _, k := range []string{"http_proxy", "HTTPS_PROXY", "npm_config_proxy"} {
		if l.Env[k] != "http://10.77.0.1:3128" {
			t.Errorf("%s 没设对: %q —— 少一套就有一半程序连不出去", k, l.Env[k])
		}
	}
}

// 授了出网却没配网 —— **拒绝启动**, 不是警告.
//
// 这是真实存在过的洞: plan.Net 有值而 launcher 从没读过它,
// 于是授权静默失效, 用户以为给了、agent 以为能连, 谁都不知道.
func TestNetGrantWithoutNamespaceRefusesToStart(t *testing.T) {
	p := mustPlan(t, []Capability{{Axis: abi.AxisNet, Scope: "example.com"}}, V)
	_, err := BuildLaunch(p, Body{Argv: []string{"/bin/true"}, Cwd: "/work"},
		LaunchContext{PID: "p1", CgroupSlice: "s", ABISocket: "/run/s.sock"}, DefaultPaths)
	if err == nil {
		t.Fatal("授了出网却没网络命名空间, 必须拒绝启动 —— 静默失效比拒绝启动糟得多")
	}
}

// 没有出网授权时保持完全隔离 —— 缺省必须是安全的那个
func TestNoNetGrantStaysFullyIsolated(t *testing.T) {
	p := mustPlan(t, nil, V)
	l, _ := BuildLaunch(p, Body{Argv: []string{"/bin/true"}, Cwd: "/work"},
		LaunchContext{PID: "p1", CgroupSlice: "s", ABISocket: "/run/s.sock"}, DefaultPaths)
	joined := strings.Join(l.Argv, " ")
	if !strings.Contains(joined, "--net") {
		t.Fatal("没有出网授权就必须 --net 完全隔离")
	}
	if strings.Contains(joined, "netns exec") {
		t.Fatal("没授权还去进具名 ns")
	}
	if l.Env["http_proxy"] != "" {
		t.Fatal("没有出网授权不该设代理变量")
	}
}

// 断网的命名空间里也得有回环.
//
// `unshare --net` 建出来的匿名 ns 里 lo 是**存在但 DOWN 的** —— 内核默认如此.
// 于是 127.0.0.1 谁也连不上, "起个服务再打它验证"这件事整个废掉,
// 而那正是 agent 验证自己工作的主要手段.
//
// 典型表现是第一次连接失败, 进程自行诊断出 lo DOWN 再 `ip link set lo up`,
// 还在交付说明里管这叫"沙箱特性" —— 一件本该 OS 保证的事变成了模型的偏方.
//
// 修法是把两条路合成一条: 断网也进**具名** ns(里面只有 lo), 于是
// 启动命令行的形状跟有授权时完全一样 —— 参数仍然逐个可见, 可审计.
func TestNamedNamespaceReplacesAnonymousNet(t *testing.T) {
	p := mustPlan(t, []Capability{{Axis: abi.AxisWrite, Scope: "/out"}}, V)
	l, err := BuildLaunch(p, Body{Argv: []string{"/bin/true"}, Cwd: "/work"},
		LaunchContext{PID: "p1", CgroupSlice: "neox-os/p1",
			ABISocket: "/run/neox-os/p1.sock", NetNS: "neox-p1"}, DefaultPaths)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	// 进了具名 ns 就**不能再 --net**: 那会把刚进来的 ns 换成一个新的空 ns,
	// lo 又是 down 的, 等于白建
	if strings.Contains(joined, "--net") {
		t.Fatalf("已经进了具名 ns, 再 --net 会换成新的空 ns: %v", l.Argv)
	}
	if !strings.Contains(joined, "netns exec neox-p1") {
		t.Fatalf("没进具名 ns: %v", l.Argv)
	}
	// 命令行仍然是扁平的 —— 参数逐个可见才谈得上审计
	if i := indexOf(l.Argv, "--ro"); i < 0 || l.Argv[i+1] == "" {
		t.Fatalf("参数不再逐个可见, 没法审计: %v", l.Argv)
	}
}

// 新的 pid 命名空间必须配一个新的 /proc.
//
// 只 `--pid` 不 `--mount-proc` 的话, procfs 还是宿主那个: ps 报的是宿主 pid,
// kill 发的是命名空间 pid, 两者对不上 —— 症状是"看得见、杀不掉",
// 而且报的错是误导性的 "No such process".
//
// 典型表现是第一轮起了个 HTTP 服务, 第二轮修改行为时要重启,
// 端口被旧进程占着却怎么都杀不掉, 最后只能换端口, 并在交付说明里写
// "旧进程我没法清掉, 你那边方便的话手动 kill". 一个连自己起的进程都停不掉的
// OS 是不成立的.
func TestPidNamespaceGetsItsOwnProc(t *testing.T) {
	p := mustPlan(t, nil, V)
	l, err := BuildLaunch(p, Body{Argv: []string{"/bin/true"}, Cwd: "/work"},
		LaunchContext{PID: "p1", CgroupSlice: "s", ABISocket: "/run/s.sock"}, DefaultPaths)
	if err != nil {
		t.Fatal(err)
	}
	iPid := indexOf(l.Argv, "--pid")
	iProc := indexOf(l.Argv, "--mount-proc")
	if iPid < 0 {
		t.Fatalf("没建 pid 命名空间: %v", l.Argv)
	}
	if iProc < 0 {
		t.Fatalf("建了 pid 命名空间却没给它配 /proc —— ps 看到的 pid 杀不掉: %v", l.Argv)
	}
	// --mount-proc 要挂在同一条 unshare 上, 而且 --mount 是它的前提
	if indexOf(l.Argv, "--mount") < 0 {
		t.Fatalf("--mount-proc 需要挂载命名空间: %v", l.Argv)
	}
	if iProc < iPid {
		t.Fatalf("--mount-proc 要跟在 --pid 之后: %v", l.Argv)
	}
	// --fork 是让 agent 成为新命名空间里 **PID 1** 的那一步.
	//
	// 这不只是个细节: PID 1 一退出, 内核把命名空间里剩下的进程一起收掉 ——
	// 也就是 **agent 起的后台进程活不过这段对话**. 这是必要的隔离
	// (后台进程逃不出笼子), 而且提示词里明确声明后台进程不会一直运行
	// (见 prompt.go 与
	// TestPromptStatesBackgroundJobsDieWithTheConversation).
	//
	// 少了 --fork 这条事实就不成立, 而提示词还在那么说 —— 那就成了假话.
	if indexOf(l.Argv, "--fork") < 0 {
		t.Fatalf("少了 --fork, agent 就不是命名空间里的 PID 1 —— "+
			"提示词里那条「后台进程活不过这段对话」会变成假话: %v", l.Argv)
	}
}

// **本机回环必须绕开代理.**
//
// 典型表现是交付的 HTTP 服务在测试时 7 个用例全红,
// 报 JSONDecodeError —— 因为请求 127.0.0.1 被 http_proxy 截走,
// 代理回了一个没有 body 的 502.
//
// 这条**只在拿到出网授权之后才发作**: 没授权时根本没有代理变量, 直连正常;
// 批过一次之后 http_proxy 全局生效, 连自己起的服务都发给代理.
// 而"起个服务再 curl 自己"正是提示词里鼓励的验证方式 ——
// 环境把这条路堵上, 等于我们一边要求它验证一边让验证必然失败.
func TestLoopbackBypassesProxy(t *testing.T) {
	env := buildEnv(nil, LaunchContext{ProxyAddr: "10.77.0.1:3128"})
	for _, k := range []string{"no_proxy", "NO_PROXY"} {
		if !strings.Contains(env[k], "127.0.0.1") {
			t.Fatalf("%s = %q, 回环没有绕开代理 —— agent 一验证自己起的服务就会撞 502",
				k, env[k])
		}
	}
	// 反过来: 别的主机**必须**还走代理. 绕过代理就是绕过按主机名的授权
	for _, k := range []string{"http_proxy", "https_proxy"} {
		if env[k] == "" {
			t.Fatalf("%s 空了 —— 出网不走代理就等于绕过了授权", k)
		}
	}
	if strings.Contains(env["no_proxy"], "*") {
		t.Fatal("no_proxy 里有通配 —— 那会让出网整个绕开代理和授权")
	}
}

// 没有出网授权时不该冒出代理变量: 那会让程序把"没有网"报成"代理连不上"
func TestNoGrantNoProxyVars(t *testing.T) {
	env := buildEnv(nil, LaunchContext{})
	for _, k := range []string{"http_proxy", "HTTP_PROXY", "no_proxy", "NO_PROXY"} {
		if _, ok := env[k]; ok {
			t.Fatalf("没有出网授权却设了 %s —— 报错会指向代理, 而真实原因是没授权", k)
		}
	}
}
