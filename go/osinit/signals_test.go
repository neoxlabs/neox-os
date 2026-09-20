package osinit

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 可控时钟 —— 窗口和水位线都跟时间有关.
// 用 sleep 测的话既慢又飘, 而飘的测试比没有测试更糟: 它会教人忽略红灯.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type busHarness struct {
	bus  *SignalBus
	clk  *fakeClock
	mu   sync.Mutex
	digs []Digest
	// n 造信号的序号. **每条的 ID 必须唯一** —— 头一版我拿
	// "来源+种类+时间"当 ID, 于是同一时刻造的 5 条信号 ID 全一样,
	// 被幂等去重正确地挡掉了 4 条, 而我差点以为是窗口的 bug.
	// (这恰好从反面验证了去重是真在工作的.)
	n int
}

func newBusHarness(t *testing.T, o SignalOptions) *busHarness {
	t.Helper()
	h := &busHarness{clk: &fakeClock{t: time.UnixMilli(1_700_000_000_000)}}
	o.Now = h.clk.now
	h.bus = NewSignalBus(nil, func(d Digest) {
		h.mu.Lock()
		h.digs = append(h.digs, d)
		h.mu.Unlock()
	}, o)
	return h
}

func (h *busHarness) got() []Digest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Digest(nil), h.digs...)
}

func (h *busHarness) sig(src, kind string, at time.Time, body map[string]any) abi.Signal {
	h.n++
	return abi.Signal{
		ID: fmt.Sprintf("%s|%s|%d", src, kind, h.n), Source: src, Kind: kind,
		At: at.UnixMilli(), Body: body,
	}
}

// ── 触发器 ① 时间到 ────────────────────────────────────────

// 窗口没到时间就不该投递 —— 否则"缓冲"这件事根本没发生,
// 又回到了"来一条醒一次".
func TestWindowHoldsUntilDue(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: 5 * time.Minute, Lateness: time.Minute})
	now := h.clk.now()
	for i := 0; i < 5; i++ {
		h.bus.Ingest(h.sig("phone.mk", "location", now, map[string]any{"i": i}))
	}
	if n := len(h.got()); n != 0 {
		t.Fatalf("窗口还没到时间就投了 %d 条 —— 缓冲没起作用", n)
	}
	if h.bus.Pending() != 5 {
		t.Fatalf("攒着的应该是 5 条, 实际 %d", h.bus.Pending())
	}

	// 时间走过窗口 + 容忍度, 水位线越过窗口末尾 → 关窗
	h.clk.advance(7 * time.Minute)
	h.bus.Tick()

	got := h.got()
	if len(got) != 1 {
		t.Fatalf("窗口到期该投 1 条摘要, 实际 %d 条", len(got))
	}
	if got[0].Reason != DigestWindow || got[0].Count != 5 {
		t.Fatalf("摘要不对: reason=%s count=%d", got[0].Reason, got[0].Count)
	}
}

// **没有新信号也必须能关窗.**
//
// 只靠 Ingest 驱动关窗 = "没有新消息就永远不通知你", 而最后一条信号
// 往往正是要说的那件事(你到家了, 然后位置就不再变了).
func TestWindowClosesWithoutNewSignals(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: 10 * time.Second})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), map[string]any{"at": "家"}))

	h.clk.advance(5 * time.Minute) // 期间一条新信号都没有
	h.bus.Tick()

	if len(h.got()) != 1 {
		t.Fatal("没有新信号时窗口关不上 —— 最后一条信号永远送不出去")
	}
}

// 空窗口不产出摘要 —— **安静的时候就该什么都不发生**.
// 这条是整个感知层的验收判据: 好的主动智能大部分时候不打扰你.
func TestQuietProducesNothing(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	for i := 0; i < 100; i++ {
		h.clk.advance(time.Minute)
		h.bus.Tick()
	}
	if n := len(h.got()); n != 0 {
		t.Fatalf("一条信号都没有, 却投了 %d 条摘要 —— 这是在制造噪音", n)
	}
}

// ── 触发器 ② 量到 ──────────────────────────────────────────

// 信号暴增时不能憋成一个巨块: 那样模型一次要读几千条,
// 而且第一条到最后一条之间可能已经过去很久.
func TestBatchTriggerClosesEarly(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Hour, MaxBatch: 10, Lateness: time.Minute})
	now := h.clk.now()
	for i := 0; i < 10; i++ {
		h.bus.Ingest(h.sig("ha.livingroom", "device.state", now, map[string]any{"i": i}))
	}
	got := h.got()
	if len(got) != 1 {
		t.Fatalf("攒够 %d 条该提前关窗, 实际投了 %d 条摘要", 10, len(got))
	}
	if got[0].Reason != DigestBatch {
		t.Fatalf("提前关窗的理由该是 batch, 实际 %s", got[0].Reason)
	}
}

