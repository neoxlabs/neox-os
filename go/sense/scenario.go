package sense

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// 一天的住宅活动 —— 用来量安静率的那把尺子.
//
// ── 为什么需要它 ──
//
// **安静率是整个感知层唯一的验收判据**("大部分时候安静"), 而它从来
// 没被量过: 噪音清干净之后, 一个没人活动的家一小时接近 0 条信号
// 没有活动就没有窗口, 没有窗口就没有安静率 ——
// 一个没人的房子, 分母是 0.
//
// ── 必须驱动真 HA 的真实体 ──
//
// 不能往采集入口灌合成信号. 灌信号会绕过采集器的**全部**判据 ——
// 钟、上线风暴、数值阈值、语义 kind 全都不经过. 那样量出来的是
// "我编的信号有多吵", 不是"这套过滤有多好", 而后者才是要验收的东西.
//
// 所以这里只描述**服务调用**: 开灯、锁门、放音乐 —— 跟一个人在家里
// 做的事一一对应, 由 HA 自己产生状态变化, 再由采集器照常拉回来.
//
// ── 时间是压缩的, 但节奏保真 ──
//
// AtMin 是"一天里的第几分钟". 跑的时候按一个比例压缩(比如 72:1,
// 一天压成 20 分钟), 事件之间的**相对间隔**保持不变.
//
// **这是一个诚实的限制**: 压缩之后窗口(60 秒)相当于真实世界的
// 一个多小时, 于是"一个窗口里挤了几件事"会比真实情况多.
// 所以跑的时候窗口也要按同一比例缩 —— 而这一条必须写下来,
// 否则量出来的安静率会被当成真实值.
type Action struct {
	// AtMin 一天里的第几分钟
	AtMin int
	// Service 形如 light/turn_on —— **是一次真的 HA 服务调用**
	Service string
	Entity  string
	// Worthy 这件事**他不知道, 而且他会想知道**.
	//
	// ── 定义收紧过一次 ──
	//
	// 原来写的是"值不值得打扰用户", 太松 —— 于是"出门锁门"被标成了 ★,
	// 而 S48 的结论里我自己写的是"第一件不说是对的: 出门锁门是他自己
	// 干的". **两处互相矛盾, 而矛盾的那一边正是验收要拿来当期望值的.**
	//
	// 收紧成"他自己刚做的事不算": 他知道自己锁了门, 告诉他等于复读.
	//
	// 这个标注**不是给系统看的, 是验收的期望值**: 它开口的那几次,
	// 是不是正好落在这几件上.
	Worthy bool
	// Note 这一步在讲什么 —— 报告里要能对得上
	Note string
}

// HouseholdDay 一天: 起床 → 出门 → 白天 → 回家 → 晚上 → 睡前.
//
// 用的都是 HA demo 集成里现成的实体, 于是不需要配任何东西就能跑.
func HouseholdDay() []Action {
	return []Action{
		// ── 早晨 ──
		{7 * 60, "light/turn_on", "light.bed_light", false, "起床, 卧室灯"},
		{7*60 + 5, "light/turn_on", "light.ceiling_lights", false, "客厅灯"},
		{7*60 + 20, "media_player/turn_on", "media_player.living_room", false, "放个音乐"},
		{7*60 + 45, "light/turn_off", "light.bed_light", false, "卧室灯关了"},
		{8 * 60, "cover/open_cover", "cover.living_room_window", false, "拉开窗帘"},
		// 出门: 门锁是不可逆的事, 值得说
		{8*60 + 30, "media_player/turn_off", "media_player.living_room", false, "关音乐"},
		{8*60 + 32, "light/turn_off", "light.ceiling_lights", false, "关灯"},
		// **不标 ★**: 是他自己锁的门, 告诉他等于复读
		{8*60 + 35, "lock/lock", "lock.front_door", false, "出门锁门"},

		// ── 白天(没人) ──
		{10 * 60, "vacuum/start", "vacuum.1_first_floor", false, "扫地机开始扫"},
		{11 * 60, "vacuum/return_to_base", "vacuum.1_first_floor", false, "扫地机回去了"},
		{13 * 60, "fan/turn_on", "fan.living_room_fan", false, "热了, 风扇"},
		{15 * 60, "fan/turn_off", "fan.living_room_fan", false, "风扇关"},
		// 白天家里门开了 —— 没人在家的时候, 这件事值得说
		{15*60 + 30, "lock/unlock", "lock.front_door", true, "白天门开了(没人在家)"},
		{15*60 + 35, "lock/lock", "lock.front_door", false, "又锁上了"},

		// ── 傍晚回家 ──
		{18 * 60, "lock/unlock", "lock.front_door", false, "回家开门"},
		{18*60 + 2, "light/turn_on", "light.ceiling_lights", false, "开灯"},
		{18*60 + 30, "media_player/turn_on", "media_player.lounge_room", false, "看电视"},
		{19 * 60, "cover/close_cover", "cover.living_room_window", false, "关窗帘"},

		// ── 晚上 ──
		{21 * 60, "media_player/turn_off", "media_player.lounge_room", false, "关电视"},
		{21*60 + 30, "light/turn_on", "light.bed_light", false, "回卧室"},
		{22 * 60, "light/turn_off", "light.ceiling_lights", false, "关客厅灯"},
		{22*60 + 30, "lock/lock", "lock.front_door", false, "睡前锁门"},
		{23 * 60, "light/turn_off", "light.bed_light", false, "睡了"},
	}
}

