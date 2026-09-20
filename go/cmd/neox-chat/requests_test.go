package main

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/osinit"
)

func newRequests(t *testing.T) (*osRequests, *[]string, *map[string]string) {
	t.Helper()
	log := osinit.NewEventLog(func() int64 { return time.Now().UnixMilli() })
	var printed []string
	replies := map[string]string{}
	r := &osRequests{
		Places:  osinit.NewPlaces(log),
		Watches: osinit.NewWatches(log),
		Timers:  osinit.NewTimers(log, nil, func(osinit.Wake, int64) {}),
		Thread:  func() string { return "t1" },
		Print:   func(s string) { printed = append(printed, s) },
		Reply:   func(pid, msg string) { replies[pid] = msg },
	}
	return r, &printed, &replies
}

// **失败要送回**问的那个进程**, 不是当前对话.**
//
// 主动进程(它没人在等着, 自己盯着外面)也会设闹钟. 设失败时,
// 原来那句"你刚才那个操作没成功"是发给 current 的 —— 也就是
// **另一段对话**. 于是:
//
//	· 主动进程不知道自己失败了, 它会照着"设好了"往下说
//	· 而正在跟用户聊别的事的那个进程, 莫名收到一条它没做过的操作的失败
//
// 这正是 rejected 这个状态存在的全部理由: 未确认成功时不能对外说已经设好了,
// 却送错了门牌号.
func TestFailureGoesBackToTheAskingProcess(t *testing.T) {
	r, _, replies := newRequests(t)
	// 时间给成过去的 —— 闹钟一定设不成
	r.Handle("p-sense", map[string]any{"phase": "remind",
		"at": float64(time.Now().Add(-time.Hour).UnixMilli()), "text": "该吃药了"})

	if (*replies)["p-sense"] == "" {
		t.Fatalf("失败没送回问的那个进程, 送到了 %v —— "+
			"主动进程会照着'设好了'往下说", *replies)
	}
	if !strings.Contains((*replies)["p-sense"], "没成功") {
		t.Fatalf("送回去的话没说清失败: %q", (*replies)["p-sense"])
	}
}

// 成功就不该往对话里塞话 —— 那是噪音, 而且会让模型以为出了事
func TestSuccessSaysNothingToTheProcess(t *testing.T) {
	r, printed, replies := newRequests(t)
	r.Handle("p1", map[string]any{"phase": "remind",
		"at": float64(time.Now().Add(time.Hour).UnixMilli()), "text": "开会"})
	if len(*replies) != 0 {
		t.Fatalf("设成功了还往对话里塞了话: %v", *replies)
	}
	if len(*printed) != 1 || !strings.Contains((*printed)[0], "开会") {
		t.Fatalf("成功那一行没打给用户看: %v", *printed)
	}
}

// 不是它管的 phase 要交出去 —— 否则渲染那一侧就再也看不到了
func TestUnhandledPhasePassesThrough(t *testing.T) {
	r, _, _ := newRequests(t)
	if r.Handle("p1", map[string]any{"phase": "step"}) {
		t.Fatal("把不归它管的 phase 吞了")
	}
}

// **它管的那几个不能再被渲染打一遍.**
//
// S36 给 renderStep 加了"不认得的也打一行"的兜底, 而 remind/name_place/
// watch/cancel 这四个是**由这里处理并自己打了更好的一行**的 ——
// 于是终端上变成了:
//
//	⏰ 记下了: 08-16 21:00 该吃药了
//	· remind                      ← 这一行是我上一轮引入的噪音
//
// 闸没抓到, 是因为它只扫 agent.go/os.go, 而这四个从 agentside.go 发出来.
func TestHandledPhasesAreNotRenderedAgain(t *testing.T) {
	for _, p := range []string{"remind", "name_place", "watch", "cancel"} {
		if out := renderStep(map[string]any{"phase": p}); out != "" {
			t.Errorf("%s 被渲染又打了一遍: %q", p, out)
		}
	}
}
