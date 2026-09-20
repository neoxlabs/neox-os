package osinit

import (
	"context"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// osWithWaitingProc 一台跑着一个进程的 OS —— 进程只是活着, 不干活
func osWithWaitingProc(t *testing.T) (*OS, abi.ProcessID) {
	t.Helper()
	o := devOS()
	t.Cleanup(func() { o.Shutdown("t") })
	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "room"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	return o, pid
}

// 群聊投递: 送进去的是带上下文的信封, 记下来的必须是**原话**.
//
// 早先两者是同一个字符串, 于是界面上用户那一句变成一大坨转述。
func TestLedgerRecordsWhatUserSaidNotTheEnvelope(t *testing.T) {
	o, pid := osWithWaitingProc(t)
	said := "你们都在哪里"
	envelope := "[屋里刚才]\n回归: 这个不对，得重跑\n\n" + said

	if !o.Deliver(pid, Delivery{Text: envelope, Said: said, From: "我", ID: "u1"}) {
		t.Fatal("投递失败")
	}
	for _, ev := range o.Log().Replay(pid, 0) {
		body, _ := ev.Payload.(map[string]any)
		if ev.Kind == abi.EvInputRecv {
			if body["text"] != said {
				t.Errorf("账本记的是信封不是原话: %v", body["text"])
			}
			if body["utterance"] != "u1" {
				t.Errorf("同一句话的身份没记下来: %v", body["utterance"])
			}
		}
		if ev.Kind == abi.EvCorrection {
			t.Fatalf("信封里别人说的\"不对\"被算成了用户的纠正: %v", body["text"])
		}
	}
}

// 进程收到的仍然是完整信封 —— 上下文是给模型看的, 不能因为"记原话"就丢掉
func TestProcessStillReceivesTheEnvelope(t *testing.T) {
	o, pid := osWithWaitingProc(t)
	envelope := "[屋里刚才]\n发版: 版本号定了\n\n继续"
	if !o.Deliver(pid, Delivery{Text: envelope, Said: "继续", From: "我"}) {
		t.Fatal("投递失败")
	}
	o.mu.Lock()
	box := o.procs[pid].inbox
	o.mu.Unlock()
	got, ok := box.tryTake()
	if !ok {
		t.Fatal("收件箱里没有东西")
	}
	if got.Text != envelope {
		t.Errorf("进程收到的不是信封: %q", got.Text)
	}
}

// 用户**自己**说的纠正照旧要记
func TestRealCorrectionStillRecorded(t *testing.T) {
	o, pid := osWithWaitingProc(t)
	o.Deliver(pid, Delivery{Text: "不对，用 pnpm 不是 npm", Said: "不对，用 pnpm 不是 npm", From: "我"})
	found := false
	for _, ev := range o.Log().Replay(pid, 0) {
		if ev.Kind == abi.EvCorrection {
			found = true
		}
	}
	if !found {
		t.Fatal("用户真的纠正了, 却没记下来")
	}
}
