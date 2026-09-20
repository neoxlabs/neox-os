package osinit

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"sync"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/confine"
	"github.com/neox-os/neox-os/engine"
)

// procContext 是注入给进程的 ProcessContext 实现.
//
// 它只持有 pid 和 OS 引用 —— 进程拿不到进程表、拿不到别人的日志.
type procContext struct {
	os  *OS
	pid abi.ProcessID
}

func (o *OS) contextFor(pid abi.ProcessID) ProcessContext {
	return &procContext{os: o, pid: pid}
}

func (c *procContext) PID() abi.ProcessID { return c.pid }
func (c *procContext) ABIVersion() string { return abi.Version }

func (c *procContext) Done() <-chan struct{} {
	c.os.mu.Lock()
	defer c.os.mu.Unlock()
	rec, ok := c.os.procs[c.pid]
	if !ok {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return rec.ctx.Done()
}

// Recv 等下一句输入.
//
// 等待期间转 waiting —— 与等待决策使用同一套状态语义,
// 所以"等一小时和等一秒成本相同"这条对它同样成立.
// TryRecv 看一眼有没有新话, **不等**.
//
// ── 为什么必须有 ──
//
// Serve 只在两轮之间调 Recv, 于是一轮跑起来之后进程就是**聋的**.
// 一个分片读日志的长任务读到第 488 行时收到"停, 别读了",
// 却接着读到第 1457 行 —— 那句话一直躺在收件箱里没人取.
// "中途打断"这件事在这个形态下根本不成立.
//
// 返回 ok=false 表示现在没有新话. **不留等待通道** —— 见 inbox.tryTake.
func (c *procContext) TryRecv() (abi.RecvResult, bool) {
	c.os.mu.Lock()
	rec, ok := c.os.procs[c.pid]
	if !ok {
		c.os.mu.Unlock()
		return abi.RecvResult{Closed: true}, true
	}
	if rec.inbox == nil {
		rec.inbox = newInbox()
	}
	box := rec.inbox
	c.os.mu.Unlock()
	return box.tryTake()
}

func (c *procContext) Recv() (abi.RecvResult, error) {
	c.os.mu.Lock()
	rec, ok := c.os.procs[c.pid]
	if !ok {
		c.os.mu.Unlock()
		return abi.RecvResult{Closed: true}, nil
	}
	if rec.inbox == nil {
		rec.inbox = newInbox()
	}
	box := rec.inbox
	c.os.mu.Unlock()

	msg, wait, ready := box.take()
	if ready {
		return msg, nil
	}

	c.os.setState(c.pid, abi.StateWaiting)
	select {
	case m := <-wait:
		if !m.Closed {
			c.os.setState(c.pid, abi.StateRunning)
		}
		return m, nil
	case <-c.Done():
		// 关机时不能被一个永远不来的输入钉住
		return abi.RecvResult{Closed: true}, context.Canceled
	}
}

func (c *procContext) Emit(payload any) {
	c.os.events.Append(c.pid, abi.EvProcOutput, payload)
}

// EmitLive 只给此刻正看着的人 —— **不进账本**. 见 abi.EvProcDelta.
//
//	边生成边吐的那些小段字走这条. 走 Emit 的话, 一次回复会往账本里
//	塞上百条, 而它们加起来的信息量跟最后那条 phase:reply 一模一样.
func (c *procContext) EmitLive(payload any) {
	c.os.events.Append(c.pid, abi.EvProcDelta, payload)
}

// Infer 请求一次推理.
//
//	跟 ABI 那条走**同一条路**: 供应商在 OS 手里, 记账也在 OS 手里.
//	不能让进程内的 body 绕过去直接拿 provider —— 那样 token 就不进预算,
//	止损线躲得掉, 而"止损线躲不掉"是这套设计的前提之一.
func (c *procContext) Infer(p abi.InferParams) (abi.InferResult, error) {
	provider := c.os.Provider()
	if provider == nil {
		// **说清是"这台机器没配", 不是"推理失败了"** ——
		// 后者会让调用方换个说法重试, 而那件事永远不会成
		return abi.InferResult{}, errors.New("这台机器没有配置推理服务")
	}
	res, err := provider.Infer(context.Background(), p)
	if err != nil {
		return abi.InferResult{}, err
	}
	total := billableTokens(res)
	if serr := c.Spend(abi.BudgetDelta{Tokens: &total}); serr != nil {
		// 已经花掉了才发现超限: 结果照给(钱都付了), 下一次调用会被 Spend 拦住
		return res, nil
	}
	return res, nil
}

// InferStream 流式推理.
//
//	供应商不支持流式就**退回一次性返回**, 而不是把整段切片假装成流:
//	假流式的节奏是均匀的, 真流式不是, 骗不过人; 更要紧的是它会让
//	上层以为自己拿到了"边生成边看", 于是不去做真正该做的那件事.
func (c *procContext) InferStream(p abi.InferParams, onDelta func(engine.StreamDelta)) (abi.InferResult, error) {
	provider := c.os.Provider()
	if provider == nil {
		return abi.InferResult{}, errors.New("这台机器没有配置推理服务")
	}
	streamer, ok := provider.(engine.Streaming)
	if !ok {
		return c.Infer(p)
	}
	res, err := streamer.InferStream(context.Background(), p, onDelta)
	// 断在半路也要记账: **token 已经花掉了**, 不记等于让止损线漏一笔
	if res.PromptTokens > 0 || res.CompletionTokens > 0 {
		total := billableTokens(res)
		_ = c.Spend(abi.BudgetDelta{Tokens: &total})
	}
	return res, err
}

func (c *procContext) Decide(req abi.DecisionRequest) (abi.DecisionResolution, error) {
	ch := c.os.decisions.Request(c.pid, req)
	select {
	case res := <-ch:
		return res, nil
	case <-c.Done():
		// OS 要求退出时不再等人 —— 否则关机会被一个没人回答的决策卡住
		return abi.DecisionResolution{}, context.Canceled
	}
}

// Can 能力预检.
//
// 语义**不在这里定义**, 在 confine.ScopeMatches —— 那是唯一实现.
// 预检不是安全边界: 真强制在内核, 进程调不调都逃不掉.
// 它唯一的价值是让进程提前知道, 少吃一个莫名其妙的 EACCES.
func (c *procContext) Can(axis, scope string) bool {
	c.os.mu.Lock()
	rec, ok := c.os.procs[c.pid]
	if !ok {
		c.os.mu.Unlock()
		return false
	}
	caps := rec.info.Spec.Caps
	thread := rec.info.Spec.Labels["thread"]
	if thread == "" {
		thread = string(c.pid)
	}
	c.os.mu.Unlock()

	// **预检也要算上这段对话攒下的批准.**
	//
	// 只把批准合进内核规则(planFor)是不够的: 预检用的是原始能力集,
	// 于是新进程明明有权写, 预检却说"不在范围内", agent 又去问一次人 ——
		// 连续问 d1 和 d2 会让**审批变成死循环**.
	//
	// 两处判断同一件事就必然分叉. 这是同一个模式的又一次:
	// 预检和内核规则必须**看同一份能力集**.
	if extra := c.os.grantsFor(thread); len(extra) > 0 {
		caps = append(append([]abi.Capability(nil), caps...), extra...)
	}

	allowed := confine.CapabilityAllows(caps, axis, scope)
	// "都不问"档下出网直接放行 —— capsFor 只在 spawn 时算,
	// 这个开关让已经跑着的 bot 也立刻拿到. 见 OS.netOpen 的说明.
	if !allowed && axis == string(abi.AxisNet) && c.os.netOpen.Load() {
		allowed = true
	}
	kind := abi.EvCapDenied
	if allowed {
		kind = abi.EvCapUsed
	}
	c.os.events.Append(c.pid, kind, map[string]any{"axis": axis, "scope": scope})
	return allowed
}

// Spend 记一笔消耗.
//
// 注意语义: Budget 是**止损上限**, 不是预先分配的配额 ——
// agent 的开销预估不出来, 按预估分配是空谈.
// 这里做的是"跑飞了别把机器拖死", 由 OS 强制, 不靠进程自觉.
func (c *procContext) Spend(delta abi.BudgetDelta) error {
	c.os.mu.Lock()
	rec, ok := c.os.procs[c.pid]
	if !ok {
		c.os.mu.Unlock()
		return nil
	}
	if delta.Tokens != nil {
		rec.spent.Tokens += *delta.Tokens
	}
	if delta.NetCalls != nil {
		rec.spent.NetCalls += *delta.NetCalls
	}
	if delta.ActiveMs != nil {
		rec.spent.ActiveMs += *delta.ActiveMs
	}
	b := rec.info.Spec.Budget
	spent := rec.spent
	active := rec.spent.ActiveMs
	if rec.runningSince != 0 {
		active += c.os.now() - rec.runningSince
	}
	c.os.mu.Unlock()

	c.os.events.Append(c.pid, abi.EvBudgetSpent, delta)

	if b == nil {
		return nil
	}
	// 撞的是哪条线、花了多少、上限多少 —— **在这儿就说清楚**.
	// 这是唯一知道真相的地方, 丢在这儿后面每一层都只能猜 (见 budgeterr.go).
	if b.Tokens != nil && spent.Tokens > *b.Tokens {
		return &BudgetExceeded{Kind: "tokens", Spent: spent.Tokens, Limit: *b.Tokens}
	}
	if b.NetCalls != nil && spent.NetCalls > *b.NetCalls {
		return &BudgetExceeded{Kind: "netCalls", Spent: spent.NetCalls, Limit: *b.NetCalls}
	}
	if b.ActiveMs != nil && active > *b.ActiveMs {
		return &BudgetExceeded{Kind: "activeMs", Spent: active, Limit: *b.ActiveMs}
	}
	return nil
}

// mintToken 一次性 token. 128 位随机, 不带任何可推断信息.
func mintToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ── 真实 spawner ────────────────────────────────────────────

type realChild struct {
	cmd  *exec.Cmd
	once sync.Once
	line func(string)
	mu   sync.Mutex
}

func realSpawner(argv []string, cwd string, env map[string]string) (Child, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	envv := make([]string, 0, len(env))
	for k, v := range env {
		envv = append(envv, k+"="+v)
	}
	cmd.Env = envv
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	rc := &realChild{cmd: cmd}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			rc.mu.Lock()
			fn := rc.line
			rc.mu.Unlock()
			if fn != nil {
				fn(sc.Text())
			}
		}
	}()
	return rc, nil
}

func (c *realChild) OnLine(fn func(string)) {
	c.mu.Lock()
	c.line = fn
	c.mu.Unlock()
}

func (c *realChild) Wait() (int, string, error) {
	err := c.cmd.Wait()
	if err == nil {
		return 0, "", nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), ee.String(), nil
	}
	return -1, "", err
}

func (c *realChild) Kill(string) {
	c.once.Do(func() {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
	})
}
