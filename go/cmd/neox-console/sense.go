package main

// 感知层 —— 接进出货的这个二进制.
//
// ── 为什么这个文件必须存在 ──
//
//	信号总线、摘要路由、打扰预算、日报、在场、地点、关注, 这七样都写完了,
//	而且都有单测和真机重放记录. 它们只挂在 neox-chat(终端 CLI)上,
//	而**用户手上跑的是 neox-console** —— Docker 里那个、手机连的那个.
//
//	于是同一个仓库里出现了这样的事: components.go 连着传三个 nil,
//	注释写着"这台机器没接位置信号"; 手机端那个 signal() 是死代码,
//	而且它 POST 的路由在这台机器上根本不存在.
//
//	盘点的结论是"不缺机制, 缺接线". 这个文件就是那截线.
//
// ── 一条设计: 出口只有一个 ──
//
//	neox-chat 那边所有出口都是 fmt.Printf —— 它有终端. 这边没有.
//	所以这里每一条出口都落到 Deliveries(投递总线): 摘要判出来的、
//	日报、采集端失联, 全部走同一个 delivery 事件, 手机端只认这一种形状.
//
//	**"没地方说"必须变成"换个地方说", 不能变成"不说"**: 主动进程没起来
//	的时候终端仍可显示消息, 但这里没有终端出口; 如果直接丢弃就是静默
//	失败 —— 那正是感知层最糟的失败方式.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"
	"github.com/neox-os/neox-os/sense"
)

// homeLatLon 配在环境变量里的家 —— "31.86,117.28".
//
//	**这是一条退路, 不是主路**: 主路是手机报位置. 但一台还没接任何
//	位置采集端的机器也该查得到天气 —— 否则"下雨提醒带伞"这条链
//	在接上手机之前一天都验证不了.
func homeLatLon() (float64, float64, bool) {
	raw := strings.TrimSpace(os.Getenv("NEOX_HOME_LATLON"))
	if raw == "" {
		return 0, 0, false
	}
	var lat, lon float64
	if _, err := fmt.Sscanf(raw, "%f,%f", &lat, &lon); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ NEOX_HOME_LATLON=%q 解不开, 要 \"纬度,经度\" 这种形状\n", raw)
		return 0, 0, false
	}
	return lat, lon, true
}

// senseStack 接好之后的一套 —— 主要是为了让 main 只拿到一个 stop
type senseStack struct {
	bus     *osinit.SignalBus
	routine *osinit.Routine
	people  *osinit.People
	devices *osinit.Devices
	world   *osinit.World
	rules   *osinit.Rules
	places  *osinit.Places
	watches *osinit.Watches
	budget  *osinit.InterruptBudget
	stop    func()
}

// senseSource 感知层自己往投递里说话时署的名.
//
//	**不署某个 bot 的名**: 采集端失联、日报, 这些不是哪个 bot 说的,
//	是这台机器说的. 署成"值守"的话用户会去问值守, 而它一无所知.
const senseSource = "感知"

