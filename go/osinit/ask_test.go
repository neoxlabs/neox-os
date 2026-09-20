package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 问设备要东西这条**不许进账本**.
//
//	它是一句吆喝, 不是一件发生过的事: 设备取到了会照常发一条正常的
//	信号, 那条才是事实. 把吆喝也记下来, 账本里就多一半没有信息量的行
func TestAskDoesNotEnterTheLedger(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	d := NewDevices(log)
	d.Declare(Device{ID: "phone", Kind: "phone", Senses: []string{"location"}})
	before := len(flatten(log))
	if n := d.Ask("location", ""); n != 1 {
		t.Fatalf("该问 1 台, 问了 %d 台", n)
	}
	if got := len(flatten(log)); got != before {
		t.Fatalf("吆喝进了账本: %d → %d", before, got)
	}
}

// **只问答得了这件事的设备**.
//
//	一台体重秤收到"给我个位置"只会困惑, 而那条吆喝还占着它一次唤醒
func TestAskOnlyReachesDevicesThatCanAnswer(t *testing.T) {
	d := NewDevices(NewEventLog(func() int64 { return 0 }))
	d.Declare(Device{ID: "phone", Kind: "phone", Senses: []string{"location", "battery"}})
	d.Declare(Device{ID: "scale", Kind: "scale", Senses: []string{"weight"}})
	if n := d.Ask("location", ""); n != 1 {
		t.Fatalf("该只问手机那一台, 问了 %d 台", n)
	}
	if n := d.Ask("weight", ""); n != 1 {
		t.Fatalf("该只问秤那一台, 问了 %d 台", n)
	}
	// 没有设备答得了的时候返回 0 —— **调用方要照实说**, 别装作问过了
	if n := d.Ask("血压", ""); n != 0 {
		t.Fatalf("没有设备能答却说问了 %d 台", n)
	}
}

// 为了答"他在哪"去唤醒她的手机, 是一次不该发生的打扰
func TestAskRespectsOwner(t *testing.T) {
	d := NewDevices(NewEventLog(func() int64 { return 0 }))
	d.Declare(Device{ID: "his", Senses: []string{"location"}, Owner: "he"})
	d.Declare(Device{ID: "hers", Senses: []string{"location"}, Owner: "she"})
	d.Declare(Device{ID: "屋里的", Senses: []string{"location"}})
	if n := d.Ask("location", "he"); n != 2 {
		t.Fatalf("该问他那台加屋里公用的(2 台), 问了 %d 台", n)
	}
}

// 等一条更新的位置 —— 等到了就用新的, 等不到就用手上这条,
// **而且照实说它多旧**: 编一个新的比说"三分钟前"糟得多
func TestWaitFresherReturnsWhenANewFixArrives(t *testing.T) {
	p := NewPlaces(nil)
	p.Observe(abi.Signal{At: 1000, Body: map[string]any{"lat": 1.0, "lon": 2.0}})
	since := p.HereAt()
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Observe(abi.Signal{At: 2000, Body: map[string]any{"lat": 1.1, "lon": 2.1}})
	}()
	if !p.WaitFresher(since, 2*time.Second) {
		t.Fatal("新位置到了却没等到")
	}
}

func TestWaitFresherGivesUp(t *testing.T) {
	p := NewPlaces(nil)
	p.Observe(abi.Signal{At: 1000, Body: map[string]any{"lat": 1.0, "lon": 2.0}})
	start := time.Now()
	if p.WaitFresher(p.HereAt(), 400*time.Millisecond) {
		t.Fatal("什么都没来却说等到了")
	}
	// **不能等太久**: 他在等着看一句话, 而卡比慢一点糟
	if d := time.Since(start); d > time.Second {
		t.Fatalf("等了 %v —— 说好最多 400ms", d)
	}
}
