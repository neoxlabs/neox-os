package main

// bot 的"随身组件" —— 这台 OS 能给它的服务.
//
// ── 为什么是一张显式的表, 不是"agent 自己想办法" ──
//
// 闹钟是最清楚的例子: 一个 agent 进程完全可以起个协程睡到 11 点,
// 但那个闹钟跟进程同生共死 —— 进程被杀、机器重启、对话结束, 它就没了,
// 而**用户不会知道它没了**. 静默地不响是闹钟最糟的失败方式.
// 所以闹钟必须是内核对象(osinit.Timers: 落事件日志、开机装回、错过补响),
// bot 只是**用**它.
//
// ── 每一条都是"配了才挂" ──
//
// 没接的组件一律传 nil, 于是工具表里根本没有它, 提示词里也不会提 ——
// 摆一个用不了的工具比没有更糟: 模型会照着调、许下做不到的承诺,
// 而它没有任何线索可以自纠(提示词说有它就信). 见 agent.DefaultToolsWith.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/osinit"
)

// components 一台 console 上所有 bot 共用的服务.
//
//	共用是对的: 闹钟登记处一台机器一个, 账本一台机器一份.
//	每个 bot 各起一套的话, "撤掉那个提醒"就得先猜是谁设的.
type components struct {
	os     *osinit.OS
	timers *osinit.Timers
	// doing 每个 bot 这一轮在干什么 —— 宿主替它记账时拿来当提交标题.
	// 见 nowDoing / agent.Agent.BeforeTurn
	doing map[string]string
	// willDo 宿主自己发起的那一轮**要干什么** —— 见 willBe
	willDo  map[string]string
	doingMu sync.Mutex
	// caps 每轮花费上限 —— 见 cap.go. nil = 不限
	caps *turnCap
	// resume 掉线之后自己接着干吗 —— 现问现用, 用户可能刚改过
	resume func() bool
	// spawn 起一个新 bot. nil = 这台机器不许 bot 自己拉人.
	//
	//	放在 components 上而不是让工具直接调 osinit: 起进程要带的东西
	//	(标签、能力、工作区)全在宿主手里, 工具只该说"我要一个人", 不该
	//	自己去拼一个进程规格 —— 拼错了就是一个能力不对的 bot 在你机器上跑.
	spawn func(persona) (abi.ProcessID, error)
	// roster 名册. 拉进来的人**要活过重启** —— 见 hireFor
	roster *botRoster
	// moveInto 把某个 bot 挪进某个房间. 拉人时用来自动成组 —— 见 hireFor.
	//
	//	**不能当场挪招人的那个**: 它正卡在 recruit 这个工具调用里等返回,
	//	换房间是换一个进程(标签在启动时定死) —— 当场杀了它, 这一轮的活
	//	就断在半路. 所以排到它这一轮说完话之后.
	moveInto func(name, thread string) error
	// pending 谁欠一次换房间. 键是 bot 名字.
	pending sync.Map
	// rebind 把某个 bot 派到另一个工作区 —— move_work 工具的落点.
	// 跟 moveInto 同一个道理: 换地方是换进程, 说完这一轮才动手.
	rebind func(name, rawWork string) error
	// ownRoom 记下谁开的这个房间 —— 群主在开房那一刻定
	ownRoom func(thread, owner string) error
	// pendingWork 谁欠一次搬工作区. 键是 bot 名字.
	pendingWork sync.Map
	// verified 谁这一轮跑过哪些验证 —— 见 verify.go.
	//
	//	验的动作归 bot, **证据归系统**: 它说"我验过了"和它真跑过测试
	//	在文本上一模一样, 而退出码是它编不出来的.
	verified *verifyLog
	// ── 感知层的三件东西 ── 见 sense.go.
	//
	//	它们放在 components 上而不是各自当全局: 工具要够得着(name_place /
	//	watch_for), 而工具是按 bot 装配的 —— 装配点只有这里.
	//	nil = 这台机器没接感知层, 那几个工具就不挂出来
	places  *osinit.Places
	watches *osinit.Watches
	// routine 长期规律 —— "他常去哪几个地方". 见 osinit/routine.go
	routine *osinit.Routine
	// geo 地图那一侧: 解析地名、他此刻在哪、问路、路况、天气、起名核对.
	// 见 geo.go. nil = 没接感知层; geo.api 没 key = 只有不出网的那几样
	geo *geo
	// mapShot 取一张静态地图 —— /mapshot 那条路用
	mapShot func(ctx context.Context, lat, lon float64, w, h int) ([]byte, error)
	// rules 条件触发 —— "下雨就提醒我带伞". 见 osinit/rules.go
	rules *osinit.Rules
	// devices 屋里接了些什么 —— 反过来问它们要东西时用. 见 Devices.Ask
	devices *osinit.Devices
	// people 屋里住着谁 —— 见 osinit/people.go. **这不是鉴权**, 是归属
	people *osinit.People
	// zone 这台机器算在哪个时区 —— 见 osinit/zone.go. **不属于感知层**:
	// 一台没接采集端的机器照样要知道现在几点
	zone *osinit.Zone
	// agenda 他要做的事和日程 —— 见 osinit/agenda.go. **不属于感知层**
	agenda *osinit.Agenda
	// notes 他让你记住的事 —— 见 osinit/notes.go. **不属于感知层**:
	// 记一句话不需要任何传感器
	notes *osinit.Notes
	// world 此刻的世界 —— bot 问"现在什么情况"时答的就是它. 见 osinit/world.go
	world *osinit.World
	// budget 打扰预算. 主动进程判完之后必须过这道硬闸 —— 提示词里
	// 那三条判据是建议, 这个是闸
	budget *osinit.InterruptBudget
	// deliveries 主动消息的唯一出口 —— 闹钟/主动判断/日报都从这儿
	// 送到人跟前(见 osinit/notice.go)
	deliveries *osinit.Deliveries
	// plans "这个 bot 在哪儿干活"只算一次 —— 见 workplan.go.
	//
	//	能力、提示词、界面三处都读它: 各算一次的话迟早分叉, 而分叉之后
	//	的样子是"界面显示 A, 提示词说 B, 内核只许写 C".
	plans *workPlanner
	// ledger 落盘账本的路径 —— "他今天去过哪儿/收到过什么"要读它,
	// 内存那份被 CompactSense 压过(见 osinit.ScanEvents)
	ledger string
}