// buildSense 把感知层接起来.
//
//	返回 nil 表示这台机器不开感知 —— 调用方照常跑, 只是少一段能力.
//	**不是错误**: 一台只用来跟 bot 说话的机器完全可以不接任何采集端.
func buildSense(o *osinit.OS, comp *components) *senseStack {
	ctxTokens := contextTokensOf(o)
	log := o.Log()
	people := osinit.NewPeople(log)
	devices := osinit.NewDevices(log)
	places := osinit.NewPlaces(log)
	// 此刻的世界 —— bot 问"现在什么情况"时答的就是它. 见 osinit/world.go
	world := osinit.NewWorld(nil)
	// 长期规律 —— "他常去哪几个地方". 见 osinit/routine.go
	routine := osinit.NewRoutine(log, nil)
	watches := osinit.NewWatches(log)

	// 打扰预算 —— **提示词里那三条判据是建议, 这里是硬闸**.
	//
	//	一天几次是可配的, 但必须有个数: 没有上限的话, "值不值得说"
	//	这件事就完全交给模型当天的心情了.
	budget := osinit.NewInterruptBudget(log, envInt("NEOX_INTERRUPT_PER_DAY", 3), nil)
	budget.UseDeliveries(comp.deliveries)

	// 主动进程 —— 摘要投给它, 它判断值不值得打扰.
	//
	//	**一条能力都不给**(ProactiveCaps 返回 nil): 它只看摘要、只说话.
	//	一个被外部信号唤醒的常驻进程如果还能读写文件, 那么任何能往采集口
	//	投信号的东西都获得了一条间接执行路径 —— 而感知层收的是位置和来电,
	//	攻击面本来就是全系统最大的.
	var senseProc abi.ProcessID
	spawnSense := func() (abi.ProcessID, error) {
		return o.Spawn(abi.ProcessSpec{
			App: "sense", Name: "感知判断",
			Caps: agent.ProactiveCaps(),
			// 它常驻, 所以止损线按"一天"配而不是按"一个任务"
			Budget: &abi.Budget{Tokens: p64(int64(envInt("NEOX_SENSE_BUDGET_TOKENS", 2000000)))},
			Labels: map[string]string{"bot": "sense", "name": "感知判断"},
		}, osinit.InprocBody{
			Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
				return serveSense(ctx, pc, comp, ctxTokens)
			},
		})
	}
	if pid, err := spawnSense(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 主动进程起不来: %v —— 摘要会直接投给你\n", err)
	} else {
		senseProc = pid
	}

	wired := osinit.BuildSenseStack(osinit.SenseStack{
		Log: log, Places: places, Watches: watches, Budget: budget,
		Window:        time.Duration(envInt("NEOX_SENSE_WINDOW_SEC", 300)) * time.Second,
		MaxBatch:      envInt("NEOX_SENSE_MAX_BATCH", 50),
		Lateness:      time.Duration(envInt("NEOX_SENSE_LATENESS_SEC", 120)) * time.Second,
		BackfillQuiet: time.Duration(envInt("NEOX_SENSE_BACKFILL_QUIET_SEC", 30)) * time.Second,
		UrgentGrace:   time.Duration(envInt("NEOX_SENSE_URGENT_GRACE_SEC", 900)) * time.Second,

		// 每条信号都让世界模型看一眼 —— 它记的是同一类的**最新那条**,
		// 于是"现在在哪""家里有没有人"随时答得出, 而不用把整条流塞进上下文
		//
		//	求规则也挂在这儿: **事实变了才可能有规则变**. 单独起一个
		//	定时器去轮询的话, 要么慢(错过窗口), 要么白烧(大多数时候
		//	什么都没变) —— 而信号本来就是唯一会改变事实的东西.
		OnSignal: func(s abi.Signal) {
			// 手机日历上的日程进同一份待办表 —— 当普通信号的话它只会
			// 进"最新一条", 而日历的价值恰恰在一批
			if comp.agenda != nil {
				comp.agenda.Observe(s)
			}
			world.Observe(s)
			// 带坐标的信号顺路喂给规律层 —— **在这儿而不是在
			// world 里面**: 世界模型答的是"现在", 规律答的是"平时",
			// 两件事共用一条输入但不该互相知道
			if lat, ok1 := s.Body["lat"]; ok1 {
				if lon, ok2 := s.Body["lon"]; ok2 {
					routine.Observe(devices.OwnerOf(s.Source),
						asFloat64(lat), asFloat64(lon), s.At)
				}
			}
			if comp.rules != nil {
				comp.rules.Tick()
			}
		},

		ToProcess: func(text string) bool {
			return senseProc != "" && o.Send(senseProc, text, "sense")
		},
		// 主动进程没起来 —— **直接投给人, 别丢**.
		//
		//	这条路走到的时候, 正好是判断者坏了的时候, 而那也正是最
		//	需要人知道的时刻. 带上"没有主动进程"这个理由: 用户看到
		//	一条没经过筛选的摘要, 得知道为什么它没被筛过.
		ToTerminal: func(d osinit.Digest, text string) {
			comp.deliveries.Post(osinit.Deliver{
				From: senseSource, Kind: osinit.DeliverProactive,
				Text: text,
				Why:  fmt.Sprintf("%s · %d 条 · 主动进程没起来, 这条没经过筛选", d.Reason, d.Count),
			})
		},
		// 用户明确说过要盯的事 —— **不过预算**. 他自己要的, 不算打扰
		OnWatchHit: func(hit string) {
			comp.deliveries.Post(osinit.Deliver{
				From: senseSource, Kind: osinit.DeliverProactive,
				Text: hit, Why: "你说过要盯这件事", Urgent: true,
			})
		},
		Respawn: func() bool {
			pid, err := spawnSense()
			if err != nil {
				return false
			}
			// **"起来了"不等于"活着"**: cgroup 没权限的环境里进程创建
			// 成功、几十毫秒后才死, 而 Send 在那几十毫秒里返回成功 ——
			// 于是摘要被投进一个马上就要死的进程的收件箱, 既不到判断者
			// 也不到人.
			time.Sleep(500 * time.Millisecond)
			if info, ok := o.Info(pid); ok && info.State.IsTerminal() {
				return false
			}
			senseProc = pid
			return true
		},
		RecycleEvery: envInt("NEOX_SENSE_RECYCLE_EVERY", 30),
		// 回收 = 换一个新的判断者, 上下文清零.
		//
		//	它每次只做一个判断("值不值得说"), 却在积累几十份跟这次判断
	//	无关的旧摘要 —— 长时间运行时上下文会线性无界增长. 它真正需要的
		//	历史只有"我今天说过什么", 而那个 OS 手里就有(budget.SaidNote).
		Recycle: func() {
			old := senseProc
			pid, err := spawnSense()
			if err != nil {
				return // 起不来就接着用旧的, 别把唯一那个也弄没了
			}
			senseProc = pid
			if old != "" {
				o.CloseInbox(old)
			}
		},
		// 采集端失联 —— **这是"系统坏了", 不是"它没事找事"**.
		//
		//	所以不占打扰额度、也不过模型: 额度防的是它话多, 而这条是
		//	感知层这一路瞎了. 走 needs_you 那条通道 —— 能修的只有人:
		//	去看采集端进程还在不在, 或者去换一个过期的凭据.
		OnStale: func(st []osinit.StaleCollector) {
			for _, s := range st {
				// **失联和瞎了要分开说**: 一个是去看进程, 一个是去看凭据 ——
				// 混成一句话的话用户会照着错的方向查
				text := fmt.Sprintf("采集端「%s」已经 %s 没消息了", s.Source, s.Silent.Round(time.Second))
				why := fmt.Sprintf("它平时每 %s 报一次 —— 感知层这一路现在是瞎的", s.Pace)
				if s.Blind {
					text = fmt.Sprintf("采集端「%s」还在, 但拿不到数据", s.Source)
					why = s.Why
				}
				comp.deliveries.Post(osinit.Deliver{
					From: senseSource, Kind: osinit.DeliverNeedsYou,
					Text: text, Why: why,
				})
			}
		},
		OnBack: func(srcs []string) {
			for _, s := range srcs {
				comp.deliveries.Post(osinit.Deliver{
					From: senseSource, Kind: osinit.DeliverProactive,
					Text: fmt.Sprintf("采集端「%s」又回来了", s),
					Why:  "上一条说它失联了, 这条是收尾 —— 不然你不知道要不要去修",
				})
			}
		},
	})

	// 日报 —— 攒下的东西**唯一不需要用户主动**的出口.
	//
	//	没有它, "延后"跟"丢掉"的区别很小: 用户不知道有东西攒着,
	//	也就永远不会去问.
	//
	//	它不占打扰额度: 一天一次、时间固定, 是可预期的, 而可预期的
	//	打扰不消耗信任.
	daily := osinit.NewDailyReport(log, budget, envInt("NEOX_DAILY_HOUR", 21), nil, nil)
	daily.UseDeliveries(comp.deliveries)

	// **恢复全收在 RestoreAll 里**: 原来散着七处 for 循环, 加第八个
	// 状态时忘掉一处, 症状是"看起来一切正常, 只是某件事悄悄不算数了".
	// 顺序也在那边定死(预算先于闹钟、静音先于捞积压)
	// ── 接线要在 RestoreAll 之前 ──
	//
	//	装回来的位置事实要查地名(places)、要认主人(devices).
	//	顺序反了的话, 装回来的那些全是"一个还没起过名的地方",
	//	而且一条都不属于任何人 —— **而它不会报错**.
	//
	//	地名(手机只报经纬度)、家里有没有人、下一个提醒是什么:
	//	这三样世界模型自己推不出来
	world.Use(places, wired.Presence, comp.timers)
	// 这条信号是谁的 —— 从它是从哪台设备来的推出来.
	// 家里的传感器故意没有主人, 于是它们的事实屋里每个人都看得见
	world.UseOwner(devices.OwnerOf)
	// 一个还在报到的采集端, 它报的最后一个位置就仍然成立 ——
	// 它是按移动触发的, 没报新的就是没挪. 见 World.alive
	world.UseAlive(wired.Bus.Alive)

	prior := o.Log().Snapshot()
	//
	//	**Timers 传 nil**: 闹钟在 comp 建出来的那一刻就已经恢复过了
	//	(main.go 里 NewTimers 紧接着 Restore). 再喂一遍的话同一条
	//	闹钟会进两次待办表 —— 到点响两声, 而用户只设过一次.
	//
	//	那边先恢复不违反 restore.go 里"预算必须先于闹钟"那条: 那条防的是
	//	补响挤占打扰额度, 而**闹钟本来就不占额度**(DeliverRemind 一定要
	//	送到, 是用户自己设的).
	sum := osinit.RestoreAll(prior, osinit.RestoreTargets{
		Timers: nil, People: people, Devices: devices, Places: places,
		World: world, Watches: watches,
		Budget: budget, Daily: daily, Bus: wired.Bus, Presence: wired.Presence,
	})
	for _, l := range sum.Lines() {
		fmt.Fprintln(os.Stderr, l)
	}

	comp.world = world
	comp.routine = routine
	// 规律是攒出来的, 一次重启不该清零 —— 攒够要好几天
	routine.Restore(flatEvents(prior))
	comp.people = people
	// 反过来问设备要东西时要够得着它们 —— 见 Devices.Ask
	comp.devices = devices

	// 条件触发 —— "下雨且我还在家就提醒我带伞".
	//
	//	**规则响的时候不过打扰预算**: 它是用户自己立的规矩, 跟闹钟
	//	同一类, 不是"它没事找事". 但 urgent 由规则自己说 —— 带伞
	//	不该把人从会议里震出来.
	rules := osinit.NewRules(log, world, nil, func(r osinit.Rule) {
		comp.deliveries.Post(osinit.Deliver{
			From: senseSource, Kind: osinit.DeliverProactive,
			Text: r.Say, Why: r.Why, Urgent: r.Urgent,
			// **谁的规矩推给谁**: 她的"到家提醒"在他手机上响一次,
			// 他就会把整个通道关掉
			To: r.For,
		})
	})
	rules.Restore(flatEvents(prior))
	comp.rules = rules

	// ── 接入器: OS 主动往外拉的那几个 ──
	//
	//	跟采集端方向相反 —— 见 osinit/connector.go. 拉回来的东西照样
	//	变成 Signal 走同一条总线, 于是世界模型不需要知道"这条是推来的
	//	还是拉来的".
	// ── 地图那一侧 ── 见 geo.go.
	//
	//	**没 key 也要建**: "他此刻在哪、旧了就现问手机"不出网, 每台接了
	//	感知层的机器都该有; 出网的那几样(问路、路况、天气、搜地方)各自
	//	看 geo.api.Ready() 决定挂不挂.
	g := newGeo(places, devices, strings.TrimSpace(os.Getenv("NEOX_BAIDU_AK")),
		o.Log(), comp.ledger)
	comp.geo = g
	if g.api.Ready() {
		// 说不出地名时兜底问一句"这儿大概是哪儿" —— 见 sense/nearby.go.
		// 只读缓存不阻塞: describe 跑在总线的同步观察链上
		world.UseNearby(g.rgc.Name)
		comp.mapShot = g.rgc.StaticMap
	}

	conns := osinit.NewConnectors(wired.Bus)
	conns.Add(&sense.Weather{
		Pace: time.Duration(envInt("NEOX_WEATHER_EVERY_MIN", 30)) * time.Minute,
		// **跟 where(weather=) 走同一个源** —— 见 sense.Weather.Baidu
		Baidu: g.api,
		// 查哪儿的天气 —— 先用最后一次知道的位置, 没有就用配的家.
		//
		//	**顺序不能反**: 人在外地的时候, 家里的天气对"要不要带伞"
		//	是错的答案, 而错的答案比没有答案糟.
		Where: func() (float64, float64, bool) {
			if lat, lon, ok := places.Here(); ok {
				return lat, lon, true
			}
			return homeLatLon()
		},
	})
	stopConns := conns.Start()

	stopBus := wired.Bus.Start()
	stopDaily := daily.Start()

	comp.places = places
	comp.watches = watches
	comp.budget = budget

	fmt.Fprintf(os.Stderr, "◉ 感知层已接: POST /signal · /signals · /heartbeat"+
		" —— 普通信号最坏 %s 后送达, 紧急的立刻\n", wired.Bus.EffectiveDelay())

	return &senseStack{
		bus: wired.Bus, people: people, devices: devices, world: world,
		rules: rules, routine: routine,
		places: places, watches: watches, budget: budget,
		stop: func() {
			stopConns()
			stopDaily()
			stopBus()
		},
	}
}

