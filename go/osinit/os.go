package osinit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/confine"
	"github.com/neox-os/neox-os/engine"
)

// OS 的核心: 进程表 + 系统调用实现.
//
// 这个模块**不跑 agent**, 它调度跑 agent 的东西.
//
// 跟 agent IDE 最重要的三处不同:
//
//  1. 进程不依附于任何"会话"或客户端连接. 关掉所有客户端, 进程照跑.
//  2. 观察全部走事件日志. OS 自己不持有任何"给人看的"渲染态.
//  3. 等待决策时进程转 waiting, 不占计算 —— "等一天"和"等一秒"
//     对资源的成本是一样的. 这是 24h 常驻能省钱的技术前提.

var (
	ErrBudgetExceeded  = errors.New("budget exceeded")
	ErrConfineUnusable = errors.New("confinement unavailable")
	ErrModeMismatch    = errors.New("mode/body mismatch")
)

// ProcessContext 是进程能拿到的**全部**东西. 没有别的入口.
type ProcessContext interface {
	PID() abi.ProcessID
	ABIVersion() string
	// Emit 产出结构化输出. 进事件日志, 所有订阅者可见
	Emit(payload any)
	// Decide 请求一个人类决策. 进程在此转 waiting 并可被换出.
	// 可能几小时后才返回 —— 这正是它存在的意义.
	Decide(req abi.DecisionRequest) (abi.DecisionResolution, error)
	// Can 能力预检. **不是安全边界** —— 真强制在内核, 进程调不调都逃不掉
	Can(axis, scope string) bool
	// Spend 记一笔消耗. 超止损线返回 ErrBudgetExceeded
	Spend(delta abi.BudgetDelta) error
	// Infer 请求一次推理. 供应商和记账都在 OS 手里 ——
	// 进程拿不到凭据, 也躲不掉止损线
	Infer(p abi.InferParams) (abi.InferResult, error)
	// EmitLive 只给此刻正看着的人, **不进账本** —— 见 abi.EvProcDelta
	EmitLive(payload any)
	// InferStream 流式推理. 供应商不支持就退回 Infer, 不假装成流
	InferStream(p abi.InferParams, onDelta func(engine.StreamDelta)) (abi.InferResult, error)
	// Recv 等下一句用户输入. **会阻塞**, 期间进程转 waiting 不占计算.
	//
	// 这是"一次对话 = 一个活着的进程"的关键: 有了它, 多轮记忆和
	// 跨轮前缀缓存都是自然结果, 而不是要额外搬运的东西.
	Recv() (abi.RecvResult, error)
	// TryRecv 看一眼有没有新话, 不等. ok=false = 现在没有.
	// 长任务跑到一半时用户插话, 靠它才听得见
	TryRecv() (abi.RecvResult, bool)
	// Done 被 OS 要求退出时关闭 (止损线到 / 管理员终止 / 关机)
	Done() <-chan struct{}
}

// Body 进程"是什么".
//
// ExecBody 是唯一的生产路径: 一个**真正的 OS 进程**, 被内核约束.
// 它不调 Can() 也做不到没授权的事 —— 能力是**强制**的, 不是问询的.
//
// InprocBody 是同一个进程里的闭包. 跑得快好调试, 但**内核管不着它**.
// 因此只允许在 dev 模式使用.
type Body interface{ isBody() }

type ExecBody struct {
	Argv []string
	Cwd  string
	Env  map[string]string
}

type InprocBody struct {
	Entry func(ctx context.Context, pc ProcessContext) (any, error)
}

func (ExecBody) isBody()   {}
func (InprocBody) isBody() {}

// Child 子进程句柄 —— 抽掉 os/exec 的形状, 测试可注入假的.
//
// 测试**绝不能真的 spawn 系统二进制**: 慢、不可移植, 而且在受限环境里
// 可能终止测试进程树. 命令行的正确性由 confine 的纯函数测试锁.
type Child interface {
	OnLine(func(string))
	Wait() (code int, signal string, err error)
	Kill(sig string)
}

type Spawner func(argv []string, cwd string, env map[string]string) (Child, error)

type Options struct {
	Now func() int64
	// Mode **缺省是 confined** —— 缺省必须是安全的那个
	Mode       abi.Mode
	VolumeRoot string
	RuntimeFS  []string
	Probe      func([]abi.Requirement) abi.EnforcementProbe
	Spawner    Spawner
	Paths      confine.LauncherPaths
	// Provider 模型供应商. **只有 OS 持有凭据** —— agent 拿不到 key,
	// 也就不需要出网能力. nil 表示这台机器不提供推理服务.
	Provider engine.Provider
	// Searcher 搜索供应商. nil 表示这台机器不提供搜索 ——
	// **nil 要一路传到工具表**: 摆一个配不出结果的工具, 模型会照着调、
	// 拿到一句"没配搜索", 而它已经对用户许过诺了
	Searcher engine.Searcher
	// Viewer 看图服务. nil 表示这台机器不认图 —— 同 Searcher,
	// nil 要一路传到工具表
	Viewer engine.Viewer
	// EventStore 事件日志落盘. nil = 不落盘 (进程一死对话就没了).
	//
	// 落的是**给人看的展示流**, 工具结果在里面是截断的 ——
	// 它够重建"说过什么、做过什么", 不够重建上下文原文.
	// 想完整恢复上下文要落的是页表, 不是把这里的截断长度调大.
	EventStore *EventStore
	// ABISocketDir ABI socket 放哪. 缺省 /run/neox-os.
	// exec 进程靠它跟 OS 说话 —— 那是它跟外界唯一的通道.
	// 注意 sun_path 上限 (104/108 字节), 所以必须是短路径.
	ABISocketDir string
}

type procRecord struct {
	info abi.ProcessInfo
	// 进入 running 的时刻, 用于累计 ActiveMs. 非 running 时为 0
	runningSince int64
	spent        abi.BudgetSpent
	cancel       context.CancelFunc
	ctx          context.Context
	token        string
	// inbox 待命时收用户输入用. 懒建 —— 大多数进程根本不待命
	inbox *inbox
}

