package osinit

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// **"重启之后没被恢复的状态"已经出过三次, 每次都是靠踩到才发现的.**
//
//	S26  打扰额度   一天重启三次就打扰九次
//	S30  静音表     崩一次那个噪音种类又开始吵
//	S31  去重表     每重启一次账本里的信号就多一份
//
// 三次的形状完全一样: 事件老老实实落了账, **却没有任何人在重启时读它**.
// 而症状都是"看起来一切正常, 只是某件事悄悄不算数了".
//
// 这道闸把那个形状本身钉住: **每一种落账的事件, 要么有人在重启时读它,
// 要么在这里写清楚为什么不用读.** 种类是从 abi/types.go 里现读的 ——
// 新加一种而没在这儿登记, 测试当场变红, 不靠任何人记得.
var restoredBy = map[abi.EventKind]string{
	// ── 重启时要读的 ──
	abi.EvWakeSet:        "Timers.Restore",
	abi.EvWakeFired:      "Timers.Restore",
	abi.EvWakeCancelled:  "Timers.Restore",
	abi.EvPlaceNamed:     "Places.Restore",
	abi.EvPlaceForgotten: "Places.Restore —— 顺序重放, 后面这条盖掉前面的命名",
	abi.EvZoneSet: "Zone.Restore —— **必须在别的恢复之前**: 日报'今天发过没有'、" +
		"额度'今天用了几次'、停留'算在哪一天'都按本地日子算, 顺序反了" +
		"就是按 UTC 分组、按本地查",
	abi.EvDeviceAsk: "不用: **它压根不进账本** —— 它是一句吆喝不是一件事. " +
		"设备取到了会照常发一条正常的信号, 那条才是事实",
	// **加它的时候这道闸当场响了** —— 这正是它存在的理由:
	// 新事件种类要么有人装回来, 要么写清楚为什么不用
	abi.EvDeviceAct: "不用: **它压根不进账本** —— 跟 device.ask 同一条路: " +
		"这是一句吆喝, 不是一件发生过的事. 灯真亮了会有一条状态信号进来, " +
		"那条才是事实; 装回一句十分钟前的'开灯'反而吓人",
	abi.EvTaskAdded: "Agenda.Restore —— 他交代过的事丢了最让人恼火: " +
		"他不会想到要再说一遍, 他会以为它记着",
	abi.EvTaskDone:    "Agenda.Restore —— 顺序重放",
	abi.EvTaskDropped: "Agenda.Restore —— 顺序重放, 这条是真删",
	abi.EvNoteSet: "Notes.Restore —— 这一层丢了最让人恼火: 地点丢了他还能" +
		"到了那儿重说一句, 而'我老婆生日'这种事他不会想到要再说一遍",
	abi.EvNoteForgot:   "Notes.Restore —— 顺序重放, 后面这条盖掉前面的记忆",
	abi.EvWatchSet:     "Watches.Restore",
	abi.EvWatchRemoved: "Watches.Restore",
	abi.EvDailyReport:  "DailyReport.Restore",
	abi.EvStayEnded: "Routine.Restore —— 规律是攒出来的, 攒够要好几天, " +
		"而进程一周会重启好几次; 清零的话它永远看不出规律",
	abi.EvPersonKnown:     "People.Restore —— 人比设备活得久, 换手机不该变成换个人",
	abi.EvDeviceForgotten: "Devices.Restore —— 顺序重放, 后面这条盖掉前面的声明",
	abi.EvPersonForgotten: "People.Restore —— 同上",
	abi.EvRuleSet:         "Rules.Restore —— 规则活得比进程长, 跟闹钟同一条道理",
	abi.EvRuleRemoved:     "Rules.Restore",
	abi.EvRuleFired: "不用: 它是观测记录(某条规则那时候响过). " +
		"**边沿状态故意不装回来** —— 它是从当下的事实算出来的, " +
		"而重启后事实要重新拉一遍; 装回一个旧的'已经成立'会让边沿判断" +
		"错过第一次真正的变化",
	abi.EvDeviceDeclared: "Devices.Restore —— 设备声明是**现状**不是流水, " +
		"不装回来的话一台一天只报一次的设备(体重秤、电表)重启后能失踪一整天",
	abi.EvInterrupt:    "InterruptBudget.Restore (S26)",
	abi.EvMuteSet:      "SignalBus.RestoreMutes (S30)",
	abi.EvMuteRemoved:  "SignalBus.RestoreMutes (S30)",
	abi.EvSignal:       "SignalBus.RestoreSeen + RecoverPending (S31)",
	abi.EvSignalDigest: "RecoverPending 用它定位'哪些还没被总结'",

	// ── 不用读的, 各有各的理由 ──
	abi.EvSignalLate: "不用: 它是给校准报告看的观测记录, 不构成任何现状",
	// 采集端的死活**不该跨重启带回来**: 重启之后哪个采集端还在,
	// 由它们自己重新报到说了算(第一次心跳就会说). 装回一个旧的
	// "它瞎了"反而会喊一次假警报
	abi.EvCollectorDown: "不用: 采集端重启后自己重新报到, 装回旧状态会喊假警报; " +
		"这两条是给校准报告算'这段账本瞎了多久'用的",
	abi.EvCollectorUp: "不用: 同上",
	abi.EvProcState:   "不用: 进程状态本来就不跨重启 —— 上次的进程已经没了",
	abi.EvProcDelta: "不用: **它压根不进账本** —— 一次回复上百段, 而它们加起来" +
		"的信息量跟最后那条 phase:reply 一模一样. 它只给此刻正看着的人",
	abi.EvProcOutput:  "不用: 输出是给当时看的; 对话历史由 RestoreEvents 整体装回",
	abi.EvInputRecv:   "不用: 同上, 对话历史整体装回, 不逐条重建状态",
	abi.EvCorrection:  "不用: 纠正摘要在 spawn 时从账本现算, 不另建内存对象",
	abi.EvProcOutcome: "不用: 同上",
	abi.EvDelivery: "不用: 它记的是**投递意图**, 而投递是一次性的 —— " +
		"重启时把昨天的通知再推一遍, 比不推更糟(用户会以为那件事又发生了一次)。" +
		"它要被读的场合只有一个: 手机端按游标补齐它离线期间错过的那几条, " +
		"而那是订阅方自己的事, 不是 OS 重建内存状态",
	abi.EvCapUsed:     "不用: 观测记录",
	abi.EvCapDenied:   "不用: 观测记录",
	abi.EvBudgetSpent: "不用: 观测记录(token 花销), 不构成现状",
	abi.EvDecideRequest: "不用: 待决策在 S17 已经单独恢复(RestoreWindow), " +
		"不走这条路",
	abi.EvDecideResolved: "不用: 同上",
}

