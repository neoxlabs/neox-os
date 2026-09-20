package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

const (
	офис = 31.86 // 故意用两个相距约 2 公里的点
	дом  = 31.88
	lonX = 117.28
)

func sig(lat, lon float64, at int64) abi.Signal {
	return abi.Signal{Source: "phone.mk", Kind: "place.arrived", At: at,
		Body: map[string]any{"lat": lat, "lon": lon, "acc": 15.0}}
}

// **坐标要变成名字, 而且要把坐标换掉.**
//
// 真机演示里模型看到的是 "lat=31.86001 lon=117.28" —— 它对这行字
// 能做的判断为零, 三条判据一条都过不了. 到达信号在语义上是空的.
func TestCoordinatesBecomeAName(t *testing.T) {
	p := NewPlaces(nil)
	p.Add("公司", офис, lonX, 0)

	d := Digest{Count: 1, Items: []DigestItem{{
		Source: "phone.mk", Kind: "place.arrived",
		Sample: map[string]any{"lat": 31.86001, "lon": lonX, "acc": 5.0},
	}}}
	p.Annotate(&d)

	if d.Items[0].Sample["地点"] != "公司" {
		t.Fatalf("没认出来: %v", d.Items[0].Sample)
	}
	// **坐标要换掉不是并列**: 留着的话模型会在回话里把坐标念出来,
	// 而用户比谁都清楚公司在哪儿
	if _, ok := d.Items[0].Sample["lat"]; ok {
		t.Fatal("认出名字之后坐标还留着 —— 模型会把它念给用户听")
	}
	if !contains(d.Text(), "公司") {
		t.Fatalf("摘要文本里没有地名:\n%s", d.Text())
	}
}

// 认不出来的原样留着 —— 坐标至少还能让它说"你到了一个我不认识的地方"
func TestUnknownPlaceKeepsCoordinates(t *testing.T) {
	p := NewPlaces(nil)
	p.Add("公司", офис, lonX, 0)
	d := Digest{Count: 1, Items: []DigestItem{{
		Sample: map[string]any{"lat": дом, "lon": lonX},
	}}}
	p.Annotate(&d)
	if _, ok := d.Items[0].Sample["lat"]; !ok {
		t.Fatal("认不出来的地方把坐标也删了 —— 那条信号就彻底空了")
	}
	if _, ok := d.Items[0].Sample["地点"]; ok {
		t.Fatal("认不出来却编了个名字")
	}
}

// 地点命名使用"这儿是公司"这样的自然表达, 不是要求输入"31.86,117.28 是公司".
// 要求输入坐标会暴露内部表示, 而且使用者通常也无法准确提供
func TestNameHereUsesLastSeenLocation(t *testing.T) {
	p := NewPlaces(nil)
	if _, err := p.NameHere("公司", 0); err == nil {
		t.Fatal("还没见过任何位置就命名成功了 —— 那会记下一个 0,0")
	}
	p.Observe(sig(офис, lonX, 1000))
	pl, err := p.NameHere("公司", 0)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Lat != офис {
		t.Fatalf("命名用错了坐标: %+v", pl)
	}
	if p.Lookup(31.86008, lonX) != "公司" {
		t.Fatal("命名之后认不出来")
	}
}

// 顺路观察必须是**机会性**的: 没有坐标的信号不能让它出错
func TestObserveIgnoresSignalsWithoutCoordinates(t *testing.T) {
	p := NewPlaces(nil)
	p.Observe(abi.Signal{Kind: "battery.level", Body: map[string]any{"level": 20}})
	p.Observe(abi.Signal{Kind: "call.incoming"}) // body 是 nil
	p.Observe(abi.Signal{Kind: "x", Body: map[string]any{"lat": "不是数字"}})
	if _, err := p.NameHere("哪儿", 0); err == nil {
		t.Fatal("从没有坐标的信号里认出了位置")
	}
}

