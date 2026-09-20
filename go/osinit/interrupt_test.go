package osinit

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func newBudget(t *testing.T, perDay int) (*InterruptBudget, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 8, 15, 9, 0, 0, 0, time.Local)}
	return NewInterruptBudget(nil, perDay, clk.now), clk
}

func notice(text string, at time.Time) Notice {
	return Notice{Text: text, Why: "测试", At: at.UnixMilli()}
}

// 额度用完之后**攒着, 不是丢掉**.
//
// 直接丢是最糟的: 那条信息可能真有用, 只是不够格现在打断你.
func TestOverQuotaDefersInsteadOfDropping(t *testing.T) {
	b, clk := newBudget(t, 3)
	for i := 0; i < 3; i++ {
		if v := b.Admit(notice(fmt.Sprintf("事情 %d", i), clk.now())); v != NoticeDelivered {
			t.Fatalf("第 %d 条该放行, 实际 %s", i, v)
		}
		clk.advance(time.Hour)
	}
	if v := b.Admit(notice("第四件事", clk.now())); v != NoticeDeferred {
		t.Fatalf("超额那条该攒着, 实际 %s", v)
	}
	got := b.Deferred()
	if len(got) != 1 || got[0].Text != "第四件事" {
		t.Fatalf("攒着的取不出来: %+v", got)
	}
	if len(b.Deferred()) != 0 {
		t.Fatal("取走之后该清空 —— 说过一次不该再说第二次")
	}
}

// **破例资格由 OS 按信号种类判, 不由 agent 自述.**
//
// 每个 agent 都会觉得自己的事最急 —— 跟"子 agent 预估不出自己的开销"
// 是同一类错.
func TestBreakthroughComesFromSignalKindNotAgentClaim(t *testing.T) {
	b, clk := newBudget(t, 1)
	b.Admit(notice("用掉额度", clk.now()))
	clk.advance(time.Minute)

	// agent 自称十万火急 —— 不作数
	n := notice("这非常非常紧急！！", clk.now())
	n.Why = "critical urgent 不可逆 立刻"
	if v := b.Admit(n); v != NoticeDeferred {
		t.Fatalf("agent 自称紧急就破例了(%s) —— 那么它每次都会这么写", v)
	}

	// OS 侧刚投过一条紧急摘要 → 这时候说的话才有破例资格
	b.NoteUrgent(clk.now().UnixMilli())
	clk.advance(10 * time.Second)
	if v := b.Admit(notice("门锁刚才开了", clk.now())); v != NoticeBreakthrough {
		t.Fatalf("紧急信号之后的话该破例, 实际 %s", v)
	}
}

// 破例是有时效的 —— 一条紧急信号不该给它一整天的免费额度
func TestBreakthroughWindowIsBounded(t *testing.T) {
	b, clk := newBudget(t, 1)
	b.Admit(notice("用掉额度", clk.now()))
	b.NoteUrgent(clk.now().UnixMilli())

	clk.advance(10 * time.Minute) // 远超 urgentEcho
	if v := b.Admit(notice("十分钟后的另一件事", clk.now())); v == NoticeBreakthrough {
		t.Fatal("一条紧急信号给了后面十分钟免费额度 —— 那等于没有预算")
	}
}

// 同一件事今天攒过一次就不再攒 —— 否则日报里会有五条"该吃药了"
func TestDeferredIsDeduped(t *testing.T) {
	b, clk := newBudget(t, 0) // 0 → 用缺省 3
	for i := 0; i < 3; i++ {
		b.Admit(notice(fmt.Sprintf("占额度 %d", i), clk.now()))
	}
	b.Admit(notice("该吃药了", clk.now()))
	for i := 0; i < 4; i++ {
		clk.advance(time.Minute)
		if v := b.Admit(notice("该吃药了", clk.now())); v != NoticeDuplicate {
			t.Fatalf("重复的攒项该判重, 实际 %s", v)
		}
	}
	if got := b.Deferred(); len(got) != 1 {
		t.Fatalf("日报里有 %d 条一样的", len(got))
	}
}

// **按自然日重置, 不是滑动 24 小时.**
//
// 滑动窗口的话, 昨晚十点用掉的三次会一直压到今晚十点 ——
// 而用户的感受是"今天它一次都没提醒我".
func TestQuotaResetsOnCalendarDay(t *testing.T) {
	b := NewInterruptBudget(nil, 2, nil)
	clk := &fakeClock{t: time.Date(2026, 8, 15, 22, 0, 0, 0, time.Local)}
	b.now = clk.now

	b.Admit(notice("昨晚一", clk.now()))
	b.Admit(notice("昨晚二", clk.now()))
	if v := b.Admit(notice("昨晚三", clk.now())); v != NoticeDeferred {
		t.Fatal("额度该用完了")
	}
	// 过了午夜
	clk.advance(4 * time.Hour)
	if v := b.Admit(notice("今天第一件", clk.now())); v != NoticeDelivered {
		t.Fatalf("跨天没重置额度, 实际 %s —— 昨晚的三次会压着今天一整天", v)
	}
}