// toolsFor 这个 bot 手里有哪些工具.
//
//	**跟 neox-chat 那条路的区别**: 那边 agent 是独立进程, 够不着 OS 的对象,
//	所以闹钟只能 Emit 一条事件出去, 拿不到回执 —— 模型设了一个过去的时刻,
//	它不会当场知道. console 这边 bot 是 in-proc 的, 直接调得到,
//	于是**能在本地验的就在本地验**: Timers.Set 会当场拒掉过去的时间点,
//	那句拒绝直接变成工具结果, 模型下一步就能改对.
func (c *components) toolsFor(p persona, pid abi.ProcessID) *agent.ToolSet {
	if c == nil || c.timers == nil {
		return agent.NewToolSet(agent.DefaultTools())
	}
	// 闹钟挂在 **bot 标签**上, 不是 pid: 进程会死, 对话不死 ——
	// 挂 pid 的话重启一次, 昨天设的提醒就找不到主人了
	thread := p.app
	remind := func(atMs int64, text string) error {
		_, err := c.timers.Set(thread, atMs, text)
		return err
	}
	/*
		周期性"到点看一眼".

		第一次的时刻 = 现在 + 一个周期。**不是立刻响一次**:
		模型刚说完"我每天早上看一眼", 马上又被自己叫醒一次,
		用户会看到它自言自语 —— 而且那一次的判断没有任何新信息。
	*/
	/*
		每天固定时刻叫醒一次.

		── 为什么是"每天几点", 不是"每隔多久" ──

			check_every 是**轮询**: 为了盯住早上 7:05–8:00
			那一小时, 设了个每 10 分钟醒一次 —— 一天 144 次空转, 每次
			烧五千 token 只为回一句"（非窗口期，不动。）", 而那些回执
			还一条条堆进了用户的对话里。

			账本可能留下**三条**几乎一样的巡检(10分/10分/30分),
			因为每次修改计划都会新建一条。

			按时刻就没有这个问题: 一天一次, 醒来就是该干活的时候。
	*/
	everyDay := func(atMs int64, what string) error {
		const day = int64(24 * time.Hour / time.Millisecond)
		_, err := c.timers.SetEvery(thread, atMs, day, what, true)
		return err
	}
	// 现在挂着哪些提醒/盯梢/规则 —— 撤不掉的时候也把它摆出来
	listPending := func() string {
		var b strings.Builder
		if c.timers != nil {
			for _, w := range c.timers.Pending() {
				when := time.UnixMilli(w.At).Format("01-02 15:04")
				if w.Every > 0 {
					when = "每天 " + time.UnixMilli(w.At).Format("15:04")
				}
				fmt.Fprintf(&b, "- [%s] %s %s\n", w.ID, when, w.Text)
			}
		}
		if c.watches != nil {
			for _, w := range c.watches.List() {
				what := w.Kind
				if w.Place != "" {
					what = strings.TrimSpace(w.Kind + " @" + w.Place)
				}
				if w.Raw != "" {
					what = w.Raw
				}
				fmt.Fprintf(&b, "- [%s] 盯着: %s\n", w.ID, what)
			}
		}
		if c.rules != nil {
			for _, r := range c.rules.List() {
				fmt.Fprintf(&b, "- [%s] 规则: %s\n", r.ID, r.Say)
			}
		}
		if b.Len() == 0 {
			return "现在一条提醒、关注、规则都没设。"
		}
		return strings.TrimRight(b.String(), "\n")
	}
	cancel := func(id string) (string, error) {
		if c.timers.Cancel(id) {
			return "撤掉了 " + id + "。", nil
		}
		// 列表里摆着盯梢(g)和规则(r), 撤却只认闹钟: 清理 g1 / r1
		// 会重复得到"没有这条", 使它们留在列表中.
		if c.watches != nil && c.watches.Remove(id) {
			return "撤掉了 " + id + "。", nil
		}
		if c.rules != nil && c.rules.Remove(id) {
			return "撤掉了 " + id + "。", nil
		}
		// **撤不掉要当场说**, 不能回一句"已经交给 OS 了" ——
		// 用户以为取消了提醒, 到点它照样响(或者本来就不存在,
		// 而他以为自己撤掉的是另一条)
		// **别指一个不存在的文件**: .neox/state.txt 是 neox-chat 那条路的东西,
		// 此运行路径没有该文件; 按说明读取会连续失败 12 次
		return "", fmt.Errorf("没有 %s 这条。现在有这些:\n%s", id, listPending())
	}
	recall := func(query string, limit int) ([]abi.RecallHit, error) {
		return c.os.Recall(pid, abi.RecallParams{Query: query, Limit: limit}).Hits, nil
	}
	// 给当前位置起个名 / 登记长期关注 —— **接了感知层才挂**.
	//
	//	没接的时候摆一个用不了的工具比没有更糟: bot 会照着调, 然后
	//	许下一个做不到的承诺("好, 你到家我就告诉你"), 而那条承诺
	//	永远不会兑现, 也永远不会报错.
	var namePlace func(name string, radius float64, sure bool) (string, error)
	if c.geo != nil {
		// **先核对他真在那儿** —— 见 geo.nameHere 开头那一幕
		namePlace = c.geo.nameHere
	}
	// 长期记忆 —— 记住/忘掉一件事. 见 osinit/notes.go.
	//
	//	**归属留空**: 一句话进来只有文字, 没有身份(见 what_now 那段),
	//	所以聊天里记下的事是这台机器公有的. 按人归属要等每个人各自
	//	一把 token, 而那是另一件事
	var remember func(key, text string) (string, error)
	var forgetThat func(key string) (string, error)
	if c.notes != nil {
		remember = func(key, text string) (string, error) {
			note, err := c.notes.Set("", key, text)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("记住了: %s —— %s", note.Key, note.Text), nil
		}
		forgetThat = func(key string) (string, error) {
			if !c.notes.Forget("", key) {
				// **忘不掉要当场说**: 回一句"忘了"而那条还在,
				// 用户以为改过来了, 而它接着照旧的说
				return "", fmt.Errorf("没有「%s」这条。先用 what_now 看一眼记着哪些", key)
			}
			return fmt.Sprintf("忘掉了: %s", key), nil
		}
	}
	var watchFor func(kind, place, say, raw string) error
	if c.watches != nil {
		watchFor = func(kind, place, say, raw string) error {
			_, err := c.watches.Add(kind, place, say, raw)
			return err
		}
	}
	tools := agent.DefaultToolsWith(
		remind,
		nil, // 起地名走 place 那一个, 见 WithPlace
		nil, // 盯一件事并进 notify_when 了, 见 WithWhen
		cancel,
		// 搜网 —— **这台机器配了才挂**. 见 ProviderConfig.SearchAPI:
		// 没配的时候工具表里根本没有它, 于是 bot 会照实说搜不了,
		// 而不是调一个必然失败的工具然后编一个答案
		c.searching(),
		c.seeing(), // 看图: 这台机器配了会看图的模型才挂
		recall,
		c.hireFor(p),
		c.passTo(p),
	)
	tools = agent.WithNotes(tools, remember, forgetThat)
	// 待办和日程 —— 一件事, 不是一句话. 见 agent.AgendaTool
	if c.agenda != nil {
		who := ""
		tools = agent.WithAgenda(tools,
			func(what string, atMs int64, where string) (string, error) {
				task, err := c.agenda.Add(who, what, atMs, where)
				if err != nil {
					return "", err
				}
				when := "没定时间"
				if task.At != 0 {
					when = time.UnixMilli(task.At).Format("01-02 15:04")
				}
				return fmt.Sprintf("记下了[%s] %s: %s", task.ID, when, task.What), nil
			},
			func(withDone bool) string {
				got := c.agenda.Text(who)
				if got == "" {
					return "一件都没有。"
				}
				return got
			},
			func(id string) (string, error) {
				task, ok := c.agenda.Done(id)
				if !ok {
					// **划不掉要当场说**: 回一句"划掉了"而那条还在,
					// 他以为做完了, 而它接着排在那儿
					return "", fmt.Errorf("没有 %s 这一条。先 agenda do=list 照编号念", id)
				}
				return "划掉了: " + task.What, nil
			},
			func(id string) (string, error) {
				if !c.agenda.Drop(id) {
					return "", fmt.Errorf("没有 %s 这一条。先 agenda do=list 照编号念", id)
				}
				return "扔掉了 " + id, nil
			})
	}
	// 换时区 —— 他说一句"我在中国"就得能当场办成. 见 SetTimezoneTool
	if c.zone != nil {
		tools = agent.WithTimezone(tools, func(z string) (string, error) {
			if err := c.zone.Set(z); err != nil {
				return "", err
			}
			// **把改完之后的当地时间报回去**: 只说"改好了"的话, 它
			// 没有任何办法知道自己填对了 —— 而填错一个相近的时区
			// (Asia/Tokyo vs Asia/Shanghai)不会报错
			return fmt.Sprintf("时区改成 %s 了, 现在是 %s",
				c.zone.Name(), time.Now().Format("2006-01-02 15:04")), nil
		})
	}
	// 搬工作区: bot 可自行切到指定目录干活, 不必依赖外部资料卡操作
	tools = agent.WithMoveWork(tools, c.moveWorkFor(p))
	// ── 现在什么情况 ──
	//
	//	**六个位置工具收成一个**(见 agent.WhereTool): 它们答的是同一类
	//	问题, 而摆六个的后果是它每一轮都要在六个里挑 —— 账本里它问
	//	"我现在在哪"时先 what_now、再 run 两次、再 fetch 两次, 而答案
	//	第一次就在手里了.
	if c.world != nil && c.people != nil {
		// **按人分组的那一份**: bot 不知道正在跟它说话的是谁(一句话进来
		// 只有文字, 没有身份), 而把两个人的位置混成一列是最坏的 ——
		// 它会说"你在公司", 而那是她
		now := func() string {
			// ── 位置太旧就现问一次 ──
			//
			//	采集端按变化触发, 人没挪 120 米就一条都不报 —— 而他问
			//	"我在哪条路"的时候, 手上最新那条可能是三分钟前的.
			//	市区里三分钟是两个路口, 答一个过期的路名比说"不知道"
			//	糟得多: 他会照着走.
			//
			//	**只在真旧的时候问**: 一分钟以内的照用, 不白唤醒手机.
			//	问完最多等两秒 —— 再久他就觉得这东西卡了, 而卡比慢一点糟.
			note := ""
			if c.geo != nil {
				if lat, lon, age, ok := c.geo.fix(time.Minute, 2*time.Second); ok {
					note += c.streetNote(lat, lon)
					// ── 位置有多旧, **必须说出来** ──
					//
					//	问了没等到, 或者根本没设备答得了 —— 那时候它手上
					//	就是那条旧的. 不说的话它会一口咬定"你在 X", 而市区里
					//	三分钟是两个路口, 他会照着走.
					if n := staleNote(age); n != "" {
						note += "\n\n" + n
					}
				}
			}
			out := c.world.TextAll(c.people.Name)
			out += note
			// 记着的事跟着一起给 —— 不然 forget_that 要按键去忘,
			// 而它没有任何一条路看得见那些键
			if known := c.knownText(); known != "" {
				if out != "" {
					out += "\n\n"
				}
				out += "他让你记住的事:\n" + known
			}
			// 接下来要做什么也一起给 —— "现在什么情况"里最要紧的一半
			// 就是"接下来干什么", 而分成两次调用是白多一个往返
			if c.agenda != nil {
				if todo := c.agenda.Text(""); todo != "" {
					if out != "" {
						out += "\n\n"
					}
					out += "他要做的事:\n" + todo
				}
			}
			return out
		}
		var habits func() string
		if c.routine != nil && c.places != nil {
			// 已经命过名的地方直接报名字: "你常去的那个地方"和"公司"
			// 是同一处, 报后者才有用
			habits = func() string {
				return c.routine.TextAll(c.people.Name, c.places.Lookup)
			}
		}
		// ── 主动要一个新位置 ──
		//
		//	"别人传输是别人传输, 他自己能拿是自己能拿的事" —— 用户的原话。
		//	这条链底层早就通了(Devices.Ask + Places.WaitFresher), 缺的
		//	是让模型知道自己能。
		fresh := func() string {
			if c.geo == nil || c.devices == nil {
				return "这台机器上没有能报位置的设备。"
			}
			// 冷启一次定位要几秒, 而他在等一句话. 等不到就用手上这条,
			// 并且**照实说它多旧**
			lat, lon, age, ok := c.geo.fix(0, 6*time.Second)
			if !ok {
				return "屋里没有能报位置的设备, 手机也一次位置都没报过 —— **照实说**, 别猜他在哪。"
			}
			street := c.streetNote(lat, lon)
			out := c.world.TextAll(c.people.Name) + street
			if n := staleNote(age); n != "" {
				out += "\n\n" + n
			}
			return out
		}
		// 认得的地方 —— 它原来只能 grep 账本. 带上在哪、跟谁叠着, 见 geo.list
		known := func() string {
			if c.geo == nil {
				return "这台机器还不认得任何地方。"
			}
			return c.geo.list()
		}
		kit := agent.WhereKit{Now: now, Habits: habits, Fresh: fresh, Places: known}
		if c.geo != nil {
			kit.Trail = c.geo.trail
		}
		// 出网的那几样**只在配了 key 的机器上挂** —— 没配的话工具每次返回
		// "没配 key", 而模型看到之后会退回去猜一个时间, 那比没有这个用法更糟
		if c.geo != nil && c.geo.api.Ready() {
			kit.Route, kit.Traffic, kit.Weather, kit.Card =
				c.geo.route, c.geo.traffic, c.geo.weather, c.geo.card
		}
		tools = agent.WithWhere(tools, kit)
	}
	// 给一个地方起名 —— 人在那儿就记坐标, 说得出地址就查地址
	var byAddress func(name, address string, radius float64) (string, error)
	var findPlace func(query, city string) (string, error)
	if c.geo != nil && c.geo.api.Ready() {
		byAddress, findPlace = c.geo.byAddress, c.geo.find
	}
	var forgetPlace func(name string) (string, error)
	if c.geo != nil {
		forgetPlace = c.geo.forget
	}
	tools = agent.WithPlace(tools, namePlace, byAddress, forgetPlace)
	// 查一个地方在哪 —— 见 agent.FindPlaceTool
	tools = agent.WithFindPlace(tools, findPlace)
	// 他那天收到过什么 —— 见 history.go. 账本一直在, 缺的是一条够得着的路
	tools = agent.WithHistory(tools, c.history)
	// 家里的灯和开关 —— **配了家居采集端才挂**. 没接的时候摆一个用不了的
	// 工具比没有更糟: 它会照着调, 然后许一个不会发生的诺
	if envOn("NEOX_HOME_ON") {
		tools = agent.WithHome(tools, c.homeAct)
	}

	// 条件触发 —— 同上, **有世界模型才给**: 没有事实可判的话每条规则
	// 都会永远不成立, 却仍会被登记为可用
	if c.rules != nil {
		tools = agent.WithWhen(tools, func(fact, field, op string, value any,
			from, to, say, why, raw string, urgent bool) (string, error) {
			r, err := c.rules.Add(osinit.Rule{
				When:   []osinit.Cond{{Fact: fact, Field: field, Op: op, Value: value}},
				From:   from,
				To:     to,
				Say:    say,
				Why:    why,
				Urgent: urgent,
				// 缺省一天最多一次 —— 见 rules.go 开头"为什么还要抑制"
				OncePerDay: true,
				Raw:        raw,
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("记下了[%s]: %s %s %v 的时候说「%s」", r.ID, fact, op, value, say), nil
		}, watchFor)
	}
	// 到点看一眼 —— 这台机器有 Timers, 所以给得起.
	// neox-chat 那条路 agent 是独立进程够不着 Timers, 那边就不叠
	tools = agent.WithDaily(tools, remind, everyDay)
	// ── 先看看有什么, 再撤 ──
	//
	//	cancel 只能撤而不能列出对象时, 调用方会按说明去读取
	//	`.neox/state.txt`; 该路径被尝试过 8 次。**能撤的前提是看得见**。
	tools = agent.WithPending(tools, cancel, listPending)
	/**
	 * 合入和同步**只在走了分支隔离的机器上挂出来**.
	 *
	 *	没有 git 的时候它们干不了活(没有分支可合), 而摆一个用不了的工具
	 *	比没有更糟: 它会照着调, 然后许下一个做不到的承诺.
	 */
	if plan, err := c.plans.planOf(p.name); err == nil && plan.Branch != "" {
		tools = agent.WithGit(tools,
			func() (string, error) {
				// **合之前先验一遍**: 它这一轮跑过什么、结果如何, 由系统记着 ——
				// 见 verify.go. 每个人只验自己那份的话, 合起来坏了没人会发现
				got, err := MergeUpChecked(plan.Project, plan.Dir, plan.Branch, p.name,
					TurnNote{Doing: c.doingOf(p.name), Verified: c.verified.peekFor(p.name)})
				if err != nil {
					return "", err
				}
				return got.Message, nil
			},
			func() (string, error) {
				got, err := SyncDownAs(plan.Project, plan.Dir, plan.Branch,
					TurnNote{Doing: c.doingOf(p.name), Verified: c.verified.peekFor(p.name)})
				if err != nil {
					return "", err
				}
				return got.Message, nil
			},
			c.handOverFrom(p, plan))
	}
	return agent.NewToolSet(tools)
}

/**
 * seeing —— 看图这条路通不通.
 *
 *	返回 nil = 这台机器不认图, 于是**工具表里根本没有 view_image**.
 *	摆一个用不了的工具比没有更糟: 它会照着调, 然后许下一个做不到的承诺.
 *
 *	**闭包里每次现问一遍 o.Viewer()**: 用户可能在两次调用之间关掉了
 *	那个勾. 拿启动时的那份去调, 换来的是一句莫名其妙的供应商报错.
 */
// searching 搜网这条路通不通.
//
//	返回 nil = 这台机器没配搜索服务, 于是**工具表里根本没有 web_search**。
//	摆一个用不了的工具比没有更糟: 它会照着调, 拿到一句"没配搜索",
//	而那时候它已经对用户许过诺了。
//
//	**每次现问一遍**: 用户可能刚在设置里填上 —— 拿启动时的那份去调,
//	换来的是"设置页上填着而 bot 说搜不了"。
// streetNote 他问"我在哪"时, 把路名和楼一起给 —— **顺带把缓存焐热**.
//
//	起过名的地方, 事实里只有"在公司": 14:42 他问"在哪条路边、什么商业楼
//	里", 它答"公司这个点只存了坐标, 路名给不了". 所以那种时候多给一行.
//	没起过名的地方不用多给 —— 这一查进了缓存, 事实那一行自己就会说成
//	"在固镇县庙岗路，汇金国际碧桂苑内".
func (c *components) streetNote(lat, lon float64) string {
	if c.geo == nil {
		return ""
	}
	near, ok := c.geo.street(lat, lon)
	if !ok || c.places == nil {
		return ""
	}
	here := c.places.Lookup(lat, lon)
	if here == "" {
		return ""
	}
	out := "\n\n具体位置: " + near
	// 同一处起了两个名字(公司 / 凤凰国际广场) —— 摆出来, 收不收拾由它定
	var also []string
	for _, p := range c.places.Known() {
		if p.Name != here && osinit.MetersBetween(lat, lon, p.Lat, p.Lon) <= p.Radius {
			also = append(also, "「"+p.Name+"」")
		}
	}
	if len(also) > 0 {
		out += "（这儿也在" + strings.Join(also, "、") + "的圈里）"
	}
	return out
}

func (c *components) searching() func(query string, limit int) ([]abi.SearchHit, error) {
	if c == nil || c.os == nil || c.os.Searcher() == nil {
		return nil
	}
	return func(query string, limit int) ([]abi.SearchHit, error) {
		s := c.os.Searcher()
		if s == nil {
			return nil, fmt.Errorf(
				"这台机器现在没接搜索服务了（设置里那一格被清掉了）。" +
					"**照实告诉他搜不了**, 别编一个答案")
		}
		res, err := s.Search(context.Background(),
			abi.SearchParams{Query: query, Limit: limit})
		if err != nil {
			return nil, err
		}
		return res.Hits, nil
	}
}

func (c *components) seeing() func(mediaType, dataB64, question string) (abi.SeeResult, error) {
	if c == nil || c.os == nil || c.os.Viewer() == nil {
		return nil
	}
	return func(mediaType, dataB64, question string) (abi.SeeResult, error) {
		viewer := c.os.Viewer()
		if viewer == nil {
			return abi.SeeResult{}, fmt.Errorf(
				"这台机器现在没接会看图的模型了（设置里那个「能看图」被关掉了）。" +
					"别猜图里是什么，让用户把关键内容打出来")
		}
		return viewer.See(context.Background(), abi.SeeParams{
			MediaType: mediaType, DataB64: dataB64, Question: question,
		})
	}
}

/**
 * 这一轮在干什么 —— 宿主替它记账时拿来当提交标题.
 *
 *	**按 bot 存, 不按进程**: 进程会死, 对话不死. 而且一个 bot 同时
 *	只在干一件事, 不需要更细的粒度.
 */
func (c *components) nowDoing(bot, task string) {
	if c == nil {
		return
	}
	c.doingMu.Lock()
	defer c.doingMu.Unlock()
	if c.doing == nil {
		c.doing = map[string]string{}
	}
	if next, ok := c.willDo[bot]; ok && next != "" {
		// 宿主早就知道这一轮是干什么的 —— 用一次就丢, 别粘在下一轮上
		delete(c.willDo, bot)
		c.doing[bot] = next
		return
	}
	c.doing[bot] = task
}

// willBe 宿主已经知道这个人下一轮要干什么 —— 见 nowDoing.
//
//	只有宿主自己发起的那几件事(交接是一件)才用得上: 那时候
//	叫醒它的是系统的一句话, 而那句话按规矩不当提交标题.
func (c *components) willBe(bot, doing string) {
	if c == nil {
		return
	}
	c.doingMu.Lock()
	defer c.doingMu.Unlock()
	if c.willDo == nil {
		c.willDo = map[string]string{}
	}
	c.willDo[bot] = doing
}

/**
 * autoResume 掉线之后自己接着干 —— 缺省关着.
 *
 *	**自己动起来是件要用户点头的事**: 一个重启之后自动开跑的东西,
 *	第一次会吓到人, 第二次会花掉他没打算花的钱.
 */
func (c *components) autoResume() bool {
	return c != nil && c.resume != nil && c.resume()
}

func (c *components) doingOf(bot string) string {
	if c == nil {
		return ""
	}
	c.doingMu.Lock()
	defer c.doingMu.Unlock()
	return c.doing[bot]
}

// fireInto 闹钟到点了怎么把话送出去.
//
// ── 不过模型 ──
//
//	Wake.Text 是**用户自己写的原话**("12 点的火车"), 到点原样送回去.
//	中间过一次模型就多一次跑偏的机会, 而这件事没有任何需要判断的地方.
//	(这条规矩写在 osinit/timers.go 的开头, 这里只是遵守它.)
//
// ── 送到哪儿 ──
//
//	送进**设它的那个 bot 的事件流**, 于是界面上它出现在那段对话里,
//	跟这个 bot 说过的别的话在一起. 单开一条"系统通知"通道的话,
//	用户就得在两个地方找同一件事.
//
//	找不到主人时**不许吞掉**: 那个 bot 可能刚被删了, 但提醒是用户要的,
//	宁可落在别处也不能静默消失.
func (c *components) fireInto(w osinit.Wake, lateMs int64) {
	/*
		Think 那条走完全不同的一条路.

		── 为什么不能跟"说原话"走同一条 ──

				说原话那条是**把一句现成的话贴到事件流上**: 界面看得见,
				bot 完全不知道发生过 —— 它没有被叫醒.

				而 Think 那条到点时**没有一句现成的话**: 出门时间
				取决于当天日程、此刻路况和当前位置, 全要现算.
			所以它要的不是"贴一句话", 是**把 bot 叫醒去判一次**.

				投进收件箱, 走的就是普通输入完全一样的那条路 ——
			同一个 Recv、同一条账本、同一套打扰预算。bot 分不出
			这句话是闹钟问的还是人问的, 而那正是想要的: 少一条通道
			就少一处会坏的地方。
	*/
	if w.Think {
		c.fireThink(w, lateMs)
		return
	}
	text := "⏰ " + w.Text
	// 晚了多久要说 —— 用户据此判断还来不来得及.
	// 一分钟以内不说: 那是正常的 tick 抖动, 说了是噪音.
	if lateMs > 60_000 {
		text += fmt.Sprintf("\n(这条晚了 %s —— 机器当时没开着)", roughly(lateMs))
	}
	/**
	 * **done 表示这一块就是全部**.
	 *
	 *	say 那条通道默认是**流**: 界面收到一块就开一行, 等后面的块接上去,
	 *	行一直开着(开着的行画一根光标, 而且按纯文本画 —— markdown 要等
	 *	写完才敢解析). 关它靠的是进程状态变化.
	 *
	 *	而闹钟这一条是**整条发出去的**, 而且发的时候进程正停在 waiting ——
	 *	不会再有状态变化了. 于是那行永远开着: 界面上一句早就说完的提醒,
	 *	屁股后面会挂着一根不会消失的光标.
	 *
	 *	所以由发的人说清楚"没有下一块了", 不让界面去猜.
	 */
	/*
		── 事件流和投递总线都必须发送 ──

			只把话贴到 bot 的事件流上时,
			**App 开着才看得见** —— 而闹钟存在的全部理由就是
			"我怕自己忘", 一个要求你盯着屏幕的闹钟等于没有闹钟。

			现在两件事都做: 贴事件流(界面上它出现在那段对话里),
			**并且**进投递总线(手机端据此推送)。
			两条都要 —— 少哪一条都有一半场景是坏的。
	*/
	c.deliveries.Post(osinit.Deliver{
		// **给人看的名字, 不是 app 标签** —— 通知栏里跳出来一个
		// "ops · 提醒", 用户要愣一下才反应过来是谁
		From: c.nameOfBot(w.Thread),
		Kind: osinit.DeliverRemind,
		Text: w.Text,
		// **闹钟一律 urgent**: 这是用户自己设的, 不是它想说的 ——
		// 打扰预算防的是"它没事找事", 而这件事是他自己让做的
		Urgent: true,
	})

	pid := c.pidOfBot(w.Thread)
	if pid == "" {
		c.os.Log().Append(abi.ProcessID("os"), abi.EvProcOutput, map[string]any{
			"stream": "wake-" + w.ID, "channel": "say", "done": true,
			"text": text + "\n(设这条提醒的 bot 已经不在了)"})
		return
	}
	c.os.Log().Append(pid, abi.EvProcOutput, map[string]any{
		"stream": "wake-" + w.ID, "channel": "say", "done": true, "text": text})
}

/*
fireThink 到点了, 把 bot 叫醒去判一次.

	── 三条判据 ──

	**投进收件箱, 不贴事件流**: 贴事件流只是让用户看见一句话,
	bot 压根没醒. 而这条要的正是它醒过来现算.

	**说清是闹钟叫的**: bot 得知道这不是人在问 —— 人问了在等回答,
	沉默是失败; 闹钟叫的没人在等, **沉默是常态**. 说不清的话它会
	每次都硬憋出一句, 而那正是打扰预算要防的.

	**晚了多久要带上**: 一条"看一眼今天的通勤"晚了六小时的话,
	该判的事早就过去了 —— 让它自己决定还值不值得说。
*/
func (c *components) fireThink(w osinit.Wake, lateMs int64) {
	pid := c.pidOfBot(w.Thread)
	if pid == "" {
		// **找不到主人不许静默吞掉**: 这条是用户设的, 而"到点没反应"
		// 跟"这台机器很安静"长得一模一样
		c.os.Log().Append(abi.ProcessID("os"), abi.EvProcOutput, map[string]any{
			"stream": "wake-" + w.ID, "channel": "say", "done": true,
			"text": "⏰ 到点该看一眼「" + w.Text + "」, 但设它的 bot 已经不在了"})
		return
	}
	// **闹钟这一路原来不带钟点**: 它每次醒来第一件事是 run `date` ——
	// 账本里 24 次. 人说话那一路早就带了(agent.nowNote)
	text := "⏰ 到点了, 该看一眼: " + w.Text + agent.NowNote() +
		"\n\n(这是你自己设的定时任务, **没有人在等你回话** —— " +
		"看完觉得没什么要紧的就什么都不用说。真有要紧的再开口。)"
	if lateMs > 60_000 {
		text += "\n(这一跳晚了 " + roughly(lateMs) + " —— 机器当时没开着, " +
			"该判的事可能已经过去了)"
	}
	if !c.os.Deliver(pid, osinit.Delivery{
		Text: text,
		// Said 是账本里记的原话 —— 记题目本身, 不记那一大段信封,
		// 否则"我让它盯什么了"这个问题在账本里读起来全是模板
		Said: "⏰ " + w.Text,
		From: "闹钟",
		// **界面两头都不画**: 题目不是用户打的字, 回话是任务回执.
		//
		//	否则界面会显示一屏"（非窗口期，不动。）", 像是代替人发出的消息.
		//	它真有要紧的事要说, 走的是
		//	投递通道, 那条照样到他跟前
		Quiet: true,
	}) {
		c.os.Log().Append(pid, abi.EvProcOutput, map[string]any{
			"stream": "wake-" + w.ID, "channel": "say", "done": true,
			"text": "⏰ 「" + w.Text + "」到点了, 但这个 bot 收不下(它可能已经停了)"})
	}
}

// pidOfBot 哪个活着的进程顶着这个 bot 标签. 没有就是空.
func (c *components) pidOfBot(bot string) abi.ProcessID {
	for _, info := range c.os.List() {
		if info.Spec.Labels["bot"] == bot {
			return info.PID
		}
	}
	return ""
}

/*
nameOfBot 这个 bot 给人看的名字.

	── 为什么不能直接用 Wake.Thread ──

		Thread 存的是 **app 标签**("ops" / "research"), 那是内部用来
		认进程的东西。通知栏若显示 "ops · 提醒", 无法直接辨认对应的 bot;
		因此优先显示创建时设定的名称。

	进程没了就退回标签: 那时候没有别的东西可查, 而一个标签
	总比空着强 —— 至少还认得出是哪一摊活。
*/
func (c *components) nameOfBot(bot string) string {
	for _, info := range c.os.List() {
		if info.Spec.Labels["bot"] == bot {
			if n := info.Spec.Labels["name"]; n != "" {
				return n
			}
			if info.Spec.Name != "" {
				return info.Spec.Name
			}
		}
	}
	return bot
}

// roughly 把毫秒说成人话. 提醒晚了多久, 精确到秒没有意义.
func roughly(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	default:
		return fmt.Sprintf("%.1f 天", d.Hours()/24)
	}
}

// flatEvents 把按 pid 分好的快照摊平.
//
// 闹钟的装回要看**全部**事件: 设它的那个进程早就死了(pid 都变了),
// 而 wake.set / wake.fired / wake.cancelled 还留在它名下.
func flatEvents(snap map[abi.ProcessID][]abi.Event) []abi.Event {
	var out []abi.Event
	for _, evs := range snap {
		out = append(out, evs...)
	}
	return out
}

// historyOf 这个 bot 以前说过的话 —— 跨进程、跨重启.
//
// ── 为什么要它 ──
//
//	"进程会死, 对话不死"这条, 界面上一直是成立的(事件日志按 bot 标签
//	拼得起来), 但**bot 自己是失忆的**: console 每次都从 NewWindow 起,
//	于是重启之后用户接着问"那个记事本做完了吗", 它答"我不知道你说的是哪个"
//	—— 而它昨天干了一下午. 界面上明明摆着那段历史.
//
//	这是最糟的一种失灵: 没有任何地方显示出错, 用户看到的是"它在装傻".
//
// ── 按 pid 分组, 不按时间戳排 ──
//
//	同一个 bot 同一时刻只有一个活进程, 所以"哪个进程在前"是明确的.
//	而按全局时间戳排会把两个进程的事件交错在一起 —— 一旦有过重叠
//	(换房间时新进程先起、旧的后杀), 拼出来的对话就是乱的.
func (c *components) historyOf(bot string, self abi.ProcessID) []abi.Event {
	type run struct {
		at  int64
		evs []abi.Event
	}
	var runs []run
	for pid, evs := range c.os.Log().Snapshot() {
		if pid == self || len(evs) == 0 || botOf(evs) != bot {
			continue
		}
		runs = append(runs, run{at: evs[0].At, evs: evs})
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].at != runs[j].at {
			return runs[i].at < runs[j].at
		}
		// 同一毫秒起的两个进程要有个稳定的先后 —— 不然装回来的历史
		// 每次开机顺序都不一样
		return runs[i].evs[0].PID < runs[j].evs[0].PID
	})
	var out []abi.Event
	for _, r := range runs {
		out = append(out, r.evs...)
	}
	return out
}

