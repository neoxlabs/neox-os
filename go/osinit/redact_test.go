package osinit

import (
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 认得出这儿是哪儿, 就**只记地名** —— 经纬度只是"在公司"的一种
// 更危险的写法, 而账本是只增不删的
func TestRedactDropsCoordsWhenPlaceIsKnown(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	places := NewPlaces(log)
	if _, err := places.Add("公司", 31.86, 117.28, 0); err != nil {
		t.Fatal(err)
	}
	redact := RedactForLedger(places)

	got := redact(abi.Signal{Kind: "place.arrived", Body: map[string]any{
		"lat": 31.8601, "lon": 117.2802, "acc": 12.0,
	}})
	if _, ok := got["lat"]; ok {
		t.Fatalf("认得出是公司却把坐标记进了账本: %v", got)
	}
	if got["place"] != "公司" {
		t.Fatalf("坐标去掉了, 地名也没留下: %v —— 那这条记录就没用了", got)
	}
	if _, ok := got["acc"]; ok {
		t.Fatal("精度字段单独留着 —— 它同样能帮着还原轨迹")
	}
}

// 认不出的地方降精度留着: 整个丢掉的话, 补传恢复那一路连"他当时在
// 某个地方"都说不出来
func TestRedactCoarsensUnknownPlace(t *testing.T) {
	redact := RedactForLedger(NewPlaces(nil))
	got := redact(abi.Signal{Kind: "location", Body: map[string]any{
		"lat": 31.8601234, "lon": 117.2802987,
	}})
	if got["lat"] != 31.86 && got["lat"] != 31.860 {
		t.Fatalf("没降精度: %v", got["lat"])
	}
	lat, _ := floatOf(got["lat"])
	if lat == 31.8601234 {
		t.Fatal("原样记进了账本 —— 逐分钟的精确轨迹是这台机器上最危险的文件")
	}
}

// **原件不能被改** —— 观察链拿到的还得是精确的那份, 否则地点匹配、
// 在场推断、世界模型全都跟着变糙
func TestRedactDoesNotTouchTheOriginal(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	places := NewPlaces(log)
	_, _ = places.Add("家", 31.86, 117.28, 0)

	body := map[string]any{"lat": 31.8601, "lon": 117.2802}
	sig := abi.Signal{Kind: "place.arrived", Body: body}
	_ = RedactForLedger(places)(sig)

	if _, ok := body["lat"]; !ok {
		t.Fatal("就地改了原件 —— 判断那一路会跟着拿到被抹过的坐标")
	}
}

// 不带坐标的信号原样过 —— 大多数信号是这一类, 不该为它们多分配一份
func TestRedactLeavesOtherSignalsAlone(t *testing.T) {
	body := map[string]any{"state": "opened"}
	got := RedactForLedger(NewPlaces(nil))(abi.Signal{Kind: "door.opened", Body: body})
	if len(got) != 1 || got["state"] != "opened" {
		t.Fatalf("动了不该动的: %v", got)
	}
}
