package main

// worker — 会干活的 bot.
//
//	跟上面那种"只会说话"的 bot 的区别只有一个: 它跑的是
//	go/agent 那套 ReAct 循环, 手里有 10 个真工具
//	(read/write/edit/run/search/find/list/fetch/recall/request_access).
//
//	**边界不在工具表, 在内核**. 所以 run 敢给到底: 子进程继承同一套
//	landlock / netns / cgroup. 越界不是崩, 是 request_access 把申请
//	送到你面前 —— 那张审批卡就是这么来的.

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"
)

// procSyscalls 把 ProcessContext 适配成 agent.Syscalls.
//
//	接口按**使用方**定 (agent 只用七个方法), 所以这里是薄薄一层翻译,
//	不是把 ProcessContext 整个搬过去.
type procSyscalls struct {
	pc osinit.ProcessContext
	// cap 这一轮的花费上限 —— nil 或没设就是不限, 见 cap.go
	cap *turnCap
	bot string
}

/**
 * Emit 照发, **顺手记账**.
 *
 *	每轮花费上限数的是 usage 这条事件里的数(送进去 + 吐出来) ——
 *	跟"花了多少"那一页同一个来源, 用户在设置里填的那个数才对得上他
 *	看到的那个数.
 *
 *	**不能用 Spend 那条当记账口**: 内核在推理之后自己记账, 而 agent 调
 *	Spend 只是零消耗地问一句"我超没超" —— 这里要答的正是这一问, 拿它
 *	记账数到的永远是 0. 上限设为 3000 时,
 *	一轮花掉 14153 照样跑完.
 */
func (s procSyscalls) Emit(payload any) {
	s.pc.Emit(payload)
	body, ok := payload.(map[string]any)
	if !ok || body["phase"] != "usage" {
		return
	}
	s.cap.note(s.bot, asInt(body["prompt"])+asInt(body["completion"]))
}

// asInt 事件里的数字过了一趟 JSON, 可能是任何形状
func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

/**
 * Spend 内核那笔照记, **顺便回答"我超没超"**.
 *
 *	agent 每一步之前会零消耗地问一次(见 agent.Run 里那两处 Spend(空)),
 *	问的就是这个. 超了它会**收尾**(说清做到哪儿了)而不是把做过的扔掉.
 */
func (s procSyscalls) Spend(d abi.BudgetDelta) error {
	if err := s.pc.Spend(d); err != nil {
		return err
	}
	return s.cap.over(s.bot)
}
func (s procSyscalls) Recv() (abi.RecvResult, error)   { return s.pc.Recv() }
func (s procSyscalls) TryRecv() (abi.RecvResult, bool) { return s.pc.TryRecv() }

// Can 预检. **不是安全边界** —— 真强制在内核, 进程调不调都逃不掉.
// 它唯一的价值是让 agent 提前知道, 少吃一个莫名其妙的 EACCES.
func (s procSyscalls) Can(axis abi.CapAxis, scope string) (bool, error) {
	return s.pc.Can(string(axis), scope), nil
}

func (s procSyscalls) Decide(req abi.DecisionRequest) (abi.DecisionResolution, error) {
	return s.pc.Decide(req)
}

func (s procSyscalls) Infer(p abi.InferParams) (abi.InferResult, error) {
	return s.pc.Infer(p)
}

// InferStream 边生成边给 —— **带工具的 bot 原来没有这条路**.
//
//	不流式的代价全在人那一侧: 一句"现在几点"要等整段生成完才一次性
//	蹦出来, 而模型第一个字早就出来了. 而这台机器上六个 bot 全带工具,
//	也就是说流式从来没生效过.
//
//	供应商不支持流式的话, ProcessContext.InferStream 自己会退回 Infer
//	(见 osinit/os.go 那条注释), 不假装成流.
//
// EmitLive 只给此刻正看着的人 —— **不进账本**. 见 abi.EvProcDelta.
//
//	忘了转发这一条的后果很隐蔽: agent.Streamer 要求 InferStream 和
//	EmitLive 两个方法, 少一个类型断言就不成立, 于是**静默退回非流式** ——
//	一切正常, 只是流式从来没生效过.
func (s procSyscalls) EmitLive(payload any) { s.pc.EmitLive(payload) }

