package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"
)

// 假的供应商 —— 摘要器走的就是这台机器当前的模型
type fakeProvider struct {
	mu   sync.Mutex
	saw  string
	hits int
}

func (f *fakeProvider) Model() string { return "fake" }
func (f *fakeProvider) Infer(ctx context.Context, p abi.InferParams) (abi.InferResult, error) {
	f.mu.Lock()
	f.hits++
	if len(p.Messages) > 0 {
		f.saw = p.Messages[0].Content
	}
	f.mu.Unlock()
	return abi.InferResult{Content: "他老婆每天 7:10 到高新实验学校\n他自己 8:30 到凤凰国际广场"}, nil
}

// 后台写摘要, 下一轮结束时换上 —— 中间不挡任何一句话
func TestCompactDraftsInBackgroundAndAppliesNextTurn(t *testing.T) {
	fp := &fakeProvider{}
	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	o.SetProvider(fp)
	c := &components{os: o}
	store := engine.NewPageStore()
	win := agent.NewWindow(store, "开头", 0)
	defer win.Release()
	// 堆到过线为止(compactAt = 120KB)
	for i := 0; i < 14; i++ {
		win.AppendUserTurn(strings.Repeat("很久以前聊过的事", 800))
		win.AppendAssistantSaid(strings.Repeat("当时的回答", 800))
	}
	win.AppendUserTurn("那我现在回家要多久")
	before := win.Bytes()
	if before < compactAt {
		t.Fatalf("样本没到阈值: %d", before)
	}
	var got []map[string]any
	emit := func(m map[string]any) { got = append(got, m) }
	cp := &compactor{}

	c.afterTurn(cp, win, emit) // ① 这一轮只起草, 不动窗口
	if win.Bytes() != before {
		t.Fatal("起草那一轮就把窗口改了 —— 它跟下一轮是并行的")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := cp.ready.Load().(string); s != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s, _ := cp.ready.Load().(string); s == "" {
		t.Fatal("后台没写出摘要")
	}
	if fp.hits != 1 {
		t.Fatalf("摘要器被调了 %d 次", fp.hits)
	}
	if strings.Contains(fp.saw, "那我现在回家要多久") {
		t.Fatal("最近这句该留在原文里, 不该拿去摘要")
	}

	c.afterTurn(cp, win, emit) // ② 下一轮结束时换上
	if win.Bytes() >= before {
		t.Fatalf("没压下去: %d → %d", before, win.Bytes())
	}
	msgs, _ := win.Messages()
	var all strings.Builder
	for _, m := range msgs {
		all.WriteString(m.Content + "\n")
	}
	if !strings.Contains(all.String(), "7:10 到高新实验学校") {
		t.Fatal("摘要没进上下文")
	}
	if !strings.Contains(all.String(), "那我现在回家要多久") {
		t.Fatal("最近那句被压掉了")
	}
	var compacted bool
	for _, m := range got {
		if m["phase"] == "compacted" {
			compacted = true
		}
	}
	if !compacted {
		t.Fatalf("没落账: %v", got)
	}
}

// 没到阈值就什么都不做 —— 压一次整段前缀作废一次
func TestCompactStaysQuietBelowThreshold(t *testing.T) {
	fp := &fakeProvider{}
	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	o.SetProvider(fp)
	c := &components{os: o}
	win := agent.NewWindow(engine.NewPageStore(), "开头", 0)
	defer win.Release()
	win.AppendUserTurn("今天天气怎么样")
	c.afterTurn(&compactor{}, win, func(map[string]any) {})
	time.Sleep(50 * time.Millisecond)
	if fp.hits != 0 {
		t.Fatalf("没到阈值就去写摘要了: %d 次", fp.hits)
	}
}
