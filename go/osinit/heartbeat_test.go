package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func hbBus(t *testing.T, now func() time.Time) *SignalBus {
	t.Helper()
	return NewSignalBus(nil, func(Digest) {}, SignalOptions{Now: now})
}

// **采集端瞎了, 而 OS 什么都不知道.**
//
// 长跑里真的发生了: HA 的令牌过期, 采集器老老实实每次都报错重试,
// 而 OS 那边一个字都没有 —— 已经八分钟没有任何信号, 用户看到的是
// "今天很安静".
//
// 这跟报告里那条"'今天真的很安静'和'日报坏了'长得一模一样"是同一个失败,
// 只是发生在更外面一层: 整个感知层可以静默地死掉.
//
// 而这正是"采集端在外面"那条边界的代价 —— 它可以死、可以重启、
// 可以跑在另一台机器上, 那 OS 就**必须**能发现它没了.
func TestStaleCollectorIsNoticed(t *testing.T) {
	now := time.Now()
	bus := hbBus(t, func() time.Time { return now })
	bus.Heartbeat("ha.livingroom", 10*time.Second)

	// 它平时每 10 秒报一次, 现在五分钟没消息了
	now = now.Add(5 * time.Minute)
	stale := bus.StaleCollectors()
	if len(stale) != 1 || stale[0].Source != "ha.livingroom" {
		t.Fatalf("五分钟没消息了却没被发现: %+v", stale)
	}
	if stale[0].Silent < 4*time.Minute {
		t.Fatalf("说它才静了 %s —— 数不对用户没法判断严不严重", stale[0].Silent)
	}
}

// **节奏由采集端自己报** —— OS 不该知道一个家居桥接多久拉一次.
// 一部手机十分钟传一次是正常的, 一个家居桥接十分钟不吭声就是坏了
func TestToleranceFollowsTheCollectorsOwnPace(t *testing.T) {
	now := time.Now()
	bus := hbBus(t, func() time.Time { return now })
	bus.Heartbeat("ha.fast", 10*time.Second)
	bus.Heartbeat("phone.slow", 10*time.Minute)

	now = now.Add(2 * time.Minute) // 对快的来说是很久, 对慢的还没到
	var names []string
	for _, s := range bus.StaleCollectors() {
		names = append(names, s.Source)
	}
	if len(names) != 1 || names[0] != "ha.fast" {
		t.Fatalf("判成失联的是 %v —— 该只有那个快的", names)
	}
}

// 又来消息了就该恢复 —— 而且要能看出"它回来了"
func TestCollectorRecovers(t *testing.T) {
	now := time.Now()
	bus := hbBus(t, func() time.Time { return now })
	bus.Heartbeat("ha.x", 10*time.Second)
	now = now.Add(5 * time.Minute)
	if len(bus.StaleCollectors()) != 1 {
		t.Fatal("没先判成失联")
	}
	bus.Heartbeat("ha.x", 10*time.Second)
	if len(bus.StaleCollectors()) != 0 {
		t.Fatal("又来消息了还说它失联")
	}
}

// **投信号本身就是报到** —— 一个一直在投的采集端不该还要额外心跳,
// 那等于让它为了证明活着而多打一次
func TestIngestCountsAsHeartbeat(t *testing.T) {
	now := time.Now()
	bus := hbBus(t, func() time.Time { return now })
	bus.Heartbeat("phone.mk", time.Minute)
	now = now.Add(30 * time.Second)
	bus.Ingest(sigAt("phone.mk", "phone.moved", "s1", now.UnixMilli()))
	now = now.Add(90 * time.Second) // 距上次心跳 2 分钟, 距上次信号 90 秒
	if n := len(bus.StaleCollectors()); n != 0 {
		t.Fatalf("刚投过信号还被判失联(%d 个) —— 那等于让它为了证明活着多打一次", n)
	}
}

// 从没报到过的来源不算失联 —— 它可能根本就没接上来, 那不是"它没了"
func TestUnknownSourceIsNotStale(t *testing.T) {
	bus := hbBus(t, time.Now)
	if n := len(bus.StaleCollectors()); n != 0 {
		t.Fatalf("一个采集端都没接, 却报了 %d 个失联", n)
	}
}

func sigAt(src, kind, id string, at int64) abi.Signal {
	return abi.Signal{ID: id, Source: src, Kind: kind, At: at, KnownAt: at}
}

