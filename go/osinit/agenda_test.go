package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func cal(id int, what string, at int64) abi.Signal {
	return abi.Signal{Kind: "calendar.event", Body: map[string]any{
		"id": float64(id), "what": what, "at": float64(at)}}
}

// 手机日历上的日程进**同一份待办表**.
//
//	当普通信号的话它只会进世界模型的"最新一条", 而日历的价值恰恰在
//	一批: "这周还有什么". 一条盖一条, 那批东西一件都留不下
func TestCalendarEventsBecomeTasks(t *testing.T) {
	a := NewAgenda(nil)
	at := time.Now().Add(3 * time.Hour).UnixMilli()
	a.Observe(cal(11, "季度评审", at))
	a.Observe(cal(12, "牙医", at+3600000))
	list := a.List("", false)
	if len(list) != 2 {
		t.Fatalf("该有 2 件, 有 %d 件", len(list))
	}
	if list[0].From != "手机日历" {
		t.Errorf("没标出来源 —— 模型会以为他能在这边划掉: %+v", list[0])
	}
}

// **每 8 分钟扫一遍, 没变的不许落账** —— 全落的话一天几百条,
// 而电量那条已经栽过一次了
func TestCalendarRescanDoesNotGrowTheLedger(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	a := NewAgenda(log)
	at := time.Now().Add(time.Hour).UnixMilli()
	for i := 0; i < 5; i++ {
		a.Observe(cal(11, "季度评审", at))
	}
	n := 0
	for _, e := range flatten(log) {
		if e.Kind == abi.EvTaskAdded {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一条日程落了 %d 次账, 该只有 1 次", n)
	}
	// 真的变了要落一条 —— 会议改时间是他要知道的事
	a.Observe(cal(11, "季度评审", at+1800000))
	n = 0
	for _, e := range flatten(log) {
		if e.Kind == abi.EvTaskAdded {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("改了时间却没落账(%d 条) —— 他不会知道会议挪了", n)
	}
}

// 划掉过的**不许被下一次扫描推回来**: 他划掉是有意的
func TestCalendarRescanDoesNotResurrect(t *testing.T) {
	a := NewAgenda(nil)
	at := time.Now().Add(time.Hour).UnixMilli()
	a.Observe(cal(11, "季度评审", at))
	list := a.List("", false)
	if len(list) != 1 {
		t.Fatal("没记上")
	}
	a.Done(list[0].ID)
	a.Observe(cal(11, "季度评审", at))
	if got := a.List("", false); len(got) != 0 {
		t.Fatalf("划掉的被扫描推回来了: %+v", got)
	}
}

// 日历的 id 是它自己的自增数 —— **不加前缀会跟这边的撞上**,
// 划掉一件会划掉另一件
func TestCalendarIDsDoNotCollide(t *testing.T) {
	a := NewAgenda(nil)
	mine, _ := a.Add("", "买牛奶", 0, "")
	a.Observe(cal(1, "季度评审", time.Now().Add(time.Hour).UnixMilli()))
	for _, x := range a.List("", false) {
		if x.ID == mine.ID && x.What != "买牛奶" {
			t.Fatal("日历那条盖掉了他自己记的那条")
		}
	}
	if len(a.List("", false)) != 2 {
		t.Fatal("两件事被当成了一件")
	}
}

// 来源要在给模型看的那份里标出来 —— 不标的话它会回一句"划掉了",
// 而那是他日历里的事, 这边划掉不会改那边
func TestAgendaTextMarksWhereItCameFrom(t *testing.T) {
	a := NewAgenda(nil)
	a.Observe(cal(11, "季度评审", time.Now().Add(time.Hour).UnixMilli()))
	if !strings.Contains(a.Text(""), "手机日历") {
		t.Fatalf("没标来源: %s", a.Text(""))
	}
}
