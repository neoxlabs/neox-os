package osinit

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neox-os/neox-os/abi"
)

// 查过去 —— 事件日志一直在写, 缺的是检索.
//
// ── 为什么是 OS 服务, 不是卷里的一个文件 ──
//
// 信号那条路写成了 .neox/signals.txt (见 signalview.go): 一行一条,
// agent 用现成的 search 就能查, 不需要新 ABI.
//
// 对话日志走不成那条路. 一条对话是上百个事件, 工具结果还截过断,
// 倒进卷里等于再造一份真相源, 而且 agent 对卷有写权限 ——
// 改了signals.txt 下次重写就回来, 改了对话副本就分叉了.
//
// 现在 MSearch / MSee 已经证明"OS 服务回值"这条路是通的,
// signalview 当时反对 recall 系统调用的理由 (第一个需要回值的调用)
// 不再成立.
//
// ── 不进提示词 ──
//
// 要查哪一条是会话中途才知道的. 塞进系统段会作废前缀缓存,
// 而且把全部历史塞进去会把真正有用的东西挤掉.
// grep + 结构化字段, 这个量级比向量库准, 查不出来也查得出为什么.

const (
	recallDefaultN = 8
	recallMaxHits  = 20
	recallSnippet  = 240
)

// Recall 从账本里找出对得上的过去.
//
// caller 是正在问的那个进程: **它自己这段对话排除掉**.
// 当前这段的历史它本来就看得见 (Window), 查出来再喂一遍
// 会让它以为那是"以前的事".
func Recall(snap map[abi.ProcessID][]abi.Event, caller abi.ProcessID, query string, limit int) []abi.RecallHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	n := limit
	if n <= 0 {
		n = recallDefaultN
	}
	if n > recallMaxHits {
		n = recallMaxHits
	}

	skip := threadOfPID(snap, caller)

	type scored struct {
		hit abi.RecallHit
		at  int64
	}
	var found []scored
	for pid, evs := range snap {
		if threadOfPID(snap, pid) == skip {
			continue
		}
		title := conversationTitle(evs)
		for _, ev := range evs {
			kind, text, ok := indexable(ev)
			if !ok {
				continue
			}
			if !strings.Contains(strings.ToLower(text), q) {
				continue
			}
			found = append(found, scored{
				hit: abi.RecallHit{
					When:  formatWhen(ev.At),
					Title: title,
					Kind:  kind,
					Text:  clipRunes(text, recallSnippet),
				},
				at: ev.At,
			})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at > found[j].at })
	if len(found) > n {
		found = found[:n]
	}
	out := make([]abi.RecallHit, len(found))
	for i, s := range found {
		out[i] = s.hit
	}
	return out
}

func threadOfPID(snap map[abi.ProcessID][]abi.Event, pid abi.ProcessID) string {
	th := ThreadOf(snap[pid])
	if th == "" {
		return string(pid)
	}
	return th
}

func conversationTitle(evs []abi.Event) string {
	for _, ev := range evs {
		switch ev.Kind {
		case abi.EvInputRecv:
			if t := payloadStr(ev, "text"); t != "" {
				return clipRunes(oneLine(t), 40)
			}
		case abi.EvProcOutput:
			if payloadStr(ev, "phase") == "start" {
				if t := payloadStr(ev, "task"); t != "" {
					return clipRunes(oneLine(t), 40)
				}
			}
		}
	}
	return "(没有开场白)"
}

// indexable 一条事件能不能、该不该被搜到.
//
// 信号 / 闹钟 / 地点 / 关注 不进: 它们已经有自己的视图文件
// (.neox/signals.txt / .neox/state.txt), 再搜一遍是第二个真相源.
// 工具结果原文不进: 日志里是 trunc(out, 200), 不是上下文的真相
// (见 agent.RestoreWindow 的说明). 动作留下, 结果丢掉.
func indexable(ev abi.Event) (kind, text string, ok bool) {
	switch ev.Kind {
	case abi.EvInputRecv:
		t := strings.TrimSpace(payloadStr(ev, "text"))
		if t == "" {
			return "", "", false
		}
		return "用户说", t, true

	case abi.EvCorrection:
		t := strings.TrimSpace(payloadStr(ev, "text"))
		if t == "" {
			return "", "", false
		}
		return "他纠正过", t, true

	case abi.EvProcOutput:
		phase := payloadStr(ev, "phase")
		switch phase {
		case "start":
			t := strings.TrimSpace(payloadStr(ev, "task"))
			if t == "" {
				return "", "", false
			}
			return "用户说", t, true
		case "reply":
			t := strings.TrimSpace(payloadStr(ev, "text"))
			if t == "" {
				return "", "", false
			}
			return "你答了", t, true
		case "done":
			t := strings.TrimSpace(payloadStr(ev, "summary"))
			if t == "" {
				return "", "", false
			}
			return "你答了", t, true
		case "step":
			t := compactStep(ev)
			if t == "" {
				return "", "", false
			}
			return "你做了", t, true
		default:
			return "", "", false
		}

	case abi.EvDecideRequest:
		t := strings.TrimSpace(presentTitle(ev))
		if t == "" {
			return "", "", false
		}
		return "问过他", t, true

	case abi.EvDecideResolved:
		choice := payloadStr(ev, "choice")
		if choice == "" {
			return "", "", false
		}
		label := "他拒了"
		if choice == "yes" {
			label = "他批了"
		}
		return label, choice, true

	default:
		return "", "", false
	}
}

func compactStep(ev abi.Event) string {
	tool := payloadStr(ev, "tool")
	if tool == "" {
		return ""
	}
	m, ok := ev.Payload.(map[string]any)
	if !ok {
		return tool
	}
	args, _ := m["args"].(map[string]any)
	if args == nil {
		return tool
	}
	for _, k := range []string{"path", "url", "command", "query", "text"} {
		if s, _ := args[k].(string); strings.TrimSpace(s) != "" {
			return tool + " " + clipRunes(oneLine(s), 80)
		}
	}
	return tool
}

func presentTitle(ev abi.Event) string {
	m, ok := ev.Payload.(map[string]any)
	if !ok {
		return ""
	}
	switch p := m["present"].(type) {
	case map[string]any:
		s, _ := p["title"].(string)
		return s
	case abi.PresentSpec:
		return p.Title
	default:
		return ""
	}
}

func payloadStr(ev abi.Event, k string) string {
	m, ok := ev.Payload.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func formatWhen(at int64) string {
	if at <= 0 {
		return ""
	}
	// UTC, 绝对时间. 相对说法会在压缩重放时失真 (S73).
	return time.UnixMilli(at).UTC().Format("2006-01-02 15:04")
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// (o *OS) Recall 给 ABI 用. 上限在这里夹, 不在进程那边.
func (o *OS) Recall(caller abi.ProcessID, p abi.RecallParams) abi.RecallResult {
	return abi.RecallResult{Hits: Recall(o.events.Snapshot(), caller, p.Query, p.Limit)}
}
