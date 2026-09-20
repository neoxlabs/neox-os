package osinit

import (
	"sync"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// **Append 返回的时候, 事件必须已经在文件里.**
//
// 原来是订阅 + 投递协程: Append 只把事件塞进队列, 另一个协程才写文件.
// 于是被杀的时候队尾那几条就没了 —— 而 S23 已经证明被杀是常态
// (开机扫出 199 个残留 socket).
//
// 丢的恰恰是**用户刚做的那件事**: 刚定的提醒、刚命名的地点、
// 刚取走的攒项. 表现是"我明明刚跟它说过" —— 查不出来.
//
// os.go 那段注释一直写着"落盘要在事件产生的那一刻做, 而不是退出时
// 统一写", 代码只是没做到.
func TestAppendIsOnDiskWhenItReturns(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/e.jsonl"
	store, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	log := NewEventLog(func() int64 { return 1 })
	log.WriteThrough(store)
	log.Append("sense", abi.EvWakeSet, map[string]any{"id": "w1", "text": "该吃药了"})

	// 不给任何喘息 —— 这里就是"下一刻被 kill"
	byPID, err := LoadEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(byPID["sense"]) != 1 {
		t.Fatal("Append 返回了, 文件里却没有 —— 这一刻被杀的话, " +
			"用户刚定的提醒就没了, 而且查不出来")
	}
}

// **盘上的顺序必须跟内存里一致.**
//
// 账本的语义就是顺序(重放靠它). 两个协程同时 Append 时, 如果落盘
// 发生在锁外, 盘上就可能是反的 —— 而重放出来的现状会跟着反:
// "设了又撤"变成"撤了又设", 那条提醒就复活了.
func TestDiskOrderMatchesMemoryOrder(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/e.jsonl"
	store, _ := OpenEventStore(p)
	defer store.Close()

	log := NewEventLog(func() int64 { return 1 })
	log.WriteThrough(store)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				log.Append(abi.ProcessID("p"), abi.EvInputRecv,
					map[string]any{"who": i, "n": j})
			}
		}(i)
	}
	wg.Wait()

	mem := log.Replay("p", 0)
	byPID, _ := LoadEvents(p)
	disk := byPID["p"]
	if len(disk) != len(mem) {
		t.Fatalf("盘上 %d 条, 内存里 %d 条", len(disk), len(mem))
	}
	for i := range mem {
		if disk[i].Seq != mem[i].Seq {
			t.Fatalf("第 %d 条对不上: 盘上 seq=%d, 内存 seq=%d —— "+
				"重放出来的顺序会跟当时发生的不一样", i, disk[i].Seq, mem[i].Seq)
		}
	}
}

// 没接落盘的时候一切照旧 —— 大量测试和 --sense-simday 都是纯内存跑的
func TestAppendWorksWithoutStore(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	log.Append("p", abi.EvInputRecv, map[string]any{"text": "话"})
	if len(log.Replay("p", 0)) != 1 {
		t.Fatal("没接落盘的时候 Append 坏了")
	}
}