// ── 触发器 ③ 穿透 ──────────────────────────────────────────

// "来电话了"不能等窗口 —— 晚 5 分钟说, 电话已经挂了, 不可逆.
func TestUrgentBypassesWindow(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Hour, Lateness: time.Minute})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	if len(h.got()) != 0 {
		t.Fatal("普通信号不该立刻投")
	}

	r := h.bus.Ingest(h.sig("phone.mk", "call.incoming", h.clk.now(),
		map[string]any{"from": "138xxxx", "name": "老王"}))

	if r != IngestUrgent {
		t.Fatalf("来电该走穿透, 实际 %s", r)
	}
	got := h.got()
	if len(got) != 1 || got[0].Reason != DigestUrgent {
		t.Fatalf("来电没有立刻投出去: %+v", got)
	}
	if got[0].Items[0].Sample["name"] != "老王" {
		t.Fatal("穿透的信号必须带原文 —— 聚合掉细节的话, 通知里说不出是谁打来的")
	}
}

// **穿透过的信号不能在关窗时再报一次.**
//
// 报两次比晚报更糟: 用户会开始怀疑这个通道, 而通道的可信度
// 是主动智能唯一的本钱.
func TestUrgentNotRepeatedInWindowDigest(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	h.bus.Ingest(h.sig("phone.mk", "call.incoming", h.clk.now(), map[string]any{"from": "1"}))
	h.clk.advance(5 * time.Minute)
	h.bus.Tick()

	got := h.got()
	if len(got) != 1 {
		t.Fatalf("来电被报了 %d 次, 应该只有 1 次(穿透那次)", len(got))
	}
}

// 紧急与否**由策略表判, 不由采集端判**.
//
// 让采集端自己声明的后果是可预见的: 每个采集端都会觉得自己的事最急.
func TestUrgencyIsPolicyNotSensorClaim(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Hour, Lateness: time.Minute,
		Urgent: []string{"lock.opened"}, // 策略只认这一种
	})
	// 采集端在 body 里自称紧急 —— 不作数
	r := h.bus.Ingest(h.sig("phone.mk", "battery", h.clk.now(),
		map[string]any{"urgent": true, "critical": true, "priority": "high"}))

	if r == IngestUrgent {
		t.Fatal("采集端自称紧急就能穿透 —— 那么所有采集端都会这么写")
	}
	if len(h.got()) != 0 {
		t.Fatal("不在策略表里的 kind 不该穿透")
	}
}

// ── 事件时间 / 水位线 / 幂等 ────────────────────────────────

// 归窗按**事件时间**, 不按到达时间.
//
// 手机离线补传是常态: 11:00 发生的事 11:30 才到. 按到达时间归窗的话,
// "11 点提醒你 12 点的火车"从根上就是错的.
func TestWindowsKeyedByEventTimeNotArrival(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: 5 * time.Minute, Lateness: time.Hour})
	base := h.clk.now()

	// 现在时刻 = base+50min, 但这条信号的事件时间是 base
	h.clk.advance(50 * time.Minute)
	h.bus.Ingest(h.sig("phone.mk", "location", base, map[string]any{"where": "早先"}))
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), map[string]any{"where": "现在"}))

	h.clk.advance(2 * time.Hour)
	h.bus.Tick()

	got := h.got()
	if len(got) != 2 {
		t.Fatalf("两条信号事件时间差 50 分钟, 该落进两个窗口, 实际产出 %d 条摘要", len(got))
	}
	if got[0].From >= got[1].From {
		t.Fatal("摘要该按事件时间先后投递")
	}
}

// 迟到的信号**记下来但不回炉**.
//
// 丢掉 = "那段时间没发生过"; 重开窗口 = 改写已经投出去的历史,
// 而 agent 可能已经据此说过话了. 两种都不行, 所以走第三条路: 记录 + 标注.
func TestLateSignalRecordedNotDropped(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(30 * time.Minute)
	h.bus.Tick()
	before := len(h.got())

	// 一条 20 分钟前的信号现在才到
	old := h.clk.now().Add(-20 * time.Minute)
	r := h.bus.Ingest(h.sig("phone.mk", "location", old, map[string]any{"补传": true}))

	if r != IngestLate {
		t.Fatalf("比水位线老的信号该判迟到, 实际 %s", r)
	}
	if len(h.got()) != before {
		t.Fatal("迟到的信号回炉重开了窗口 —— 那会改写已经投出去的历史")
	}
}