// 跨天时**攒着的不能丢**: 它们还没被说过
func TestDeferredSurvivesDayRoll(t *testing.T) {
	b := NewInterruptBudget(nil, 1, nil)
	clk := &fakeClock{t: time.Date(2026, 8, 15, 22, 0, 0, 0, time.Local)}
	b.now = clk.now
	b.Admit(notice("用掉", clk.now()))
	b.Admit(notice("攒下的", clk.now()))
	clk.advance(4 * time.Hour)
	b.Admit(notice("新的一天", clk.now()))
	if got := b.Deferred(); len(got) != 1 || got[0].Text != "攒下的" {
		t.Fatalf("跨天把攒着的弄丢了: %+v —— 它们还没被说过", got)
	}
}

// **没放行的也要记账.**
//
// "今天很安静"和"额度早就用完、后面全在攒"两种情况, 用户看到的
// 都是"它没怎么说话" —— 只有账本能分开.
func TestEveryVerdictIsRecorded(t *testing.T) {
	log := NewEventLog(func() int64 { return 0 })
	clk := &fakeClock{t: time.Date(2026, 8, 15, 9, 0, 0, 0, time.Local)}
	b := NewInterruptBudget(log, 1, clk.now)
	b.Admit(notice("放行的", clk.now()))
	b.Admit(notice("攒下的", clk.now()))

	var deliver, defer_ int
	for _, e := range log.Replay(signalPID, 0) {
		m, _ := e.Payload.(map[string]any)
		switch m["verdict"] {
		case string(NoticeDelivered):
			deliver++
		case string(NoticeDeferred):
			defer_++
		}
	}
	if deliver != 1 || defer_ != 1 {
		t.Fatalf("账本里 放行=%d 攒下=%d —— 没放行的那些必须也留痕", deliver, defer_)
	}
}

// 用了几次/攒了几条要能被问出来
func TestStatsAreObservable(t *testing.T) {
	b, clk := newBudget(t, 2)
	b.Admit(notice("一", clk.now()))
	b.Admit(notice("二", clk.now()))
	b.Admit(notice("三", clk.now()))
	used, quota, def := b.Stats()
	if used != 2 || quota != 2 || def != 1 {
		t.Fatalf("用了 %d/%d, 攒了 %d —— 这三个数要准", used, quota, def)
	}
}

// **主动进程需要的历史只有"我今天说过什么".**
//
// 长跑时它的上下文线性无界增长(40 份摘要 → prompt 1099 涨到 5112),
// 而它每次只做一个判断, 积累的几十份旧摘要跟这次判断无关.
//
// 提示词里写着"你已经说过的事再次发生不该再说" —— 那条历史 OS 手里就有.
// 把它作为事实给出去, 主动进程就不再需要靠自己的上下文记住.
func TestSaidTodayFeedsBackWhatWasSpoken(t *testing.T) {
	b, clk := newBudget(t, 3)
	if b.SaidNote() != "" {
		t.Fatal("什么都没说过却贴了话 —— 每份摘要多一段空话是纯噪音")
	}
	b.Admit(Notice{Text: "大门锁在凌晨 3 点被打开", At: clk.now().UnixMilli()})
	b.Admit(Notice{Text: "降压药超时未服", At: clk.now().UnixMilli()})

	note := b.SaidNote()
	for _, want := range []string{"大门锁", "降压药", "别重复", "除非这次真的不一样"} {
		if !strings.Contains(note, want) {
			t.Fatalf("贴的话里缺 %q:\n%s", want, note)
		}
	}
	// 攒下的不算"说过" —— 用户没看到
	b.Admit(Notice{Text: "这条被攒下了", At: clk.now().UnixMilli()})
	b.Admit(Notice{Text: "这条也攒下了", At: clk.now().UnixMilli()})
	if strings.Contains(b.SaidNote(), "这条也攒下了") {
		t.Fatal("把攒下的也算成说过了 —— 用户根本没看到它")
	}
}

// 跨天要清空 —— "昨天说过"不该压着今天
func TestSaidTodayResetsAcrossDays(t *testing.T) {
	b := NewInterruptBudget(nil, 3, nil)
	clk := &fakeClock{t: time.Date(2026, 8, 15, 22, 0, 0, 0, time.Local)}
	b.now = clk.now
	b.Admit(Notice{Text: "昨晚说过的", At: clk.now().UnixMilli()})
	if len(b.SaidToday()) != 1 {
		t.Fatal("当天的没记上")
	}
	clk.advance(4 * time.Hour)
	if len(b.SaidToday()) != 0 {
		t.Fatalf("跨天没清空: %v —— '昨天说过'不该压着今天", b.SaidToday())
	}
}
