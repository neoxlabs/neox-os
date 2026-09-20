package osinit

import (
	"context"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func TestIsCorrectionCatchesTheRealTriggers(t *testing.T) {
	yes := []string{
		"不是这样，是按分存",
		"不是那样,用 ledger.py",
		"不对，应该用分",
		"算了还是按分存",
		"账本用分不要用元",
		"用 json 别用 xml",
		"我纠正一下：路径是 proj/ 不是项目/",
		"I meant the ledger uses cents",
		"don't use dollars, use cents",
		"不是 git，是 jj",
	}
	for _, s := range yes {
		if !IsCorrection(s) {
			t.Errorf("该认成纠正: %q", s)
		}
	}
}

func TestIsCorrectionRejectsQuestionsAndChatter(t *testing.T) {
	no := []string{
		"你好",
		"帮我改一下",
		"这是不是该用分？",
		"是不是用 json",
		"不是吧",
		"对不对？",
		"不对吗？",
		"用一下这个文件",
		"don't worry about it",
		"",
		"   ",
	}
	for _, s := range no {
		if IsCorrection(s) {
			t.Errorf("不该认成纠正: %q", s)
		}
	}
}

func TestCorrectionIsRecordedOnSend(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if !o.Send(pid, "账本用分不要用元", "t") {
		t.Fatal("Send 失败")
	}
	var gotCorr, gotInput bool
	for _, ev := range o.Log().Replay(pid, 0) {
		switch ev.Kind {
		case abi.EvCorrection:
			gotCorr = true
			if payloadStr(ev, "text") != "账本用分不要用元" {
				t.Fatalf("纠正原文被改了: %+v", ev.Payload)
			}
		case abi.EvInputRecv:
			gotInput = true
		}
	}
	if !gotInput {
		t.Fatal("纠正也是一句输入, input.recv 必须还在 —— 对话重放靠它")
	}
	if !gotCorr {
		t.Fatal("纠正没单独落账 —— 流水里看不出这是对的还是错的")
	}
}

func TestOrdinaryTalkIsNotACorrectionEvent(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	o.Send(pid, "帮我改一下 README", "t")
	for _, ev := range o.Log().Replay(pid, 0) {
		if ev.Kind == abi.EvCorrection {
			t.Fatal("普通话被记成了纠正")
		}
	}
}

func TestCorrectionsDigestExcludesCurrentThreadAndIsBounded(t *testing.T) {
	snap := map[abi.ProcessID][]abi.Event{
		"old": {
			recThread("old", "t-old"),
			recEv("old", 100, abi.EvCorrection, map[string]any{"text": "账本用分不要用元"}),
		},
		"now": {
			recThread("now", "t-now"),
			recEv("now", 200, abi.EvCorrection, map[string]any{"text": "现在这段的纠正"}),
		},
	}
	got := CorrectionsDigest(snap, "t-now")
	if !strings.Contains(got, "账本用分不要用元") {
		t.Fatalf("过去的纠正丢了:\n%s", got)
	}
	if strings.Contains(got, "现在这段") {
		t.Fatalf("把当前这段的纠正又列了一遍:\n%s", got)
	}
}

func TestCorrectionsDigestIncludesRejections(t *testing.T) {
	snap := map[abi.ProcessID][]abi.Event{
		"old": {
			recThread("old", "t-old"),
			recEv("old", 1, abi.EvDecideRequest, map[string]any{
				"did": "d1", "present": map[string]any{"title": "允许连 pypi.org 吗?"},
			}),
			recEv("old", 2, abi.EvDecideResolved, map[string]any{"did": "d1", "choice": "no"}),
		},
		"now": {recThread("now", "t-now")},
	}
	got := CorrectionsDigest(snap, "t-now")
	if !strings.Contains(got, "拒绝") || !strings.Contains(got, "pypi.org") {
		t.Fatalf("拒绝没进摘要, 而下一段最不该再去问这件事:\n%s", got)
	}
}

func TestCorrectionsDigestIsDeterministicAndCapped(t *testing.T) {
	snap := map[abi.ProcessID][]abi.Event{"old": {recThread("old", "t-old")}}
	for i := 0; i < 20; i++ {
		snap["old"] = append(snap["old"], recEv("old", int64(i+1), abi.EvCorrection,
			map[string]any{"text": "纠正 " + strings.Repeat("x", i+1)}))
	}
	first := CorrectionsDigest(snap, "")
	for i := 0; i < 10; i++ {
		if CorrectionsDigest(snap, "") != first {
			t.Fatal("摘要不稳定 —— 前缀缓存会废")
		}
	}
	if n := strings.Count(first, "\n") + 1; n > correctionsMaxLines {
		t.Fatalf("摘要 %d 行, 没有上限", n)
	}
}

func TestCorrectionsDigestEmptyWhenNone(t *testing.T) {
	if got := CorrectionsDigest(nil, ""); got != "" {
		t.Fatalf("没有纠正却给了摘要: %q", got)
	}
}
