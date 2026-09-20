package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 地址推导: 三种写法都得落到同一个口上. 差一个 /anthropic
// 的表现是 404, 而 404 看起来像"这个模型不存在"
func TestAnthropicURL(t *testing.T) {
	for _, c := range []struct{ base, want string }{
		{"https://api.deepseek.com", "https://api.deepseek.com/anthropic/v1/messages"},
		{"https://api.deepseek.com/", "https://api.deepseek.com/anthropic/v1/messages"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/anthropic/v1/messages"},
		{"https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic/v1/messages"},
		{"https://gw.example.com/x", "https://gw.example.com/x/v1/messages"},
	} {
		if got := (&Anthropic{BaseURL: c.base}).messagesURL(); got != c.want {
			t.Errorf("%s → %s, 想要 %s", c.base, got, c.want)
		}
	}
}

// 认协议: DeepSeek 缺省要落到 Messages 那条 —— 官方搜索只在那儿.
// 而用户填死的一律听他的
func TestWantsAnthropic(t *testing.T) {
	for _, c := range []struct {
		proto, base string
		want        bool
	}{
		{"", "https://api.deepseek.com", true},
		{"", "https://API.DeepSeek.com/v1", true},
		{"openai", "https://api.deepseek.com", false}, // 填死了就听他的
		{"anthropic", "https://gw.example.com", true},
		{"", "https://gw.example.com/n1", false}, // 认不出来的别猜
		{"", "https://gw.example.com/anthropic", true},
	} {
		if got := WantsAnthropic(c.proto, c.base); got != c.want {
			t.Errorf("(%q,%q) = %v", c.proto, c.base, got)
		}
	}
}

// thinking 块**只在带 tool_calls 那轮回传**.
//
//	这个口两个方向都管: 开着 thinking 而带 tool_use 的轮次不回传 →
//	400("must be passed back"). 因此带 tool_use 的轮次必须保留 thinking 块.
func TestAnthropicThinkingPassback(t *testing.T) {
	a := &Anthropic{ModelID: "m", Think: true}
	req := a.buildRequest(abi.InferParams{Messages: []abi.InferMessage{
		{Role: "user", Content: "喂"},
		{Role: "assistant_reply", Content: "好", Reasoning: "想了想"},
		{Role: "user", Content: "再来"},
		{Role: "assistant_reply", Reasoning: "该查一下", ToolCalls: []abi.ToolCall{
			{ID: "c1", Name: "look", Arguments: `{"q":"x"}`}}},
		{Role: "tool_result", ToolCallID: "c1", Content: "没有"},
	}})
	if has := hasType(req.Messages[1], "thinking"); has {
		t.Error("纯文本那轮不该带 thinking 块")
	}
	if !hasType(req.Messages[3], "thinking") {
		t.Fatal("带 tool_use 那轮必须回传 thinking —— 不带是 400")
	}
	if req.Messages[3].Content[0].Type != "thinking" {
		t.Error("thinking 得排在最前面")
	}
}

// 一轮两个调用, 结果要并进**同一条** user 消息.
// 拆成两条的话中间那条对不上 tool_use, 整个请求被拒
func TestAnthropicToolResultsMerge(t *testing.T) {
	a := &Anthropic{ModelID: "m"}
	req := a.buildRequest(abi.InferParams{Messages: []abi.InferMessage{
		{Role: "user", Content: "查两个"},
		{Role: "assistant_reply", ToolCalls: []abi.ToolCall{
			{ID: "c1", Name: "look"}, {ID: "c2", Name: "look"}}},
		{Role: "tool_result", ToolCallID: "c1", Content: "A"},
		{Role: "tool_result", ToolCallID: "c2", Content: "B"},
		{Role: "user", Content: "然后呢"},
	}})
	if len(req.Messages) != 4 {
		t.Fatalf("该是 4 条, 得到 %d", len(req.Messages))
	}
	if len(req.Messages[2].Content) != 2 {
		t.Fatalf("两条结果该并在一条里, 得到 %d", len(req.Messages[2].Content))
	}
	// 后面那条真正的 user 话不能被并进去
	if req.Messages[3].Content[0].Type != "text" {
		t.Error("用户的下一句被并进结果里了")
	}
	// 没参数的调用, input 得是个合法对象
	if string(req.Messages[1].Content[0].Input) != "{}" {
		t.Errorf("空参数该是 {}, 得到 %s", req.Messages[1].Content[0].Input)
	}
}

// max_tokens 在这个协议里是必填的 —— 上层不关心时也得给一个
func TestAnthropicMaxTokensRequired(t *testing.T) {
	req := (&Anthropic{ModelID: "m"}).buildRequest(abi.InferParams{})
	if req.MaxTokens <= 0 {
		t.Fatal("max_tokens 不填是 400")
	}
	if req.Thinking == nil || req.Thinking.Type != "disabled" {
		t.Error("缺省得显式关掉想 —— 不说的话由模型自己定, 而它默认是想的")
	}
}

// 用量: 两家语义不一样, 不能照一家写死(见 promptOf)
func TestAnthropicPromptTokens(t *testing.T) {
	// DeepSeek: input 含缓存那部分
	if got := promptOf(anUsage{Input: 137, CacheRead: 128}); got != 137 {
		t.Errorf("含缓存的那家算成了 %d", got)
	}
	// 官方 Anthropic: input 不含
	if got := promptOf(anUsage{Input: 9, CacheRead: 128, CacheWrite: 3}); got != 140 {
		t.Errorf("不含缓存的那家算成了 %d", got)
	}
}

func TestAnthropicFinish(t *testing.T) {
	// 上层按 "length" 判截断, 原样透传 max_tokens 那个判断就静默失效
	if finishOf("max_tokens") != "length" {
		t.Error("截断没翻过来")
	}
	if finishOf("tool_use") != "tool_calls" {
		t.Error("工具调用没翻过来")
	}
}

// 一整轮: 工具调用从回包里拿出来, 用量记上
func TestAnthropicInfer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-version") == "" {
			t.Error("少了 anthropic-version")
		}
		var got anRequest
		_ = json.NewDecoder(r.Body).Decode(&got)
		if len(got.Tools) != 1 || got.Tools[0].Name != "look" {
			t.Errorf("工具没带过来: %+v", got.Tools)
		}
		_, _ = w.Write([]byte(`{"model":"m","stop_reason":"tool_use","content":[
			{"type":"thinking","thinking":"想"},{"type":"text","text":"稍等"},
			{"type":"tool_use","id":"c1","name":"look","input":{"q":"x"}}],
			"usage":{"input_tokens":100,"output_tokens":7,"cache_read_input_tokens":64}}`))
	}))
	defer srv.Close()
	a := NewAnthropic(srv.URL, "k", "m")
	res, err := a.Infer(context.Background(), abi.InferParams{
		Messages: []abi.InferMessage{{Role: "user", Content: "喂"}},
		Tools:    []abi.ToolDef{{Name: "look", Params: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "look" {
		t.Fatalf("工具调用丢了: %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Arguments != `{"q":"x"}` {
		t.Errorf("参数变了: %s", res.ToolCalls[0].Arguments)
	}
	if res.Content != "稍等" || res.Reasoning != "想" {
		t.Errorf("正文和推理没分开: %q / %q", res.Content, res.Reasoning)
	}
	if res.FinishReason != "tool_calls" || res.CachedTokens != 64 || res.PromptTokens != 100 {
		t.Errorf("记账不对: %+v", res)
	}
}

// 流式: 参数是一小截一小截拼的. 漏了这段的表现**不是报错**,
// 是上层拿到"空回复"然后重试三次
func TestAnthropicStream(t *testing.T) {
	frames := []string{
		`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":50,"cache_read_input_tokens":40}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c1","name":"look"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, f := range frames {
			_, _ = w.Write([]byte("data: " + f + "\n\n"))
		}
	}))
	defer srv.Close()
	var seen strings.Builder
	res, err := NewAnthropic(srv.URL, "k", "m").InferStream(context.Background(),
		abi.InferParams{Messages: []abi.InferMessage{{Role: "user", Content: "喂"}}},
		func(d StreamDelta) { seen.WriteString(d.Text) })
	if err != nil {
		t.Fatal(err)
	}
	if seen.String() != "好" || res.Content != "好" {
		t.Errorf("流里的字对不上: %q / %q", seen.String(), res.Content)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Arguments != `{"q":"x"}` {
		t.Fatalf("参数没拼起来: %+v", res.ToolCalls)
	}
	if res.CompletionTokens != 12 || res.PromptTokens != 50 || res.CachedTokens != 40 {
		t.Errorf("记账不对: %+v", res)
	}
}

func hasType(m anMessage, kind string) bool {
	for _, b := range m.Content {
		if b.Type == kind {
			return true
		}
	}
	return false
}
