package osinit

import (
	"context"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

var readIn = []abi.Capability{{Axis: abi.AxisRead, Scope: "/in"}}

// ── 策略层 ──────────────────────────────────────────────────

func TestDefaultIsGuarded(t *testing.T) {
	// 缺省必须是安全的那个 —— 不是全自主
	if NewPolicyStore(nil).AutonomyOf("任意应用") != AutonomyGuarded {
		t.Fatal("缺省自主级别必须是 guarded")
	}
}

func TestEvaluateMatrix(t *testing.T) {
	cases := []struct {
		name  string
		level Autonomy
		axis  string
		scope string
		want  Verdict
	}{
		// 能力集内: 除 paranoid 外都直接放行 —— 那是进程本来就有的权限
		{"全自主 · 集内", AutonomyFull, "read", "/in/a.txt", VerdictAllow},
		{"有人兜底 · 集内", AutonomyGuarded, "read", "/in/a.txt", VerdictAllow},
		{"每步都问 · 集内", AutonomyParanoid, "read", "/in/a.txt", VerdictAsk},

		// 能力集外: 全自主直接拒 (不打扰人), 其余升级
		{"全自主 · 集外", AutonomyFull, "read", "/secret/k", VerdictDeny},
		{"有人兜底 · 集外", AutonomyGuarded, "read", "/secret/k", VerdictAsk},
		{"每步都问 · 集外", AutonomyParanoid, "read", "/secret/k", VerdictAsk},

		// 轴不同也算集外
		{"有人兜底 · 写没授", AutonomyGuarded, "write", "/in/a.txt", VerdictAsk},
	}
	for _, c := range cases {
		p := NewPolicyStore(nil)
		p.SetAutonomy("demo", c.level)
		if got := p.Evaluate("demo", c.axis, c.scope, readIn); got != c.want {
			t.Fatalf("[%s] got %v want %v", c.name, got, c.want)
		}
	}
}

// 全自主不代表放开边界 —— 越界照样拒, 只是不打扰人
func TestFullAutonomyStillBounded(t *testing.T) {
	p := NewPolicyStore(nil)
	p.SetAutonomy("demo", AutonomyFull)
	if p.Evaluate("demo", "read", "/etc/shadow", readIn) != VerdictDeny {
		t.Fatal("全自主也不能越过能力集")
	}
}

// 记住之后不再打扰 —— 打扰频率单调下降
func TestRememberStopsAsking(t *testing.T) {
	p := NewPolicyStore(func() int64 { return 12345 })
	if p.Evaluate("demo", "read", "/data/x.csv", readIn) != VerdictAsk {
		t.Fatal("第一次应当问")
	}
	p.Remember(Grant{App: "demo", Axis: "read", Scope: "/data", By: "phone:刘"})
	if p.Evaluate("demo", "read", "/data/x.csv", readIn) != VerdictAllow {
		t.Fatal("记住之后不该再问")
	}
	// 目录授权覆盖子路径, 但不越界到别处
	if p.Evaluate("demo", "read", "/data/深/嵌套.txt", readIn) != VerdictAllow {
		t.Fatal("目录授权应覆盖子路径")
	}
	if p.Evaluate("demo", "read", "/datax/y", readIn) != VerdictAsk {
		t.Fatal("/data 不该覆盖 /datax —— 按路径段比, 不按前缀比")
	}
}

// 授权按应用隔离 —— 一次批准不该让所有应用都能干这事
func TestGrantIsScopedToApp(t *testing.T) {
	p := NewPolicyStore(nil)
	p.Remember(Grant{App: "财务", Axis: "read", Scope: "/data", By: "phone"})
	if p.Evaluate("财务", "read", "/data/x", readIn) != VerdictAllow {
		t.Fatal("本应用应当放行")
	}
	if p.Evaluate("相册", "read", "/data/x", readIn) != VerdictAsk {
		t.Fatal("**别的应用不该沾光** —— 授权必须按应用隔离")
	}
}

// 学到的东西必须可撤销, 否则就是不可逆的权限膨胀
func TestForget(t *testing.T) {
	p := NewPolicyStore(nil)
	p.Remember(Grant{App: "demo", Axis: "read", Scope: "/data", By: "phone"})
	if !p.Forget("demo", "read", "/data") {
		t.Fatal("撤销应当成功")
	}
	if p.Evaluate("demo", "read", "/data/x", readIn) != VerdictAsk {
		t.Fatal("撤销后应当重新问")
	}
}

// 授权必须能追到人 —— 三个月后要回答"这条谁批的"
func TestGrantIsAuditable(t *testing.T) {
	p := NewPolicyStore(func() int64 { return 999 })
	p.Remember(Grant{App: "demo", Axis: "read", Scope: "/data", By: "phone:刘"})
	gs := p.Grants()
	if len(gs) != 1 || gs[0].By != "phone:刘" || gs[0].At != 999 {
		t.Fatalf("授权缺审计字段: %+v", gs)
	}
}

// write 蕴含 read —— 必须跟能力集的蕴含关系一致
func TestGrantWriteImpliesRead(t *testing.T) {
	p := NewPolicyStore(nil)
	p.Remember(Grant{App: "demo", Axis: "write", Scope: "/out", By: "phone"})
	if p.Evaluate("demo", "read", "/out/a", readIn) != VerdictAllow {
		t.Fatal("批了写就应当能读, 否则 agent 会放弃它本来有权做的事")
	}
}

// ── Broker: 接到"问人"那条链上 ──────────────────────────────

func brokerHarness(t *testing.T, level Autonomy) (*OS, *Broker, abi.ProcessID) {
	t.Helper()
	o := devOS()
	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "demo", Caps: readIn},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return "done", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	p := NewPolicyStore(func() int64 { return 1 })
	p.SetAutonomy("demo", level)
	t.Cleanup(func() { o.Shutdown("t") })
	return o, NewBroker(o, p), pid
}

