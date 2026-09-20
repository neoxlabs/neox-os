// neox-console — 起一台 OS, 打开观察口, 让对话客户端连上来.
//
//	这不是模拟器: 下面三个 bot 是**真的 OS 进程**, 有进程表、有事件日志、
//	有能力集、有预算. 它们通过 ProcessContext 收话和吐字, 客户端看到的
//	每一条都从事件日志里来.
//
//	端到端的一圈:
//	  客户端 POST /say → OS.Send → 进程的 Recv 醒来 → Emit 吐字
//	  → 事件日志 → SSE → 客户端渲染
//	  进程 Decide → 事件日志 → 客户端弹卡片 → POST /decide → Resolve → 进程继续
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"

	// ── 时区表打进二进制 ──
	//
	//	精简镜像里没有 /usr/share/zoneinfo, 于是 LoadLocation 认不出
	//	"Asia/Shanghai" —— 而那时候它会**静默退回 UTC**, 每一条时间
	//	判断跟着错几个小时, 一处都不报错. 450KB 换掉这个坑
	_ "time/tzdata"
)

// askMode 审批口径. **要能并发读**: 事件回调在别的 goroutine 里,
// spawn 也可能从 HTTP 处理里进来 —— 普通变量在这儿就是数据竞争.
var askMode atomic.Value

// autoHireOn 拉人要不要问 —— 跟 askMode 一样现问现用, 用户可能刚改过
var autoHireOn atomic.Value

func currentAutoHire() bool {
	v, ok := autoHireOn.Load().(bool)
	return ok && v
}

func currentAsk() osinit.AskMode {
	if v, ok := askMode.Load().(string); ok && v != "" {
		return osinit.AskMode(v)
	}
	return osinit.AskBounds
}

// initialAsk 开机时的审批口径.
//
//	用户在设置里存过的最大; 没存过时认环境变量 NEOX_ASK_MODE ——
//	镜像/编排靠它定自己的缺省(Docker 形态里容器本身就是隔离环境,
//	缺省"都不问"才对得起"个人 AI Agent 操作系统"这句话); 都没有
//	才落到系统缺省 bounds(缺省必须是安全的那个).
func initialAsk(cfg osinit.ProviderConfig) osinit.AskMode {
	if cfg.Ask != "" {
		return cfg.AskModeOf()
	}
	switch osinit.AskMode(os.Getenv("NEOX_ASK_MODE")) {
	case osinit.AskAlways:
		return osinit.AskAlways
	case osinit.AskNever:
		return osinit.AskNever
	case osinit.AskBounds:
		return osinit.AskBounds
	}
	return osinit.AskBounds
}

type persona struct {
	name  string
	app   string
	intro string
	// thread 同一个 thread 的几个进程就是一个**房间**.
	//
	//	这不是为界面发明的概念: OS 本来就把授权挂在 thread 上
	//	("对话跨进程存活, 进程会死对话不死"), 房间只是同一个东西
	//	被人看见的样子.
	thread string
	// role 这个 bot 是干什么的 —— 进系统段
	role string
	// initGit 用户点过头: 可以在这个项目目录里 git init.
	//
	//	**在别人的目录里凭空建一个 git 仓库是不该自作主张的事**:
	//	它改变那个目录的性质, 而且事后看不出是谁干的.
	initGit bool
	// asks 第一次说话前要一次许可 —— 用来演示审批卡片这条链路
	asks bool
	// returning 这个 bot 以前说过话.
	//
	//	**回来的人不再打一次招呼**. 早先每次开机每个 bot 都发一遍开场白,
	//	而历史是跨重启合并的 —— 重启四次, 房间里就有四条一模一样的
	//	"版本号定了 3.4.0". 用户看到的是"它在复读", 而真相是
	//	"每一条都来自一次不同的开机".
	returning bool
	/**
	 * braked 这次起回来是**因为人按了停**.
	 *
	 *	刹车的做法是杀掉这一轮再把同一个人起回来. 而"掉线自动接着干"
	 *	那个勾看到的正是同一副样子: 工作区里有没提交的改动, 于是它
	 *	立刻发一句"接着做"——可能又运行 87 秒, 即使人已经按了停,
	 *	它仍会继续工作.
	 *
	 *	**那不叫停得住**. 自动接着干是给断电、退出这类意外准备的;
	 *	人明确按了停, 就该停在那儿等他下一句话.
	 */
	braked bool
	// rebound 这个进程是**刚被换了工作区**才起来的.
	//
	//	要在下一轮里明说一句, 否则它会信自己的历史而不是系统段 ——
	//	进程可能说明: "我在这房间实际用的工作区是 …/work/writer;
	//	系统配置里写的 …/AI/发布文案 我没见过, 不猜是同一处".
	//	它没说错: 装回来的历史里全是旧路径, 而那些话是它自己说的.
	rebound bool
	// work 它在哪儿干活. 空 = 系统分一个匿名目录.
	//
	//	跟 role 一样是环境事实: 进程启动时定死, 进提示词尾段, 不破前缀缓存.
	work string
	// tools 跑 ReAct 循环, 手里有真工具.
	//
	//	**代价说清楚**: 带工具的 bot 拿不到逐字流式 —— 它的模型输出是
	//	结构化的(要调哪个工具、参数是什么), 逐字吐出来是一串 JSON,
	//	对人没有意义. 换来的是**按步出进度**: 想什么、动了哪个文件、
	//	结果如何, 一步一条. 那比看着 JSON 一个字一个字冒出来有用得多.
	tools bool
}

var personas = []persona{
	{name: "研究", app: "research", intro: "在的。要查什么?", role: "你负责查资料、读代码、把结论说清楚。", asks: true, tools: true},
	{name: "值守", app: "ops", intro: "盯着呢。CI 现在是绿的。", role: "你负责盯线上和 CI，出事第一时间说清楚是什么坏了。", tools: true},
	{name: "构建", app: "build", intro: "构建机空着。要我跑什么?", role: "你负责编译和跑测试，给出可执行的下一步。", tools: true},
	// 一个房间: 三个人在里面一起收拾这次发布
	{name: "发版", app: "release", thread: "#这次发布", intro: "版本号定了 3.4.0，改动清单我拉出来了。", role: "你在一个发布房间里，负责把这次发布推到能发的状态。", tools: true},
	{name: "回归", app: "qa", thread: "#这次发布", intro: "回归跑完 2007 条，全绿。", role: "你在发布房间里负责测试，只认证据不认感觉。", tools: true},
	{name: "文案", app: "writer", thread: "#这次发布", intro: "更新日志我先起个草，三段十行封顶。", role: "你在发布房间里写更新日志：三段十行封顶，一条一行一句，不写形容词。", tools: true},
}

// personasEN 英文界面下的开场阵容 —— 同样六个位置、同样的 app 与房间,
// 只有名字、开场白和职责说明是英文的.
//
//	**开场白必须跟界面一个语言**: 界面是英文、六个 bot 一开口全是中文,
//	那就是假英文版. role 也翻 —— 它进提示词, 决定 bot 之后用哪种语言说话.
var personasEN = []persona{
	{name: "Research", app: "research", intro: "Here. What should I look into?", role: "You research: read sources and code, and state conclusions clearly.", asks: true, tools: true},
	{name: "Ops", app: "ops", intro: "Watching. CI is green.", role: "You watch production and CI. When something breaks, say exactly what broke, first.", tools: true},
	{name: "Build", app: "build", intro: "Build machine is idle. What should I run?", role: "You compile and run tests, and give an actionable next step.", tools: true},
	// One room: three agents preparing this release together
	{name: "Release", app: "release", thread: "#release", intro: "Version set to 3.4.0. I've compiled the change list.", role: "You are in a release room, responsible for getting this release to a shippable state.", tools: true},
	{name: "QA", app: "qa", thread: "#release", intro: "Regression finished: 2,007 cases, all green.", role: "You handle testing in the release room. Evidence only; no hunches.", tools: true},
	{name: "Docs", app: "writer", thread: "#release", intro: "Drafting the release notes: three sections, ten lines max.", role: "You write the release notes in the release room: three sections, ten lines max, one item per line, no adjectives.", tools: true},
}

// seedPersonas 按 NEOX_LANG 选开场阵容. 缺省中文 —— 这套系统是用中文写的,
// 英文是桌面壳按用户偏好显式带过来的 (electron/main.cjs 起 OS 时设 NEOX_LANG).
func seedPersonas() []persona {
	if strings.HasPrefix(strings.ToLower(os.Getenv("NEOX_LANG")), "en") {
		return personasEN
	}
	return personas
}

