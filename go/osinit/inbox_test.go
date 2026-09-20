package osinit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 待命进程: 起来后 Recv 阻塞, Send 之后醒来
func TestRecvBlocksThenWakes(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	got := make(chan string, 4)
	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			for {
				m, err := pc.Recv()
				if err != nil || m.Closed {
					return "closed", nil
				}
				got <- m.Text
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	// 等它进 waiting —— 待命就该不占计算
	waitState(t, o, pid, abi.StateWaiting)

	if !o.Send(pid, "第一句", "test") {
		t.Fatal("Send 应当成功")
	}
	if v := <-got; v != "第一句" {
		t.Fatalf("收到 %q", v)
	}
	waitState(t, o, pid, abi.StateWaiting)

	o.Send(pid, "第二句", "test")
	if v := <-got; v != "第二句" {
		t.Fatalf("收到 %q", v)
	}

	o.CloseInbox(pid)
	waitState(t, o, pid, abi.StateExited)
}

// 进程忙着的时候说的话不能丢 —— 否则用户以为说了但系统没听见
func TestMessagesQueueWhileBusy(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	release := make(chan struct{})
	got := make(chan string, 8)
	started := make(chan struct{})
	pid, _ := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-release // 模拟正在干活, 还没来得及 Recv
			for {
				m, err := pc.Recv()
				if err != nil || m.Closed {
					return nil, nil
				}
				got <- m.Text
			}
		}})
	<-started

	// 干活期间连说三句
	for _, s := range []string{"a", "b", "c"} {
		if !o.Send(pid, s, "test") {
			t.Fatalf("忙的时候 %s 应当排队而不是被拒", s)
		}
	}
	close(release)

	for _, want := range []string{"a", "b", "c"} {
		select {
		case v := <-got:
			if v != want {
				t.Fatalf("顺序错了: 收到 %q 期望 %q", v, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("没收到 %q —— 忙时说的话被丢了", want)
		}
	}
	o.CloseInbox(pid)
}

// 终态进程收不了信 —— 而且要如实返回 false, 不能静默吞
func TestSendToDeadProcessFails(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	pid, _ := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			return "done", nil
		}})
	waitState(t, o, pid, abi.StateExited)
	if o.Send(pid, "喂", "test") {
		t.Fatal("终态进程不该收得下, 而且要如实报 false")
	}
}

// 关机时不能被一个永远不来的输入钉住
func TestShutdownUnblocksRecv(t *testing.T) {
	o := devOS()
	done := make(chan struct{})
	started := make(chan struct{})
	o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			_, _ = pc.Recv() // 永远不会有人 Send
			close(done)
			return nil, nil
		}})
	<-started
	time.Sleep(20 * time.Millisecond)
	o.Shutdown("bye")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("关机没能把 Recv 叫醒")
	}
}

// 收到输入要进事件日志 —— 事件流要能重放出完整对话
func TestRecvLogged(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	started := make(chan struct{})
	pid, _ := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			pc.Recv()
			return nil, nil
		}})
	<-started
	waitState(t, o, pid, abi.StateWaiting)
	o.Send(pid, "你好", "phone:刘")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range o.Log().Replay(pid, 0) {
			if e.Kind == abi.EvInputRecv {
				m := e.Payload.(map[string]any)
				if m["text"] == "你好" && m["from"] == "phone:刘" {
					return
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("输入没进事件日志")
}

// 并发投递不能丢也不能崩
func TestConcurrentSend(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	const n = 50
	got := make(chan string, n)
	started := make(chan struct{})
	pid, _ := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			for {
				m, err := pc.Recv()
				if err != nil || m.Closed {
					return nil, nil
				}
				got <- m.Text
			}
		}})
	<-started

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); o.Send(pid, "msg", "t") }(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		select {
		case <-got:
		case <-time.After(3 * time.Second):
			t.Fatalf("并发投递丢了第 %d 条", i)
		}
	}
	o.CloseInbox(pid)
}

// 等决策期间收到输入, 状态必须仍是 waiting.
//
// 投递不该猜进程停在哪儿. agent 请求审批后转 waiting,
// 用户接着说了五句话, 每句都把状态改回 running, 而进程其实还卡在
// 等决策 —— 五句全进了收件箱没人取, 界面上却显示在跑.
// 一次没人回答的审批让整个对话永久卡死, 而且完全看不出来.
func TestSendDoesNotFakeRunningWhileAwaitingDecision(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	asked := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(asked)
			// 停在决策上等人 —— 不是停在 Recv 上
			_, _ = pc.Decide(abi.DecisionRequest{
				Present: abi.PresentSpec{Kind: "choice", Title: "允许吗?"}})
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-asked
	waitState(t, o, pid, abi.StateWaiting)

	// 这时候说话: 消息该排队, 状态**不该**变成 running
	if !o.Send(pid, "决策还没答就先说的话", "test") {
		t.Fatal("消息该收得下")
	}
	time.Sleep(50 * time.Millisecond)
	if info, _ := o.Info(pid); info.State != abi.StateWaiting {
		t.Fatalf("还在等决策, 状态却成了 %s —— 界面会显示它在跑", info.State)
	}
}

// 决策答了之后, 排队的输入要能被取到 —— 不能因为等过决策就丢
func TestQueuedInputSurvivesDecision(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	got := make(chan string, 2)
	asked := make(chan struct{})
	pid, _ := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(asked)
			pc.Decide(abi.DecisionRequest{
				Present: abi.PresentSpec{Kind: "choice", Title: "允许吗?"}})
			m, err := pc.Recv()
			if err == nil && !m.Closed {
				got <- m.Text
			}
			return nil, nil
		}})
	<-asked
	waitState(t, o, pid, abi.StateWaiting)
	o.Send(pid, "等决策期间说的话", "test")

	// 找到那条待决策并回答它
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ps := o.Decisions().Pending(); len(ps) > 0 {
			o.Decisions().Resolve(ps[0].DID, "yes", "test", nil)
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case v := <-got:
		if v != "等决策期间说的话" {
			t.Fatalf("取到的是 %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等决策期间说的话丢了")
	}
	o.CloseInbox(pid)
}

