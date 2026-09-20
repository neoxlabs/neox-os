package osinit

import (
	"github.com/neox-os/neox-os/abi"
)

// 感知层账本压紧.
//
// ── 为什么需要它 ──
//
// S24 之后 sense 永不轮转 —— 它是这台机器的状态, 不是一段可以归档的
// 对话. 代价是它**只增不减**: 摘要按窗口产出, 一天百来条,
// 一年五万条, 而每次开机都要把它们从头读一遍再重建.
//
// 但这些事件里绝大多数对"现在还剩什么"**已经没有贡献**:
// 响过的闹钟、撤掉的关注、上上周的摘要 —— 读它们只是为了得出
// "它们不在了"这个结论.
//
// ── 唯一的正确性判据 ──
//
// **压紧前后, 重建出来的现状必须一模一样.** 允许少读事件,
// 不允许改变结论. 所以这里的每一条丢弃都要对得上一个重建函数:
//
//	places.Restore        地点 —— 一条都不丢(见下)
//	watches.Restore       撤掉的关注: set 和 removed 成对丢
//	timers.Restore        响过/撤过的闹钟: set 和终止事件成对丢
//	daily.Restore         只认最后一天 → 只留最后一份日报
//	SignalBus.RecoverPending  从最后一份摘要往后捞 → 只留最后一份摘要
//	                          和它之后的信号
//
// **不认得的种类一律留着**(interrupt.verdict、signal.late…).
// 压紧的安全性就建立在这条上: 将来有人加一种新事件、又加一个读它的
// 重建函数, 忘了改这里, 结果是账本大一点, 不是状态错一点.
//
// ── 地点为什么一条都不丢 ──
//
// 它是所有位置信号的语义锚点. 丢一个的后果不是"少个地名",
// 是"到达/离开"重新变回一串经纬度 —— 模型对那行字能做的判断为零
// 而地点总共也就几条, 省它没有意义.
//
// ── 踩到的: id 计数器会退回去 ──
//
// seqOf/watchSeqOf 是从**幸存事件的 id** 反推计数器的.
// 把 w1..w9 都压掉之后计数器归零, 下一个提醒又叫 w1 ——
// 而 w1 在归档里还活着. 那就是 S18 那个"撤销撤错对象"从另一扇门
// 回来了, 而且更隐蔽: 用户撤 w1 撤掉的可能是另一件事,
// 要到该响时才发现.
//
// 所以**每一类里 id 最大的那一对留着不动**: 留的是"设了又撤"的
// 完整一对, 计数器认得它, 而重建出来它照样是死的.
func compactSense(events []abi.Event) (keep, drop []abi.Event) {
	lastDigest, lastDaily := -1, -1
	// 终止过的 id —— 只有成对丢, 单丢 set 会让 CheckLedger 判
	// "撤了一个从来没设过的", 单丢终止事件会把死的闹钟复活
	deadWake, deadWatch := map[string]bool{}, map[string]bool{}
	maxWakeID, maxWatchID := "", ""
	maxWake, maxWatch := 0, 0

	for i, e := range events {
		m, _ := e.Payload.(map[string]any)
		id, _ := m["id"].(string)
		switch e.Kind {
		case abi.EvSignalDigest:
			lastDigest = i
		case abi.EvDailyReport:
			lastDaily = i
		case abi.EvWakeFired, abi.EvWakeCancelled:
			deadWake[id] = true
		case abi.EvWatchRemoved:
			deadWatch[id] = true
		}
		switch e.Kind {
		case abi.EvWakeSet:
			if n := seqOf(id); n > maxWake {
				maxWake, maxWakeID = n, id
			}
		case abi.EvWatchSet:
			if n := watchSeqOf(id); n > maxWatch {
				maxWatch, maxWatchID = n, id
			}
		}
	}

	for i, e := range events {
		m, _ := e.Payload.(map[string]any)
		id, _ := m["id"].(string)
		dead := false
		switch e.Kind {
		case abi.EvSignal:
			// 最后一份摘要之前的信号已经被总结过 —— 再捞回来
			// 等于把旧消息重播一遍
			dead = i < lastDigest
		case abi.EvSignalDigest:
			dead = i != lastDigest
		case abi.EvDailyReport:
			dead = i != lastDaily
		case abi.EvWakeSet, abi.EvWakeFired, abi.EvWakeCancelled:
			dead = deadWake[id] && id != maxWakeID
		case abi.EvWatchSet, abi.EvWatchRemoved:
			dead = deadWatch[id] && id != maxWatchID
		}
		if dead {
			drop = append(drop, e)
		} else {
			keep = append(keep, e)
		}
	}
	return keep, drop
}
