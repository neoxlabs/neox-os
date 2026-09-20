package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/neox-os/neox-os/osinit"
)

// agent 求 OS 办的事 —— 记地点 / 盯着 / 设提醒 / 撤销.
//
// ── 为什么拎出来 ──
//
// 原来是订阅回调里的四段 if. **这段逻辑一旦漏接, 而且会静默失败**:
// 补丁的缩进没对上, 替换一声不吭地没生效, go build 照样绿、单测也绿,
// 只有出现"工具说设好了、账本里一条 wake 都没有"时才会暴露.
//
// 拎出来之后它能被测 —— 而第一条测试就抓到了下面这个送错门牌号的 bug.
//
// ── 失败要送回**问的那个进程** ──
//
// 主动进程(没人在等它, 自己盯着外面)也会设闹钟. 设失败时, 原来那句
// "你刚才那个操作没成功"是发给 current 的 —— 也就是**另一段对话**:
//
//	· 主动进程不知道自己失败了, 它会照着"设好了"往下说
//	· 而正在跟用户聊别的事的那个进程, 莫名收到一条它没做过的操作的失败
//
// 这条回话的作用就是报告失败, 却被送到了错误的进程.
// 事件里带着 e.PID, 用它就行.
// errNoSenseLayer 撤销要感知层在跑 —— 它管着地点/关注/闹钟的现状表
var errNoSenseLayer = errors.New("没开感知层, 撤不了")

type osRequests struct {
	Places  *osinit.Places
	Watches *osinit.Watches
	Timers  *osinit.Timers
	// Cancel 撤销. 感知层没开时是 nil
	Cancel func(id string) (string, error)
	// Thread 当前这段对话的线头 —— 闹钟响了要接回它
	Thread func() string
	// Print 打给用户看
	Print func(line string)
	// Reply 把话送回**问的那个进程**
	Reply func(pid, msg string)
}

// Handle 处理一条 agent 的请求. 返回它管不管这个 phase ——
// **不管的要交出去**, 否则渲染那一侧就再也看不到了
func (r *osRequests) Handle(pid string, m map[string]any) bool {
	fail := func(what string, err error) {
		r.Print(fmt.Sprintf("\n  ⚠ %s: %v\n> ", what, err))
		if r.Reply != nil && pid != "" {
			r.Reply(pid, fmt.Sprintf(
				"【系统】你刚才那个操作**没成功**: %s —— %v。"+
					"不要对用户说已经设好了; 把这个情况和下一步告诉他。", what, err))
		}
	}
	switch m["phase"] {
	case "name_place":
		if pl, err := r.Places.NameHere(str(m["name"]), 0); err != nil {
			fail("命名失败", err)
		} else {
			r.Print(fmt.Sprintf("\n  📍 记下了: %s\n> ", pl.Name))
		}
	case "cancel":
		if r.Cancel == nil {
			r.Print("\n  ⚠ 没开感知层, 撤不了\n> ")
		} else if detail, err := r.Cancel(str(m["id"])); err != nil {
			fail("撤销失败", err)
		} else {
			r.Print(fmt.Sprintf("\n  ✂ %s\n> ", detail))
		}
	case "watch":
		if it, err := r.Watches.Add(str(m["kind"]), str(m["place"]),
			str(m["say"]), str(m["raw"])); err != nil {
			fail("关注没设成", err)
		} else {
			what := it.Kind
			if it.Place != "" {
				what += " @" + it.Place
			}
			r.Print(fmt.Sprintf("\n  👁 盯上了: %s\n> ", what))
		}
	case "remind":
		if w, err := r.Timers.Set(r.Thread(),
			int64(asFloat(m["at"])), str(m["text"])); err != nil {
			fail("闹钟没设成", err)
		} else {
			r.Print(fmt.Sprintf("\n  ⏰ 记下了: %s %s\n> ",
				time.UnixMilli(w.At).Format("01-02 15:04"), w.Text))
		}
	default:
		return false
	}
	return true
}
