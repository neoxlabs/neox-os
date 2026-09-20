package osinit

import (
	"sort"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 纠正 —— 用户明确改过方向的话.
//
// ── 为什么必须单独记, 而且必须 OS 来记 ──
//
// 事件日志里只有它做了什么, 没有"这是对的还是错的".
// "不是这样, 是那样"是唯一带标签的监督信号.
//
// 反面教材是"让 agent 自己记笔记": 失败模式是**忘了记**,
// 而且没有任何机制能发现它忘了. 所以认的动作在收话那一刻
// (Send), 不在工具表里.
//
// ── 认的要准, 不要广 ──
//
// 把猜测当事实存, 错了会自我强化. 宁可漏一条, 不要把
// "这是不是该用分"这种问句当成纠正.

const correctionsMaxLines = 8

// IsCorrection 这句是不是一次明确的方向纠正.
//
// 只认结构清楚的对照 / 撤回 / 点名"纠正". 问句不算.
func IsCorrection(text string) bool {
	s := strings.TrimSpace(text)
	if s == "" {
		return false
	}
	if isBareQuestion(s) {
		return false
	}
	low := strings.ToLower(s)

	if strings.Contains(s, "纠正") {
		return true
	}
	if strings.Contains(s, "算了还是") {
		return true
	}
	if strings.Contains(low, "i meant") || strings.HasPrefix(low, "i mean ") ||
		strings.Contains(low, " i mean ") {
		return true
	}

	if contrastCorrection(s) {
		return true
	}
	if useNotUse(s) || useNotUse(low) {
		return true
	}
	return false
}

func isBareQuestion(s string) bool {
	t := strings.TrimSpace(s)
	if !strings.HasSuffix(t, "？") && !strings.HasSuffix(t, "?") {
		return false
	}
	body := strings.TrimRight(t, "？?")
	body = strings.TrimSpace(body)
	// "不对，用分存？" 有对照, 仍然是纠正. 纯问句才排除.
	if contrastCorrection(body) || useNotUse(body) || strings.Contains(body, "纠正") ||
		strings.Contains(body, "算了还是") {
		return false
	}
	return true
}

func contrastCorrection(s string) bool {
	// 不是这样/那样/对, 后面还有下文
	for _, p := range []string{"不是这样", "不是那样", "不对"} {
		i := strings.Index(s, p)
		if i < 0 {
			continue
		}
		rest := strings.TrimLeft(s[i+len(p):], "，,。、. \t")
		rest = strings.TrimRight(rest, "？?！!")
		rest = strings.TrimSpace(rest)
		if rest == "" || rest == "吗" || rest == "呢" || rest == "吧" || rest == "啊" {
			continue
		}
		return true
	}
	// 不是 A，是 B
	if i := strings.Index(s, "不是"); i >= 0 {
		rest := s[i+len("不是"):]
		if j := strings.IndexAny(rest, "，,"); j >= 0 {
			if strings.Contains(rest[j:], "是") &&
				strings.TrimSpace(rest[:j]) != "" {
				return true
			}
		}
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "not like that") || strings.Contains(low, "that's wrong") ||
		strings.Contains(low, "thats wrong") {
		return true
	}
	return false
}

func useNotUse(s string) bool {
	// 用A不要用B / 用A别用B / don't use X, use Y
	if i := strings.Index(s, "不要用"); i > 0 && strings.Contains(s[:i], "用") {
		return true
	}
	if i := strings.Index(s, "别用"); i > 0 && strings.Contains(s[:i], "用") {
		return true
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "don't use") && strings.Contains(low, "use ") {
		return true
	}
	if strings.Contains(low, "do not use") && strings.Contains(low, "use ") {
		return true
	}
	return false
}

// CorrectionsDigest 给下一进程看的纠正摘要.
//
// 进程启动时定死一次, 同一段对话里逐字节不变 (跟 recent 同一条).
// 当前这段排除: 它已经在 Window 里.
//
// 拒绝也算: 点了"拒绝"是已经带标签的信号, 日志里有, 只是从没
// 被铺进下一进程能看见的地方.
func CorrectionsDigest(snap map[abi.ProcessID][]abi.Event, excludeThread string) string {
	type row struct {
		at   int64
		line string
	}
	var rows []row
	seen := map[string]bool{}
	for pid, evs := range snap {
		th := ThreadOf(evs)
		if th == "" {
			th = string(pid)
		}
		if excludeThread != "" && th == excludeThread {
			continue
		}
		didTitle := map[string]string{}
		for _, ev := range evs {
			switch ev.Kind {
			case abi.EvCorrection:
				t := strings.TrimSpace(payloadStr(ev, "text"))
				if t == "" {
					continue
				}
				line := "纠正：「" + clipRunes(oneLine(t), 40) + "」"
				if seen[line] {
					continue
				}
				seen[line] = true
				rows = append(rows, row{ev.At, line})
			case abi.EvDecideRequest:
				if id := payloadStr(ev, "did"); id != "" {
					didTitle[id] = strings.TrimSpace(presentTitle(ev))
				}
			case abi.EvDecideResolved:
				if payloadStr(ev, "choice") == "yes" {
					continue
				}
				title := didTitle[payloadStr(ev, "did")]
				if title == "" {
					title = payloadStr(ev, "choice")
				}
				if title == "" {
					continue
				}
				line := "拒绝：「" + clipRunes(oneLine(title), 40) + "」"
				if seen[line] {
					continue
				}
				seen[line] = true
				rows = append(rows, row{ev.At, line})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].at > rows[j].at })
	if len(rows) > correctionsMaxLines {
		rows = rows[:correctionsMaxLines]
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(r.line)
	}
	return b.String()
}
