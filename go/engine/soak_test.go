package engine

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// 长跑与并发压测.
//
// 单项回收逻辑早有测试, 但**多个 agent 进程同时跑很久**的行为一直没验过 ——
// 而那正是引用计数、去重、回收这类东西最容易"看着对其实不对"的地方.
// 单进程顺序跑不出来的错, 并发下会露出来.

// 多个空间并发读写同一个页存储, 活页一个都不许丢.
//
// 这是最要命的一条: 回收多回收一页, 表现是模型突然"忘了"自己读过什么,
// 而且没有任何报错 —— 只会看到它莫名其妙重做一遍.
func TestConcurrentSpacesNeverLoseLivePages(t *testing.T) {
	store := NewPageStore()
	const spaces, appends = 12, 200

	var wg sync.WaitGroup
	ids := make([][]PageID, spaces)
	for s := 0; s < spaces; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			sp := NewContextSpace(store, fmt.Sprintf("agent%d", s))
			for i := 0; i < appends; i++ {
				sp.Append(KindToolResult, map[string]any{
					"space": s, "seq": i, "body": fmt.Sprintf("结果 %d-%d", s, i)})
			}
			ids[s] = sp.PageIDs()
		}(s)
	}
	wg.Wait()

	// 全部空间还活着 —— 一页都不该丢
	for s := range ids {
		if len(ids[s]) != appends {
			t.Fatalf("空间 %d 只剩 %d 页", s, len(ids[s]))
		}
		for _, id := range ids[s] {
			if !store.Has(id) {
				t.Fatalf("空间 %d 的活页 %v 不见了", s, id)
			}
		}
	}
}

// 并发 fork + release: 父空间放手之后, 子空间引用的页仍然要活着.
//
// 引用计数错一格的后果不对称: 多算了只是晚点回收, 少算了是**活页被删**.
func TestConcurrentForkReleaseKeepsSharedPages(t *testing.T) {
	store := NewPageStore()
	// 容量压到极小, 逼回收真的动手 —— 否则默认容量下它什么都不删,
	// 测出来的"没丢页"是假的绿.
	store.SetCapacity(Capacity{SoftBytes: 1, HardBytes: 1})

	parent := NewContextSpace(store, "父")
	for i := 0; i < 50; i++ {
		parent.Append(KindOutput, map[string]any{"i": i})
	}
	shared := parent.PageIDs()

	var wg sync.WaitGroup
	kids := make([]*ContextSpace, 8)
	for i := range kids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kids[i] = parent.Fork(fmt.Sprintf("子%d", i))
		}(i)
	}
	wg.Wait()

	parent.Release() // 父放手, 但子还在
	store.Reclaim()
	for _, id := range shared {
		if !store.Has(id) {
			t.Fatalf("父放手之后共享页 %v 被回收了 —— 子空间还在用", id)
		}
	}

	for _, k := range kids {
		k.Release()
	}
	store.Reclaim()
	for _, id := range shared {
		if store.Has(id) {
			t.Fatalf("全都放手了又超硬限, 页 %v 还占着", id)
		}
	}
}

// 重复 Release 不许把子空间的引用减没.
//
// 这条早先出过: selfReleased 和 childRefs 合成一个计数, 拥有者多调一次
// Release 就把子的引用扣掉了.
func TestDoubleReleaseDoesNotStealChildRefs(t *testing.T) {
	store := NewPageStore()
	store.SetCapacity(Capacity{SoftBytes: 1, HardBytes: 1})

	parent := NewContextSpace(store, "父")
	parent.Append(KindInput, map[string]any{"x": 1})
	id := parent.PageIDs()[0]

	kid := parent.Fork("子")
	parent.Release()
	parent.Release() // 重复放手
	parent.Release()

	store.Reclaim()
	if !store.Has(id) {
		t.Fatal("重复 Release 把子空间的引用扣没了, 活页被回收")
	}
	kid.Release()
	store.Reclaim()
	if store.Has(id) {
		t.Fatal("子也放手了又超硬限, 页该被回收")
	}
}

