package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func newDaily(t *testing.T, hour int, startAt time.Time) (
	*DailyReport, *InterruptBudget, *fakeClock, *[]string, *EventLog) {
	t.Helper()
	clk := &fakeClock{t: startAt}
	log := NewEventLog(func() int64 { return clk.now().UnixMilli() })
	b := NewInterruptBudget(log, 1, clk.now)
	var sent []string
	d := NewDailyReport(log, b, hour, clk.now, func(s string) { sent = append(sent, s) })
	return d, b, clk, &sent, log
}

func defer1(b *InterruptBudget, clk *fakeClock, text string) {
	b.Admit(Notice{Text: text, At: clk.now().UnixMilli()})
}

// **攒下的必须有一条不需要用户主动的出口.**
//
// 没有它, "延后"跟"丢掉"的区别很小 —— 用户不知道有东西攒着,
// 也就永远不会去问.
func TestDailyReportDeliversHeldItems(t *testing.T) {
	d, b, clk, sent, _ := newDaily(t, 21, time.Date(2026, 8, 15, 9, 0, 0, 0, time.Local))
	defer1(b, clk, "用掉额度的那条") // 额度 1, 这条放行
	defer1(b, clk, "疫苗过期了三天") // 攒着
	defer1(b, clk, "有个快递到了")  // 攒着

	// 还没到点
	clk.advance(6 * time.Hour) // 15:00
	d.Tick()
	if len(*sent) != 0 {
		t.Fatal("没到点就发了")
	}

	clk.advance(7 * time.Hour) // 22:00
	d.Tick()
	if len(*sent) != 1 {
		t.Fatalf("到点没发: %v", *sent)
	}
	for _, want := range []string{"疫苗过期了三天", "有个快递到了", "2 件"} {
		if !strings.Contains((*sent)[0], want) {
			t.Fatalf("日报里缺 %q:\n%s", want, (*sent)[0])
		}
	}
	// 放行过的那条不该再出现在日报里 —— 他已经知道了
	if strings.Contains((*sent)[0], "用掉额度的那条") {
		t.Fatal("已经说过的事又在日报里说了一遍")
	}
}

// **空的不发** —— "今天没什么要说的"这句话本身就是打扰
func TestEmptyDailyReportIsNotSent(t *testing.T) {
	d, _, clk, sent, log := newDaily(t, 21, time.Date(2026, 8, 15, 9, 0, 0, 0, time.Local))
	clk.advance(13 * time.Hour)
	d.Tick()
	if len(*sent) != 0 {
		t.Fatalf("没东西可说却发了: %v", *sent)
	}
	// **但要记账**: "今天真的很安静"和"日报坏了"两种情况,
	// 用户看到的都是"没收到日报", 只有账本能分开
	found := false
	for _, e := range log.Replay(signalPID, 0) {
		if e.Kind == abi.EvDailyReport {
			found = true
		}
	}
	if !found {
		t.Fatal("空日报没记账 —— '很安静'和'坏了'就分不开了")
	}
}

// 一天只发一次, 否则它退化成一个慢一点的通知流
func TestDailyReportFiresOncePerDay(t *testing.T) {
	d, b, clk, sent, _ := newDaily(t, 21, time.Date(2026, 8, 15, 20, 0, 0, 0, time.Local))
	b.Admit(Notice{Text: "占额度", At: clk.now().UnixMilli()})
	defer1(b, clk, "攒着的")
	clk.advance(2 * time.Hour)
	for i := 0; i < 20; i++ {
		d.Tick()
		clk.advance(time.Minute)
	}
	if len(*sent) != 1 {
		t.Fatalf("一天发了 %d 次", len(*sent))
	}

	// 第二天有新的攒项 → 再发一次.
	//
	// **跨天时额度会重置**, 所以第二天的第一条是放行不是攒着 ——
	// 头一版我漏了这一步, 于是"第二天没东西攒"被我读成"跨天没重新武装".
	// 错在测试不在代码.
	clk.advance(20 * time.Hour) // 次日 18 点左右
	defer1(b, clk, "第二天占额度的")
	defer1(b, clk, "第二天攒的")
	clk.advance(4 * time.Hour)
	d.Tick()
	if len(*sent) != 2 {
		t.Fatalf("跨天没有重新武装: %d 次", len(*sent))
	}
	if !strings.Contains((*sent)[1], "第二天攒的") {
		t.Fatalf("第二天的日报内容不对:\n%s", (*sent)[1])
	}
}

// 发过就清空 —— 说过一次不该再说第二次
func TestDailyReportDoesNotRepeatItems(t *testing.T) {
	d, b, clk, sent, _ := newDaily(t, 21, time.Date(2026, 8, 15, 20, 0, 0, 0, time.Local))
	b.Admit(Notice{Text: "占额度", At: clk.now().UnixMilli()})
	defer1(b, clk, "只该说一次")
	clk.advance(2 * time.Hour)
	d.Tick()
	clk.advance(24 * time.Hour)
	d.Tick()
	if len(*sent) != 1 {
		t.Fatalf("同一条被说了 %d 次", len(*sent))
	}
}

