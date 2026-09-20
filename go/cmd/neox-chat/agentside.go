package main

import (
	"fmt"
	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"
	"os"
)

// osAgent 把 chat 侧的 agent 跑起来. 单独一个文件是为了让 main.go
// 只管"人机交互", 这里只管"跑任务".
type osAgent struct{ cli *osinit.AbiClient }

// searchFn 搜索这条线接不接得上 —— **由 OS 说了算**.
//
// 返回 nil 时工具表里根本不会出现 web_search. 这跟"挂上去然后报错"
// 是两回事: 后者模型会先对用户许诺"我去搜一下", 再拿到一句
// "这台机器没配搜索" —— 用户看到的是一次失约, 而不是一开始就没这个能力.
func searchFn(cli *osinit.AbiClient) func(string, int) ([]abi.SearchHit, error) {
	if !cli.HasSearch() {
		return nil
	}
	return func(query string, limit int) ([]abi.SearchHit, error) {
		res, err := cli.Search(abi.SearchParams{Query: query, Limit: limit})
		if err != nil {
			return nil, err
		}
		return res.Hits, nil
	}
}

// seeFn 看图这条线接不接得上 —— 同 searchFn, 由 OS 说了算
func seeFn(cli *osinit.AbiClient) func(string, string, string) (abi.SeeResult, error) {
	if !cli.HasVision() {
		return nil
	}
	return func(mediaType, dataB64, question string) (abi.SeeResult, error) {
		return cli.See(abi.SeeParams{
			MediaType: mediaType, DataB64: dataB64, Question: question})
	}
}

// recallFn 查过去. 账本是 OS 的内核对象, 生产路径上一直在,
// 所以这条不像搜索/看图那样按配置开关 —— 没账本的环境才是测试.
func recallFn(cli *osinit.AbiClient) func(string, int) ([]abi.RecallHit, error) {
	return func(query string, limit int) ([]abi.RecallHit, error) {
		res, err := cli.Recall(abi.RecallParams{Query: query, Limit: limit})
		if err != nil {
			return nil, err
		}
		return res.Hits, nil
	}
}

