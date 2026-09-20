package sense

import (
	"strings"
	"testing"
)

// **安静率量不出来, 因为一个没人的房子分母是 0.**
//
// S47 确认了: 噪音清干净之后, 一个没人活动的家一小时接近 0 条信号.
// 那是设计要的("沉默是结构性免费的"), 但也意味着**整个感知层唯一的
// 验收判据从来没被量过** —— 没有活动就没有窗口, 没有窗口就没有安静率.
//
// 所以要有一天真实的住宅活动. 而它必须**驱动真 HA 的真实体**,
// 不能往采集入口灌合成信号 —— 灌信号会绕过采集器的全部判据
// (钟、上线风暴、数值阈值、语义 kind), 量出来的是"我编的信号有多吵",
// 不是"这套过滤有多好".
func TestHouseholdDayDrivesRealEntities(t *testing.T) {
	day := HouseholdDay()
	if len(day) < 15 {
		t.Fatalf("一天只有 %d 个动作 —— 太少了, 量不出安静率", len(day))
	}
	for _, a := range day {
		if a.Service == "" || a.Entity == "" {
			t.Fatalf("%+v 不是一次真的 HA 服务调用", a)
		}
		if !strings.Contains(a.Entity, ".") {
			t.Fatalf("%q 不像 HA 的 entity_id", a.Entity)
		}
	}
}

// 时间必须单调 —— 一天是有先后的, 乱序的剧本量出来的窗口分布是假的
func TestHouseholdDayIsMonotonic(t *testing.T) {
	prev := -1
	for _, a := range HouseholdDay() {
		if a.AtMin < prev {
			t.Fatalf("剧本时间倒流: %d 分 在 %d 分之后", a.AtMin, prev)
		}
		prev = a.AtMin
	}
}

// **一天要有早中晚**, 否则"大部分时候安静"这句话没有意义 ——
// 全挤在一小时里的话, 安静率量的是那一小时
func TestHouseholdDayCoversWholeDay(t *testing.T) {
	var morning, day, evening int
	for _, a := range HouseholdDay() {
		switch {
		case a.AtMin < 12*60:
			morning++
		case a.AtMin < 18*60:
			day++
		default:
			evening++
		}
	}
	if morning == 0 || day == 0 || evening == 0 {
		t.Fatalf("早/中/晚 的动作数是 %d/%d/%d —— 有一段是空的", morning, day, evening)
	}
}

// **里面要有真正值得打扰的那几件**, 也要有大量不值得的.
//
// 全是琐事的话, "它一次都没开口"证明不了判断力;
// 全是大事的话, 那不是一天真实的生活.
func TestHouseholdDayMixesWorthyAndTrivial(t *testing.T) {
	worthy := 0
	for _, a := range HouseholdDay() {
		if a.Worthy {
			worthy++
		}
	}
	if worthy == 0 {
		t.Fatal("一天里没有一件值得说的 —— 那'它没开口'证明不了任何事")
	}
	if worthy > len(HouseholdDay())/3 {
		t.Fatalf("%d/%d 件都值得说 —— 那不是一天真实的生活",
			worthy, len(HouseholdDay()))
	}
}

// **对照日: 一天全是琐事, 人一直在家, 它一次都不该开口.**
//
// S49 给了它"家里有没有人"这条事实, 那一天它正好说中了那件★.
// 但**一个只在该说时说的系统, 必须两边都测**: 光证明"该说的说了"
// 不够 —— 一个见门就喊的系统同样能通过那一半, 而它会在第一周
// 就被关掉通知.
//
// 所以对照日里要有一次**开门**, 用跟那件★同一个实体、同一个动作,
// 唯一的差别是"人在家". 不然这个对照日证明不了任何东西.
func TestQuietDayHasNothingWorthSaying(t *testing.T) {
	for _, a := range QuietDay() {
		if a.Worthy {
			t.Fatalf("对照日里混进了一件该说的: %+v —— 那它就不是对照日了", a)
		}
	}
}

// 对照日必须包含那次开门 —— 否则它只是"一天没事发生"
func TestQuietDayStillOpensTheDoor(t *testing.T) {
	var opened bool
	for _, a := range QuietDay() {
		if a.Service == "lock/unlock" {
			opened = true
		}
	}
	if !opened {
		t.Fatal("对照日里没有开门 —— 那证明不了'人在家时它不喊', " +
			"只证明了'没事发生时它不喊'")
	}
}

