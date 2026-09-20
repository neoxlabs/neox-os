package osinit

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 测试绝不真的 spawn 系统二进制 —— 命令行正确性由 confine 的纯函数测试锁
func fakeSpawner(argv []string, cwd string, env map[string]string) (Child, error) {
	return &fakeChild{}, nil
}

type fakeChild struct{}

func (fakeChild) OnLine(func(string))        {}
func (fakeChild) Wait() (int, string, error) { return 0, "", nil }
func (fakeChild) Kill(string)                {}

func okProbe([]abi.Requirement) abi.EnforcementProbe {
	return abi.EnforcementProbe{Platform: "linux", Usable: true,
		Mechanisms: []abi.Mechanism{abi.MechLandlock, abi.MechCgroup2, abi.MechNetns}}
}
func badProbe([]abi.Requirement) abi.EnforcementProbe {
	return abi.EnforcementProbe{Platform: "linux", Usable: false,
		Missing:          []abi.Requirement{abi.ReqFSEnforce},
		DetectedNotWired: []abi.Mechanism{abi.MechBPFLSM},
		Reason:           "无法满足: fs-enforce (内核有 bpf-lsm 但我们尚未接入, 不能算数)"}
}

func devOS(opts ...func(*Options)) *OS {
	o := Options{Mode: abi.ModeDev, Spawner: fakeSpawner, RuntimeFS: []string{"/usr"}}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

func waitState(t *testing.T, o *OS, pid abi.ProcessID, want abi.ProcessState) {
	t.Helper()
	// 死线放宽到 10 秒.
	//
	// 断言的是"**最终**会到达", 不是"两秒内到达" —— 正常情况下 1ms 就到了,
	// 放宽不会让任何一次通过变慢, 只影响真失败时要等多久.
	//
	// 原来 2 秒: 单跑 osinit 包稳过, 但 go test ./... 所有包并行时
	// 机器负载一上来就偶发假红. **随机变红的测试比没有测试更糟** ——
	// 人会开始忽略红色.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := o.Info(pid); ok && info.State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	info, _ := o.Info(pid)
	t.Fatalf("等 %s 超时, 当前 %s", want, info.State)
}

// 无人在场时进程仍须运行并得到确定的决策结果, 不能因没有 UI 或订阅者而卡住.

func TestM0_NoAudienceNeeded(t *testing.T) {
	o := devOS()
	defer o.Shutdown("test")

	// 1. 起进程. 注意: 没有任何订阅者, 没有任何 UI 连着
	pid, err := o.Spawn(abi.ProcessSpec{
		App: "demo", Name: "夜间调研",
		Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/work"}},
	}, InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
		pc.Emit(map[string]any{"step": "started"})
		if !pc.Can("read", "/work/notes.md") {
			t.Error("授了的应当能过")
		}
		// 没授的不能 —— 而且无人在场时给的是确定答案, 不是卡住
		if pc.Can("write", "/work/notes.md") {
			t.Error("没授的不该过")
		}
		res, err := pc.Decide(abi.DecisionRequest{
			Urgency: "high",
			Present: abi.PresentSpec{Kind: "choice", Title: "选方向",
				Options: []abi.PresentOption{{ID: "deep", Label: "深挖"}, {ID: "wide", Label: "铺开"}}},
		})
		if err != nil {
			return nil, err
		}
		pc.Emit(map[string]any{"step": "resumed", "chose": res.Choice})
		return map[string]any{"direction": res.Choice}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	// 2. 进程自己跑到了"等人"这一步, 全程没有观众
	waitState(t, o, pid, abi.StateWaiting)
	pending := o.Decisions().Pending()
	if len(pending) != 1 || pending[0].PID != pid {
		t.Fatalf("待决策: %+v", pending)
	}

	// 3. 现在才有客户端接入 —— 必须看到之前发生的一切
	var mu sync.Mutex
	var seen []abi.EventKind
	stop := o.Log().Subscribe(pid, 0, func(e abi.Event) {
		mu.Lock()
		seen = append(seen, e.Kind)
		mu.Unlock()
	})
	want := []abi.EventKind{
		abi.EvProcState, abi.EvProcState, abi.EvProcOutput,
		abi.EvCapUsed, abi.EvCapDenied, abi.EvDecideRequest, abi.EvProcState,
	}
	waitKinds(t, &mu, &seen, len(want))
	mu.Lock()
	for i, w := range want {
		if seen[i] != w {
			mu.Unlock()
			t.Fatalf("迟到的订阅者补齐不完整: %v", seen)
		}
	}
	mu.Unlock()

	// 4. 决策由"手机"解决 —— 跟起进程的不是同一个客户端
	o.Decisions().Resolve(pending[0].DID, "wide", "phone:刘", nil)
	waitState(t, o, pid, abi.StateExited)
	stop()

	info, _ := o.Info(pid)
	if !info.Outcome.OK {
		t.Fatalf("应当正常结束: %+v", info.Outcome)
	}
	v := info.Outcome.Value.(map[string]any)
	if v["direction"] != "wide" {
		t.Fatalf("结果不对: %+v", v)
	}

	// 5. 事后任意时刻接入, 从日志完整重建 —— seq 连续无洞
	full := o.Log().Replay(pid, 0)
	for i, e := range full {
		if e.Seq != i {
			t.Fatalf("seq 有洞: %d != %d", e.Seq, i)
		}
	}
}

// 超时兜底不静默 —— 日志里必须留下"这是超时替你做的"
func TestM0_TimeoutIsLogged(t *testing.T) {
	o := devOS()
	defer o.Shutdown("test")
	pid, _ := o.Spawn(abi.ProcessSpec{App: "demo"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			return pc.Decide(abi.DecisionRequest{
				Present:   abi.PresentSpec{Kind: "approve", Title: "要继续吗"},
				OnTimeout: &abi.DecisionTimeout{AfterMs: 20, Choose: "no"},
			})
		}})
	waitState(t, o, pid, abi.StateExited)

	found := false
	for _, e := range o.Log().Replay(pid, 0) {
		if e.Kind == abi.EvDecideResolved {
			m := e.Payload.(map[string]any)
			if m["by"] != "timeout" || m["choice"] != "no" {
				t.Fatalf("超时兜底必须标明: %+v", m)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("没有 decide.resolved 事件")
	}
}

// 止损线由 OS 强制, 进程躲不掉
func TestM0_BudgetIsEnforced(t *testing.T) {
	o := devOS()
	defer o.Shutdown("test")
	limit := int64(100)
	pid, _ := o.Spawn(
		abi.ProcessSpec{App: "demo", Budget: &abi.Budget{Tokens: &limit}},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			n := int64(60)
			if err := pc.Spend(abi.BudgetDelta{Tokens: &n}); err != nil {
				return nil, err
			}
			if err := pc.Spend(abi.BudgetDelta{Tokens: &n}); err != nil {
				return nil, err // 超了
			}
			return "never", nil
		}})
	waitState(t, o, pid, abi.StateFailed)
	info, _ := o.Info(pid)
	if info.Outcome.Error.Code != "budget_exceeded" {
		t.Fatalf("%+v", info.Outcome.Error)
	}
}

// 等人期间不计活跃时长 —— 等一天和等一秒成本相同
func TestM0_WaitingIsFree(t *testing.T) {
	var clockVal int64 = 1_000_000
	o := devOS(func(op *Options) { op.Now = func() int64 { return clockVal } })
	defer o.Shutdown("test")

	limit := int64(5000)
	pid, _ := o.Spawn(
		abi.ProcessSpec{App: "demo", Budget: &abi.Budget{ActiveMs: &limit}},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			if _, err := pc.Decide(abi.DecisionRequest{
				Present: abi.PresentSpec{Kind: "approve", Title: "ok?"}}); err != nil {
				return nil, err
			}
			clockVal += 10
			n := int64(1)
			if err := pc.Spend(abi.BudgetDelta{Tokens: &n}); err != nil {
				return nil, err
			}
			return "done", nil
		}})

	waitState(t, o, pid, abi.StateWaiting)
	clockVal += 24 * 60 * 60 * 1000 // 等了一整天
	o.Decisions().Resolve(o.Decisions().Pending()[0].DID, "yes", "phone", nil)
	waitState(t, o, pid, abi.StateExited)

	info, _ := o.Info(pid)
	if !info.Outcome.OK {
		t.Fatalf("等一天不该超止损线: %+v", info.Outcome)
	}
}