// 采集端重传是常态(断网补发、APP 重启), 同一个 ID 只算一次
func TestDedupByID(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	s := h.sig("phone.mk", "step", h.clk.now(), map[string]any{"n": 100})
	if r := h.bus.Ingest(s); r != IngestAccepted {
		t.Fatalf("第一次该收下, 实际 %s", r)
	}
	for i := 0; i < 3; i++ {
		if r := h.bus.Ingest(s); r != IngestDuplicate {
			t.Fatalf("重传该判重复, 实际 %s", r)
		}
	}
	if h.bus.Pending() != 1 {
		t.Fatalf("重传被计入了窗口: pending=%d", h.bus.Pending())
	}
}

// ── 摘要本身 ────────────────────────────────────────────────

// 摘要要**聚合**, 而且要留一个具体的锚.
//
// 只说"位置变了 37 次"模型没法判断任何事; 给最新那条的原文,
// 因为对状态类信号来说最新的那条就是当前状态.
func TestDigestAggregatesButKeepsLatestSample(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	base := h.clk.now()
	for i := 0; i < 37; i++ {
		h.bus.Ingest(h.sig("phone.mk", "location",
			base.Add(time.Duration(i)*time.Second), map[string]any{"seq": i}))
	}
	h.clk.advance(10 * time.Minute)
	h.bus.Tick()

	got := h.got()
	if len(got) == 0 {
		t.Fatal("没有产出摘要")
	}
	var total int
	var last any
	for _, d := range got {
		for _, it := range d.Items {
			total += it.N
			if it.Sample != nil {
				last = it.Sample["seq"]
			}
		}
	}
	if total != 37 {
		t.Fatalf("聚合把条数算丢了: %d", total)
	}
	if last != 36 {
		t.Fatalf("锚该是最新那条(seq=36), 实际 %v", last)
	}
}

// **同样一批信号必须生成同样的字节.**
//
// 摘要会进上下文, 而上下文是前缀缓存的载体. 顺序随 map 遍历漂的话,
// 缓存会莫名其妙地断 —— 而这种断法是静默的, 只表现为账单变贵.
func TestDigestTextIsDeterministic(t *testing.T) {
	build := func() string {
		h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
		now := h.clk.now()
		for _, src := range []string{"z.src", "a.src", "m.src"} {
			for _, k := range []string{"k2", "k1"} {
				h.bus.Ingest(h.sig(src, k, now, map[string]any{"b": 2, "a": 1, "c": 3}))
			}
		}
		h.clk.advance(10 * time.Minute)
		h.bus.Tick()
		var s string
		for _, d := range h.got() {
			s += d.Text()
		}
		return s
	}
	first := build()
	for i := 0; i < 20; i++ {
		if build() != first {
			t.Fatal("同样的信号生成了不同的摘要文本 —— 前缀缓存会静默失效")
		}
	}
	if !strings.Contains(first, "a.src") {
		t.Fatalf("摘要文本不对:\n%s", first)
	}
}

// 事件时间是补出来的这件事必须记下来 ——
// 补出来的时间混了网络延迟和补传间隔, 跟采集端给的不是一回事
func TestGuessedEventTimeIsMarked(t *testing.T) {
	log := NewEventLog(func() int64 { return 1_700_000_000_000 })
	clk := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
	bus := NewSignalBus(log, nil, SignalOptions{Now: clk.now})
	bus.Ingest(abi.Signal{ID: "x", Source: "s", Kind: "k"}) // 没给 At

	// 用 Replay 而不是 SubscribeAll: **订阅是异步投递的**(enqueue 之后由
	// 订阅者自己的协程排空), 同步读订阅结果必然是空的 —— 头一版我就是
	// 这么写的, 红了才想起来. 要断言"落进日志了"就该读日志本身.
	for _, e := range log.Replay(signalPID, 0) {
		if e.Kind != abi.EvSignal {
			continue
		}
		m, _ := e.Payload.(map[string]any)
		if m["guessedTime"] != true {
			t.Fatal("事件时间是补的, 却没标注 —— 事后追查会得出错误结论")
		}
		return
	}
	t.Fatal("原始信号没有落进事件日志")
}

// **穿透的摘要也要落事件日志.**
//
// 来电确实投到了终端, 而账本里 signal.digest 是 0 ——
// 头一版只有 closeWindow 记, 穿透那条路径绕过了它.
//
// 后果不是少一行日志: 事后追查"它当时到底通知过我什么"会得出
// "从来没通知过"的结论, 而那正是账本存在的理由.
func TestUrgentDigestIsRecorded(t *testing.T) {
	log := NewEventLog(func() int64 { return 1_700_000_000_000 })
	clk := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{Now: clk.now})

	bus.Ingest(abi.Signal{ID: "c1", Source: "phone.mk", Kind: "call.incoming",
		At: clk.now().UnixMilli(), Body: map[string]any{"from": "138"}})

	for _, e := range log.Replay(signalPID, 0) {
		if e.Kind == abi.EvSignalDigest {
			return
		}
	}
	t.Fatal("穿透投出去了, 账本里却没有 signal.digest —— 事后追查会得出'从来没通知过'")
}

