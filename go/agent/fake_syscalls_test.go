package agent

import (
	"fmt"
	"sync"

	"github.com/neox-os/neox-os/abi"
)

// fakeSys 一个假的系统调用面, 让主循环能脱离真 OS 跑.
//
// 它**不模拟模型** —— 模型用 Scripted, 那是两件事:
// Scripted 决定"下一步做什么", fakeSys 决定"OS 怎么回答".
// 混在一起就分不清失败是哪一层的.
type fakeSys struct {
	mu sync.Mutex
	// events 按 phase 记下所有 Emit —— 断言要能查"有没有走到那个分支"
	events []map[string]any

	// canFn 能力预检. nil = 一律允许
	canFn func(axis abi.CapAxis, scope string) (bool, error)
	// spendErr 非 nil 表示止损线到了
	spendErr error
	// spendFn 比 spendErr 精细: 能让止损线在第 N 次才到
	spendFn func(abi.BudgetDelta) error
	// decision 人工决策的结果
	decision  abi.DecisionResolution
	decideErr error
	// decideFn 比 decision 精细: 能看到 agent 到底问了用户什么
	decideFn func(abi.DecisionRequest) (abi.DecisionResolution, error)
	// inferFn 让个别用例真走一次推理路径 —— 默认仍然是"不该走真推理",
	// 免得哪个用例悄悄依赖上模型
	inferFn func(abi.InferParams) (abi.InferResult, error)
	// interrupts TryRecv 依次返回这些 —— 模拟用户在干活中途插话
	interrupts []string
	// inbox Recv 依次返回这些; 用完返回 Closed
	inbox []abi.RecvResult
	recvN int
}

func newFakeSys() *fakeSys {
	return &fakeSys{decision: abi.DecisionResolution{Choice: "yes", By: "test"}}
}

func (f *fakeSys) Emit(payload any) {
	m, ok := payload.(map[string]any)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, m)
}

func (f *fakeSys) Can(axis abi.CapAxis, scope string) (bool, error) {
	if f.canFn != nil {
		return f.canFn(axis, scope)
	}
	return true, nil
}

func (f *fakeSys) Spend(d abi.BudgetDelta) error {
	if f.spendFn != nil {
		return f.spendFn(d)
	}
	return f.spendErr
}

func (f *fakeSys) Decide(req abi.DecisionRequest) (abi.DecisionResolution, error) {
	if f.decideFn != nil {
		return f.decideFn(req)
	}
	return f.decision, f.decideErr
}

func (f *fakeSys) Infer(p abi.InferParams) (abi.InferResult, error) {
	if f.inferFn != nil {
		return f.inferFn(p)
	}
	return abi.InferResult{}, fmt.Errorf("这个测试不该走真推理 —— 模型是 Scripted")
}

// TryRecv 看一眼有没有新话. 默认没有 —— 绝大多数用例不该被插话打扰
func (f *fakeSys) TryRecv() (abi.RecvResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.interrupts) == 0 {
		return abi.RecvResult{}, false
	}
	t := f.interrupts[0]
	f.interrupts = f.interrupts[1:]
	return abi.RecvResult{Text: t, From: "test"}, true
}

func (f *fakeSys) Recv() (abi.RecvResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recvN >= len(f.inbox) {
		return abi.RecvResult{Closed: true}, nil
	}
	r := f.inbox[f.recvN]
	f.recvN++
	return r, nil
}

// phases 所有出现过的 phase, 按顺序
func (f *fakeSys) phases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		if p, ok := e["phase"].(string); ok {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeSys) saw(phase string) bool {
	for _, p := range f.phases() {
		if p == phase {
			return true
		}
	}
	return false
}

// last 某个 phase 最后一次的 payload
func (f *fakeSys) last(phase string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.events) - 1; i >= 0; i-- {
		if p, _ := f.events[i]["phase"].(string); p == phase {
			return f.events[i]
		}
	}
	return nil
}