func TestEveryEventKindDeclaresWhoRestoresIt(t *testing.T) {
	raw, err := os.ReadFile("../abi/types.go")
	if err != nil {
		t.Fatal(err)
	}
	// 从源码里现读 —— 维护一份手写清单等于把同一个"记得改两处"的
	// 陷阱又搭一遍, 而那正是这道闸要挡的东西
	re := regexp.MustCompile(`(?m)^\s*(Ev\w+)\s+EventKind = "([^"]+)"`)
	ms := re.FindAllStringSubmatch(string(raw), -1)
	if len(ms) < 15 {
		t.Fatalf("只从 abi/types.go 里读出 %d 个事件种类 —— 正则跟源码对不上了, "+
			"这道闸已经形同虚设", len(ms))
	}
	var missing []string
	for _, m := range ms {
		if _, ok := restoredBy[abi.EventKind(m[2])]; !ok {
			missing = append(missing, m[1]+" ("+m[2]+")")
		}
	}
	if len(missing) > 0 {
		t.Fatalf("新加了事件种类却没交代重启时谁读它: %s\n"+
			"在 restoredBy 里登记一下 —— 要么写清楚谁 Restore 它, "+
			"要么写清楚为什么不用读。\n"+
			"这个问题已经踩过三次(S26 打扰额度 / S30 静音表 / S31 去重表), "+
			"每次的症状都是'看起来一切正常, 只是某件事悄悄不算数了'。",
			strings.Join(missing, ", "))
	}
}

