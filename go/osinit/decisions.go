package osinit

import (
	"fmt"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// DecisionRegistry 待决策登记处.
//
// 这是 Neox OS 区别于 agent IDE 的核心原语:
//
//	旧: agent 请求人工决策 → 弹一个框 → 阻塞等**当前连着的那个 UI** 回答.
//	    没人连着 = 卡死或超时失败. 这就是"必须待命在电脑边"的成因.
//
//	新: agent 请求人工决策 → 登记一条待决策 → 进程转 waiting (不占计算,
//	    可被换出) → OS 路由到能找到人的通道 → 人在任意时刻、任意设备上
//	    解决 → 进程被唤醒继续.
//
// 关键性质:
//   - 决策活得比进程的任何一次运行都长
//   - 决策不属于任何客户端. 谁先解决算谁的
//   - 超时有 OS 兜底, 而且**不许静默默认** —— 日志里要留下 by="timeout"
type DecisionRegistry struct {
	mu      sync.Mutex
	waiters map[abi.DecisionID]*waiter
	// settled 已解决的保留最近 N 条, 让重复 resolve 拿到明确答复而不是"不存在".
	// 有上限是刻意的 —— 长跑进程会解决成千上万个决策, 无上限就是内存泄漏.
	settled      map[abi.DecisionID]abi.DecisionResolution
	settledOrder []abi.DecisionID
	counter      int
	now          func() int64
	hooks        DecisionHooks
}

const settledMax = 256

type DecisionHooks struct {
	OnRequested func(abi.PendingDecision)
	OnResolved  func(abi.DecisionResolution)
}

type waiter struct {
	pending abi.PendingDecision
	ch      chan abi.DecisionResolution
	timer   *time.Timer
}

func NewDecisionRegistry(now func() int64, hooks DecisionHooks) *DecisionRegistry {
	return &DecisionRegistry{
		waiters: map[abi.DecisionID]*waiter{},
		settled: map[abi.DecisionID]abi.DecisionResolution{},
		now:     now,
		hooks:   hooks,
	}
}

// Request 登记一条待决策, 返回一个在解决时才收到值的 channel.
//
// 这个 channel 可能几小时后才有值 —— 刻意不设内部超时,
// 超时该由 DecisionRequest.OnTimeout 表达.
func (r *DecisionRegistry) Request(pid abi.ProcessID, req abi.DecisionRequest) <-chan abi.DecisionResolution {
	r.mu.Lock()
	r.counter++
	did := fmt.Sprintf("d%d", r.counter)
	w := &waiter{
		pending: abi.PendingDecision{DID: did, PID: pid, Request: req, RequestedAt: r.now()},
		ch:      make(chan abi.DecisionResolution, 1),
	}
	if req.OnTimeout != nil {
		d := time.Duration(req.OnTimeout.AfterMs) * time.Millisecond
		choose := req.OnTimeout.Choose
		w.timer = time.AfterFunc(d, func() {
			// 超时兜底也走同一条 Resolve 路径 —— 于是日志里看得见
			// "这个决定是超时替你做的". 不许静默默认.
			r.Resolve(did, choose, "timeout", nil)
		})
	}
	r.waiters[did] = w
	pending := w.pending
	r.mu.Unlock()

	if r.hooks.OnRequested != nil {
		r.hooks.OnRequested(pending)
	}
	return w.ch
}

func (r *DecisionRegistry) Pending() []abi.PendingDecision {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]abi.PendingDecision, 0, len(r.waiters))
	for _, w := range r.waiters {
		out = append(out, w.pending)
	}
	return out
}

// Resolve 解决一个决策.
//
// 返回 false 表示它已经被解决过 (或从不存在) —— 调用方据此告诉用户
// "这条已经处理过了", 而不是静默吞掉.
func (r *DecisionRegistry) Resolve(did abi.DecisionID, choice, by string, values map[string]any) bool {
	r.mu.Lock()
	w, ok := r.waiters[did]
	if !ok {
		r.mu.Unlock()
		return false
	}
	delete(r.waiters, did)
	if w.timer != nil {
		w.timer.Stop()
	}
	res := abi.DecisionResolution{DID: did, Choice: choice, By: by, At: r.now(), Values: values}
	r.settled[did] = res
	r.settledOrder = append(r.settledOrder, did)
	if len(r.settledOrder) > settledMax {
		oldest := r.settledOrder[0]
		r.settledOrder = r.settledOrder[1:]
		delete(r.settled, oldest)
	}
	r.mu.Unlock()

	if r.hooks.OnResolved != nil {
		r.hooks.OnResolved(res)
	}
	w.ch <- res
	close(w.ch)
	return true
}

func (r *DecisionRegistry) SettledResult(did abi.DecisionID) (abi.DecisionResolution, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.settled[did]
	return res, ok
}

// Dispose 关机时清掉所有定时器, 否则进程退不干净
func (r *DecisionRegistry) Dispose() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.waiters {
		if w.timer != nil {
			w.timer.Stop()
		}
	}
}