// serveSense 主动进程的一生: 等摘要, 判断值不值得说, 绝大多数时候什么都不做.
//
//	跟 neox-chat 那边的 runProactive 是同一件事, 差别只在这边是**进程内**跑的
//	(console 的 bot 都是 InprocBody), 所以不用 exec 自己、不用 --proactive 参数.
func serveSense(ctx context.Context, pc osinit.ProcessContext, comp *components,
	ctxTokens int64) (any, error) {
	ts := agent.NewToolSet([]agent.Tool{
		// 主动进程手上**只有这一个工具** —— 它不读不写不跑命令, 只会说.
		//
		//	notify_user 的参数里强制要一条判据(不可逆/要你动手/信息差),
		//	填不出来就报错 —— 那道闸在 agent/proactive.go 里.
		agent.NotifyTool(func(text, why string) {
			// 过打扰预算这道硬闸. Admit 里判完会自己 Post 到投递总线,
			// 所以这里不再 Post 一次 —— 两处都发的话用户收两条
			comp.budget.Admit(osinit.Notice{Text: text, Why: why})
		}),
	})
	store := engine.NewPageStore()
	task := "待命。有摘要进来时判断值不值得打扰用户；绝大多数时候什么都不做。"
	win := agent.NewWindow(store, task, 0)
	defer win.Release()
	bud := agent.NewBudgeter(ctxTokens)
	win.UseBudgeter(bud)

	sys := procSyscalls{pc: pc, bot: "sense"}
	ag := &agent.Agent{
		ABI: sys,
		Model: agent.NewProactiveLLM(&agent.LLM{
			ABI: sys, Tools: ts, Window: win, Budgeter: bud,
			// 主动进程没有可写范围 —— 它不写东西
			Writable: "(无)",
			// 他交代过的事也影响"值不值得打扰": "我老婆孕期"跟
			// "我不喝咖啡"在这一层的分量完全不同, 而它看不见的话
			// 只能按平均值判
			Known: comp.knownText()}),
		Tools:  ts,
		Box:    agent.Toolbox{Root: neoxHome(), Sys: sys, Secrets: secretPaths()},
		Window: win,
	}
	// **手上没东西就不推理**: 开局那一轮一份摘要都没有, 而模型在没有
	// 依据时被要求判断"要不要打扰用户", 输出是不可预期的 —— 那正是这套
	// 设计最怕的: 判断力一飘, 用户就把通知关掉, 真正重要的那次也到不了他.
	//
	// 常驻指令照样进上下文(它得知道自己是干什么的), 只是不触发一轮.
	if err := ag.Serve(""); err != nil {
		return nil, err
	}
	return "感知判断结束", nil
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func p64(v int64) *int64 { return &v }

// asFloat64 信号 body 里的数 —— JSON 解出来是 float64, 内存里直接放的
// 可能是 int. **两种都要认**: 只认一种的话, 从账本重放回来的那一批
// 会静默地全部跳过
func asFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