// 两天要用同一批实体 —— 不然差异可能来自实体不同, 而不是来自上下文
func TestBothDaysShareEntities(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range HouseholdDay() {
		seen[a.Entity] = true
	}
	for _, a := range QuietDay() {
		if !seen[a.Entity] {
			t.Fatalf("对照日用了 %q, 而那一天没有 —— "+
				"两天的差别就不只是'有没有人在家'了", a.Entity)
		}
	}
}

// **时间压缩会让事件本身消失.**
//
// 对照日里"开门拿快递"是 12:00 开、12:02 关 —— 两分钟.
// 压缩 72:1 之后是 **1.7 秒**, 而采集器 5 秒才拉一次快照,
// 于是那扇门**从来没被看见过开着**. 账本里一条 lock 信号都没有.
//
// 比"一定看不见"更糟的是它**时好时坏**: 轮询正好落在那 1.7 秒里就看得见,
// 落在外面就看不见 —— 同一份剧本跑两次结果不一样, 而人会去找一个
// 不存在的 bug(我就找了).
//
// 所以尺子必须知道自己什么时候是坏的.
func TestScenarioWarnsWhenCompressionHidesEvents(t *testing.T) {
	bad := TooFastForPolling(QuietDay(), 1200, 5) // 一天压成 20 分钟, 5 秒一拉
	if len(bad) == 0 {
		t.Fatal("两分钟的开门压成 1.7 秒, 比 5 秒的轮询还短, " +
			"却没被报出来 —— 那扇门从来没被看见过开着")
	}
	if bad[0].Entity == "" || bad[0].GapSec <= 0 {
		t.Fatalf("报得不清楚: %+v", bad[0])
	}
}

// 压得不那么狠的时候不该乱报 —— 一个只会喊狼来了的检查等于没有
func TestNoWarningWhenCompressionIsGentle(t *testing.T) {
	if bad := TooFastForPolling(QuietDay(), 8*3600, 5); len(bad) != 0 {
		t.Fatalf("压得很轻却报了 %d 处: %+v", len(bad), bad[0])
	}
}

// **闸只比了轮询周期, 没比设备自己走完一轮要多久.**
//
// HA 的 demo 锁转一圈是
// locked → unlocking → unlocked → locking → locked, 要 2 秒左右.
// 而压缩后的"开门两分钟"只有 1.7 秒 —— 那扇门**在物理上根本没来得及开**:
// HA 自己的历史里 unlocked 只存在了 0 秒(unlocked 和 locking 同一秒).
//
// S50 那道闸把这一天放行了(1.7 秒 > 1 秒的轮询), 于是跑出来一份
// **看起来像"系统很安静"的空数据** —— 而那正是它要防的事.
func TestRulerAlsoCountsDeviceSettleTime(t *testing.T) {
	// 一天压成 10 分钟, 1 秒一拉 —— 只看轮询的话这是放行的
	// (五分钟的开门压完剩 2.1 秒 > 1 秒的轮询, 但 < 锁转一圈的 3 秒)
	bad := TooFastForPolling(QuietDay(), 600, 1)
	if len(bad) == 0 {
		t.Fatal("锁转一圈要 2 秒, 而压缩后只剩 1.7 秒, 闸却放行了 —— " +
			"跑出来的是一份看起来像'很安静'的空数据")
	}
	if bad[0].Entity != "lock.front_door" {
		t.Fatalf("报的是 %q, 该是那把锁", bad[0].Entity)
	}
}

// **瞬时的设备不该被这条卡住** —— 灯就是灯, 开关一下没有中间态.
// 一个把所有东西都按最慢的设备卡的闸, 会让剧本没法写
func TestInstantDevicesAreNotHeldToSettleTime(t *testing.T) {
	quick := []Action{
		{0, "light/turn_on", "light.bed_light", false, "开灯"},
		{2, "light/turn_off", "light.bed_light", false, "关灯"}, // 压缩后 1.7 秒
	}
	if bad := TooFastForPolling(quick, 1200, 1); len(bad) != 0 {
		t.Fatalf("灯被按锁的标准卡住了: %+v —— 它没有中间态", bad[0])
	}
}