func (s procSyscalls) InferStream(p abi.InferParams,
	onDelta func(engine.StreamDelta)) (abi.InferResult, error) {
	return s.pc.InferStream(p, onDelta)
}

// projectElsewhere 这个 bot 的"第二个地址": 项目根.
//
//	只有走了分支隔离才有 —— 没隔离时干活的地方就是项目根本身,
//	折一次等于自己折自己.
//
// shouldResume 上次没干完的活, 要不要它自己接着干 —— 见上面那段.
//
//	dirty 拿函数传是有意的: 那是一次 git 调用, 前面几个条件都不成立
//	的时候不该白跑一次.
func shouldResume(auto, braked bool, branch string, dirty func() bool) bool {
	if !auto || braked || branch == "" {
		return false
	}
	return dirty()
}

func projectElsewhere(plan Plan) string {
	if plan.Mode != ModeWorktree || plan.Project == plan.Dir {
		return ""
	}
	return plan.Project
}

// serveWorker 一个会干活的 bot 的一生.
func serveWorker(ctx context.Context, pc osinit.ProcessContext, c *components, p persona, contextTokens int64) (any, error) {
	// 跟能力那一处**读同一个答案** —— 见 workplan.go
	plan, err := c.plans.plan(p)
	if err != nil {
		return nil, err
	}
	root := plan.Dir
	sys := procSyscalls{pc: pc, cap: c.caps, bot: p.name}
	/**
	 * 这段对话起过的后台进程, 结束时一起收掉.
	 *
	 *	提示词里对模型许过一句: "你起的后台进程活不过这段对话 ——
	 *	nohup、&、setsid 都拦不住". 那句话在真内核那条路上是 pid 命名空间
	 *	保证的; 而 console 是 in-proc 的, **没有命名空间**.
	 *
	 *	它可能起一个 8123 端口的服务做验证, 服务会一直活着,
	 *	占着端口. 下一次验证会撞上"端口已被占用", 而那个错跟真正的原因
	 *	隔着好几层 —— "看起来停了其实没停"是最难查的一类问题.
	 *
	 *	**不承诺做不到的事**: 要么改口, 要么让它成真. 这里选后者.
	 */
	crew := &startedProcs{}
	defer crew.reap()
	// 工具表由**这台机器真有的服务**决定 —— 没接的组件不摆出来.
	// 摆一个用不了的工具比没有更糟: 它会照着调、许下做不到的承诺.
	tools := c.toolsFor(p, pc.PID())

	// 第一句先打个招呼, 顺便把工作目录说清楚 —— 用户得知道它在哪儿动手
	// 回来的人不再打一次招呼 —— 打了就是复读
	if !p.returning {
		intro := p.intro
		if intro == "" {
			intro = "我上线了。要我干什么?"
		}
		pc.Emit(map[string]any{"stream": "s0", "channel": "say", "text": intro})
	}

	/**
	 * ── 上次没干完的活, 要不要自己接着干 ──
	 *
	 *	断电、退出、被停掉 —— 这一轮就断在半路了, 而改动留在工作区里.
	 *	等待下一条输入才继续会让活停在那儿一整夜: 人可能已经离开,
	 *	而工作区里的改动不会自行恢复.
	 *
	 *	**缺省关着**: 自己动起来是件要用户点头的事(设置里那个勾).
	 *	判据是**工作区里真有没提交的改动** —— 那是"干到一半"唯一的
	 *	客观证据; 没有的话就是干净收尾, 不该无中生有地叫它.
	 *
	 *	**人按了停不算意外**: 那时候起回来是刹车自己做的(见 stopBot),
	 *	工作区里当然有没提交的改动 —— 照这个判据它会在 87 秒内立刻
	 *	自己接着干. 人按的是停，就停在那儿等待下一条输入.
	 */
	if shouldResume(c.autoResume(), p.braked, plan.Branch, func() bool { return Dirty(plan.Dir) }) {
		note := "[系统] 上次干到一半停了，工作区里还有没提交的改动。看一眼接着做，别重来。"
		pc.Emit(map[string]any{"phase": "resumed_work"})
		c.os.Deliver(pc.PID(), osinit.Delivery{Text: note, Said: note, From: "系统"})
	}

	first, rerr := pc.Recv()
	if rerr != nil || first.Closed {
		return "对话结束", nil
	}

	// 谁在说话, **要在建 Window 之前拼好** —— 模型看的是 Window 里的消息,
	// 只加在 Serve 的 turn 上是不够的: 事件日志里带着标记(看着是对的),
	// 而真正发给模型的第一条没有，于是事件日志会造成已带标记的假象.
	firstTurn := agent.SpeakerNote(first.From) + agent.VoiceNote(first.Voice) +
		movedNote(p, root) + first.Text + agent.NowNote()

	// 页存储 + 地址空间: 历史只增不改, 前缀缓存才可能命中
	store := engine.NewPageStore()
	// **把以前说过的话装回来.** 不装的话界面上摆着一整段历史, 而 bot
	// 自己是失忆的 —— 用户接着问"那个记事本做完了吗", 它说不知道.
	// 没有任何地方显示出错, 看起来就是它在装傻.
	win := agent.NewWindow(store, firstTurn, 0)
	if hist := c.historyOf(p.app, pc.PID()); agent.HasConversation(hist) {
		win = agent.RestoreWindow(store, hist, firstTurn)
		pc.Emit(map[string]any{"phase": "resumed", "events": len(hist)})
	}
	defer win.Release()
	budgeter := agent.NewBudgeter(contextTokens)
	win.UseBudgeter(budgeter)

	ag := &agent.Agent{
		ABI: sys,
		Model: &agent.LLM{
			ABI: sys, Tools: tools,
			// Writable 要跟真实授权一致 —— 提示词比现实更窄的话,
			// 它会以为自己动不了工作目录, 白白少做事
			Persona:  personaWithRoom(p),
			Writable: root,
			Branch:   plan.Branch,
			// 没给项目目录的(小助理那种)不讲项目那套规矩 —— 见 agent.buildSystemPromptKnowing
			NoProject: p.work == "",
			// 边界由沙箱挡着才算"有人强制" —— 见 agent.boundaryTruth
			Enforced: agent.CanSandbox(),
			// 底下跑的是哪个模型 —— **不是秘密**, OS 一直知道.
			// 见 agent.layerModel
			ModelName: modelNameOf(c.os),
			// 他让你记住的事 —— 见 osinit/notes.go.
			//
			//	**必须在进程启动时就进提示词**: 靠工具去查等于要求它
			//	先想到该查, 而它正是因为不知道才答错的. 这一轮里说的话
			//	不靠它(那些本来就在窗口里), 它管的是跨进程那一半
			Known:  c.knownText(),
			Window: win, Budgeter: budgeter,
		},
		Tools: tools,
		/**
		 * **每一轮重新问一次工具表**.
		 *
		 *	这台机器有什么服务是会变的: 用户在设置里打开"能看图"、
		 *	给这个项目起了 git —— 都不该等到重启才生效. 原来只在进程
		 *	启动时定一次, 于是设置页上写着"能看图", 而在跑的 bot 收到图
		 *	只会说"我看不了".
		 */
		ToolsNow: func() []agent.Tool { return c.toolsFor(p, pc.PID()).All() },
		/**
		 * **这一轮在干什么, 要变成提交标题**.
		 *
		 *	不接这一行的话 git log 是一列"合入前把手上的活收一下" ——
		 *	代码全对、测试全绿, 而这份记录一个字都没说. 见 TurnNote.
		 */
		BeforeTurn: func(task string) {
			c.nowDoing(p.name, task)
			// 新的一轮, 花费重新数 —— 上限管的是"一轮", 见 cap.go
			c.caps.reset(p.name)
		},
		// Sys 传进工具箱: request_access 要靠它把申请送到用户面前
		/**
		 * **沙箱决定那句话怎么说**.
		 *
		 *	console 是 in-proc 的, 没有 landlock:
		 *	一条重定向就能把文件写到工作区外面, 没有审批也没有拦截.
		 *	现在 run 包在 sandbox-exec 里(内核层的 Seatbelt, 子孙进程
		 *	一起受约束). 关不住的机器照实说"靠你自觉", 不假装有保护.
		 */
		Box: agent.Toolbox{Root: root, Ctx: ctx, Sys: sys, Started: crew.track,
			// 模型 key 和 token 碰不得 —— 见 agent/secrets.go
			Secrets: secretPaths(),
			// 拉人要不要问 —— 权限页那档, 现问现用
			AutoHire: currentAutoHire,
			// 项目根下的旧地址落到它自己那份副本上 —— 见 agent.Toolbox.Elsewhere.
			// 历史对话、PLAN.md、同事的话里写的都是项目根下的路径
			Elsewhere: projectElsewhere(plan),
			// "这个文件不存在"最常见的原因不是路径写错, 是**写它的那个人
			// 还没合上来**. 只有知道分支布局的宿主答得出来 —— 见 whohas.go
			Missing: whoHas(plan.Project, mainBranch(plan.Project), p.name,
				c.plans.inProject(plan.Project)),
			// 它跑过什么、结果如何 —— 进提交尾注. 见 verify.go
			OnRan:   func(cmd string, exit int) { c.verified.note(p.name, cmd, exit) },
			Sandbox: agent.CanSandbox(),
			// **一个 bot 一份**: 它只该为自己读到的那一版负责.
			//	拉人进来是同一个工作区, 两个 bot 写同一个文件迟早发生 ——
			//	没有这道闸的话, 后写的把先写的整份盖掉, 两边都显示成功.
			Seen: agent.NewSeenFiles(),
			// **第一轮也要带上**: 它走的不是 Serve 的循环, 那儿的
			//	a.Box.Relayed 赋值够不着这一句. 漏了的话被交办的人
			//	在自己的第一轮里还能再往下转 —— 闸只挡住第二轮起.
			Relayed: first.Relay},
		Window: win,
		// 谁在跟它说话 —— 客户端把用户设的名字放在 from 里送过来.
		// 不接这一步的话, 用户在界面上填了名字, bot 还是说"我不知道你是谁"
		Speaker: first.From,
		// 第一句是闹钟拉起来的话, 这一轮的回话是**任务回执**,
		// 界面不该画 —— 见 abi.RecvResult.Quiet
		FirstQuiet: first.Quiet,
		// **默认无限** —— 步数不是有意义的度量:
		// 花费由止损线管, 跑飞由停滞检测管
		MaxSteps: 0,
	}
	/**
	 * ── 系统替它把这一轮的活记进账 ──
	 *
	 *	提示词里写着"一件事干完就提交", 但那是**要求**不是保证:
	 *	模型忘了、这一轮撞上止损线断在半路, 改动就留在工作目录里.
	 *	而没提交的东西对别人等于不存在 —— 交接传不过去, 合并合不进来,
	 *	界面上也说不出它干了什么.
	 *
	 *	提交本身**不替它做任何决定**: 改动已经是它做出来的了, 这一步
	 *	只是把"谁在第几轮改了什么"记下来. 它自己提交过就没东西可提,
	 *	CommitTurn 会当场返回.
	 */
	if plan.Branch != "" {
		turn := int64(0)
		ag.AfterTurn = func(said string) {
			turn++
			hash, err := CommitTurnWith(plan.Dir, p.name, plan.Branch, said, turn,
				c.verified.takeFor(p.name))
			if err != nil {
				// 提交失败不该打断对话: 说一声, 活还在工作目录里没丢
				pc.Emit(map[string]any{"phase": "commit_failed", "err": err.Error()})
				return
			}
			if hash != "" {
				pc.Emit(map[string]any{"phase": "committed", "hash": hash, "branch": plan.Branch})
			}
		}
	}
	// ── 上下文压紧 ── 见 compact.go. **AfterTurn 原来只有走 git 的 bot 才挂**,
	// 而生活助理那种没有分支 —— 于是接在前一个后面, 两件事都做
	cp := &compactor{}
	prevAfter := ag.AfterTurn
	ag.AfterTurn = func(said string) {
		if prevAfter != nil {
			prevAfter(said)
		}
		c.afterTurn(cp, win, func(m map[string]any) { pc.Emit(m) })
	}
	return "对话结束", ag.Serve(firstTurn)
}