// **一次真实的使用, 杀掉, 恢复, 逐项比对.**
//
// 上面那道闸挡的是"忘了写 Restore", 这一条挡的是"写了但没接上" ——
// 而 main 里原来有七处 for 循环散在三百多行里, 加第八个状态时
// 忘掉一处, 症状同样是静默的.
func TestRestoreAllBringsEverythingBack(t *testing.T) {
	now := func() time.Time { return time.Now() }
	log := NewEventLog(func() int64 { return now().UnixMilli() })

	places := NewPlaces(log)
	watches := NewWatches(log)
	timers := NewTimers(log, now, func(Wake, int64) {})
	budget := NewInterruptBudget(log, 2, now)
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Hour, Lateness: time.Minute})

	// 一段真实的使用
	places.Add("公司", 31.86, 117.28, 0)
	watches.Add("place.left", "公司", "", "离开公司告诉我")
	timers.Set("th", time.Now().Add(2*time.Hour).UnixMilli(), "吃药")
	budget.Admit(Notice{Text: "门锁开了"})
	budget.Admit(Notice{Text: "快递到了"})
	budget.Admit(Notice{Text: "攒下的这条"}) // 额度 2, 这条攒着
	bus.Mute("ha.badsensor", "state.changed", "太吵")
	nowMs := time.Now().UnixMilli()
	bus.Ingest(abi.Signal{ID: "s1", Source: "phone.mk", Kind: "phone.arrived",
		At: nowMs, KnownAt: nowMs})

	// 杀掉 —— 全新的对象, 只有账本
	prior := map[abi.ProcessID][]abi.Event{signalPID: log.Replay(signalPID, 0)}
	log2 := NewEventLog(func() int64 { return now().UnixMilli() })
	tg := RestoreTargets{
		Places: NewPlaces(log2), Watches: NewWatches(log2),
		Timers: NewTimers(log2, now, func(Wake, int64) {}),
		Budget: NewInterruptBudget(log2, 2, now),
		Bus: NewSignalBus(log2, func(Digest) {}, SignalOptions{
			Window: time.Hour, Lateness: time.Minute}),
	}
	got := RestoreAll(prior, tg)

	for _, c := range []struct {
		what     string
		want, is int
	}{
		{"地点", 1, got.Places},
		{"关注", 1, got.Watches},
		{"提醒", 1, got.Wakes},
		{"静音", 1, got.Mutes},
		{"今天打扰过几次", 2, got.Used},
		{"攒着的", 1, got.Held},
		{"幂等键", 1, got.SeenKeys},
		{"捞回的信号", 1, got.Pending},
	} {
		if c.is != c.want {
			t.Errorf("%s: 恢复出来 %d, 该是 %d", c.what, c.is, c.want)
		}
	}
	// 静音在恢复之后仍然有效 —— 只恢复了列表而没接上判断的话,
	// 上面那个 Mutes=1 也是绿的, 但噪音照样进摘要
	if v := tg.Bus.Ingest(abi.Signal{ID: "s9", Source: "ha.badsensor",
		Kind: "state.changed", At: nowMs, KnownAt: nowMs}); v != IngestMuted {
		t.Errorf("恢复之后静音没生效, 判成了 %s", v)
	}
}

// **预算必须先于闹钟恢复.**
//
// Timers.Restore 末尾会走一遍 Tick, 把过期的闹钟当场补响 —— 而补响走的是
// 通知那条路. 预算还没恢复的话那几条不占额度, 于是"一天最多打扰三次"
// 在每次重启时都被悄悄突破一点.
func TestBudgetIsRestoredBeforeTimersFire(t *testing.T) {
	now := func() time.Time { return time.Now() }
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	budget := NewInterruptBudget(log, 2, now)
	budget.Admit(Notice{Text: "一"})
	budget.Admit(Notice{Text: "二"}) // 额度用完
	// 一个早就过期的闹钟 —— 恢复时会当场补响.
	// **直接往账本里写**: Set 一个过去的时间是当场就响掉的, 落不下这条,
	// 而"机器关着的时候那个时刻过去了"才是真实的来路
	log.Append(signalPID, abi.EvWakeSet, map[string]any{
		"id": "w1", "thread": "th", "text": "早就该响的",
		"at":    float64(time.Now().Add(-time.Hour).UnixMilli()),
		"setAt": float64(time.Now().Add(-2 * time.Hour).UnixMilli())})

	prior := map[abi.ProcessID][]abi.Event{signalPID: log.Replay(signalPID, 0)}
	log2 := NewEventLog(func() int64 { return now().UnixMilli() })
	b2 := NewInterruptBudget(log2, 2, now)
	var firedWhenUsed int
	tg := RestoreTargets{
		Budget: b2,
		Timers: NewTimers(log2, now, func(Wake, int64) {
			firedWhenUsed, _, _ = b2.Stats()
		}),
	}
	RestoreAll(prior, tg)
	if firedWhenUsed != 2 {
		t.Fatalf("闹钟补响那一刻预算里记着用了 %d 次(该是 2) —— "+
			"预算在闹钟之后才恢复, 这条补响没占额度", firedWhenUsed)
	}
}

