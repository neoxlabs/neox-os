package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func fixedNow(s string) func() time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	return func() time.Time { return t }
}

// 跑一段, 把它的账本原样喂给一个新的预算对象 —— 那就是重启
func replayInto(t *testing.T, log *EventLog, now func() time.Time, perDay int) *InterruptBudget {
	t.Helper()
	b := NewInterruptBudget(nil, perDay, now)
	b.Restore(log.Replay(signalPID, 0))
	return b
}

// **重启之后额度归零 = 一天重启三次就打扰九次.**
//
// 而 S23 已经证明这台机器**经常被杀**(开机扫出 199 个残留 socket).
// 后果不是多花几个 token, 是用户把通知关掉 —— 而通知一旦被关掉,
// 真正重要的那次也到不了他. 这正是这个预算存在的全部理由.
func TestBudgetSurvivesRestart(t *testing.T) {
	now := fixedNow("2026-08-16 10:00")
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	b := NewInterruptBudget(log, 3, now)
	for i, txt := range []string{"一", "二", "三"} {
		if v := b.Admit(Notice{Text: txt}); v != NoticeDelivered {
			t.Fatalf("第 %d 条该放行, 实际 %s", i+1, v)
		}
	}

	b2 := replayInto(t, log, now, 3)
	if used, _, _ := b2.Stats(); used != 3 {
		t.Fatalf("重启之后用了 %d 次, 该是 3 —— 额度归零, 今天会被打扰六次", used)
	}
	if v := b2.Admit(Notice{Text: "四"}); v != NoticeDeferred {
		t.Fatalf("重启之后第四条是 %s, 该攒着", v)
	}
}

// **重启之后"今天说过什么"全忘 = 它会把同一件事再说一遍.**
//
// SaidToday 是 S22 建起来的: 主动进程不再靠自己的上下文记住说过什么,
// 改成 OS 给的事实. 那个事实一重启就没了, 于是提示词里
// "你已经说过的事再次发生不该再说"就落空了.
func TestSaidTodaySurvivesRestart(t *testing.T) {
	now := fixedNow("2026-08-16 10:00")
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	b := NewInterruptBudget(log, 3, now)
	b.Admit(Notice{Text: "门锁开了"})

	b2 := replayInto(t, log, now, 3)
	said := b2.SaidToday()
	if len(said) != 1 || said[0] != "门锁开了" {
		t.Fatalf("重启之后今天说过的是 %v —— 它会把门锁那条再说一遍", said)
	}
}

// **重启之后攒着的全丢 = "超额不是拒绝, 是延后"变成了静默丢弃.**
//
// 那是这套设计唯一明确许下的承诺: 攒着 → 等你问、或者进日报.
func TestDeferredSurvivesRestart(t *testing.T) {
	now := fixedNow("2026-08-16 10:00")
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	b := NewInterruptBudget(log, 1, now)
	b.Admit(Notice{Text: "说出去的"})
	b.Admit(Notice{Text: "攒着的甲"})
	b.Admit(Notice{Text: "攒着的乙"})

	b2 := replayInto(t, log, now, 1)
	held := b2.Deferred()
	if len(held) != 2 {
		t.Fatalf("重启之后攒着 %d 条, 该是 2 —— 它们永远不会进日报了", len(held))
	}
}

// **取走过的不许复活.**
//
// 日报发完就把攒的清空了("说过一次就不该再说第二次").
// 重放时如果只看 defer 事件, 昨天日报里已经报过的那些会原样再报一遍 ——
// 而日报的全部价值在于"一天一次、可预期".
func TestFlushedDeferredDoesNotComeBack(t *testing.T) {
	now := fixedNow("2026-08-16 10:00")
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	b := NewInterruptBudget(log, 1, now)
	b.Admit(Notice{Text: "说出去的"})
	b.Admit(Notice{Text: "攒着的"})
	if n := len(b.Deferred()); n != 1 { // 日报取走
		t.Fatalf("取走了 %d 条", n)
	}

	b2 := replayInto(t, log, now, 1)
	if held := b2.Deferred(); len(held) != 0 {
		t.Fatalf("已经报过的又活了: %+v", held)
	}
}

// **跨天要重置额度, 但攒着的留着** —— 这条原来就写在 rollDay 里,
// 重放必须守同一条规矩, 否则昨晚的三次会压着今天上午
func TestRestoreResetsQuotaAcrossDay(t *testing.T) {
	yest := fixedNow("2026-08-15 22:00")
	log := NewEventLog(func() int64 { return yest().UnixMilli() })
	b := NewInterruptBudget(log, 1, yest)
	b.Admit(Notice{Text: "昨晚说的"})
	b.Admit(Notice{Text: "昨晚攒的"})

	b2 := replayInto(t, log, fixedNow("2026-08-16 09:00"), 1)
	if used, _, _ := b2.Stats(); used != 0 {
		t.Fatalf("今天开机用了 %d 次 —— 昨晚的额度压到了今天", used)
	}
	if len(b2.SaidToday()) != 0 {
		t.Fatalf("昨天说过的算进了今天: %v", b2.SaidToday())
	}
	if held := b2.Deferred(); len(held) != 1 {
		t.Fatalf("昨晚攒的 %d 条 —— 跨天不该丢, 它们还没被说过", len(held))
	}
}

// 破例不占额度 —— 重放要跟当时的判定一致, 不能把破例算成用掉一次
func TestRestoreKeepsBreakthroughOffQuota(t *testing.T) {
	now := fixedNow("2026-08-16 10:00")
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	b := NewInterruptBudget(log, 3, now)
	b.NoteUrgent(now().UnixMilli())
	if v := b.Admit(Notice{Text: "门开了"}); v != NoticeBreakthrough {
		t.Fatalf("该破例, 实际 %s", v)
	}
	b2 := replayInto(t, log, now, 3)
	if used, _, _ := b2.Stats(); used != 0 {
		t.Fatalf("破例被算成用掉 %d 次额度", used)
	}
	// 但它**说过**了 —— 不然重启后会再说一遍门开了
	if len(b2.SaidToday()) != 1 {
		t.Fatalf("破例说出去的话没算进'今天说过什么': %v", b2.SaidToday())
	}
}

// **调用方是"每个桶喂一遍"的循环 —— Restore 不许清空.**
//
// 账本按进程分桶, main 里的写法是 `for _, evs := range prior { X.Restore(evs) }`
// (timers/places/watches 都这样, 因为"不假设它在哪个桶里").
// Restore 开头如果重置状态, sense 那个桶只要不是最后一个,
// 读到的东西就会被后面的空桶抹干净 —— 而它看起来完全正常.
func TestRestoreAcrossBucketsDoesNotWipe(t *testing.T) {
	now := fixedNow("2026-08-16 10:00")
	log := NewEventLog(func() int64 { return now().UnixMilli() })
	b := NewInterruptBudget(log, 3, now)
	b.Admit(Notice{Text: "说过的"})

	b2 := NewInterruptBudget(nil, 3, now)
	b2.Restore(log.Replay(signalPID, 0)) // sense 那个桶
	b2.Restore(nil)                      // 后面还有别的进程的桶
	b2.Restore([]abi.Event{})

	if used, _, _ := b2.Stats(); used != 1 {
		t.Fatalf("喂完别的桶之后用了 %d 次 —— 前面读到的被抹掉了", used)
	}
	if len(b2.SaidToday()) != 1 {
		t.Fatalf("今天说过的被抹掉了: %v", b2.SaidToday())
	}
}
