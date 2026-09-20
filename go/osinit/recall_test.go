package osinit

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neox-os/neox-os/abi"
)

func recEv(pid abi.ProcessID, at int64, kind abi.EventKind, payload map[string]any) abi.Event {
	return abi.Event{PID: pid, At: at, Kind: kind, Payload: payload}
}

func recOut(pid abi.ProcessID, at int64, payload map[string]any) abi.Event {
	return recEv(pid, at, abi.EvProcOutput, payload)
}

func recThread(pid abi.ProcessID, thread string) abi.Event {
	return recEv(pid, 1, abi.EvProcState, map[string]any{
		"labels": map[string]string{"thread": thread},
	})
}

func sampleLog() map[abi.ProcessID][]abi.Event {
	return map[abi.ProcessID][]abi.Event{
		"old": {
			recThread("old", "t-old"),
			recOut("old", 1_700_000_000_000, map[string]any{"phase": "start", "task": "做个记账小工具"}),
			recEv("old", 1_700_000_001_000, abi.EvInputRecv, map[string]any{"text": "账本用分不要用元"}),
			recOut("old", 1_700_000_002_000, map[string]any{
				"phase": "step", "tool": "write_file",
				"args": map[string]any{"path": "ledger.py"}}),
			recOut("old", 1_700_000_003_000, map[string]any{
				"phase": "done", "summary": "记账按分存, 文件已写好"}),
		},
		"now": {
			recThread("now", "t-now"),
			recOut("now", 1_700_000_100_000, map[string]any{"phase": "start", "task": "现在这段"}),
			recEv("now", 1_700_000_101_000, abi.EvInputRecv, map[string]any{"text": "账本用分不要用元"}),
		},
	}
}

func TestRecallFindsPastUserWords(t *testing.T) {
	hits := Recall(sampleLog(), "now", "用分", 8)
	if len(hits) != 1 {
		t.Fatalf("该只命中过去那段, 得到 %d 条: %+v", len(hits), hits)
	}
	if hits[0].Kind != "用户说" || !strings.Contains(hits[0].Text, "用分不要用元") {
		t.Fatalf("没命中那句纠正: %+v", hits[0])
	}
	if hits[0].Title != "做个记账小工具" {
		t.Fatalf("标题该是开场白, 不是 pid: %q", hits[0].Title)
	}
}

func TestRecallExcludesCurrentThread(t *testing.T) {
	// 当前这段里也有"用分", 但那是 Window 里已经看得见的
	hits := Recall(sampleLog(), "now", "用分", 8)
	for _, h := range hits {
		if h.Title == "现在这段" {
			t.Fatalf("把当前这段也搜出来了: %+v", h)
		}
	}
}

