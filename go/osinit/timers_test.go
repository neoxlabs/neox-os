package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

type fired struct {
	w      Wake
	lateMs int64
}

func newTimers(t *testing.T) (*Timers, *fakeClock, *[]fired, *EventLog) {
	t.Helper()
	clk := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
	log := NewEventLog(func() int64 { return clk.now().UnixMilli() })
	var got []fired
	tm := NewTimers(log, clk.now, func(w Wake, late int64) {
		got = append(got, fired{w, late})
	})
	return tm, clk, &got, log
}

// 到点才响, 没到点不响
func TestWakeFiresAtTime(t *testing.T) {
	tm, clk, got, _ := newTimers(t)
	at := clk.now().Add(10 * time.Minute).UnixMilli()
	if _, err := tm.Set("th1", at, "12 点的火车该出门了"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		clk.advance(time.Minute)
		tm.Tick()
	}
	if len(*got) != 0 {
		t.Fatalf("没到点就响了: %+v", *got)
	}
	clk.advance(2 * time.Minute)
	tm.Tick()
	if len(*got) != 1 || (*got)[0].w.Text != "12 点的火车该出门了" {
		t.Fatalf("到点没响: %+v", *got)
	}
	if len(tm.Pending()) != 0 {
		t.Fatal("响过的还留在待响列表里")
	}
}

// **过去的时间点要当场拒.**
//
// 模型算错时间是常有的事(时区、24 小时制、"明天"). 静默收下一个过去的
// 时刻, 表现出来就是"闹钟没响" —— 而那是最糟的失败方式.
func TestWakeRejectsPastTime(t *testing.T) {
	tm, clk, _, _ := newTimers(t)
	_, err := tm.Set("th1", clk.now().Add(-time.Minute).UnixMilli(), "早就过了")
	if err == nil {
		t.Fatal("收下了一个过去的时刻 —— 它永远不会响, 而用户以为设好了")
	}
	if _, err := tm.Set("th1", clk.now().UnixMilli(), ""); err == nil {
		t.Fatal("空的提醒内容该被拒: 到点了说什么?")
	}
}

// **闹钟必须活得比进程长.**
//
// 挂在进程里的闹钟跟进程同生共死, 而**用户不会知道它没了** ——
// 他会在 12 点错过火车, 然后才发现"你不是说好提醒我吗".
func TestWakeSurvivesRestart(t *testing.T) {
	tm, clk, _, log := newTimers(t)
	at := clk.now().Add(time.Hour).UnixMilli()
	tm.Set("th1", at, "该吃药了")
	tm.Set("th1", clk.now().Add(2*time.Hour).UnixMilli(), "该睡了")
	tm.Cancel(tm.Pending()[1].ID) // 撤掉第二个

	// 机器重启: 新的 Timers, 从事件日志装回来
	clk2 := &fakeClock{t: clk.now()}
	var got2 []fired
	tm2 := NewTimers(nil, clk2.now, func(w Wake, late int64) { got2 = append(got2, fired{w, late}) })
	tm2.Restore(log.Replay(signalPID, 0))

	if len(tm2.Pending()) != 1 {
		t.Fatalf("重启后剩下 %d 个闹钟, 该是 1 个(另一个撤了)", len(tm2.Pending()))
	}
	if tm2.Pending()[0].Text != "该吃药了" {
		t.Fatalf("装回来的闹钟不对: %+v", tm2.Pending()[0])
	}
	clk2.advance(2 * time.Hour)
	tm2.Tick()
	if len(got2) != 1 {
		t.Fatal("重启之后闹钟不响了 —— 用户会错过它, 而且不会知道为什么")
	}
}