// 实际延迟 = 窗口 + 容忍度, **不等于窗口长度**.
//
// 这个数必须能被问出来: 配的人只看窗口会等错时间(我自己就等错过一次).
func TestEffectiveDelayIncludesLateness(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: 10 * time.Second, Lateness: 2 * time.Minute})
	if got := h.bus.EffectiveDelay(); got != 2*time.Minute+10*time.Second {
		t.Fatalf("实际延迟报成了 %s —— 只报窗口长度会让配的人等错时间", got)
	}
}

// **紧急信号迟到了也要送达.**
//
// 这条是真机跑出来的: 假 HA 用固定时间戳, 于是一条 lock.closed
// 被判成迟到 —— 我去看代码才发现, 紧急那条分支在水位线检查**之前**,
// 所以紧急信号天然绕过了迟到判定.
//
// 那个行为是对的(迟到 3 分钟的门锁异常仍然值得知道), 但它当时只是
// 代码顺序的副产品, 没有任何地方说过. 没说过的行为等于随时会被
// 一次"顺手整理一下"改掉 —— 所以钉住它.
func TestUrgentIsDeliveredEvenWhenLate(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	// 先把水位线推到很前面
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(30 * time.Minute)
	h.bus.Tick()

	// 一条 5 分钟前的紧急信号现在才到 —— 在时效内, 仍然算"刚刚"
	//
	// (这个数原来写的是 20 分钟. 后来给紧急加了时效上界之后它就不对了:
	// 见 TestStaleUrgentGoesToBackfill —— 对着昨天的事喊"刚刚"会毁掉通道.)
	old := h.clk.now().Add(-5 * time.Minute)
	r := h.bus.Ingest(h.sig("ha.lock", "lock.opened", old, map[string]any{"door": "大门"}))

	if r != IngestUrgent {
		t.Fatalf("时效内迟到的紧急信号被判成 %s —— 门锁异常晚 3 分钟知道也比不知道强", r)
	}
	last := h.got()[len(h.got())-1]
	if last.Reason != DigestUrgent {
		t.Fatal("迟到的紧急信号没有送达")
	}
}

// ── 补传通道 ────────────────────────────────────────────────

// **迟到的信号必须能到达 agent, 只是要走另一条路.**
//
// 重放一天的轨迹, 7 条里 6 条判迟到, 于是只进账本、
// 没有任何人看得到 —— 而离线补传正是手机采集端存在的理由之一.
func TestBackfillReachesTheAgent(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Minute, Lateness: time.Second,
		BackfillQuiet: 10 * time.Second})
	// 把水位线推到很前面
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(2 * time.Hour)
	h.bus.Tick()
	before := len(h.got())

	// 手机上线, 补传一天的停留记录
	for i := 1; i <= 6; i++ {
		old := h.clk.now().Add(-time.Duration(i) * time.Hour)
		h.bus.Ingest(h.sig("phone.mk", "place.arrived", old, map[string]any{"n": i}))
	}
	if h.bus.PendingBackfill() != 6 {
		t.Fatalf("补传缓冲里该有 6 条, 实际 %d", h.bus.PendingBackfill())
	}

	// 补传突发结束(安静够久) → 会话窗口关闭
	h.clk.advance(30 * time.Second)
	h.bus.Tick()

	got := h.got()
	if len(got) != before+1 {
		t.Fatalf("补传没有产出摘要: 之前 %d 条, 现在 %d 条", before, len(got))
	}
	last := got[len(got)-1]
	if last.Reason != DigestBackfill || last.Count != 6 {
		t.Fatalf("补传摘要不对: reason=%s count=%d", last.Reason, last.Count)
	}
}

// **补传摘要的措辞必须把"已经发生过了"说死.**
//
// 跟实时摘要长得一样的话, agent 会把昨天的门锁当成现在的门锁 ——
// 那两件事该做的反应完全不同.
func TestBackfillTextSaysItIsHistory(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Minute, Lateness: time.Second, BackfillQuiet: time.Second})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(2 * time.Hour)
	h.bus.Tick()
	h.bus.Ingest(h.sig("phone.mk", "place.left",
		h.clk.now().Add(-90*time.Minute), map[string]any{"x": 1}))
	h.clk.advance(5 * time.Second)
	h.bus.Tick()

	txt := h.got()[len(h.got())-1].Text()
	for _, want := range []string{"历史", "已经发生过了", "不需要现在处理"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("补传摘要没说清它是历史(缺 %q):\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "刚刚发生") {
		t.Fatal("补传摘要用了实时的措辞 —— agent 会把昨天的事当成现在的")
	}
}