// **"今天发过没有"必须落盘.**
//
// 不落的话每次重启都重新武装 —— 一天重启五次就发五份日报,
// 而日报的全部价值在于"一天一次、可预期". 发五次比不发更糟:
// 它从"可预期的摘要"变成了"随机的打扰".
func TestDailyReportDoesNotRepeatAfterRestart(t *testing.T) {
	d, b, clk, sent, log := newDaily(t, 21, time.Date(2026, 8, 15, 20, 0, 0, 0, time.Local))
	b.Admit(Notice{Text: "占额度", At: clk.now().UnixMilli()})
	defer1(b, clk, "攒着的")
	clk.advance(2 * time.Hour)
	d.Tick()
	if len(*sent) != 1 {
		t.Fatal("第一次没发")
	}

	// 重启: 新的 DailyReport + 新的预算(攒项也一起没了, 那是另一件事)
	var sent2 []string
	b2 := NewInterruptBudget(nil, 1, clk.now)
	d2 := NewDailyReport(nil, b2, 21, clk.now, func(s string) { sent2 = append(sent2, s) })
	d2.Restore(log.Replay(signalPID, 0))
	defer1(b2, clk, "重启后又攒的")
	for i := 0; i < 10; i++ {
		d2.Tick()
		clk.advance(time.Minute)
	}
	if len(sent2) != 0 {
		t.Fatalf("重启之后又发了一遍今天的日报: %v —— "+
			"一天重启五次就发五份, 那比不发更糟", sent2)
	}
}

// **机器整天没开, 晚上才开机 → 补发.**
//
// 跟错过的闹钟同一条原则: 晚发比不发强.
func TestDailyReportCatchesUpAfterBeingOff(t *testing.T) {
	// 机器 23 点才开机, 而发送时刻是 21 点
	d, b, clk, sent, _ := newDaily(t, 21, time.Date(2026, 8, 15, 23, 0, 0, 0, time.Local))
	b.Admit(Notice{Text: "占额度", At: clk.now().UnixMilli()})
	defer1(b, clk, "白天攒的")
	d.Tick()
	if len(*sent) != 1 {
		t.Fatal("过了发送时刻才开机, 日报被吞掉了 —— 晚发比不发强")
	}
}

// 日报**不占打扰额度**: 它一天一次、时间固定, 是可预期的.
// 可预期的打扰不消耗信任 —— 这跟"它突然冒出来说一句"是两件事
func TestDailyReportDoesNotConsumeQuota(t *testing.T) {
	d, b, clk, _, _ := newDaily(t, 21, time.Date(2026, 8, 15, 20, 0, 0, 0, time.Local))
	b.Admit(Notice{Text: "占额度", At: clk.now().UnixMilli()})
	defer1(b, clk, "攒着的")
	usedBefore, _, _ := b.Stats()
	clk.advance(2 * time.Hour)
	d.Tick()
	usedAfter, _, _ := b.Stats()
	if usedAfter != usedBefore {
		t.Fatalf("日报占了打扰额度: %d → %d", usedBefore, usedAfter)
	}
}

// **日报是唯一不需要用户主动的出口, 而它只有终端看得见.**
//
// 它走的是 send 回调 → 宿主 Printf. 于是:
//
//	· 人不在电脑前的时候, 那份小结等于没发 —— 而"用户不必主动问"
//	  正是它存在的全部意义
//	· 账本里只记了"发过了"(items/used/quota), **没记它说了什么** ——
//	  于是"昨天那份小结讲了啥"事后查不到
//	· 而且第二天不会重发: 记着 lastDay, 看起来一切正常
//
// 这跟仓库那条"UI 只是订阅者之一, 没有特权客户端"是直接冲突的:
// 推送、语音、手机端都该能订到同一份小结, 而现在只有那一个终端能.
func TestDailyReportTextLandsInTheLedger(t *testing.T) {
	now := time.Date(2026, 8, 16, 21, 30, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	b := NewInterruptBudget(log, 1, func() time.Time { return now })
	b.Admit(Notice{Text: "说出去的"})
	b.Admit(Notice{Text: "攒着的这条"})

	var said string
	d := NewDailyReport(log, b, 21, func() time.Time { return now },
		func(s string) { said = s })
	d.Tick()

	if !strings.Contains(said, "攒着的这条") {
		t.Fatalf("日报没把攒着的那条说出来: %q", said)
	}
	// **正文要进账本** —— 不然"昨天那份小结讲了啥"事后查不到
	var text string
	for _, e := range log.Replay(signalPID, 0) {
		if e.Kind == abi.EvDailyReport {
			if m, ok := e.Payload.(map[string]any); ok {
				text, _ = m["text"].(string)
			}
		}
	}
	if !strings.Contains(text, "攒着的这条") {
		t.Fatalf("账本里只记了'发过了', 没记它说了什么: %q —— "+
			"而这份小结是唯一不需要用户主动的出口", text)
	}
}

// 空的那次不发, 但**照样要记账** —— 这条原来就对, 别在改的时候弄丢:
// "今天真的很安静"和"日报坏了"长得一模一样
func TestEmptyDailyStillRecordsWithoutText(t *testing.T) {
	now := time.Date(2026, 8, 16, 21, 30, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	b := NewInterruptBudget(log, 3, func() time.Time { return now })
	d := NewDailyReport(log, b, 21, func() time.Time { return now },
		func(string) { t.Fatal("空的时候不该发") })
	d.Tick()

	found := false
	for _, e := range log.Replay(signalPID, 0) {
		if e.Kind == abi.EvDailyReport {
			found = true
			if m, ok := e.Payload.(map[string]any); ok {
				if txt, _ := m["text"].(string); txt != "" {
					t.Fatalf("空的那次记了正文: %q", txt)
				}
			}
		}
	}
	if !found {
		t.Fatal("空的那次一条账都没记 —— '今天很安静'和'日报坏了'从此长得一样")
	}
}
