package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// 并发批次做过的事, 恢复之后不能不见.
//
// RestoreWindow 原来只认 `phase: "step"`(单工具那条路), 而 agent 一次调
// 多个互不依赖的工具时发的是 `phase: "batch"` —— 那条事件里 tools 和 args
// 都带着, 只是没人读.
//
// 后果落在"隔一段时间回来继续"这条路上: 用批次做过的活全部消失,
// 它回来会以为自己没做过 —— 要么重做一遍(白花钱), 要么把没做的说成做了.
func TestRestoreKeepsParallelBatches(t *testing.T) {
	evs := []abi.Event{
		out2("p1", 1, map[string]any{"phase": "start", "task": "盘点项目"}),
		out2("p1", 2, map[string]any{"phase": "batch", "n": 2,
			"tools": []any{"read_file", "read_file"},
			"args": []any{
				map[string]any{"path": "a.py"},
				map[string]any{"path": "b.py"},
			}}),
		out2("p1", 3, map[string]any{"phase": "step", "tool": "list_dir",
			"args": map[string]any{"path": "src"}}),
		out2("p1", 4, map[string]any{"phase": "reply", "text": "看完了"}),
	}
	w := RestoreWindow(engine.NewPageStore(), evs, "接着干")
	msgs, _ := w.Messages()
	all := ""
	for _, m := range msgs {
		all += m.Content
		for _, c := range m.ToolCalls {
			all += c.Name + c.Arguments
		}
	}
	for _, want := range []string{"a.py", "b.py"} {
		if !strings.Contains(all, want) {
			t.Fatalf("批次里做过的 %s 恢复之后不见了 —— 它会以为自己没做过:\n%s", want, all)
		}
	}
	// 单工具那条路本来就在, 别改坏了
	if !strings.Contains(all, "src") {
		t.Fatalf("单工具的动作也丢了:\n%s", all)
	}
}

// 恢复出来的每个动作后面都得跟一条结果占位 —— 一串没有结果的动作
// 比明说"结果没保留"更让模型糊涂, 而且会留下没有结果的调用.
func TestRestoredBatchActionsEachGetAPlaceholder(t *testing.T) {
	evs := []abi.Event{
		out2("p1", 1, map[string]any{"phase": "start", "task": "盘点"}),
		out2("p1", 2, map[string]any{"phase": "batch", "n": 3,
			"tools": []any{"read_file", "read_file", "read_file"},
			"args": []any{
				map[string]any{"path": "a"},
				map[string]any{"path": "b"},
				map[string]any{"path": "c"},
			}}),
	}
	w := RestoreWindow(engine.NewPageStore(), evs, "接着干")
	msgs, _ := w.Messages()
	acts, results := 0, 0
	for _, m := range msgs {
		if strings.Contains(m.Content, "read_file") || len(m.ToolCalls) > 0 {
			acts++
		}
		if strings.Contains(m.Content, "结果没有保留下来") {
			results++
		}
	}
	if acts == 0 {
		t.Fatal("批次一条都没恢复")
	}
	if results < acts {
		t.Fatalf("有 %d 个动作只有 %d 条结果占位 —— 没有结果的动作会让模型糊涂", acts, results)
	}
}

func out2(pid abi.ProcessID, at int64, m map[string]any) abi.Event {
	return abi.Event{PID: pid, At: at, Kind: abi.EvProcOutput, Payload: m}
}