// **S32 那道闸漏了一半, 而漏掉的那一半刚出了第四例.**
//
// 它查的是"每种事件有没有人在重启时读". 而 presence 是**从事件推导
// 出来的状态**: place.left 是 EvSignal, 有人读(RestoreSeen +
// RecoverPending), 闸看着是绿的 —— 可 RecoverPending 只捞最后一份
// 摘要之后的信号, 三小时前那条"离开家了"根本不会重放.
//
// 于是升级/崩溃/断电之后"家里有没有人"回到"不知道",
// 而那件★(没人在家时门开了)从此不会被说.
//
// 所以再补一条: **RestoreTargets 里的每一样, 都要真的在 RestoreAll
// 里被恢复**. 加一个字段却忘了接上, 当场变红.
func TestEveryRestoreTargetIsActuallyRestored(t *testing.T) {
	raw, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	// 从源码里现读字段名 —— 手写清单等于把同一个陷阱再搭一遍
	fields := regexp.MustCompile(`(?m)^\s+(\w+)\s+\*(Timers|Places|Watches|InterruptBudget|DailyReport|SignalBus|Presence)\b`).
		FindAllStringSubmatch(body, -1)
	if len(fields) < 6 {
		t.Fatalf("只读出 %d 个要恢复的东西 —— 正则跟源码对不上了", len(fields))
	}
	var missing []string
	for _, f := range fields {
		// 每个字段都得在 RestoreAll 的函数体里被用到
		if !strings.Contains(body, "t."+f[1]) {
			missing = append(missing, f[1])
		}
	}
	if len(missing) > 0 {
		t.Fatalf("这些登记要恢复却没人恢复: %s —— "+
			"加了字段忘了接上, 症状是静默的(看起来一切正常, "+
			"只是某件事悄悄不算数了)", strings.Join(missing, " "))
	}
}

// **开机横幅里"认得哪些地方"那一行, S32 重构时没跟过来.**
//
// 而 S67 刚证明这一条是决定性的: 没认过"家"的话, 手机投的坐标查不到
// 地名, "家里有没有人"永远是"不知道" —— 那件★(没人在家时门开了)
// 从此不会被说, 而且是静默的.
//
// 一眼看得见"它认得家"和"它一个地方都不认得", 是用户唯一能自己
// 发现这件事的地方.
//
// **而且要点名**: "认得 3 个地方"跟"认得家/公司/健身房"对用户的意义
// 完全不一样 —— 后者他能一眼看出少了哪个.
func TestBootBannerNamesTheKnownPlaces(t *testing.T) {
	now := func() time.Time { return time.Now() }
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	p := NewPlaces(log)
	p.Add("家", 31.88, 117.28, 0)
	p.Add("公司", 31.86, 117.28, 0)

	prior := map[abi.ProcessID][]abi.Event{signalPID: log.Replay(signalPID, 0)}
	got := RestoreAll(prior, RestoreTargets{Places: NewPlaces(nil)})

	// 断的是**点名**这件事, 不是某个措辞 —— 措辞已经改过一次了
	var line string
	for _, l := range got.Lines() {
		if strings.Contains(l, "认得") {
			line = l
		}
	}
	if line == "" {
		t.Fatal("开机横幅里没有'认得哪些地方' —— 而没认过家的话, " +
			"那件'没人在家时门开了'永远不会被说, 且是静默的")
	}
	for _, name := range []string{"家", "公司"} {
		if !strings.Contains(line, name) {
			t.Fatalf("没点名: %q —— '认得 2 个地方'看不出少了哪个", line)
		}
	}
}

// 一个地方都没认过的时候**要显眼地说出来**, 而不是什么都不显示 ——
// 那正是最需要提醒的状态(手机的位置信号会全部白投)
func TestBootBannerSaysWhenItKnowsNoPlace(t *testing.T) {
	got := RestoreAll(map[abi.ProcessID][]abi.Event{}, RestoreTargets{
		Places: NewPlaces(nil), Bus: NewSignalBus(nil, func(Digest) {}, SignalOptions{}),
	})
	var found bool
	for _, l := range got.Lines() {
		if strings.Contains(l, "还不认得任何地方") {
			found = true
		}
	}
	if !found {
		t.Fatal("一个地方都没认过却什么都不说 —— " +
			"而那时候手机的位置信号会全部白投, 用户无从知道")
	}
}