// **报错要说清是被哪一条卡住的** —— 两条的办法不一样:
// 轮询不够就把 POLL_SEC 调小, 设备不够只能把 DAY_SECONDS 调大.
// 锁转一圈的那两秒是物理的, 调采集器没有用
func TestRulerSaysWhichLimitBit(t *testing.T) {
	bad := TooFastForPolling(QuietDay(), 600, 1)
	if len(bad) == 0 {
		t.Fatal("该报的没报")
	}
	if bad[0].Because != "设备" {
		t.Fatalf("说是卡在%q —— 1 秒轮询下卡住那把锁的是它自己的 2 秒", bad[0].Because)
	}
	if bad[0].NeedSec <= bad[0].GapSec {
		t.Fatalf("没说清要多少秒: 只剩 %.1f, 要 %.1f", bad[0].GapSec, bad[0].NeedSec)
	}
}

// **剧本里那个 ★ 我标错了一件.**
//
// "出门锁门"原来标着 Worthy —— 而 S48 的结论里我自己写的是
// "第一件不说是对的: 出门锁门是他自己干的". **两处互相矛盾**,
// 而矛盾的那一边正是验收要拿来当期望值的.
//
// 根子在 Worthy 的定义太松("值不值得打扰"). 收紧成:
// **他自己刚做的事不算** —— 他知道自己锁了门, 告诉他等于复读.
func TestWorthyMeansHeDoesNotAlreadyKnow(t *testing.T) {
	var worthy []string
	for _, a := range HouseholdDay() {
		if a.Worthy {
			worthy = append(worthy, a.Note)
		}
	}
	if len(worthy) != 1 {
		t.Fatalf("标了 %d 件该说的: %v —— 一天里只有'白天没人在家时门开了'"+
			"是他不知道的; 出门锁门是他自己干的", len(worthy), worthy)
	}
}

// **验收的期望值要从剧本推出来, 不能在别处再写一遍.**
//
// 原来 --judge-day 里写死了 MustHave/Notices, 而剧本自己就标着 ★ ——
// 两处必然漂移: 剧本里多加一件该说的, 验收还按老数字判, 而它会"通过".
// 那是一种静默的错: 报告说通过, 而它根本没在验你以为的那件事.
func TestExpectComesFromTheScenario(t *testing.T) {
	ev := ExpectFor("household", HouseholdDay())
	if ev.Notices != 1 {
		t.Fatalf("有事发生的那天期望开口 %d 次, 剧本里标着 1 件", ev.Notices)
	}
	q := ExpectFor("quiet", QuietDay())
	if q.Notices != 0 {
		t.Fatalf("对照日期望开口 %d 次, 剧本里一件都没标", q.Notices)
	}
	// **必须要求那件事真的进过账本** —— 否则尺子坏掉的一天也会"通过"
	found := false
	for _, k := range q.MustHave {
		if k == "lock.opened" {
			found = true
		}
	}
	if !found {
		t.Fatalf("对照日没要求 lock.opened 进账本: %v —— "+
			"那两次开门被压没了的话, 这一天照样会'通过'", q.MustHave)
	}
}

// 剧本改了, 期望值要跟着改 —— 这才是"从剧本推出来"的意义
func TestExpectFollowsScenarioChanges(t *testing.T) {
	day := append(HouseholdDay(), Action{23 * 60, "lock/unlock",
		"lock.front_door", true, "半夜门开了"})
	if ev := ExpectFor("household", day); ev.Notices != 2 {
		t.Fatalf("剧本里多标了一件, 期望值还是 %d —— 两处漂移了", ev.Notices)
	}
}

