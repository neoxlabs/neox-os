package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func okBody() string {
	return `{"choices":[{"message":{"content":"好"},"finish_reason":"stop"}],"model":"m","usage":{"prompt_tokens":10}}`
}

// 限流之后要自己扛过去 —— 一次 429 不该把整轮的活废掉
func TestRateLimitIsRetried(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, okBody())
	}))
	defer srv.Close()

	res, err := NewOpenAICompatible(srv.URL, "k", "m").
		Infer(t.Context(), abi.InferParams{Messages: []abi.InferMessage{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("两次 429 之后该成功: %v", err)
	}
	if res.Content != "好" {
		t.Fatalf("拿到的不是最后那次的结果: %q", res.Content)
	}
	if n != 3 {
		t.Fatalf("重试次数不对: %d", n)
	}
}

// 5xx 同理 —— 服务端自己的问题, 过一会儿多半就好了
func TestServerErrorIsRetried(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, okBody())
	}))
	defer srv.Close()
	if _, err := NewOpenAICompatible(srv.URL, "k", "m").
		Infer(t.Context(), abi.InferParams{}); err != nil {
		t.Fatalf("502 之后该成功: %v", err)
	}
}

// **不该重试的不许重试.**
//
// 这是这套逻辑真正的难点, 不是退避算法. 400/401/403/404 重试一百次
// 也是同一个错, 只是把一次快速失败拖成一分钟 —— 而且有些供应商
// 对它们也计费.
func TestClientErrorsFailFast(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422} {
		var n int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&n, 1)
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":{"message":"参数不对"}}`)
		}))
		if _, err := NewOpenAICompatible(srv.URL, "k", "m").
			Infer(t.Context(), abi.InferParams{}); err == nil {
			t.Errorf("HTTP %d 该直接失败", code)
		}
		if n != 1 {
			t.Errorf("HTTP %d 被重试了 %d 次 —— 白等而已", code, n)
		}
		srv.Close()
	}
}

// 认服务端给的 Retry-After —— 它比我们更清楚什么时候该回来
func TestRetryAfterIsHonored(t *testing.T) {
	if got := parseRetryAfter(http.Header{"Retry-After": []string{"2"}}); got != 2*time.Second {
		t.Fatalf("没读 Retry-After: %v", got)
	}
	// 服务端说等一小时也不能真等一小时
	if got := parseRetryAfter(http.Header{"Retry-After": []string{"3600"}}); got > retryMax {
		t.Fatalf("等太久了: %v", got)
	}
	// HTTP-date 形式不认 —— 解析错了会等到天荒地老, 宁可用自己的退避
	if got := parseRetryAfter(http.Header{"Retry-After": []string{"Wed, 21 Oct 2026 07:28:00 GMT"}}); got != 0 {
		t.Fatalf("不该认 date 形式: %v", got)
	}
	// 服务端说了就听它的, 不用自己算的
	if got := backoffFor(1, 3*time.Second); got != 3*time.Second {
		t.Fatalf("没听服务端的: %v", got)
	}
}

// 用户按停止时不许还在那儿睡着重试
func TestCancelStopsRetrying(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := NewOpenAICompatible(srv.URL, "k", "m").Infer(ctx, abi.InferParams{})
	if err == nil {
		t.Fatal("取消了却成功了")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("取消之后还在睡: %v", time.Since(start))
	}
}

// 上下文取消不算"网络抖动" —— 那是用户按了停止, 再试一次是无视他
func TestCancelIsNotTransient(t *testing.T) {
	if classifyTransport(context.Canceled).yes {
		t.Fatal("把用户取消当成了网络抖动")
	}
	if classifyTransport(context.DeadlineExceeded).yes {
		t.Fatal("把我们自己设的超时当成了网络抖动")
	}
	if !classifyTransport(errors.New("read: connection reset by peer")).yes {
		t.Fatal("真的网络抖动没认出来")
	}
}

// 退避必须带抖动: 一批请求同时被限流之后会在同一时刻一起回来,
// 那等于自己 DDoS 自己
func TestBackoffHasJitter(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 30; i++ {
		seen[backoffFor(3, 0)] = true
	}
	if len(seen) < 5 {
		t.Fatalf("退避没有抖动, 只有 %d 种取值", len(seen))
	}
}
