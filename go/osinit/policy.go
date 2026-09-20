package osinit

import (
	"fmt"
	"sync"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/confine"
)

// 策略层 —— 决定一次被冻结的系统调用是"自动放行 / 自动拒绝 / 升级给人".
//
// 没有这一层, syscall 级审批会把人问死: 一个编译可能触发上万次 openat.
// 有了它, 打扰频率**随时间单调下降** —— 每一次人类决策都是一条标注数据,
// 收敛成策略之后就不用再问.
//
// 这也是 syscall 级审批比工具级审批强的地方: 工具级的粒度太粗
// ("允许 bash" = 允许一切), 学不出有意义的策略, 只能在"每次都问"和
// "全放开"之间二选一. syscall 级粒度细, 策略才有收敛的空间.
//
// **判断复用 confine.CapabilityAllows** —— 不新开第二个真相源.

type Verdict int

const (
	// VerdictAllow 自动放行
	VerdictAllow Verdict = iota
	// VerdictDeny 自动拒绝, 进程收到 EACCES
	VerdictDeny
	// VerdictAsk 升级给人. 进程在 syscall 上冻住, 等多久都行
	VerdictAsk
)

func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictDeny:
		return "deny"
	default:
		return "ask"
	}
}

// Autonomy 自主级别 —— 由用户按应用/按进程设定.
type Autonomy string

const (
	// AutonomyFull 全自主: 能力集内自动放行, 集外自动拒绝, **从不打扰人**.
	// 适合你信任的长跑任务. 注意它不是"放开", 边界仍然是能力集.
	AutonomyFull Autonomy = "autonomous"

	// AutonomyGuarded 有人兜底 (**缺省**): 能力集内自动放行,
	// 集外升级给人 —— 人可以一次性允许, 也可以让它记住.
	AutonomyGuarded Autonomy = "guarded"

	// AutonomyParanoid 每一步都问: 连能力集内的也要问 (记住过的除外).
	// 适合第一次跑一个不熟悉的任务.
	AutonomyParanoid Autonomy = "paranoid"
)

// Grant 一条人类批准过的授权 —— "以后不用再问了".
type Grant struct {
	// App 作用域: 只对这个应用生效.
	// **刻意不做全局授权** —— 一次批准不该让所有应用都能干这事.
	App string `json:"app"`
	// Axis/Scope 跟能力集同一套语义, 所以判断可以复用同一份实现
	Axis  string `json:"axis"`
	Scope string `json:"scope"`
	// By 谁批的; At 什么时候. 审计要能追到人
	By string `json:"by"`
	At int64  `json:"at"`
}

// PolicyStore 策略与学到的授权.
//
// 线程安全: syscall 拦截来自多个进程, 会并发查询.
type PolicyStore struct {
	mu       sync.RWMutex
	autonomy map[string]Autonomy // app → 级别
	grants   []Grant
	now      func() int64
}

func NewPolicyStore(now func() int64) *PolicyStore {
	if now == nil {
		now = func() int64 { return 0 }
	}
	return &PolicyStore{autonomy: map[string]Autonomy{}, now: now}
}

// SetAutonomy 设定某个应用的自主级别
func (p *PolicyStore) SetAutonomy(app string, a Autonomy) {
	p.mu.Lock()
	p.autonomy[app] = a
	p.mu.Unlock()
}

// AutonomyOf 取自主级别. **缺省是 guarded** —— 缺省必须是安全的那个.
func (p *PolicyStore) AutonomyOf(app string) Autonomy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if a, ok := p.autonomy[app]; ok {
		return a
	}
	return AutonomyGuarded
}

// Remember 记住一条人类批准 —— 下次同类请求不再打扰.
//
// 必须带 by/at: 三个月后要能回答"这条谁批的、什么时候批的".
func (p *PolicyStore) Remember(g Grant) {
	p.mu.Lock()
	g.At = p.now()
	p.grants = append(p.grants, g)
	p.mu.Unlock()
}

// Forget 撤销一条授权. 学到的东西必须可撤销, 否则就是不可逆的权限膨胀.
func (p *PolicyStore) Forget(app, axis, scope string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, g := range p.grants {
		if g.App == app && g.Axis == axis && g.Scope == scope {
			p.grants = append(p.grants[:i], p.grants[i+1:]...)
			return true
		}
	}
	return false
}

func (p *PolicyStore) Grants() []Grant {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Grant(nil), p.grants...)
}

// granted 学到的授权里有没有覆盖这次请求的.
// 用跟能力集完全相同的匹配语义 —— 不新开第二套规则.
func (p *PolicyStore) granted(app, axis, scope string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, g := range p.grants {
		if g.App != app || !axisSatisfies(g.Axis, axis) {
			continue
		}
		if confine.ScopeMatches(axis, g.Scope, scope) {
			return true
		}
	}
	return false
}