// **说一次就够了.**
//
// 一个采集端挂一整夜, 按窗口报的话就是几百行"它还没回来" ——
// 而喊多了跟不喊一样: 用户会开始忽略它, 那时候真出事也没人看.
// (跟打扰预算那条是同一个道理, 只是这条不经过模型.)
func TestStaleIsAnnouncedOnce(t *testing.T) {
	now := time.Now()
	bus := hbBus(t, func() time.Time { return now })
	bus.Heartbeat("ha.x", 10*time.Second)
	now = now.Add(5 * time.Minute)

	first := bus.NewlyStale()
	if len(first) != 1 {
		t.Fatalf("第一次没报出来: %+v", first)
	}
	if again := bus.NewlyStale(); len(again) != 0 {
		t.Fatalf("同一次失联报了第二遍: %+v —— 挂一整夜就是几百行", again)
	}
}

// **回来了要说, 而且下次再挂还要能再说.**
//
// 只报一次的代价是: 恢复之后必须把"报过了"这件事清掉, 否则第二次
// 失联就永远没人知道了 —— 那比一开始就不报更糟
func TestRecoveryIsAnnouncedAndRearms(t *testing.T) {
	now := time.Now()
	bus := hbBus(t, func() time.Time { return now })
	bus.Heartbeat("ha.x", 10*time.Second)
	now = now.Add(5 * time.Minute)
	bus.NewlyStale()

	bus.Heartbeat("ha.x", 10*time.Second) // 回来了
	if back := bus.NewlyBack(); len(back) != 1 || back[0] != "ha.x" {
		t.Fatalf("回来了没说: %v", back)
	}
	if again := bus.NewlyBack(); len(again) != 0 {
		t.Fatal("回来这件事说了两遍")
	}
	now = now.Add(5 * time.Minute) // 又挂了
	if len(bus.NewlyStale()) != 1 {
		t.Fatal("第二次失联没被报出来 —— 只报一次不能是只报一辈子一次")
	}
}

// **检查不能挂在摘要那条路上.**
//
// 采集端死了就没有信号, 没有信号就没有摘要 —— 而我第一版正是把
// 检查放在摘要回调里. 那条路在最需要它的时候**永远走不到**:
// 感知层越是彻底地瞎了, 越没人发现.
//
// 差一点就这么发出去了. 挂在总线自己的心跳上才行.
func TestStaleCheckRunsWithoutAnySignals(t *testing.T) {
	now := time.Now()
	var told []StaleCollector
	bus := NewSignalBus(nil, func(Digest) { t.Fatal("不该有摘要") },
		SignalOptions{
			Now:     func() time.Time { return now },
			OnStale: func(s []StaleCollector) { told = append(told, s...) },
		})
	bus.Heartbeat("ha", 10*time.Second)
	now = now.Add(5 * time.Minute)

	bus.Tick() // 一条信号都没有, 只是时间过去了
	if len(told) != 1 || told[0].Source != "ha" {
		t.Fatalf("一条信号都没有的时候没发现失联: %+v —— "+
			"而那正是感知层彻底瞎掉的样子", told)
	}
}

// **采集端活着, 但它什么都拿不到 —— 而心跳在跳, OS 以为一切正常.**
//
// 这正是那天真实发生的事: HA 令牌过期, 采集器每次都报错重试,
// 心跳照打(S42 特意把报到放在拉取**之前**, 好让"我还在, 只是投不出
// 东西"能传出来). 结果是 S42 那道闸看到的是"它很健康", 而账本里
// 一条信号都没有.
//
// **S42 只治了"采集端死了", 没治"采集端瞎了"** —— 而后者才是那天
// 真正发生的. 真机复现: 给一个坏令牌, 跑 2.5 分钟, 采集器报错 3 次,
// 账本 0 条信号, OS 一个字都没说.
func TestBlindCollectorIsNoticed(t *testing.T) {
	now := time.Now()
	var told []StaleCollector
	bus := NewSignalBus(nil, func(Digest) {}, SignalOptions{
		Now:     func() time.Time { return now },
		OnStale: func(s []StaleCollector) { told = append(told, s...) },
	})
	// 心跳一直在跳, 但每次都说"我拿不到数据"
	for i := 0; i < 20; i++ {
		// **照采集器真实的顺序来**: 每轮先报到("我还在"), 再报失败.
		// 第一版里报到那一下把"瞎了"的计时清零了, 门槛永远到不了
		bus.HeartbeatAlive("ha", 10*time.Second)
		bus.HeartbeatFailing("ha", 10*time.Second, "HA 拒绝了 token")
		now = now.Add(10 * time.Second)
		bus.Tick()
	}
	if len(told) == 0 {
		t.Fatal("心跳在跳但一条数据都拿不到, OS 什么都没说 —— " +
			"用户看到的还是'今天很安静'")
	}
	if told[0].Why == "" {
		t.Fatalf("没说为什么拿不到: %+v —— 用户不知道该去修什么", told[0])
	}
	if !told[0].Blind {
		t.Fatalf("报成了'失联'而不是'瞎了': %+v —— 这两件事的下一步不一样: "+
			"一个是去看采集端还在不在, 一个是去看它的凭据", told[0])
	}
}

