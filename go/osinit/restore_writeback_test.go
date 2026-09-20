package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// **装回来的一律不许往账本里写.**
//
// ── 这是一个自我恶化的坑, 而且已经吃过一次 ──
//
//	Places.Restore 重放"忘掉"那条时调 Forget, 而 Forget 会 Append.
//	于是每次开机, place.forgotten 在账本里翻一倍 —— 而新写的那几条
//	排在**末尾**. 顺序重放时它们盖掉前面的 place.named,
//	**他重新教过的"家"在下一次重启后消失**。
//
//	一条可能变成四条, 导致"家"消失。同样的重复写入风险也适用于 Rules。
//
// ── 为什么钉在这一层, 不是逐个 Restore 里写测试 ──
//
//	逐个写的话, 加第七个登记簿的那天没人记得也要写一条 —— 而这个
//	仓库已经证明了"记得改两处"这种约定是靠不住的(Places 那条注释里
//	明明白白写着为什么要摘 log, 而隔壁那半就是忘了).
//
//	所以判据是通用的: **开机三次, 账本一条都不许涨**。
func TestRestoreNeverWritesBack(t *testing.T) {
	now := func() int64 { return time.Now().UnixMilli() }

	for _, c := range []struct {
		name string
		// seed 造一段真实历史(建了又删又建), 返回重放用的函数
		seed  func(log *EventLog)
		again func(log *EventLog)
	}{
		{
			name: "地点",
			seed: func(log *EventLog) {
				p := NewPlaces(log)
				p.Add("家", 31, 117, 0)
				p.Forget("家")
				p.Add("家", 31, 117, 600)
			},
			again: func(log *EventLog) { NewPlaces(log).Restore(flatten(log)) },
		},
		{
			name: "规则",
			seed: func(log *EventLog) {
				r := NewRules(log, nil, nil, nil)
				got, _ := r.Add(Rule{When: []Cond{{Fact: "weather", Op: ">", Value: 1.0}},
					Say: "带伞", Why: "要下雨"})
				r.Remove(got.ID)
				r.Add(Rule{When: []Cond{{Fact: "weather", Op: ">", Value: 2.0}},
					Say: "带伞", Why: "要下雨"})
			},
			again: func(log *EventLog) {
				NewRules(log, nil, nil, nil).Restore(flatten(log))
			},
		},
		{
			name: "人",
			seed: func(log *EventLog) {
				p := NewPeople(log)
				p.Know(Person{ID: "u1", Name: "老王"})
				p.Forget("u1")
				p.Know(Person{ID: "u1", Name: "老王"})
			},
			again: func(log *EventLog) { NewPeople(log).Restore(flatten(log)) },
		},
		{
			name: "设备",
			seed: func(log *EventLog) {
				d := NewDevices(log)
				d.Declare(Device{ID: "phone", Kind: "phone", Senses: []string{"location"}})
				d.Forget("phone")
				d.Declare(Device{ID: "phone", Kind: "phone", Senses: []string{"location"}})
			},
			again: func(log *EventLog) { NewDevices(log).Restore(flatten(log)) },
		},
		{
			name: "记着的事",
			seed: func(log *EventLog) {
				n := NewNotes(log)
				n.Set("", "咖啡", "不喝")
				n.Forget("", "咖啡")
				n.Set("", "咖啡", "只喝美式")
			},
			again: func(log *EventLog) { NewNotes(log).Restore(flatten(log)) },
		},
		{
			name: "待办",
			seed: func(log *EventLog) {
				a := NewAgenda(log)
				got, _ := a.Add("", "买牛奶", 0, "")
				a.Done(got.ID)
				a.Add("", "买鸡蛋", 0, "")
			},
			again: func(log *EventLog) { NewAgenda(log).Restore(flatten(log)) },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := NewEventLog(now)
			c.seed(log)
			before := len(flatten(log))
			if before == 0 {
				t.Fatal("这一摊没往账本里写过东西, 判据是空的")
			}
			for i := 1; i <= 3; i++ {
				c.again(log)
				if got := len(flatten(log)); got != before {
					t.Fatalf("第 %d 次开机把账本从 %d 条写成了 %d 条 —— "+
						"这是个自我恶化的坑: 新写的那几条排在末尾, "+
						"顺序重放时会盖掉它们前面的记录", i, before, got)
				}
			}
		})
	}
}

// 而且**重新建过的那个必须还在** —— 那才是这条判据真正护着的东西
func TestRestoreKeepsWhatWasRebuilt(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	p := NewPlaces(log)
	p.Add("家", 31, 117, 0)
	p.Forget("家")
	p.Add("家", 31, 117, 600)
	for i := 0; i < 3; i++ {
		NewPlaces(log).Restore(flatten(log))
	}
	got := NewPlaces(nil)
	got.Restore(flatten(log))
	known := got.Known()
	if len(known) != 1 || known[0].Radius != 600 {
		t.Fatalf("重新教过的家丢了或半径不对: %+v", known)
	}
	_ = abi.EvPlaceNamed
}