// **过了时效的紧急信号不许再喊"刚刚".**
//
// 这条是真机重放逼出来的: 上一版"紧急先于水位线检查"没有上界,
// 于是一条 20 小时前补传上来的 lock.opened 会让系统当场喊
// "刚刚发生了一件需要立刻知道的事" —— 对着昨天的事喊"刚刚",
// 一次就够毁掉这条通道的可信度.
func TestStaleUrgentGoesToBackfill(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Minute, Lateness: time.Second,
		UrgentGrace: 15 * time.Minute, BackfillQuiet: time.Second})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(24 * time.Hour)
	h.bus.Tick()

	// 20 小时前的门锁事件现在才补传上来
	old := h.clk.now().Add(-20 * time.Hour)
	r := h.bus.Ingest(h.sig("ha.lock", "lock.opened", old, map[string]any{"door": "大门"}))

	if r == IngestUrgent {
		t.Fatal("20 小时前的门锁事件仍然走了穿透 —— 系统会对着昨天的事喊'刚刚'")
	}
	h.clk.advance(5 * time.Second)
	h.bus.Tick()
	last := h.got()[len(h.got())-1]
	if last.Reason != DigestBackfill {
		t.Fatalf("过期的紧急信号该转补传, 实际 %s", last.Reason)
	}
}

// 补传一次可能跨好几天, 只显示时分秒会让人读错日期
func TestBackfillAcrossDaysShowsDate(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Minute, Lateness: time.Second, BackfillQuiet: time.Second})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(72 * time.Hour)
	h.bus.Tick()
	for _, ago := range []time.Duration{70 * time.Hour, 2 * time.Hour} {
		h.bus.Ingest(h.sig("phone.mk", "place.arrived",
			h.clk.now().Add(-ago), map[string]any{"a": ago.String()}))
	}
	h.clk.advance(5 * time.Second)
	h.bus.Tick()

	txt := h.got()[len(h.got())-1].Text()
	if !strings.Contains(txt, "-") {
		t.Fatalf("跨天的补传摘要没显示日期, 读起来像同一天:\n%s", txt)
	}
}

// 补传攒太多要先出一份 —— 一次灌进来一万条的话,
// 憋成一个摘要模型根本读不完
func TestBackfillFlushesWhenTooMany(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Minute, Lateness: time.Second,
		BackfillMax: 10, BackfillQuiet: time.Hour})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(2 * time.Hour)
	h.bus.Tick()
	before := len(h.got())

	old := h.clk.now().Add(-90 * time.Minute)
	for i := 0; i < 10; i++ {
		h.bus.Ingest(h.sig("phone.mk", "place.arrived", old, map[string]any{"i": i}))
	}
	if len(h.got()) != before+1 {
		t.Fatal("补传攒够 10 条没有提前出摘要 —— 一万条会憋成一个读不完的块")
	}
}

// **整数必须按整数打 —— 这次是给模型看的.**
//
// 信号过一趟 JSON 之后数字全是 float64, 而 %v 对 float64 用 %g:
// 摘要里出现了 stayedMs=3.75e+06.
//
// 面向终端的 intish 处理曾经只覆盖给人看的输出.
// 模型读科学计数法比人还糟: 它可能照着算, 也可能当乱码跳过.
func TestDigestRendersIntegersAsIntegers(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	// 模拟"过了一趟 JSON"的形状: 数字都是 float64
	h.bus.Ingest(abi.Signal{
		ID: "x", Source: "phone.mk", Kind: "place.left",
		At:   h.clk.now().UnixMilli(),
		Body: map[string]any{"stayedMs": float64(3750000), "pct": float64(87)},
	})
	h.clk.advance(10 * time.Minute)
	h.bus.Tick()

	txt := h.got()[0].Text()
	if strings.Contains(txt, "e+") {
		t.Fatalf("摘要里出现了科学计数法, 模型会读错:\n%s", txt)
	}
	// 非时长字段仍然按整数原样打
	if !strings.Contains(txt, "pct=87") {
		t.Fatalf("整数没按整数打:\n%s", txt)
	}
	// 时长字段改成人话了(见 TestDigestRendersDurationsAsHumanText) ——
	// 这条测试原来断言的是 stayedMs=3750000 的原样输出, 那是**旧契约**.
	// 换成断言"至少不是科学计数法", 那才是它本来要守的东西
	if strings.Contains(txt, "3.75e") {
		t.Fatalf("时长退回了科学计数法:\n%s", txt)
	}
}