// ── Fail-closed: 四种组合只有两种合法 ──────────────────────

func TestFailClosed_DefaultIsConfined(t *testing.T) {
	if New(Options{}).Mode() != abi.ModeConfined {
		t.Fatal("缺省必须是安全的那个")
	}
}

func TestFailClosed_Matrix(t *testing.T) {
	cases := []struct {
		name  string
		mode  abi.Mode
		body  Body
		probe func([]abi.Requirement) abi.EnforcementProbe
		want  error
	}{
		{"confined+inproc → 拒", abi.ModeConfined, InprocBody{Entry: nil}, okProbe, ErrModeMismatch},
		{"dev+exec → 拒", abi.ModeDev, ExecBody{Argv: []string{"/bin/true"}}, okProbe, ErrModeMismatch},
		{"confined+exec+内核拦不住 → 拒", abi.ModeConfined,
			ExecBody{Argv: []string{"/bin/true"}}, badProbe, ErrConfineUnusable},
	}
	for _, c := range cases {
		o := New(Options{Mode: c.mode, Probe: c.probe, Spawner: fakeSpawner, RuntimeFS: []string{"/usr"}})
		_, err := o.Spawn(abi.ProcessSpec{App: "x"}, c.body)
		if !errors.Is(err, c.want) {
			t.Fatalf("[%s] err = %v, want %v", c.name, err, c.want)
		}
		// 拒绝启动时不留下半个进程 —— 进程表必须干净
		if n := len(o.List()); n != 0 {
			t.Fatalf("[%s] 进程表残留 %d 条", c.name, n)
		}
		o.Shutdown("t")
	}
}

