package osinit

import (
	"fmt"
	"sort"

	"github.com/neox-os/neox-os/abi"
)

// 账本自洽性 —— 把"用眼睛核对"变成一道能跑的闸.
//
// ── 为什么值得单独做 ──
//
// 跑完一段完整的真实使用(13 轮混合操作)之后, 我是**逐条比对**
// 账本计数和 state.txt 才确认它自洽的:
//
//	wake.set 3 / wake.cancelled 2  →  剩 w3, 跟 state.txt 对得上
//	watch.set 1 / removed 0        →  剩 g1
//
// 对一次是运气, 每次改动都对才是性质. 而这类不一致**从表面看不出来**:
// state.txt 长得永远是对的(它就是内存对象的渲染), 错的是内存对象
// 跟账本的关系 —— 也就是重建那一步.
//
// 而重建那一步**已经出过一次事**: 闹钟和关注原来共用 w%d 前缀,
// 重启之后新 id 会顶掉旧的(S18 那次的 TestSeqSurvivesRestore 挡的就是它).
// 那种错的表现是"用户的某条提醒莫名其妙消失了", 没有任何报错.
//
// ── 判据都是"账本自己能证伪自己" ──
//
// 不需要拿 state.txt 来对: 账本里的事件顺序本身就蕴含了所有约束.
// 撤一个从没设过的 id、同一个 id 设两次 —— 这些都是账本内部矛盾,
// 一眼能判, 而且**跟当时的内存状态无关**, 所以可以对着任何一份
// 历史账本跑(包括从生产机器拷回来的).

// Problem 一处不自洽
type Problem struct {
	Kind string // 哪一类
	ID   string
	Why  string
}

func (p Problem) String() string {
	return fmt.Sprintf("[%s] %s: %s", p.Kind, p.ID, p.Why)
}

// LedgerState 账本重放出来的现状 —— 也就是重启之后**应该**是什么样
type LedgerState struct {
	Wakes   []string // 还没响也没撤的闹钟 id
	Watches []string // 还生效的关注 id
	Places  []string // 认得的地点名
}

// CheckLedger 重放账本, 报告不自洽的地方和最终现状.
//
// **空 problems 不等于系统没问题**, 只等于账本内部没有矛盾.
// 它挡的是重建这一步的错, 不是业务逻辑的错.
func CheckLedger(events []abi.Event) (LedgerState, []Problem) {
	var probs []Problem
	wakes := map[string]bool{}   // id → 还活着
	watches := map[string]bool{} //
	places := map[string]bool{}
	// seenWake/seenWatch 记"这个 id 出现过没有" —— 用来判"撤一个从没设过的"
	seenWake, seenWatch := map[string]bool{}, map[string]bool{}

	for _, e := range events {
		m, _ := e.Payload.(map[string]any)
		id, _ := m["id"].(string)
		switch e.Kind {
		case abi.EvWakeSet:
			if seenWake[id] && wakes[id] {
				// **同一个 id 设两次 = 前一条被顶掉了, 而且是静默的.**
				// S18 之前闹钟和关注共用 w%d 前缀, 重启之后就会这样 ——
				// 表现是"用户的某条提醒莫名其妙消失了", 没有任何报错.
				probs = append(probs, Problem{"wake", id,
					"同一个 id 被设了两次 —— 前一条被顶掉了, 而且没有任何报错"})
			}
			seenWake[id], wakes[id] = true, true
		case abi.EvWakeFired, abi.EvWakeCancelled:
			if !seenWake[id] {
				probs = append(probs, Problem{"wake", id,
					"撤/响了一个从来没设过的闹钟 —— 账本缺了 wake.set, " +
						"或者 id 对不上"})
			}
			delete(wakes, id)
		case abi.EvWatchSet:
			if seenWatch[id] && watches[id] {
				probs = append(probs, Problem{"watch", id,
					"同一个 id 被设了两次 —— 前一条被顶掉了"})
			}
			seenWatch[id], watches[id] = true, true
		case abi.EvWatchRemoved:
			if !seenWatch[id] {
				probs = append(probs, Problem{"watch", id,
					"撤了一个从来没设过的关注"})
			}
			delete(watches, id)
		case abi.EvPlaceNamed:
			if n, _ := m["name"].(string); n != "" {
				places[n] = true
			}
		}
		// **闹钟和关注的 id 不许撞** —— 撞了的话撤销会撤错对象,
		// 而用户要到下次该响时才发现
		if id != "" && seenWake[id] && seenWatch[id] {
			probs = append(probs, Problem{"id", id,
				"这个 id 同时是闹钟和关注 —— 撤销会撤错对象, " +
					"而用户要到下次该响时才发现"})
		}
	}
	return LedgerState{
		Wakes: keysOf(wakes), Watches: keysOf(watches), Places: keysOf(places),
	}, probs
}

// Matches 重放出来的现状跟活着的对象一不一致.
//
// 这一步才需要活的对象: 账本说该剩 w3, 而内存里剩的是 w1 的话,
// 说明**重建那一步错了** —— 而 state.txt 长得永远是对的
// (它就是内存对象的渲染), 从表面看不出来.
func (s LedgerState) Matches(w *Watches, t *Timers, p *Places) []Problem {
	var probs []Problem
	cmp := func(kind string, want []string, got []string) {
		wantSet := map[string]bool{}
		for _, x := range want {
			wantSet[x] = true
		}
		for _, g := range got {
			if !wantSet[g] {
				probs = append(probs, Problem{kind, g,
					"内存里有, 而账本重放出来没有 —— 它是哪儿冒出来的?"})
			}
			delete(wantSet, g)
		}
		for x := range wantSet {
			probs = append(probs, Problem{kind, x,
				"账本说它还在, 内存里却没有 —— 重启之后用户会发现它没了"})
		}
	}
	if t != nil {
		var ids []string
		for _, x := range t.Pending() {
			ids = append(ids, x.ID)
		}
		cmp("wake", s.Wakes, ids)
	}
	if w != nil {
		var ids []string
		for _, x := range w.List() {
			ids = append(ids, x.ID)
		}
		cmp("watch", s.Watches, ids)
	}
	if p != nil {
		var names []string
		for _, x := range p.Known() {
			names = append(names, x.Name)
		}
		cmp("place", s.Places, names)
	}
	return probs
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