type OS struct {
	// running 还在跑的进程体.
	//
	//	**关机要等它们收拾完**: 进程可以注册收尾(收掉自己起的后台进程、
	//	落一笔账). 不等的话那些收尾一个都跑不到 —— 而进程注册它们的时候
	//	必须让它们运行: 否则后台服务会存活到下一次启动, 一直占着端口.
	running sync.WaitGroup
	// providerMu 护着 provider —— 设置页能在运行中换它
	providerMu sync.RWMutex
	mu         sync.Mutex
	procs      map[abi.ProcessID]*procRecord
	// decisionOwner did → pid 索引.
	// 纯派生数据: 完全可以从事件日志重放得出. 放这里只为 O(1) 查找 ——
	// 遍历所有进程的全部日志是 O(进程数 × 日志长度), 长跑后明显变慢.
	decisionOwner map[abi.DecisionID]abi.ProcessID
	tokens        map[string]abi.ProcessID
	counter       int
	// pidPrefix 本实例的进程号前缀.
	//
	// **进程号必须整台机器唯一, 不只是单实例内唯一.**
	//
	// 原来是裸的 p1/p2/…, 单实例没问题, 但 socket 路径和事件账本
	// 都是**整台机器共享的**. 同时运行三个实例时:
	//   · 三个实例都想要 /run/neox-os/p1.sock —— 后两个进程直接
	//     起不来, 退出码 2
	//   · 三份 p1 的事件写进同一个账本, seq 乱成一团, 历史串味
	//
	// 前缀 = 宿主 PID + 进程内实例序号:
	//
	//	宿主 PID    跨进程唯一 —— 同一时刻不可能有两个宿主用同一个 PID
	//	实例序号    进程内唯一 —— 一个宿主里起两个 OS 也不会撞
	//
	// 只用宿主 PID 是不够的, 单测当场抓到: 同进程里的两个 OS 实例
	// 前缀相同, 照样分到同一个号.
	//
	// (宿主重启后 PID 可能被系统复用, 但那时旧实例早已不在、也不会再
	// 往账本里写 —— 那个号不会被两个活着的实例同时用.)
	pidPrefix string
	// threadGrants 用户批准过的额外能力, 挂在**对话**上.
	// 进程会死, 对话不死 —— 批准的是"让它做这件事", 不是"让这个进程做".
	threadGrants map[string][]abi.Capability
	/**
	 * netOpen 出网对**所有活着的进程**放开 —— "都不问"档的即时生效开关.
	 *
	 *	capsFor 只在 spawn 时算一次, 于是用户切到"都不问"之后, 已经跑着
	 *	的 bot 照样撞出网墙 —— 他看到的是"我都放开了还不行". 能力集
	 *	改不了(landlock 只能收紧), 但预检(Can)是软的: 这个开关让它对
	 *	net 轴直接放行. 切回去即关, 不留痕于任何进程的能力集.
	 *
	 *	用 atomic.Bool: 读在每次工具预检上, 写在设置页 —— 别的锁不该
	 *	为它多持一毫秒.
	 */
	netOpen atomic.Bool
	// decisionScope 每条决策请求针对的路径 —— 批准时要知道授的是什么
	decisionScope map[abi.DecisionID]string
	// decisionAxis 那条决策授的是哪条能力轴 —— 只有 scope 的话
	// `registry.npmjs.org` 会被当成一个路径去授写权限
	decisionAxis map[abi.DecisionID]abi.CapAxis

	events    *EventLog
	decisions *DecisionRegistry
	now       func() int64
	mode      abi.Mode
	volume    string
	runtimeFS []string
	probe     func([]abi.Requirement) abi.EnforcementProbe
	spawner   Spawner
	paths     confine.LauncherPaths
	provider  engine.Provider
	searcher  engine.Searcher
	viewer    engine.Viewer
	sockDir   string
	// abiServers 每个 exec 进程一个, 终态时关掉
	abiServers map[abi.ProcessID]*AbiServer
}

// instanceSeq 进程内的 OS 实例序号 —— 让同一个宿主里的多个实例也不撞
var instanceSeq atomic.Int64

