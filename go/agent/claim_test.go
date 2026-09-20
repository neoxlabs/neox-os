package agent

import (
	"testing"
)

// 真机 08:06: "记下了…以后倒推我都按这个来" —— 一个记录工具都没调.
// 话收不回来, 但要让它变成真的: 静默再给一步, 这一步真去记
func TestClaimWithoutRecordGetsOneQuietStep(t *testing.T) {
	sys := newFakeSys()
	var noted []string
	ts := NewToolSet(WithNotes(DefaultTools(),
		func(k, v string) (string, error) { noted = append(noted, k+"="+v); return "ok", nil },
		func(string) (string, error) { return "", nil }))
	ag := &Agent{ABI: sys, Tools: ts, Box: Toolbox{Root: t.TempDir()}, MaxSteps: 10,
		Model: &Scripted{Steps: []Step{
			{Reply: "记下了：8 点到高新是你老婆单位。"},
			{Tool: "remember", Args: map[string]any{"key": "老婆单位", "text": "8 点到高新实验学校"}},
			{Reply: "好"},
		}}}
	if err := ag.Run("8点到高新实验学校，是我老婆的单位。你记一下就行"); err != nil {
		t.Fatal(err)
	}
	if len(noted) != 1 {
		t.Fatalf("说了记下了却没记: %v", noted)
	}
	// 第一句给他看, 补的那一步和它的"好"不给他看
	var shown, quiet int
	for _, e := range sys.events {
		if e["phase"] == "reply" {
			if e["quiet"] == true {
				quiet++
			} else {
				shown++
			}
		}
	}
	if shown != 1 || quiet != 1 {
		t.Fatalf("给他看了 %d 句、静默 %d 句", shown, quiet)
	}
}

// 真记了就不补; 没说"记下了"也不补 —— 这道闸不能变成每轮多一次推理
func TestNoNudgeWhenRecordedOrNotClaimed(t *testing.T) {
	for name, steps := range map[string][]Step{
		"记了": {
			{Tool: "remember", Args: map[string]any{"key": "k", "text": "v"}},
			{Reply: "记下了"},
		},
		"没说": {{Reply: "明早 6:40 我会按实时路况再看一次。"}},
	} {
		sys := newFakeSys()
		ts := NewToolSet(WithNotes(DefaultTools(),
			func(k, v string) (string, error) { return "ok", nil },
			func(string) (string, error) { return "", nil }))
		m := &Scripted{Steps: steps}
		ag := &Agent{ABI: sys, Tools: ts, Box: Toolbox{Root: t.TempDir()}, MaxSteps: 10, Model: m}
		if err := ag.Run("x"); err != nil {
			t.Fatal(name, err)
		}
		for _, e := range sys.events {
			if e["phase"] == "claim_check" {
				t.Errorf("%s: 不该补", name)
			}
		}
	}
}