// 一句话都没投过、而且进程还没走到 Recv 就关 —— 收件箱是懒创建的,
// 关的时候它根本不存在.
//
// "批准之后自己接着干"依赖严格的时序:
// agent 的第一句话走的是 spawn 时带的任务, **不过收件箱**; 它要把这一轮
// 干完才会调 Recv. 而"批准出网 → 关掉当前进程 → 用扩权后的能力重开"
// 这一步恰好发生在它还在干活的时候 —— 那一刻箱子是 nil, CloseInbox
// 直接返回、一声不吭; 等它干完调 Recv, Recv 自己又建了个**新的、开着的**
// 箱子, 于是它接着等下去, 永远不结束, 收尾事件永远不来.
//
// 现有用例全都是"起来就 Recv"或"先 Send 再 Close", 所以一直没碰到.
func TestCloseInboxWhileProcessStillWorking(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	started := make(chan struct{})
	working := make(chan struct{}) // 放行之后它才去 Recv
	closed := make(chan bool, 1)
	_, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-working // 模拟"第一轮活还没干完", 此时收件箱尚未存在
			m, err := pc.Recv()
			closed <- (err != nil || m.Closed)
			return "done", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	pids := o.List()
	if len(pids) != 1 {
		t.Fatalf("要 1 个进程, 得到 %d", len(pids))
	}
	o.CloseInbox(pids[0].PID) // 它还在干活, 箱子还没建出来
	close(working)

	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("关过的收件箱, Recv 却没说关了 —— 进程会永远等下去")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Recv 一直没返回 —— 关闭在收件箱建出来之前是空操作")
	}
}

// 一句话都没投过就关 —— 收件箱是懒创建的, 关的时候它可能根本不存在.
//
// "批准之后自己接着干"依赖严格的时序: 第一句话走的是 spawn 时带的任务,
// 不过收件箱; 所以进程收到第二句话之前箱子不存在,
// CloseInbox 直接返回、**一声不吭**, 进程停在 waiting 永远不结束.
//
// 现有的用例全都是"先 Send 再 Close", 所以一直没碰到.
func TestCloseInboxBeforeAnyMessageArrives(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			m, err := pc.Recv()
			if err != nil || m.Closed {
				return "closed", nil
			}
			return "got:" + m.Text, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	waitState(t, o, pid, abi.StateWaiting)

	o.CloseInbox(pid)
	// 关上就必须真的结束. 关不上的症状是这里超时 —— 更严重时会永远不结束,
	// 收尾事件也永远不来.
	waitState(t, o, pid, abi.StateExited)
}

// 关了之后再投进来的话不能被收下.
//
// 只让进程退出还不够: 如果 Send 还能成功, 用户会以为话被听见了,
// 而实际上没有任何人会取走它. "静默吞掉"比明说收不下更糟.
func TestSendAfterCloseIsRefusedEvenWithoutPriorMessage(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			m, _ := pc.Recv()
			return m.Closed, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	waitState(t, o, pid, abi.StateWaiting)

	o.CloseInbox(pid)
	if o.Send(pid, "还收得下吗", "test") {
		t.Fatal("收件箱关了还收下了话 —— 用户会以为它被听见了")
	}
}

// 看一眼**不能把话弄丢**.
//
// 危险在于: take() 队列空时会把自己登记成 waiter. 轮询用它的话, 调用方
// 转身就走, 之后 push 进来的那句话就送进了没人收的通道 —— 静默丢掉.
// 而"用户中途插的那句话"正是最不能丢的.
func TestTryRecvNeverSwallowsAMessage(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	started := make(chan struct{})
	got := make(chan string, 4)
	polled := make(chan struct{})
	sent := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			// 干活中途看一眼: 现在没有话
			if _, has := pc.TryRecv(); has {
				got <- "不该有话"
			}
			close(polled)
			// **危险窗口就在这儿**: 消息在"看过一眼之后、正式等之前"进来.
			// 如果那一眼留下了等待通道, 这句话就送进了没人收的地方.
			<-sent
			m, err := pc.Recv()
			if err != nil || m.Closed {
				return "closed", nil
			}
			got <- m.Text
			return "done", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	<-polled
	if !o.Send(pid, "轮询之后说的话", "test") {
		t.Fatal("Send 应当成功")
	}
	close(sent)
	select {
	case v := <-got:
		if v != "轮询之后说的话" {
			t.Fatalf("收到 %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("看一眼之后, 紧接着投进来的话再也收不到了 —— 消息被吞了")
	}
}

// 队列里已经有话时, 看一眼要能拿到, 而且**只拿一次**
func TestTryRecvTakesPendingMessageExactlyOnce(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")

	started := make(chan struct{})
	res := make(chan [2]string, 1)
	pid, err := o.Spawn(abi.ProcessSpec{App: "chat"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			<-started
			a, _ := pc.TryRecv()
			b, hasB := pc.TryRecv()
			second := ""
			if hasB {
				second = b.Text
			}
			res <- [2]string{a.Text, second}
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	o.Send(pid, "插话", "test")
	close(started)
	r := <-res
	if r[0] != "插话" {
		t.Fatalf("第一次没拿到: %q", r[0])
	}
	if r[1] != "" {
		t.Fatalf("同一句话被拿了两次: %q", r[1])
	}
}