// QuietDay 对照日: 人一直在家, 一天全是琐事, **一件都不该说**.
//
// ── 为什么必须有这一天 ──
//
// S49 给了主动进程"家里有没有人"这条事实, 那一天它正好说中了那件★.
// 但**一个只在该说时说的系统, 必须两边都测**: 光证明"该说的说了"不够 ——
// 一个见门就喊的系统同样能通过那一半, 而它会在第一周就被关掉通知.
//
// 关键是里面那次**开门**: 跟那件★用同一个实体、同一个动作,
// 唯一的差别是人在家. 没有它, 这一天只能证明"没事发生时它不喊",
// 证明不了"人在家时它不喊" —— 而后者才是新加的那条上下文要担的责任.
func QuietDay() []Action {
	return []Action{
		{8 * 60, "light/turn_on", "light.bed_light", false, "起床"},
		{8*60 + 5, "light/turn_on", "light.ceiling_lights", false, "客厅灯"},
		{8*60 + 20, "cover/open_cover", "cover.living_room_window", false, "拉窗帘"},
		{9 * 60, "media_player/turn_on", "media_player.living_room", false, "放音乐"},
		{10 * 60, "vacuum/start", "vacuum.1_first_floor", false, "扫地"},
		{10*60 + 40, "vacuum/return_to_base", "vacuum.1_first_floor", false, "扫完了"},
		{11 * 60, "fan/turn_on", "fan.living_room_fan", false, "开风扇"},
		// **人在家的时候开门** —— 跟那件★同一个实体、同一个动作.
		// 快递、家人、自己出去扔个垃圾, 一天好几次
		// **五分钟, 不是两分钟**: 拿个快递、进门放东西、送人出门都不止
		// 两分钟 —— 而且这决定了整套验收最快能跑多快(闸要求压缩之后
		// 每一对动作 ≥ 设备转一圈的 3 秒). 两分钟把验收锁死在一小时以上,
		// 而**一套跑一次要两小时的验收, 实际上没人会跑**
		{12 * 60, "lock/unlock", "lock.front_door", false, "人在家, 开门拿快递"},
		{12*60 + 5, "lock/lock", "lock.front_door", false, "关上"},
		{13 * 60, "fan/turn_off", "fan.living_room_fan", false, "关风扇"},
		{14 * 60, "media_player/turn_off", "media_player.living_room", false, "关音乐"},
		{15 * 60, "light/turn_off", "light.bed_light", false, "卧室灯"},
		{16 * 60, "media_player/turn_on", "media_player.lounge_room", false, "看会儿电视"},
		{18 * 60, "lock/unlock", "lock.front_door", false, "又开一次门"},
		{18*60 + 5, "lock/lock", "lock.front_door", false, "关上"},
		{19 * 60, "cover/close_cover", "cover.living_room_window", false, "关窗帘"},
		{21 * 60, "media_player/turn_off", "media_player.lounge_room", false, "关电视"},
		{22 * 60, "light/turn_off", "light.ceiling_lights", false, "关客厅灯"},
		{22*60 + 30, "lock/lock", "lock.front_door", false, "睡前确认锁门"},
		{23 * 60, "light/turn_on", "light.bed_light", false, "卧室灯"},
		{23*60 + 30, "light/turn_off", "light.bed_light", false, "睡了"},
	}
}

// TooFast 一处"压缩之后短得看不见"的地方
type TooFast struct {
	Entity string
	From   string  // 哪一步
	To     string  // 到哪一步
	GapSec float64 // 压缩之后这两步之间只剩几秒
	// NeedSec 至少要几秒才够, 和它是被哪一条卡住的.
	//
	// **两条的办法不一样**: 轮询不够就把 POLL_SEC 调小,
	// 设备不够就只能把 DAY_SECONDS 调大(压得轻一点) —— 锁转一圈的
	// 那两秒是物理的, 调采集器没有用. 不说清楚的话人会去调错的旋钮.
	NeedSec float64
	Because string // "轮询" 或 "设备"
}