// 长跑: 反复建空间→用→放手, 存储不该无限长大.
//
// 泄漏一点点看不出来, 跑久了就是内存吃满 —— 这台设备可能几周不重启.
// **这条一开始是绿的假象**: 缺省容量是零值, Reclaim 两步都被
// `if cap > 0` 挡掉, 什么都没删. 现在缺省容量是真的了.
func TestLongRunDoesNotLeak(t *testing.T) {
	store := NewPageStore()
	const hard = 16 << 10
	store.SetCapacity(Capacity{SoftBytes: 4 << 10, HardBytes: hard})

	const rounds = 300
	for r := 0; r < rounds; r++ {
		sp := NewContextSpace(store, fmt.Sprintf("轮%d", r))
		for i := 0; i < 20; i++ {
			// 每轮内容都不同, 免得被去重掩盖了泄漏
			sp.Append(KindToolResult, map[string]any{"r": r, "i": i})
		}
		sp.Release()
		if r%50 == 0 {
			store.Reclaim()
		}
	}
	store.Reclaim()

	// **判据是字节, 不是页数.** 回收管的是字节上限, 页可以很小很多.
	// 我第一版按页数判, 结果"剩 1056 页"看着像泄漏, 其实总共才 16KB,
	// 正好等于设定的硬限 —— 回收完全正确, 是判据挑错了维度.
	rep, _ := store.Reclaim()
	st := store.Stats()
	total := st.ResidentBytes
	if total > hard {
		t.Fatalf("跑了 %d 轮之后还占 %d 字节, 超过硬限 %d —— 在泄漏 (报告 %+v)",
			rounds, total, hard, rep)
	}
	if rep.LiveBytes != 0 {
		t.Fatalf("所有空间都放手了, 却还有 %d 字节算作活页", rep.LiveBytes)
	}
}

// 缺省构造就该能回收 —— 零值容量等于永不回收, 那是 fail-open.
//
// 生产路径一直是裸的 NewPageStore(), 所以回收模块写了三百行
// 却在真实运行中一次都没生效过.
func TestDefaultStoreActuallyReclaims(t *testing.T) {
	store := NewPageStore()
	if c := store.Capacity(); c.SoftBytes <= 0 || c.HardBytes <= 0 {
		t.Fatalf("缺省容量是 %+v —— 回收整个是死的", c)
	}
}

// 全是活页还超限: **不许静默丢**, 要把压力报出来让 OS 去处置.
// 沉默地丢数据比 OOM 危险得多.
func TestAllLivePagesReportsPressureNotSilentLoss(t *testing.T) {
	store := NewPageStore()
	store.SetCapacity(Capacity{SoftBytes: 1, HardBytes: 1})

	sp := NewContextSpace(store, "一直活着")
	for i := 0; i < 30; i++ {
		sp.Append(KindToolResult, map[string]any{"i": i, "body": "占地方的内容"})
	}
	live := sp.PageIDs()

	rep, err := store.Reclaim()
	if err == nil && !rep.StillOver {
		t.Fatal("全是活页还超硬限, 却既没报错也没标 StillOver")
	}
	for _, id := range live {
		if !store.Has(id) {
			t.Fatalf("活页 %v 被静默丢了", id)
		}
	}
}

// 相同内容跨空间要去重 —— 那是页表省内存的全部理由
func TestIdenticalContentSharesOnePage(t *testing.T) {
	store := NewPageStore()
	var spaces []*ContextSpace
	for i := 0; i < 20; i++ {
		sp := NewContextSpace(store, fmt.Sprintf("s%d", i))
		sp.Append(KindToolResult, map[string]any{"body": "一模一样的工具结果"})
		spaces = append(spaces, sp)
	}
	if n := store.Stats().Resident; n != 1 {
		t.Fatalf("20 个空间存同样的内容, 占了 %d 页 —— 去重没生效", n)
	}
	// 只放手一个, 其余还在用, 不能回收
	spaces[0].Release()
	store.Reclaim()
	if store.Stats().Resident != 1 {
		t.Fatal("还有 19 个空间在用, 页却被回收了")
	}
}

// 并发回收与并发写同时进行, 不许崩也不许丢活页
func TestReclaimUnderConcurrentWrites(t *testing.T) {
	store := NewPageStore()
	stop := make(chan struct{})

	// 两个 WaitGroup 分开等.
	//
	// 合成一个会死锁: wg.Wait() 等回收 goroutine 结束, 回收 goroutine
	// 等 stop 关闭, 而 stop 在 Wait 之后才关 —— 我第一版就是这么写的.
	// 回收也不能写成不让步的忙循环, 那会把写入方全饿死(第二版栽的).
	var writers, janitor sync.WaitGroup

	janitor.Add(1)
	go func() {
		defer janitor.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				store.Reclaim()
			}
		}
	}()

	// 一边写一边检查自己的页还在不在
	for s := 0; s < 6; s++ {
		writers.Add(1)
		go func(s int) {
			defer writers.Done()
			sp := NewContextSpace(store, fmt.Sprintf("w%d", s))
			for i := 0; i < 300; i++ {
				p := sp.Append(KindOutput, map[string]any{"s": s, "i": i})
				if !store.Has(p.ID) {
					t.Errorf("刚写进去的页 %v 就被回收了", p.ID)
					return
				}
			}
			sp.Release()
		}(s)
	}
	writers.Wait()
	close(stop)
	janitor.Wait()
}