// **一次网抖不该喊** —— 跟失联同一个门槛
func TestOneFailedPollIsNotBlindness(t *testing.T) {
	now := time.Now()
	var told []StaleCollector
	bus := NewSignalBus(nil, func(Digest) {}, SignalOptions{
		Now:     func() time.Time { return now },
		OnStale: func(s []StaleCollector) { told = append(told, s...) },
	})
	bus.HeartbeatFailing("ha", 10*time.Second, "一次超时")
	now = now.Add(10 * time.Second)
	bus.Tick()
	bus.Heartbeat("ha", 10*time.Second) // 下一次就好了
	now = now.Add(10 * time.Second)
	bus.Tick()
	if len(told) != 0 {
		t.Fatalf("抖了一次就喊: %+v —— 喊多了跟不喊一样", told)
	}
}

// 拿到数据了要说一声, 而且下次再瞎还要能再说
func TestBlindnessRecoversAndRearms(t *testing.T) {
	now := time.Now()
	bus := NewSignalBus(nil, func(Digest) {}, SignalOptions{
		Now: func() time.Time { return now },
	})
	for i := 0; i < 20; i++ {
		bus.HeartbeatAlive("ha", 10*time.Second)
		bus.HeartbeatFailing("ha", 10*time.Second, "坏了")
		now = now.Add(10 * time.Second)
	}
	if len(bus.NewlyStale()) != 1 {
		t.Fatal("没先判成瞎了")
	}
	bus.Heartbeat("ha", 10*time.Second)
	if back := bus.NewlyBack(); len(back) != 1 {
		t.Fatalf("拿到数据了没说: %v", back)
	}
	for i := 0; i < 20; i++ {
		bus.HeartbeatAlive("ha", 10*time.Second)
		bus.HeartbeatFailing("ha", 10*time.Second, "又坏了")
		now = now.Add(10 * time.Second)
	}
	if len(bus.NewlyStale()) != 1 {
		t.Fatal("第二次瞎了没被报出来")
	}
}

// **手机端不报到, 而它是最容易死的那一端.**
//
// S42/S43 给采集端做了报到, 但只接在 HA 桥接上. 手机没做 ——
// 而手机恰恰是最容易死的: 没电、被安卓杀后台、进了省电模式、断网.
//
// 后果跟 S42 那次一模一样, 只是换了一端: 手机静默地不再上报,
// 而 OS 那边一个字都没有 —— "家里有没有人"从此停在最后一次位置上,
// 而 S49 的判据正是靠它. 用户看到的是"它不再提醒我了", 查不到根.
//
// **手机的节奏跟家居完全不是一个量级**: 一部手机十分钟传一次是正常的,
// 一个家居桥接十分钟不吭声就是坏了. 所以节奏必须由采集端自己报,
// 而不是 OS 定一个数(S42 那条判据在这儿第二次派上用场).
func TestPhoneAndHomeHaveDifferentPaces(t *testing.T) {
	now := time.Now()
	var told []StaleCollector
	bus := NewSignalBus(nil, func(Digest) {}, SignalOptions{
		Now:     func() time.Time { return now },
		OnStale: func(s []StaleCollector) { told = append(told, s...) },
	})
	bus.Heartbeat("ha", 10*time.Second)       // 家居桥接: 10 秒一次
	bus.Heartbeat("phone.mk", 10*time.Minute) // 手机: 十分钟一次

	now = now.Add(20 * time.Minute) // 对家居是天塌了, 对手机是刚过两轮
	bus.Tick()

	var names []string
	for _, s := range told {
		names = append(names, s.Source)
	}
	if len(names) != 1 || names[0] != "ha" {
		t.Fatalf("判成失联的是 %v —— 手机十分钟传一次是正常的, "+
			"按家居的标准判它会天天误报", names)
	}

	now = now.Add(20 * time.Minute) // 手机也四十分钟没消息了
	told = nil
	bus.Tick()
	if len(told) != 1 || told[0].Source != "phone.mk" {
		t.Fatalf("手机四十分钟没消息了却没被发现: %+v —— "+
			"而'家里有没有人'正停在它最后一次位置上", told)
	}
}
