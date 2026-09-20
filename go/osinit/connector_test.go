package osinit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

type fakeConn struct {
	name string
	err  error
	sigs []abi.Signal
}

func (f *fakeConn) Name() string         { return f.name }
func (f *fakeConn) Every() time.Duration { return time.Hour }
func (f *fakeConn) Fetch(context.Context) ([]abi.Signal, error) {
	return f.sigs, f.err
}

// 拉到的东西**走同一条总线** —— 不给接入器开第二条路
func TestConnectorFeedsTheSameBus(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second})
	c := NewConnectors(bus)
	c.Add(&fakeConn{name: "weather", sigs: []abi.Signal{
		{ID: "w1", Kind: "weather.now", Body: map[string]any{"text": "晴"}},
	}})
	stop := c.Start()
	defer stop()
	time.Sleep(80 * time.Millisecond)

	if bus.Pending() == 0 {
		t.Fatal("接入器拉到了, 总线里没有 —— 那它就自己开了第二条路")
	}
}

// **"还没准备好"不该报成"坏了"**: 一台刚装好的机器会立刻推一条
// "采集端出问题了", 而它其实一切正常 —— 用户学到的是"这些警报不用看"
func TestConnectorNotReadyIsNotBroken(t *testing.T) {
	clock := time.Now()
	log := NewEventLog(func() int64 { return clock.UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second,
		Now: func() time.Time { return clock },
	})
	c := NewConnectors(bus)
	conn := &fakeConn{name: "weather",
		err: fmt.Errorf("还不知道你在哪儿: %w", abi.ErrNotReady)}
	// 跟上面那条一样连着失败很多轮 —— 而**它仍然不该被说成瞎了**
	for i := 0; i < 5; i++ {
		c.once(context.Background(), conn, 30*time.Second)
		clock = clock.Add(30 * time.Second)
	}

	for _, st := range bus.StaleCollectors() {
		if st.Source == "weather" && st.Blind {
			t.Fatal("把'还没准备好'报成了'它瞎了' —— 那会在装好第一天就推一条假警报")
		}
	}
}

// 真的坏了要说 —— 不说的话一个过期的密钥会让这一路静默死掉.
//
//	要等过门槛才算数(抖一次不该喊, 见 minStaleSilence), 所以这里
//	推一个假时钟往前走, 而不是真的睡 90 秒
func TestConnectorRealFailureIsReported(t *testing.T) {
	clock := time.Now()
	log := NewEventLog(func() int64 { return clock.UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second,
		Now: func() time.Time { return clock },
	})
	c := NewConnectors(bus)
	conn := &fakeConn{name: "weather", err: fmt.Errorf("HTTP 401")}

	// **直接推 once, 不走 goroutine**: 真实节奏下要连着失败一分半才
	// 算数(minStaleSilence), 而"抖一次不该喊"那条正是要测的一部分 ——
	// 睡 90 秒的测试没人会留着
	for i := 0; i < 5; i++ {
		c.once(context.Background(), conn, 30*time.Second)
		clock = clock.Add(30 * time.Second)
	}

	found := false
	for _, st := range bus.StaleCollectors() {
		if st.Source == "weather" && st.Blind {
			found = true
			if st.Why == "" {
				t.Fatal("说了它瞎了, 却没说为什么 —— 用户不知道该去查凭据还是查网")
			}
		}
	}
	if !found {
		t.Fatal("401 之后一个字都没说 —— 用户看到的会是'它从来不提醒我带伞'")
	}
}