func New(opts Options) *OS {
	if opts.Now == nil {
		opts.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if opts.Mode == "" {
		opts.Mode = abi.ModeConfined
	}
	if opts.VolumeRoot == "" {
		opts.VolumeRoot = "/work"
	}
	if opts.RuntimeFS == nil {
		opts.RuntimeFS = confine.ResolveRuntimeFS(func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		})
	}
	if opts.Probe == nil {
		opts.Probe = func(r []abi.Requirement) abi.EnforcementProbe {
			return confine.Probe(r, confine.RealDeps())
		}
	}
	if opts.Spawner == nil {
		opts.Spawner = realSpawner
	}
	if opts.Paths == (confine.LauncherPaths{}) {
		opts.Paths = confine.DefaultPaths
	}
	if opts.ABISocketDir == "" {
		opts.ABISocketDir = "/run/neox-os"
	}
	// **开机扫掉死 socket.**
	//
	// 每起一个进程留一个 <pid>.sock. 正常退出时 AbiServer.Close 会删,
	// 但宿主被杀/崩溃/断电时那个 defer 根本没机会跑 —— 而那恰恰是
	// 长运行中一定会发生, 残留数量可达到 200 个.
	//
	// 判据是"连不连得上", 不是文件名或时间: 连得上的一个都不碰,
	// 所以同一台机器上跑着的另一个实例不会被误伤(见 SweepStaleSockets).
	if n, live := SweepStaleSockets(opts.ABISocketDir); n > 0 {
		fmt.Printf("清掉 %d 个残留的 socket(上次没干净地退出), %d 个还活着\n", n, live)
	}

	o := &OS{
		procs:         map[abi.ProcessID]*procRecord{},
		decisionOwner: map[abi.DecisionID]abi.ProcessID{},
		tokens:        map[string]abi.ProcessID{},
		events:        NewEventLog(opts.Now),
		now:           opts.Now,
		mode:          opts.Mode,
		volume:        opts.VolumeRoot,
		runtimeFS:     opts.RuntimeFS,
		probe:         opts.Probe,
		spawner:       opts.Spawner,
		paths:         opts.Paths,
		provider:      opts.Provider,
		searcher:      opts.Searcher,
		viewer:        opts.Viewer,
		sockDir:       opts.ABISocketDir,
		abiServers:    map[abi.ProcessID]*AbiServer{},
		pidPrefix:     fmt.Sprintf("p%d.%d.", os.Getpid(), instanceSeq.Add(1)),
		threadGrants:  map[string][]abi.Capability{},
		decisionScope: map[abi.DecisionID]string{},
		decisionAxis:  map[abi.DecisionID]abi.CapAxis{},
	}
	// 扫掉上次留下的空 cgroup 叶子.
	//
	// 进程退出时会自己删, 但那依赖时序对得上 —— 僵尸还没收时 rmdir 被拒,
	// 一旦某次没删成, 那个目录就会一直留着; 可出现 18 个空叶子、零个活进程.
	//
	// 放在 OS 启动里而不是 boot 里: cgroup 是 **OS 管的资源**,
	// 而不是所有宿主都走 boot (neox-chat 就不走), 放 boot 里等于漏掉一半入口.
	confine.SweepOrphanCgroups(opts.Paths.CgroupRoot + "/neox-os")
	// 网络命名空间同理, 而且是同一个错犯第二次: Teardown 挂在 defer 上,
	// 而 defer 只在正常退出时跑 —— 进程被 kill 就留下一个 netns 和一根
	// veth, 永远没人再收; 可留下 3 个残留.
	//
	// **退出时清理只是优化, 启动时清扫才是保证.** 两个子系统必须同一套.
	confine.SweepOrphanNetNamespaces(opts.Paths.IP)

	if opts.EventStore != nil {
		// 落盘要在事件产生的那一刻做, 而不是退出时统一写 ——
		// 退出时统一写等于"崩溃就全丢", 那正是要防的场景.
		//
		// **原来这里是 SubscribeAll, 而订阅根本不是"那一刻"**:
		// Append 只把事件塞进队列就返回了, 另一个协程才写文件.
		// 被杀时队尾那几条没落地, 丢的恰恰是刚发生的操作.
		// 异步订阅可能让三次启动中只有一次的 flushed 事件落盘.
		o.events.WriteThrough(opts.EventStore)
	}
	o.decisions = NewDecisionRegistry(opts.Now, DecisionHooks{
		OnRequested: func(p abi.PendingDecision) {
			o.mu.Lock()
			o.decisionOwner[p.DID] = p.PID
			o.decisionScope[p.DID] = p.Request.Scope
			o.decisionAxis[p.DID] = p.Request.Axis
			o.mu.Unlock()
			o.events.Append(p.PID, abi.EvDecideRequest, map[string]any{
				"did": p.DID, "present": p.Request.Present,
				"urgency": urgencyOr(p.Request.Urgency),
			})
			// 进程在等人 → 不占计算. 触达层从事件流里看到这条, 决定要不要打断人
			o.setState(p.PID, abi.StateWaiting)
		},
		OnResolved: func(r abi.DecisionResolution) {
			o.mu.Lock()
			pid, ok := o.decisionOwner[r.DID]
			o.mu.Unlock()
			if !ok {
				return
			}
			// 批准了就把能力记进**这段对话**的授权表.
			//
			// ── 为什么批准不能当场生效 ──
			//
			// landlock 的规则在进程启动时就定死, 而且语义是**只能收紧、
			// 不能放宽** —— 运行中的进程没法扩权. 所以批准之后那次调用
			// 照样被内核拒: 批准后 agent 仍会拿到"权限被拒".
			// **已经批准、仍然做不到** —— 那审批就是假的.
			//
			// 真解是让**下一个**进程带上这个能力. 我们本来就有
			// "对话跨进程存活"(thread), 所以授权挂在对话上:
			// 用户批准 → 记账 → 下次说话时新进程带着扩大后的能力集起来.
			if r.Choice == "yes" {
				o.grantFromDecision(pid, r.DID)
			}
			o.events.Append(pid, abi.EvDecideResolved, map[string]any{
				"did": r.DID, "choice": r.Choice, "by": r.By, "values": r.Values,
			})
			o.setState(pid, abi.StateRunning)
		},
	})
	return o
}

func urgencyOr(u string) string {
	if u == "" {
		return "normal"
	}
	return u
}

func (o *OS) Mode() abi.Mode { return o.mode }

// Provider 当前的推理供应商.
//
//	**要加锁**: 设置页可以在运行中换掉它, 而进程随时可能正在读.
//	不加锁的话换 provider 会变成一个偶发的数据竞争 —— 而它的症状是
//	"偶尔一次推理用了旧 key", 几乎查不出来.
func (o *OS) Provider() engine.Provider {
	o.providerMu.RLock()
	defer o.providerMu.RUnlock()
	return o.provider
}

// SetViewer 运行中换看图的模型.
//
//	nil = 这台机器不认图 —— **nil 是一个有意义的答案**: 调用方据此
//	不给 agent 挂 view_image 工具, 而 bot 收到图时会直接说"我看不了",
//	不去猜图里是什么.
func (o *OS) SetViewer(v engine.Viewer) {
	o.providerMu.Lock()
	o.viewer = v
	o.providerMu.Unlock()
}

// SetProvider 运行中换供应商. 换完立刻生效, 不影响已经在跑的那次请求.
func (o *OS) SetProvider(p engine.Provider) {
	o.providerMu.Lock()
	o.provider = p
	o.providerMu.Unlock()
}
func (o *OS) Searcher() engine.Searcher { return o.searcher }
func (o *OS) Viewer() engine.Viewer {
	// 跟 provider 同一把锁: 设置页改一次会同时换掉这两样
	o.providerMu.Lock()
	defer o.providerMu.Unlock()
	return o.viewer
}
func (o *OS) Log() *EventLog { return o.events }

// SetNetOpen "都不问"档的即时生效开关 —— 见字段说明.
// true = 出网预检对所有活着的进程放行, 不改任何进程的能力集.
func (o *OS) SetNetOpen(open bool)         { o.netOpen.Store(open) }
func (o *OS) ABIVersion() string           { return abi.Version }
func (o *OS) Decisions() *DecisionRegistry { return o.decisions }