// settleSeconds 这一类设备走完一轮状态机要多久.
//
// ── 真机量出来的, 不是猜的 ──
//
// HA 的 demo 锁: locked → unlocking → unlocked → locking → locked,
// 一圈 2 秒. 而压缩后的"开门两分钟"只有 1.7 秒 —— 那扇门**在物理上
// 根本没来得及开**: HA 自己的历史里 unlocked 只存在了 0 秒
// (unlocked 和 locking 在同一秒).
//
// S50 那道闸只比了轮询周期, 于是把这一天放行了, 跑出来一份
// **看起来像"系统很安静"的空数据** —— 而那正是它要防的事.
//
// **只给有中间态的那几类**: 灯就是灯, 开关一下没有中间态.
// 一个把所有东西都按最慢的设备卡的闸, 会让剧本没法写.
// 这几类跟 isTransitionalState 那一组是对应的 —— 有过渡态的才需要时间.
var settleSeconds = map[string]float64{
	"lock":                3, // 需要约 2 秒, 留一点余量
	"cover":               3, // 窗帘/车库门要走一段
	"alarm_control_panel": 3, // arming/disarming
	"vacuum":              3, // 出坞/回坞
}

func settleFor(entity string) float64 {
	domain, _, _ := strings.Cut(entity, ".")
	return settleSeconds[domain]
}

// TooFastForPolling 找出压缩之后短得看不见的那些间隔.
//
// 两条都要过: 比轮询周期长(采集器才采得到), 而且比**设备自己走完
// 一轮状态机**长(那件事在物理上才来得及发生).
//
// ── 尺子要知道自己什么时候是坏的 ──
//
// 对照日里"开门拿快递"是 12:00 开、12:02 关 —— 两分钟.
// 一天压成 20 分钟(72:1)之后是 **1.7 秒**, 而采集器 5 秒才拉一次快照,
// 于是那扇门**从来没被看见过开着** —— 账本里一条 lock 信号都没有,
// 而那次开门正是这一天存在的全部意义(它要证明"人在家时它不喊").
//
// 比"一定看不见"更糟的是它**时好时坏**: 轮询正好落在那 1.7 秒里就看得见,
// 落在外面就看不见. 同一份剧本跑两次结果不一样, 而人会去找一个不存在的
// bug —— 我就找了.
//
// 只看**同一个实体**相邻两步的间隔: 不同实体之间的间隔跟"看不看得见"
// 无关, 那是一个快照里的两条不同记录.
func TooFastForPolling(day []Action, daySeconds, pollSeconds int) []TooFast {
	if daySeconds <= 0 || pollSeconds <= 0 {
		return nil
	}
	scale := float64(daySeconds) / (24 * 60 * 60)
	last := map[string]Action{}
	var out []TooFast
	for _, a := range day {
		if prev, ok := last[a.Entity]; ok {
			gap := float64(a.AtMin-prev.AtMin) * 60 * scale
			need, because := float64(pollSeconds), "轮询"
			if st := settleFor(a.Entity); st > need {
				need, because = st, "设备"
			}
			if gap < need {
				out = append(out, TooFast{Entity: a.Entity,
					From: prev.Note, To: a.Note, GapSec: gap,
					NeedSec: need, Because: because})
			}
		}
		last[a.Entity] = a
	}
	return out
}

// ExpectFor 一天该长什么样 —— **从剧本自己推出来**.
//
// ── 为什么不能在验收那边再写一遍 ──
//
// 原来 --judge-day 里写死了"该开口 1 次""账本里必须有 lock.opened",
// 而剧本自己就标着 ★. 两处必然漂移: 剧本里多加一件该说的, 验收还按
// 老数字判, 而它会**"通过"** —— 报告说通过, 而它根本没在验你以为的
// 那件事. 那是一种静默的错.
//
// MustHave 也从剧本推: 剧本里动过的域, 就是账本里必须出现的东西.
// 没有它的话, 尺子坏掉的那一天(事件被时间压缩弄没了)照样会"通过" ——
// 这会让验收在事件已经丢失时仍然"通过".
type DayExpect struct {
	Name string
	// MustHave 这几种信号必须真的进过账本, 否则这一天不作数
	MustHave []string
	// Notices 它该开口几次 = 剧本里标了 ★ 的件数
	Notices int
}

