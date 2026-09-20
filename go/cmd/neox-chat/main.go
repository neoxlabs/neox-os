// neox-chat — shell 进服务器就能跟它对话.
//
// 每说一句话就在 OS 上起一个 Process 去干. 关键性质:
//
//	· agent 是被内核约束的真进程, 不是这个 shell 的一部分
//	· 它越界会停下来问你, 你在同一个终端回答
//	· 你按 Ctrl-C 走人, **正在跑的进程不受影响** (只是没人看着了)
//
// 这是"远程有个 AI"的最小形态: 没有客户端、没有推送, 但链路是完整的.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/confine"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"
	"github.com/neox-os/neox-os/sense"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--agent" {
		runAgentSide()
		return
	}
	// 常驻主动进程 —— 感知层的判断者. 跟对话进程是两个不同的东西,
	// 见 agentside.go 的 runProactive
	if len(os.Args) > 1 && os.Args[1] == "--proactive" {
		cli, err := osinit.FromEnv()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer cli.Close()
		(&osAgent{cli: cli}).runProactive()
		return
	}
	// 校准报告: 对着任何一份账本跑, **不需要把系统起起来**.
	//
	// 于是它可以看昨天的、看别人给的、看一份从生产机器拷回来的 ——
	// 而"跑一天然后调阈值"这件事, 如果办法是翻 jsonl, 实际上不会发生.
	// 合成一天并落成账本, 在长时间运行前检查信号量和落盘体积.
	//
	// 它写的是**真的 jsonl**, 走的是真的 EventStore, 于是可以直接
	// 拿 --sense-report 去读. 单元测试量的是内存里的字节数,
	// 这里量的是落盘之后的文件 —— 两个数不一样(轮转、格式).
	if len(os.Args) > 1 && os.Args[1] == "--sense-simday" {
		out := "/tmp/simday.jsonl"
		if len(os.Args) > 2 {
			out = os.Args[2]
		}
		_ = os.Remove(out)
		store, err := osinit.OpenEventStore(out, 20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "账本建不了: %v\n", err)
			os.Exit(1)
		}
		defer store.Close()
		start := time.Now().Add(-24 * time.Hour)
		cur := start
		log := osinit.NewEventLog(func() int64 { return cur.UnixMilli() })
		// 订阅一份写进真账本 —— EventLog 自己不落盘, 落盘是 OS 那层接的.
		// 这里不起整个 OS(它要 API key 和内核约束), 所以自己接一根线
		stopW := log.SubscribeAll(func(e abi.Event) { _ = store.Append(e) })
		defer stopW()
		n := 0
		bus := osinit.NewSignalBus(log, func(osinit.Digest) { n++ },
			osinit.SignalOptions{
				Window: 5 * time.Minute, Lateness: 2 * time.Minute,
				Now: func() time.Time { return cur },
			})
		st := osinit.SimulateDay(bus, start, func(d time.Duration) { cur = cur.Add(d) })
		fmt.Printf("合成一天: 定点 %d + 家居观察 %d → 信号 %d 条 → 摘要 %d 份\n",
			st.Fixes, st.HAObs, st.Signals, n)
		if fi, err := os.Stat(out); err == nil {
			fmt.Printf("账本 %s: %.1f KB\n", out, float64(fi.Size())/1024)
		}
		return
	}
	// 一天跑完之后判它证明了什么. **对着真账本跑** ——
	// 这个判断原来只活在 Go 测试里, 而真账本在另一台机器上、是一份 jsonl,
	// 于是每次还是我肉眼去翻, 而肉眼翻恰恰是它要治的那件事
	//
	//	neox-chat --judge-day <账本> household|quiet
	// 这两天最快能压成几秒 —— **算出来的, 不是猜的**.
	//
	// 能压多快是剧本和设备一起决定的(每一对动作压缩之后要 ≥ 设备转一圈,
	// 也 ≥ 轮询周期). 剧本一改这个数就变了: 把开门从 2 分钟改成
	// 5 分钟, 最快压缩就跟着变, 而脚本里写死的那几处不会跟着变.
	//
	//	neox-chat --fastest-day [轮询秒数]
	if len(os.Args) > 1 && os.Args[1] == "--fastest-day" {
		poll := 1
		if len(os.Args) > 2 {
			poll, _ = strconv.Atoi(os.Args[2])
		}
		fmt.Println(sense.FastestDaySeconds(sense.Household(), sense.Quiet(), poll))
		return
	}

	// 往账本里落一条"这儿叫什么", 重放地点命名这项环境事实.
	//
	// 只有坐标没有地点名时, 即使该提醒的事件发生、主动进程判断 20 次,
	// 也得不出"家里现在没人", 因为 OS 不知道"这儿是家".
	// 验收要重放一天真实的生活, 就必须同时重放地点命名.
	//
	//	neox-chat --seed-place <账本> <名字> <lat> <lon>
	if len(os.Args) > 5 && os.Args[1] == "--seed-place" {
		store, err := osinit.OpenEventStore(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "账本打不开: %v\n", err)
			os.Exit(1)
		}
		defer store.Close()
		log := osinit.NewEventLog(func() int64 { return time.Now().UnixMilli() })
		log.WriteThrough(store)
		lat, _ := strconv.ParseFloat(os.Args[4], 64)
		lon, _ := strconv.ParseFloat(os.Args[5], 64)
		osinit.SeedPlace(log, os.Args[3], lat, lon)
		fmt.Printf("✓ 记下了: %s (%.5f, %.5f)\n", os.Args[3], lat, lon)
		return
	}

	// 等账本安静下来 —— 验收脚本用它代替猜一个 sleep.
	//
	// **猜早了的后果不是报错, 是证据少了一块而结论看起来没变**:
	// 最后几条没进账本, 而判决照样说"通过". 见 osinit.WaitQuiet
	//
	//	neox-chat --wait-quiet <账本> [安静几秒] [最多等几秒]
	if len(os.Args) > 2 && os.Args[1] == "--wait-quiet" {
		quiet, max := 45, 600
		if len(os.Args) > 3 {
			quiet, _ = strconv.Atoi(os.Args[3])
		}
		if len(os.Args) > 4 {
			max, _ = strconv.Atoi(os.Args[4])
		}
		osinit.WaitQuiet(os.Args[2],
			time.Duration(quiet)*time.Second, time.Duration(max)*time.Second)
		// **判据是"账本里有没有东西", 不是"这几十秒里长没长"**.
		//
		// 8 条信号摊在半小时里(平均四分钟一条)时, 几十秒的
		// 观察窗口完全可能没有新增 —— 拿这个当"采集器没接上"是误报,
		// 而脚本会照着它掐掉一场好好的验收.
		if !osinit.HasContent(os.Args[2]) {
			fmt.Fprintln(os.Stderr,
				"⚠ 这份账本是空的 —— 采集器接上了吗?")
			os.Exit(1)
		}
		return
	}

	// 开跑前点名: 剧本要用的实体, 这台 HA 上有没有.
	//
	// **不点名可能运行两小时后才发现缺前提**: HA 即使有 12 个实体,
	// 也可能没有锁和灯. 此时对照日会以"开口 0 次"通过(跟真正的成功
	// 一模一样), 有事那天会以"开口 0 次, 期望 1 次"失败, 而人会去查
	// 模型的判断力, 根本原因却是那扇门压根不存在.
	//
	//	neox-chat --accept-preflight <ha-url> <ha-token>
	if len(os.Args) > 3 && os.Args[1] == "--accept-preflight" {
		req, _ := http.NewRequest("GET", strings.TrimRight(os.Args[2], "/")+"/api/states", nil)
		req.Header.Set("Authorization", "Bearer "+os.Args[3])
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "连不上 HA: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "HA 返回 %d —— token 不对?\n", resp.StatusCode)
			os.Exit(1)
		}
		var states []struct {
			EntityID string `json:"entity_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&states); err != nil {
			fmt.Fprintf(os.Stderr, "HA 的 /api/states 解不开: %v\n", err)
			os.Exit(1)
		}
		have := map[string]bool{}
		for _, st := range states {
			have[st.EntityID] = true
		}
		miss := sense.MissingEntities(have, sense.HouseholdDay(), sense.QuietDay())
		if len(miss) > 0 {
			fmt.Fprintf(os.Stderr, "这台 HA 缺 %d 个剧本要用的实体:\n", len(miss))
			for _, m := range miss {
				fmt.Fprintf(os.Stderr, "   %s\n", m)
			}
			fmt.Fprintln(os.Stderr,
				"   多半是没开 demo 集成 —— 跑下去的话对照日会以'开口 0 次'通过,\n"+
					"   而那跟真正的成功一模一样")
			os.Exit(1)
		}
		fmt.Printf("✓ 剧本要用的 %d 个实体都在\n",
			len(sense.EntitiesNeeded(sense.HouseholdDay(), sense.QuietDay())))
		return
	}

	// 一次完整的验收: 两天一起判.
	//
	//	neox-chat --accept <有事那天的账本> <对照日的账本> [DAY_SECONDS POLL_SEC]
	//
	// **跑哪两天、怎么判、什么时候拒绝跑都在代码里**. 一次性脚本的
	// 错误样本是九份中两份无效: 一份取不到端口, 一份使用校验会拒绝的参数.
	// 验收可信度不能依赖每次手工拼接正确, 因此统一执行入口和判据.
	if len(os.Args) > 3 && os.Args[1] == "--accept" {
		var r osinit.AcceptResult
		// 尺子先自检 —— 参数给了就查, 没给就跳过(账本已经跑完了)
		if len(os.Args) > 5 {
			daySec, _ := strconv.Atoi(os.Args[4])
			poll, _ := strconv.Atoi(os.Args[5])
			for _, d := range [][]sense.Action{sense.HouseholdDay(), sense.QuietDay()} {
				for _, b := range sense.TooFastForPolling(d, daySec, poll) {
					r.RulerProblems = append(r.RulerProblems, fmt.Sprintf(
						"%s %s → %s 只剩 %.1f 秒(要 %.0f 秒, 卡在%s)",
						b.Entity, b.From, b.To, b.GapSec, b.NeedSec, b.Because))
				}
			}
		}
		if len(r.RulerProblems) == 0 {
			// **前提跟着剧本走** —— 两天的全部差别就是"人在不在家",
			// 而它原来只活在脚本的一条 curl 上. 那条没成功的话:
			// 对照日照样"通过", 有事那天报"开口 0 次, 期望 1 次" ——
			// 同一个缺失前提产生两种判决, 单看通知次数无法指出原因
			for i, spec := range []struct {
				path string
				day  sense.Day
			}{
				{os.Args[2], sense.Household()},
				{os.Args[3], sense.Quiet()},
			} {
				prior, err := osinit.LoadEvents(spec.path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "第 %d 天的账本读不了: %v\n", i+1, err)
					os.Exit(1)
				}
				var evs []abi.Event
				for _, e := range prior {
					evs = append(evs, e...)
				}
				e := sense.ExpectForDay(spec.day)
				r.Days = append(r.Days, osinit.JudgeDay(evs,
					osinit.DayExpect{Name: e.Name, MustHave: e.MustHave, Notices: e.Notices}))
			}
		}
		fmt.Print(r.Text())
		if !r.OK() {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 3 && os.Args[1] == "--judge-day" {
		prior, err := osinit.LoadEvents(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "账本读不了: %v\n", err)
			os.Exit(1)
		}
		var evs []abi.Event
		for _, e := range prior {
			evs = append(evs, e...)
		}
		// **期望值从剧本自己推出来**, 不在这儿再写一遍 ——
		// 两处写的话必然漂移: 剧本里多加一件该说的, 这儿还按老数字判,
		// 而它会"通过"(报告说通过, 而它根本没在验你以为的那件事)
		d := sense.Household()
		if os.Args[3] == "quiet" {
			d = sense.Quiet()
		}
		e := sense.ExpectForDay(d)
		want := osinit.DayExpect{Name: e.Name, MustHave: e.MustHave, Notices: e.Notices}
		v := osinit.JudgeDay(evs, want)
		fmt.Print(v.Text())
		if !v.OK {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--sense-report" {
		ledger := envOr("NEOX_LEDGER", "/var/lib/neox-os/events.jsonl")
		if len(os.Args) > 2 {
			ledger = os.Args[2]
		}
		byPid, err := osinit.LoadEvents(ledger)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读不了账本 %s: %v\n", ledger, err)
			os.Exit(1)
		}
		var all []abi.Event
		for _, evs := range byPid {
			all = append(all, evs...)
		}
		fmt.Print(osinit.AnalyzeLedger(all).Text())
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--selfcheck" {
		runSelfCheck()
		return
	}
	runShell()
}

func runShell() {
	work := envOr("NEOX_WORK_ROOT", "/agentwork")
	_ = os.MkdirAll(work, 0o755)

	key := os.Getenv("NEOX_API_KEY")
	if key == "" {
		fmt.Println("缺 NEOX_API_KEY —— 没有它就没法推理")
		os.Exit(1)
	}
	// 协议缺省自己认: api.deepseek.com 走 Messages 那条 ——
	// 官方联网搜索只挂在那条口上 (engine/nativesearch.go)
	prov := engine.NewProvider(os.Getenv("NEOX_API_PROTOCOL"),
		envOr("NEOX_API_BASE", "https://api.deepseek.com"),
		key, envOr("NEOX_MODEL_ID", "deepseek-v4-flash"), false, nil)

	// 账本放在 agent **写不到**的地方 —— 它不能改自己的历史.
	// 放进工作目录等于给了它一支笔改自己的账.
	ledger := envOr("NEOX_LEDGER", "/var/lib/neox-os/events.jsonl")
	// 轮转在 OpenEventStore 内部做 —— 顺序错了会静默丢数据, 不交给调用方
	store, ledgerErr := osinit.OpenEventStore(ledger, 20)
	if ledgerErr != nil {
		fmt.Printf("账本打不开 (%v) —— 这次对话不会被记住\n", ledgerErr)
	}

	o := osinit.New(osinit.Options{
		EventStore: store,
		Mode:       abi.Mode(envOr("NEOX_OS_MODE", string(abi.ModeConfined))),
		VolumeRoot: work,
		RuntimeFS: confine.ResolveRuntimeFS(func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		}),
		Provider: prov,
		// 搜索: 没配 NEOX_SEARCH_KEY 就是 nil, 于是 agent 那边根本
		// 不会出现 web_search 工具. **不承诺做不到的事**
		Searcher: engine.SearcherFromEnv(),
		// 看图: 主模型自己认图(NEOX_MODEL_VISION=1)就用它,
		// 否则看有没有单独配 NEOX_VISION_*; 都没有就是 nil —— 不认图
		Viewer:  engine.ViewerFromEnv(prov),
		Spawner: spawner,
	})

	// 把上次的对话装回内存日志, /继续 才有东西可接
	prior, _ := osinit.LoadEvents(ledger)
	o.RestoreEvents(prior)

	// 对话列表**每次现算**, 不用启动时的快照.
	//
	// 原来是开机算一次就再也不更新 —— 这一段会话里做过的事
	// `/对话` 一条都看不见, `/继续` 也够不着. 而且失败是半静默的:
	// 切换没成功, 下一句话照样被当前进程收下, 用户以为切过去了.
	//
	// 事件日志本来就在 OS 手里, 问它就行. 快照当真相源是错的.
	listConvs := func() []agent.Conversation {
		return agent.Conversations(o.Log().Snapshot())
	}

	// 线头记账全在 chatThreads 里 —— 原来是三个散落的变量加上七八处
	// 直接赋值, 每加一条命令都得记得"这条要不要清接续、要不要落膛",
	// 而漏掉的症状全是静默的(见 threads.go)
	var threads chatThreads
	var mu sync.Mutex
	pending := map[string]bool{} // did → 待你回答
	// autoLine OS 自己发起的一句话. 目前只有一个来源: 批准授权之后
	// 接着把被挡住的事做完. 缓冲 1 + 非阻塞投递 —— 事件回调里绝不能卡住.
	autoLine := make(chan string, 1)
	// current 当前对话的进程. 一次对话一个进程, 后续的话投给它
	var current abi.ProcessID

	// ── 闹钟 ──
	//
	// **活得比进程长**: 载体是事件日志, 跟决策活得比进程长同一个办法.
	// 到点直接投给用户, 不过主动进程 —— 用户自己要的提醒不需要谁再判断
	// 一次值不值得说, 也不占打扰额度(预算防的是"它没事找事").
	timers := osinit.NewTimers(o.Log(), nil, func(w osinit.Wake, lateMs int64) {
		late := ""
		if lateMs > 90_000 {
			// **晚了要说清晚多久** —— 用户要据此判断还来不来得及.
			// 机器关过、升级过, 补响是对的, 假装准时不是
			late = fmt.Sprintf("(这条晚了 %d 分钟才响, 机器当时没在跑)", lateMs/60000)
		}
		fmt.Printf("\n  ⏰ %s %s\n> ", w.Text, late)
	})
	// 闹钟事件挂在感知层那个伪进程号上, 但这里不假设它在哪个桶里 ——
	// 全扫一遍. Restore 只认 wake.* 三种事件, 别的一律跳过
	stopTimers := timers.Start()
	defer stopTimers()
	if n := len(timers.Pending()); n > 0 {
		fmt.Printf("⏰ 记着 %d 个提醒\n", n)
	}

	// 地点 —— 让"你到了 31.86,117.28"变成"你到公司了".
	//
	// **不在总线里**: senseapi 那条"OS 不解释 body 的形状"不能破.
	// 命名是宿主在投递路径上的**增强**: 有 lat/lon 就用, 没有就算.
	places := osinit.NewPlaces(o.Log())
	if n := len(places.Known()); n > 0 {
		names := make([]string, 0, n)
		for _, pl := range places.Known() {
			names = append(names, pl.Name)
		}
		fmt.Printf("📍 认得 %d 个地方: %s\n", n, strings.Join(names, " "))
	}

	// 用户定的关注 —— "以后我一到家你就跟我说一声".
	//
	// **这不是 IFTTT**: 那条禁令针对的是"采集端报判断"(智能被写进规则).
	// 用户声明关注事项是在表达意图, 不是让采集端代做判断 ——
	// 跟打扰预算里"critical 由策略层判"是同一侧, **用户就是策略层**.
	//
	// 声明位置要在订阅之前: 事件处理那一段要用到它.
	// (这一条我摆错过一次 —— 放进感知层那个块里, 于是订阅回调够不着.)
	// rejected 把 OS 侧的拒绝**送回 agent**.
	//
	// ── 单向请求必须有拒绝回传路径 ──
	//
	// 设闹钟/设关注/撤销走单向 Emit. 如果工具当场返回 success,
	// 而 OS 的拒绝只打在终端上, 三类操作都会面临虚假成功:
	//
	//	闹钟时间算错七个月, OS 拒绝, agent 却回复"提醒设好了"
	//	关注一个不存在的地点, OS 拒绝, agent 却承诺
	//	"以后你一进家门我就说一声"
	//
	// 时间可在本地验证, 但地点**本地验不了**: agent 不知道有哪些地点.
	// 所以本地校验之外还需要让 OS 的拒绝回得去.
	//
	// 而这条路本来就有: autoLine 早就证明了 OS 能往对话里说话.
	// 进程这时候多半还在跑, 于是这句话会作为**中途插话**被它读到,
	// 来得及在它对用户开口之前纠正.

	// cancelFn 撤销的入口. 由感知层那一段装上 —— 事件处理在它之前,
	// 所以这里先留一个口子.
	// (声明位置这类错我上一轮刚栽过一次: watches 摆进了感知层的块里,
	// 而订阅回调够不着它.)
	var cancelFn func(string) (string, error)

	watches := osinit.NewWatches(o.Log())
	// requests 处理 agent 求 OS 办的事. Cancel 那一项由感知层那一段
	// 事后装上 —— 用闭包读 cancelFn, 而不是在这儿取它的当前值(nil)
	requests := &osRequests{
		Places: places, Watches: watches, Timers: timers,
		Cancel: func(id string) (string, error) {
			if cancelFn == nil {
				return "", errNoSenseLayer
			}
			return cancelFn(id)
		},
		Thread: func() string { return threads.Current() },
		Print:  func(line string) { fmt.Print(line) },
		Reply:  func(pid, msg string) { o.Send(abi.ProcessID(pid), msg, "os") },
	}
	// 装上地点表 —— **关注一个不存在的地点是静默失效**:
	// 登记成功、看着好好的、永远不会响. 见 watch.go 的 Add
	watches.UsePlaces(places)
	if n := len(watches.List()); n > 0 {
		fmt.Printf("👁 盯着 %d 件事\n", n)
	}

	// 订阅所有事件 —— UI 只是订阅者之一, 这里的 UI 就是你的终端
	// **回调里一律不直接打印** —— 打印统一走 out, 渲染统一返回字符串.
	// 直接 fmt.Print 会绕过渲染, 单独修补调用点不能保证后续代码遵守约束.
	// 统一输出入口才能让所有事件经过同一套渲染规则. 见 events.go
	out := func(s string) { fmt.Print(s) }
	stop := o.Log().SubscribeAll(func(e abi.Event) {
		m, _ := e.Payload.(map[string]any)
		switch e.Kind {
		case abi.EvDecideRequest:
			pres, _ := m["present"].(abi.PresentSpec)
			did, _ := m["did"].(string)
			mu.Lock()
			pending[did] = true
			mu.Unlock()
			out(renderDecision(did, pres))
		case abi.EvProcOutput:
			// agent 设的闹钟在这一侧登记.
			//
			// **事件处理必须接到登记入口**: 漏掉这一步仍可能通过构建和组件
			// 单测, 却出现"工具说设好了、账本里一条 wake 都没有"的静默失效.
			// 组件本身正确不能证明调用链完整.
			// agent 求 OS 办的事(记地点/盯着/设提醒/撤销)全在 osRequests 里.
			// **失败要送回问的那个进程** —— 原来一律发给 current, 也就是
			// 另一段对话: 主动进程不知道自己失败了、会照着"设好了"往下说,
			// 而正在聊别的事的那个进程莫名收到一条它没做过的操作的失败
			requests.Handle(string(e.PID), m)
			out(renderStep(m))
		case abi.EvProcOutcome:
			if e.PID == current {
				current = "" // 这个对话结束了, 下一句起新的
				if threads.FireResume() {
					select {
					case autoLine <- resumeAfterGrantLine:
					default: // 已经排着一句了, 不重复
					}
				}
			}
			out(renderOutcome(e.Payload))
		}
		os.Stdout.Sync()
	})
	defer stop()

	// ── 感知层 ──
	//
	// 配了 token 才起 —— 采集口收的是位置和来电, 不能默认开着.
	//
	// 摘要的去向: 有正在进行的对话就投进它的收件箱(它会被当成一句
	// 中途插话, agent 自己判断值不值得说); 没有对话就先摆在终端上.
	//
	// **v1 刻意不为摘要新起进程**: 那等于每个窗口买一次推理, 而
	// "它大部分时候应该安静"还没有任何东西来保证. 常驻的主动进程
	// 要等打扰预算做完再说 —— 先把总线验对.
	// senseProc 常驻主动进程. 摘要投给它, **不再插进当前对话**.
	//
	// 原来是投进当前对话的收件箱. 那有两个问题, 而且都是结构性的:
	//	① 拿"你正在聊的事"的上下文去装"外面发生的事", 互相污染
	//	② 你每聊一句都在为感知付钱(感知的历史一直挂在对话的前缀里)
	// 更根本的是: 对话进程有人在等它说话, 沉默是失败;
	// 主动进程没人在等, 沉默才是常态. 两种默认值不能共存于一个进程.
	var senseProc abi.ProcessID
	// recycleSense 换一个新的主动进程. 由感知层那一段装上.
	//
	// 计数器也放在这儿: 用它的地方(投递回调)在感知层那一段**之前**,
	// 摆在那边的话编译不过 —— 这类"声明位置"的错这一轮已经栽第二次了.
	var recycleSense func()
	// respawnSense 投不进去时重新拉一个. 跟 recycleSense 是两件事:
	// 回收是"它还活着, 但上下文该清了", 这个是"它已经没了"
	var respawnSense func() bool
	// 计数器归 DigestRouter 管了 —— 它原来摆在这儿, 正是"声明位置"
	// 那三次错里的第二次
	recycleEvery := envInt("NEOX_SENSE_RECYCLE_EVERY", 30)
	// budget 打扰预算. **提示词里的三条判据是建议, 这里是硬闸** ——
	// 跟停滞检测同一条道理: 提示词管不住的东西得由外面兜住
	var budget *osinit.InterruptBudget
	// bus 信号总线. 跟 budget 一样提到这儿来 —— 用它的地方(/静音 那几条
	// 命令)在感知层那一段**之后**, 但声明在里面的话作用域够不着.
	// **这类"声明位置"的错这个文件里已经栽第三次了**, 说明这个函数太长了
	var bus *osinit.SignalBus
	if tok := os.Getenv("NEOX_SENSE_TOKEN"); tok != "" {
		// 信号的可读视图 —— **"拉"的路径**.
		//
		// 推的路径(摘要 → 主动进程 → 打扰预算)全建完了, 但用户问
		// "我今天去过哪儿", agent 无从答起. 做成卷里的一个文件而不是
		// 一个新工具/新系统调用, 因为**能做成环境事实的, 就不要做成记忆**:
		// 信号已有持久化来源, 暴露可读视图后用现成的 search 就能查.
		view := osinit.NewSignalView(work, places)
		stopView := view.Start(5 * time.Second)
		defer stopView()

		// 当前生效的东西 —— **agent 要看得见自己设过什么, 否则无从撤起**.
		//
		// 跟 signals.txt 分开: 一个是流水(只增不改), 一个是现状(每次重写).
		// 混成一个的话, 撤掉的关注还留在文件里, agent 会照着念一个
		// 已经不存在的东西.
		status := osinit.NewStatusView(work, places, watches, timers)
		stopStatus := status.Start(3 * time.Second)
		defer stopStatus()
		cancelFn = status.Cancel

		budget = osinit.NewInterruptBudget(o.Log(),
			envInt("NEOX_INTERRUPT_PER_DAY", 3), nil)
		// 真送出去的那几条也进投递总线 —— 于是手机端只认一种形状
		budget.UseDeliveries(osinit.NewDeliveries(o.Log()))
		// 摘要投递的接线全在 osinit.DigestRouter 里 —— 它有五条各自
		// 独立的性质(换地名/关注直说/破例资格/回收/兜底), 原来活在这个
		// 闭包中, **一条测试都没有**: 组件全测过, 连它们的那截线没人管
		// **接线只有一份** —— 宿主和端到端测试都从 BuildSenseStack 拿.
		//
		// 在这儿再接一遍的话, 测试测的是**测试里那一份接线**,
		// 而这一份照样可以是错的 —— "组件全测过, 连它们的那截线没人管"
		// 会造成静默失效; 三处调用链漏接的案例说明组件测试不能替代端到端验证.
		wired := osinit.BuildSenseStack(osinit.SenseStack{
			Log: o.Log(), Places: places, Watches: watches, Budget: budget,
			Window:   time.Duration(envInt("NEOX_SENSE_WINDOW_SEC", 300)) * time.Second,
			MaxBatch: envInt("NEOX_SENSE_MAX_BATCH", 50),
			Lateness: time.Duration(envInt("NEOX_SENSE_LATENESS_SEC", 120)) * time.Second,
			// 补传是另一条通道: 会话窗口(安静够久算这一波补完了).
			// 旋钮要露出来 —— 一部离线一天的手机跟一个偶尔抖一下的
			// 家居桥接, 合适的静默期不是一个数
			BackfillQuiet: time.Duration(envInt("NEOX_SENSE_BACKFILL_QUIET_SEC", 30)) * time.Second,
			UrgentGrace:   time.Duration(envInt("NEOX_SENSE_URGENT_GRACE_SEC", 900)) * time.Second,
			// 每条信号写一行进可读视图 —— 好让用户能问"我今天去过哪儿".
			// (地点和在家判断由 BuildSenseStack 自己挂上)
			OnSignal:   view.Append,
			OnWatchHit: func(hit string) { fmt.Printf("\n  👁 %s\n> ", hit) },
			ToProcess: func(text string) bool {
				return senseProc != "" && o.Send(senseProc, text, "sense")
			},
			ToTerminal: func(d osinit.Digest, text string) {
				// 主动进程没起来 —— 摊在终端上, 别静默丢.
				// 带上种类和条数: 走到这条路的时候主动进程正好是坏的,
				// 而那正是最需要线索的时刻
				fmt.Printf("\n  ◉ 感知(%s, %d 条, 无主动进程):\n%s> ",
					d.Reason, d.Count, text)
			},
			// 主动进程死了就再拉一个 —— 它会死(撞止损线、崩溃、被杀),
			// 而死了之后投递永远失败, 摘要从此只会摊在终端上,
			// 回收那条路也走不到(它要"投递成功够 N 次"才触发)
			Respawn: func() bool {
				if respawnSense == nil {
					return false
				}
				return respawnSense()
			},
			RecycleEvery: recycleEvery,
			Recycle: func() {
				if recycleSense != nil {
					recycleSense()
				}
			},
			// 采集端失联要说出来. **不占打扰额度、也不过模型**:
			// 额度防的是"它没事找事", 而这条是**系统坏了**;
			// 而"ha 已经 8 分钟没消息了"这句话也不需要润色.
			OnStale: func(st []osinit.StaleCollector) {
				for _, s := range st {
					// **两件事分开说**: 失联是去看进程还在不在,
					// 瞎了是去看凭据 —— 混成一句话用户会照着错的方向查
					if s.Blind {
						fmt.Printf("\n  ⚠ 采集端 %s 还在, 但它已经 %s 拿不到数据了: %s\n> ",
							s.Source, s.Silent.Round(time.Second), s.Why)
						continue
					}
					fmt.Printf("\n  ⚠ 采集端 %s 已经 %s 没消息了(它平时每 %s 一次)——感知层这一路是瞎的\n> ",
						s.Source, s.Silent.Round(time.Second), s.Pace)
				}
			},
			OnBack: func(srcs []string) {
				for _, s := range srcs {
					fmt.Printf("\n  ✓ 采集端 %s 又回来了\n> ", s)
				}
			},
		})
		bus = wired.Bus
		// 把上次进程死掉时还挂在窗口里的信号捞回来.
		//
		// **它们在账本里, 但永远不会有人去总结它们** —— 没有报错、
		// 账本完整、事后翻也翻得到, 只是从来没被送到任何人面前.
		// 跟"闹钟活得比进程长"是同一类, 而窗口一直没做.
		//
		// 捞回来走补传通道(它们已经发生过了), 不是实时.
		//
		// **恢复全部收在 osinit.RestoreAll 里**: 七处独立的 for 循环
		// 在增加第八个状态时容易漏接. 三类遗漏都有同一种静默症状:
		// "看起来一切正常, 只是某件事悄悄不算数了".
		// 顺序也在那边定死(预算先于闹钟、静音先于捞积压), 见 restore.go
		for _, l := range osinit.RestoreAll(prior, osinit.RestoreTargets{
			Timers: timers, Places: places, Watches: watches,
			Budget: budget, Bus: bus, Presence: wired.Presence,
		}).Lines() {
			fmt.Println(l)
		}
		stopBus := bus.Start()
		defer stopBus()
		api, err := osinit.NewSenseAPI(bus, osinit.SenseOptions{
			Addr: envOr("NEOX_SENSE_ADDR", "127.0.0.1:8787"), Token: tok})
		if err != nil {
			fmt.Printf("⚠ 感知入口起不来: %v\n", err)
		} else {
			api.Start()
			defer api.Close()
			// **把实际延迟打出来, 不要只打窗口长度.**
			// 实际延迟 = 窗口 + 容忍度. 只说窗口的话, 配的人会等错时间 ——
			// 把容忍度内的正常等待误判为故障, 排查方向也就错了.
			fmt.Printf("◉ 感知入口 http://%s  (POST /signal · /signals) · 普通信号最坏 %s 后送达, 紧急的立刻\n",
				api.Addr(), bus.EffectiveDelay())
		}

		// 日报 —— 攒下的东西**唯一不需要用户主动**的出口.
		//
		// 没有它, "延后"跟"丢掉"的区别很小: 用户不知道有东西攒着,
		// 也就永远不会去问.
		//
		// 它不占打扰额度: 一天一次、时间固定, 是**可预期的**,
		// 而可预期的打扰不消耗信任.
		daily := osinit.NewDailyReport(o.Log(), budget,
			envInt("NEOX_DAILY_HOUR", 21), nil, func(text string) {
				// 走跟别的输出同一个口子:
				// 事件回调之外的直接打印同样会绕过渲染, 而日报是
				// 唯一不需要用户主动的出口, 最不该只有终端看得见.
				// 正文现在也进账本(见 daily.record), 于是推送/语音/
				// 手机端都能订到同一份
				fmt.Print(renderDaily(text))
			})
		// 日报的"今天发过没有"也交给 RestoreAll —— 它就是第八处,
		// 正是这道闸要防的那一处. **RestoreAll 只能调一次**(预算的
		// Restore 是累加的, 调两次今天就白白多用几次额度),
		// 所以这里只补 daily 这一项
		osinit.RestoreDaily(prior, daily)
		stopDaily := daily.Start()
		defer stopDaily()

		// 把预算装到渲染那一侧
		noticeGate = func(text, why string) (osinit.NoticeVerdict, int, int) {
			v := budget.Admit(osinit.Notice{Text: text, Why: why})
			used, quota, _ := budget.Stats()
			return v, used, quota
		}

		// ── 主动进程要定期回收 ──
		//
		// 不回收时上下文**线性无界增长**: 40 份摘要之后,
		// prompt 从 1099 涨到 5112, 每份约 +100 token, 完全线性.
		//
		// 它每次只做一个判断("值不值得说"), 却在积累几十份**跟这次
		// 判断无关**的旧摘要. 把整条流塞进上下文会用噪音稀释当前事实,
		// 降低判断质量:
		// 到第 100 份时, 它是在 99 份无关的东西里做一个判断.
		//
		// 它真正需要的历史只有"我今天说过什么"(别重复), 而那个 OS 手里
		// 就有(budget.SaidNote). 把那条作为事实给出去之后,
		// **进程本身就是可丢弃的** —— 这正是这个 OS 的形状:
		// 进程可以死, 状态在 OS 里.
		//
		// 不做成"折叠"是因为折叠折的是工具结果, 而摘要是以用户输入
		// 的形式进去的, 折叠保留它们(那条规矩本身是对的).
		// 起常驻主动进程.
		//
		// **一条能力都不给**(ProactiveCaps 返回 nil): 它只看摘要、只说话.
		// 一个被外部信号唤醒的常驻进程如果还能读写文件, 那么任何能往
		// 采集口投信号的东西都获得了一条间接执行路径 —— 而感知层收的是
		// 位置和来电, 攻击面本来就是全系统最大的.
		self, _ := os.Executable()
		spawnSense := func() (abi.ProcessID, error) {
			return o.Spawn(abi.ProcessSpec{
				App: "sense", Name: "感知判断",
				Caps: agent.ProactiveCaps(),
				// 它常驻, 所以止损线按"一天"配而不是按"一个任务"
				Budget: &abi.Budget{Tokens: p64(int64(envInt("NEOX_SENSE_BUDGET_TOKENS", 2000000)))},
			}, osinit.ExecBody{
				Argv: []string{self, "--proactive"},
				Cwd:  work,
				Env: map[string]string{
					"NEOX_WORK": work,
					"NEOX_TASK": "待命。有摘要进来时判断值不值得打扰用户；" +
						"绝大多数时候什么都不做。",
				},
			})
		}
		if pid, err := spawnSense(); err != nil {
			fmt.Printf("⚠ 主动进程起不来: %v —— 摘要会摊在终端上\n", err)
		} else {
			senseProc = pid
			fmt.Printf("◉ 主动进程 %s 待命中(默认不打扰)\n", pid)
		}
		respawnSense = func() bool {
			pid, err := spawnSense()
			if err != nil {
				// 拉不起来就摊在终端上 —— 但**要说一声**,
				// 不然感知层已经降级了而没有任何一处提过
				fmt.Printf("\n  ⚠ 主动进程没了, 也拉不起来(%v) —— 摘要会摊在这儿\n> ", err)
				return false
			}
			// **"起来了"不等于"活着".**
			//
			// 整条链自跑时撞到的: cgroup 没权限的环境里, 进程创建成功、
			// 几十毫秒后才死. 而 Send 在那几十毫秒里返回成功 ——
			// 于是摘要被投进一个马上就要死的进程的收件箱: **既不到
			// 主动进程, 也不到终端**. 比不重拉更糟(之前至少摊在终端上).
			//
			// 所以等一下再看它还在不在. 这一等值得: 拉起来之后本来就
			// 要走一遍 exec, 而判错的代价是**那一份摘要彻底消失**.
			time.Sleep(500 * time.Millisecond)
			if info, ok := o.Info(pid); ok && info.State.IsTerminal() {
				fmt.Printf("\n  ⚠ 主动进程重拉之后立刻就死了(%s) —— 摘要会摊在这儿\n> ",
					info.State)
				return false
			}
			senseProc = pid
			fmt.Printf("\n  ♻ 主动进程没了, 重新拉起一个(%s)\n> ", pid)
			return true
		}
		recycleSense = func() {
			old := senseProc
			pid, err := spawnSense()
			if err != nil {
				return // 起不来就接着用旧的, 别把唯一那个也弄没了
			}
			senseProc = pid
			if old != "" {
				o.CloseInbox(old)
			}
			fmt.Printf("\n  ♻ 主动进程换了一个新的(%s) —— 上下文清零, "+
				"今天说过什么由 OS 带过去\n> ", pid)
		}
	}

	fmt.Printf("Neox OS · 模型 %s · 工作目录 %s\n", prov.Model(), work)
	// 强制上限要摆在明面上 —— 用户有权知道这台机器到底关得住什么.
	// 藏起来的话, 他会以为约束是完整的.
	if pr := confine.Probe(confine.AllRequirements(), confine.RealDeps()); len(pr.Limits) > 0 {
		for _, l := range pr.Limits {
			fmt.Printf("⚠ %s\n", l)
		}
	}
	fmt.Println("直接说你要它干什么。/对话 看历史，/继续 <编号> 接着聊，/答 <did> <选项> 回决策，/退 走人。")
	// 上次有活没干完就说出来.
	//
	// ── 为什么这一行值得单独写 ──
	//
	// 进程会死 (被杀、重启、断电), 但**这段活是什么**落在事件日志里,
	// 活得比任何一次运行长 —— 那本来就是这个日志存在的理由之一.
	//
	// 缺失任务恢复的案例: 工作中途终止, 重启收到"接着刚才的活干完",
	// 新进程却没有任务原文, 只能在文件系统上做 **16 轮追查**
	// (比时间戳、翻每个目录、grep), 仍只能答"我不知道 shop 要做什么, 你说吧".
	// 而任务原文就在上一个进程的 start 事件里, 一字不差.
	//
	// 它没有假装完成, 那是对的; 但**答案在手边而没人递给它**.
	// 更糟的情形是磁盘上的半成品刚好够反推出一个**像样但更小**的任务 ——
	// 那时候它会照着反推出来的活报"干完了", 用户拿到的是一次假的完成.
	//
	// 这里只做一件事: 把日志已经知道的事说出来, **不替用户决定接不接**.
	// 他可能就是想换个话题. 判断谁来做是他的事, 事实由我们摆出来.
	//
	// 找的是**最近的那段没干完的**, 不是"最近的那段". 中间聊过别的
	// 不会让欠着的活变成干完了 —— 只看第一条的话, 随便说句别的
	// 就把它永久盖住了.
	for i, c := range listConvs() {
		if !c.Unfinished {
			continue
		}
		fmt.Printf("\n⚠ 上次有一段活没干完（%s，干到第 %d 步被打断）：\n   「%s」\n"+
			"   接着干就 /继续 %d；不管它，直接说别的就行。\n",
			since(c.LastAt), c.PendingSteps, trunc(oneLine(c.Pending), 78), i+1)
		break
	}
	fmt.Print("> ")

	// 键盘不是唯一的输入源.
	//
	// 批准一次授权之后, 对话要自己接着走 —— 那句"接着干"是 OS 该说的,
	// 不是用户该再敲一遍的 (见下面 /答 处的说明). 而它只能在**旧进程
	// 收尾之后**才发得出去, 否则同一段对话上会同时挂两个进程.
	// 所以读键盘挪进 goroutine, 主循环同时等两个来源.
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			lines <- strings.TrimSpace(sc.Text())
		}
		close(lines)
	}()

	for {
		var line string
		var ok bool
		select {
		case line, ok = <-lines:
			if !ok {
				return
			}
		case line = <-autoLine:
			fmt.Printf("\n  ↻ %s\n", line)
		}
		if line == "" {
			fmt.Print("> ")
			continue
		}
		// **不认得的命令绝不能发给模型.**
		//
		// 原来 default 分支是"把这行发给模型", 于是 /退出、/攒着 这类
		// 手误会起一个进程去问模型, 而模型会煞有介事地回答 ——
		// 用户看到一段一本正经的胡话, 完全不知道自己只是敲错了一个字,
		// 而且这一下是花钱的
		c := classify(line)
		if c.Kind == lineUnknownCommand {
			fmt.Println("  " + unknownCommandHint(line))
			fmt.Print("> ")
			continue
		}
		switch {
		case c.Name == "退":
			if current != "" {
				o.CloseInbox(current) // 显式关, 别让它永远等一句不会来的话
			}
			fmt.Println("走了。正在跑的进程不受影响。")
			return
		case c.Name == "对话":
			convs := listConvs()
			if len(convs) == 0 {
				fmt.Println("  还没有聊过什么。")
			} else {
				fmt.Println("  能接着聊的对话 (/继续 <编号>):")
				for i, c := range convs {
					if i >= 15 {
						fmt.Printf("     …还有 %d 段\n", len(convs)-i)
						break
					}
					// 没干完的要标出来 —— 开机那一行只提最近的一段,
					// 更早的那些只有在这儿才看得见
					mark := ""
					if c.Unfinished {
						mark = "  ⚠ 没干完"
					}
					fmt.Printf("   %2d  %s  %d 轮 · %s%s\n",
						i+1, padDisplay(c.Title, 36), c.Turns, since(c.LastAt), mark)
				}
			}
			fmt.Print("> ")
			continue

		case c.Name == "继续":
			convs := listConvs()
			if len(convs) == 0 {
				fmt.Println("  没有可以接着聊的对话。")
				fmt.Print("> ")
				continue
			}
			pick := 0 // 不带编号就是最近的那段
			if c.Arg != "" {
				n, err := strconv.Atoi(c.Arg)
				if err != nil || n < 1 || n > len(convs) {
					fmt.Printf("  没有第 %s 段。先用 /对话 看看有哪些。\n", c.Arg)
					fmt.Print("> ")
					continue
				}
				pick = n - 1
			}
			// 切走之前把当前这段收掉 —— 同时只留一个在待命.
			// 进程可以死, 对话不死: 它的历史已经落盘, 随时能再调回来.
			if current != "" {
				o.CloseInbox(current)
				current = ""
			}
			threads.Continue(convs[pick].Thread)
			fmt.Printf("  下一句会接着「%s」。\n", trunc(convs[pick].Title, 40))
			fmt.Print("> ")
			continue

		case c.Name == "静音":
			// 校准闭环的最后一段: 报告认出噪音 → 这里让它闭嘴.
			// **不做成给 agent 的工具**: 静音是用户对"我不想听什么"的
			// 决定, 不是 agent 该替他做的判断
			if bus == nil {
				fmt.Println("  没开感知层。")
				fmt.Print("> ")
				continue
			}
			arg := c.Arg
			if arg == "" {
				ms := bus.Mutes()
				if len(ms) == 0 {
					fmt.Println("  现在什么都没静音。用法: /静音 <来源>/<种类>")
				} else {
					fmt.Println("  静着这些:")
					for _, m := range ms {
						fmt.Printf("   · %s\n", m)
					}
					fmt.Println("  解除: /取消静音 <来源>/<种类>")
				}
				fmt.Print("> ")
				continue
			}
			src, kind, ok := strings.Cut(arg, "/")
			if !ok || src == "" || kind == "" {
				fmt.Println("  要写成 <来源>/<种类>，比如 phone.mk/phone.moved")
				fmt.Print("> ")
				continue
			}
			bus.Mute(src, kind, "手动静音")
			fmt.Printf("  静了 %s/%s。它照样进账本(你还能问'我今天去过哪儿')，只是不再为它开口。\n", src, kind)
			fmt.Print("> ")
			continue
		case c.Name == "取消静音":
			if bus == nil {
				fmt.Println("  没开感知层。")
				fmt.Print("> ")
				continue
			}
			arg := c.Arg
			src, kind, ok := strings.Cut(arg, "/")
			if !ok || !bus.Unmute(src, kind) {
				// **撤一个不存在的要报错** —— 静默成功会让用户以为撤过了
				fmt.Printf("  没有 %q 这条静音。/静音 看现在静着什么。\n", arg)
			} else {
				fmt.Printf("  %s/%s 又能说话了。\n", src, kind)
			}
			fmt.Print("> ")
			continue
		case c.Name == "攒":
			// **攒着的必须有一条能看见的路**, 否则"延后"就等于"丢掉",
			// 只是听起来好听一点
			if budget == nil {
				fmt.Println("  没开感知层。")
				fmt.Print("> ")
				continue
			}
			used, quota, _ := budget.Stats()
			held := budget.Deferred()
			fmt.Printf("  今天打扰了 %d/%d 次。", used, quota)
			if len(held) == 0 {
				fmt.Println("没有攒着的。")
			} else {
				fmt.Printf("攒着 %d 条:\n", len(held))
				for _, n := range held {
					fmt.Printf("   · %s\n", n.Text)
				}
			}
			fmt.Print("> ")
			continue

		case c.Name == "新":
			if current != "" {
				o.CloseInbox(current)
				current = ""
			}
			// **开新话题是一次明确的放弃**: 待接续的那一段作废,
			// 批准的那一膛也落掉 —— 少做任何一样, 上一段对话都会
			// 从某个缝里渗回来, 而用户刚说过不要它
			threads.NewTopic()
			fmt.Println("  开了新话题，之前的上下文不再带着。")
		case c.Name == "答":
			f := append([]string{"/答"}, strings.Fields(c.Arg)...)
			if len(f) != 3 {
				fmt.Println("用法: /答 <did> <选项>")
			} else if o.Decisions().Resolve(f[1], f[2], "shell:"+os.Getenv("USER"), nil) {
				mu.Lock()
				delete(pending, f[1])
				mu.Unlock()
				fmt.Printf("  ✓ 已回答 %s → %s\n", f[1], f[2])
				// 批准之后要**用扩权后的能力重开进程**, 对话接着走.
				//
				// 内核的规则在进程启动时定死、只能收紧不能放宽, 所以批准
				// 在当前进程里永远生效不了: 即使回答 yes,
				// 那次调用照样被拒, 再收到"再试一次"仍是同一个进程,
				// 于是又问一遍同样的问题. **审批变成了死循环.**
				//
				// 授权已经记在这段对话上, 所以只要让下一句话起个新进程
				// 就行: 历史照常接续(thread), 用户完全无感.
				// **点"允许"本身就是"去吧".**
				//
				// 只记下线头、等用户**再说一句话**才重开进程会多一次无效交互.
				// 例如批准 pypi 三个主机后, agent 只能收尾说
				// "授权已生效, 但这一轮还装不成……下一句话随便说句'继续'
				// 我就装" —— 用户点了允许, 结果什么都没发生, 还得再敲一次.
				// 那一敲是在让人替 OS 干活.
				//
				// 内核那条约束是对的、也必须是对的: 能力只能在 exec 时定死,
				// 只能收紧不能放宽, 所以扩权在当前进程里永远生效不了.
				// 要修的不是它, 是它后面那一步 —— **由 OS 自己把话接上**.
				if f[2] == "yes" && current != "" {
					o.CloseInbox(current)
					threads.Approve() // 等它收尾, 别在它还跑着的时候起第二个
					fmt.Println("  权限已扩大。等这一轮收尾, 我会自己用新权限接着干。")
				}
			} else {
				fmt.Println("  这条决策不存在或已经处理过了")
			}
		default:
			// **一次对话一个进程**: 第一句起进程, 之后的话投进它的收件箱.
			//
			// 这样多轮记忆和跨轮前缀缓存都是自然结果 ——
			// 每句话起新进程的话, 两样都得重来.
			if current != "" {
				if o.Send(current, line, "shell:"+os.Getenv("USER")) {
					// 等待人工决策时, 普通消息不能解除阻塞: 消息虽进入收件箱,
					// 但进程卡在决策上, 取不走.
					//
					// 只说"已转给"会误导: 连问"改好了吗""喂?"并两次收到
					// "已转给", 并不表示消息已处理; 进程可能仍在等决策 d1.
					if ds := pendingFor(o, current); len(ds) > 0 {
						fmt.Printf("  → 收下了, 但它在等你拍板 %s —— 回答之前它动不了\n",
							strings.Join(ds, " "))
					} else {
						fmt.Printf("  → 已转给 %s\n", current)
					}
					fmt.Print("> ")
					continue
				}
				fmt.Printf("  (进程 %s 已结束, 开一个新的)\n", current)
				current = ""
			}
			self, _ := os.Executable()
			pid, err := o.Spawn(abi.ProcessSpec{
				App: "chat", Name: trunc(line, 30),
				// ── 卷内自由, 卷外一个字节都碰不到 ──
				//
				// 原来只给 /site 可写. 那是"少给工具"的思路的残留:
				// 怕它乱改, 所以圈一个小格子. 但边界既然已经由内核守着,
				// 再在卷**里面**多切一道就只是给自己找麻烦 ——
				// agent 想跑个测试、建个临时文件都要打扰用户一次审批,
				// 而那次审批批的东西其实早就在笼子里了.
				//
				// 真正的边界是**卷**: 出了卷内核直接拒, 审批也改不了当前进程.
				// 卷内它是自由的, 该有的样子.
				Caps: []abi.Capability{
					{Axis: abi.AxisWrite, Scope: "/"},
					{Axis: abi.AxisRead, Scope: "/"},
					// 起进程 —— 有了它 agent 才能跑命令: 编译、测试、git、
					// 脚本. 这台机器上装了什么它就能用什么.
					//
					// 敢给是因为 landlock/netns/cgroup 都是 fork+exec 继承的:
					// 子进程跟父进程关在同一个笼子里, 这是内核的语义,
					// **不是我们的约定**, agent 没有办法通过起进程来逃出去.
					{Axis: abi.AxisProc, Scope: "*"},
				},
				Labels: resumeLabel(threads.pending),
				// 止损线要配得住真活.
				//
				// 200k 上限在缓存命中按全价计费时会把"做个 React 工程"卡在中途.
				// 命中率 97~100% 时, 全价计费相当于按十倍速度消耗预算.
				// 因此记账区分缓存价格, 缺省上限也要容纳完整工程任务:
				// 止损线是防跑飞的, 不是拿来卡正常工作的.
				Budget: &abi.Budget{Tokens: p64(int64(envInt("NEOX_BUDGET_TOKENS", 2000000)))},
			}, osinit.ExecBody{
				Argv: []string{self, "--agent"},
				Cwd:  work,
				Env: spawnEnv(work, line, listConvs(), joiningThread(threads.pending),
					osinit.CorrectionsDigest(o.Log().Snapshot(), string(threads.pending))),
			})
			if err != nil {
				fmt.Printf("  起不来: %v\n", err)
			} else {
				current = pid
				if joined := threads.SpawnInto(string(pid)); joined != "" {
					fmt.Printf("  → 起了进程 %s, 接着上次的对话\n", pid)
				} else {
					fmt.Printf("  → 起了进程 %s (后续的话会接着这个上下文)\n", pid)
				}
			}
		}
		fmt.Print("> ")
	}
}

// noticeGate 打扰预算的裁决入口. 由 runShell 装上 ——
// printStep 是个纯渲染函数, 不该自己去找预算对象
var noticeGate func(text, why string) (osinit.NoticeVerdict, int, int)

// renderNotify 主动那条要不要打到终端上, 打成什么样.
//
// **原来它直接 Printf**, 于是既测不到、渲染那道闸也看不见它 ——
// 而这是唯一一条"OS 决定要不要让用户看见"的路径, 最该被看住的就是它.
func renderNotify(m map[string]any) string {
	text, why := str(m["text"]), str(m["why"])
	if noticeGate == nil {
		return fmt.Sprintf("\n  ◆ 主动: %s\n> ", text)
	}
	switch v, used, quota := noticeGate(text, why); v {
	case osinit.NoticeDelivered:
		return fmt.Sprintf("\n  ◆ 主动: %s\n     (今天第 %d/%d 次)\n> ", text, used, quota)
	case osinit.NoticeBreakthrough:
		// 破例要**标出来**: 用户有权知道这条是绕过额度进来的,
		// 否则他会以为额度形同虚设
		return fmt.Sprintf("\n  ◆ 主动(紧急破例): %s\n> ", text)
	default:
		// 攒下的不打扰用户, 但要留痕 —— 账本里查得到.
		// **这一行不打到终端: 打了就等于还是打扰了他一次**
		return ""
	}
}

// asFloat 过了一趟 JSON 的数字. 第三处了 —— 见 timers.go 的 asMillis
func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

// renderStep 一步渲染成什么样. 空字符串 = 什么都不显示,
// 而**那正是这个文件反复吃亏的那一类**, 所以有个兜底分支
func renderStep(m map[string]any) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	pl := func(s string) { b.WriteString(s + "\n") }
	switch m["phase"] {
	// 一轮没做完必须**说出来**.
	//
	// 进程耗尽预算停下时, 若终端不显示原因, 看起来就像卡住,
	// 只能翻账本才能知道是止损线到了.
	// 「停了但不说」比报错难查得多: 报错至少给了方向.
	case "turn_failed", "fatal":
		p("  ✗ 这一轮没做完: %s\n", str(m["err"]))
		if strings.Contains(str(m["err"]), "budget") ||
			strings.Contains(str(m["err"]), "止损线") {
			pl("     预算用完了。/新 开一段新对话，或者起进程前把 " +
				"NEOX_BUDGET_TOKENS 调大。")
		}
	case "step_limit":
		// 撞上限**要说出来**. 不说的话下面那段总结看起来跟正常完成一样,
		// 而用户需要知道"它是被止损线停下的, 不是自己认为干完了" ——
		// 这决定他要不要让它接着干.
		// 这条路径**不止步数上限一种**: 止损线(钱花完了)走的也是这儿.
		// 一律渲染成"上限 %v 步"会在 payload 没有 steps 时显示
		// "到止损线了 (上限 <nil> 步)", 既缺数值又混淆边界种类.
		// 步数默认无限, 把"钱花完了"报成"步数用完了"会让用户做错决定.
		p("  ⏹ %s\n", str(m["msg"]))
	case "prefix_broken":
		// **这条必须显眼**: 前缀断了不会报错、结果照样对, 只是账单翻几倍.
		// 不喊出来就永远没人知道.
		p("  ⚑ 前缀缓存断了: %s\n", str(m["why"]))
	// **主动进程说的话要跟对话回复长得不一样.**
	//
	// 用户没在等它 —— 一条突然冒出来的话如果跟他刚才提问的回答长得一样,
	// 他会以为是自己刚才问的东西. 标记必须让"这是它主动找我"一眼可辨.
	case "notify":
		// 预算在**投递这一处**裁决, 不在 agent 那一侧.
		//
		// 放这儿的理由跟能力预检一样: agent 提出请求, OS 决定放不放行.
		// 放在 agent 侧的话它就成了自己给自己批额度.
		b.WriteString(renderNotify(m))
	case "reply":
		// 直接回话 —— 最常见的一条路径, 显示成对话的样子
		p("  %s\n", str(m["text"]))
	case "step":
		path := ""
		if a, ok := m["args"].(map[string]any); ok {
			path, _ = a["path"].(string)
		}
		p("  · %v %v  %s\n", m["tool"], path, trunc(str(m["thought"]), 50))
	// 预算用完之后再说话 —— 零推理的一句实话, 外加这个宿主自己的出路
	// 用户在干活中途插的话 —— **必须回显**.
	//
	// 原来这里没有分支, 于是他敲完看到的是彻底的沉默: 不知道收没收到、
	// 更不知道它认不认. 跟当年"只说'已转给'是在骗人"是同一类问题,
	// 只是更糟 —— 那时候至少还说了一句.
	case "interrupt":
		p("  ↯ 收到你中途说的: %s\n", trunc(str(m["text"]), 60))
	case "budget_done":
		p("  ⛔ %s\n     (这个终端里: /新 另起一段, 或者重启时把 NEOX_BUDGET_TOKENS 调高)\n",
			str(m["msg"]))
	case "budget_starved":
		p("    ⚠ %s\n", str(m["msg"]))
	// 上下文回收要看得见 —— 不然"这一轮怎么变笨了"没有任何一处能回答
	case "context_fold":
		// 同样按整数打 —— 见 intish
		p("    ⊟ 折掉 %s 条旧结果(省 %s 字节, 预算 %s)\n",
			intish(m["dropped"]), intish(m["droppedBytes"]), intish(m["budgetBytes"]))
	case "budget":
		b2, ok := m["budget"].(map[string]any)
		if !ok {
			// 没带明细也要说一声 —— 静默是这个文件反复吃亏的那一类
			pl("    [预算]")
		}
		if b := b2; ok {
			// **整数要按整数打.**
			//
			// 过 JSON 之后数字回来全是 float64, 而 %v 对 float64 用的是 %g ——
			// 值一大就变成科学计数法: 128000 的窗口值尚可直读, 1048576 则会
			// 显示成 `窗口=1.048576e+06 tok … → 2.014118e+06 字节`.
			// 字节数和 token 数是给人比较的整数, 不该增加心算负担.
			p("    [预算 窗口=%s tok · 实测 %v 字节/tok · %v 样本 → %s 字节]\n",
				intish(b["contextTokens"]), b["bytesPerToken"], b["samples"],
				intish(b["budgetBytes"]))
		}
	case "usage":
		// 缓存命中率直接显示 —— "省了多少"不能只是说法
		p("    [用量 prompt=%s 缓存=%s(%v) 输出=%s]\n",
			intish(m["prompt"]), intish(m["cached"]), m["hitRate"], intish(m["completion"]))
	case "tool_ok":
		p("    ✓ %s\n", trunc(str(m["result"]), 70))
	case "tool_err":
		p("    ✗ %s\n", trunc(str(m["err"]), 70))
	case "model_err":
		p("    模型错: %s\n", trunc(str(m["err"]), 80))
	case "batch":
		// 并行批次要看得见 —— 否则不知道省了几轮往返
		mark := "串行"
		if p, _ := m["parallel"].(bool); p {
			mark = "并发"
		}
		p("  ⇉ %v 个工具%s: %v  %s\n", m["n"], mark, m["tools"], trunc(str(m["thought"]), 40))
	case "stall":
		// 停滞干预要让用户看得见 —— 否则他不知道系统在替他踩刹车
		p("    ⏸ [%v] %s\n", m["level"], trunc(str(m["msg"]), 90))
	case "denied":
		p("    ⛔ 被拒: %v\n", m["path"])
	// 这四个由 osRequests 处理, 而且它自己打了更好的一行
	// (提醒和地点的"记下了"、关注的"盯上了"、撤销结果), 这儿再打就是重复.
	// **登记在 render_test.go 的 silentPhases 里**: 沉默可以,
	// 但必须是写下来的决定
	case "remind", "name_place", "watch", "cancel":
	// ── 下面这几个原来一个字都不打 ──
	//
	// 它们老老实实发了事件, 而渲染这一步把它们吃掉了.
	// 这道闸(TestEveryEmittedPhaseIsRendered)一写出来就抓到六个.
	case "netns_degraded":
		// **最刺眼的一个**: os.go 那儿写着"静默降级是不行的 ——
		// 之后'服务起来了连不上'会查不到根", 事件也发了,
		// 然后终端一个字不打 —— 意图在最后一米被抵消.
		// 这是**约束没生效**, 必须显眼
		p("  ⚑ 网络隔离降级了: %s\n", str(m["msg"]))
	case "empty_done":
		// 这一步它什么都没产出. 不说的话用户看到的是彻底的沉默,
		// 而沉默会被读成"卡住了"
		p("    ⌀ 第 %s 步没产出, 接着走\n", intish(m["n"]))
	case "approved":
		// 批准生效了要说一声 —— 用户点了允许, 总得看见点什么
		p("    ✓ 批了: %v (%v)\n", m["scope"], m["by"])
	case "verify":
		// 它自己验了一下 —— 这是"说做完了"和"真做完了"之间的那一步,
		// 藏起来的话用户没办法判断该不该信
		// **收成一行**: 自查的原文是多行的(前面还带两个换行),
		// 直接塞进 %s 会把这一条摊成一屏, 而它只是一步里的一个旁注
		p("    ⌕ 自查 %v: %s\n", m["path"], trunc(oneLine(str(m["note"])), 70))
	case "reclaim":
		// 页表回收在 19 份长跑账本的样本中均未触发, 属于低频事件;
		// 一旦触发更需要显示, 才能判断内存压力和回收是否正常.
		p("    ♻ 页表回收: 降级 %s 页, 删 %s 页(还剩 %s 字节)\n",
			intish(m["demoted"]), intish(m["deleted"]), intish(m["liveBytes"]))
	case "start":
		// **故意不显示**: 宿主自己已经打过"起了进程 pXX (后续的话会
		// 接着这个上下文)"了, 再来一行是重复. 一份账本里 start 有 174 次,
		// 每次多一行就是 174 行噪音.
		//
		// 这条"故意"登记在 render_test.go 的 silentPhases 里 ——
		// 沉默可以, 但**必须是写下来的决定**, 不能是忘了写
	case "done":
		if m["hitLimit"] == true {
			pl("  （以下是撞到步数上限时的交代，不是它认为已经干完）")
		}
		p("  ✔ %s\n", trunc(str(m["summary"]), 100))
	default:
		// **不认得的也要打一行.** 沉默比丑陋糟得多: 一行看不懂的字
		// 至少说明"有事发生了", 而什么都没有会被读成"它卡住了"
		p("  · %v\n", m["phase"])
	}
	return b.String()
}

func runAgentSide() {
	cli, err := osinit.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer cli.Close()
	// 这里永远用真模型 —— chat 就是拿来跟真模型说话的
	a := &osAgent{cli: cli}
	a.run(os.Getenv("NEOX_TASK"))
}

func spawner(argv []string, cwd string, env map[string]string) (osinit.Child, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	var envv []string
	for k, v := range env {
		envv = append(envv, k+"="+v)
	}
	cmd.Env = envv
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &child{cmd: cmd, out: out}, nil
}

type child struct {
	cmd *exec.Cmd
	out interface{ Read([]byte) (int, error) }
}

func (c *child) OnLine(fn func(string)) {
	go func() {
		s := bufio.NewScanner(c.out)
		for s.Scan() {
			fn(s.Text())
		}
	}()
}
func (c *child) Wait() (int, string, error) {
	err := c.cmd.Wait()
	if err == nil {
		return 0, "", nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), "", nil
	}
	return -1, "", err
}
func (c *child) Kill(string) {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func p64(v int64) *int64 { return &v }
func str(v any) string   { s, _ := v.(string); return s }
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var _ = time.Now

// since 人话时间. 清单里给"多久以前"比给时间戳有用得多 ——
// 用户是靠"刚才那段"和"昨天那段"来认对话的.
// resumeAfterGrantLine 批准之后 OS 发给新进程的接续指令.
//
// **要说清两件事**: 权限真的到手了(不然它会再申请一次同样的东西),
// 以及接着干哪件事(它自己的上下文里有, 但得点一下题).
const resumeAfterGrantLine = "授权批下来了，新进程已经带上扩大后的权限。" +
	"接着把刚才被挡住的那件事做完，做完告诉我结果。"

// 两种线头, **故意做成两个类型**.
//
// 它们都是一个字符串, 长得一模一样, 意思却是相反的两件事:
//
//	joiningThread   下一个进程**将要加入**哪一段(接续时才有)
//	currentThread   **当前**这段是哪一段
//
// 若两者都用 string, 把后者传给需要前者的地方也能编译通过,
// 对话清单却会少一段. 只测被调函数的单测覆盖不到调用点,
// 即使抽出 spawnEnv, 同类错误也可能不触发测试失败.
//
// 所以用两个独立类型让传错**编译不过**, 在编译期检查语义不同的参数.
type joiningThread string

type currentThread string

// spawnEnv 交给被约束进程的全部环境.
//
// ── 为什么单拎出来 ──
//
// 内联在 runShell 中会让**"传哪个值进去"难以单独测试**.
// 函数本身正确而调用点错误有三种典型表现:
//
//	commandPath   算出来了, 调用点还写着旧的固定 PATH
//	插话框架话     写好了, 塞进了 LLM 根本不读的 history
//	对话清单       算出来了, 排除项传的是**上一段**的线头(threadOfCurrent)
//
// 三种情况的共同点: 被测函数正确, 错的是**调用方传入的内容**.
// 抽出环境构造函数, 才能直接检查这些输入与输出的对应关系.
//
// ── 这里的规矩 ──
//
// **没有 NEOX_API_KEY**: 凭据不进被约束的进程.
//
// 想给进程什么就得在这里**逐个写明**. OS 一律不继承宿主环境
// (长驻进程继承了 env 就摘不掉, 临时约束会变成永久约束), 所以在外面
// export 一个变量对它不可见, 例如只在宿主设置 NEOX_CTX_BYTES 不会生效.
//
// joining 是**这个新进程将要属于的那一段**: 接续时是那段的线头, 全新对话是空.
// 不能拿"上一段"的线头来当它, 否则会错误排除对话清单中的上一段.
func spawnEnv(work, task string, convs []agent.Conversation, joining joiningThread, corrections string) map[string]string {
	return map[string]string{
		"NEOX_WORK": work, "NEOX_TASK": task,
		// 这台机器上还有哪些对话 —— 跟"可写范围"一样是环境事实.
		// 不给它, 用户问"昨天那个做完了吗"它会说"我没有跨会话的记忆",
		// 而 OS 明明记着.
		"NEOX_RECENT": recentDigest(convs, string(joining)),
		// 他纠正过的方向. 流水里没有"对还是错", 必须单独喂.
		// 空串时提示词那一层整段不加.
		"NEOX_CORRECTIONS": corrections,
		// 步数**默认无限**. 写死一个上限会让"做不完"被误读成 agent 能力不行,
		// 其实是被闸砍的 —— 而且步数根本不是有意义的度量:
		// 花费由止损线管, 跑飞由停滞检测管.
		"NEOX_MAX_STEPS": os.Getenv("NEOX_MAX_STEPS"),
		"NEOX_CTX_BYTES": os.Getenv("NEOX_CTX_BYTES"),
		// 压小它能确定性复现"输出被截断"那条路
		"NEOX_MAX_OUTPUT_TOKENS": os.Getenv("NEOX_MAX_OUTPUT_TOKENS"),
	}
}

// recentDigest 给 agent 看的对话清单.
//
// **确定性**: 按最近活动倒序(Conversations 已经排好), 取固定条数, 一行一段.
// 抖动会让系统段每次都不一样, 前缀缓存整段作废 —— 而这一段是进程启动时
// 定死的, 同一段对话里逐字节不变.
//
// 排除当前这一段: 它的历史 agent 本来就全看得见, 再列一遍是噪音.
func recentDigest(convs []agent.Conversation, current string) string {
	const maxLines = 8
	var b strings.Builder
	n := 0
	for _, c := range convs {
		if c.Thread == current || n >= maxLines {
			continue
		}
		mark := ""
		if c.Unfinished {
			// 「没干完」是这份清单里最值钱的一条 —— 它是真的还欠着活
			mark = "  ⚠ 没干完(停在第 " + strconv.Itoa(c.PendingSteps) + " 步)"
		}
		b.WriteString("- 「" + oneLine(trunc(c.Title, 40)) + "」 " +
			strconv.Itoa(c.Turns) + " 轮 · " + since(c.LastAt) + mark + "\n")
		n++
	}
	return strings.TrimRight(b.String(), "\n")
}

// intish 把过了一趟 JSON 的数字按整数打.
//
// JSON 里的数字回来都是 float64, `%v` 用的是 %g, 值一大就是科学计数法.
// 这些是给人看的字节数和 token 数, 没有小数一说.
func intish(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case int:
		return strconv.Itoa(n)
	}
	return fmt.Sprint(v)
}

// oneLine 把多行的交代压成一行. 用户的原话可能有换行,
// 直接塞进提示里会把那一行撑成一屏, 反而看不出重点.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func since(at int64) string {
	d := time.Since(time.UnixMilli(at))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
}

// resumeLabel 空标签 = 全新对话, 那是常态.
//
// thread 是**对话身份**, 跟进程身份分开: 每次接续都是一个新进程,
// 但它们属于同一段对话. 不分开的话, 接续之后清单里会冒出一段
// "新对话"(其实是续集), 接几次就全是碎片.
func resumeLabel(thread string) map[string]string {
	if thread == "" {
		return nil
	}
	return map[string]string{"thread": thread}
}

// padDisplay 按**显示宽度**补齐, 不是按字节.
//
// %-38s 数的是字节: 一个汉字 3 字节但只占 2 列, 于是中文标题
// 每多一个字就往左缩一列, 清单看起来是斜的.
// 中文界面上这个错永远会出现, 所以按宽度补.
func padDisplay(s string, width int) string {
	w := 0
	var b strings.Builder
	for _, r := range s {
		rw := 1
		if r > 0x2000 { // CJK / 全角标点, 占两列
			rw = 2
		}
		if w+rw > width {
			b.WriteString("…")
			w++
			break
		}
		b.WriteRune(r)
		w += rw
	}
	for ; w < width; w++ {
		b.WriteByte(' ')
	}
	return b.String()
}

// runSelfCheck 真跑一遍约束, 报告它现在到底拦不拦得住.
//
// 跟 --probe 那种"问支持不支持"是两回事: 机制在、二进制在、探测全绿,
// 而规则写错一个字就什么都拦不住, 探测完全看不出来.
func runSelfCheck() {
	pr := confine.Probe(confine.AllRequirements(), confine.RealDeps())
	fmt.Printf("机制: %v\n", pr.Mechanisms)
	for _, l := range pr.Limits {
		fmt.Printf("⚠ %s\n", l)
	}

	bad := 0
	for _, r := range confine.SelfCheck(confine.DefaultPaths) {
		switch {
		case r.Skipped:
			fmt.Printf("  –  %s: %s\n", r.Name, r.Detail)
		case r.OK:
			fmt.Printf("  ✓  %s: %s\n", r.Name, r.Detail)
		default:
			bad++
			fmt.Printf("  ✗  %s: %s\n", r.Name, r.Detail)
		}
	}
	if bad > 0 {
		// 自检不过就该退非零 —— 这样它能进 CI, 退化了会红
		fmt.Printf("\n%d 项没通过。约束没在按预期工作。\n", bad)
		os.Exit(1)
	}
	fmt.Println("\n约束实测通过。")
}

// pendingFor 这个进程还欠哪些决策没答.
//
// 问 OS 而不是自己记一份: chat 侧的 pending 表只记"我看见过哪些请求",
// 而决策可能被超时兜底解决掉 —— 两份账早晚对不上.
func pendingFor(o *osinit.OS, pid abi.ProcessID) []string {
	var out []string
	for _, p := range o.Decisions().Pending() {
		if p.PID == pid {
			out = append(out, "/答 "+p.DID)
		}
	}
	return out
}

// envInt 步数上限之类的整数配置. 值不合法就用缺省 ——
// 一个拼错的环境变量不该让进程起不来, 但也不该悄悄变成 0
// (0 步等于什么都不干, 那比用缺省糟得多).
func envInt(k string, d int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return d
	}
	return n
}