func main() {
	// 设了 NEOX_OBSERVE_ADDR 就是"我要把口子开到别处"的显式决定 ——
	// 远程放行只认这一个来源, 代码里没有别的路径替用户做这个决定
	addr, addrChosen := os.LookupEnv("NEOX_OBSERVE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:7717"
	}
	token := observeToken()

	// 凭据**只在宿主手里**: 不进任何被约束的进程的环境.
	// 进程要推理就调 ProcessContext.Infer, 由 OS 代它去调.
	store := newProviderStore()
	cfg := store.load()
	askMode.Store(string(initialAsk(cfg)))
	autoHireOn.Store(cfg.AutoHire)
	// 落盘: 不接这个, **进程一死对话就没了** —— 用户重启一次客户端,
	// 之前说过的全部消失, 而事件日志本来就是 append-only 的, 白扔.
	ledger := ledgerPath()
	ledgerStore, serr := osinit.OpenEventStore(ledger)
	if serr != nil {
		fmt.Fprintln(os.Stderr, "账本打不开, 这次不落盘:", serr)
	}

	o := osinit.New(osinit.Options{Mode: abi.ModeDev, Provider: buildProvider(cfg),
		// 认不认图跟着同一份配置走 —— 见 buildViewer
		Viewer: buildViewer(cfg),
		// 能不能搜网 —— **nil 要一路传到工具表**: 摆一个配不出结果的
		// 工具, 模型会照着调、拿到一句"没配搜索", 而它已经许过诺了
		Searcher: buildSearcher(cfg), EventStore: ledgerStore})
	if ledgerStore != nil {
		defer ledgerStore.Close()
		// 先把历史装回内存, 再起进程 —— 顺序反了的话新进程的
		// created 事件会被历史盖掉
		if snap, lerr := osinit.LoadEvents(ledger); lerr == nil && len(snap) > 0 {
			o.RestoreEvents(snap)
			fmt.Fprintf(os.Stderr, "装回 %d 段历史\n", len(snap))
		}
	}
	defer o.Shutdown("console 退出")
	// 开机就把"都不问"落到位 —— 不然重启后第一句话还是撞出网墙
	o.SetNetOpen(currentAsk() == osinit.AskNever)

	// ── 这台机器能给 bot 的服务 ──
	//
	//	闹钟必须**活得比进程长**: 一个 agent 自己起协程睡到 11 点, 进程一死
	//	它就没了, 而用户不会知道 —— 静默不响是闹钟最糟的失败方式.
	//	所以它是内核对象, 落在同一份事件日志里, 开机从日志装回来.
	comp := &components{os: o, verified: newVerifyLog(), ledger: ledger}
	// ── 时区要在别的一切之前定下来 ──
	//
	//	日报"今天发过没有"、打扰额度"今天用了几次"、停留"算在哪一天"
	//	全是按**本地日子**算的. 先恢复它们再定时区的话, 装回来的按
	//	UTC 的日子分组, 之后按本地的日子查 —— 于是日报当天再发一遍,
	//	额度从零开始. 见 osinit/zone.go
	comp.zone = osinit.NewZone(o.Log())
	comp.zone.Restore(flatEvents(o.Log().Snapshot()))
	comp.zone.UseEnv(os.Getenv("TZ"))
	fmt.Fprintln(os.Stderr, comp.zone.Note())
	// 每轮花费上限: 现问现用 —— 用户可能刚在设置里改过. 0 = 不限
	comp.caps = newTurnCap(func() int64 { return store.load().CapTokens })
	// 掉线之后自己接着干吗 —— 同上, 现问现用. 缺省关着
	comp.resume = func() bool { return store.load().AutoResume }
	// 投递总线要在闹钟之前建 —— fireInto 会用到它
	comp.deliveries = osinit.NewDeliveries(o.Log())
	comp.timers = osinit.NewTimers(o.Log(), nil, comp.fireInto)
	comp.timers.Restore(flatEvents(o.Log().Snapshot()))

	// 长期记忆 —— 见 osinit/notes.go.
	//
	//	**跟感知层无关, 所以建在这儿**: "记住我老婆生日"不需要任何
	//	传感器. 挂在感知层里的话, 一台没接采集端的机器就连一句话
	//	都记不住 —— 而它不会报错, 只会在下次开机时不知道
	comp.notes = osinit.NewNotes(o.Log())
	comp.notes.Restore(flatEvents(o.Log().Snapshot()))

	// 待办和日程 —— 同上, 跟感知层无关. 见 osinit/agenda.go
	comp.agenda = osinit.NewAgenda(o.Log())
	comp.agenda.Restore(flatEvents(o.Log().Snapshot()))

	roster := newBotRoster()
	projNames := newProjectNames()
	owners := newRoomOwners()
	// 样式基底装到 ~/.neox-os/kit/ —— bot 搭前端照着用, 见 kit.go
	installKit()
	comp.roster = roster
	/**
	 * **每摊活都收进 git, 不问**.
	 *
	 *	原来这里是反的: 要用户先点头才走 git —— 而界面上从来没有过
	 *	那个勾, 于是**每一个项目都落在独占模式里**: 一个项目只能一个
	 *	bot, 交接、合入、产物、验证证据全是空的, 而且没有一处说得出
	 *	为什么. 用户的原话: "不然有的不用 git, bot 来说也是灾难吧".
	 *
	 *	nil = 全都收. 收之前的那几道闸(家目录、系统目录、太大的目录)
	 *	在 adopt.go 里, 收不了的会带着原因降级成独占, 不会硬来.
	 */
	comp.plans = newWorkPlanner(nil)
	// **晚绑**: spawn 要用 comp(它把组件交给新进程), comp 又要用 spawn ——
	// 一个闭包解开这个环. 顺手也是"这台机器许不许 bot 自己拉人"的开关:
	// 不接这一行, 工具表里就没有 recruit
	comp.spawn = func(p persona) (abi.ProcessID, error) { return spawn(o, comp, p) }
	// 名册上的人运行中死了也要拉起来 —— 见 keeper.go. 不接的话,
	// 用户只能靠重启客户端走开机那条 spawn, 界面上就是"进程离线了".
	keeper := &botKeeper{os: o, roster: roster, spawn: comp.spawn}
	// 有人合进主干 → 通知真正受影响的那几个 —— 见 bus.go 的判据
	comp.watchMerges()
	/**
	 * 批准之后叫它一声 —— 见 granted.go.
	 *
	 *	例如 bot 申请连 pypi 装 pytest, 获得批准后会回"授权已生效,
	 *	**下一句话**我就装并跑测试", 然后停在那儿 —— 代码都写好了,
	 *	主干还坏着, 中间只差一句"继续". 而刚点完同意的那个人, 最可能
	 *	正在离开.
	 */
	stopGrants := watchGrants(o)
	defer stopGrants()
	// 把一个 bot 换到别的房间: 起一个带新标签的进程, 停掉旧的.
	//
	//	**标签在进程启动时定死**, 改不了 —— 所以"拉进房间"只能是
	//	换一个进程. 历史跟着 bot 标签走, 所以对话不断:
	//	进程会死, 对话不死.
	/**
	 * stopBot —— **刹车**.
	 *
	 * ── 为什么非有不可 ──
	 *
	 *	一个 bot 跑偏了(在错的文件上改、绕着圈子跑命令), 原来能做的只有
	 *	三件: 打字插一句(那是"劝", 停不停由它自己判)、把整段对话删掉
	 *	(不可逆, 连历史一起没)、或者关掉整个客户端.
	 *
	 *	一个会自己动手改文件、自己花钱的东西, **必须有一颗按下去就停的
	 *	按钮**. 没有的话, 人对它的信任只能靠"希望它别出错"撑着.
	 *
	 * ── 停 = 杀掉这一轮, 但人还在 ──
	 *
	 *	杀掉进程就停住了这一轮(工具调用、推理、还没跑完的命令一起停),
	 *	然后**立刻把同一个人起回来**: 历史跟着 bot 标签走, 所以对话不断;
	 *	returning=true 是为了它别再打一遍招呼.
	 *
	 *	它手上没提交的改动**留在工作区里**, 一个字不动 —— 停是"别再往下
	 *	干了", 不是"把干过的扔掉". 下一轮它自己会把那些收进提交.
	 */
	stopBot := func(name string) error {
		next, _, err := findPersona(roster, name)
		if err != nil {
			return err
		}
		stopped := false
		for _, info := range o.List() {
			if info.Spec.Name == name && !info.State.IsTerminal() {
				o.Kill(info.PID, "用户按了停")
				stopped = true
			}
		}
		if !stopped {
			return fmt.Errorf("「%s」现在没在干活", name)
		}
		next.returning = true // 不是重新认识, 别再打一次招呼
		next.braked = true    // 人按的是停 —— 别让"自动接着干"把它又叫起来
		_, err = spawn(o, comp, next)
		return err
	}

	moveBot := func(name, thread string) error {
		next, builtin, err := findPersona(roster, name)
		if err != nil {
			return err
		}
		for _, info := range o.List() {
			if info.Spec.Name == name {
				o.Kill(info.PID, "换房间")
			}
		}
		next.thread = thread
		next.returning = true // 换个房间不是重新认识, 别再打一次招呼
		if _, err := spawn(o, comp, next); err != nil {
			return err
		}
		return roster.setRoom(name, thread, builtin)
	}

	// 把一个 bot 派到另一个工作区.
	//
	//	**跟换房间是同一种动作**: 能力在进程启动时定死(landlock 只能收紧
	//	不能放宽), 所以"换个地方干活"只能是换一个进程. 历史跟着 bot 标签走,
	//	所以对话不断 —— 进程会死, 对话不死.
	rebindBot := func(name, rawWork string) error {
		work, err := resolveWork(rawWork)
		if err != nil {
			return err
		}
		next, builtin, err := findPersona(roster, name)
		if err != nil {
			return err
		}
		for _, info := range o.List() {
			if info.Spec.Name == name {
				o.Kill(info.PID, "换工作区")
			}
		}
		next.work = work
		next.returning = true // 换个地方不是重新认识, 别再打一次招呼
		next.rebound = true   // 但**换了地方要说一声** —— 见 persona.rebound
		if _, err := spawn(o, comp, next); err != nil {
			return err
		}
		// **起成了才记名册**: 记了却起不来的话, 下次开机会一直报错
		return roster.setWork(name, work, builtin)
	}

	// 改岗位 —— 跟换工作区同一种动作.
	//
	//	系统段在进程起来的那一刻就定死了(它是启动参数, 不是可以中途改的
	//	状态), 所以"换个岗位"只能是换一个进程. 历史跟着 bot 标签走,
	//	所以对话不断 —— 进程会死, 对话不死.
	roleBot := func(name, role string) error {
		next, builtin, err := findPersona(roster, name)
		if err != nil {
			return err
		}
		for _, info := range o.List() {
			if info.Spec.Name == name {
				o.Kill(info.PID, "改岗位")
			}
		}
		next.role = role
		next.returning = true // 不是重新认识, 别再打一次招呼
		if _, err := spawn(o, comp, next); err != nil {
			return err
		}
		// **起成了才记名册** —— 记了却起不来的话, 下次开机会一直报错
		return roster.setRole(name, role, builtin)
	}

	comp.moveInto = moveBot
	comp.rebind = rebindBot
	comp.ownRoom = owners.set
	// 换房间/搬工作区都**说完话之后**才动手 —— 两者都是换一个进程,
	// 当场换就把它正在干的这一轮断在半路. 挂在进程状态上而不是定时器上:
	// "这一轮说完了"是一个事实, 不是一段时间.
	stopMoves := o.Log().SubscribeAll(func(e abi.Event) {
		if name := waitingBot(o, e); name != "" {
			go comp.applyPendingMove(name)
			go comp.applyPendingRebind(name)
		}
	})
	defer stopMoves()

	// ── 感知层 ──
	//
	//	必须在 observe server 之前: 采集口挂在观察口上(见
	//	ObserveOptions.Sense), server 建的时候就要拿到总线.
	sense := buildSense(o, comp)
	defer sense.stop()

	srv, err := osinit.NewObserveServer(o, osinit.ObserveOptions{
		Addr: addr, Token: token,
		// 远程放行只在用户显式设了地址时成立 —— 见 AllowRemote 的三条边界
		AllowRemote: addrChosen,
		// 项目显示名: 机器认路径, 人看名字 —— 见 projects.go
		ProjectNames: osinit.ProjectNameHooks{Get: projNames.load, Set: projNames.set},
		// 群主: 开房那一刻定 —— 见 roomOwners
		RoomOwners: owners.load,
		// 打进二进制的 Web 界面; 没打进来就只开 API(uiFS 返回 nil)
		UI: uiFS(),
		// 用户发进来的图落在这儿 —— 界面按 id 取来渲染, bot 按路径 view_image
		Blobs: filepath.Join(neoxHome(), "blobs"),
		// 边界有没有人强制 —— 界面照实说, 不写死"边界在内核"
		Enforced: agent.CanSandbox(),
		OnMove:   moveBot,
		// 刹车: 停掉这一轮, 人还在, 手上的改动留着 —— 见 stopBot
		OnStop:   stopBot,
		OnRebind: rebindBot,
		OnRole:   roleBot,
		// 采集口跟观察口同一个端口、同一个 token —— 一个设备只该
		// 记住一个地址. 见 ObserveOptions.Sense
		Sense:   sense.bus,
		Devices: sense.devices,
		People:  sense.people,
		World:   sense.world,
		Places:  sense.places,
		Routine: sense.routine,
		// 它记住的事 —— **跟感知层无关**, 所以从 comp 拿:
		// 一台没接采集端的机器照样记得住话
		Notes:  comp.notes,
		Agenda: comp.agenda,
		// 提醒和盯着的事 —— **界面上一直看不到它们**, 而
		// "我让你提醒我什么了"是他最常问的一句
		Timers:  comp.timers,
		Watches: sense.watches,
		// 时区 —— 同上, 跟感知层无关
		Zone: comp.zone,
		// 手机上那张位置卡片的缩略图由 OS 去取 —— AK 不落到手机上,
		// 而且服务端 key 绑了出口 IP, 手机每换基站就变
		MapShot: comp.mapShot,

		Rules: sense.rules,
		OnForget: func(pids []abi.ProcessID) error {
			/**
			 * 删一段对话 —— **四个地方一起删**.
			 *
			 *	少删一个就是假删, 而假删的症状各不相同:
			 *	  只删内存   → 下次开机复活
			 *	  只删账本   → 这次会话还看得见
			 *	  不删名册   → 下次开机它自己又起来了
			 *	  不杀进程   → 界面上没了, 它还在后台跑着花钱
			 *	  不删记录   → **几秒后自己长回来**: 杀掉只是让它停下,
			 *	              记录还在进程表里, 轮询一拉就又出现, 显示成"不在"
			 */
			drop := map[abi.ProcessID]bool{}
			names := map[string]bool{}
			for _, pid := range pids {
				drop[pid] = true
				if info, ok := o.Info(pid); ok {
					names[info.Spec.Name] = true
					o.Kill(pid, "用户删掉了这段对话")
				}
				o.Log().Forget(pid)
				// **记录也要抹**: 只杀不抹的话, 下一次轮询进程表就把它捞回来了
				o.Drop(pid)
			}
			if ledgerStore != nil {
				if _, err := ledgerStore.Forget(drop); err != nil {
					return err
				}
			}
			/**
			 * **算好的工作区也要一起忘掉**.
			 *
			 *	不忘的话, 同名再建一个 bot 会拿到旧计划 —— 而那份计划
			 *	指着的 worktree 可能已经跟着项目一起没了. 典型失败情况是:
			 *	它在一个 .git 指向空气的目录里干活、"验证通过", 而什么
			 *	都无法到达主干.
			 */
			for name := range names {
				comp.plans.forget(name)
			}
			/**
			 * **删完要广播一声**.
			 *
			 *	删如果只由界面自己先做, 再告诉服务器 —— 一个窗口
			 *	一条路走下来是对的. 但**从别处删的没人知道**: 测试台
			 *	按接口清掉了 108 个进程、账本只剩一行, 而开着的那个窗口
			 *	侧栏里 12 间屋子一间不少 —— 点进去是空的.
			 *
			 *	删是**世界变了**, 不是"这个窗口的私事". 广播一声, 谁在看
			 *	谁就跟着删.
			 */
			o.Log().Append("sense", abi.EvProcState, map[string]any{
				"phase": "forgotten", "pids": pids})
			return roster.remove(names)
		},
		OnPolicy: osinit.PolicyHooks{
			Get: func() osinit.AskMode { return currentAsk() },
			Set: func(next osinit.AskMode) error {
				askMode.Store(string(next))
				// **切档要立刻生效于已经跑着的 bot**: capsFor 只管下一代进程,
				// 不拨这个开关的话, 用户切到"都不问"之后看到的还是撞墙 ——
				// "我都放开了还不行"(真机原话).
				o.SetNetOpen(next == osinit.AskNever)
				current := store.load()
				current.Ask = string(next)
				return store.save(current)
			},
		},
		OnProvider: osinit.ProviderHooks{
			Get: func() osinit.ProviderConfig { return store.load() },
			Models: func(want osinit.ProviderConfig) ([]string, error) {
				provider := buildProvider(withStored(store, want))
				if provider == nil {
					return nil, errors.New("还没有 key")
				}
				catalog, ok := provider.(engine.Catalog)
				if !ok {
					return nil, errors.New("这个供应商不支持列模型")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				return catalog.Models(ctx)
			},
			Test: func(want osinit.ProviderConfig) osinit.ProviderProbe {
				provider := buildProvider(withStored(store, want))
				if provider == nil {
					return osinit.ProviderProbe{Error: "还没有 key"}
				}
				// 一次**最小**的真请求: 少数几个 token, 但走的是完整那条路
				// (鉴权、路由、模型名), 所以它答得出的东西 bot 也答得出
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				began := time.Now()
				res, err := provider.Infer(ctx, abi.InferParams{
					Messages:  []abi.InferMessage{{Role: "user", Content: "回一个字: ok"}},
					MaxTokens: 16,
				})
				took := time.Since(began).Milliseconds()
				if err != nil {
					return osinit.ProviderProbe{LatencyMS: took, Error: err.Error()}
				}
				probe := osinit.ProviderProbe{
					OK: true, LatencyMS: took, Model: res.Model,
					Reply: strings.TrimSpace(res.Content),
				}
				/**
				 * 勾了"能看图"就**真送一张图过去**.
				 *
				 *	送的是一张纯红的小图, 问它什么颜色 —— 答得出红色,
				 *	这条路就是通的; 答不出或者报错, 当场就知道这个勾
				 *	不该勾. 这正是"谁配谁负责"里缺的那一半: 让配的人
				 *	能当场验证.
				 */
				if want.Vision {
					probe.Vision = probeVision(withStored(store, want))
				}
				return probe
			},
			Set: func(next osinit.ProviderConfig) error {
				// 只填地址和模型、不填 key = 沿用已经存着的那把.
				// 否则每次改模型都要重贴一次 key, 而重贴意味着它又
				// 在剪贴板里走一遭.
				current := store.load()
				if strings.TrimSpace(next.APIKey) == "" {
					next.APIKey = current.APIKey
				}
				if strings.TrimSpace(next.APIKey) == "" {
					return errors.New("还没有 key")
				}
				if err := store.save(next); err != nil {
					return err
				}
				autoHireOn.Store(next.AutoHire)
				o.SetProvider(buildProvider(next))
				// **一起换**: 只换推理不换看图的话, 改完模型之后
				// "能不能看图"还停在上一个模型上
				o.SetViewer(buildViewer(withStored(store, next)))
				return nil
			},
		},
		/**
		 * 产物: **判据是 git, 不是它自己说的话**.
		 *
		 *	原来"它干了什么"只能从对话里读: 它说"改好了", 而到底改了
		 *	哪几个文件得人去翻 —— 翻不动的时候就只能信它.
		 */
		OnOutput: func(bot string) (any, error) {
			plan, err := comp.plans.planOf(bot)
			if err != nil {
				return nil, err
			}
			return OutputOf(plan.Dir, plan.Branch), nil
		},
		/**
		 * 项目现在什么状态 —— **项目才是干活的单位**, 人是围着它转的.
		 *
		 *	少了这一页, "这摊活现在怎么样"只能一个人一个人点开看;
		 *	而最要紧的那一格("谁手上还有没合的")在别处根本看不见.
		 */
		OnProject: func(path string) (any, error) {
			if strings.TrimSpace(path) == "" {
				return nil, errors.New("没说是哪个项目")
			}
			return projectOf(realPath(path), comp.plans.inProject(path)), nil
		},
		// 合错了要退得回去 —— 见 RevertOn
		OnRevert: func(project, hash string) (string, error) { return RevertOn(realPath(project), hash) },
		// 花了多少、换来了什么 —— 全部从事件账本算, 不另记一份(见 spend.go)
		OnSpend:  func() any { return spendNow(o, comp.plans) },
		OnRevive: keeper.fromDead,
		OnCreate: func(req osinit.CreateRequest) (abi.ProcessID, error) {
			// 界面上新建的 bot **默认会干活** —— "能聊天"是顺带的,
			// "能干活"才是要它的理由
			work, werr := resolveWork(req.Work)
			if werr != nil {
				return "", werr
			}
			made := savedBot{Name: req.Name, Thread: req.Thread, Work: work, Role: req.Role}
			pid, err := spawn(o, comp, made.persona())
			if err != nil {
				return pid, err
			}
			// 起成了才记名册 —— 记了却起不来的话, 下次开机会一直报错
			if serr := roster.add(made); serr != nil {
				fmt.Fprintln(os.Stderr, "名册写不下:", serr)
			}
			return pid, nil
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "观察口起不来:", err)
		os.Exit(1)
	}
	srv.Start()
	defer srv.Close()

	// 不问那一档: 决策一登记就自动批, 但**照样进日志** ——
	// 由谁批的写着"自动", 事后翻得出来"这一步是谁放的行".
	// 静默放行不留痕迹的话, 出事之后没人能重建当时发生了什么.
	stopAuto := o.Log().SubscribeAll(func(e abi.Event) {
		if currentAsk() != osinit.AskNever || e.Kind != abi.EvDecideRequest {
			return
		}
		body, _ := e.Payload.(map[string]any)
		did, _ := body["did"].(string)
		if did == "" {
			return
		}
		/**
		 * **拉人永远问, 哪怕档位是"都不问"**.
		 *
		 *	"都不问"这个档说的是"别为它够不够权限干活来烦我" ——
		 *	那类决策的代价是有界的: 让它读个文件、连个域名.
		 *
		 *	拉人不一样: 它是唯一一个**会让花费翻倍**的动作, 而且拉进来的人
		 *	还会接着拉人. 一档"都不问"就能让一句话变成一屋子 bot,
		 *	等用户发现时账单已经出去了.
		 *
		 *	自动批的判据是"代价有界", 不是"用户嫌烦" —— 两者混在一起,
		 *	那个开关就成了一张空白支票.
		 */
		if isRecruit(o, did) {
			return
		}
		o.Decisions().Resolve(did, "yes", "自动", nil)
	})
	defer stopAuto()

	// 哪些 bot 以前说过话 —— 装回来的历史里认
	spoken := map[string]bool{}
	for _, pid := range o.Log().PIDs() {
		for _, ev := range o.Log().Replay(pid, 0) {
			body, _ := ev.Payload.(map[string]any)
			labels, _ := body["labels"].(map[string]any)
			if bot, ok := labels["bot"].(string); ok {
				spoken[bot] = true
			}
			break
		}
	}

	// 名册里的房间归属**盖过**内置的 —— 用户拉过人就以用户为准
	rooms := roster.rooms()
	works := roster.works()
	roles := roster.roles()
	buried := roster.buried()
	for _, p := range seedPersonas() {
		// 删过的不再起 —— 见 savedBot.Gone
		if buried[p.name] {
			continue
		}
		p.returning = spoken[p.app]
		if thread, moved := rooms[p.name]; moved {
			p.thread = thread
		}
		// 用户派过的工作区盖过代码里的默认 —— 跟房间归属同一条规矩
		if work, sent := works[p.name]; sent {
			p.work = work
		}
		// 岗位同理: 改过的那句话得活过重启
		if role, set := roles[p.name]; set {
			p.role = role
		}
		_, _ = spawn(o, comp, p)
	}
	// 界面上建过的 bot **要活过重启**.
	//
	//	不装回来的话它们在界面上是灰的, 而输入框照样能打字 ——
	//	用户对着一个没气的进程说话, 消息石沉大海, 而侧栏里
	//	历史的最后一句看起来像"它刚回了你". 那是最糟的一种失灵:
	//	没有任何地方显示出错, 但它就是不工作.
	for _, saved := range roster.load() {
		/**
		 * **内置的那几个不从名册起**.
		 *
		 *	名册里之所以有它们, 是因为"在哪个房间"这件事要活过重启;
		 *	但身份是代码里的 persona. 拿名册那条去起进程的话,
		 *	会起出一个 bot 标签完全不同的同名分身 —— 界面上就是
		 *	同一个人出现两次, 而且两个都活着、各说各的.
		 */
		if saved.Builtin {
			continue
		}
		/**
		 * **墓碑不是 bot**.
		 *
		 *	删掉的名字在名册里留一行 {name, gone:true}(见 savedBot.Gone).
		 *	这个循环原来照单全收, 于是那六块墓碑被当成六个 bot 起了回来 ——
		 *	名字还是那几个, 身份却是空的. 清空之后重启时,
		 *	六条会话原样长回来, 而名册里明明写着 gone。
		 */
		if saved.Gone {
			continue
		}
		back := saved.persona()
		back.returning = spoken[back.app]
		if _, err := spawn(o, comp, back); err != nil {
			fmt.Fprintf(os.Stderr, "装不回 %s: %v\n", saved.Name, err)
		}
	}
	// 开机那一圈走完之后才开始看管 —— 早看的话会跟上面这几行抢着起.
	stopKeep := keeper.watch()
	defer stopKeep()
	sweep := time.NewTicker(5 * time.Second)
	defer sweep.Stop()
	go func() {
		for range sweep.C {
			keeper.sweep()
		}
	}()

	// **起完 bot 再让闹钟走**. 反过来的话, 开机时补响一条错过的提醒,
	// 那一刻还没有任何进程顶着 bot 标签, 它只能落到"设它的 bot 已经不在了"
	// 那条兜底路径上 —— 而它其实就在下面几行里正要起来.
	stopTimers := comp.timers.Start()
	defer stopTimers()
	if n := len(comp.timers.Pending()); n > 0 {
		fmt.Fprintf(os.Stderr, "装回 %d 个没响的闹钟\n", n)
	}

	// 这两行是给客户端读的 —— Electron 主进程 spawn 我们, 从 stdout 认地址
	if o.Provider() == nil {
		// 没配就说没配, 不让 bot 拿套话糊过去
		fmt.Fprintln(os.Stderr, "没有 NEOX_API_KEY —— bot 会直说这台机器没接推理服务")
	} else {
		fmt.Fprintf(os.Stderr, "推理走 %s\n", o.Provider().Model())
	}
	fmt.Printf("NEOX_OBSERVE_URL http://%s\n", srv.Addr())
	fmt.Printf("NEOX_OBSERVE_TOKEN %s\n", token)
	// 给人看的那一行: 自托管(docker run / 直跑二进制)的用户没有 Electron
	// 替他拼地址 —— 界面打进来了就把能直接点开的链接说全.
	// 绑在通配地址上时打出来是 http://[::]:7717 —— 一个点不开的地址.
	// 人要的是能点的: 换成 localhost(容器里映射出去也是这个入口).
	if uiFS() != nil {
		fmt.Printf("浏览器打开 http://%s/?token=%s\n", clickable(srv.Addr()), token)
	}
	os.Stdout.Sync()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

// clickable 把监听地址变成人能点开的样子.
//
//	绑通配地址(0.0.0.0 / [::])时, Addr() 打出来的是 http://[::]:7717 ——
//	浏览器点不开. 对着它的人在本机(或把容器端口映到了本机), 入口就是
//	localhost. 绑具体地址的照原样说.
func clickable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "0.0.0.0" || host == "::" || host == "" {
		return net.JoinHostPort("localhost", port)
	}
	return addr
}

func spawn(o *osinit.OS, c *components, p persona) (abi.ProcessID, error) {
	spec := abi.ProcessSpec{
		App:  p.app,
		Name: p.name,
		Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/"}},
	}
	/**
	 * 身份进 labels.
	 *
	 *	进程会死, 对话不死 —— 而"对话"靠的就是这几个标签:
	 *	重启之后 pid 全变了, 但事件日志里那条 created 事件带着
	 *	bot/name, 界面据此把新旧进程认成**同一个人**.
	 *	没有它, 历史就是一堆认不出是谁说的话.
	 */
	spec.Labels = map[string]string{"bot": p.app, "name": p.name}
	if p.thread != "" {
		spec.Labels["thread"] = p.thread
	}
	/**
	 * 岗位也进标签.
	 *
	 *	用户的原话: "确实不同的助手太多, 我自己都分不清"。而每个 bot
	 *	**本来就有岗位说明**(拉人时写的、内置的写在代码里), 只是界面上
	 *	一个字都看不到 —— 侧栏只有名字, 而"小登/小记/小勤/报表"这种名字
	 *	光看是分不出谁管什么的.
	 *
	 *	**截一句**: 标签跟着每条创建事件进账本, 完整的岗位说明能有几百字,
	 *	那是每次开机都要落一遍的钱, 而界面上只需要一句话.
	 */
	if role := roleLabel(p.role); role != "" {
		spec.Labels["role"] = role
	}
	/**
	 * **先把地方定下来, 定不下来就不起这个进程**.
	 *
	 *	原来是能力那一处算一遍、提示词那一处再算一遍. 现在算一次,
	 *	两处都读它 —— 而且失败要在这儿就说出来: 起来之后再撞
	 *	"这个项目已经派给别人了", 用户看到的是一个刚上线就干不了活的 bot.
	 */
	plan, err := c.plans.plan(p)
	if err != nil {
		return "", err
	}
	/**
	 * 分支进标签.
	 *
	 *	**产物归属靠它**: 界面要答"这个人干出来的东西在哪儿", 交接要答
	 *	"把哪条分支交出去". 而标签跟着 created 事件进账本, 所以这两个
	 *	问题在进程死掉之后照样答得出.
	 */
	if plan.Branch != "" {
		spec.Labels["branch"] = plan.Branch
		// **项目根也要进标签**: 干活目录现在是各自的 worktree, 而"这几个人
		// 在同一个项目里"这件事只有项目根答得出 —— 侧栏按项目分堆靠它,
		// 少了它每个 bot 会各自成一堆(它们的 worktree 路径互不相同).
		spec.Labels["project"] = plan.Project
		if c.roster != nil {
			_ = c.roster.setBranch(p.name, plan.Branch)
		}
	}
	spec.Caps = append(spec.Caps, capsFor(p, currentAsk(), plan)...)
	pid, err := o.Spawn(spec, osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
		if p.tools {
			return serveWorker(ctx, pc, c, p, contextTokensOf(o))
		}
		return converse(ctx, pc, p)
	}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "起不来 %s: %v\n", p.name, err)
	}
	return pid, err
}

