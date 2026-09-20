package osinit

import (
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func evAt(kind abi.EventKind, at int64, p map[string]any) abi.Event {
	return abi.Event{PID: signalPID, At: at, Kind: kind, Payload: p}
}

func kinds(evs []abi.Event) map[abi.EventKind]int {
	m := map[abi.EventKind]int{}
	for _, e := range evs {
		m[e.Kind]++
	}
	return m
}

// 响过/撤过的闹钟, set 和它的终止事件成对丢掉 —— 那对事件对"现在还剩什么"
// 已经没有贡献, 却要在每次开机时被读一遍
func TestCompactDropsDeadWakePairs(t *testing.T) {
	in := []abi.Event{
		evAt(abi.EvWakeSet, 1, map[string]any{"id": "w1", "text": "吃药"}),
		evAt(abi.EvWakeFired, 2, map[string]any{"id": "w1"}),
		evAt(abi.EvWakeSet, 3, map[string]any{"id": "w2", "text": "开会"}),
		evAt(abi.EvWakeCancelled, 4, map[string]any{"id": "w2"}),
		evAt(abi.EvWakeSet, 5, map[string]any{"id": "w3", "text": "还没响的"}),
	}
	keep, drop := compactSense(in)
	if len(drop) != 4 {
		t.Fatalf("丢了 %d 条, 该丢 4 条(两对死的)", len(drop))
	}
	if len(keep) != 1 || keep[0].Payload.(map[string]any)["id"] != "w3" {
		t.Fatalf("留下的不是还活着的那个: %+v", keep)
	}
}

// **压紧之后 id 计数器绝不能退回去.**
//
// seqOf/watchSeqOf 是从**幸存事件的 id** 反推计数器的. 把 w1..w9 都
// 压掉之后计数器归零, 下一个提醒又叫 w1 —— 而 w1 在归档里还活着.
// 那就是 S18 那个"撤销撤错对象"从另一扇门回来了, 而且更隐蔽:
// 用户撤 w1 撤掉的可能是另一件事, 要到该响时才发现.
func TestCompactNeverRegressesIDCounter(t *testing.T) {
	var in []abi.Event
	for i := 1; i <= 9; i++ {
		id := string(rune('0' + i))
		in = append(in,
			evAt(abi.EvWakeSet, int64(i*2), map[string]any{"id": "w" + id}),
			evAt(abi.EvWakeFired, int64(i*2+1), map[string]any{"id": "w" + id}))
	}
	keep, _ := compactSense(in)

	tm := NewTimers(nil, nil, nil)
	tm.Restore(keep)
	if got := tm.seq; got < 9 {
		t.Fatalf("压紧之后计数器退到 %d —— 下一个提醒会叫 w%d, "+
			"而那个 id 在归档里已经用过了", got, got+1)
	}
	// 而且那条被留下来占位的必须是**死的**, 不能把已经响过的闹钟复活
	if len(tm.Pending()) != 0 {
		t.Fatalf("已经响过的闹钟被复活了: %+v", tm.Pending())
	}
}

// 关注这边同一条 —— g%d 用的是另一个计数器
func TestCompactNeverRegressesWatchCounter(t *testing.T) {
	in := []abi.Event{
		evAt(abi.EvWatchSet, 1, map[string]any{"id": "g1", "kind": "place.left", "place": "公司"}),
		evAt(abi.EvWatchRemoved, 2, map[string]any{"id": "g1"}),
		evAt(abi.EvWatchSet, 3, map[string]any{"id": "g2", "kind": "door.opened"}),
		evAt(abi.EvWatchRemoved, 4, map[string]any{"id": "g2"}),
	}
	keep, _ := compactSense(in)
	w := NewWatches(nil)
	w.Restore(keep)
	if w.seq < 2 {
		t.Fatalf("关注计数器退到 %d —— 下一个关注会叫 g%d, 归档里已经有了", w.seq, w.seq+1)
	}
	if len(w.List()) != 0 {
		t.Fatalf("撤掉的关注被复活了: %+v", w.List())
	}
}

// 地点一条都不许动 —— 它是所有位置信号的语义锚点,
// 丢一个的后果是"到达/离开"重新变成一串经纬度, 模型判不出任何东西
func TestCompactKeepsAllPlaces(t *testing.T) {
	in := []abi.Event{
		evAt(abi.EvPlaceNamed, 1, map[string]any{"name": "家"}),
		evAt(abi.EvPlaceNamed, 2, map[string]any{"name": "公司"}),
	}
	keep, drop := compactSense(in)
	if len(drop) != 0 || len(keep) != 2 {
		t.Fatalf("地点被压掉了: 留 %d 丢 %d", len(keep), len(drop))
	}
}

// 只留最后一份摘要 + 它之后的信号 —— RecoverPending 就是从最后一份摘要
// 往后捞的. 更早的信号已经被总结过, 再捞回来等于把旧消息重播一遍
func TestCompactKeepsLastDigestAndLaterSignals(t *testing.T) {
	in := []abi.Event{
		evAt(abi.EvSignal, 1, map[string]any{"id": "s1", "kind": "phone.arrived"}),
		evAt(abi.EvSignalDigest, 2, map[string]any{"n": 1}),
		evAt(abi.EvSignal, 3, map[string]any{"id": "s2", "kind": "phone.left"}),
		evAt(abi.EvSignalDigest, 4, map[string]any{"n": 1}),
		evAt(abi.EvSignal, 5, map[string]any{"id": "s3", "kind": "door.opened"}),
	}
	keep, _ := compactSense(in)
	k := kinds(keep)
	if k[abi.EvSignalDigest] != 1 {
		t.Fatalf("留了 %d 份摘要, 该只留最后一份", k[abi.EvSignalDigest])
	}
	if k[abi.EvSignal] != 1 {
		t.Fatalf("留了 %d 条信号, 该只留最后一份摘要之后的那 1 条", k[abi.EvSignal])
	}
	if keep[len(keep)-1].Payload.(map[string]any)["id"] != "s3" {
		t.Fatalf("留错了信号: %+v", keep)
	}
}

// 日报只认最后一天 —— Restore 就是拿它判"今天发过没有"
func TestCompactKeepsLastDailyReport(t *testing.T) {
	in := []abi.Event{
		evAt(abi.EvDailyReport, 1, map[string]any{"day": "2026-08-14"}),
		evAt(abi.EvDailyReport, 2, map[string]any{"day": "2026-08-15"}),
	}
	keep, _ := compactSense(in)
	if len(keep) != 1 {
		t.Fatalf("留了 %d 份日报", len(keep))
	}
	d := NewDailyReport(nil, nil, 21, nil, nil)
	d.Restore(keep)
	if d.lastDay != "2026-08-15" {
		t.Fatalf("最后一天认成了 %q —— 会重发一份今天已经发过的日报", d.lastDay)
	}
}

// **压紧前后, 重建出来的现状必须一模一样.** 这是压紧唯一的正确性判据:
// 它允许少读事件, 不允许改变结论
func TestCompactPreservesRestoredState(t *testing.T) {
	in := []abi.Event{
		evAt(abi.EvPlaceNamed, 1, map[string]any{"name": "家", "lat": 31.88, "lon": 117.28, "radius": 150.0}),
		evAt(abi.EvWakeSet, 2, map[string]any{"id": "w1", "text": "旧的"}),
		evAt(abi.EvWakeFired, 3, map[string]any{"id": "w1"}),
		evAt(abi.EvWakeSet, 4, map[string]any{"id": "w2", "text": "还在", "at": float64(1 << 40)}),
		evAt(abi.EvWatchSet, 5, map[string]any{"id": "g1", "kind": "place.left", "place": "家"}),
		evAt(abi.EvWatchSet, 6, map[string]any{"id": "g2", "kind": "door.opened"}),
		evAt(abi.EvWatchRemoved, 7, map[string]any{"id": "g2"}),
	}
	restore := func(evs []abi.Event) LedgerState {
		st, _ := CheckLedger(evs)
		return st
	}
	full, comp := restore(in), func() LedgerState {
		keep, _ := compactSense(in)
		return restore(keep)
	}()
	if len(full.Wakes) != len(comp.Wakes) || len(full.Watches) != len(comp.Watches) ||
		len(full.Places) != len(comp.Places) {
		t.Fatalf("压紧改变了重建出来的现状:\n完整 %+v\n压紧 %+v", full, comp)
	}
	for i := range full.Wakes {
		if full.Wakes[i] != comp.Wakes[i] {
			t.Fatalf("闹钟对不上: %v vs %v", full.Wakes, comp.Wakes)
		}
	}
}

// 压紧之后账本自己还得自洽 —— 只丢 set 不丢终止事件的话,
// CheckLedger 会判"撤了一个从来没设过的"
func TestCompactedLedgerStaysConsistent(t *testing.T) {
	in := []abi.Event{
		evAt(abi.EvWakeSet, 1, map[string]any{"id": "w1"}),
		evAt(abi.EvWakeCancelled, 2, map[string]any{"id": "w1"}),
		evAt(abi.EvWatchSet, 3, map[string]any{"id": "g1"}),
		evAt(abi.EvWatchRemoved, 4, map[string]any{"id": "g1"}),
	}
	keep, _ := compactSense(in)
	if _, probs := CheckLedger(keep); len(probs) != 0 {
		t.Fatalf("压紧之后账本不自洽了: %v", probs)
	}
}

// **很少聊天的机器也要能压紧.**
//
// 挂在对话轮转上是不够的: 轮转的门槛是"对话超过 20 段", 而一台主要在
// 感知、很少聊天的机器永远到不了那个门槛 —— 那恰恰是这个系统最终要跑的
// 样子(它替你盯着, 你偶尔说句话). 于是 sense 一路长到几万条.
func TestCompactRunsEvenWithFewConversations(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/e.jsonl"
	st, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	// 两段对话 —— 远不到 20 段
	for c := 0; c < 2; c++ {
		st.Append(abi.Event{Seq: 0, PID: abi.ProcessID("p" + string(rune('a'+c))),
			At: 9_000_000, Kind: abi.EvInputRecv, Payload: map[string]any{"text": "话"}})
	}
	// sense 上一堆已经被总结过的信号 + 摘要
	for i := 0; i < 6000; i++ {
		st.Append(evAt(abi.EvSignal, int64(i), map[string]any{
			"id": "s" + string(rune(i)), "kind": "phone.moved"}))
	}
	st.Append(evAt(abi.EvSignalDigest, 7000, map[string]any{"n": 6000}))
	// 时间要给未来的 —— 给 0 的话 Restore 之后那一遍 Tick 会当场把它
	// 当成过期的补响掉, 于是 Pending 是空的(这不是压紧的问题)
	st.Append(evAt(abi.EvWakeSet, 7001, map[string]any{
		"id": "w1", "text": "还在", "at": float64(1 << 42)}))
	st.Close()

	if _, err := rotateIfNeeded(p, 20); err != nil {
		t.Fatal(err)
	}
	left, _ := LoadEvents(p)
	if n := len(left[signalPID]); n > 10 {
		t.Fatalf("sense 还剩 %d 条 —— 压紧没跑, 因为对话不够 20 段", n)
	}
	// **对话一段都不许动** —— sense 变大时只能归档 sense, 不能归档对话内容
	if len(left) != 3 {
		t.Fatalf("活跃账本剩 %d 段(含 sense), 期望 3 —— 用户的对话被顺手归档了", len(left))
	}
	// 压掉的必须能在归档里找回来 —— 红线②
	arch, _ := LoadEvents(p + ".archive")
	if len(arch[signalPID]) < 6000 {
		t.Fatalf("归档里只有 %d 条 —— 压掉的信号被真删了, 调阈值的依据没了",
			len(arch[signalPID]))
	}
	// 还活着的提醒必须还在
	tm := NewTimers(nil, nil, nil)
	tm.Restore(left[signalPID])
	if len(tm.Pending()) != 1 {
		t.Fatalf("压紧之后还剩 %d 个提醒, 期望 1", len(tm.Pending()))
	}
}