// **剧本用的实体在这台 HA 上不存在, 而跑完看起来像成功.**
//
// S48 那会儿的 HA 只有 12 个实体(没开 demo 集成), 一把锁、一盏灯都没有.
// 拿那台 HA 跑对照日的话: 一个动作都生效不了, 账本空空, 而"开口 0 次"
// **跟真正的成功一模一样** —— 而有事那天会以"开口 0 次, 期望 1 次"
// 失败, 人会去查模型的判断力, 而根本原因是那扇门压根不存在.
//
// 所以开跑前先点一遍名: 剧本要用的实体, 这台 HA 上有没有.
func TestEntitiesNeededCoversBothDays(t *testing.T) {
	need := EntitiesNeeded(HouseholdDay(), QuietDay())
	if len(need) < 8 {
		t.Fatalf("只点出 %d 个实体 —— 两天加起来用到的远不止", len(need))
	}
	var hasLock, hasLight bool
	for _, e := range need {
		if strings.HasPrefix(e, "lock.") {
			hasLock = true
		}
		if strings.HasPrefix(e, "light.") {
			hasLight = true
		}
	}
	if !hasLock || !hasLight {
		t.Fatalf("锁/灯没被点到: %v —— 而那扇门是整个验收的分水岭", need)
	}
	// 两天共用的实体只该出现一次 —— 报缺失时列一堆重复的没人看
	seen := map[string]bool{}
	for _, e := range need {
		if seen[e] {
			t.Fatalf("%q 出现了两次", e)
		}
		seen[e] = true
	}
}

// **缺哪个要点名**, 而且要说清后果 —— 只说"实体不全"的话,
// 人不知道是该开 demo 集成还是该改剧本
func TestMissingEntitiesAreNamed(t *testing.T) {
	have := map[string]bool{"light.bed_light": true}
	miss := MissingEntities(have, HouseholdDay())
	if len(miss) == 0 {
		t.Fatal("这台 HA 上只有一盏灯, 却没报缺")
	}
	found := false
	for _, m := range miss {
		if m == "lock.front_door" {
			found = true
		}
	}
	if !found {
		t.Fatalf("没点出缺了那把锁: %v", miss)
	}
	// 有的那个不该出现在缺失名单里
	for _, m := range miss {
		if m == "light.bed_light" {
			t.Fatalf("把已有的实体也报成缺了: %v", miss)
		}
	}
}

// **剧本的节奏决定了这套验收最快能跑多快.**
//
// 闸要求每一对动作压缩之后 ≥ 设备转一圈的时间(锁是 3 秒). 而剧本里
// "开门 2 分钟"压到 48:1 只剩 2.5 秒 —— 于是整套验收被锁死在
// 一小时以上, 而**一套跑一次要两小时的验收, 实际上没人会跑**.
//
// 两分钟本来也偏短: 拿个快递、进门放东西、送人出门, 都不止两分钟.
// 改成五分钟既更像真的, 也让压缩比能到 48:1(整套约一小时).
func TestScenarioSurvivesFasterCompression(t *testing.T) {
	// 一天压成 30 分钟(48:1), 1 秒一拉
	for _, day := range [][]Action{HouseholdDay(), QuietDay()} {
		if bad := TooFastForPolling(day, 1800, 1); len(bad) != 0 {
			t.Fatalf("48:1 下闸就拒了: %s %s → %s 只剩 %.1f 秒(要 %.0f) —— "+
				"整套验收被锁死在一小时以上, 而那种验收实际上没人会跑",
				bad[0].Entity, bad[0].From, bad[0].To, bad[0].GapSec, bad[0].NeedSec)
		}
	}
}

// 但**不能为了跑得快就把节奏改到失真** —— 压到 288:1 该照样被拒,
// 那时候连五分钟的开门都只剩 1 秒, 锁转一圈都不够
func TestScenarioStillRefusesAbsurdCompression(t *testing.T) {
	// 一天压成 5 分钟(288:1) —— 五分钟的开门只剩 1 秒, 锁转一圈都不够
	if bad := TooFastForPolling(QuietDay(), 300, 1); len(bad) == 0 {
		t.Fatal("288:1 压缩下闸放行了 —— 那已经快到设备来不及动了")
	}
}

