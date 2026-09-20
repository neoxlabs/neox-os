package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func deliveries(t *testing.T) (*Deliveries, *EventLog) {
	t.Helper()
	log := NewEventLog(func() int64 { return 1 })
	return NewDeliveries(log), log
}

func posted(log *EventLog) []map[string]any {
	var out []map[string]any
	for _, e := range flatten(log) {
		if e.Kind == abi.EvDelivery {
			out = append(out, e.Payload.(map[string]any))
		}
	}
	return out
}

func flatten(log *EventLog) []abi.Event {
	var out []abi.Event
	for _, evs := range log.Snapshot() {
		out = append(out, evs...)
	}
	return out
}

// 空文本一律丢掉 —— 一条没内容的通知在手机上是个空气泡,
// 用户点进来什么都没有, 那比不通知更让人不安
func TestDeliveryDropsEmpty(t *testing.T) {
	d, log := deliveries(t)
	d.Post(Deliver{Kind: DeliverRemind, Text: "   "})
	d.Post(Deliver{Kind: DeliverRemind, Text: ""})
	if n := len(posted(log)); n != 0 {
		t.Errorf("空文本该丢掉, 却送出去了 %d 条", n)
	}
}

// from 和 why 空着就不写进 payload —— 订阅方那边少判一次
// "这是空的还是没给"
func TestDeliveryOmitsEmptyFields(t *testing.T) {
	d, log := deliveries(t)
	d.Post(Deliver{Kind: DeliverProactive, Text: "有事"})
	p := posted(log)[0]
	if _, ok := p["from"]; ok {
		t.Error("from 空着不该写进 payload")
	}
	if _, ok := p["why"]; ok {
		t.Error("why 空着不该写进 payload")
	}
}

// **攒着和重复的一律不送** —— 它们的意思恰恰是"这次不打扰你",
// 送出去的话整套打扰预算就白做了
func TestBudgetOnlyDeliversWhatItActuallySaid(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	d := NewDeliveries(log)
	now := time.Now()
	b := NewInterruptBudget(log, 1, func() time.Time { return now })
	b.UseDeliveries(d)

	// 第一条: 额度内, 送
	b.Admit(Notice{Text: "第一条", Why: "actionable: 能做点什么"})
	// 第二条: 额度用完, 攒着
	b.Admit(Notice{Text: "第二条", Why: "actionable: 也能做点什么"})

	got := posted(log)
	if len(got) != 1 {
		t.Fatalf("额度是 1, 该只送出去 1 条, 实际 %d 条 —— "+
			"攒着的那条送出去的话打扰预算就白做了", len(got))
	}
	if got[0]["text"] != "第一条" {
		t.Errorf("送错了: %v", got[0]["text"])
	}
	if got[0]["why"] == nil || got[0]["why"] == "" {
		t.Error("why 必须带上 —— 一次打扰值不值得, " +
			"只有看见它凭什么说才判得出来")
	}
}

// 普通放行不吵醒人, **破例才吵** —— 后者是它判断值得"现在就说"
func TestOnlyBreakthroughIsUrgent(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	d := NewDeliveries(log)
	now := time.Now()
	b := NewInterruptBudget(log, 5, func() time.Time { return now })
	b.UseDeliveries(d)
	b.Admit(Notice{Text: "普通的", Why: "actionable: x"})
	got := posted(log)
	if len(got) == 0 {
		t.Fatal("该送出去一条")
	}
	if got[0]["urgent"] == true {
		t.Error("普通放行不该 urgent —— 只有破例才配吵醒人")
	}
}