// converse 一个 bot 的一生: 一直等话, 来一句答一句, 直到 OS 关机.
//
//	注意它**没有 turn 的概念** —— 它就是一个循环. "一问一答"是
//	这个循环碰巧长成的样子, 不是内核里的一种对象.
//
//	历史留在它自己的地址空间里, 所以多轮记忆天然成立, 前缀缓存也跨轮保持 ——
//	这正是"一次对话 = 一个活着的进程"而不是"每句话起一个新进程"的理由.
func converse(ctx context.Context, pc osinit.ProcessContext, p persona) (any, error) {
	stream := 0
	// 回来的人不再打一次招呼 —— 打了就是复读.
	//
	//	带工具那条路径早就有这道闸, 这条没有. 于是每开一次机,
	//	同一句开场白就在同一个房间里多贴一遍 ——
	//	账本里躺着 8 份"回归跑完 2007 条", 界面照实渲染,
	//	看起来像 bot 得了失心疯. 两条路径必须同一个判据.
	if !p.returning {
		say(pc, nextStream(&stream), "say", p.intro)
	}

	history := []abi.InferMessage{}
	answered := 0
	for {
		in, err := pc.Recv()
		if err != nil || in.Closed {
			return "对话结束", nil
		}
		id := nextStream(&stream)

		if p.asks && answered == 0 {
			res, derr := pc.Decide(abi.DecisionRequest{
				Axis:  abi.AxisNet,
				Scope: "api.github.com",
				Present: abi.PresentSpec{
					Kind:   "approve",
					Title:  "要出网读 PR 列表",
					Detail: "只读 api.github.com，不写任何东西。",
				},
			})
			if derr != nil {
				return nil, derr
			}
			if res.Choice != "yes" {
				say(pc, id, "say", "行，那我不出网，就用本地能看到的部分接着说。")
			}
		}

		history = append(history, abi.InferMessage{Role: "user", Content: in.Text})

		// 流式: 每一小块**当场发出去**, 不攒到最后.
		//
		//	正文和推理走**两条不同的 stream id** —— 客户端按 stream 折行,
		//	混一条 id 的话推理和正文会被折进同一行, 再也分不开.
		thinkID := nextStream(&stream)
		emitted := 0
		res, ierr := pc.InferStream(abi.InferParams{
			System:    systemPrompt(p),
			Messages:  history,
			MaxTokens: 900,
		}, func(d engine.StreamDelta) {
			if d.Reasoning != "" {
				pc.Emit(map[string]any{"stream": thinkID, "channel": "think", "text": d.Reasoning})
			}
			if d.Text != "" {
				pc.Emit(map[string]any{"stream": id, "channel": "say", "text": d.Text})
				emitted += len(d.Text)
			}
		})
		if ierr != nil && emitted > 0 {
			// 说到一半断了: 已经吐出去的留着, 后面补一句说清楚是断了,
			// **不许把前半段抹掉重来** —— 用户已经读过了
			say(pc, nextStream(&stream), "say", "（这段说到一半断了："+ierr.Error()+"）")
			answered++
			continue
		}
		if ierr != nil {
			// **不许拿套话糊过去**: 说不了话就说为什么说不了.
			// 一个假装在思考然后回一段通用废话的 bot, 比一个明说
			// "这台机器没接推理服务"的 bot 糟得多.
			say(pc, id, "say", "我这会儿答不了："+ierr.Error())
			answered++
			continue
		}
		content := strings.TrimSpace(res.Content)
		if content == "" {
			// 空回复到底是"没话说"还是"被截断"完全是两回事
			say(pc, id, "say", "（模型这次没给内容，收尾原因是 "+res.FinishReason+"）")
			answered++
			continue
		}
		history = append(history, abi.InferMessage{Role: "assistant", Content: content})
		// 历史留一个上限: 一个跑一整天的进程不能把上下文撑爆
		if len(history) > 40 {
			history = history[len(history)-40:]
		}
		// 流式已经把正文发完了; 只有退回一次性返回时才需要在这儿补发
		if emitted == 0 {
			say(pc, id, "say", content)
		}
		answered++
	}
}