// 用旧的位置命名会记错地方 —— 只认最新的那次
func TestObserveKeepsTheLatest(t *testing.T) {
	p := NewPlaces(nil)
	p.Observe(sig(дом, lonX, 2000))
	p.Observe(sig(офис, lonX, 1000)) // 更旧的一条后到
	pl, _ := p.NameHere("家", 0)
	if pl.Lat != дом {
		t.Fatalf("被一条更旧的信号覆盖了: %+v", pl)
	}
}

// 同名覆盖表示地点已经迁移: "公司搬了"应当更新原有记录
func TestSameNameReplaces(t *testing.T) {
	p := NewPlaces(nil)
	p.Add("公司", офис, lonX, 0)
	p.Add("公司", дом, lonX, 0)
	if len(p.Known()) != 1 {
		t.Fatalf("同名地点存了两份: %+v", p.Known())
	}
	if p.Lookup(дом, lonX) != "公司" {
		t.Fatal("搬家之后新地址认不出来")
	}
	if p.Lookup(офис, lonX) == "公司" {
		t.Fatal("搬家之后旧地址还认成公司")
	}
}

// 重叠时取最近的 —— 家和小区门口可能都覆盖到, 用户想听更具体的那个
func TestOverlappingPlacesPickNearest(t *testing.T) {
	p := NewPlaces(nil)
	p.Add("小区", офис, lonX, 500)
	p.Add("家", офис+0.001, lonX, 150)
	if got := p.Lookup(офис+0.0011, lonX); got != "家" {
		t.Fatalf("重叠时取了 %q, 该取更近的「家」", got)
	}
}

// **地点是用户教的, 丢了要重教一遍** —— 那比丢一条提醒更让人恼火
func TestPlacesSurviveRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return 0 })
	p := NewPlaces(log)
	p.Add("公司", офис, lonX, 0)
	p.Add("家", дом, lonX, 0)

	p2 := NewPlaces(nil)
	p2.Restore(log.Replay(signalPID, 0))
	if len(p2.Known()) != 2 {
		t.Fatalf("重启后剩 %d 个地点", len(p2.Known()))
	}
	if p2.Lookup(офис, lonX) != "公司" || p2.Lookup(дом, lonX) != "家" {
		t.Fatal("装回来的地点认不出来")
	}
}

// 装回来不该再记一遍日志, 否则每次开机日志翻倍
func TestRestoreDoesNotRelog(t *testing.T) {
	log := NewEventLog(func() int64 { return 0 })
	p := NewPlaces(log)
	p.Add("公司", офис, lonX, 0)
	before := len(log.Replay(signalPID, 0))
	p.Restore(log.Replay(signalPID, 0))
	if after := len(log.Replay(signalPID, 0)); after != before {
		t.Fatalf("装回来又记了一遍日志: %d → %d, 每次开机都会翻倍", before, after)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 重传的同一条不该被当成新观察 —— 否则"最近一次位置"会被
// 一条补传的旧位置盖掉, 而用户接着说"这儿是公司"就记错了地方
func TestObserveRunsAfterDedup(t *testing.T) {
	p := NewPlaces(nil)
	var seen int
	log := NewEventLog(func() int64 { return 0 })
	bus := NewSignalBus(log, nil, SignalOptions{
		Observe: func(s abi.Signal) { seen++; p.Observe(s) },
	})
	// 时间戳要**贴着现在**: 幂等键有老化(dedupRetention), 用一个几年前的
	// 时间戳配真实时钟, 那条记录当场就被清掉, 于是去重看起来"不工作" ——
	// 头一版我就是这么写的, 红了才发现错在测试不在代码
	s := abi.Signal{ID: "x", Source: "phone.mk", Kind: "place.arrived",
		At: time.Now().UnixMilli(), Body: map[string]any{"lat": офис, "lon": lonX}}
	for i := 0; i < 4; i++ {
		bus.Ingest(s)
	}
	if seen != 1 {
		t.Fatalf("重传的信号被观察了 %d 次 —— 最近位置会被旧数据盖掉", seen)
	}
}