/**
 * handOverFrom 把这摊活交给同项目的另一个人.
 *
 *	**接手的人必须已经在这个项目里**: 交接是把分支挪过去, 而分支属于
 *	一个仓库 —— 交给一个在别处干活的人, 他签出来的是另一个项目的代码.
 *	想交给一个还不存在的人, 先 recruit 把他拉进来(那一步会问用户,
 *	因为拉人是唯一会让花费翻倍的动作).
 */
func (c *components) handOverFrom(boss persona, from Plan) func(string) (string, error) {
	if c == nil || c.os == nil {
		return nil
	}
	return func(to string) (string, error) {
		to = strings.TrimSpace(to)
		if to == boss.name {
			return "", fmt.Errorf("这是你自己")
		}
		toPlan, err := c.plans.planOf(to)
		if err != nil {
			// **把同项目还有谁列出来**: 只说"没这个人"的话它只能瞎猜名字
			return "", fmt.Errorf("「%s」现在没在干活。同一个项目里在干的是: %s",
				to, strings.Join(c.inProject(from.Project, boss.name), " "))
		}
		got, err := HandOver(from.Project, from, toPlan, boss.name, to)
		if err != nil {
			return "", err
		}
		// 留一笔 —— 三天之后"这块归谁"要在项目卡上答得出来, 见 handoff_log.go
		noteHandoff(from.Project, got)
		/**
		 * **接手这一轮的提交叫什么, 现在就定下来**.
		 *
		 *	接手回合由系统交接消息启动, 而系统消息不当提交标题
		 *	(见 TurnNote.title). 若不在交接时指定标题, 提交会退化为
		 *	"同步前把手上的活收一下"这类无法说明工作的套话.
		 *
		 *	交接目标在此时已知, 直接使用它作为标题, 不让模型猜测.
		 */
		c.willBe(to, "接手"+boss.name+"的活")
		// 交接说明**送到接手的人手上** —— 他现在就该知道自己接了什么
		for _, info := range c.os.List() {
			if info.State.IsTerminal() || info.Spec.Labels["name"] != to {
				continue
			}
			c.os.Deliver(info.PID, osinit.Delivery{
				Text: got.Message, Said: got.Message, From: "系统", Relay: true,
			})
			break
		}
		return fmt.Sprintf("交给「%s」了：%d 次提交、%d 个文件都在他手上了。"+
			"你这条分支还留着(你干过的东西不会消失)，但这摊活从现在起归他。",
			to, got.Commits, len(got.Files)), nil
	}
}

