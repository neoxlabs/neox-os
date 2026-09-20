package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// DeepSeek thinking 协议**双向** 400 规则, 验的是真正发出去的那个 body:
//
//	带 tool_calls  必须回传 reasoning_content
//	纯文本         必须不回传
//
// 只做一半是真出过的错. 在 Window 那一层验不够 —— 翻译成 wire format
// 的是这一层, 漏在这儿一样会 400.
func TestReasoningPassbackIsTwoSided(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readAll(r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"model":"m"}`))
	}))
	defer srv.Close()

	p := NewOpenAICompatible(srv.URL, "k", "deepseek-v4-flash")
	_, err := p.Infer(t.Context(), abi.InferParams{
		Messages: []abi.InferMessage{
			{Role: "user", Content: "干活"},
			{Role: "assistant_reply", Reasoning: "带调用的推理",
				ToolCalls: []abi.ToolCall{{ID: "c1", Name: "run", Arguments: "{}"}}},
			{Role: "tool_result", ToolCallID: "c1", Content: "结果"},
			{Role: "assistant_reply", Content: "说完了", Reasoning: "纯文本的推理"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var req struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []any  `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	for i, m := range req.Messages {
		if len(m.ToolCalls) > 0 && m.ReasoningContent == "" {
			t.Errorf("第 %d 条带 tool_calls 却没发 reasoning_content —— 会 400", i)
		}
		if len(m.ToolCalls) == 0 && m.ReasoningContent != "" {
			t.Errorf("第 %d 条纯文本却发了 reasoning_content —— 这个方向同样会 400", i)
		}
	}
	// 角色翻译: OS 层不用行业词汇, 到这一层才翻
	if !strings.Contains(string(body), `"role":"tool"`) {
		t.Error("tool_result 没翻译成 tool 角色")
	}
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