// **"这一天人在不在家"是剧本的一部分, 不是脚本里的一个 curl.**
//
// 两天的全部差别就是这个前提. 如果只靠 accept.sh 里的一条 curl 设置,
// 而那条 curl 没成功(端口错了、OS 还没起来、token 不对):
//
//	对照日     照样"通过" —— 它本来就该开口 0 次
//	有事那天   失败, 报"开口 0 次, 期望 1 次"
//
// **同一个原因, 两种表现**, 而判决说不出真正的原因 —— 排查会转向
// 模型的判断力, 而不是缺失的在家状态.
//
// 所以前提要跟着剧本走, 并且进 MustHave: 那一天的前提没进账本,
// 这一天就不作数.
func TestDayCarriesItsPresencePremise(t *testing.T) {
	// 前提从"一天一个常量"变成了"一天里会变的一串"(见 Day.Phone),
	// 但要钉的性质没变: **两天的前提必须不同, 否则它们不是对照**
	firstOf := func(d Day) string {
		if len(d.Phone) == 0 {
			return ""
		}
		return d.Phone[0].Kind
	}
	if firstOf(Household()) != "place.left" {
		t.Fatalf("有事那天开头是 %q —— 该是'人离开了家'", firstOf(Household()))
	}
	if firstOf(Quiet()) != "place.arrived" {
		t.Fatalf("对照日开头是 %q —— 该是'人在家'", firstOf(Quiet()))
	}
}

// **前提没进账本, 这一天不作数** —— 跟"门没开过"同一类
func TestExpectRequiresThePremiseInTheLedger(t *testing.T) {
	e := ExpectForDay(Household())
	var found bool
	for _, k := range e.MustHave {
		if k == "place.left" {
			found = true
		}
	}
	if !found {
		t.Fatalf("有事那天没要求'离开家'进账本: %v —— "+
			"那条信号没投进去的话, 这一天会以'开口 0 次'失败, "+
			"而人会去查模型的判断力", e.MustHave)
	}
	// 门那条也不能丢
	var hasLock bool
	for _, k := range e.MustHave {
		if k == "lock.opened" {
			hasLock = true
		}
	}
	if !hasLock {
		t.Fatalf("门那条丢了: %v", e.MustHave)
	}
}

// **剧本自相矛盾: 故事里人回家了, 而前提里人一直不在家.**
//
// 整条链自跑抓到的: 有事那天它开口了 **2 次**, 而期望是 1 次 ——
// 多的那次是
//
//	"前门打开后, 家里客厅灯现在又亮了, 而手机显示你不在家。
//	 很可能有人进屋了, 建议立刻查监控。"
//
// **它是对的.** 剧本里 18:00 "回家开门"、18:02 "开灯", 而手机从来
// 没报过"到家" —— 于是那之后的每一件事在系统眼里都是"没人在家却有动静".
//
// 根子在把前提当成了**一天一个常量**(S69 那一步只做到这儿).
// 而人在不在家**是一天里会变的**: 早上出门、晚上回来 ——
// 手机的位置信号跟灯和门一样, 就是这一天的一部分.
func TestPhoneEventsArePartOfTheDay(t *testing.T) {
	d := Household()
	if len(d.Phone) < 2 {
		t.Fatalf("有事那天只有 %d 条手机信号 —— 人出了门总要回来的, "+
			"不然回家之后的每件事都会被当成'没人在家却有动静'", len(d.Phone))
	}
	var left, arrived bool
	for _, p := range d.Phone {
		if p.Kind == "place.left" {
			left = true
		}
		if p.Kind == "place.arrived" {
			arrived = true
		}
	}
	if !left || !arrived {
		t.Fatal("出门和回家要成对 —— 只有出门的话, 晚上的活动全是异常")
	}
}

// **那件★必须落在"人不在家"的那一段里** —— 不然它证明不了任何事
func TestStarFallsWhileNobodyHome(t *testing.T) {
	d := Household()
	var star int = -1
	for _, a := range d.Actions {
		if a.Worthy {
			star = a.AtMin
		}
	}
	if star < 0 {
		t.Fatal("剧本里没有★")
	}
	// 找★之前最后一条手机信号
	last := ""
	for _, p := range d.Phone {
		if p.AtMin <= star {
			last = p.Kind
		}
	}
	if last != "place.left" {
		t.Fatalf("★发生时最近一条手机信号是 %q —— 该是'已经离开家'", last)
	}
}

// 对照日全程在家 —— 出门那条一条都不能有, 否则它就不是对照日了
func TestQuietDayStaysHome(t *testing.T) {
	for _, p := range Quiet().Phone {
		if p.Kind == "place.left" {
			t.Fatalf("对照日里有'离开家'(%d 分) —— 那它就不是'人在家'那一天了", p.AtMin)
		}
	}
}