// ── 可知时间 vs 发生时间 ────────────────────────────────────

// **一条"刚算出来的、描述 5 分钟前的事"的信号必须走实时窗口, 不是补传.**
//
// 手机的 place.arrived 事件时间是"停留开始那一刻",
// 而这件事要到 dwell 之后才判得出来. 生产配置 dwell 5 分钟、容忍 2 分钟,
// 于是**每一条"你到公司了"都比水位线老 3 分钟, 永远进补传**,
// 主动进程收到的是"这些已经发生过了" —— 而那恰恰是最该实时说的一类.
func TestKnownAtKeepsComputedSignalsLive(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Minute, Lateness: 2 * time.Minute, BackfillQuiet: time.Second})
	// 先让水位线往前走
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(30 * time.Minute)
	h.bus.Tick()
	before := len(h.got())

	// 一条 5 分钟前开始的停留, **现在**才算出来
	s := h.sig("phone.mk", "place.arrived", h.clk.now().Add(-5*time.Minute), nil)
	s.KnownAt = h.clk.now().UnixMilli()
	if r := h.bus.Ingest(s); r != IngestAccepted {
		t.Fatalf("刚算出来的到达被判成 %s —— 它会进历史通道, 而这是最该实时说的一类", r)
	}

	h.clk.advance(5 * time.Minute)
	h.bus.Tick()
	got := h.got()
	if len(got) != before+1 {
		t.Fatalf("没有产出实时摘要: %d → %d", before, len(got))
	}
	last := got[len(got)-1]
	if last.Reason == DigestBackfill {
		t.Fatal("刚算出来的到达进了补传通道")
	}
	// **内容里的时间仍然是发生时刻** —— "你 19:00 到的", 不是"19:05 到的"
	want := h.clk.now().Add(-10 * time.Minute).UnixMilli()
	if last.Items[0].First != want {
		t.Fatalf("摘要里的时间用了可知时刻(%d), 该用发生时刻(%d)",
			last.Items[0].First, want)
	}
}

// **KnownAt 不是"上传时刻".**
//
// 一部离线一天的手机补传上来, 那些信号当时就算出来了, 只是发不出去 ——
// 它们的 KnownAt 是当时, 所以照样该走补传.
// 填成上传时刻的话, 补传会伪装成实时, 主动进程会拿昨天的事当现在的敲门.
func TestBackfilledSignalsStayBackfilled(t *testing.T) {
	h := newBusHarness(t, SignalOptions{
		Window: time.Minute, Lateness: time.Second, BackfillQuiet: time.Second})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(6 * time.Hour)
	h.bus.Tick()

	// 6 小时前发生、6 小时前就算出来了, 现在才传上来
	old := h.clk.now().Add(-6 * time.Hour)
	s := h.sig("phone.mk", "place.arrived", old, nil)
	s.KnownAt = old.UnixMilli()
	if r := h.bus.Ingest(s); r != IngestLate {
		t.Fatalf("补传的信号被当成实时(%s) —— 主动进程会拿昨天的事敲门", r)
	}
}

// 时钟倒挂要纠正: KnownAt 早于 At 只可能是采集端时钟坏了,
// 照单全收的话它能绕过水位线, 而我们看不出来
func TestKnownAtBeforeAtIsCorrected(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(time.Hour)
	h.bus.Tick()

	old := h.clk.now().Add(-30 * time.Minute)
	s := h.sig("phone.mk", "place.arrived", old, nil)
	s.KnownAt = old.Add(-10 * time.Minute).UnixMilli() // 比发生还早
	if r := h.bus.Ingest(s); r != IngestLate {
		t.Fatalf("时钟倒挂的信号绕过了水位线: %s", r)
	}
}

// 缺省行为不能变: 不给 KnownAt 的信号跟以前一模一样
func TestKnownAtDefaultsToAt(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	if r := h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil)); r != IngestAccepted {
		t.Fatalf("不给 KnownAt 的普通信号行为变了: %s", r)
	}
}

// **容忍度必须大于采集端的上传周期, 否则一切都是迟到.**
//
// 真机演示时撞出来的: 容忍度 4 秒, 手机每 10 秒批量传一次,
// 于是连"你刚到公司"都被判成历史 —— 而它看起来完全正常,
// 信号都在、账本都有, 只是全在错误的那条通道上.
func TestLatenessMustCoverUploadPeriod(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: 4 * time.Second})
	if !h.bus.LatenessTooSmallFor(10 * time.Second) {
		t.Fatal("容忍度 4 秒配上 10 秒的上传周期, 自检没报 —— 每条信号都会判迟到")
	}
	h2 := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: 2 * time.Minute})
	if h2.bus.LatenessTooSmallFor(10 * time.Second) {
		t.Fatal("容忍度 2 分钟够 10 秒上传周期用, 不该报")
	}
	// 真实迟到的路径: 上传周期内到达的信号必须还算实时
	now := h2.clk.now()
	s := h2.sig("phone.mk", "place.arrived", now.Add(-30*time.Second), nil)
	s.KnownAt = now.Add(-30 * time.Second).UnixMilli()
	if r := h2.bus.Ingest(s); r != IngestAccepted {
		t.Fatalf("30 秒前算出来的信号在 2 分钟容忍度下被判 %s", r)
	}
}