// personaWithRoom 把"你在哪个房间、同屋还有谁"写进身份.
//
//	不说的话它以为自己是唯一的助手 —— 问"文案在吗"时会回答
//	"这里只有我一个助手, 没有专职文案". 它不是在撒谎,
//	它**真的看不见别人**: 每个进程只有自己那份对话历史.
//
//	跟 Writable 一样是环境事实: 进程启动时定死, 逐字节不变, 不破前缀缓存.
func personaWithRoom(p persona) string {
	if p.thread == "" {
		return p.role
	}
	mates := []string{}
	// 同屋的人按**当前语言的阵容**找 —— 英文阵容的房间叫 #release, 中文的叫
	// #这次发布, 两套不会串; 而这段话进提示词, 语言也得跟着阵容走
	for _, other := range seedPersonas() {
		if other.thread == p.thread && other.name != p.name {
			mates = append(mates, other.name)
		}
	}
	if strings.HasPrefix(strings.ToLower(os.Getenv("NEOX_LANG")), "en") {
		room := "You are in a room called \"" + p.thread + "\"."
		if len(mates) > 0 {
			room += " Also in the room: " + strings.Join(mates, ", ") + ". Each has their own work; do not answer for them. Whoever the user addresses by name answers."
		}
		if p.role == "" {
			return room
		}
		return p.role + "\n" + room
	}
	room := "你在一个叫「" + p.thread + "」的房间里。"
	if len(mates) > 0 {
		room += "同屋的还有：" + strings.Join(mates, "、") + "。他们各有各的活儿，别替他们答；用户点名找谁，谁答。"
	}
	if p.role == "" {
		return room
	}
	return p.role + "\n" + room
}