// axisSatisfies 跟 confine 里的蕴含关系保持一致 (write 蕴含 read)
func axisSatisfies(granted, requested string) bool {
	return granted == requested || (granted == "write" && requested == "read")
}

// Evaluate 裁决一次请求.
//
// 顺序是刻意的:
//  1. 能力集内 → 除非 paranoid, 否则直接放行 (这是进程本来就有的权限)
//  2. 学到的授权覆盖 → 放行 (人批过了)
//  3. 剩下的按自主级别: full=拒 / guarded=问 / paranoid=问
func (p *PolicyStore) Evaluate(app, axis, scope string, caps []abi.Capability) Verdict {
	inCaps := confine.CapabilityAllows(caps, axis, scope)
	level := p.AutonomyOf(app)

	if inCaps {
		if level == AutonomyParanoid && !p.granted(app, axis, scope) {
			return VerdictAsk
		}
		return VerdictAllow
	}
	if p.granted(app, axis, scope) {
		return VerdictAllow
	}
	if level == AutonomyFull {
		// 全自主不代表放开边界 —— 越界照样拒, 只是不打扰人
		return VerdictDeny
	}
	return VerdictAsk
}

// ── 把裁决接到"问人"那条链上 ───────────────────────────────

// SyscallRequest 一次被冻结的系统调用 (来自 confine.Supervisor)
type SyscallRequest struct {
	PID  abi.ProcessID
	Call string
	Axis string
	Path string
}

// Broker 把 syscall 拦截接到 ctx.Decide() 上.
//
// 这是整条链的接缝:
//
//	内核冻结 openat → Supervisor → Broker → 策略层
//	  ├─ allow/deny → 立即回内核 (微秒级)
//	  └─ ask        → ctx.Decide() 登记待决策 → 推手机 → 人点一下
//	                   → 进程醒来, syscall 继续
//
// 关键: ask 分支里进程**一直冻在内核里**, 不占 CPU. 等一小时和等一秒
// 对系统是一回事 —— 这正是 waiting 不计活跃时长那条设计的延伸.
type Broker struct {
	policy *PolicyStore
	os     *OS
	// AskTimeoutMs 没人回答时的兜底. 0 = 不兜底 (一直等).
	// 兜底必须是**拒绝** —— 没人回答不能当成同意.
	AskTimeoutMs int64
}

func NewBroker(os *OS, policy *PolicyStore) *Broker {
	return &Broker{os: os, policy: policy, AskTimeoutMs: 0}
}

// Decide 裁决一次被冻结的调用. 会阻塞直到有结论 (可能很久).
func (b *Broker) Decide(req SyscallRequest) Verdict {
	info, ok := b.os.Info(req.PID)
	if !ok {
		return VerdictDeny // 进程都没了, 拒绝
	}
	app := info.Spec.App

	v := b.policy.Evaluate(app, req.Axis, req.Path, info.Spec.Caps)
	if v != VerdictAsk {
		// 自动裁决也要进审计 —— 否则"它昨晚到底碰了什么"答不上来
		kind := abi.EvCapUsed
		if v == VerdictDeny {
			kind = abi.EvCapDenied
		}
		b.os.Log().Append(req.PID, kind, map[string]any{
			"call": req.Call, "axis": req.Axis, "scope": req.Path,
			"verdict": v.String(), "by": "policy",
		})
		return v
	}

	// ── 升级给人 ──
	ctx := b.os.contextFor(req.PID)
	dreq := abi.DecisionRequest{
		Urgency: "high",
		Present: abi.PresentSpec{
			Kind:   "choice",
			Title:  fmt.Sprintf("允许 %s 访问 %s 吗?", app, req.Path),
			Detail: fmt.Sprintf("进程 %s 正在 %s (%s), 这不在它的能力集里", req.PID, req.Call, req.Axis),
			Options: []abi.PresentOption{
				{ID: "once", Label: "允许这一次"},
				{ID: "always", Label: "允许, 以后不用问"},
				{ID: "deny", Label: "拒绝", Destructive: true},
			},
		},
	}
	if b.AskTimeoutMs > 0 {
		// 没人回答 → 拒绝. **不许把沉默当同意**
		dreq.OnTimeout = &abi.DecisionTimeout{AfterMs: b.AskTimeoutMs, Choose: "deny"}
	}

	res, err := ctx.Decide(dreq)
	if err != nil {
		return VerdictDeny // 进程被终止 / OS 关机 → 拒绝
	}

	switch res.Choice {
	case "always":
		// 记住: 下次同类请求直接放行, 打扰频率就这样降下来
		b.policy.Remember(Grant{App: app, Axis: req.Axis, Scope: req.Path, By: res.By})
		return VerdictAllow
	case "once":
		return VerdictAllow
	default:
		return VerdictDeny
	}
}
