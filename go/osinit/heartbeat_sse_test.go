package osinit

import (
	"os"
	"strings"
	"testing"
)

// 心跳必须存在, 而且必须是**注释**不是事件.
//
//	一条安静的 SSE 会被路上每一跳掐掉(Cloudflare 100 秒、家用 NAT
//	通常 60-300 秒). 断了不报错, 只是从此收不到任何事件 —— 而那跟
//	"今天很安静"长得一模一样.
//
//	这条测试钉的是三件事, 每件对应一种把它做坏的方式:
//	  · 心跳还在              (有人重构时顺手删掉)
//	  · 它是注释不是事件      (发成 data: 的话订阅方会当成一条真事件)
//	  · 写失败要报告          (不报的话对面断了循环还在转)
func TestStreamHasHeartbeat(t *testing.T) {
	src := readSelf(t, "observe.go")

	if !strings.Contains(src, "beat := time.NewTicker(") {
		t.Fatal("SSE 流里没有心跳 —— 安静的连接会被中间每一跳掐掉, " +
			"而且断了是静默的")
	}
	if !strings.Contains(src, `writeComment(w, flusher, "beat")`) {
		t.Error("心跳必须走 writeComment(SSE 注释) —— " +
			"发成 data: 的话订阅方会把它当成一条真事件")
	}
	// 写失败要能被发现: 心跳是这条流上唯一定期发生的写,
	// 对面断开时它是第一个失败的
	if !strings.Contains(src, `if !writeComment(w, flusher, "beat") {`) {
		t.Error("心跳的返回值没人看 —— 对面断了之后循环会一直转, " +
			"对着一个早就没了的连接")
	}
}

func readSelf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("./" + name)
	if err != nil {
		t.Fatalf("读不到 %s: %v", name, err)
	}
	return string(b)
}