func TestRecallExcludesResumedSiblings(t *testing.T) {
	// 接续是新进程, 但同一条线 —— 排除必须按 thread 不是按 pid
	snap := sampleLog()
	snap["now2"] = []abi.Event{
		recThread("now2", "t-now"),
		recOut("now2", 1_700_000_200_000, map[string]any{"phase": "start", "task": "接着刚才"}),
		recEv("now2", 1_700_000_201_000, abi.EvInputRecv, map[string]any{"text": "账本用分不要用元"}),
	}
	hits := Recall(snap, "now2", "用分", 8)
	for _, h := range hits {
		if strings.Contains(h.Title, "现在这段") || strings.Contains(h.Title, "接着刚才") {
			t.Fatalf("同一条线的兄弟进程没排除: %+v", h)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("该只剩过去那段, 得到 %d: %+v", len(hits), hits)
	}
}

func TestRecallSkipsSignals(t *testing.T) {
	snap := map[abi.ProcessID][]abi.Event{
		"sense": {
			recEv("sense", 1, abi.EvSignal, map[string]any{
				"text": "前门被打开解锁了", "kind": "lock.opened"}),
		},
		"chat": {
			recThread("chat", "t1"),
			recOut("chat", 2, map[string]any{"phase": "start", "task": "嗨"}),
		},
	}
	hits := Recall(snap, "chat", "前门", 8)
	if len(hits) != 0 {
		t.Fatalf("信号不该进 recall, 它们已经有 signals.txt: %+v", hits)
	}
}

func TestRecallSkipsToolResults(t *testing.T) {
	snap := map[abi.ProcessID][]abi.Event{
		"old": {
			recThread("old", "t-old"),
			recOut("old", 1, map[string]any{"phase": "start", "task": "改文件"}),
			recOut("old", 2, map[string]any{
				"phase": "tool_ok", "tool": "read_file",
				"result": "secret-token-should-not-be-searchable"}),
		},
		"now": {recThread("now", "t-now")},
	}
	hits := Recall(snap, "now", "secret-token", 8)
	if len(hits) != 0 {
		t.Fatalf("工具结果原文不该能搜到: %+v", hits)
	}
}

func TestRecallFindsWhatItDid(t *testing.T) {
	hits := Recall(sampleLog(), "now", "ledger.py", 8)
	if len(hits) != 1 || hits[0].Kind != "你做了" {
		t.Fatalf("该命中那次 write_file: %+v", hits)
	}
	if !strings.Contains(hits[0].Text, "write_file") || !strings.Contains(hits[0].Text, "ledger.py") {
		t.Fatalf("动作没把工具和路径写清楚: %+v", hits[0])
	}
}

func TestRecallNewestFirstAndClamps(t *testing.T) {
	snap := map[abi.ProcessID][]abi.Event{
		"a": {
			recThread("a", "ta"),
			recEv("a", 100, abi.EvInputRecv, map[string]any{"text": "同一个词 早"}),
			recEv("a", 300, abi.EvInputRecv, map[string]any{"text": "同一个词 晚"}),
			recEv("a", 200, abi.EvInputRecv, map[string]any{"text": "同一个词 中"}),
		},
		"now": {recThread("now", "tn")},
	}
	hits := Recall(snap, "now", "同一个词", 2)
	if len(hits) != 2 {
		t.Fatalf("该被夹到 2 条: %d", len(hits))
	}
	if !strings.Contains(hits[0].Text, "晚") || !strings.Contains(hits[1].Text, "中") {
		t.Fatalf("没按时间新到旧: %+v", hits)
	}
	// 进程要 100 条也只能拿到上限
	many := Recall(snap, "now", "同一个词", 999)
	if len(many) > recallMaxHits {
		t.Fatalf("上限没夹住: %d", len(many))
	}
}

func TestRecallEmptyQueryIsNothing(t *testing.T) {
	if hits := Recall(sampleLog(), "now", "  ", 8); len(hits) != 0 {
		t.Fatalf("空词不该搜: %+v", hits)
	}
}

func TestRecallWhenIsAbsoluteUTC(t *testing.T) {
	hits := Recall(sampleLog(), "now", "用分", 8)
	if len(hits) == 0 {
		t.Fatal("没命中")
	}
	// 1_700_000_001_000 ms = 2023-11-14 22:13:21 UTC
	if !strings.HasPrefix(hits[0].When, "2023-11-14") {
		t.Fatalf("时间不是绝对 UTC 日期: %q", hits[0].When)
	}
	if strings.Contains(hits[0].When, "分钟前") {
		t.Fatalf("用了相对时间, 压缩重放会失真: %q", hits[0].When)
	}
}

func TestRecallSnippetIsBounded(t *testing.T) {
	long := strings.Repeat("账本用分", 200)
	snap := map[abi.ProcessID][]abi.Event{
		"old": {
			recThread("old", "t-old"),
			recEv("old", 1, abi.EvInputRecv, map[string]any{"text": long}),
		},
		"now": {recThread("now", "t-now")},
	}
	hits := Recall(snap, "now", "用分", 8)
	if len(hits) != 1 {
		t.Fatalf("该命中: %d", len(hits))
	}
	body := strings.TrimSuffix(hits[0].Text, "…")
	if utf8.RuneCountInString(body) > recallSnippet {
		t.Fatalf("摘要没裁: %d 字", utf8.RuneCountInString(hits[0].Text))
	}
	if !strings.HasSuffix(hits[0].Text, "…") {
		t.Fatal("超长没标省略, 模型会以为看到了全文")
	}
}

func TestRecallFindsDecisionTitle(t *testing.T) {
	snap := map[abi.ProcessID][]abi.Event{
		"old": {
			recThread("old", "t-old"),
			recOut("old", 1, map[string]any{"phase": "start", "task": "装包"}),
			recEv("old", 2, abi.EvDecideRequest, map[string]any{
				"did":     "d1",
				"present": map[string]any{"title": "允许连 pypi.org 吗?"},
			}),
			recEv("old", 3, abi.EvDecideResolved, map[string]any{"did": "d1", "choice": "yes"}),
		},
		"now": {recThread("now", "t-now")},
	}
	hits := Recall(snap, "now", "pypi.org", 8)
	if len(hits) != 1 || hits[0].Kind != "问过他" {
		t.Fatalf("该命中那次审批: %+v", hits)
	}
}

func TestRecallCaseInsensitive(t *testing.T) {
	hits := Recall(sampleLog(), "now", "LEDGER.PY", 8)
	if len(hits) != 1 {
		t.Fatalf("大小写不该挡住: %+v", hits)
	}
}