func (a *osAgent) run(task string) {
	// 闹钟走事件: 进程够不着 OS 的对象, 而**事件日志本来就是闹钟的
	// 持久化载体** —— 落进日志的那一刻它就已经活得比进程长了
	ts := agent.NewToolSet(agent.DefaultToolsWith(
		func(atMs int64, text string) error {
			a.cli.Emit(map[string]any{"phase": "remind", "at": atMs, "text": text})
			return nil
		},
		// 命名同样走事件 —— 跟闹钟一个理由: 事件日志本来就是持久化载体.
		// 代价是拿不到回执(比如"这儿还没有位置信号"), 所以工具结果
		// 只能说"记下了", 真出错时用户会在终端看到 OS 那一侧的报错
		func(name string) (string, error) {
			a.cli.Emit(map[string]any{"phase": "name_place", "name": name})
			return "记下了: 以后到这儿会说「" + name + "」。" +
				"(如果手机还没传过位置, OS 会在终端上报错)", nil
		},
		// 长期关注同样走事件 —— 跟闹钟一个理由: 事件日志就是持久化载体
		func(kind, place, say, raw string) error {
			a.cli.Emit(map[string]any{"phase": "watch",
				"kind": kind, "place": place, "say": say, "raw": raw})
			return nil
		},
		// 撤销同样走事件. **代价**: 拿不到"撤没撤成"的回执, 所以
		// 工具结果只能说"已经交给 OS 了" —— 撤一个不存在的 id 时,
		// 用户会在终端上看到 OS 那侧的报错.
		// (这跟闹钟那次是同一个取舍, 而那次的教训是: 能在本地验的
		// 别留给对端. 这里 agent 侧唯一能验的是"id 非空、前缀合法".)
		func(id string) (string, error) {
			if len(id) == 0 || (id[0] != 'w' && id[0] != 'g') {
				return "", fmt.Errorf(
					"%q 不是合法的 id: 提醒是 w 开头, 关注是 g 开头。"+
						"先读 .neox/state.txt 照方括号里的念", id)
			}
			a.cli.Emit(map[string]any{"phase": "cancel", "id": id})
			return "已经交给 OS 撤了。撤不掉的话(比如这条已经不在了), " +
				"用户会在终端上看到报错 —— 别对他打包票说一定撤掉了。", nil
		},
		// 搜索: **这台机器配了才挂**. OS 在握手时告诉我们有没有 ——
		// 进程自己猜不出来, 而摆一个配不出结果的工具比没有更糟.
		//
		// 这条走的是同步调用(不是 Emit): 搜索要拿回结果才有意义,
		// 而闹钟/命名那几条只是"记下来", 拿不到回执也能用.
		searchFn(a.cli),
		// 看图: 同上, **这台机器认图才挂**.
		//
		// 认不认由 OS 定 —— 主模型自己认图就用它, 不认就用另配的
		// 视觉供应商, 两个都没有就是不认. 进程不需要知道是哪一种.
		seeFn(a.cli),
		recallFn(a.cli),
		// 拉人: 终端这条路是**一个进程一段对话**, 没有"起个同事"的概念 ——
		// 起得了才挂, 见 DefaultToolsWith
		nil,
		// 交办: 同上, 这条路没有房间, 也就没有"同屋的人"
		nil))
	// 页存储 + 地址空间: 历史只增不改, 前缀缓存才可能命中
	store := engine.NewPageStore()
	var win *agent.Window
	// 被授权接续一段旧对话就先把它装回来. 取不到是常态(全新对话).
	if evs, err := a.cli.History(); err == nil && agent.HasConversation(evs) {
		win = agent.RestoreWindow(store, evs, task)
		a.cli.Emit(map[string]any{"phase": "resumed", "events": len(evs)})
	} else {
		win = agent.NewWindow(store, task, 0)
	}
	defer win.Release()
	// 预算跟着模型窗口走. OS 在握手时告诉我们窗口有多大 ——
	// 进程自己不查表, 它根本不知道自己在用哪个模型.
	bud := agent.NewBudgeter(a.cli.ContextTokens())
	win.UseBudgeter(bud)

	work := envOr("NEOX_WORK", "/agentwork")
	ag := &agent.Agent{
		ABI: a.cli,
		// Writable 要跟真实授权一致. 这里原来写死 "site/ 目录",
		// 而能力集早就是整个卷了 —— 提示词比现实更窄, 它会以为
		// 自己动不了工作目录里的东西, 白白少做事或者白白申请一次授权.
		Model: &agent.LLM{ABI: a.cli, Tools: ts, Writable: "工作目录(整个卷)",
			// **这条路跑在真内核上**: landlock / netns / cgroup 真的挡得住,
			// 所以可以对模型说"越界会被当场拒". console 那条(in-proc)不行.
			Enforced: true,
			Window:   win, Budgeter: bud,
			// 这台机器上还有哪些对话 —— 环境事实, 见 layerRecent
			Recent:      os.Getenv("NEOX_RECENT"),
			Corrections: os.Getenv("NEOX_CORRECTIONS"),
			// 单次输出额度. 缺省 16k(见 model_llm.go 的说明).
			//
			// 这个旋钮用于复现截断那条**罕见但昂贵**的路径(发生过 6 次,
			// 每次白烧 16000 输出 token), 靠提示词逼不出来, 而只在单元测试里
			// 验过就发出去, 等于没验过真链路. 压小它就能确定性复现.
			MaxTokens: envInt("NEOX_MAX_OUTPUT_TOKENS", 0)},
		Tools: ts,
		// Sys 传进工具箱 —— request_access 要靠它把申请送到用户面前
		Box:    agent.Toolbox{Root: work, Sys: a.cli},
		Window: win,
		// **默认无限** —— 步数不是有意义的度量, 花费由止损线管、跑飞由停滞检测管.
		// 客户端那边同样是 DEFAULT_MAX_ITERATIONS = 0.
		MaxSteps: envInt("NEOX_MAX_STEPS", 0),
	}
	if err := ag.Serve(task); err != nil {
		ag.ABI.Emit(map[string]any{"phase": "fatal", "err": err.Error()})
	}
}