func TestFailClosed_ConfinedExecAudited(t *testing.T) {
	// socket 目录必须是本机存在且路径短的 —— /run/neox-os 在 macOS 上没有,
	// 而 sun_path 有 104 字节上限, 不能塞进深目录
	d, err0 := os.MkdirTemp("", "nx")
	if err0 != nil {
		t.Fatal(err0)
	}
	defer os.RemoveAll(d)
	o := New(Options{Mode: abi.ModeConfined, Probe: okProbe, Spawner: fakeSpawner,
		RuntimeFS: []string{"/usr", "/bin"}, ABISocketDir: d})
	defer o.Shutdown("t")
	pid, err := o.Spawn(
		abi.ProcessSpec{App: "x", Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/in"}}},
		ExecBody{Argv: []string{"/bin/true"}, Cwd: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, o, pid, abi.StateExited)

	var audited bool
	for _, e := range o.Log().Replay(pid, 0) {
		if e.Kind != abi.EvCapUsed {
			continue
		}
		m := e.Payload.(map[string]any)
		argv, _ := m["argv"].([]string)
		// 审计里必须看得到**实际下发的命令行**, 不是"我们打算怎么限".
		// 不钉最外层是谁 —— 那是分层顺序的事, confine 包自己有测试;
		// 这里只关心隔离层确实在命令行里.
		if len(argv) == 0 || !strings.Contains(strings.Join(argv, " "), "unshare") {
			t.Fatalf("审计缺实际命令行: %+v", m)
		}
		audited = true
	}
	if !audited {
		t.Fatal("confined 进程必须留审计")
	}
}

// 一个 in-proc 进程崩了, 整台 OS 必须还活着.
//
//	console 的 bot 跟 OS 共一个进程. 原来 Entry 里 panic 会把整台
//	机器带走, 客户端看起来就是所有人同时离线, 只有重启才能说话.
func TestInprocPanicDoesNotKillOS(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	pid, err := o.Spawn(abi.ProcessSpec{App: "x", Name: "崩的"},
		InprocBody{Entry: func(context.Context, ProcessContext) (any, error) {
			panic("boom")
		}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, o, pid, abi.StateFailed)
	info, _ := o.Info(pid)
	if info.Outcome == nil || info.Outcome.Error == nil || !strings.Contains(info.Outcome.Error.Message, "boom") {
		t.Fatalf("该记下崩了, 得到 %+v", info.Outcome)
	}
	other, err := o.Spawn(abi.ProcessSpec{App: "y", Name: "还在"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			<-ctx.Done()
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, o, other, abi.StateRunning)
}

// Kill 通过 context 传导, 进程能感知并收尾
func TestKillPropagates(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	started := make(chan struct{})
	pid, _ := o.Spawn(abi.ProcessSpec{App: "demo"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return "被要求退出后自己收的尾", nil
		}})
	<-started
	o.Kill(pid, "管理员终止")
	waitState(t, o, pid, abi.StateExited)
}

// 索引是派生数据, 可以从日志重建 —— 它永远不是真相源
func TestRebuildIndexFromLog(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	pid, _ := o.Spawn(abi.ProcessSpec{App: "demo"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			return pc.Decide(abi.DecisionRequest{
				Present: abi.PresentSpec{Kind: "approve", Title: "x"}})
		}})
	waitState(t, o, pid, abi.StateWaiting)

	o.mu.Lock()
	o.decisionOwner = map[abi.DecisionID]abi.ProcessID{} // 模拟索引丢失
	o.mu.Unlock()
	o.RebuildIndex()

	o.mu.Lock()
	n := len(o.decisionOwner)
	o.mu.Unlock()
	if n != 1 {
		t.Fatalf("索引没从日志重建出来: %d", n)
	}
}

func waitKinds(t *testing.T, mu *sync.Mutex, got *[]abi.EventKind, want int) {
	t.Helper()
	// 同上: 断言的是最终会收到, 死线太紧只会造出假红
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("等 %d 条事件超时, 只收到 %d 条: %v", want, len(*got), *got)
}