func ExpectFor(name string, day []Action) DayExpect {
	e := DayExpect{Name: name}
	for _, a := range day {
		if a.Worthy {
			e.Notices++
		}
	}
	// 锁是这两天唯一的"该说/不该说"分水岭 —— 两天都动它,
	// 差别只在人在不在家. 它没进账本的话, 这一天什么都证明不了
	for _, a := range day {
		if strings.HasPrefix(a.Entity, "lock.") && a.Service == "lock/unlock" {
			e.MustHave = append(e.MustHave, "lock.opened")
			break
		}
	}
	return e
}

// EntitiesNeeded 这几天要用到的实体, 去重、有序.
//
// ── 为什么要点这个名 ──
//
// S48 那会儿的 HA 只有 12 个实体(没开 demo 集成), 一把锁、一盏灯都没有.
// 拿那台 HA 跑对照日: 一个动作都生效不了, 账本空空, 而"开口 0 次"
// **跟真正的成功一模一样**; 而有事那天会以"开口 0 次, 期望 1 次"失败,
// 人会去查模型的判断力, 而根本原因是那扇门压根不存在.
//
// 开跑前点一遍名, 比跑完两小时再回头查便宜得多.
func EntitiesNeeded(days ...[]Action) []string {
	seen := map[string]bool{}
	var out []string
	for _, day := range days {
		for _, a := range day {
			if !seen[a.Entity] {
				seen[a.Entity] = true
				out = append(out, a.Entity)
			}
		}
	}
	sort.Strings(out)
	return out
}

// MissingEntities 这台 HA 上缺哪些. have 是 /api/states 里现有的实体.
//
// **缺哪个要点名**: 只说"实体不全"的话, 人不知道是该开 demo 集成
// 还是该改剧本.
func MissingEntities(have map[string]bool, days ...[]Action) []string {
	var out []string
	for _, e := range EntitiesNeeded(days...) {
		if !have[e] {
			out = append(out, e)
		}
	}
	return out
}

// Day 一天: 一串动作 **加上它的前提**.
//
// ── 前提为什么必须是剧本的一部分 ──
//
// 两天的全部差别就是"人在不在家", 而它原来活在 accept.sh 里的一条
// curl 上. 那条 curl 要是没成功(端口错了、OS 还没起来、token 不对):
//
//	对照日     照样"通过" —— 它本来就该开口 0 次
//	有事那天   失败, 报"开口 0 次, 期望 1 次"
//
// **同一个原因, 两种表现**, 而判决说不出真正的原因 —— 人会去查模型的
// 判断力, 而不是先检查前提是否生效(例如"这儿是家"是否漏掉).
// PhoneEvent 手机在这一天的哪一刻报了什么
type PhoneEvent struct {
	AtMin int
	Kind  string // place.left / place.arrived
}

type Day struct {
	Name    string
	Actions []Action
	// Phone 这一天里手机投的位置信号 —— **它就是这一天的一部分**.
	//
	// 原来这儿是一个常量(Presence): 一天一个"人在/不在家". 而整条链
	// 自跑时它露馅了 —— 有事那天开口 **2 次**, 多的那次是
	// "前门打开后客厅灯又亮了, 而手机显示你不在家, 很可能有人进屋了".
	// **它是对的**: 剧本里 18:00 回家开门、18:02 开灯, 而手机从来没报过
	// "到家", 于是那之后每一件事在系统眼里都是"没人在家却有动静".
	//
	// 人在不在家**是一天里会变的**: 早上出门、晚上回来. 手机的位置信号
	// 跟灯和门一样, 就该躺在剧本里, 而不是被脚本在开头特殊照顾一下.
	Phone []PhoneEvent
	// Which 给驱动器用的名字
	Which string
}

func Household() Day {
	return Day{Name: "有事发生的一天", Actions: HouseholdDay(), Which: "household",
		Phone: []PhoneEvent{
			// 8:36 出门(锁门之后一分钟), 18:00 回家 —— **跟剧本里的
			// 故事对齐**: 那件★在 15:30, 正落在"不在家"那一段里
			{8*60 + 36, "place.left"},
			{18 * 60, "place.arrived"},
		}}
}

func Quiet() Day {
	return Day{Name: "全是琐事的一天(人在家)", Actions: QuietDay(), Which: "quiet",
		// **全程在家** —— 一条"离开家"都不能有, 否则它就不是对照日了
		Phone: []PhoneEvent{{7 * 60, "place.arrived"}}}
}