// **时长要换算成人话.**
//
// 摘要里出现的是 stayedMs=75216. 模型当然能算, 但每一次让它算的
// 东西都是一次它可能算错、而且没人会发现的地方 —— 而"在公司待了多久"
// 正是它判断值不值得说的依据之一.
func TestDigestRendersDurationsAsHumanText(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Second})
	h.bus.Ingest(abi.Signal{
		ID: "x", Source: "phone.mk", Kind: "place.left", At: h.clk.now().UnixMilli(),
		Body: map[string]any{"stayedMs": float64(75216), "地点": "公司"},
	})
	h.clk.advance(10 * time.Minute)
	h.bus.Tick()
	txt := h.got()[0].Text()
	if strings.Contains(txt, "75216") {
		t.Fatalf("时长还是毫秒原文:\n%s", txt)
	}
	if !strings.Contains(txt, "stayed=1 分钟") {
		t.Fatalf("时长没换算成人话:\n%s", txt)
	}
	// 小时级的也要对
	if got := humanDuration(3 * 3600 * 1000); got != "3.0 小时" {
		t.Fatalf("小时级换算错了: %s", got)
	}
	if got := humanDuration(45 * 1000); got != "45 秒" {
		t.Fatalf("秒级换算错了: %s", got)
	}
}

// **一个时钟不对的采集端不能饿死所有别的采集端.**
//
// 水位线取 max(最大可知时间, 墙钟) 是为了"没有新信号时窗口也能关",
// 但副作用是: 任何一个采集端只要报出未来的时间戳, 就把水位线推到未来,
// 于是别人的正常信号全部变成"比水位线老", 全判迟到.
//
// 日尺度合成时撞到的: 一个用错时钟的桥接(快 24 小时)让**手机的信号
// 一条不剩全迟到**. 而表现是"另一个采集端突然全不工作了" ——
// 查起来会先怀疑那个受害者.
func TestFutureTimestampsCannotStarveOtherCollectors(t *testing.T) {
	h := newBusHarness(t, SignalOptions{Window: time.Minute, Lateness: time.Minute})
	now := h.clk.now()

	// 一个时钟快一天的采集端
	bad := h.sig("ha.broken", "device.state", now.Add(24*time.Hour), nil)
	bad.KnownAt = now.Add(24 * time.Hour).UnixMilli()
	h.bus.Ingest(bad)

	// 另一个采集端的正常信号 —— 不该被它连累
	good := h.sig("phone.mk", "place.arrived", now, nil)
	good.KnownAt = now.UnixMilli()
	if r := h.bus.Ingest(good); r != IngestAccepted {
		t.Fatalf("正常信号被一个时钟坏掉的采集端连累成 %s —— "+
			"表现会是'另一个采集端突然全不工作了'", r)
	}
	if wm := h.bus.Watermark(); wm > now.UnixMilli() {
		t.Fatalf("水位线被推到了未来: %d > %d", wm, now.UnixMilli())
	}
}

// ── 崩溃恢复 ────────────────────────────────────────────────

