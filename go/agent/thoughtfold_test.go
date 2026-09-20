package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/engine"
)

// 每步带一大段推理时, 折叠不能退化成"每步折一条".
//
// 真机形态: 起初 7 条、3 条、2 条, 之后**每步 1 条一直到底**(21 步里 16 次).
// 而每折一次就改写一次历史、打断一次前缀缓存 —— 折叠那一步的未命中
// token 大约是不折那步的两倍(21938 vs 11473), 16 次多花十几万.
//
// 成因: 折叠只认工具结果, 而模型每步附带的推理(5000~6800 output tokens
// ≈ 15~20KB)从来不折, 无限累积把预算吃满, 逼得每步去折仅剩的那一条.
func TestLongThoughtsDoNotDegradeFoldingToOnePerStep(t *testing.T) {
	count := func(thoughtBytes int) (folds, ones int) {
		w := NewWindow(engine.NewPageStore(), "读日志", 160000)
		for step := 0; step < 18; step++ {
			id := fmt.Sprintf("c%d", step)
			w.AppendBatch([]PageCall{{ID: id, Tool: "read_file",
				Args: map[string]any{"path": "big.log", "offset": step * 200}}},
				strings.Repeat("想", thoughtBytes/3))
			w.AppendResult(strings.Repeat("x", 26000)+fmt.Sprint(step), "", id)
			if _, tr := w.Messages(); tr.Dropped > 0 {
				folds++
				if tr.Dropped == 1 {
					ones++
				}
			}
		}
		return
	}
	_, onesNoThought := count(0)
	if onesNoThought != 0 {
		t.Fatalf("没有推理时本来就该是成批折, 却出现了 %d 次单条折", onesNoThought)
	}
	folds, ones := count(18000)
	if ones > 0 {
		t.Fatalf("带长推理时退化成每步折一条(%d 次) —— 每次都打断一次前缀缓存", ones)
	}
	if folds == 0 {
		t.Fatal("一次都没折, 这个用例什么都没验到")
	}
}

// 收掉的推理要换成**非空**的短标记.
//
// 带 tool_calls 的消息必须回传 reasoning_content, 空的会被供应商直接拒掉 ——
// 这条是客户端那边用 400 换回来的经验, 不该再付一次.
func TestTrimmedThoughtIsNotEmpty(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "读日志", 80000)
	for step := 0; step < 8; step++ {
		id := fmt.Sprintf("c%d", step)
		w.AppendBatch([]PageCall{{ID: id, Tool: "read_file",
			Args: map[string]any{"path": "big.log", "offset": step * 200}}},
			strings.Repeat("想", 6000))
		w.AppendResult(strings.Repeat("x", 26000)+fmt.Sprint(step), "", id)
		w.Messages()
	}
	msgs, _ := w.Messages()
	var withCalls, emptyReasoning int
	for _, m := range msgs {
		if len(m.ToolCalls) == 0 {
			continue
		}
		withCalls++
		if strings.TrimSpace(m.Reasoning) == "" {
			emptyReasoning++
		}
	}
	if withCalls == 0 {
		t.Fatal("没有带工具调用的消息, 这个用例是空的")
	}
	if emptyReasoning > 0 {
		t.Fatalf("%d 条带 tool_calls 的消息 reasoning 是空的 —— 供应商会直接拒掉", emptyReasoning)
	}
}

// 动作本身(tool + args)永远留着 —— 那是"我做过什么"的证据.
// 收掉的只是附在上面的推敲过程.
func TestTrimmingThoughtKeepsTheAction(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "读日志", 80000)
	for step := 0; step < 8; step++ {
		id := fmt.Sprintf("c%d", step)
		w.AppendBatch([]PageCall{{ID: id, Tool: "read_file",
			Args: map[string]any{"path": fmt.Sprintf("f%d.log", step)}}},
			strings.Repeat("想", 6000))
		w.AppendResult(strings.Repeat("x", 26000), "", id)
		w.Messages()
	}
	msgs, _ := w.Messages()
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			seen[c.Arguments] = true
		}
	}
	for step := 0; step < 7; step++ {
		want := fmt.Sprintf("f%d.log", step)
		found := false
		for a := range seen {
			if strings.Contains(a, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("动作没了: %s —— 模型会不知道自己做过这件事", want)
		}
	}
}
