package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func dupBus(t *testing.T, log *EventLog) (*SignalBus, *[]Digest) {
	t.Helper()
	var got []Digest
	bus := NewSignalBus(log, func(d Digest) { got = append(got, d) }, SignalOptions{
		Window: 50 * time.Millisecond, Lateness: 10 * time.Millisecond,
		BackfillQuiet: 20 * time.Millisecond,
	})
	return bus, &got
}

func countSignals(log *EventLog) int {
	n := 0
	for _, e := range log.Replay(signalPID, 0) {
		if e.Kind == abi.EvSignal {
			n++
		}
	}
	return n
}

// **捞回来的信号在账本里又记了一遍.**
//
// 60 条独立信号可能在账本里留下 150 条记录 ——
// 同一条出现了 3 次, 每重启一次就多一份.
//
// 最重的后果不是占地方, 是**报告会说谎**: 报告是校准唯一的依据,
// 而它把 60 条数成 150 条. 拿虚高 2.5 倍的数字去调阈值,
// 比不调更糟 —— 你会以为这个种类吵得要命, 然后把它静掉.
func TestRecoveredSignalsAreNotRecordedAgain(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	bus, _ := dupBus(t, log)
	now := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		bus.Ingest(abi.Signal{ID: string(rune('a' + i)), Source: "phone.mk",
			Kind: "phone.moved", At: now, KnownAt: now})
	}
	before := countSignals(log)
	if before != 5 {
		t.Fatalf("灌之后账本里 %d 条", before)
	}

	// 重启: 新总线, 拿同一份账本捞
	bus2, _ := dupBus(t, log)
	bus2.RecoverPending(log.Replay(signalPID, 0))
	if after := countSignals(log); after != before {
		t.Fatalf("捞回来之后账本里 %d 条, 原来 %d 条 —— 每重启一次就多一份, "+
			"而报告是靠这个数算的", after, before)
	}
}

// **但捞回来的还是要被总结** —— 否则 S16 那整件事就白做了.
//
// 这条是上面那条的对照: 如果用"去重挡住"的办法去修重复记账,
// 捞回来的信号会被判成 duplicate, 于是"捞回上次没来得及总结的 N 条"
// 变成一句空话, 而且没有任何报错.
func TestRecoveredSignalsStillGetSummarized(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	bus, _ := dupBus(t, log)
	now := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		bus.Ingest(abi.Signal{ID: string(rune('a' + i)), Source: "phone.mk",
			Kind: "phone.moved", At: now, KnownAt: now})
	}

	bus2, got2 := dupBus(t, log)
	n := bus2.RecoverPending(log.Replay(signalPID, 0))
	if n != 5 {
		t.Fatalf("捞回来 %d 条, 该是 5 条", n)
	}
	time.Sleep(120 * time.Millisecond)
	bus2.Tick()
	total := 0
	for _, d := range *got2 {
		total += d.Count
	}
	if total != 5 {
		t.Fatalf("捞回来的 5 条只总结出 %d 条 —— 它们又一次没被送到任何人面前", total)
	}
}

// **去重表要活过重启.**
//
// "断网补发是常态"是去重存在的全部理由(注释里就这么写的). 而我们
// 自己一重启, 那张表就空了 —— 采集端此刻重传的那一批会被当成新的:
// 账本里多一份, 摘要里多一遍, 用户听到两次"你到公司了".
func TestDedupSurvivesRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	bus, _ := dupBus(t, log)
	now := time.Now().UnixMilli()
	s := abi.Signal{ID: "same-one", Source: "phone.mk", Kind: "phone.arrived",
		At: now, KnownAt: now}
	bus.Ingest(s)

	bus2, _ := dupBus(t, log)
	bus2.RestoreSeen(log.Replay(signalPID, 0))
	if v := bus2.Ingest(s); v != IngestDuplicate {
		t.Fatalf("重启之后采集端重传同一条, 判成了 %s —— "+
			"用户会听到两次同一件事", v)
	}
}

// 恢复去重表要守同一个保留期 —— 否则一份跑了一年的账本会把
// 几十万个幂等键全装进内存, 而它们早就没用了
func TestDedupRestoreRespectsRetention(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	old := time.Now().Add(-2 * dedupRetention).UnixMilli()
	log.Append(signalPID, abi.EvSignal, map[string]any{
		"id": "ancient", "source": "phone.mk", "kind": "phone.moved",
		"at": float64(old), "knownAt": float64(old)})

	bus, _ := dupBus(t, log)
	bus.RestoreSeen(log.Replay(signalPID, 0))
	if bus.SeenSize() != 0 {
		t.Fatalf("把 %d 个过期的幂等键装回了内存", bus.SeenSize())
	}
}