// 自动裁决走微秒级路径, 且必须进审计 ——
// 否则"它昨晚到底碰了什么"答不上来
func TestBrokerAutoVerdictIsAudited(t *testing.T) {
	o, b, pid := brokerHarness(t, AutonomyFull)

	if v := b.Decide(SyscallRequest{PID: pid, Call: "openat", Axis: "read", Path: "/in/a"}); v != VerdictAllow {
		t.Fatalf("集内应放行: %v", v)
	}
	if v := b.Decide(SyscallRequest{PID: pid, Call: "openat", Axis: "read", Path: "/secret/k"}); v != VerdictDeny {
		t.Fatalf("集外应拒绝: %v", v)
	}

	var used, denied bool
	for _, e := range o.Log().Replay(pid, 0) {
		m, _ := e.Payload.(map[string]any)
		if m == nil || m["by"] != "policy" {
			continue
		}
		if e.Kind == abi.EvCapUsed {
			used = true
		}
		if e.Kind == abi.EvCapDenied {
			denied = true
		}
	}
	if !used || !denied {
		t.Fatal("自动裁决必须留审计")
	}
}

// 升级给人: 进程冻着等, 人在别的设备上点一下才继续
func TestBrokerEscalatesToHuman(t *testing.T) {
	o, b, pid := brokerHarness(t, AutonomyGuarded)

	got := make(chan Verdict, 1)
	go func() {
		got <- b.Decide(SyscallRequest{PID: pid, Call: "openat", Axis: "read", Path: "/data/x"})
	}()

	// 等待决策被登记 —— 此刻进程在内核里冻着
	deadline := time.Now().Add(2 * time.Second)
	for len(o.Decisions().Pending()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	pending := o.Decisions().Pending()
	if len(pending) != 1 {
		t.Fatal("应当登记一条待决策")
	}
	// 呈现给人的必须是语义化的选项, 而不是一句技术黑话
	pres := pending[0].Request.Present
	if pres.Kind != "choice" || len(pres.Options) != 3 {
		t.Fatalf("呈现不对: %+v", pres)
	}

	select {
	case <-got:
		t.Fatal("没人回答之前不该有结论")
	case <-time.After(30 * time.Millisecond):
	}

	o.Decisions().Resolve(pending[0].DID, "once", "phone:刘", nil)
	select {
	case v := <-got:
		if v != VerdictAllow {
			t.Fatalf("人批了就该放行: %v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("人回答后应当立刻有结论")
	}
}

// 选"以后不用问" → 记住 → 第二次不再打扰
func TestBrokerAlwaysStopsAsking(t *testing.T) {
	o, b, pid := brokerHarness(t, AutonomyGuarded)

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p := o.Decisions().Pending(); len(p) > 0 {
				o.Decisions().Resolve(p[0].DID, "always", "phone:刘", nil)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	req := SyscallRequest{PID: pid, Call: "openat", Axis: "read", Path: "/data/x"}
	if v := b.Decide(req); v != VerdictAllow {
		t.Fatalf("第一次: %v", v)
	}

	// 第二次不该再产生待决策
	done := make(chan Verdict, 1)
	go func() { done <- b.Decide(req) }()
	select {
	case v := <-done:
		if v != VerdictAllow {
			t.Fatalf("第二次应当直接放行: %v", v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("第二次还在问 —— 没记住")
	}
	if len(o.Decisions().Pending()) != 0 {
		t.Fatal("第二次不该再打扰人")
	}
}

// 没人回答 → 拒绝. **不许把沉默当同意**
func TestBrokerTimeoutDenies(t *testing.T) {
	o, b, pid := brokerHarness(t, AutonomyGuarded)
	b.AskTimeoutMs = 30

	if v := b.Decide(SyscallRequest{PID: pid, Call: "openat", Axis: "read", Path: "/data/x"}); v != VerdictDeny {
		t.Fatalf("超时必须拒绝, got %v", v)
	}
	// 而且日志里要看得见是超时替你做的
	found := false
	for _, e := range o.Log().Replay(pid, 0) {
		if e.Kind == abi.EvDecideResolved {
			if m, _ := e.Payload.(map[string]any); m != nil && m["by"] == "timeout" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("超时兜底必须在日志里标明 by=timeout")
	}
}

// 进程没了 → 拒绝, 不能挂着
func TestBrokerDeniesForDeadProcess(t *testing.T) {
	_, b, _ := brokerHarness(t, AutonomyGuarded)
	if b.Decide(SyscallRequest{PID: "不存在", Axis: "read", Path: "/x"}) != VerdictDeny {
		t.Fatal("进程不存在必须拒绝")
	}
}