// **错过的闹钟要补响, 而且要说清晚了多久.**
//
// 机器可能关着、可能在升级. 11 点该响的闹钟如果 11:30 开机时因为
// "已经过期"被丢掉, 用户就再也不会知道它存在过 —— 而他设它正是怕自己忘.
// 晚响比不响强, 但必须说清晚了多久: 他要据此判断还来不来得及.
func TestMissedWakeFiresLateWithHowLate(t *testing.T) {
	tm, clk, _, log := newTimers(t)
	at := clk.now().Add(10 * time.Minute).UnixMilli()
	tm.Set("th1", at, "11 点该出门了")

	// 机器关了两小时才开
	clk2 := &fakeClock{t: clk.now().Add(2 * time.Hour)}
	var got2 []fired
	tm2 := NewTimers(nil, clk2.now, func(w Wake, late int64) { got2 = append(got2, fired{w, late}) })
	tm2.Restore(log.Replay(signalPID, 0))

	if len(got2) != 1 {
		t.Fatal("错过的闹钟被吞掉了 —— 用户永远不会知道它存在过")
	}
	wantLate := (2*time.Hour - 10*time.Minute).Milliseconds()
	if got2[0].lateMs != wantLate {
		t.Fatalf("晚了多久算错了: %d, 期望 %d —— 用户要据此判断还来不来得及",
			got2[0].lateMs, wantLate)
	}
}

// 同时到期的按它们本该响的顺序说
func TestSimultaneousWakesFireInOrder(t *testing.T) {
	tm, clk, got, _ := newTimers(t)
	tm.Set("th1", clk.now().Add(3*time.Minute).UnixMilli(), "第三")
	tm.Set("th1", clk.now().Add(time.Minute).UnixMilli(), "第一")
	tm.Set("th1", clk.now().Add(2*time.Minute).UnixMilli(), "第二")
	clk.advance(10 * time.Minute)
	tm.Tick()
	if len(*got) != 3 {
		t.Fatalf("该响 3 个, 实际 %d", len(*got))
	}
	for i, want := range []string{"第一", "第二", "第三"} {
		if (*got)[i].w.Text != want {
			t.Fatalf("第 %d 个是 %q, 该是 %q", i, (*got)[i].w.Text, want)
		}
	}
}

// 重启之后 ID 不能撞 —— 撞了的话新闹钟会把旧的顶掉
func TestSeqSurvivesRestore(t *testing.T) {
	tm, clk, _, log := newTimers(t)
	tm.Set("th1", clk.now().Add(time.Hour).UnixMilli(), "一")
	tm.Set("th1", clk.now().Add(2*time.Hour).UnixMilli(), "二")

	tm2 := NewTimers(nil, clk.now, nil)
	tm2.Restore(log.Replay(signalPID, 0))
	w, err := tm2.Set("th1", clk.now().Add(3*time.Hour).UnixMilli(), "三")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tm2.Pending() {
		if p.ID == w.ID && p.Text != "三" {
			t.Fatalf("新闹钟的 ID %s 跟旧的撞了, 把它顶掉了", w.ID)
		}
	}
	if len(tm2.Pending()) != 3 {
		t.Fatalf("该有 3 个闹钟, 实际 %d —— ID 撞了", len(tm2.Pending()))
	}
}

// 事件日志里的时间戳过一趟 JSON 会变 float64 —— 因此恢复时必须兼容该类型
func TestRestoreHandlesJSONNumbers(t *testing.T) {
	w := wakeFromPayload(map[string]any{
		"id": "w1", "thread": "th", "text": "x",
		"at": float64(1_700_000_060_000), "setAt": float64(1_700_000_000_000),
	})
	if w.At != 1_700_000_060_000 {
		t.Fatalf("过了 JSON 的时间戳读错了: %d", w.At)
	}
}

// 事件种类要对 —— 靠它才能从日志重建
func TestWakeEventsAreRecorded(t *testing.T) {
	tm, clk, _, log := newTimers(t)
	w, _ := tm.Set("th1", clk.now().Add(time.Minute).UnixMilli(), "x")
	tm.Cancel(w.ID)
	kinds := map[abi.EventKind]int{}
	for _, e := range log.Replay(signalPID, 0) {
		kinds[e.Kind]++
	}
	if kinds[abi.EvWakeSet] != 1 || kinds[abi.EvWakeCancelled] != 1 {
		t.Fatalf("事件没记全: %v", kinds)
	}
}
