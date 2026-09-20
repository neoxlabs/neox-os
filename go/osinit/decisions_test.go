package osinit

import (
	"context"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func approve(title string) abi.DecisionRequest {
	return abi.DecisionRequest{Present: abi.PresentSpec{Kind: "approve", Title: title}}
}

func TestResolveByAnyDevice(t *testing.T) {
	r := NewDecisionRegistry(clock(), DecisionHooks{})
	ch := r.Request("p1", approve("要继续吗"))

	pending := r.Pending()
	if len(pending) != 1 || pending[0].PID != "p1" {
		t.Fatalf("待决策没登记上: %+v", pending)
	}
	// 决策不属于任何客户端 —— 手机、网页、IM 谁都能解决
	if !r.Resolve(pending[0].DID, "yes", "phone:刘", nil) {
		t.Fatal("Resolve 应当成功")
	}
	res := <-ch
	if res.Choice != "yes" || res.By != "phone:刘" {
		t.Fatalf("%+v", res)
	}
	if len(r.Pending()) != 0 {
		t.Fatal("解决后应从待决策里移除")
	}
}

// 重复解决要拿到明确答复, 而不是静默吞掉
func TestResolveTwiceReportsFalse(t *testing.T) {
	r := NewDecisionRegistry(clock(), DecisionHooks{})
	ch := r.Request("p1", approve("x"))
	did := r.Pending()[0].DID
	r.Resolve(did, "yes", "a", nil)
	<-ch
	if r.Resolve(did, "no", "b", nil) {
		t.Fatal("第二次 Resolve 应返回 false")
	}
	if res, ok := r.SettledResult(did); !ok || res.By != "a" {
		t.Fatal("已解决的结果应当查得到, 且是第一次那个")
	}
}

// 超时兜底也走同一条路径, 日志里看得见"是超时替你做的" —— 不许静默默认
func TestTimeoutIsNotSilent(t *testing.T) {
	var resolved abi.DecisionResolution
	got := make(chan struct{})
	r := NewDecisionRegistry(clock(), DecisionHooks{
		OnResolved: func(res abi.DecisionResolution) { resolved = res; close(got) },
	})
	req := approve("会超时")
	req.OnTimeout = &abi.DecisionTimeout{AfterMs: 20, Choose: "no"}
	ch := r.Request("p1", req)

	select {
	case res := <-ch:
		if res.By != "timeout" || res.Choice != "no" {
			t.Fatalf("超时兜底必须标明 by=timeout: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时兜底没触发")
	}
	<-got
	if resolved.By != "timeout" {
		t.Fatal("OnResolved 也必须收到 by=timeout, 否则日志里看不见")
	}
}

// 人先回答了, 超时定时器不该再开火
func TestManualResolveCancelsTimeout(t *testing.T) {
	n := 0
	r := NewDecisionRegistry(clock(), DecisionHooks{
		OnResolved: func(abi.DecisionResolution) { n++ },
	})
	req := approve("先手动")
	req.OnTimeout = &abi.DecisionTimeout{AfterMs: 30, Choose: "no"}
	ch := r.Request("p1", req)
	r.Resolve(r.Pending()[0].DID, "yes", "phone", nil)
	<-ch
	time.Sleep(80 * time.Millisecond)
	if n != 1 {
		t.Fatalf("超时定时器没被取消, OnResolved 触发了 %d 次", n)
	}
}

// 决策活得比任何一次运行都长 —— 登记后进程可以走开, 回来还在
func TestDecisionOutlivesRun(t *testing.T) {
	r := NewDecisionRegistry(clock(), DecisionHooks{})
	ch := r.Request("p1", approve("挂很久"))
	time.Sleep(50 * time.Millisecond)
	if len(r.Pending()) != 1 {
		t.Fatal("等待期间待决策必须一直在")
	}
	r.Resolve(r.Pending()[0].DID, "ok", "later", nil)
	if (<-ch).Choice != "ok" {
		t.Fatal("醒来后应拿到结果")
	}
}

// settled 表有上限 —— 长跑进程会解决成千上万个决策, 无上限就是内存泄漏
func TestSettledIsBounded(t *testing.T) {
	r := NewDecisionRegistry(clock(), DecisionHooks{})
	var first abi.DecisionID
	for i := 0; i < settledMax+10; i++ {
		ch := r.Request("p1", approve("x"))
		did := r.Pending()[0].DID
		if i == 0 {
			first = did
		}
		r.Resolve(did, "y", "t", nil)
		<-ch
	}
	if _, ok := r.SettledResult(first); ok {
		t.Fatal("最旧的记录应当被淘汰")
	}
	if len(r.settled) > settledMax {
		t.Fatalf("settled 超过上限: %d", len(r.settled))
	}
}

func TestValuesCarried(t *testing.T) {
	r := NewDecisionRegistry(clock(), DecisionHooks{})
	ch := r.Request("p1", abi.DecisionRequest{
		Present: abi.PresentSpec{Kind: "form", Title: "填一下",
			Fields: []abi.PresentField{{ID: "n", Label: "数量", Type: "number"}}},
	})
	r.Resolve(r.Pending()[0].DID, "submit", "web", map[string]any{"n": float64(3)})
	res := <-ch
	if res.Values["n"] != float64(3) {
		t.Fatalf("form 的字段值没带回来: %+v", res.Values)
	}
}

// 批准要记进**对话**的授权表, 下个进程带着它起来.
//
// landlock 的规则在进程启动时定死、只能收紧不能放宽 —— 所以批准
// 在当前进程里**根本无法生效**: 用户答了 yes, agent 仍会
// 拿到"权限被拒". 问了人、人说可以、还是做不到, 那审批就是假的.
//
// 记在对话上而不是进程上: 进程会死, 对话不死 ——
// 用户批准的是"让它做这件事", 不是"让这个进程做这件事".
func TestApprovalGrantsCarryToNextProcess(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	asked := make(chan abi.DecisionID, 1)
	pid, err := o.Spawn(abi.ProcessSpec{
		App: "chat", Labels: map[string]string{"thread": "t1"},
		Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/"}},
	}, InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
		go func() {
			for i := 0; i < 200; i++ {
				if ps := o.Decisions().Pending(); len(ps) > 0 {
					asked <- ps[0].DID
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		pc.Decide(abi.DecisionRequest{
			Scope:   "/data/report.md",
			Present: abi.PresentSpec{Kind: "choice", Title: "允许写吗?"}})
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	did := <-asked
	o.Decisions().Resolve(did, "yes", "test", nil)
	waitState(t, o, pid, abi.StateExited)

	// 下一个属于同一段对话的进程, 计划里必须带上那条授权
	plan, err := o.planFor(abi.ProcessSpec{
		Labels: map[string]string{"thread": "t1"},
		Caps:   []abi.Capability{{Axis: abi.AxisRead, Scope: "/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range plan.FS {
		for _, a := range r.Access {
			if a == "write" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("批准过的写权限没带到下个进程: %+v", plan.FS)
	}
}

// 拒绝不该留下授权 —— 否则"拒绝"等于"稍后允许"
func TestDenialGrantsNothing(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	asked := make(chan abi.DecisionID, 1)
	o.Spawn(abi.ProcessSpec{
		App: "chat", Labels: map[string]string{"thread": "t2"},
	}, InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
		go func() {
			for i := 0; i < 200; i++ {
				if ps := o.Decisions().Pending(); len(ps) > 0 {
					asked <- ps[0].DID
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		pc.Decide(abi.DecisionRequest{
			Scope:   "/data/x",
			Present: abi.PresentSpec{Kind: "choice", Title: "允许?"}})
		return nil, nil
	}})
	did := <-asked
	o.Decisions().Resolve(did, "no", "test", nil)
	time.Sleep(50 * time.Millisecond)

	if g := o.grantsFor("t2"); len(g) != 0 {
		t.Fatalf("拒绝却留下了授权: %+v", g)
	}
}

// 预检也要算上对话攒下的批准.
//
// 只把批准合进内核规则是不够的: 预检用原始能力集, 于是新进程明明
// 有权写, 预检却说"不在范围内", agent 又去问一次人 ——
// 先问 d1 又问 d2, **审批变成死循环**.
// 两处判断同一件事就必然分叉.
func TestPrecheckSeesThreadGrants(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	asked := make(chan abi.DecisionID, 1)
	o.Spawn(abi.ProcessSpec{
		App: "chat", Labels: map[string]string{"thread": "t9"},
		Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/"}},
	}, InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
		go func() {
			for i := 0; i < 200; i++ {
				if ps := o.Decisions().Pending(); len(ps) > 0 {
					asked <- ps[0].DID
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		pc.Decide(abi.DecisionRequest{
			Scope:   "/data/report.md",
			Present: abi.PresentSpec{Kind: "choice", Title: "允许写吗?"}})
		return nil, nil
	}})
	o.Decisions().Resolve(<-asked, "yes", "test", nil)
	time.Sleep(50 * time.Millisecond)

	// 同一段对话的下一个进程: 预检必须说"可以", 否则它会再问一次
	done := make(chan bool, 1)
	o.Spawn(abi.ProcessSpec{
		App: "chat", Labels: map[string]string{"thread": "t9"},
		Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/"}},
	}, InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
		done <- pc.Can("write", "/data/report.md")
		return nil, nil
	}})
	if !<-done {
		t.Fatal("批准过的路径, 预检仍说不允许 —— agent 会再问一遍, 审批死循环")
	}
}
