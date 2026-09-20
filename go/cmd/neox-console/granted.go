package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

/**
 * granted —— **批了之后, 得有人叫它一声**.
 *
 * ── 为什么需要自动唤醒 ──
 *
 *	进程申请连接 pypi.org 来安装 pytest, 获得批准后的回话是:
 *
 *	    "授权已生效，**下一句话**我就装 pytest 并跑测试，跑通后合进主干。"
 *
 *	然后它停在那儿. budget.py 已经写好躺在工作区里, 测试也写好了,
 *	主干还是坏的 —— 而**中间只差一句"继续"**.
 *
 *	它没做错: 内核的约束在进程启动时定死, 只能收紧不能放宽, 所以这一轮
 *	确实用不上新授权(见 agent/access.go). 提示词里也是这么教它的.
 *
 *	问题在**谁来说那句"继续"**. 原来的答案是"用户" —— 而用户可能已经
 *	走开了, 他刚做的事恰恰是"点了同意". 一个人点完同意就离开, 是最
 *	正常不过的事.
 *
 * ── 为什么不怕转圈 ──
 *
 *	只在**明确批准**之后叫一次, 而且同一条决策只叫一次(seen).
 *	被拒绝的不叫 —— 那时候它该做的是换条路, 不是重试.
 */

// nudged 已经叫过的那些决策 —— 同一条只叫一次
var nudged sync.Map

/**
 * watchGrants 盯着"批准"这件事: 批完就把那个人叫起来接着干.
 *
 *	**要等它这一轮真的停下来**: 批准发生在它还在跑的时候(它卡在
 *	decide 上等), 立刻投一句话进去只会排在收件箱里, 而它这一轮
 *	结束时会把整个收件箱当成"用户又说了一堆" —— 那反而乱.
 */
func watchGrants(o *osinit.OS) func() {
	return o.Log().SubscribeAll(func(e abi.Event) {
		if e.Kind != abi.EvDecideResolved {
			return
		}
		body, _ := e.Payload.(map[string]any)
		did, _ := body["did"].(string)
		choice, _ := body["choice"].(string)
		if did == "" || choice != "yes" {
			return
		}
		if _, already := nudged.LoadOrStore(did, true); already {
			return
		}
		go nudgeWhenIdle(o, e.PID, did)
	})
}

// nudgeWhenIdle 等它这一轮走完, 再叫.
func nudgeWhenIdle(o *osinit.OS, pid abi.ProcessID, did string) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		info, ok := o.Info(pid)
		if !ok || info.State.IsTerminal() {
			return
		}
		if info.State == abi.StateRunning {
			continue // 还在跑, 等它自己走完这一轮
		}
		// 停下来了 —— 但要确认它是真停在"等下一句话"上, 而不是又卡在
		// 另一条决策上(那时候需要等待决策, 不能催它)
		if anyOpenDecision(o, pid) {
			continue
		}
		what := grantedFor(o, pid, did)
		text := fmt.Sprintf("[系统] %s批了，接着干吧。", what)
		o.Deliver(pid, osinit.Delivery{Text: text, Said: text, From: "系统"})
		return
	}
}

// grantedFor 批的是什么 —— 说清楚一点, 它才知道接着干哪件事
func grantedFor(o *osinit.OS, pid abi.ProcessID, did string) string {
	for _, e := range o.Log().Replay(pid, 0) {
		if e.Kind != abi.EvDecideRequest {
			continue
		}
		body, _ := e.Payload.(map[string]any)
		if got, _ := body["did"].(string); got != did {
			continue
		}
		present, _ := body["present"].(map[string]any)
		title, _ := present["title"].(string)
		if line := firstLine(strings.TrimSpace(title), 60); line != "" {
			return "「" + line + "」"
		}
	}
	return "那个权限"
}

// anyOpenDecision 它是不是还卡在别的决策上
func anyOpenDecision(o *osinit.OS, pid abi.ProcessID) bool {
	open := map[string]bool{}
	for _, e := range o.Log().Replay(pid, 0) {
		body, _ := e.Payload.(map[string]any)
		did, _ := body["did"].(string)
		if did == "" {
			continue
		}
		switch e.Kind {
		case abi.EvDecideRequest:
			open[did] = true
		case abi.EvDecideResolved:
			delete(open, did)
		}
	}
	return len(open) > 0
}