// Spawn 起一个进程.
//
// Fail-closed 闸在分配 pid **之前** —— 不合法的组合根本不存在:
//
//	confined + exec   → 唯一的生产路径. 内核必须真能强制, 否则拒绝
//	dev      + inproc → 开发路径. 约束退化为记账, 只能跑闭包
//	confined + inproc → 拒. 闭包在 OS 自己的堆里, 内核管不着它
//	dev      + exec   → 拒. 那会是一个**没有任何约束的真进程**,
//	                    比什么都不做更危险. 不给这条路.
func (o *OS) Spawn(spec abi.ProcessSpec, body Body) (abi.ProcessID, error) {
	switch b := body.(type) {
	case InprocBody:
		if o.mode == abi.ModeConfined {
			return "", fmt.Errorf("%w: confined 模式不接受 inproc 进程 —— "+
				"同堆闭包不受内核约束, 能力检查会形同虚设", ErrModeMismatch)
		}
	case ExecBody:
		if o.mode == abi.ModeDev {
			return "", fmt.Errorf("%w: dev 模式不接受 exec 进程 —— "+
				"那会起一个完全不受约束的真进程", ErrModeMismatch)
		}
		plan, err := o.planFor(spec)
		if err != nil {
			return "", err
		}
		probe := o.probe(plan.Requires)
		// 内核拦不住 → 不降级、不 warning 后继续, 直接拒绝启动
		if !probe.Usable {
			return "", fmt.Errorf("%w: %s", ErrConfineUnusable, probe.Reason)
		}
		_ = b
	default:
		return "", fmt.Errorf("%w: 未知进程体", ErrModeMismatch)
	}

	o.mu.Lock()
	o.counter++
	pid := abi.ProcessID(fmt.Sprintf("%s%d", o.pidPrefix, o.counter))
	at := o.now()
	ctx, cancel := context.WithCancel(context.Background())
	rec := &procRecord{
		info: abi.ProcessInfo{
			PID: pid, Spec: spec, State: abi.StateCreated,
			CreatedAt: at, ChangedAt: at,
		},
		ctx: ctx, cancel: cancel, token: mintToken(),
	}
	o.procs[pid] = rec
	o.tokens[rec.token] = pid
	o.mu.Unlock()

	// 创建事件带上 labels —— 进程是**用什么身份创建的**必须留痕.
	// thread 就靠它跨进程存活: 进程会死, 对话不死.
	created := map[string]any{"state": abi.StateCreated}
	if len(spec.Labels) > 0 {
		created["labels"] = spec.Labels
	}
	/**
	 * **它在哪儿干活也要留痕**.
	 *
	 *	工作区就是写轴那条能力的 scope —— 进程还活着时从能力集读得到,
	 *	进程一死就**没有任何地方能回答**"这个 bot 当时在哪儿动的手".
	 *	而账本本来就是为了回答这类问题存在的.
	 *
	 *	界面也吃这一口: 工具行里工作区内的路径写成相对的(见 inWorkspace),
	 *	靠的就是"这条事件属于哪个工作区". 历史里没有它, 一整段旧对话
	 *	就只能摊着一堆逐字相同的绝对路径.
	 */
	for _, c := range spec.Caps {
		if c.Axis == abi.AxisWrite && c.Scope != "" {
			created["work"] = c.Scope
			break
		}
	}
	o.events.Append(pid, abi.EvProcState, created)
	// 立刻转 running 并异步执行. Spawn 不等进程跑完 ——
	// 调用方拿到 pid 就可以走人, 这正是"不需要观众"的入口.
	o.setState(pid, abi.StateRunning)
	// **先加计数再起 goroutine**: 反过来的话, 关机正好卡在这两句之间时
	// Wait 会以为没人在跑, 直接走人
	o.running.Add(1)
	go o.run(pid, body)
	return pid, nil
}

func (o *OS) planFor(spec abi.ProcessSpec) (abi.ConfinementPlan, error) {
	// 把这段对话攒下的批准并进能力集.
	//
	// 用户批准过的东西, 下一个进程就该带着它起来 —— 否则每次说话
	// 都要重新问一遍同一件事, 而且**批准本身在当前进程里是无效的**
	// (landlock 规则启动时定死, 只能收紧不能放宽).
	caps := spec.Caps
	if extra := o.grantsFor(spec.Labels["thread"]); len(extra) > 0 {
		caps = append(append([]abi.Capability(nil), caps...), extra...)
	}
	return confine.PlanFor(caps, confine.PlanOptions{
		VolumeRoot: o.volume, RuntimeFS: o.runtimeFS,
	})
}

func (o *OS) Info(pid abi.ProcessID) (abi.ProcessInfo, bool) {
	o.mu.Lock()
	rec, ok := o.procs[pid]
	if !ok {
		o.mu.Unlock()
		return abi.ProcessInfo{}, false
	}
	info := rec.info
	o.mu.Unlock()
	info.LogLength = o.events.Length(pid)
	return info, true
}

// List 进程表快照.
//
//	**顺序必须稳定**. 早先这里直接遍历 map 就返回 —— Go 的 map 迭代
//	是随机的, 于是每调一次顺序都不一样. 单看后端没症状, 接上界面就露馅:
//	客户端每几秒拉一次进程表, 侧栏就每几秒重排一次, 用户正要点的那一项
//	会在手指落下前换掉位置.
//
//	按创建时间排, 时间相同再按 pid —— 后者是为了同一毫秒内起的几个进程
//	也有确定顺序, 否则随机只是从"每次都乱"变成"偶尔乱", 更难查.
func (o *OS) List() []abi.ProcessInfo {
	o.mu.Lock()
	out := make([]abi.ProcessInfo, 0, len(o.procs))
	for _, rec := range o.procs {
		out = append(out, rec.info)
	}
	o.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].PID < out[j].PID
	})
	for i := range out {
		out[i].LogLength = o.events.Length(out[i].PID)
	}
	return out
}

func (o *OS) Kill(pid abi.ProcessID, reason string) {
	o.mu.Lock()
	rec, ok := o.procs[pid]
	if !ok || rec.info.State.IsTerminal() {
		o.mu.Unlock()
		return
	}
	cancel := rec.cancel
	o.mu.Unlock()
	cancel()
}

/**
 * Drop 把这条**进程记录**也抹掉.
 *
 *	Kill 只是让它停下来 —— 记录还在进程表里, /processes 照样返回它.
 *	删一段对话时只 Kill 不 Drop 的症状: 界面上那条会话没了, 几秒之后
 *	轮询把进程表拉回来, 它**自己长回来**, 显示成"不在".
 *	用户看到的是"删了个寂寞", 而账本那边其实真删干净了.
 *
 *	只抹记录, 不碰日志 —— 日志归 Log().Forget. 两件事分开是因为
 *	"这个进程不在了"和"这段历史不要了"本来就不是同一个决定.
 */
