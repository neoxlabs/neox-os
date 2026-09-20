package osinit

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// **验收只活在单测里, 等于没有.**
//
// S51 把"这一天证明了什么"写成了纯函数, 但它只在 Go 测试里跑得到 ——
// 而真账本在另一台机器上, 是一份 jsonl. 每次还是我肉眼去翻,
// 而肉眼翻恰恰是 S51 要治的那件事.
//
// 所以它得能对着**一份真账本**跑, 并且自己说清这一天算不算数.
func TestVerdictOnEventfulDay(t *testing.T) {
	v := JudgeDay([]abi.Event{
		sigKindEv("ha.light.bed", "device.state"),
		sigKindEv("ha.lock.front_door", "lock.opened"),
		noticeEv("家里没人，但前门刚刚被解锁了"),
	}, DayExpect{Name: "有事发生的一天", MustHave: []string{"lock.opened"}, Notices: 1})
	if !v.OK {
		t.Fatalf("该通过的一天没通过: %v", v.Problems)
	}
	if !strings.Contains(v.Text(), "通过") {
		t.Fatalf("报告没说清结论:\n%s", v.Text())
	}
}

func TestVerdictOnQuietDay(t *testing.T) {
	v := JudgeDay([]abi.Event{
		sigKindEv("ha.light.bed", "device.state"),
		sigKindEv("ha.lock.front_door", "lock.opened"),
	}, DayExpect{Name: "全是琐事的一天", MustHave: []string{"lock.opened"}, Notices: 0})
	if !v.OK {
		t.Fatalf("对照日没通过: %v", v.Problems)
	}
}

// **尺子坏掉的那一天要被判死, 而且要说清为什么** ——
// 两次开门可能被时间压缩弄没, 而"开口 0 次"看起来像成功
func TestVerdictCatchesBrokenRuler(t *testing.T) {
	v := JudgeDay([]abi.Event{sigKindEv("ha.light.bed", "device.state")},
		DayExpect{Name: "尺子坏了的一天", MustHave: []string{"lock.opened"}, Notices: 0})
	if v.OK {
		t.Fatal("要证明的事根本没进账本, 却判了通过")
	}
	if !strings.Contains(v.Text(), "压根没发生") {
		t.Fatalf("没说清是尺子坏了:\n%s", v.Text())
	}
	// **不能顺带说"开口 0 次符合预期"** —— 那会让人以为只是缺了点数据
	if strings.Contains(v.Text(), "符合") {
		t.Fatalf("这一天不作数, 却还在夸它的数字:\n%s", v.Text())
	}
}