func systemPrompt(p persona) string {
	return strings.Join([]string{
		"你是 " + p.name + "，跑在 NeoxOS 上的一个进程。" + p.role,
		"跟用户说话像同事，不像客服：不写\"根据您的需求\"这种话，不列一二三除非真的是清单。",
		"跟随用户的语言。答不了就直说答不了，别用套话填。",
		"回答控制在几句话，除非用户明确要长的。",
	}, "\n")
}

func say(pc osinit.ProcessContext, stream, channel, text string) {
	pc.Emit(map[string]any{"stream": stream, "channel": channel, "text": text})
}

func clip(text string, n int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[:n]) + "…"
}

func nextStream(counter *int) string {
	*counter++
	return fmt.Sprintf("s%d", *counter)
}

func mintToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

/**
 * storedToken 持久的观察口凭据 —— 生成一次, 落在家目录里.
 *
 *	原来每次启动都随机: 只有 spawn 我们的 Electron 能从 stdout 接住.
 *	自托管形态里接住它的是**人**(复制进浏览器/客户端), 每次重启都换
 *	等于每次重启都要重配一遍连接. 所以改成: 第一次生成就存下,
 *	以后每次开机都是同一把.
 *
 *	0600, 跟 provider.json 同一个待遇 —— 它就是这台 OS 的钥匙.
 *	换钥匙 = 删掉这个文件重启, 或用 NEOX_OBSERVE_TOKEN 显式指定.
 */