func (o *OS) Drop(pid abi.ProcessID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if rec, ok := o.procs[pid]; ok {
		delete(o.tokens, rec.token)
		delete(o.procs, pid)
	}
}

// TokenFor 取某进程的 ABI token —— 只给 spawn 路径用, 注入进程 env
func (o *OS) TokenFor(pid abi.ProcessID) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, ok := o.procs[pid]
	if !ok {
		return "", false
	}
	return rec.token, true
}

// ResolveToken token → 该进程的 ProcessContext. ABI 服务用.
// 进程终态后 token 立即失效.
func (o *OS) ResolveToken(token string) (abi.ProcessID, ProcessContext, bool) {
	o.mu.Lock()
	pid, ok := o.tokens[token]
	if !ok {
		o.mu.Unlock()
		return "", nil, false
	}
	rec := o.procs[pid]
	if rec == nil || rec.info.State.IsTerminal() {
		o.mu.Unlock()
		return "", nil, false
	}
	o.mu.Unlock()
	return pid, o.contextFor(pid), true
}

// RebuildIndex 从事件日志重建派生索引 —— 恢复/重挂时调用.
// 索引永远不是真相源.
func (o *OS) RebuildIndex() {
	o.mu.Lock()
	pids := make([]abi.ProcessID, 0, len(o.procs))
	for pid := range o.procs {
		pids = append(pids, pid)
	}
	o.decisionOwner = map[abi.DecisionID]abi.ProcessID{}
	o.mu.Unlock()

	for _, pid := range pids {
		for _, e := range o.events.Replay(pid, 0) {
			if e.Kind != abi.EvDecideRequest {
				continue
			}
			if m, ok := e.Payload.(map[string]any); ok {
				if did, ok := m["did"].(string); ok {
					o.mu.Lock()
					o.decisionOwner[did] = pid
					o.mu.Unlock()
				}
			}
		}
	}
}

func (o *OS) setState(pid abi.ProcessID, state abi.ProcessState) {
	o.mu.Lock()
	rec, ok := o.procs[pid]
	if !ok || rec.info.State == state {
		o.mu.Unlock()
		return
	}
	now := o.now()
	// 离开 running 时结算活跃时长 —— waiting/suspended 不计入止损线.
	// 这是"等一天和等一秒成本相同"的实现点.
	if rec.runningSince != 0 {
		rec.spent.ActiveMs += now - rec.runningSince
		rec.runningSince = 0
	}
	if state == abi.StateRunning {
		rec.runningSince = now
	}
	rec.info.State = state
	rec.info.ChangedAt = now
	o.mu.Unlock()

	o.events.Append(pid, abi.EvProcState, map[string]any{"state": state})
}

/**
 * recordOf 取这个进程的记录. 第二个返回值 false = **它已经不在了**.
 *
 *	以前这几处是 `o.procs[pid].xxx` 直接取. 那在 Drop 出现之前一直成立
 *	(记录只增不删), 而 Drop 让它随时可能不在 —— 取到 nil 当场 panic,
 *	而且是在 body 那个 goroutine 里, 整个 OS 跟着倒.
 *
 *	收成一处是为了**下次加字段时不会又漏一个**: 散在各处的裸下标,
 *	每加一个新的删除路径就要重新数一遍.
 */
func (o *OS) recordOf(pid abi.ProcessID) (*procRecord, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, ok := o.procs[pid]
	return rec, ok
}

func (o *OS) run(pid abi.ProcessID, body Body) {
	defer o.running.Done()
	value, err, dropped := o.execBody(pid, body)
	if dropped {
		return
	}

	outcome := abi.ProcessOutcome{OK: err == nil, Value: value}
	state := abi.StateExited
	if err != nil {
		code := "error"
		if errors.Is(err, ErrBudgetExceeded) {
			code = "budget_exceeded"
		}
		outcome = abi.ProcessOutcome{OK: false,
			Error: &abi.OutcomeError{Code: code, Message: err.Error()}}
		state = abi.StateFailed
	}

	o.mu.Lock()
	if rec, ok := o.procs[pid]; ok {
		rec.info.Outcome = &outcome
		delete(o.tokens, rec.token) // 终态后 token 立即失效
	}
	o.mu.Unlock()

	o.setState(pid, state)
	o.events.Append(pid, abi.EvProcOutcome, outcome)
}

// execBody 跑进程体. dropped=true 表示记录已经被抹掉, 不要再写终态.
//
//	**panic 必须就地接住**. in-proc 的 bot 跟 OS 共一个进程: 它崩一次
//	原来会把整台机器带走, 客户端看起来就是"过一会儿所有人离线,
//	只有重启才能说话". 一个人出事不该连坐所有人.
func (o *OS) execBody(pid abi.ProcessID, body Body) (value any, err error, dropped bool) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("进程崩了: %v", rec)
			dropped = false
		}
	}()
	switch b := body.(type) {
	case InprocBody:
		rec, alive := o.recordOf(pid)
		/**
		 * **记录被抹掉了就收工**, 不要硬取.
		 *
		 *	Drop 会在进程还没死透的时候把记录抹掉(用户删了这段对话:
		 *	先 Kill 再 Drop, 而 Kill 是异步的). 原来这里是
		 *	`o.procs[pid].ctx` —— 取到 nil 就当场 panic, 而且是在
		 *	**另一个 goroutine 里**, 整个 OS 跟着倒.
		 *
		 *	一条被删掉的对话不该有能力弄死这台机器.
		 */
		if !alive {
			return nil, nil, true
		}
		value, err = b.Entry(rec.ctx, o.contextFor(pid))
		return value, err, false
	case ExecBody:
		value, err = o.runExec(pid, b)
		return value, err, false
	}
	return nil, nil, false
}

