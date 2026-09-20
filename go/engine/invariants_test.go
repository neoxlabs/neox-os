package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// ── 对照 6X 客户端已知的适配层不变量 ──
//
// 这些规则是客户端用真机 400 换来的, 早就写在
// neox-kernel/src/models/*.ts 的注释里. 我在这边重写适配层时没去读,
// 于是把其中两条又撞了一遍 (tool_call id 不许自己编、一条消息不许拆成
// N 条). 这个文件是那份清单在 OS 侧的对照, 免得靠记性.
func sendAndCapture(t *testing.T, p abi.InferParams) []map[string]any {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readAll(r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"model":"m"}`))
	}))
	defer srv.Close()
	if _, err := NewOpenAICompatible(srv.URL, "k", "m").Infer(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

// R3.5 带 tool_calls 的 assistant, content 必须是 "" 而不是 null.
// 客户端三家适配器一致这么做 (openai/glm/kimi).
func TestAssistantWithToolCallsHasEmptyStringContent(t *testing.T) {
	msgs := sendAndCapture(t, abi.InferParams{Messages: []abi.InferMessage{
		{Role: "user", Content: "干活"},
		{Role: "assistant_reply", ToolCalls: []abi.ToolCall{{ID: "c1", Name: "run", Arguments: "{}"}}},
		{Role: "tool_result", ToolCallID: "c1", Content: "结果"},
	}})
	for _, m := range msgs {
		if _, ok := m["tool_calls"]; !ok {
			continue
		}
		c, present := m["content"]
		if !present || c == nil {
			t.Fatalf("带 tool_calls 的消息 content 是 null/缺失, 应该是空串: %+v", m)
		}
	}
}

// R6.2 空参数必须是 "{}" 而不是空串或 null —— 供应商按 JSON 对象解析
func TestEmptyToolArgumentsAreEmptyObject(t *testing.T) {
	msgs := sendAndCapture(t, abi.InferParams{Messages: []abi.InferMessage{
		{Role: "user", Content: "干活"},
		{Role: "assistant_reply", ToolCalls: []abi.ToolCall{
			{ID: "c1", Name: "list_dir", Arguments: "{}"}}},
		{Role: "tool_result", ToolCallID: "c1", Content: "结果"},
	}})
	for _, m := range msgs {
		tcs, ok := m["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tc := range tcs {
			fn := tc.(map[string]any)["function"].(map[string]any)
			args, _ := fn["arguments"].(string)
			var v map[string]any
			if err := json.Unmarshal([]byte(args), &v); err != nil {
				t.Fatalf("参数不是合法 JSON 对象: %q", args)
			}
		}
	}
}

// 两套结构化机制不许叠: 有 tools 时不再设 response_format.
// 各家对同时出现的行为不一 —— 有的直接拒, 有的让 tool_calls 静默失效.
func TestToolsAndJSONModeNeverCoexist(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readAll(r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"model":"m"}`))
	}))
	defer srv.Close()
	_, err := NewOpenAICompatible(srv.URL, "k", "m").Infer(t.Context(), abi.InferParams{
		Messages: []abi.InferMessage{{Role: "user", Content: "x"}},
		JSONOut:  true,
		Tools:    []abi.ToolDef{{Name: "run", Desc: "跑命令"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if _, ok := req["response_format"]; ok {
		t.Fatal("有 tools 时还设了 response_format —— 两套结构化机制叠在一起, 各家行为不一")
	}
	if _, ok := req["tools"]; !ok {
		t.Fatal("tools 没发出去")
	}
}