// runProactive 常驻主动进程.
//
// ── 它跟对话进程共用几乎所有机制, 只有三处不同 ──
//
//	提示词   感知判断那一套(2257 字节), 不是对话那套(12529)
//	工具     只有 notify_user. 它的职责是判断, 不是干活
//	能力集   一条都没有. 一个被外部信号唤醒的常驻进程如果还能读写文件,
//	         那么任何能往采集口投信号的东西都获得了一条间接执行路径
//
// **它是个长期存活的进程**, 不是每个摘要起一次:
// 起一次要重建前缀、丢掉"这件事我上次说过了"的记忆, 而那正是
// "别重复打扰"最需要的信息.
func (a *osAgent) runProactive() {
	ts := agent.NewToolSet([]agent.Tool{
		agent.NotifyTool(func(text, why string) {
			// 走事件流出去 —— UI 只是订阅者之一, 这里同样成立:
			// 终端、推送、语音都可以订阅同一条 notify
			a.cli.Emit(map[string]any{"phase": "notify", "text": text, "why": why})
		}),
	})
	store := engine.NewPageStore()
	win := agent.NewWindow(store, proactiveStandingOrder(os.Getenv("NEOX_TASK")), 0)
	defer win.Release()
	bud := agent.NewBudgeter(a.cli.ContextTokens())
	win.UseBudgeter(bud)

	ag := &agent.Agent{
		ABI: a.cli,
		Model: agent.NewProactiveLLM(&agent.LLM{
			ABI: a.cli, Tools: ts, Window: win, Budgeter: bud,
			// 主动进程没有可写范围 —— 它不写东西
			Writable: "(无)"}),
		Tools:  ts,
		Box:    agent.Toolbox{Root: envOr("NEOX_WORK", "/agentwork"), Sys: a.cli},
		Window: win,
	}
	// **手上没东西就不推理.**
	//
	// 真机长跑里看到的: 进程刚起来日志里就是
	//
	//	[用量 prompt=1099 缓存=1024(93%) 输出=90]
	//	待命中。当前没有需要判断的摘要或事件——什么都不做，直接收工。
	//
	// 因为 NEOX_TASK 那句常驻指令被当成了第一句话, Serve 立刻跑了一整轮.
	//
	// 不只是浪费一次调用(每回收一次还要再烧一次, 每 30 份摘要一回收);
	// 更糟的是它在**没有任何依据**的情况下被要求判断"要不要打扰用户",
	// 而模型在没有依据时的输出是不可预期的 —— 那正是这套设计最怕的:
	// 判断力一飘, 用户就把通知关掉, 真正重要的那次也到不了他.
	//
	// 常驻指令照样进上下文(它得知道自己是干什么的), 只是不触发一轮.
	if err := ag.Serve(proactiveFirstTurn(os.Getenv("NEOX_TASK"))); err != nil {
		ag.ABI.Emit(map[string]any{"phase": "fatal", "err": err.Error()})
	}
}

// proactiveFirstTurn 主动进程开局要不要跑一轮 —— **不要**.
//
// 它是"没人在等它说话"的那一侧: 沉默才是常态, 而开局那一轮手上
// 一份摘要都没有. 见 runProactive 里的说明.
func proactiveFirstTurn(task string) string { return "" }

// proactiveStandingOrder 常驻指令 —— 进上下文, 不触发推理.
//
// 丢了的话第一份摘要来时它不知道自己该沉默, 而"默认不打扰"正是
// 这个进程存在的全部意义
func proactiveStandingOrder(task string) string { return task }