// ExpectForDay 从一天(含前提)推出该长什么样
func ExpectForDay(d Day) DayExpect {
	e := ExpectFor(d.Name, d.Actions)
	// 手机那几条也要进账本 —— 少一条, 那之后的判断就建立在错的前提上,
	// 而症状是"它多说了一次"(而它其实是对的)
	seen := map[string]bool{}
	for _, p := range d.Phone {
		if !seen[p.Kind] {
			seen[p.Kind] = true
			e.MustHave = append(e.MustHave, p.Kind)
		}
	}
	return e
}

// FastestDaySeconds 这几天最快能压成几秒 —— **算出来的, 不是猜的**.
//
// ── 为什么不能在脚本里写死 ──
//
// 能压多快是**剧本和设备一起决定的**(见 TooFastForPolling: 每一对动作
// 压缩之后要 ≥ 设备转一圈, 也 ≥ 轮询周期). 剧本一改这个数就变了 ——
// S65 把开门从 2 分钟改成 5 分钟, 最快压缩就从 12:1 变成了 30:1,
// 而脚本里写死的那几处不会跟着变.
//
// 漂移的后果不是报错: 压太狠时闸会拒(还好), 压太松只是白等 ——
// 而**一套跑一次要两小时的验收, 实际上没人会跑**(S65).
//
// 算法: 找出所有"同一实体相邻两步"里最紧的那一对, 让它压缩之后正好
// 够用; 再留 20% 余量(设备偶尔慢一拍, 而卡在边界上的验收会时好时坏).
func FastestDaySeconds(a, b Day, pollSeconds int) int {
	const dayMinutes = 24 * 60
	tightest := math.Inf(1) // 最紧的那一对隔了几分钟
	need := float64(pollSeconds)
	for _, d := range []Day{a, b} {
		last := map[string]Action{}
		for _, act := range d.Actions {
			if prev, ok := last[act.Entity]; ok {
				gapMin := float64(act.AtMin - prev.AtMin)
				// 每一对按**它自己的**要求算: 有中间态的设备要 3 秒
				req := float64(pollSeconds)
				if st := settleFor(act.Entity); st > req {
					req = st
				}
				// 这一对要满足的话, 一天最少几秒: gapMin*60*scale >= req
				if gapMin > 0 {
					if s := gapMin / req; s < tightest {
						tightest, need = s, req
					}
				}
			}
			last[act.Entity] = act
		}
	}
	if math.IsInf(tightest, 1) {
		return 0
	}
	// tightest = gapMin/req 分钟每秒 → 一天最少 dayMinutes/tightest 秒
	sec := dayMinutes / tightest
	_ = need
	// **留 20% 余量**: 设备偶尔慢一拍, 而卡在边界上的验收会时好时坏 ——
	// 那正是 S50 那次"同一份剧本跑两次结果不一样"的来源
	return int(sec*1.2) + 1
}

// CompressionWarnings 这个压缩比会把什么弄失真 —— **一条一条说出来**.
//
// ── 为什么必须说 ──
//
// 压缩是这套验收能跑起来的前提(一天两小时的验收没人会跑), 但它有代价,
// 而代价不写出来的话, 后来看结果的人会把失真当成事实.
//
// 两类失真尤其需要显式报告:
//
//	两分钟的开门压成 1.7 秒, 比轮询还短 —— 那扇门从来没被看见过开着
//	主动进程说"手机显示你不在家(**4 分钟前离开**)", 而剧本里
//	     那是 7 小时前
//
// 后一条**修不掉**: 信号的时间戳必须是真实时钟, 否则总线会判它迟到
// (那是水位线的语义). 所以只能说出来.
//
// 而它不是小数点问题: "你走了 4 分钟门开了"多半是你回来拿钥匙,
// "你走了 7 小时门开了"才是可疑的 —— 喂给模型的是一个跟剧本
// **实质不同**的处境.
//
// **一个不说自己哪儿失真的尺子, 比不准更危险.**
func CompressionWarnings(daySeconds int) []string {
	const realDay = 24 * 60 * 60
	if daySeconds <= 0 || daySeconds >= realDay {
		return nil // 一天就是一天, 一点都不失真
	}
	ratio := float64(realDay) / float64(daySeconds)
	return []string{
		fmt.Sprintf("窗口: 压缩之后一个 60 秒的窗口相当于真实世界的 %.0f 分钟 —— "+
			"窗口也要按同一比例缩, 不然一个窗口里会挤进真实情况下不会同时发生的事",
			60*ratio/60),
		fmt.Sprintf("**\"多久前\"会失真**: 信号的时间戳是真实时钟(必须如此, "+
			"否则总线会判它迟到), 于是剧本里的\"7 小时前离开\"到了模型眼里是"+
			"\"%.0f 分钟前\" —— 而\"走了几分钟\"和\"走了几小时\"是实质不同的处境",
			7*60/ratio),
	}
}
