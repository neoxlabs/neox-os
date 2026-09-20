package osinit

import (
	"context"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func decEv(pid string, kind abi.EventKind, payload map[string]any) abi.Event {
	return abi.Event{PID: abi.ProcessID(pid), Kind: kind, Payload: payload}
}

func resolutionsOf(o *OS, pid string) map[string]string {
	out := map[string]string{}
	for _, e := range o.Log().Replay(abi.ProcessID(pid), 0) {
		if e.Kind != abi.EvDecideResolved {
			continue
		}
		m, _ := e.Payload.(map[string]any)
		did, _ := m["did"].(string)
		choice, _ := m["choice"].(string)
		out[did] = choice
	}
	return out
}

// 恢复时仍未决的决策必须当场判过期.
//
// 不判的话界面照着账本渲染, 那张卡永远停在"等待决策" —— 而等在它上面的
// waiter 只活在内存里, 实例一换就没了. 用户点"允许", Resolve 找不到人,
// **什么都不会发生, 也不会有任何提示**.
func TestRestoreExpiresOrphanDecisions(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试结束")
	o.RestoreEvents(map[abi.ProcessID][]abi.Event{
		"p1": {
			decEv("p1", abi.EvDecideRequest, map[string]any{"did": "d1"}),
			decEv("p1", abi.EvDecideRequest, map[string]any{"did": "d2"}),
			decEv("p1", abi.EvDecideResolved, map[string]any{"did": "d2", "choice": "yes"}),
		},
	})
	got := resolutionsOf(o, "p1")
	if got["d1"] != DecisionExpired {
		t.Fatalf("没拍板的 d1 没判过期: %q —— 那张卡会永远停在等你拍板", got["d1"])
	}
	// 已经拍过的不许被改写 —— 那是伪造历史
	if got["d2"] != "yes" {
		t.Fatalf("已经拍过的 d2 被改成了 %q", got["d2"])
	}
}

// 过期**不能装成用户选了某一项**.
//
// 判成 "no" 看起来更省事(界面现成的路径), 但那是把"无人决策"说成
// "明确拒绝" —— 事后翻账本的人会据此得出完全错的结论.
func TestExpiredIsItsOwnOutcome(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试结束")
	o.RestoreEvents(map[abi.ProcessID][]abi.Event{
		"p1": {decEv("p1", abi.EvDecideRequest, map[string]any{"did": "d1"})},
	})
	for _, e := range o.Log().Replay("p1", 0) {
		if e.Kind != abi.EvDecideResolved {
			continue
		}
		m, _ := e.Payload.(map[string]any)
		if choice, _ := m["choice"].(string); choice == "yes" || choice == "no" {
			t.Fatalf("过期被写成了 %q —— 那是把「没人拍板」说成「人做了选择」", choice)
		}
		// 为什么过期要说清楚, 否则事后翻出来是一条无头案
		if why, _ := m["why"].(string); why == "" {
			t.Error("过期没写原因")
		}
		if by, _ := m["by"].(string); by == "" {
			t.Error("过期没写是谁判的 —— 这个登记处的规矩是不许静默默认")
		}
	}
}

// 一个进程上有好几条时, 判的顺序要跟账本里的登记顺序一致.
func TestExpireKeepsLedgerOrder(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试结束")
	o.RestoreEvents(map[abi.ProcessID][]abi.Event{
		"p1": {
			decEv("p1", abi.EvDecideRequest, map[string]any{"did": "a"}),
			decEv("p1", abi.EvDecideRequest, map[string]any{"did": "b"}),
			decEv("p1", abi.EvDecideRequest, map[string]any{"did": "c"}),
		},
	})
	var order []string
	for _, e := range o.Log().Replay("p1", 0) {
		if e.Kind != abi.EvDecideResolved {
			continue
		}
		m, _ := e.Payload.(map[string]any)
		did, _ := m["did"].(string)
		order = append(order, did)
	}
	want := []string{"a", "b", "c"}
	for i, did := range want {
		if i >= len(order) || order[i] != did {
			t.Fatalf("过期顺序是 %v, 应该跟账本一致 %v", order, want)
		}
	}
}

// **关机要等进程收拾完**.
//
// 进程可以注册收尾: 收掉自己起的后台服务、落最后一笔账. 不等的话那些
// 收尾一个都跑不到 —— 而进程注册它们的时候是当真的.
//
// bot 为验证启动的 http 服务应在关机时被收掉; 否则会活过整次重启,
// 一直占着端口. 下一次验证撞上"端口已被占用",
// 而那个错跟真正的原因隔着好几层.
func TestShutdownWaitsForCleanup(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	cleaned := make(chan struct{})
	if _, err := o.Spawn(abi.ProcessSpec{App: "x", Name: "x"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			defer close(cleaned) // 进程注册的收尾
			<-ctx.Done()
			return nil, nil
		}}); err != nil {
		t.Fatal(err)
	}
	// 等它真的跑起来, 否则测的是"还没开始"而不是"收拾完了"
	time.Sleep(30 * time.Millisecond)
	o.Shutdown("测试关机")
	select {
	case <-cleaned:
	default:
		t.Fatal("关机没等进程收拾完 —— 它注册的收尾一个都没跑到")
	}
}

// 但**只等一小会儿**: 一个卡住的进程不能把关机拖到永远.
//
// 而且到点走人要**说出来**, 不静默放弃 —— 否则没人知道有东西留了下来.
func TestShutdownDoesNotHangOnAStuckProcess(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	if _, err := o.Spawn(abi.ProcessSpec{App: "stuck", Name: "stuck"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			<-make(chan struct{}) // 永远不回来
			return nil, nil
		}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	o.Shutdown("测试关机")
	if took := time.Since(start); took > shutdownGrace+2*time.Second {
		t.Fatalf("被一个卡住的进程拖了 %s —— 用户点的是退出", took)
	}
	said := false
	for _, e := range o.Log().Replay(abi.ProcessID("os"), 0) {
		if m, ok := e.Payload.(map[string]any); ok {
			if m["phase"] == "shutdown_slow" {
				said = true
			}
		}
	}
	if !said {
		t.Error("到点走人却没说 —— 没人知道有东西留了下来")
	}
}
