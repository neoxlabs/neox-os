package main

import (
	"os"
	"strings"
	"testing"
)

// 两条上线路径的开场白, 都必须听 returning 这道闸.
//
// 这条是**读源码文本**的闸, 不是行为测试: 招呼发生在 spawn 之后的
// 进程里, 需要完整的 OS 和模型才能运行. 只要有一条路径漏掉这个判断,
// 文本比对就能在不启动完整运行环境的情况下挡住它.
//
// 只有带工具那条(serveWorker)有闸而 converse 那条没有时, 每开一次机
// 同一句话就多贴一遍, 账本里会攒出 8 份.
func TestBothIntroPathsCheckReturning(t *testing.T) {
	for _, c := range []struct{ file, fn string }{
		{"main.go", "func converse("},
		{"worker.go", "func serveWorker("},
	} {
		body := funcBody(t, c.file, c.fn)
		if !strings.Contains(body, "p.intro") {
			t.Errorf("%s 里找不到开场白 —— 这个闸盯错地方了, 修它", c.fn)
			continue
		}
		if !strings.Contains(body, "!p.returning") {
			t.Errorf("%s 发开场白时没查 returning —— 每次开机都会复读一遍", c.fn)
		}
	}
}

// funcBody 取一个函数从签名到下一个顶层 func 之间的正文
func funcBody(t *testing.T, file, sig string) string {
	t.Helper()
	raw := readSource(t, file)
	start := strings.Index(raw, sig)
	if start < 0 {
		t.Fatalf("%s 里没有 %s", file, sig)
	}
	rest := raw[start+len(sig):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func readSource(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读不到 %s: %v", file, err)
	}
	return string(raw)
}