/**
 * observeToken 这台 OS 的钥匙 —— **只有一处说得准**.
 *
 *	原来是 envOr("NEOX_OBSERVE_TOKEN", storedToken()): env 给了就用 env,
 *	而**家目录里那个文件还躺在那儿, 内容是另一把**. 于是容器里
 *	`cat ~/.neox-os/token` 拿到的是一把用不了的钥匙, 而所有请求
 *	回的都是 unauthorized —— 看起来像权限坏了, 其实是两份真相.
 *
 *	如果 bot 照着那个文件读, 连试十几个入口都会得到 unauthorized,
 *	然后开始去反编译二进制找路由. 而它的推断是对的, 只是那个文件在骗它.
 *
 *	所以 env 显式指定时**把文件同步成它** —— 文件永远是当前那把.
 */
func observeToken() string {
	if t := strings.TrimSpace(os.Getenv("NEOX_OBSERVE_TOKEN")); t != "" {
		writeTokenFile(t)
		return t
	}
	return storedToken()
}

// writeTokenFile 把钥匙落到家目录. 0600 —— 跟 provider.json 同一个待遇.
//
//	写不进去不算错: 环境变量那份照样能用, 只是外面读不到 ——
//	stderr 说清就行
func writeTokenFile(t string) {
	path := filepath.Join(neoxHome(), "token")
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == t {
		return
	}
	if err := os.MkdirAll(neoxHome(), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "token 文件写不了(环境变量那份照样能用):", err)
	}
}