// runExec 起一个真正被内核约束的 OS 进程.
//
// 能力已经在 Spawn 时翻译成规则并验证过内核支持, 这里只负责拼命令行、
// 起进程、等退出码. 进程调不调 Can() 无关紧要 —— 它**做不到**没授权的事.
func (o *OS) runExec(pid abi.ProcessID, b ExecBody) (any, error) {
	rec, alive := o.recordOf(pid)
	// 记录没了 = 这段对话被删掉了 —— 收工, 不要硬取(见 recordOf)
	if !alive {
		return nil, fmt.Errorf("这个进程已经被删掉了")
	}
	spec := rec.info.Spec
	ctx := rec.ctx

	plan, err := o.planFor(spec)
	if err != nil {
		return nil, err
	}

	// 能力集里的路径必须先落实到文件系统 —— landlock 装规则要求路径存在,
	// 而进程没有能力创建它们. 卷是 OS 拥有的, 所以这是 OS 的活.
	dropped := confine.PrepareVolume(&plan,
		func(p string) error { return os.MkdirAll(p, 0o755) },
		func(p string) bool { _, err := os.Stat(p); return err == nil })
	if len(dropped) > 0 {
		// **摘掉了什么必须说出来** —— 静默摘除会让人以为授权生效了
		o.events.Append(pid, abi.EvCapDenied, map[string]any{
			"reason": "路径不存在, 已从规则里摘掉", "paths": dropped,
		})
	}

	// cgroup 必须在 spawn **之前**建好并写好限额 ——
	// 进程启动时第一件事就是把自己写进 cgroup.procs, 目录不存在就直接失败.
	slice := "neox-os/" + string(pid)
	if err := confine.EnsureCgroup(o.paths.CgroupRoot, slice, plan.Cgroup); err != nil {
		return nil, fmt.Errorf("准备 cgroup 失败: %w", err)
	}
	defer confine.RemoveCgroup(o.paths.CgroupRoot, slice)

	// 先把 ABI 服务起起来 —— socket 文件必须在 landlock 装规则**之前**存在,
	// 否则规则里那条路径不存在, landlock-run 会直接报错退出.
	sock := fmt.Sprintf("%s/%s.sock", o.sockDir, pid)
	token, _ := o.TokenFor(pid)
	srv, err := NewAbiServer(sock, o.ResolveToken, nil)
	if err != nil {
		return nil, err
	}
	srv.WithOS(o)
	// 接续哪段对话由 spec 说了算, 进程无权自选.
	// 空标签 = 全新对话, 那是常态.
	//
	// 按 **thread** 取而不是按 pid: 一段对话可能横跨好几个进程
	// (每次接续都是一个新进程). 只取一个 pid 的事件, 接续之后
	// 再接一次就只剩最后那一小段, 前面聊的全丢.
	if th := spec.Labels["thread"]; th != "" {
		srv.resumeEvents = o.threadEvents(th)
	} else if prev := spec.Labels["resume"]; prev != "" {
		srv.resumeEvents = o.events.Replay(prev, 0)
	}
	if err := srv.Listen(); err != nil {
		return nil, fmt.Errorf("起 ABI 服务失败: %w", err)
	}
	o.mu.Lock()
	o.abiServers[pid] = srv
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.abiServers, pid)
		o.mu.Unlock()
		_ = srv.Close()
	}()

	// ── 出网 ──
	//
	// 两条路都进**具名**命名空间, 区别只在里面有没有那根 veth:
	//
	//	有授权  ns + veth + 地址, 对端是 OS 的代理; 没有默认路由也没有 DNS
	//	没授权  ns 里只有 lo —— 出不去, 但自己连得上自己
	//
	// 没授权那条原来是 `unshare --net` 的匿名 ns. 那里面 **lo 是 DOWN 的**
	// (内核默认, 新 ns 一律如此), 于是 127.0.0.1 谁也连不上, 而"起个服务
	// 再打它验证"正是 agent 验证自己工作的主要手段. 回环出不了这个
	// 命名空间, 关着它不带来任何安全, 只是把那一整类活废掉.
	//
	// 建不出来就**直接失败**, 不降级成"断网跑着先". 降级的后果是
	// 用户授了权、agent 以为自己能连, 然后在超时里打转 ——
	// 而真正的原因(网没配起来)一个字都没出现在任何地方.
	var netns *confine.NetNamespace
	var proxyAddr string
	if len(plan.Net) == 0 {
		// 只有回环. 建不出来(比如这台机器没装 iproute2)就退回匿名 ns ——
		// 那是原来的行为, 约束一点没少, 只是 lo 仍然是 down.
		// **不能因此拒绝启动**: 这不是强制机制, 只是环境完整性;
		// 为它拒绝启动等于让没装 iproute2 的机器整个跑不了.
		if ns, nerr := confine.SetupLoopbackOnlyNamespace(string(pid), o.paths.IP); nerr == nil {
			netns = ns
			defer netns.Teardown(o.paths.IP)
		} else {
			// 静默降级是不行的 —— 之后"服务起来了连不上"会查不到根.
			o.events.Append(pid, abi.EvProcOutput, map[string]any{
				"phase": "netns_degraded", "err": nerr.Error(),
				"msg": "建不出只有回环的命名空间, 退回匿名断网 ns —— " +
					"127.0.0.1 在里面连不上, 起本地服务验证会失败",
			})
		}
	} else {
		ns, nerr := confine.SetupNetNamespace(string(pid), o.paths.IP)
		if nerr != nil {
			return nil, fmt.Errorf("能力集里有出网授权但网络配不出来: %w", nerr)
		}
		netns = ns
		defer netns.Teardown(o.paths.IP)

		// 现查, 不用 plan.Net 那份快照 —— 用户中途批的域名要立刻生效
		px, perr := startNetProxy(pid, ns.ProxyAddr(), func() []abi.NetRule {
			live, lerr := o.planFor(spec)
			if lerr != nil {
				// 算不出来就退回启动时那份 —— 只会更严, 不会更松
				return plan.Net
			}
			return live.Net
		}, func(host string) {
			// 被挡下来的目标要记账 —— 用户日后要能回答
			// "它到底想连哪儿", 而不是只看到一堆 403
			o.events.Append(pid, abi.EvCapDenied, map[string]any{
				"axis": string(abi.AxisNet), "scope": host,
				"reason": "不在出网授权里",
			})
		})
		if perr != nil {
			return nil, perr
		}
		defer px.Close()
		proxyAddr = ns.ProxyAddr()
	}

	launch, err := confine.BuildLaunch(plan,
		confine.Body{Argv: b.Argv, Cwd: b.Cwd, Env: b.Env},
		confine.LaunchContext{
			PID:         string(pid),
			CgroupSlice: slice,
			ABISocket:   sock,
			ABIToken:    token,
			NetNS:       nsName(netns),
			ProxyAddr:   proxyAddr,
		}, o.paths)
	if err != nil {
		return nil, err
	}

	// 审计记命令行, 但**不记 env** —— env 里有一次性 token,
	// 日志是要给人看、要长期留的, 凭据不该进去.
	o.events.Append(pid, abi.EvCapUsed, map[string]any{
		"confinement": plan, "argv": launch.Argv,
	})

	child, err := o.spawner(launch.Argv, b.Cwd, launch.Env)
	if err != nil {
		return nil, err
	}
	// 进程的 stdout 是结构化输出通道, 一行一条 —— 不是"给人看的日志"
	child.OnLine(func(line string) {
		if line != "" {
			o.events.Append(pid, abi.EvProcOutput, map[string]any{"line": line})
		}
	})
	go func() {
		<-ctx.Done()
		child.Kill("SIGTERM")
	}()

	code, sig, err := child.Wait()
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("进程退出: code=%d signal=%s", code, sig)
	}
	return map[string]any{"exitCode": 0}, nil
}

