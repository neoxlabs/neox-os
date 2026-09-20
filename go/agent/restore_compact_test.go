package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// 压紧过的那一截不该再装回来 —— 不认这条的话, 线上每次发版重启
// 都把原文全量装回, 压了等于没压
func TestRestoreStartsFromLastCompaction(t *testing.T) {
	evs := []abi.Event{
		{Kind: abi.EvInputRecv, Payload: map[string]any{"text": "很久以前聊的那件事"}},
		{Kind: abi.EvProcOutput, Payload: map[string]any{"phase": "reply", "text": "当时的回答"}},
		{Kind: abi.EvProcOutput, Payload: map[string]any{"phase": "compacted",
			"summary": "他老婆每天 7:10 到高新实验学校", "dropped": 9000}},
		{Kind: abi.EvInputRecv, Payload: map[string]any{"text": "最近这句"}},
		{Kind: abi.EvProcOutput, Payload: map[string]any{"phase": "reply", "text": "最近的回答"}},
	}
	w := RestoreWindow(engine.NewPageStore(), evs, "现在这句")
	defer w.Release()
	msgs, _ := w.Messages()
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	got := b.String()
	if !strings.Contains(got, "7:10 到高新实验学校") {
		t.Fatalf("摘要没装回来: %q", got)
	}
	if strings.Contains(got, "很久以前聊的那件事") || strings.Contains(got, "当时的回答") {
		t.Fatalf("压紧过的原文又装回来了: %q", got)
	}
	if !strings.Contains(got, "最近这句") || !strings.Contains(got, "最近的回答") {
		t.Fatalf("压紧之后的那些该留着: %q", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "user: 现在这句") {
		t.Fatalf("这次要问的话必须在最后: %q", got)
	}
}

// 没压过就照旧全装
func TestRestoreWithoutCompactionUnchanged(t *testing.T) {
	evs := []abi.Event{
		{Kind: abi.EvInputRecv, Payload: map[string]any{"text": "第一句"}},
		{Kind: abi.EvProcOutput, Payload: map[string]any{"phase": "reply", "text": "第一答"}},
	}
	w := RestoreWindow(engine.NewPageStore(), evs, "接着问")
	defer w.Release()
	msgs, _ := w.Messages()
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content + "\n")
	}
	if !strings.Contains(b.String(), "第一句") || !strings.Contains(b.String(), "第一答") {
		t.Fatalf("没压过的历史被吃了: %q", b.String())
	}
}