func storedToken() string {
	path := filepath.Join(neoxHome(), "token")
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t
		}
	}
	t := mintToken()
	if err := os.MkdirAll(neoxHome(), 0o700); err == nil {
		// 写不进去就退回"这次随机": 能用一次比起不来强, stderr 说清就行
		if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "token 存不下来, 这次用一次性的:", err)
		}
	}
	return t
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── 供应商配置的落盘 ────────────────────────────────────────

// providerStore 把推理配置存在宿主的家目录里.
//
//	**0600, 而且只存在宿主这一侧**. 它带着 API key,
//	所以既不进事件日志, 也不通过观察口读回去.
type providerStore struct {
	path string
	// envKey 启动时环境变量里那把. 它是**种子, 不是资产** —— 只用来
	// 让这次运行有推理服务, 绝不会被 save 写进文件
	envKey string
}

func newProviderStore() *providerStore {
	return &providerStore{
		path:   filepath.Join(neoxHome(), "provider.json"),
		envKey: os.Getenv("NEOX_API_KEY"),
	}
}

func (s *providerStore) load() osinit.ProviderConfig {
	cfg := osinit.ProviderConfig{
		BaseURL: envOr("NEOX_API_BASE", "https://api.deepseek.com"),
		Model:   envOr("NEOX_MODEL_ID", "deepseek-v4-flash"),
		APIKey:  s.envKey,
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		var stored osinit.ProviderConfig
		if json.Unmarshal(raw, &stored) == nil {
			// 存着的优先; env 只当首次的种子
			if stored.BaseURL != "" {
				cfg.BaseURL = stored.BaseURL
			}
			if stored.Model != "" {
				cfg.Model = stored.Model
			}
			if stored.APIKey != "" {
				cfg.APIKey = stored.APIKey
			}
			// 审批档也要读回来.
			//
			//	漏了它的后果不是"少了个设置": 这个档**只对新起的 bot 生效**,
			//	改完当场什么都不变, 要重启才看得见 —— 而重启时它又被丢掉了。
			//	于是用户在界面上选了"都不问", 存也存进去了, 界面下次打开
			//	还显示"都不问", 但 OS 里跑的一直是缺省档。
			//	写得进读不回, 比根本没这个设置更坏: 它看起来是好的。
			if stored.Ask != "" {
				cfg.Ask = stored.Ask
			}
			// 认不认图也要读回来.
			//
			//	跟审批档一个道理: 写得进读不回, 比根本没这个设置更坏 ——
			//	设置页上勾着"能看图", 而 bot 收到图只会说"我看不了".
			cfg.Vision = stored.Vision
			// 想不想也要读回来 —— 同上: 写得进读不回, 比没有这个开关更坏
			cfg.Think = stored.Think
			// 协议同理, 而且这一格读不回的后果更重: 用户在界面上把一个
			// 认不出来的自建网关改成了 OpenAI 兼容, 重启之后又被认回
			// Anthropic —— 整台机器不能推理, 而设置页上写的还是他选的那个
			cfg.Protocol = stored.Protocol
			// 搜索那三格也要读回来 —— 写得进读不回比没这个设置更坏
			cfg.SearchAPI = stored.SearchAPI
			cfg.SearchURL = stored.SearchURL
			cfg.SearchKey = stored.SearchKey
			cfg.CapTokens = stored.CapTokens
			cfg.AutoResume = stored.AutoResume
		}
	}
	cfg.HasKey = strings.TrimSpace(cfg.APIKey) != ""
	/**
	 * **这把 key 是哪来的, 界面必须知道**.
	 *
	 *	环境变量那把从来不落盘(见下面 save 的注释) —— 而设置页据 HasKey
	 *	写着"已经存着一把". 那句话在这种机器上是假的: 关掉再开, key 就没了,
	 *	而用户以为它存着. (这个会话里我自己就撞上过: 重启一次, 推理直接没了.)
	 */
	switch {
	case !cfg.HasKey:
		cfg.KeyFrom = ""
	case s.envKey != "" && cfg.APIKey == s.envKey:
		cfg.KeyFrom = "env"
	default:
		cfg.KeyFrom = "file"
	}
	return cfg
}

// save 落盘.
//
// ── 环境变量里的 key 绝不落盘 ──
//
//	load() 会拿 NEOX_API_KEY 当种子, 于是**任何一次 save 都会把它写进文件**
//	—— 包括在设置里只改了一下权限档那次. 用户把 key 放进环境变量, 要的正是
//	"它只活在这个进程里"; 我们背着他把它抄到磁盘上, 等于替他做了这个决定,
//	而他根本不知道自己已经有了一份长期副本.
//
//	判据是**来源**不是内容: 只有用户在界面上亲手填的那把才存.
//	正好等于环境变量那把 = 它就是环境变量那把.
func (s *providerStore) save(cfg osinit.ProviderConfig) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if s.envKey != "" && cfg.APIKey == s.envKey {
		cfg.APIKey = ""
	}
	cfg.HasKey = strings.TrimSpace(cfg.APIKey) != "" || s.envKey != ""
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再原子替换: 写到一半断电不该留下一个解不开的配置,
	// 那会让下次启动直接没有推理服务
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// withStored 空着的字段沿用存着的那份.
//
//	界面上只改了模型名想试一下, 不该被逼着把 key 再贴一遍 ——
//	而重贴意味着它又在剪贴板里走一遭.
func withStored(store *providerStore, want osinit.ProviderConfig) osinit.ProviderConfig {
	current := store.load()
	if strings.TrimSpace(want.BaseURL) == "" {
		want.BaseURL = current.BaseURL
	}
	if strings.TrimSpace(want.Model) == "" {
		want.Model = current.Model
	}
	if strings.TrimSpace(want.APIKey) == "" {
		want.APIKey = current.APIKey
	}
	// 搜索那把 key 同理 —— 界面上永远拿不到它(只进不出),
	// 所以回传的一定是空, 而空不该被理解成"删掉"
	if strings.TrimSpace(want.SearchKey) == "" {
		want.SearchKey = current.SearchKey
	}
	return want
}

