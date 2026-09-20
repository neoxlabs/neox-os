package osinit

import (
	"sync"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func clock() func() int64 {
	var t int64 = 1_000_000
	return func() int64 { t++; return t }
}

func TestSeqIsMonotonicPerProcess(t *testing.T) {
	l := NewEventLog(clock())
	for i := 0; i < 5; i++ {
		l.Append("p1", abi.EvProcOutput, i)
	}
	l.Append("p2", abi.EvProcOutput, "别的进程")
	evs := l.Replay("p1", 0)
	for i, e := range evs {
		if e.Seq != i {
			t.Fatalf("seq 必须连续无洞: 第 %d 条 seq=%d", i, e.Seq)
		}
	}
	// 每个进程独立编号 —— p2 的第一条也应该是 0
	if l.Replay("p2", 0)[0].Seq != 0 {
		t.Fatal("seq 应当是 per-process 的")
	}
}

// 迟到的订阅者必须补齐全部历史 —— 零断层
func TestLateSubscriberGetsFullHistory(t *testing.T) {
	l := NewEventLog(clock())
	for i := 0; i < 3; i++ {
		l.Append("p1", abi.EvProcOutput, i)
	}
	var mu sync.Mutex
	var got []int
	stop := l.Subscribe("p1", 0, func(e abi.Event) { mu.Lock(); got = append(got, e.Seq); mu.Unlock() })
	defer stop()
	waitLen(t, &mu, &got, 3)
	if got[0] != 0 || got[2] != 2 {
		t.Fatalf("补齐不完整: %v", got)
	}
	l.Append("p1", abi.EvProcOutput, "实时")
	waitLen(t, &mu, &got, 4)
	if got[3] != 3 {
		t.Fatalf("实时没接上: %v", got)
	}
}

func TestSubscribeFromSeq(t *testing.T) {
	l := NewEventLog(clock())
	for i := 0; i < 5; i++ {
		l.Append("p1", abi.EvProcOutput, i)
	}
	var mu sync.Mutex
	var got []int
	stop := l.Subscribe("p1", 3, func(e abi.Event) { mu.Lock(); got = append(got, e.Seq); mu.Unlock() })
	defer stop()
	waitLen(t, &mu, &got, 2)
	if got[0] != 3 {
		t.Fatalf("从 seq=3 起补齐应得 [3 4], got %v", got)
	}
}

// 补齐期间产生的事件必须排在历史之后, 不重不漏不乱序
func TestNoGapNoDupUnderConcurrentAppend(t *testing.T) {
	l := NewEventLog(clock())
	for i := 0; i < 50; i++ {
		l.Append("p1", abi.EvProcOutput, i)
	}

	var mu sync.Mutex
	var got []int
	var wg sync.WaitGroup
	wg.Add(1)
	// 一边订阅一边猛写 —— 这是最容易暴露乱序的场景
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			l.Append("p1", abi.EvProcOutput, i)
		}
	}()
	stop := l.Subscribe("p1", 0, func(e abi.Event) {
		mu.Lock()
		got = append(got, e.Seq)
		mu.Unlock()
	})
	wg.Wait()
	time.Sleep(20 * time.Millisecond)
	stop()

	mu.Lock()
	defer mu.Unlock()
	seen := map[int]bool{}
	last := -1
	for _, s := range got {
		if seen[s] {
			t.Fatalf("重复 seq=%d", s)
		}
		seen[s] = true
		if s <= last {
			t.Fatalf("乱序: %d 出现在 %d 之后", s, last)
		}
		last = s
	}
	if len(got) < 50 {
		t.Fatalf("至少要补齐 50 条历史, got %d", len(got))
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	l := NewEventLog(clock())
	var mu sync.Mutex
	var got []int
	stop := l.Subscribe("p1", 0, func(abi.Event) { mu.Lock(); got = append(got, 1); mu.Unlock() })
	l.Append("p1", abi.EvProcOutput, 1)
	waitLen(t, &mu, &got, 1)
	stop()
	l.Append("p1", abi.EvProcOutput, 2)
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("退订后不该再收到, n=%d", n)
	}
}

func TestSubscribeAllCrossesProcesses(t *testing.T) {
	l := NewEventLog(clock())
	var mu sync.Mutex
	var pids []abi.ProcessID
	stop := l.SubscribeAll(func(e abi.Event) { mu.Lock(); pids = append(pids, e.PID); mu.Unlock() })
	defer stop()
	l.Append("p1", abi.EvProcOutput, 1)
	l.Append("p2", abi.EvProcOutput, 2)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(pids)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pids) != 2 || pids[0] != "p1" || pids[1] != "p2" {
		t.Fatalf("触达层要看得到所有进程: %v", pids)
	}
}

// 订阅者回调里再调日志不能死锁 —— 派发时不持锁
func TestCallbackMayCallLog(t *testing.T) {
	l := NewEventLog(clock())
	done := make(chan struct{})
	stop := l.SubscribeAll(func(e abi.Event) {
		if e.Kind == abi.EvProcOutput {
			l.Length(e.PID) // 回调里读日志
			close(done)
		}
	})
	defer stop()
	l.Append("p1", abi.EvProcOutput, 1)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("回调里调日志死锁了")
	}
}

func TestSnapshotRestore(t *testing.T) {
	l := NewEventLog(clock())
	l.Append("p1", abi.EvProcOutput, "a")
	l.Append("p1", abi.EvProcOutput, "b")
	snap := l.Snapshot()

	l2 := NewEventLog(clock())
	l2.Restore(snap)
	if l2.Length("p1") != 2 {
		t.Fatalf("恢复后长度 = %d", l2.Length("p1"))
	}
	// 恢复出来的日志仍能被订阅并补齐
	var mu sync.Mutex
	var got []int
	stop := l2.Subscribe("p1", 0, func(e abi.Event) { mu.Lock(); got = append(got, e.Seq); mu.Unlock() })
	defer stop()
	waitLen(t, &mu, &got, 2)
}

// waitLen 等异步投递协程把量投够 —— 投递现在是有序但异步的
func waitLen[T any](t *testing.T, mu *sync.Mutex, got *[]T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	n := len(*got)
	mu.Unlock()
	t.Fatalf("等 %d 条超时, 只收到 %d 条", want, n)
}