// Shutdown 关机 —— 停所有进程, 清定时器
// shutdownGrace 关机时给进程收拾的时间.
//
// 三秒: 收尾该做的事(杀几个进程组、写一行日志)是毫秒级的; 要是三秒还没完,
// 那多半是卡住了, 再等也等不出结果.
const shutdownGrace = 3 * time.Second

func (o *OS) Shutdown(reason string) {
	o.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(o.procs))
	for _, rec := range o.procs {
		if !rec.info.State.IsTerminal() {
			cancels = append(cancels, rec.cancel)
		}
	}
	o.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	o.decisions.Dispose()

	/**
	 * **等进程收拾完**, 但只等一小会儿.
	 *
	 *	不等: 进程注册的收尾(收掉自己起的后台服务、落最后一笔账)一个都
		 *	跑不到 —— 而它们注册的时候必须执行. 后台服务活过关机后会一直
		 *	占着端口, 下次验证撞上"端口被占",
	 *	而那个错跟真正的原因隔着好几层.
	 *
	 *	不设上限也不行: 一个卡住的进程能把关机拖到永远, 而用户点的是
	 *	"退出". 所以给一个宽限期, 到点就走 —— **并且把这件事说出来**,
	 *	不静默放弃.
	 */
	done := make(chan struct{})
	go func() { o.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		o.events.Append(abi.ProcessID("os"), abi.EvProcOutput, map[string]any{
			"phase": "shutdown_slow",
			"msg": fmt.Sprintf("等进程收拾了 %s 还没完, 不等了 —— "+
				"它们起的后台进程可能留了下来", shutdownGrace)})
	}
}

// RestoreEvents 把落盘的历史装回内存日志.
//
// EventLog 早就有 Restore(), 注释写着"落盘用", 但**一直没有人调用它** ——
// 那就是"只支持不接入", 等于没有. 这里才算真正接上.
// grantFromDecision 把一次批准记进这段对话的授权表.
//
// 记在**对话**上而不是进程上: 进程会死, 对话不死 —— 而用户批准的是
// "让它做这件事", 不是"让这个进程做这件事".
func (o *OS) grantFromDecision(pid abi.ProcessID, did abi.DecisionID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, ok := o.procs[pid]
	if !ok {
		return
	}
	// 一条决策可以授**多个目标** —— 见下面 splitScopes 的说明.
	scopes := splitScopes(o.decisionScope[did])
	axis := o.decisionAxis[did]
	if axis == "" {
		axis = abi.AxisWrite // 历史缺省: 早先只有写会走到审批
	}
	if len(scopes) == 0 {
		// 请求没带 scope —— 授权无从记起.
		// **这件事要说出来**: 否则用户批准了却毫无效果, 而账本里
		// 什么线索都没有, 排查只能猜测.
		o.events.Append(pid, abi.EvCapUsed, map[string]any{
			"granted": false, "did": did,
			"why": "决策请求没带 scope, 不知道该授什么 —— 批准不会生效"})
		return
	}
	thread := rec.info.Spec.Labels["thread"]
	if thread == "" {
		thread = string(pid) // 没有线头标签 = 它自己就是线头
	}
	have := map[string]bool{}
	for _, c := range o.threadGrants[thread] {
		if c.Axis == axis {
			have[c.Scope] = true
		}
	}
	var added []string
	for _, sc := range scopes {
		if have[sc] {
			continue // 已经授过
		}
		have[sc] = true
		added = append(added, sc)
		o.threadGrants[thread] = append(o.threadGrants[thread],
			abi.Capability{Axis: axis, Scope: sc})
	}
	if len(added) == 0 {
		return
	}
	o.events.Append(pid, abi.EvCapUsed, map[string]any{
		"granted": true, "thread": thread, "axis": string(axis), "scope": added})
}

// grantsFor 这段对话攒下的额外授权
func (o *OS) grantsFor(thread string) []abi.Capability {
	if thread == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]abi.Capability(nil), o.threadGrants[thread]...)
}

