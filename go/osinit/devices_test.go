package osinit

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func TestDeviceMustDoSomething(t *testing.T) {
	d := NewDevices(nil)
	// 既不感知也不呈现 —— 多半是字段名拼错了. 收下的话它会安静地
	// 待在列表里, 既不报信号也收不到通知
	if _, err := d.Declare(Device{ID: "x", Name: "空壳"}); err == nil {
		t.Fatal("一台什么都不会的设备被收下了 —— 接进来什么也不会发生, 而没人会知道")
	}
	if _, err := d.Declare(Device{Name: "没 id"}); err == nil {
		t.Fatal("没有 id 也收下了 —— 那就没法认出这是哪一台")
	}
}

// 呈现方式**必须是认得的那几种**.
//
//	写采集端的人手上只有一份 curl. 把 "vibrate" 静默收下的话,
//	OS 永远不会用那台设备说话, 而设备那头一切正常 ——
//	这类错误只有在"它为什么从来不通知我"的时候才会被发现
func TestDevicePresentsMustBeKnown(t *testing.T) {
	d := NewDevices(nil)
	_, err := d.Declare(Device{ID: "b", Name: "耳机", Presents: []string{"vibrate"}})
	if err == nil {
		t.Fatal("不认得的呈现方式被收下了")
	}
	if !strings.Contains(err.Error(), PresentSpeak) {
		t.Fatalf("报错没说清楚有哪几种可选: %v —— 写设备端的人只能靠这句话", err)
	}
}

func TestDeviceCapabilities(t *testing.T) {
	dev := Device{
		ID: "phone", Senses: []string{"place", "battery"},
		Presents: []string{PresentNotify, PresentAlert},
	}
	// 前缀匹配: 声明 "place" 就该认得 place.arrived
	if !dev.CanSense("place.arrived") {
		t.Fatal("声明了 place 却不认 place.arrived —— 那设备端每加一种事件都要改声明")
	}
	if dev.CanSense("placebo") {
		t.Fatal("place 不该匹配 placebo —— 前缀比要卡在点上")
	}
	if !dev.CanPresent(PresentAlert) || dev.CanPresent(PresentSpeak) {
		t.Fatal("呈现能力判错了")
	}
}

// 屋里有没有人接得住这种说法 —— 一条要念出来的话, 如果一台能出声的
// 设备都没有, 那它该退回成一条摆出来的通知, 而不是发出去石沉大海
func TestDevicePresentingPicksByCapability(t *testing.T) {
	d := NewDevices(nil)
	mustDeclare(t, d, Device{ID: "p", Name: "手机", Presents: []string{PresentNotify, PresentAlert}})
	mustDeclare(t, d, Device{ID: "e", Name: "耳机", Presents: []string{PresentSpeak}})

	if got := d.Presenting(PresentSpeak, ""); len(got) != 1 || got[0].ID != "e" {
		t.Fatalf("能出声的该只有耳机, 得到 %v", got)
	}
	if got := d.Presenting(PresentAsk, ""); len(got) != 0 {
		t.Fatalf("没有设备能问一句并等到答案, 却找出了 %v —— "+
			"这会让 needs_you 那条通道以为自己闭环了", got)
	}
}

// 能力**会变** —— 用户关掉了定位权限, 那台手机就不再 senses location.
// 只记第一次的话, OS 会一直等一个永远不来的信号
func TestDeviceRedeclareReplaces(t *testing.T) {
	d := NewDevices(nil)
	mustDeclare(t, d, Device{ID: "p", Name: "手机", Senses: []string{"place", "battery"}})
	mustDeclare(t, d, Device{ID: "p", Name: "手机", Senses: []string{"battery"}})

	got, _ := d.Get("p")
	if got.CanSense("place.arrived") {
		t.Fatal("重新声明之后还认得旧能力 —— OS 会一直等一个永远不来的信号")
	}
	if len(d.List()) != 1 {
		t.Fatalf("同一台设备报到两次变成了 %d 台", len(d.List()))
	}
}

// 重启之后 OS 得知道屋里有哪些设备 —— 一台一天只报一次的设备
// (体重秤、电表)否则能失踪一整天
func TestDeviceSurvivesRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	d := NewDevices(log)
	mustDeclare(t, d, Device{
		ID: "e", Name: "耳机", Kind: "earbuds",
		Senses: []string{"heartrate"}, Presents: []string{PresentSpeak},
	})

	// 重启: 一个全新的登记处, 只有账本
	var evs []abi.Event
	for _, bucket := range log.Snapshot() {
		evs = append(evs, bucket...)
	}
	next := NewDevices(nil)
	next.Restore(evs)

	got, ok := next.Get("e")
	if !ok {
		t.Fatal("重启之后这台设备不见了")
	}
	// **能力清单要一起回来**: 账本里它是 []any, 内存里是 []string ——
	// 只认一种的话清单会静默地变成空的, 而设备还在列表里
	if !got.CanPresent(PresentSpeak) || !got.CanSense("heartrate") {
		t.Fatalf("设备回来了但能力没了: %+v —— 于是它再也不会被用来说话", got)
	}
	if got.Kind != "earbuds" || got.Name != "耳机" {
		t.Fatalf("身份没装回来: %+v", got)
	}
}

func mustDeclare(t *testing.T, d *Devices, dev Device) {
	t.Helper()
	if _, err := d.Declare(dev); err != nil {
		t.Fatalf("报到失败 %s: %v", dev.ID, err)
	}
}

// 换手机、卖掉一块表 —— **删得掉**. 删不掉的话列表里会永远躺着
// 一台三年前的手机, 而 OS 还在等它报到
func TestDeviceForgetSurvivesRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	d := NewDevices(log)
	mustDeclare(t, d, Device{ID: "old", Name: "旧手机", Presents: []string{PresentNotify}})
	if !d.Forget("old") {
		t.Fatal("删不掉")
	}
	if len(d.List()) != 0 {
		t.Fatal("说删了, 列表里还在")
	}
	// **重启之后不能自己长回来** —— 那是"假删": 账本里那条声明还在,
	// 顺序重放时后面这条删除要盖掉它
	var evs []abi.Event
	for _, b := range log.Snapshot() {
		evs = append(evs, b...)
	}
	next := NewDevices(nil)
	next.Restore(evs)
	if _, ok := next.Get("old"); ok {
		t.Fatal("重启之后它自己长回来了 —— 那就是假删")
	}
}