// **窗口里攒着的信号不能随进程一起死.**
//
// 这是最难发现的一种丢失: 没有报错、账本完整、事后翻也能翻到那些信号,
// 只是"它们从来没被送到过任何人面前". 跟闹钟那条是同一类(活不过进程),
// 而窗口一直没做.
func TestPendingSignalsSurviveRestart(t *testing.T) {
	clk := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
	log := NewEventLog(func() int64 { return clk.now().UnixMilli() })
	var digs []Digest
	bus := NewSignalBus(log, func(d Digest) { digs = append(digs, d) },
		SignalOptions{Window: 5 * time.Minute, Lateness: time.Minute, Now: clk.now})

	// 攒了三条, 窗口还没关
	for i := 0; i < 3; i++ {
		bus.Ingest(abi.Signal{
			ID: fmt.Sprintf("s%d", i), Source: "phone.mk", Kind: "place.arrived",
			At: clk.now().UnixMilli(), Body: map[string]any{"i": i}})
		clk.advance(10 * time.Second)
	}
	if len(digs) != 0 {
		t.Fatal("窗口不该已经关了")
	}

	// ── 进程死掉, 重启 ──
	clk.advance(30 * time.Minute)
	var digs2 []Digest
	bus2 := NewSignalBus(nil, func(d Digest) { digs2 = append(digs2, d) },
		SignalOptions{Window: 5 * time.Minute, Lateness: time.Minute,
			BackfillQuiet: 10 * time.Second, Now: clk.now})

	if n := bus2.RecoverPending(log.Replay(signalPID, 0)); n != 3 {
		t.Fatalf("捞回来 %d 条, 该是 3 条 —— 剩下的永远不会被送到任何人面前", n)
	}
	clk.advance(30 * time.Second)
	bus2.Tick()

	// **一份摘要, 而且全走补传.**
	//
	// 头一版这里出来两份: 第一条被当成实时(新总线的水位线还是 0),
	// 其余走补传. 现象很怪 —— 同一批捞回来的信号, 第一条报成
	// "这段时间外界发生的事", 其余报成"这些已经发生过了".
	if len(digs2) != 1 {
		t.Fatalf("捞回来的产出了 %d 份摘要, 该只有一份补传: %+v", len(digs2), digs2)
	}
	if digs2[0].Reason != DigestBackfill {
		t.Fatalf("捞回来的走了 %s 通道, 该走补传 —— "+
			"当成'刚刚'再报一次就是对着旧事喊刚刚", digs2[0].Reason)
	}
	if digs2[0].Count != 3 {
		t.Fatalf("捞回来 %d 条", digs2[0].Count)
	}
}

// **已经总结过的不许再捞一遍** —— 否则每次重启用户都被念一遍旧事.
//
// 判据是"最后一条摘要之后的信号": 窗口一关就落一条 digest,
// 所以顺序本身就是答案, 不需要额外记状态.
func TestRecoverSkipsAlreadyDigested(t *testing.T) {
	clk := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
	log := NewEventLog(func() int64 { return clk.now().UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second, Now: clk.now})

	// 第一批: 会被窗口总结掉
	for i := 0; i < 3; i++ {
		bus.Ingest(abi.Signal{ID: fmt.Sprintf("old%d", i), Source: "x", Kind: "y",
			At: clk.now().UnixMilli()})
	}
	clk.advance(5 * time.Minute)
	bus.Tick() // 关窗 → 落 digest

	// 第二批: 还挂在窗口里
	for i := 0; i < 2; i++ {
		bus.Ingest(abi.Signal{ID: fmt.Sprintf("new%d", i), Source: "x", Kind: "y",
			At: clk.now().UnixMilli()})
	}

	bus2 := NewSignalBus(nil, func(Digest) {}, SignalOptions{Now: clk.now})
	if n := bus2.RecoverPending(log.Replay(signalPID, 0)); n != 2 {
		t.Fatalf("捞回来 %d 条, 该只有没总结过的那 2 条 —— "+
			"多捞的话每次重启用户都会被念一遍旧事", n)
	}
}

// 幂等键要保住原来那个: 捞回来的跟原件是同一件事.
// 重新生成的话, 万一它其实已经被总结过(崩在落 digest 事件的前一刻),
// 用户就会看到重复的一条
func TestRecoverKeepsOriginalIDs(t *testing.T) {
	clk := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
	log := NewEventLog(func() int64 { return clk.now().UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{Now: clk.now})
	bus.Ingest(abi.Signal{ID: "原件", Source: "x", Kind: "y",
		At: clk.now().UnixMilli()})

	bus2 := NewSignalBus(nil, func(Digest) {}, SignalOptions{Now: clk.now})
	bus2.RecoverPending(log.Replay(signalPID, 0))
	// 同一个 ID 再来一次要判重
	if r := bus2.Ingest(abi.Signal{ID: "原件", Source: "x", Kind: "y",
		At: clk.now().UnixMilli()}); r != IngestDuplicate {
		t.Fatalf("捞回来的信号换了幂等键(%s) —— 原件再到会被当成新的", r)
	}
}

// 空账本 / 没有待处理的, 不该捞出东西来
func TestRecoverNothingWhenAllDigested(t *testing.T) {
	bus := NewSignalBus(nil, func(Digest) {}, SignalOptions{})
	if n := bus.RecoverPending(nil); n != 0 {
		t.Fatalf("空账本捞出了 %d 条", n)
	}
	evs := []abi.Event{
		{Kind: abi.EvSignal, Payload: map[string]any{"id": "a", "kind": "k"}},
		{Kind: abi.EvSignalDigest, Payload: map[string]any{"reason": "window"}},
	}
	if n := bus.RecoverPending(evs); n != 0 {
		t.Fatalf("全总结过了却捞出 %d 条", n)
	}
}