func (o *OS) RestoreEvents(snap map[abi.ProcessID][]abi.Event) {
	o.events.Restore(snap)

	// 装回来的未决决策一律判过期 —— 见 expireOrphanDecisions
	o.expireOrphanDecisions(snap)

	/**
	 * 给上一代进程收尸.
	 *
	 *	重启后同名 bot 是**新 pid**(spawn 只发新号), 账本里的旧 pid 从此
	 *	没有宿主. 但进程被硬杀(docker rm / kill -9)时来不及留终态 ——
	 *	账本里最后一眼还是 running/waiting, 于是每个客户端都把死人画成
		 *	活人, 甚至把话发给它: OS 只能回 ok:false, 界面上就是"发了没反应".
	 *
	 *	在这儿补一句"它退出了". 幂等: 补过之后下次装回看到的就是终态.
	 */
	for pid, events := range snap {
		last := abi.ProcessState("")
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind != abi.EvProcState {
				continue
			}
			if body, ok := events[i].Payload.(map[string]any); ok {
				if s, ok := body["state"].(string); ok {
					last = abi.ProcessState(s)
				}
			}
			break
		}
		if last.IsTerminal() {
			continue
		}
		o.events.Append(pid, abi.EvProcState, map[string]any{
			"state": string(abi.StateExited), "why": "上一代进程, 这次启动不再存在"})
	}

	// **pid 计数器要跳过账本里已经用掉的号.**
	//
	// 不跳的话重启后第一个进程又叫 p1, 它的新事件会追加到旧 p1 的流上 ——
	// 两段毫不相干的对话会在账本里混成一条: 恢复后全新对话再次
	// 拿到旧的进程号.
	//
	// 号只增不减: 复用一个死掉进程的号, 等于伪造它的历史.
	o.mu.Lock()
	defer o.mu.Unlock()
	// 只跳过**本实例**用过的号 —— 别的实例有自己的前缀, 天然不撞.
	for pid := range snap {
		var n int
		if _, err := fmt.Sscanf(string(pid), o.pidPrefix+"%d", &n); err == nil && n > o.counter {
			o.counter = n
		}
	}
}

// threadEvents 一段对话横跨的所有进程的事件, 按时间接起来.
//
// **进程身份 ≠ 对话身份.** pid 必须唯一(复用等于伪造历史),
// 而对话要能跨进程活下去 —— 每次接续都是一个新进程.
// 两者混成一个的症状: 接续之后清单里冒出一段"新对话",
// 其实是同一段的续集; 再接几次, 清单全是碎片, 用户认不出哪个是正主.
func (o *OS) threadEvents(thread string) []abi.Event {
	var out []abi.Event
	for _, pid := range o.threadMembers(thread) {
		out = append(out, o.events.Replay(pid, 0)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// threadMembers 属于这段对话的所有进程, 含线头自己
func (o *OS) threadMembers(thread string) []abi.ProcessID {
	members := []abi.ProcessID{abi.ProcessID(thread)}
	for pid, evs := range o.events.Snapshot() {
		if pid == abi.ProcessID(thread) {
			continue
		}
		if ThreadOf(evs) == thread {
			members = append(members, pid)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
	return members
}

// ThreadOf 这个进程属于哪段对话. 空 = 它自己就是线头.
func ThreadOf(evs []abi.Event) string {
	for _, ev := range evs {
		if ev.Kind != abi.EvProcState {
			continue
		}
		m, ok := ev.Payload.(map[string]any)
		if !ok {
			continue
		}
		labels, ok := m["labels"].(map[string]any)
		if !ok {
			// 从盘上读回来的是 map[string]any; 内存里是 map[string]string
			if ls, ok2 := m["labels"].(map[string]string); ok2 {
				return ls["thread"]
			}
			continue
		}
		if t, ok := labels["thread"].(string); ok {
			return t
		}
	}
	return ""
}

// splitScopes 一条决策可以授多个目标.
//
// ── 为什么一次操作该只打扰一次 ──
//
// 安装一个 Python 包会需要**两次审批**: 先 pypi.org(查包), 再
// files.pythonhosted.org(下载). 同一次操作被打断两次, 而这两次问的其实是
// 同一件事 —— "允许它装 tabulate 吗".
//
// 分层错了: 人关心的是**这次操作**, 系统展示的却是
// 技术细节(要连哪几个主机). 细节该看得见, 但不该拆成多次打扰.
//
// 所以 scope 收下"逗号/空格分隔的一串", 一次问、一次全授.
// **仍然逐个列给用户看** —— 批量不等于含糊.
func splitScopes(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';'
	}) {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// expireOrphanDecisions 把装回来的未决决策判过期. 返回判了几条.
//
// ── 为什么可以在这里断定它们死了 ──
//
// 决策活得比**进程**的任何一次运行都长 —— 那是这套东西的核心性质,
// 靠的是事件日志. 但等在那条 channel 上的 waiter 只活在**内存**里:
// 登记处是 OS 实例的一部分, 实例一换, 所有 waiter 连同它们要唤醒的进程
// 一起没了.
//
// 所以: 从账本里装回来的、没配上 resolved 的那些 did, **可以断定不会再有人接**.
// 不是猜, 是这条链的结构决定的.
//
// ── 不判会怎样 ──
//
// 界面照着账本渲染, 于是那张卡永远停在"等待决策". 请求它的进程已经
// 不存在时, Resolve 找不到 waiter, **什么都不会发生, 也不会有任何提示**.
// 那是最糟的一种失灵: 界面上一切正常, 按钮按下去石沉大海.
//
// ── 为什么要写进账本, 而不是界面自己过滤掉 ──
//
// "不许静默默认 —— 日志里要留下 by" 是这个登记处自己的规矩(见文件头).
// 过期也是一种结局, 它跟超时、跟人拒了是同一类事: 事后要能翻出来
// "这条当时到底怎么了". 界面偷偷不显示的话, 这条链就断在没人看得见的地方.
func (o *OS) expireOrphanDecisions(snap map[abi.ProcessID][]abi.Event) int {
	n := 0
	for pid, events := range snap {
		open := map[string]bool{}
		var order []string
		for _, e := range events {
			m, ok := e.Payload.(map[string]any)
			if !ok {
				continue
			}
			did, _ := m["did"].(string)
			if did == "" {
				continue
			}
			switch e.Kind {
			case abi.EvDecideRequest:
				if !open[did] {
					order = append(order, did)
				}
				open[did] = true
			case abi.EvDecideResolved:
				open[did] = false
			}
		}
		// 按登记顺序判 —— 同一个进程上有好几条时, 账本里的先后要稳
		for _, did := range order {
			if !open[did] {
				continue
			}
			o.events.Append(pid, abi.EvDecideResolved, map[string]any{
				"did": did, "choice": DecisionExpired, "by": "OS",
				"why": "问这件事的那个进程已经不在了(机器重启过). 它不会再有人接。",
			})
			n++
		}
	}
	return n
}

// DecisionExpired 过期这个结局的名字. 界面按它显示"已过期", 不当成
// 由人选择的某一项 —— 那会把"无人决策"说成"人选了这个".
const DecisionExpired = "expired"
