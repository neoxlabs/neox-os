package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// **每次重启之后"这儿是家"都会失败.**
//
//	NameHere 靠的是最后一次收到的坐标, 而那个只活在内存里 ——
//	它报的是"还没收到过任何带位置的信号", 而账本里明明有, 世界模型
//	也答得出来. 两处各存一份"最后位置", 而只有一处会恢复.
//
//	而且它不会自己好起来: 采集端按移动触发, 人不动就不报 ——
//	人可能坐了一下午, 而这句话一直失败.
func TestPlacesRestoresLastPosition(t *testing.T) {
	now := time.Now()
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second,
		Observe: func(s abi.Signal) {},
	})
	bus.Ingest(abi.Signal{ID: "l1", Source: "phone", Kind: "location",
		At:   now.Add(-2 * time.Minute).UnixMilli(),
		Body: map[string]any{"lat": 32.919, "lon": 117.357}})

	var evs []abi.Event
	for _, b := range log.Snapshot() {
		evs = append(evs, b...)
	}
	// 重启: 一张全新的地点表, 只有账本
	next := NewPlaces(nil)
	next.Restore(evs)

	if _, _, ok := next.Here(); !ok {
		t.Fatal("重启之后不知道'刚才在哪'了 —— 于是「这儿是家」会一直失败")
	}
	if _, err := next.NameHere("家", 0); err != nil {
		t.Fatalf("重启之后起不了名: %v", err)
	}
}

// **装回来的一律不许记账**.
//
// ── 这条栽过, 而且是自我恶化的 ──
//
//	EvPlaceNamed 那条特意摘了 log, 而 EvPlaceForgotten 那条忘了.
//	于是每次开机, "忘掉家"这条事件在账本里翻一倍, 新写的那几条排在
//	**末尾** —— 顺序重放时它们盖掉前面的 named, 而他重新教过的"家"
//	在下一次重启后消失。
//
//	一条 place.forgotten 变成四条, 家就没了。
func TestRestoreNeverWritesBackToTheLedger(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	p := NewPlaces(log)
	p.Add("家", 31.0, 117.0, 0)
	p.Forget("家")
	p.Add("家", 31.0, 117.0, 600)
	before := len(flatten(log))

	// 开三次机 —— 事件数一条都不该涨
	for i := 0; i < 3; i++ {
		NewPlaces(log).Restore(flatten(log))
		if got := len(flatten(log)); got != before {
			t.Fatalf("第 %d 次开机把账本从 %d 条写成了 %d 条", i+1, before, got)
		}
	}

	// 而且重新教过的那个必须还在 —— 那才是这条判据真正护着的东西
	next := NewPlaces(nil)
	next.Restore(flatten(log))
	got := next.Known()
	if len(got) != 1 || got[0].Name != "家" || got[0].Radius != 600 {
		t.Fatalf("重新教过的家丢了或者半径不对: %+v", got)
	}
}