// movedNote 刚被换了工作区时, 在下一轮里说的那一句.
//
// ── 为什么非说不可 ──
//
// 换工作区之后历史照装(对话不断是对的), 但**历史里全是旧路径**, 而且
// 那些话是它自己说的，例如:
//
//	"我在这房间实际用的工作区是 …/work/writer; 系统配置里写的
//	 …/AI/发布文案 我没见过, 不猜是同一处"
//
// 它没说错 —— 它只是更信自己说过的话. 系统段里那一句是新的, 但历史里
// 有几十处旧的, 光靠一句压不过.
//
// 所以在**这一轮的用户输入里**说一次: 跟 SpeakerNote 同一个位置、
// 同一个理由 —— 提示词是每轮都付的前缀, 这句话只在换过地方之后付一次.
func movedNote(p persona, root string) string {
	if !p.rebound || root == "" {
		return ""
	}
	return "[你的工作区已经换成 " + root + "。以前那些对话里出现的别的路径" +
		"是你以前的地盘, 现在写不进去了 —— 以这一句为准]\n"
}

// startedProcs 这段对话起过的进程组.
//
//	只记 id 不记别的: 我们唯一要做的事是最后收掉它们, 而进程可能
//	早就自己结束了 —— 收一个已经没了的组是无害的(ESRCH).
type startedProcs struct {
	mu   sync.Mutex
	pgid []int
}

func (s *startedProcs) track(pgid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pgid = append(s.pgid, pgid)
}

// reap 一起收掉. **倒着收**: 后起的通常是前面那个的孩子.
func (s *startedProcs) reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.pgid) - 1; i >= 0; i-- {
		agent.KillTree(s.pgid[i])
	}
	s.pgid = nil
}
