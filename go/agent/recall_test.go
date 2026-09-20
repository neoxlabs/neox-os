package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func TestRecallFormatsHitsForTheModel(t *testing.T) {
	got := formatRecallHits("用分", []abi.RecallHit{
		{When: "2023-11-14 22:13", Title: "做个记账小工具", Kind: "用户说",
			Text: "账本用分不要用元"},
	})
	if !strings.Contains(got, "用户说") || !strings.Contains(got, "账本用分不要用元") {
		t.Fatalf("没把那句话写清楚: %s", got)
	}
	if !strings.Contains(got, "做个记账小工具") {
		t.Fatalf("没标这段对话是哪一段: %s", got)
	}
	// 当前这段不在结果里 —— 那是 OS 排的, 但提示还是要说
	if !strings.Contains(got, "当前这段对话不在里面") {
		t.Fatalf("没说当前这段被排除了, 它会以为没查过: %s", got)
	}
}

func TestRecallEmptyQueryIsActionable(t *testing.T) {
	tool := RecallTool(func(string, int) ([]abi.RecallHit, error) { return nil, nil })
	_, err := tool.Run(Toolbox{}, map[string]any{"query": "  "})
	if err == nil || !strings.Contains(err.Error(), "关键词") {
		t.Fatalf("空词要说清给什么: %v", err)
	}
}

func TestRecallNoHitsTellsItToRephrase(t *testing.T) {
	tool := RecallTool(func(string, int) ([]abi.RecallHit, error) { return nil, nil })
	_, err := tool.Run(Toolbox{}, map[string]any{"query": "从来没说过的事"})
	if err == nil || !strings.Contains(err.Error(), "换一组关键词") {
		t.Fatalf("没找到要说换词, 不然它会原样再查: %v", err)
	}
}

func TestRecallNeedsNoCapability(t *testing.T) {
	if RecallTool(nil).Needs != "" {
		t.Fatal("recall 不该要能力轴 —— 它读的是 OS 手里的账本, 不出网也不碰卷")
	}
}

func TestNoRecallMeansNoTool(t *testing.T) {
	ts := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil, nil, nil, nil))
	if _, ok := ts.Get("recall"); ok {
		t.Fatal("没接账本却挂上了 recall")
	}
	with := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil,
		func(string, int) ([]abi.RecallHit, error) { return nil, nil },
		func(name, role string) (string, error) { return "", nil }, nil))
	if _, ok := with.Get("recall"); !ok {
		t.Fatal("接了账本却没挂工具")
	}
}
