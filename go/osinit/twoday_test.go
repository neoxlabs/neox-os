package osinit

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// **验收是两边的, 而两边必须用同一把尺子.**
//
// 两天都使用真实 Home Assistant、真实主动进程和真实模型完整运行:
//
//	有事发生的一天(没人在家时门开了)   21 条信号 · 判断 14 次 · **开口 1 次**
//	全是琐事的一天(人在家, 也开了两次门) 21 条信号 · 判断 20 次 · **开口 0 次**
//
// 两天用**同一份剧本骨架、同一批实体、同一个模型**, 差别只有一句话:
// "家里现在没人". 这个前提决定开门是否需要提醒, 必须作为剧本的一部分.
//
// 这条测试把"两边"绑在一起: 只验一边的话, 一个从不开口的系统能通过
// 对照日, 一个见门就喊的系统能通过有事那天 —— 而两个都是坏的.
func TestBothDaysMustPassTogether(t *testing.T) {
	eventful := []abi.Event{
		sigKindEv("ha.light.bed", "device.state"),
		sigKindEv("ha.lock.front_door", "lock.opened"),
		noticeEv("家里没人，但前门刚刚被解锁了"),
	}
	quiet := []abi.Event{
		sigKindEv("ha.light.bed", "device.state"),
		sigKindEv("ha.lock.front_door", "lock.opened"),
		sigKindEv("ha.fan.living", "device.state"),
	}
	for _, c := range []struct {
		name   string
		evs    []abi.Event
		want   DayExpect
		mustOK bool
	}{
		{"有事发生的一天", eventful,
			DayExpect{Name: "有事发生的一天", MustHave: []string{"lock.opened"}, Notices: 1}, true},
		{"全是琐事的一天", quiet,
			DayExpect{Name: "全是琐事的一天", MustHave: []string{"lock.opened"}, Notices: 0}, true},
		// **一个从不开口的系统**: 对照日照样过, 有事那天过不了
		{"从不开口的系统(拿对照日的账本去当有事那天)", quiet,
			DayExpect{Name: "有事发生的一天", MustHave: []string{"lock.opened"}, Notices: 1}, false},
		// **一个见门就喊的系统**: 有事那天过, 对照日过不了
		{"见门就喊的系统(拿有事那天的账本去当对照日)", eventful,
			DayExpect{Name: "全是琐事的一天", MustHave: []string{"lock.opened"}, Notices: 0}, false},
	} {
		v := JudgeDay(c.evs, c.want)
		if v.OK != c.mustOK {
			t.Errorf("%s: 判成 OK=%v, 该是 %v (%v)", c.name, v.OK, c.mustOK, v.Problems)
		}
	}
}

// 两边都过的那一份判决要**说得出是哪两天** —— 只说"通过"的话,
// 人没办法知道它验的是不是自己以为的那两天
func TestVerdictNamesTheDay(t *testing.T) {
	v := JudgeDay([]abi.Event{sigKindEv("ha.lock.front", "lock.opened")},
		DayExpect{Name: "全是琐事的一天(人在家)", MustHave: []string{"lock.opened"}})
	if !strings.Contains(v.Text(), "人在家") {
		t.Fatalf("判决没说清是哪一天:\n%s", v.Text())
	}
}