func buildProvider(cfg osinit.ProviderConfig) engine.Provider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil
	}
	// 温度: **缺省不提**, 由供应商自己定.
	//
	//	给一个环境变量是为了基准台能定住它 —— 不定住的话同一个二进制
	//	跑两遍差半分(满分 8), 而一版提示词的效果多半就在这个数量级.
	var temp *float64
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("NEOX_TEMPERATURE")), 64); err == nil {
		temp = &v
	}
	// 协议按地址认, 缺省把 DeepSeek 认到 Messages 那条 ——
	// 官方联网搜索只在那条口上. 见 ProviderConfig.Protocol
	// 想不想 —— 缺省关. 见 ProviderConfig.Think
	return engine.NewProvider(cfg.Protocol, cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Think, temp)
}

/**
 * buildViewer 谁来看图.
 *
 *	**nil 是一个有意义的答案**: 这台机器不认图, 于是 agent 那边不挂
 *	view_image, 而 bot 收到图时直接说"我看不了" —— 不去猜图里是什么.
 *
 *	判据是用户在设置里勾的那一下, 不是模型名. 按名字猜错的表现不是
 *	报错, 是模型一本正经地描述一张它根本没收到的图(见 engine/vision.go).
 *	环境变量那条路留着 —— 命令行起的那些不经过设置页.
 */
// buildSearcher 谁来搜网. nil = 这台机器不能搜 —— 见 ProviderConfig.SearchAPI
func buildSearcher(cfg osinit.ProviderConfig) engine.Searcher {
	// 用户自己配了就听他的 —— 他挑那家是有理由的
	if s := engine.NewSearcher(cfg.SearchAPI, cfg.SearchURL, cfg.SearchKey); s != nil {
		return s
	}
	// 没配的话看供应商自己带不带. **DeepSeek 是带的**(走 Messages
	// 那条口), 于是这台机器不用配任何第三方 key 就能搜网 ——
	// 而在这之前, 没配 = web_search 根本不挂出来
	if engine.WantsAnthropic(cfg.Protocol, cfg.BaseURL) {
		if s := engine.NativeSearchFor(cfg.BaseURL, cfg.APIKey, cfg.Model); s != nil {
			return s
		}
	}
	// 环境变量那条路留着 —— 命令行起的那些不经过设置页
	return engine.SearcherFromEnv()
}

func buildViewer(cfg osinit.ProviderConfig) engine.Viewer {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil
	}
	if cfg.Vision {
		return engine.NewVision(cfg.BaseURL, cfg.APIKey, cfg.Model)
	}
	return engine.ViewerFromEnv(buildProvider(cfg))
}

// contextTokensOf 模型窗口有多大.
//
//	**进程自己不查表**: 它根本不知道自己在用哪个模型, 由 OS 告诉它.
//	供应商不说就用保守缺省 —— 编一个数字比说不知道危险.
func contextTokensOf(o *osinit.OS) int64 {
	provider := o.Provider()
	if provider == nil {
		return 0
	}
	if sized, ok := provider.(engine.ContextSized); ok {
		return sized.ContextTokens()
	}
	return 0
}

// slug 名字 → 目录名.
//
//	**必须一名一目录**. 早先这里把非字母数字一律换成下划线, 于是
//	「测试员」和「写文档」都变成 "___" —— 两个 bot 共用一个工作目录,
//	一个的 edit 会把另一个正在读的文件改掉, 而它们各自都以为
//	自己是那儿唯一的人. 那是领地互斥被破坏, 症状是随机的文件损坏.
//
//	所以: 能读的部分照留(方便人在 Finder 里认), 后面**永远拼一段
//	名字本身的哈希** —— 可读性是加分项, 不撞才是硬要求.
//
// capsFor 起这个 bot 时给它哪些能力.
//
// ── 三档的区别就在这个函数里 ──
//
//	always  什么都不给 —— 于是每一个副作用都越界, 每一次都问你。
//	        这不是绕路, 这就是"每次都问"在能力模型里的正确写法。
//	bounds  给工作目录和跑命令 —— 在自己家里动手不用问, 越界才问。
//	never   连出网也直接给 —— **一次都不问**。
//
//	never 为什么要在这儿给足, 而不是靠"自动批准":
//	自动批准仍然要走一整趟 request_access(它得先撞墙、再问、再拿到),
//	对话里会多出一行"1 次授权", 而用户明明已经说了"别问我"。
//	把能力直接给够, 那一趟才真的消失。
//
//	代价说清楚: never 档下这个 bot 能连任意主机。它仍然出不了自己的
//	工作目录 —— 那道墙在内核, 不受这个开关影响。
func capsFor(p persona, mode osinit.AskMode, plan Plan) []abi.Capability {
	if !p.tools || mode == osinit.AskAlways {
		return nil
	}
	if plan.Dir == "" {
		return nil
	}
	caps := []abi.Capability{
		// **写能力绑的是它自己那块**: worktree 模式下这不是项目根,
		// 而是只属于它的那个工作目录 —— 隔离靠这一行落地, 不靠自觉
		{Axis: abi.AxisWrite, Scope: plan.Dir},
		// proc 轴上 scope 是**命令行**不是路径, 所以给的是 "*".
		//
		//	为什么敢给: 边界不在这条能力上, 在内核 ——
		//	子进程继承同一套 landlock/netns/cgroup, 它跑什么命令
		//	都出不了这个工作目录。
		//
		//	不给的话会撞上一个真实的设计缺口: 授权挂在**对话**上、
		//	只对下一个进程生效(landlock 只能收紧不能放宽), 而这个
		//	bot 是长活进程 —— "下一个进程"永远不来, 于是批准多少次
		//	都还是被拦。那不是安全, 那是死循环。
		{Axis: abi.AxisProc, Scope: "*"},
	}
	if mode == osinit.AskNever {
		caps = append(caps, abi.Capability{Axis: abi.AxisNet, Scope: "*"})
	}
	return caps
}

func slug(name string) string {
	readable := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			readable = append(readable, r)
		case r >= 'A' && r <= 'Z':
			readable = append(readable, r+32)
		default:
			// 连续的非 ASCII 只塌成一个连字符, 不是一串下划线
			if len(readable) > 0 && readable[len(readable)-1] != '-' {
				readable = append(readable, '-')
			}
		}
	}
	head := strings.Trim(string(readable), "-")
	if len(head) > 16 {
		head = head[:16]
	}
	if head == "" {
		head = "bot"
	}
	return head + "-" + nameHash(name)
}

// nameHash 名字的 6 位十六进制指纹 —— FNV-1a, 够稳也够短
func nameHash(name string) string {
	var h uint32 = 2166136261
	for _, b := range []byte(name) {
		h ^= uint32(b)
		h *= 16777619
	}
	return fmt.Sprintf("%06x", h&0xffffff)
}

// ledgerPath 账本放哪. 跟配置同一个目录 —— 都是"这台机器的状态"
func ledgerPath() string { return filepath.Join(neoxHome(), "events.jsonl") }

// ── 界面上建过的 bot ────────────────────────────────────────

// savedBot 名册里的一条. **只存身份, 不存能力** ——
// 给什么能力是宿主每次开机按当时的策略决定的, 存下来等于把
// 一次授权永久化了.
type savedBot struct {
	Name string `json:"name"`
	// Thread 在哪个房间. 空 = 一对一.
	//
	//	名册是**房间归属的真相源**, 连内置的那几个 bot 也算 ——
	//	把人拉进房间之后, 这件事必须活过重启, 否则下次开机
	//	他又回到原来那儿了.
	Thread string `json:"thread,omitempty"`
	// Work 它的项目目录. **必须活过重启** —— 不记的话下次开机
	//	它回到匿名目录, 而用户以为它还守着那个项目: 界面上一切正常,
	//	它却在另一个地方干活, 上一次的成果一个字都看不见.
	Work string `json:"work,omitempty"`
	// Role 他负责什么.
	//
	//	**拉进来的人必须记住岗位**: 不记的话重启之后他变成一个通用助手,
	//	上线只会问"要我干什么" —— 而当初拉他进来正是为了不用再交代一遍.
	Role string `json:"role,omitempty"`
	// Builtin 内置 persona 的归属覆盖 —— 只记房间, 不记身份
	Builtin bool `json:"builtin,omitempty"`
	/**
	 * Gone 这个 bot 被删过 —— **别再自己起回来**.
	 *
	 *	内置那几个(研究/值守/构建/发版/回归/文案)的身份写在代码里,
	 *	删掉只是杀了进程和账本, 下次开机它们照样长回来. 用户的原话:
	 *	"让你把所有的 BOT 都清掉…你还是没有清".
	 *
	 *	他说得对: 从他的角度这就是没清掉 —— 而且这种"清了又回来"最费解,
	 *	因为界面上确实清空过一次.
	 *
	 *	名册里留一块墓碑: 删过的名字不再起. 他要是又建一个同名的,
	 *	墓碑跟着撤掉(见 remember).
	 */
	Gone bool `json:"gone,omitempty"`
	// InitGit 老字段: 早先要用户点头才走 git. 现在**默认全都收**
	// (见 newWorkPlanner 那段), 这一格只为读得懂旧的名册文件而留着.
	InitGit bool `json:"initGit,omitempty"`
	// Branch 它的产物在哪条分支上. 由系统写, 不由用户填
	Branch string `json:"branch,omitempty"`
}

