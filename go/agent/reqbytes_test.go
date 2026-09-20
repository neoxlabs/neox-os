package agent

import (
	"github.com/neox-os/neox-os/engine"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 计量必须覆盖**整个请求**, 尤其是 ToolCalls.Arguments.
//
// 那是最大的一块: write_file 的整段文件内容就在 Arguments 里, 而那条
// 消息的 Content 是空的. 漏掉它会让"字节/token"被系统性低估, 预算跟着
// 缩水, 于是更早折叠, 而折叠改写历史 → 前缀缓存被打穿.
//
// 真机: 71 步的会话预算从 175131 缩到 65997(比率压到接近下限 0.8),
// 折叠 50 次, 缓存命中在 96% 和 9% 之间反复跳.
func TestRequestBytesCountsToolCallArguments(t *testing.T) {
	body := strings.Repeat("文件内容", 2000)
	msgs := []abi.InferMessage{
		{Role: "assistant", Content: "", ToolCalls: []abi.ToolCall{
			{ID: "c1", Name: "write_file", Arguments: `{"path":"a.py","content":"` + body + `"}`},
		}},
	}
	got := requestBytes("", nil, msgs)
	if got < len(body) {
		t.Fatalf("最大的一块没数进去: 参数里有 %d 字节, 只数到 %d", len(body), got)
	}
}

// 工具声明每次请求都带, 也要算 —— 它是请求里一块固定的、不小的开销
func TestRequestBytesCountsToolDefs(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	withTools := requestBytes("sys", ToolDefs(ts), nil)
	without := requestBytes("sys", nil, nil)
	if withTools <= without {
		t.Fatalf("工具声明没算进去: %d vs %d", withTools, without)
	}
}

// reasoning 和 tool_call_id 也进 wire format
func TestRequestBytesCountsReasoningAndToolCallID(t *testing.T) {
	base := requestBytes("", nil, []abi.InferMessage{{Role: "tool", Content: "x"}})
	more := requestBytes("", nil, []abi.InferMessage{
		{Role: "tool", Content: "x", ToolCallID: "call_abcdef", Reasoning: "想了很久"},
	})
	if more <= base {
		t.Fatalf("reasoning/tool_call_id 没算: %d vs %d", more, base)
	}
}

// 跟指纹算的是同一张表 —— 指纹列全了却在计量这儿漏, 说明表抄了两遍抄漏一次.
// 这条钉住"两处不许再分叉": 指纹认得的字段, 计量必须都数.
func TestRequestBytesCoversEverythingTheFingerprintDoes(t *testing.T) {
	full := abi.InferMessage{Role: "assistant", Content: "正文", ToolCallID: "tc",
		Reasoning: "推理", ToolCalls: []abi.ToolCall{{ID: "i", Name: "n", Arguments: "args"}}}
	for _, tc := range []struct {
		name string
		mod  func(m *abi.InferMessage)
	}{
		{"content", func(m *abi.InferMessage) { m.Content += "更多" }},
		{"toolCallID", func(m *abi.InferMessage) { m.ToolCallID += "更多" }},
		{"reasoning", func(m *abi.InferMessage) { m.Reasoning += "更多" }},
		{"arguments", func(m *abi.InferMessage) { m.ToolCalls[0].Arguments += "更多" }},
	} {
		before := requestBytes("", nil, []abi.InferMessage{full})
		m := full
		m.ToolCalls = []abi.ToolCall{{ID: "i", Name: "n", Arguments: "args"}}
		tc.mod(&m)
		if after := requestBytes("", nil, []abi.InferMessage{m}); after <= before {
			t.Fatalf("%s 变长了, 计量却没变: %d → %d", tc.name, before, after)
		}
	}
}

// 算对了还得接上 —— 钉的是"写了但没接线".
//
// 上一轮就栽过一次: commandPath 单独测是绿的, 而调用点还写着旧的固定值,
// 测试全绿而现实没变.
func TestBudgetObservesWholeRequestNotJustContent(t *testing.T) {
	sys := newFakeSys()
	sys.inferFn = func(abi.InferParams) (abi.InferResult, error) {
		return abi.InferResult{Content: "好", PromptTokens: 20000}, nil
	}
	body := strings.Repeat("x", 40000)
	w := NewWindow(engine.NewPageStore(), "任务", 1<<20)
	w.AppendBatch([]PageCall{{ID: "c1", Tool: "write_file",
		Args: map[string]any{"path": "a.py", "content": body}}}, "")
	w.AppendResult("c1", "写好了", "")

	bud := NewBudgeter(128000)
	m := &LLM{ABI: sys, Tools: NewToolSet(DefaultTools()), Window: w, Budgeter: bud}
	_, _ = m.once("任务", nil)

	// 20000 token 的请求里, 光系统段(约 16KB)只有 0.8 —— 正好是下限;
	// 把 40KB 的 ToolCalls.Arguments 数进去才会到 2.9 左右.
	// **门槛必须选在两种算法分得开的地方**, 否则测试是假绿的:
	// 第一版我写的是 1000 token, 单条系统段就撞上限 6.0, 漏不漏数都过.
	if got := bud.Stats()["bytesPerToken"].(float64); got < 2.0 {
		t.Fatalf("预算观测没数上 ToolCalls.Arguments, 比率被低估成 %v", got)
	}
}
