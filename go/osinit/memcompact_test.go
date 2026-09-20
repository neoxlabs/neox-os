package osinit

import (
	"fmt"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func fillSense(l *EventLog, n int) {
	for i := 0; i < n; i++ {
		l.Append(signalPID, abi.EvSignal, map[string]any{
			"id": fmt.Sprintf("s%d", i), "kind": "phone.moved"})
	}
	l.Append(signalPID, abi.EvSignalDigest, map[string]any{"n": n})
}

// **磁盘压紧了, 内存没有.**
//
// EventStore 压紧的是文件, 而 EventLog.logs 那张 map 不会随之压紧.
// 2500 条合法信号进来时, 宿主 RSS 会永久涨 6MB —— 一台跑一年的
// 机器, 内存里躺着的是**全部历史**, 而其中绝大多数对"现在还剩什么"
// 已经没有贡献(响过的闹钟、上上周的摘要).
func TestCompactSenseFreesMemory(t *testing.T) {
	// 走**开机装载**那条路: Restore 不经过 Append, 于是不会顺手压紧 ——
	// 一份 6000 条的账本读回来, 内存里就是 6000 条
	var snap []abi.Event
	for i := 0; i < 6000; i++ {
		snap = append(snap, abi.Event{Seq: i, PID: signalPID, Kind: abi.EvSignal,
			Payload: map[string]any{"id": fmt.Sprintf("s%d", i), "kind": "phone.moved"}})
	}
	snap = append(snap,
		abi.Event{Seq: 6000, PID: signalPID, Kind: abi.EvSignalDigest,
			Payload: map[string]any{"n": 6000}},
		// at 要给未来的 —— 给 0 的话 Restore 之后那一遍 Tick 会当场补响
		abi.Event{Seq: 6001, PID: signalPID, Kind: abi.EvWakeSet,
			Payload: map[string]any{"id": "w1", "text": "还活着的", "at": float64(1 << 42)}})

	l := NewEventLog(func() int64 { return 1 })
	l.Restore(map[abi.ProcessID][]abi.Event{signalPID: snap})

	before := l.Length(signalPID)
	l.CompactSense()
	after := l.Length(signalPID)
	if after >= before/10 {
		t.Fatalf("压紧前 %d 条, 压紧后 %d 条 —— 几乎没压", before, after)
	}
	// 活着的状态一条不能少 —— 判据跟磁盘那边同一条:
	// 允许少读事件, 不允许改变结论
	tm := NewTimers(nil, nil, nil)
	tm.Restore(l.Replay(signalPID, 0))
	if len(tm.Pending()) != 1 {
		t.Fatalf("压紧之后还剩 %d 个提醒, 期望 1", len(tm.Pending()))
	}
}

// **压紧之后 seq 不许重复.**
//
// Append 里 seq 是 `len(log)` 算出来的 —— 一压紧, 下一条的 seq 就退回去,
// 跟一条早就存在的事件撞上. 而 Replay(pid, fromSeq) 是靠 seq 定位的:
// 订阅方拿着旧 seq 回来续, 会**跳过或者重放一批**, 而它自己不会知道.
//
// 这跟 S25 那个"id 计数器退回去"是同一类错的第二次出现 ——
// 凡是从"现存内容"反推出来的计数器, 压紧都会把它打回去.
func TestSeqNeverRepeatsAfterCompact(t *testing.T) {
	l := NewEventLog(func() int64 { return 1 })
	fillSense(l, 6000)
	seen := map[int]bool{}
	for _, e := range l.Replay(signalPID, 0) {
		seen[e.Seq] = true
	}
	l.CompactSense()
	ev := l.Append(signalPID, abi.EvWakeSet, map[string]any{"id": "w1"})
	if seen[ev.Seq] {
		t.Fatalf("压紧之后新事件的 seq=%d 跟一条老事件撞了 —— "+
			"拿旧 seq 回来续的订阅方会跳过或者重放一批, 而它不会知道", ev.Seq)
	}
}

// 拿压紧之前的 seq 回来续, 必须还能对上 —— 不能因为压紧就返回一堆无关的
func TestReplayFromSeqSurvivesCompact(t *testing.T) {
	l := NewEventLog(func() int64 { return 1 })
	fillSense(l, 6000)
	mark := l.Append(signalPID, abi.EvWakeSet, map[string]any{"id": "w1"}).Seq
	l.Append(signalPID, abi.EvPlaceNamed, map[string]any{"name": "家"})
	l.CompactSense()

	got := l.Replay(signalPID, mark+1)
	if len(got) != 1 || got[0].Kind != abi.EvPlaceNamed {
		t.Fatalf("从 seq=%d 往后续, 拿到 %d 条 %v —— 该是那一条 place.named",
			mark+1, len(got), kindsOf(got))
	}
}

// **对话一条都不许动.**
//
// /继续 要的是那段对话的**完整**历史. 从中间砍掉几条的话, 恢复出来
// "看着对但其实不对" —— 那比没有历史更糟(磁盘轮转的红线① 同一条).
func TestCompactLeavesConversationsAlone(t *testing.T) {
	l := NewEventLog(func() int64 { return 1 })
	for i := 0; i < 200; i++ {
		l.Append("p1.1.1", abi.EvInputRecv, map[string]any{"text": "话"})
	}
	fillSense(l, 6000)
	l.CompactSense()
	if n := l.Length("p1.1.1"); n != 200 {
		t.Fatalf("对话被压掉了, 剩 %d 条 —— /继续 会接到一段残缺的历史", n)
	}
}

func kindsOf(evs []abi.Event) []abi.EventKind {
	out := make([]abi.EventKind, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// **长跑里没人会来调 CompactSense —— 它必须自己发生.**
//
// 放在开机时做等于没做: 内存是在**跑的过程中**涨起来的, 而这台机器
// 可以几个月不重启(那正是它该有的样子).
func TestSenseCompactsItselfWhileRunning(t *testing.T) {
	l := NewEventLog(func() int64 { return 1 })
	peak := 0
	for i := 0; i < 40000; i++ {
		l.Append(signalPID, abi.EvSignal, map[string]any{
			"id": fmt.Sprintf("s%d", i), "kind": "phone.moved"})
		if i%500 == 0 {
			l.Append(signalPID, abi.EvSignalDigest, map[string]any{"n": 500})
		}
		if n := l.Length(signalPID); n > peak {
			peak = n
		}
	}
	// 没人调过 CompactSense, 它自己就该压
	if peak > 2*compactAfterSenseEvents {
		t.Fatalf("跑了四万条, 内存里峰值 %d 条 —— 它没自己压, "+
			"而这台机器可以几个月不重启", peak)
	}
	if l.Length(signalPID) > compactAfterSenseEvents {
		t.Fatalf("跑完还剩 %d 条", l.Length(signalPID))
	}
}