// inProject 这个项目里还有谁在干活.
func (c *components) inProject(project, except string) []string {
	var out []string
	for _, info := range c.os.List() {
		if info.State.IsTerminal() {
			continue
		}
		name := info.Spec.Labels["name"]
		if name == "" || name == except || info.Spec.Labels["project"] != project {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{"(没有别人)"}
	}
	return out
}

/**
 * watchMerges 有人合进主干时, **把话送到真正受影响的人手上**.
 *
 *	受影响的判据是文件交集(见 bus.go): 这次合进去的文件里有没有你也在改的.
 *	同项目但没交集的不打扰 —— 每次合入都群发一遍, 三天之后没人会读它,
 *	而一条被无视的通知等于不存在.
 *
 *	送的方式跟"同屋交办"走同一条路(Deliver), 并且**标清是系统说的**:
 *	不标的话它会把这条当成用户的新要求去执行.
 */
func (c *components) watchMerges() {
	Events.OnMerged(func(event Merged) {
		if c == nil || c.os == nil || len(event.Files) == 0 {
			return
		}
		for _, info := range c.os.List() {
			if info.State.IsTerminal() {
				continue
			}
			labels := info.Spec.Labels
			who := labels["name"]
			// 合的人自己不用收 —— 他刚干完这件事
			if who == "" || who == event.Bot || labels["project"] != event.Project {
				continue
			}
			plan, err := c.plans.planOf(who)
			if err != nil || plan.Branch == "" {
				continue
			}
			shared := overlap(event.Files, touchedBy(plan.Dir, plan.Branch))
			if len(shared) == 0 {
				continue // 跟它没关系
			}
			text := mergedNotice(event.Bot, shared, len(event.Files))
			c.os.Deliver(info.PID, osinit.Delivery{
				Text: text, Said: text, From: "系统",
				// 到此为止: 收到这条的人不该再往下转 —— 它不是一件要交办的活
				Relay: true,
			})
		}
	})
}

// botOf 这一串事件是谁的. 标签在**第一条**事件里(进程创建时定死).
//
// **两种形态都要认**: 同一份事件, 活着的时候 labels 是 map[string]string
// (原样的 Go 值), 从账本装回来之后是 map[string]any (过了一趟 JSON).
// 只认后者的话, 症状是"重启前拼不起来、重启后能拼" —— 而这恰恰跟
// 我们想测的东西反着, 极难看出来. (osinit/os.go 的 threadOf 同样两种都认.)
func botOf(evs []abi.Event) string {
	m, ok := evs[0].Payload.(map[string]any)
	if !ok {
		return ""
	}
	switch labels := m["labels"].(type) {
	case map[string]string:
		return labels["bot"]
	case map[string]any:
		bot, _ := labels["bot"].(string)
		return bot
	}
	return ""
}

// hireFor 给这个 bot 的"拉人"能力. 起不了进程就返回 nil —— 工具表里
// 也就不会有 recruit, 提示词里一个字都不提.
func (c *components) hireFor(boss persona) func(name, role string) (string, error) {
	if c == nil || c.spawn == nil || c.roster == nil {
		return nil
	}
	return func(name, role string) (string, error) {
		if _, _, err := findPersona(c.roster, name); err == nil {
			// **重名要当场拒**: 名册按名字认人, 侧栏也按名字认人 ——
			// 起两个同名的, 界面上就是同一个人出现两次, 而且两个都活着
			return "", fmt.Errorf("已经有一个叫「%s」的了, 换个名字", name)
		}
		/**
		 * 没有房间就**当场建一个**.
		 *
		 *	拉人是为了一起干活, 而房间里"听见对方"是这套东西唯一的活口 ——
		 *	拉进来却看不见对方, 等于没拉成. 让用户事后自己去拉, 正是
		 *	这个工具要省掉的那一步.
		 *
		 *	房间名取项目目录的最后一段: 它就是这摊活的名字, 用户一眼认得出.
		 */
		thread := boss.thread
		formed := false
		if thread == "" {
			thread = roomNameFor(boss)
			formed = true
		}
		hand := persona{
			name: name, app: "custom-" + slug(name),
			// **同一个房间、同一个工作区**: 拉人是为了一起干这摊活,
			// 不是各干各的. 分开目录的话他连你写的代码都改不了.
			thread: thread, work: boss.work,
			role:  role,
			intro: "报到。" + role,
			tools: true,
		}
		if _, err := c.spawn(hand); err != nil {
			return "", err
		}
		// 起成了才记名册 —— 记了却起不来的话, 下次开机会一直报错
		if err := c.roster.add(savedBot{Name: name, Thread: thread, Work: boss.work, Role: role}); err != nil {
			return "", fmt.Errorf("人起来了, 但名册没记上(重启后他会不见): %v", err)
		}
		out := "「" + name + "」上线了, 岗位是: " + role
		if boss.work != "" {
			out += "\n工作区跟你同一个: " + boss.work
		}
		if formed {
			// 开房的那个就是群主 —— 记下来, 界面要摆出来
			if c.ownRoom != nil {
				if err := c.ownRoom(thread, boss.name); err != nil {
					fmt.Fprintf(os.Stderr, "群主没记上(%s → %s): %v\n", thread, boss.name, err)
				}
			}
			// 招人的那个**这一轮说完才挪** —— 它现在正卡在这个工具里等返回,
			// 当场换进程就把这一轮的活断在半路了
			c.pending.Store(boss.name, thread)
			out += "\n\n我给你俩开了个房间「" + thread + "」, 他已经在里面了。" +
				"你这一轮说完话之后会自动进去(进程会重起一次, **对话不会断**)。" +
				"进去之后要点他就 @" + name + "。"
		} else {
			out += "\n你俩在同一个房间「" + boss.thread + "」, 用户说话时他能看见你们的对话。" +
				"要点他就 @" + name + "。"
		}
		return out, nil
	}
}

// roomNameFor 给一摊活起个房间名.
//
// 取工作区目录的最后一段 —— 它就是这摊活的名字, 用户一眼认得出.
// 没有工作区的(系统分的匿名目录)就用招人那个的名字, 至少能认出是谁的组.
func roomNameFor(boss persona) string {
	if boss.work != "" {
		if base := filepath.Base(boss.work); base != "" && base != "." && base != "/" {
			return "#" + base
		}
	}
	return "#" + boss.name
}

/**
 * moveWorkFor 给这个 bot"搬工作区"的能力.
 *
 *	切换工作区由宿主的 rebind 能力完成; 将它暴露给 bot 后, 工作区可在
 *	对话中切换, 不必依赖平台层操作.
 *
 *	边界没有变宽的口子:
 *	  · 路径过 resolveWork —— 家目录本身、根、账本目录一律拒
 *	  · 目录必须已经存在 —— "搬去一个不存在的地方"多半是路径听岔了
 *	  · **这一轮说完才搬**(同 applyPendingMove): 换工作区是换进程,
 *	    当场搬会把它正干着的这一轮断在半路
 */
func (c *components) moveWorkFor(p persona) func(string) (string, error) {
	if c == nil || c.rebind == nil {
		return nil
	}
	return func(raw string) (string, error) {
		work, err := resolveWork(raw)
		if err != nil {
			return "", err
		}
		if work == "" {
			return "", fmt.Errorf("要搬去哪儿? 给一条绝对路径或 ~/ 开头的路径")
		}
		if info, err := os.Stat(work); err != nil || !info.IsDir() {
			return "", fmt.Errorf("%s 不是一个存在的目录 —— 跟用户确认路径, 或者先把它建出来", work)
		}
		c.pendingWork.Store(p.name, work)
		return "登记好了。这一轮说完话我就搬到 " + work + " 接着干 —— 进程会重起一次, 对话不断。", nil
	}
}

// applyPendingRebind 说完话了, 把欠着的"搬工作区"落掉. 同 applyPendingMove.
func (c *components) applyPendingRebind(name string) {
	work, ok := c.pendingWork.LoadAndDelete(name)
	if !ok || c.rebind == nil {
		return
	}
	if err := c.rebind(name, work.(string)); err != nil {
		fmt.Fprintf(os.Stderr, "把 %s 搬到 %s 失败: %v\n", name, work, err)
	}
}

// applyPendingMove 招人的那个说完话了, 现在把它挪进房间.
//
// 挂在进程状态上而不是定时器上: **"这一轮说完了"是一个事实, 不是一段时间**.
// 拿时间猜的话, 活干得久一点就会在半路上被换掉进程.
func (c *components) applyPendingMove(name string) {
	thread, ok := c.pending.LoadAndDelete(name)
	if !ok || c.moveInto == nil {
		return
	}
	if err := c.moveInto(name, thread.(string)); err != nil {
		fmt.Fprintf(os.Stderr, "把 %s 挪进 %s 失败: %v\n", name, thread, err)
	}
}

// waitingBots 事件流里认出"谁刚说完话". 返回 bot 名字, 空 = 这条不是.
func waitingBot(o *osinit.OS, e abi.Event) string {
	if e.Kind != abi.EvProcState {
		return ""
	}
	m, ok := e.Payload.(map[string]any)
	if !ok {
		return ""
	}
	// **两种形态都要认**: 活着的时候 state 是 abi.ProcessState(原样的 Go 值),
	// 从账本装回来之后是 string(过了一趟 JSON).
	//
	// 只认 string 的后果是这个功能**只在重放历史时生效, 真跑的时候不生效** ——
	// 而这恰恰会造成"看起来测过了": 新成员进入房间后,
	// 招募它的 bot 永远不会被挪过去.
	//
	// labels 同样会在内存中是 map[string]string、重放后是 map[string]any;
	// 同类的反序列化边界都必须兼容.
	switch state := m["state"].(type) {
	case string:
		if state != string(abi.StateWaiting) {
			return ""
		}
	case abi.ProcessState:
		if state != abi.StateWaiting {
			return ""
		}
	default:
		return ""
	}
	info, ok := o.Info(e.PID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(info.Spec.Name)
}

// passTo 把一件事交办给同屋的人 —— handoff 工具的宿主侧.
//
//	房间若是**单向**的, 只有客户端输入会捎带上下文(roomContext.ts),
//	除此之外没有任何东西能让一个 bot 动起来. 领头 bot 只能提出分工,
//	再停下来等待外部转达 —— 判断和执行被割裂.
//
//	nil = 这个 bot 不在房间里. 一个人待着的没有"同屋的人", 摆着这个工具
//	它会照着调然后拿到一句"这儿没有别人" —— 而它已经许过诺了.
func (c *components) passTo(boss persona) func(who, task string) (string, error) {
	if c == nil || c.os == nil || boss.thread == "" {
		return nil
	}
	return func(who, task string) (string, error) {
		if who == boss.name {
			return "", fmt.Errorf("这是你自己。要么自己做, 要么交给屋里别人")
		}
		var target abi.ProcessID
		var roommates []string
		for _, info := range c.os.List() {
			l := info.Spec.Labels
			if l["thread"] != boss.thread || l["name"] == boss.name {
				continue
			}
			// **已经结束的进程不算在屋里**: 投给它的话 Deliver 返回 false,
			// 而那时候话已经"发出去"了 —— 派活的那个以为交办成功了
			if info.State.IsTerminal() {
				continue
			}
			roommates = append(roommates, l["name"])
			if l["name"] == who {
				target = info.PID
			}
		}
		if target == "" {
			// **把屋里有谁列出来**: 只说"没这个人"的话它只能瞎猜名字,
			// 一次次试到步数用完
			if len(roommates) == 0 {
				return "", fmt.Errorf("屋里现在只有你一个人, 没人可交办")
			}
			return "", fmt.Errorf("屋里没有叫「%s」的。现在在屋里的是: %s",
				who, strings.Join(roommates, " "))
		}
		/**
		 * **必须标清楚这是同事说的**.
		 *
		 *	跟 roomContext 那条信封同一个理由: 不标的话他会把同事的话
		 *	当成用户的要求去执行 —— 而同事可能正在把一件用户没要求过的事
		 *	派出去.
		 */
		text := "[同屋的「" + boss.name + "」把这件事交办给你, 不是用户说的]\n" + task
		if !c.os.Deliver(target, osinit.Delivery{
			Text: text, Said: task, From: boss.name,
			// 到此为止: 收到这一轮的人不能再往下转 —— 见 agent/handoff.go
			Relay: true,
		}) {
			return "", fmt.Errorf("「%s」现在收不了话(可能刚结束)。自己干, 或者告诉用户", who)
		}
		// "你收不到他的回复"那句由工具自己接 —— 换个宿主不该丢, 见 agent/handoff.go
		return "已经交给「" + who + "」了。", nil
	}
}

// knownText 他让你记住的事, 摊平成提示词那一段.
//
//	空 = 什么都没记过, 那一层就不出现 —— 摆一个空标题会让它以为
//	"记忆是空的"是一条事实, 然后去解释这件事
func (c *components) knownText() string {
	if c == nil || c.notes == nil {
		return ""
	}
	return c.notes.Text("")
}
