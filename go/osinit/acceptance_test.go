package osinit

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// **验收是两边的, 而且要能跑, 不能靠肉眼看日志.**
//
// S48–S50 把两天都跑通了(有事发生的那天开口 1 次、正是那件★;
// 全是琐事的那天开口 0 次). 但那个结论是我**逐条翻日志**得出来的 ——
// 翻对一次是运气, 每次都对才是性质.
//
// 而这件事已经有过一次教训: 对照日里那两次开门根本没进账本(尺子在
// 压缩下把它们弄没了), 而我是跑完之后才发现的. 一条能跑的验收会
// 当场说"你要证明的那件事压根没发生".
type dayCheck struct {
	Ledger   []abi.Event
	MustSay  []string // 这几件必须被说过(子串匹配)
	MustHave []string // 这几种信号必须真的进过账本 —— 否则这一天证明不了任何事
}

// judgeDay 一天跑完之后, 它到底证明了什么. **返回问题, 不直接断言** ——
// 于是"尺子坏掉的那一天"这件事本身也能被测.
func judgeDay(c dayCheck, wantNotices int) []string {
	r := AnalyzeLedger(c.Ledger)
	var probs []string
	// ① **先确认这一天真的发生过** —— 尺子坏掉的时候, 下面所有的
	// 结论都是空的, 而它们看起来跟"系统很安静"一模一样
	for _, need := range c.MustHave {
		found := false
		for _, k := range r.Kinds {
			if strings.Contains(k.Kind, need) {
				found = true
			}
		}
		if !found {
			probs = append(probs, "账本里根本没有 "+need+
				" —— 这一天要证明的事压根没发生, 下面的数字全是空的")
		}
	}
	if len(probs) > 0 {
		return probs // 这一天不作数, 别再拿它的数字说话
	}
	// ② 才轮到"它说了几次"
	if said := sumOf(r.Notices); said != wantNotices {
		probs = append(probs, fmt.Sprintf("开口 %d 次, 期望 %d 次", said, wantNotices))
	}
	return probs
}

func checkDay(t *testing.T, name string, c dayCheck, wantNotices int) {
	t.Helper()
	if probs := judgeDay(c, wantNotices); len(probs) > 0 {
		t.Fatalf("%s: %s", name, strings.Join(probs, "; "))
	}
}

func noticeEv(text string) abi.Event {
	return abi.Event{PID: signalPID, Kind: abi.EvInterrupt,
		Payload: map[string]any{"verdict": "deliver", "text": text}}
}

func sigKindEv(src, kind string) abi.Event {
	return abi.Event{PID: signalPID, Kind: abi.EvSignal, At: time.Now().UnixMilli(),
		Payload: map[string]any{"source": src, "kind": kind}}
}

// 有事发生的那天: 门开过, 而且它说了一次
func TestAcceptanceEventfulDay(t *testing.T) {
	checkDay(t, "有事发生的一天", dayCheck{
		Ledger: []abi.Event{
			sigKindEv("ha.light.bed", "device.state"),
			sigKindEv("ha.lock.front_door", "lock.opened"),
			noticeEv("家里没人，但前门刚刚被解锁了"),
		},
		MustHave: []string{"lock.opened"},
	}, 1)
}

// 对照日: 门也开过(必须!), 而它一次都没说
func TestAcceptanceQuietDay(t *testing.T) {
	checkDay(t, "全是琐事的一天", dayCheck{
		Ledger: []abi.Event{
			sigKindEv("ha.light.bed", "device.state"),
			sigKindEv("ha.lock.front_door", "lock.opened"),
			sigKindEv("ha.fan.living", "device.state"),
		},
		MustHave: []string{"lock.opened"},
	}, 0)
}

// **尺子坏掉的那一天要当场被判死, 不是被当成"很安静".**
//
// 对照日里的开门可能被时间压缩弄没, 账本里一条
// lock 信号都没有 —— 而"开口 0 次"看起来跟成功一模一样.
func TestAcceptanceCatchesBrokenRuler(t *testing.T) {
	probs := judgeDay(dayCheck{
		Ledger:   []abi.Event{sigKindEv("ha.light.bed", "device.state")},
		MustHave: []string{"lock.opened"},
	}, 0)
	if len(probs) == 0 {
		t.Fatal("要证明的那件事根本没进账本, 却被当成了'很安静' —— " +
			"而这正是真机上发生过的那一次")
	}
	if !strings.Contains(probs[0], "压根没发生") {
		t.Fatalf("报错没说清是尺子坏了: %v", probs)
	}
}
