package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// sse 造一段真实形状的流.
//
//	**工具调用在流式里是碎的**: 第一帧带 index+id+name, 之后几十帧只带
//	index 和 arguments 的一小截 —— 参数是一个字符一个字符拼出来的.
func sse(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			_, _ = w.Write([]byte("data: " + f + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(s.Close)
	return s
}

// **这一处漏了会静默地废掉所有带工具的 bot.**
//
//	模型发起调用时 finish_reason 是 tool_calls 而 content 是空的.
//	不攒的话上层拿到一个"空回复", 重试三次, 整轮失败 —— 而错误信息
//	说的是"模型返回了空回复", 跟真正的原因一个字都不沾.
//
//	因此"你好"这种纯说话的能通, 而"我在哪条路旁边"
//	(要调 what_now)会整轮失败.
func TestInferStreamAssemblesToolCalls(t *testing.T) {
	srv := sse(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"what_now","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	p := &OpenAICompatible{BaseURL: srv.URL, APIKey: "k", ModelID: "m", Client: srv.Client()}
	res, err := p.InferStream(context.Background(), abi.InferParams{}, func(StreamDelta) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("工具调用没攒起来: %+v —— 上层会把它当成空回复", res.ToolCalls)
	}
	got := res.ToolCalls[0]
	if got.Name != "what_now" || got.ID != "call_1" || got.Arguments != "{}" {
		t.Fatalf("攒错了: %+v", got)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason 丢了: %q", res.FinishReason)
	}
}

// 参数是一截一截拼的 —— 拼错了工具会收到一段不合法的 JSON
func TestInferStreamJoinsArgumentFragments(t *testing.T) {
	srv := sse(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"remind_me","arguments":"{\"te"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"xt\":\"喝"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"水\"}"}}]}}]}`,
	)
	p := &OpenAICompatible{BaseURL: srv.URL, APIKey: "k", ModelID: "m", Client: srv.Client()}
	res, _ := p.InferStream(context.Background(), abi.InferParams{}, func(StreamDelta) {})
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Arguments != `{"text":"喝水"}` {
		t.Fatalf("参数没拼对: %+v", res.ToolCalls)
	}
}

// 一批多个调用 —— **顺序按模型发的来**, map 遍历顺序会把它打乱
func TestInferStreamKeepsBatchOrder(t *testing.T) {
	srv := sse(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"second","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"first","arguments":"{}"}}]}}]}`,
	)
	p := &OpenAICompatible{BaseURL: srv.URL, APIKey: "k", ModelID: "m", Client: srv.Client()}
	res, _ := p.InferStream(context.Background(), abi.InferParams{}, func(StreamDelta) {})
	if len(res.ToolCalls) != 2 || res.ToolCalls[0].Name != "first" {
		t.Fatalf("顺序乱了: %+v", res.ToolCalls)
	}
}

// 说话那条路照旧 —— 增量要一段段给出去
func TestInferStreamStillStreamsText(t *testing.T) {
	srv := sse(t,
		`{"choices":[{"delta":{"content":"你"}}]}`,
		`{"choices":[{"delta":{"content":"好"}}]}`,
	)
	p := &OpenAICompatible{BaseURL: srv.URL, APIKey: "k", ModelID: "m", Client: srv.Client()}
	var got []string
	res, _ := p.InferStream(context.Background(), abi.InferParams{}, func(d StreamDelta) {
		got = append(got, d.Text)
	})
	if len(got) != 2 || strings.Join(got, "") != "你好" {
		t.Fatalf("增量没一段段给: %v", got)
	}
	if res.Content != "你好" {
		t.Fatalf("完整内容不对: %q", res.Content)
	}
}
