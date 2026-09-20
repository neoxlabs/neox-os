package engine

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 重试与退避 —— 让长跑扛得住一次限流.
//
// ── 为什么必须有 ──
//
// 原来一次 429 或一次网络抖动就直接把错误抛给上层, 上层数到第 3 次
// 就把整轮判死. 对"跑一整夜"这种场景那是致命的: 供应商偶尔限流是常态,
// 而代价是**整轮的活白干**.
//
// ── 重试错东西比不重试更糟 ──
//
// 这是这套逻辑真正的难点, 不是退避算法.
//
//	能重试   限流(429) / 服务端 5xx / 连接被掐 / 超时
//	         —— 同样的请求过一会儿会成功
//	不能重试 400 参数不对 / 401 凭据不对 / 403 没权限 / 404 模型不存在
//	         —— 重试一百次也是同样的错, 只是把一次快速失败拖成一分钟
//
// 判错方向的代价不对称: 该重试的没重试, 用户损失一整轮;
// 不该重试的重了, 用户等更久才看到同一个错, 而且账单可能翻倍
// (有些供应商对 400 也计费).
//
// ── 认服务端给的 Retry-After ──
//
// 供应商比我们更清楚什么时候该回来. 它给了就听它的, 别用自己算的 ——
// 算早了继续撞墙, 算晚了白等.

const (
	retryMaxAttempts = 4
	retryInitial     = 400 * time.Millisecond
	retryMax         = 20 * time.Second
	// retryJitter 抖动幅度. **必须有**: 一批请求同时被限流之后
	// 会在同一时刻一起回来, 那等于自己 DDoS 自己.
	retryJitter = 0.25
)

// retriable 这个错值不值得再试一次.
type retriable struct {
	yes bool
	// after 服务端明说的等待时长. 0 = 它没说, 用我们自己的退避
	after time.Duration
	why   string
}

// classifyHTTP 按状态码判. 只有 provider 看得见状态码, 所以判定放在这一层.
func classifyHTTP(status int, hdr http.Header) retriable {
	switch {
	case status == http.StatusTooManyRequests:
		return retriable{true, parseRetryAfter(hdr), "限流"}
	case status == http.StatusRequestTimeout, status == 408:
		return retriable{true, 0, "请求超时"}
	case status >= 500:
		// 5xx 是服务端自己的问题, 过一会儿多半就好了
		return retriable{true, parseRetryAfter(hdr), "服务端错误"}
	}
	// 4xx (除了上面两个) 一律不重试: 参数/凭据/权限/模型名不对,
	// 重试一百次也是同一个错, 只是把快速失败拖成一分钟.
	return retriable{false, 0, ""}
}

// classifyTransport 网络层的错值不值得再试.
//
// 只认**明确是传输问题**的那几类. 认得太宽会把"上下文取消"也当成
// 可重试 —— 那是用户按了停止, 再试一次是无视他.
func classifyTransport(err error) retriable {
	if err == nil {
		return retriable{}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return retriable{} // 是我们自己取消的, 不是网络的错
	}
	s := err.Error()
	for _, sig := range []string{
		"connection reset", "connection refused", "broken pipe",
		"EOF", "timeout", "no such host", "TLS handshake",
	} {
		if strings.Contains(s, sig) {
			return retriable{true, 0, "网络抖动"}
		}
	}
	return retriable{}
}

// parseRetryAfter 读服务端给的等待时长. 只认秒数形式 ——
// HTTP-date 形式极少见, 而解析错了会等到天荒地老.
func parseRetryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > retryMax {
		d = retryMax // 服务端说等一小时也不能真等一小时
	}
	return d
}

// backoffFor 第 attempt 次重试该等多久 (attempt 从 1 起).
//
// 指数退避 + 抖动. 抖动**必须有**: 一批请求同时被限流之后会在同一时刻
// 一起回来, 那等于自己 DDoS 自己.
func backoffFor(attempt int, serverSaid time.Duration) time.Duration {
	if serverSaid > 0 {
		return serverSaid // 供应商比我们更清楚什么时候该回来
	}
	d := retryInitial
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= retryMax {
			d = retryMax
			break
		}
	}
	// ±25% 抖动
	j := 1 + (rand.Float64()*2-1)*retryJitter
	out := time.Duration(float64(d) * j)
	if out > retryMax {
		out = retryMax
	}
	if out < 0 {
		out = retryInitial
	}
	return out
}

// sleepCtx 可被打断的等待. 用户按停止时不该还在那儿睡着.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
