package osinit

import (
	"context"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// **待命必须是免费的.**
//
// 这是这个 OS 的经济前提: 一台机器托很多人的常驻 agent, 而 agent 的
// 常态是等人说话, 不是干活. 待命要是花钱, 整个成本结构就是错的.
//
// 真机量到的(Lima VM, deepseek-v4-flash 干完一个真活之后):
//
//	起点     cpu=0 tick · RSS=1772 kB · 线程=1
//	130 秒后 cpu=0 tick · RSS=1772 kB · 线程=1
//	cgroup   整个对话生命周期累计 CPU 120ms
//
// 但 /proc 的 tick 粒度是 10ms, 所以"0 tick"只能说明"小于 10ms",
// 真实数字要看 cgroup. 这条测试钉的是**机制**: 等待期间不许有
// 任何轮询/心跳/定时器把进程叫醒 —— 那才是待命免费的技术前提.
func TestWaitingCostsNothing(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev, VolumeRoot: t.TempDir()})

	woke := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "t", Name: "待命"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			// 进程干完活就等下一句 —— 这一步必须**真的阻塞**,
			// 不能是"每 N 毫秒查一次收件箱"
			msg, err := pc.Recv()
			if err != nil || msg.Closed {
				return nil, err
			}
			close(woke)
			return msg.Text, nil
		}})
	if err != nil {
		t.Fatal(err)
	}

	// 等它进 waiting
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := o.Info(pid); ok && info.State == abi.StateWaiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, _ := o.Info(pid)
	if info.State != abi.StateWaiting {
		t.Fatalf("等下一句时状态该是 waiting, got %s —— "+
			"不是 waiting 就说明它在忙着轮询, 待命就不免费了", info.State)
	}

	// 待命期间**一条事件都不该产生**. 有心跳/轮询就会在这儿露馅.
	before := o.Log().Length(pid)
	time.Sleep(300 * time.Millisecond)
	if after := o.Log().Length(pid); after != before {
		t.Fatalf("待命期间凭空多了 %d 条事件 —— 有东西在后台空转", after-before)
	}

	// 叫得醒, 而且醒来就是我们发的那句话
	if !o.Send(pid, "醒醒", "test") {
		t.Fatal("待命的进程收不到消息 —— 那不是待命是死了")
	}
	select {
	case <-woke:
	case <-time.After(3 * time.Second):
		t.Fatal("发了消息却叫不醒")
	}
}
