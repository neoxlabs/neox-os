package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func evp(kind abi.EventKind, p map[string]any) abi.Event {
	return abi.Event{PID: signalPID, Kind: kind, Payload: p}
}

// 一段正常的使用要判自洽 —— 这条是基线, 它红了说明闸本身写错了
func TestCleanLedgerHasNoProblems(t *testing.T) {
	evs := []abi.Event{
		evp(abi.EvPlaceNamed, map[string]any{"name": "公司"}),
		evp(abi.EvWatchSet, map[string]any{"id": "g1"}),
		evp(abi.EvWakeSet, map[string]any{"id": "w1"}),
		evp(abi.EvWakeSet, map[string]any{"id": "w2"}),
		evp(abi.EvWakeCancelled, map[string]any{"id": "w1"}), // 改时间: 撤了重设
		evp(abi.EvWakeSet, map[string]any{"id": "w3"}),
		evp(abi.EvWakeCancelled, map[string]any{"id": "w2"}), // 用户撤的
	}
	st, probs := CheckLedger(evs)
	if len(probs) != 0 {
		t.Fatalf("正常的一段使用被判不自洽: %v", probs)
	}
	if len(st.Wakes) != 1 || st.Wakes[0] != "w3" {
		t.Fatalf("重放出来的闹钟不对: %v", st.Wakes)
	}
	if len(st.Watches) != 1 || len(st.Places) != 1 {
		t.Fatalf("重放出来的关注/地点不对: %+v", st)
	}
}

// **同一个 id 设两次 = 前一条被顶掉了, 而且是静默的.**
//
// S18 之前闹钟和关注共用 w%d 前缀, 重启之后就会这样 ——
// 表现是"用户的某条提醒莫名其妙消失了", 没有任何报错.
func TestDuplicateIDIsCaught(t *testing.T) {
	_, probs := CheckLedger([]abi.Event{
		evp(abi.EvWakeSet, map[string]any{"id": "w1"}),
		evp(abi.EvWakeSet, map[string]any{"id": "w1"}),
	})
	if len(probs) == 0 {
		t.Fatal("同一个 id 设两次没被抓到 —— 前一条被顶掉而没人知道")
	}
	if !strings.Contains(probs[0].Why, "顶掉") {
		t.Fatalf("报错没说清后果: %v", probs[0])
	}
}

// **闹钟和关注的 id 撞了 = 撤销会撤错对象.**
//
// 这正是 S18 修掉的那个隐患. 有了这道闸, 它退化了会被当场抓住.
func TestCrossKindIDCollisionIsCaught(t *testing.T) {
	_, probs := CheckLedger([]abi.Event{
		evp(abi.EvWakeSet, map[string]any{"id": "w1"}),
		evp(abi.EvWatchSet, map[string]any{"id": "w1"}),
	})
	var found bool
	for _, p := range probs {
		if p.Kind == "id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("闹钟和关注的 id 撞了却没抓到: %v", probs)
	}
}

// 撤一个从来没设过的 —— 账本缺了一条, 或者 id 对不上
func TestCancelWithoutSetIsCaught(t *testing.T) {
	_, probs := CheckLedger([]abi.Event{
		evp(abi.EvWakeCancelled, map[string]any{"id": "w9"}),
	})
	if len(probs) == 0 {
		t.Fatal("撤了一个从没设过的闹钟却没抓到")
	}
}

// **重建那一步错了, 从表面看不出来.**
//
// state.txt 长得永远是对的(它就是内存对象的渲染), 错的是内存对象
// 跟账本的关系. 所以要拿账本重放出来的现状去对活着的对象.
func TestMatchesCatchesRestoreDrift(t *testing.T) {
	log := NewEventLog(func() int64 { return 0 })
	w := NewWatches(log)
	tm := NewTimers(log, nil, nil)
	p := NewPlaces(log)

	p.Add("公司", 31.86, 117.28, 0)
	w.Add("place.left", "公司", "", "离开公司告诉我")
	wk, _ := tm.Set("th", time.Now().Add(time.Hour).UnixMilli(), "吃药")

	evs := log.Replay(signalPID, 0)
	st, probs := CheckLedger(evs)
	if len(probs) != 0 {
		t.Fatalf("账本自身不自洽: %v", probs)
	}
	if got := st.Matches(w, tm, p); len(got) != 0 {
		t.Fatalf("活着的对象跟账本对不上: %v", got)
	}

	// 模拟重建漏了一条: 内存里把闹钟撤了, 但拿**旧的**账本快照去对 ——
	// 那正是"重建漏了一条"的样子
	tm.Cancel(wk.ID)
	got := st.Matches(w, tm, p)
	if len(got) == 0 {
		t.Fatal("账本说闹钟还在、内存里却没有, 这种漂移没被抓到 —— " +
			"重启之后用户会发现提醒没了")
	}
	if !strings.Contains(got[0].Why, "重启之后") {
		t.Fatalf("报错没说清后果: %v", got[0])
	}
}

// 内存里冒出一条账本里没有的 —— 同样是漂移, 只是方向相反
func TestMatchesCatchesGhostEntries(t *testing.T) {
	w := NewWatches(nil) // 不落账本
	w.Add("door.opened", "", "", "开门告诉我")
	st, _ := CheckLedger(nil)
	got := st.Matches(w, nil, nil)
	if len(got) == 0 {
		t.Fatal("内存里有账本里没有的条目, 没被抓到 —— 它是哪儿冒出来的?")
	}
}