func (s savedBot) persona() persona {
	return persona{
		name: s.Name, app: "custom-" + slug(s.Name), thread: s.Thread,
		work:    s.Work,
		initGit: s.InitGit,
		intro:   introOf(s.Role),
		role:    roleOf(s.Role),
		tools:   true,
	}
}

type botRoster struct {
	mu   sync.Mutex
	path string
}

func newBotRoster() *botRoster {
	return &botRoster{path: filepath.Join(neoxHome(), "bots.json")}
}

func (r *botRoster) load() []savedBot {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return nil
	}
	var out []savedBot
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func (r *botRoster) add(bot savedBot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	kept := current[:0]
	for _, seen := range current {
		// 同名当成同一个 —— slug 也是按名字算的, 记两条会起两个抢同一个目录
		if seen.Name == bot.Name {
			// **墓碑要撤掉**: 他又建了一个同名的, 那就是想要它回来
			if seen.Gone {
				continue
			}
			return nil
		}
		kept = append(kept, seen)
	}
	current = append(kept, bot)
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// remove 从名册里划掉几个 —— 不划的话下次开机它自己又起来了
/**
 * remove 从名册里删掉, 并**立一块墓碑**.
 *
 *	光删不够: 内置那几个的身份写在代码里, 名册里本来就没有它们的行,
 *	删完下次开机照样长回来 —— 用户看到的是"清了又回来".
 */
func (r *botRoster) remove(names map[string]bool) error {
	if len(names) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := []savedBot{}
	for _, bot := range r.load() {
		if !names[bot.Name] {
			kept = append(kept, bot)
		}
	}
	for name := range names {
		kept = append(kept, savedBot{Name: name, Gone: true})
	}
	raw, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// buried 删过的那些名字 —— 它们不该自己长回来
func (r *botRoster) buried() map[string]bool {
	out := map[string]bool{}
	for _, bot := range r.load() {
		if bot.Gone {
			out[bot.Name] = true
		}
	}
	return out
}

// rooms 名册里记着的房间归属
func (r *botRoster) rooms() map[string]string {
	out := map[string]string{}
	for _, bot := range r.load() {
		out[bot.Name] = bot.Thread
	}
	return out
}

// works 每个 bot 被派到哪个工作区. 空 = 没派过, 用系统分的匿名目录.
func (r *botRoster) works() map[string]string {
	out := map[string]string{}
	for _, bot := range r.load() {
		if bot.Work != "" {
			out[bot.Name] = bot.Work
		}
	}
	return out
}

// roles 用户改过岗位的那几个.
//
//	**内置的也算**: 身份(名字、开场白)是代码里的, 岗位是用户定的 ——
//	不在开机那一圈盖回去的话, 改过的岗位活不过一次重启, 而界面上
//	看不出任何异常.
func (r *botRoster) roles() map[string]string {
	out := map[string]string{}
	for _, bot := range r.load() {
		if bot.Role != "" {
			out[bot.Name] = bot.Role
		}
	}
	return out
}

// setWork 把一个 bot 派到某个工作区 —— 跟 setRoom 同一套规矩.
//
//	**内置 persona 也进名册**: 身份是代码里写死的, 而"在哪儿干活"是用户
//	定的. 不记的话下次开机它又回到那个匿名目录, 而用户以为它还守着项目 ——
//	界面上一切正常, 它却在别处干活, 上次的成果一个字都看不见.
func (r *botRoster) setWork(name, work string, builtin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	updated := false
	for i := range current {
		if current[i].Name == name {
			current[i].Work = work
			updated = true
			break
		}
	}
	if !updated {
		current = append(current, savedBot{Name: name, Work: work, Builtin: builtin})
	}
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// setRole 记下一个 bot 的岗位.
//
//	**内置的也记**: 身份(名字、开场白)写在代码里, 岗位是用户改的.
//	不记的话下次开机它又回到代码里那句, 而用户以为改过了.
func (r *botRoster) setRole(name, role string, builtin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	updated := false
	for i := range current {
		if current[i].Name == name {
			current[i].Role = role
			updated = true
			break
		}
	}
	if !updated {
		current = append(current, savedBot{Name: name, Role: role, Builtin: builtin})
	}
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// setRoom 记下一个 bot 现在在哪个房间.
//
//	内置 persona 也进名册 —— 只记归属不记身份, 因为身份是代码里写死的,
//	而归属是用户改的. 两者混在一起的话, 改一次代码就会把用户的安排冲掉.
//
// setBranch 记下这个 bot 的产物在哪条分支上.
//
//	**只记不改**: 分支名由 BranchOf 从名字算出来, 这里存的是一份给界面
//	和交接用的副本 —— 进程没起来的时候(重启前)也答得出"它的活在哪儿".
func (r *botRoster) setBranch(name, branch string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.load()
	for i := range list {
		if list[i].Name != name {
			continue
		}
		if list[i].Branch == branch {
			return nil
		}
		list[i].Branch = branch
		raw, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
			return err
		}
		tmp := r.path + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, r.path)
	}
	return nil
}

func (r *botRoster) setRoom(name, thread string, builtin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	updated := false
	for i := range current {
		if current[i].Name == name {
			current[i].Thread = thread
			updated = true
			break
		}
	}
	if !updated {
		current = append(current, savedBot{Name: name, Thread: thread, Builtin: builtin})
	}
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// findPersona 按名字找一个 bot 的身份, 顺便说清它是不是内置的.
//
//	**内置和界面上建的要分开记**: 内置的身份写在代码里, 名册只记用户改过的
//	那部分(房间、工作区); 拿名册那条去起进程会起出一个 bot 标签完全不同的
//	同名分身 —— 界面上就是同一个人出现两次, 而且两个都活着、各说各的.
func findPersona(roster *botRoster, name string) (persona, bool, error) {
	// **两套内置阵容都认**: 用户中途切了语言, 名册里记的还是老语言的名字,
	// 只认当前语言的话那几个内置 bot 就再也起不来了 —— 而它们的历史还在.
	for _, set := range [][]persona{personas, personasEN} {
		for i := range set {
			if set[i].name == name {
				return set[i], true, nil
			}
		}
	}
	if roster == nil {
		return persona{}, false, fmt.Errorf("没有叫 %q 的 bot", name)
	}
	for _, saved := range roster.load() {
		if saved.Name == name {
			return saved.persona(), false, nil
		}
	}
	return persona{}, false, fmt.Errorf("没有叫 %q 的 bot", name)
}

// roleOf 名册里没记岗位的(界面上手建的), 给一句通用的.
func roleOf(role string) string {
	if strings.TrimSpace(role) == "" {
		return "你是用户建的一个通用助手。"
	}
	return role
}

// introOf 有岗位的人一上线就报自己管哪一摊, 而不是问"要我干什么" ——
// 后者等于把活又推回给用户, 而拉他进来正是为了不推回去.
func introOf(role string) string {
	if strings.TrimSpace(role) == "" {
		return "我上线了。要我干什么?"
	}
	return "报到。" + role
}

// isRecruit 这条决策是不是"要不要多雇一个人".
//
// 判据取自决策本身带的能力轴和 scope(recruit 用 proc:bot:<名字>) ——
// 不靠标题里的字去猜: 标题是给人看的话, 改一次文案判据就失效了.
func isRecruit(o *osinit.OS, did string) bool {
	for _, p := range o.Decisions().Pending() {
		if string(p.DID) != did {
			continue
		}
		return p.Request.Axis == abi.AxisProc && strings.HasPrefix(p.Request.Scope, "bot:")
	}
	return false
}

// modelNameOf 这台机器底下跑的是哪个模型.
//
//	**进程自己不查表**: 它根本不知道用的是哪家, 由 OS 告诉它 ——
//	跟 contextTokensOf 同一条道理.
//
//	没配推理服务就回空, 那时候提示词里那一层不出现, 它照实说不知道.
func modelNameOf(o *osinit.OS) string {
	provider := o.Provider()
	if provider == nil {
		return ""
	}
	return provider.Model()
}