// **"最快能压到多少"该由闸算出来, 不是我在脚本里猜.**
//
// 现在这个数散在三处而且互相不一致:
//
//	注释说 "FAST=1 → 24:1, 约一小时"
//	代码写 DAY_SECONDS=3600(24:1), 而改成 1800 就会得到 48:1
//	窗口比例 DAY_SECONDS/120 也是脚本里拍的
//
// 而**能压多快是剧本和设备一起决定的**(S54: 每一对动作压缩之后要
// ≥ 设备转一圈): 剧本一改(S65 把开门从 2 分钟改成 5 分钟), 这个数
// 就变了, 而脚本里那三处不会跟着变.
//
// 漂移的后果不是报错: 压得太狠时闸会拒(还好), 而压得太松只是白等 ——
// 一套跑一次要两小时的验收, 实际上没人会跑(S65).
func TestFastestScaleIsComputed(t *testing.T) {
	// 1 秒轮询下, 两天都能跑的最快压缩
	sec := FastestDaySeconds(Household(), Quiet(), 1)
	if sec <= 0 {
		t.Fatal("算不出最快能压到多少")
	}
	// 算出来的那个必须真的过闸
	for _, d := range []Day{Household(), Quiet()} {
		if bad := TooFastForPolling(d.Actions, sec, 1); len(bad) != 0 {
			t.Fatalf("算出来的 %d 秒过不了闸: %s 只剩 %.1f 秒(要 %.0f)",
				sec, bad[0].Entity, bad[0].GapSec, bad[0].NeedSec)
		}
	}
	// **再压掉那 20% 余量就该被拒** —— 否则这个数是白算的.
	//
	// 余量是有意留的: 设备偶尔慢一拍, 而卡在边界上的验收会时好时坏 ——
	// 那正是 S50 那次"同一份剧本跑两次结果不一样"的来源. 所以判据是
	// "去掉余量之后不能再快", 不是"再快一点点就得拒"
	if bad := TooFastForPolling(Quiet().Actions, int(float64(sec)/1.25), 1); len(bad) == 0 {
		t.Fatalf("%d 秒把余量也算进去还是太松 —— 这个数是白算的", sec)
	}
}

// 轮询慢的时候, 最快的压缩也得跟着慢 —— 两条约束都要满足
func TestFastestScaleFollowsPolling(t *testing.T) {
	fast := FastestDaySeconds(Household(), Quiet(), 1)
	slow := FastestDaySeconds(Household(), Quiet(), 10)
	if slow <= fast {
		t.Fatalf("轮询从 1 秒放慢到 10 秒, 最快压缩却没变慢(%d → %d)", fast, slow)
	}
}

// **压缩让"多久前"这个量失真, 而它恰恰是判断的依据之一.**
//
// 主动进程说"前门在 15:31 被打开了, 但手机定位显示
// 你不在家(**4 分钟前离开**)". 而剧本里那是 08:36 出门、15:30 开门 ——
// **7 小时前**.
//
// 这不是小数点问题: "你走了 4 分钟门开了"多半是你回来拿钥匙,
// "你走了 7 小时门开了"才是可疑的. 压缩重放喂给模型的,
// 是一个跟剧本**实质不同**的处境.
//
// 这一条修不掉(信号的时间戳必须是真实时钟, 否则总线会判它迟到),
// 所以**要说出来** —— 跟窗口那条警告一样, 印在驱动器开跑的时候.
// 一个不说自己哪儿失真的尺子, 比不准更危险.
func TestCompressionWarningsMentionRelativeTime(t *testing.T) {
	ws := CompressionWarnings(1037)
	if len(ws) < 2 {
		t.Fatalf("只有 %d 条警告 —— 窗口那条之外, '多久前'那条也要说", len(ws))
	}
	var mentioned bool
	for _, w := range ws {
		if strings.Contains(w, "多久前") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Fatalf("没提'多久前'会失真: %v —— 而那正是判断的依据之一", ws)
	}
}

// 不压缩(一天就是一天)的时候不该乱警告 —— 那时候一点都不失真
func TestNoWarningsWhenNotCompressed(t *testing.T) {
	if ws := CompressionWarnings(24 * 3600); len(ws) != 0 {
		t.Fatalf("一天就是一天, 却警告了 %d 条: %v", len(ws), ws)
	}
}
