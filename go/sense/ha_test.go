package sense

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func st(entity, state, changed string) HAState {
	return HAState{EntityID: entity, State: state, LastChanged: changed,
		Attributes: map[string]any{"friendly_name": entity}}
}

const (
	t1 = "2026-08-15T19:00:00Z"
	t2 = "2026-08-15T19:00:10Z"
	t3 = "2026-08-15T19:00:20Z"
)

// **头一次轮询不报.**
//
// 第一次看到的 300 个实体全是"新的", 原样报出去就是一场开机风暴,
// 而那 300 条没有一条是"刚刚发生的事" —— 它们只是"现在的状态".
// 事实和事件不是一回事.
func TestFirstPollIsSilent(t *testing.T) {
	b := NewHABridge(HAConfig{})
	got := b.Step([]HAState{
		st("light.living", "on", t1),
		st("lock.front", "locked", t1),
		st("sensor.power", "120.5", t1),
	})
	if len(got) != 0 {
		t.Fatalf("头一次轮询报了 %d 条 —— 那是开机风暴, 不是发生的事", len(got))
	}
}

// 状态没变就不该报. 轮询是电平触发的, 不是边沿计数器
func TestUnchangedProducesNothing(t *testing.T) {
	b := NewHABridge(HAConfig{})
	snap := []HAState{st("light.living", "on", t1)}
	b.Step(snap)
	for i := 0; i < 10; i++ {
		if got := b.Step(snap); len(got) != 0 {
			t.Fatalf("状态没变却报了 %d 条", len(got))
		}
	}
}

// ── 降采样: 这一层挡掉的噪音比其它所有规则加起来都多 ──

// 数值传感器的抖动不是信息.
//
// 功率计每秒读数都不一样, 全报的话一天几万条, 而摘要里
// "power 变了 8000 次"会把那条"门开了"淹掉.
func TestNumericJitterIsFiltered(t *testing.T) {
	b := NewHABridge(HAConfig{NumericEpsilon: 0.05})
	b.Step([]HAState{st("sensor.power", "100.0", t1)})

	// 抖动 1%: 不报
	if got := b.Step([]HAState{st("sensor.power", "101.0", t2)}); len(got) != 0 {
		t.Fatalf("1%% 的抖动被报上去了: %+v", got)
	}
	// 真变化 30%: 报
	got := b.Step([]HAState{st("sensor.power", "131.0", t3)})
	if len(got) != 1 || got[0].Kind != "sensor.changed" {
		t.Fatalf("真实的数值变化没报上去: %+v", got)
	}
}

// 零附近不能用相对变化判 —— 0 → 0.1 的相对变化是无穷大.
// 而"设备刚开始耗电"是真事件, 既不能当噪音丢掉, 也不能放大成风暴
func TestNumericNearZero(t *testing.T) {
	b := NewHABridge(HAConfig{NumericEpsilon: 0.05})
	b.Step([]HAState{st("sensor.power", "0", t1)})
	if got := b.Step([]HAState{st("sensor.power", "0", t2)}); len(got) != 0 {
		t.Fatal("0 → 0 报了")
	}
	if got := b.Step([]HAState{st("sensor.power", "0.5", t3)}); len(got) != 1 {
		t.Fatal("0 → 0.5 没报 —— 设备开始耗电是真事件")
	}
}

// 属性变了但状态没变, 不报.
// 亮度从 254 抖到 255、播放进度条 —— 这类一天几万条
func TestAttributeOnlyChangeIsIgnored(t *testing.T) {
	b := NewHABridge(HAConfig{})
	a := HAState{EntityID: "light.living", State: "on", LastChanged: t1,
		Attributes: map[string]any{"brightness": 254}}
	c := HAState{EntityID: "light.living", State: "on", LastChanged: t1,
		Attributes: map[string]any{"brightness": 255}}
	b.Step([]HAState{a})
	if got := b.Step([]HAState{c}); len(got) != 0 {
		t.Fatalf("只有属性变了却报了: %+v", got)
	}
}

// **HA 重启风暴 —— 最常见的一种假信号.**
//
// HA 重启/集成断线时全部实体一起变 unavailable, 恢复时再一起变回来.
// 两场 300 条的风暴, 而没有一条对应真实世界的变化: 灯没动,
// 只是我们看不见了. 而且它最像真的 —— 时间戳、实体、状态转换全都合法.
func TestRestartStormIsSuppressed(t *testing.T) {
	b := NewHABridge(HAConfig{})
	var snap []HAState
	for i := 0; i < 50; i++ {
		snap = append(snap, st(fmt.Sprintf("light.l%d", i), "on", t1))
	}
	b.Step(snap)

	// HA 掉线: 全变 unavailable
	var down []HAState
	for i := 0; i < 50; i++ {
		down = append(down, st(fmt.Sprintf("light.l%d", i), "unavailable", t2))
	}
	if got := b.Step(down); len(got) != 0 {
		t.Fatalf("掉线风暴报了 %d 条 —— 那不是世界变了, 是我们看不见了", len(got))
	}
	// 恢复: 全变回 on
	var up []HAState
	for i := 0; i < 50; i++ {
		up = append(up, st(fmt.Sprintf("light.l%d", i), "on", t3))
	}
	if got := b.Step(up); len(got) != 0 {
		t.Fatalf("恢复风暴报了 %d 条", len(got))
	}
}

// ── 语义翻译: 谁认识这个世界谁来翻 ──

// 门锁开了要翻成 lock.opened, 因为 OS 的紧急策略表是按 kind 匹配的.
//
// 全都报成 device.state 的话, 策略表就得在 OS 里再解析一次 entity_id ——
// 那等于把 HA 的领域知识搬进内核, 换一种家居协议就要改内核.
func TestSemanticKindsMapToPolicyTable(t *testing.T) {
	cases := []struct {
		entity, from, to, want string
	}{
		{"lock.front_door", "locked", "unlocked", "lock.opened"},
		{"lock.front_door", "unlocked", "locked", "lock.closed"},
		{"binary_sensor.front_door", "off", "on", "door.opened"},
		{"binary_sensor.hall_motion", "off", "on", "presence.motion"},
		{"person.mk", "not_home", "home", "presence.changed"},
		{"device_tracker.phone", "home", "not_home", "presence.changed"},
		{"alarm_control_panel.home", "armed_away", "triggered", "alarm.fired"},
		{"light.living", "off", "on", "device.state"},
	}
	for _, c := range cases {
		b := NewHABridge(HAConfig{})
		b.Step([]HAState{st(c.entity, c.from, t1)})
		got := b.Step([]HAState{st(c.entity, c.to, t2)})
		if len(got) != 1 {
			t.Fatalf("%s %s→%s 没报", c.entity, c.from, c.to)
		}
		if got[0].Kind != c.want {
			t.Fatalf("%s %s→%s 翻成了 %q, 期望 %q",
				c.entity, c.from, c.to, got[0].Kind, c.want)
		}
	}
}

// **事件时间用 HA 的 last_changed, 不是轮询时刻.**
//
// 灯是 19:00:03 开的, 我们 19:00:09 才轮询到. 用轮询时刻的话整条
// 时间线会系统性地晚 0~一个间隔 —— 而"几点开的灯"正是这类信号唯一的价值.
func TestEventTimeComesFromHANotPollTime(t *testing.T) {
	b := NewHABridge(HAConfig{})
	b.Step([]HAState{st("light.living", "off", t1)})
	got := b.Step([]HAState{st("light.living", "on", "2026-08-15T19:00:03Z")})
	if len(got) != 1 {
		t.Fatal("没报")
	}
	want := parseHATime("2026-08-15T19:00:03Z")
	if got[0].At != want {
		t.Fatalf("事件时间用了轮询时刻(%d), 应该是 HA 的 last_changed(%d)", got[0].At, want)
	}
}

// 幂等键含变化时刻 —— 轮询天生会重复看到同一个状态.
// 两侧各有一道去重(这边按变化时刻, 总线那边按 ID), 判据不同, 互相兜底
func TestIDIsStableAcrossPolls(t *testing.T) {
	b1, b2 := NewHABridge(HAConfig{}), NewHABridge(HAConfig{})
	for _, b := range []*HABridge{b1, b2} {
		b.Step([]HAState{st("light.living", "off", t1)})
	}
	a := b1.Step([]HAState{st("light.living", "on", t2)})
	c := b2.Step([]HAState{st("light.living", "on", t2)})
	if len(a) != 1 || len(c) != 1 || a[0].ID != c[0].ID {
		t.Fatalf("同一次状态变化产出了不同的幂等键: %q vs %q", a[0].ID, c[0].ID)
	}
}

// 同一次快照必须产出同样顺序的信号 —— 摘要最终要进上下文,
// 而那是前缀缓存的载体
func TestOutputOrderIsDeterministic(t *testing.T) {
	build := func() string {
		b := NewHABridge(HAConfig{})
		var before, after []HAState
		for _, e := range []string{"light.z", "light.a", "lock.m", "sensor.b"} {
			before = append(before, st(e, "off", t1))
			after = append(after, st(e, "on", t2))
		}
		b.Step(before)
		var s string
		for _, sig := range b.Step(after) {
			s += sig.ID + ";"
		}
		return s
	}
	first := build()
	for i := 0; i < 20; i++ {
		if build() != first {
			t.Fatal("同一次快照产出了不同顺序 —— 前缀缓存会静默失效")
		}
	}
}

// 新实体上线要报, 但报成"上线"而不是"变化"
func TestNewEntityReportedAsAdded(t *testing.T) {
	b := NewHABridge(HAConfig{})
	b.Step([]HAState{st("light.living", "on", t1)})
	got := b.Step([]HAState{st("light.living", "on", t1), st("light.new", "on", t2)})
	if len(got) != 1 || got[0].Kind != "device.added" {
		t.Fatalf("新实体没报成 device.added: %+v", got)
	}
}

// 真实规模下的降噪比 —— 这是这一层存在的理由, 值得直接量出来.
//
// ── 这条测试必须同时钉住两头 ──
//
// 只断言"信号少"是个**坏闸**: 一个把什么都丢掉的实现照样能过.
// 头一版我就是那么写的, 跑出来 "降噪 100%%" —— 那个数字好看得不对劲,
// 因为它同时也是"这个采集器完全不工作"的读数.
//
// 所以噪音和真事件要一起造, 分别断言: 噪音必须被挡掉, **真事件必须一条不少**.
func TestNoiseReductionAtRealisticScale(t *testing.T) {
	b := NewHABridge(HAConfig{NumericEpsilon: 0.05})
	const rounds = 100
	// 真事件: 每 20 轮开一次门. 这些**一条都不许丢**
	doorOpensAt := map[int]bool{20: true, 40: true, 60: true, 80: true, 100: true}

	mk := func(round int) []HAState {
		var out []HAState
		// 10 个高频数值传感器: 每轮抖 0.5%, 全是噪音
		for i := 0; i < 10; i++ {
			out = append(out, st(fmt.Sprintf("sensor.power%d", i),
				fmt.Sprintf("%.2f", 100.0+float64(round%3)*0.5),
				fmt.Sprintf("2026-08-15T19:%02d:00Z", round%60)))
		}
		// 40 个安静的设备
		for i := 0; i < 40; i++ {
			out = append(out, st(fmt.Sprintf("light.l%d", i), "off", t1))
		}
		// 一扇门: 平时关着, 到点开一下
		state, ts := "off", t1
		if doorOpensAt[round] {
			state, ts = "on", fmt.Sprintf("2026-08-15T20:%02d:00Z", round%60)
		}
		out = append(out, st("binary_sensor.front_door", state, ts))
		return out
	}

	b.Step(mk(0))
	total, doors := 0, 0
	for r := 1; r <= rounds; r++ {
		for _, sig := range b.Step(mk(r)) {
			total++
			if sig.Kind == "door.opened" {
				doors++
			}
		}
	}
	observations := rounds * 51

	// 一头: 噪音要被挡住
	if total > 30 {
		t.Fatalf("%d 轮轮询产出了 %d 条信号 —— 降采样没起作用, 账本会爆", rounds, total)
	}
	// 另一头: 真事件一条都不许丢. **没有这一半, 上面那个断言毫无意义**
	if doors != len(doorOpensAt) {
		t.Fatalf("开门事件丢了: 发生 %d 次, 只报上来 %d 次 —— "+
			"降噪把真事件也降掉了, 那比不降噪更糟", len(doorOpensAt), doors)
	}
	t.Logf("%d 次观察 → %d 条信号 (降噪 %.1f%%), 其中 %d 条是真实的开门",
		observations, total, 100-float64(total)/float64(observations)*100, doors)
}

// **可知时间是轮询到的那一刻, 不是 last_changed.**
//
// 日尺度合成跑出来的: 一天下来门的信号 23 条**全部判迟到**, 全走历史通道.
// 轮询源必须同样区分事件时间和可知时间 ——
// HA 用 last_changed 当事件时间是对的(灯确实是 19:00:03 开的),
// 但我们是轮询才发现的, 两者差一个轮询周期.
func TestHASignalsCarryKnownAt(t *testing.T) {
	now := time.Date(2026, 8, 15, 19, 0, 30, 0, time.UTC)
	b := NewHABridge(HAConfig{Now: func() time.Time { return now }})
	b.Step([]HAState{st("light.living", "off", t1)})
	got := b.Step([]HAState{st("light.living", "on", "2026-08-15T19:00:03Z")})
	if len(got) != 1 {
		t.Fatal("没报")
	}
	if got[0].At != parseHATime("2026-08-15T19:00:03Z") {
		t.Fatal("发生时间该是 HA 的 last_changed")
	}
	if got[0].KnownAt != now.UnixMilli() {
		t.Fatalf("可知时间该是轮询这一刻(%d), 实际 %d —— "+
			"不填的话家居信号全会被判成历史", now.UnixMilli(), got[0].KnownAt)
	}
	if got[0].KnownAt <= got[0].At {
		t.Fatal("可知时间必须晚于发生时间")
	}
}

// **状态是时间戳的实体, 报的是"计划"不是"发生".**
//
// 真实 HA 数据给出的第一个校准结论: 一台几乎没接设备的 HA, 11 天
// 产出 97 条信号, 其中 **60 条**是 sensor.sun_next_* 这类"下次日出在几点".
// 每天重算一次, 于是每天报 6 条 —— 而这件事没有任何人需要被告知.
func TestTimestampValuedSensorsAreNotEvents(t *testing.T) {
	b := NewHABridge(HAConfig{})
	b.Step([]HAState{
		st("sensor.sun_next_dawn", "2026-01-01T07:12:00+00:00", t1),
		st("sun.sun", "below_horizon", t1),
	})
	got := b.Step([]HAState{
		st("sensor.sun_next_dawn", "2026-01-02T07:11:30+00:00", t2), // 重算了
		st("sun.sun", "above_horizon", t2),                          // 真的日出了
	})
	if len(got) != 1 {
		t.Fatalf("该只报 1 条(真的日出), 实际 %d 条: %v", len(got), kindsOf(got))
	}
	if got[0].Body["entity"] != "sun.sun" {
		t.Fatalf("报错了实体: %v —— '下次日出时间变了'不是发生的事", got[0].Body)
	}
}

// 判据是**状态的形状**不是实体名 —— 黑名单永远不全, 换套集成就要重写
func TestTimestampDetectionIsByShapeNotName(t *testing.T) {
	for _, s := range []string{
		"2026-01-01T07:12:00+00:00", "2026-01-01T07:12:00Z", "2026-01-01T07:12:00",
	} {
		if !isTimestampState(s) {
			t.Fatalf("没认出时间戳: %q", s)
		}
	}
	for _, s := range []string{"on", "off", "below_horizon", "23.5", "", "unavailable"} {
		if isTimestampState(s) {
			t.Fatalf("把 %q 当成时间戳了 —— 真状态会被滤掉", s)
		}
	}
}

func kindsOf(sigs []abi.Signal) []string {
	out := make([]string, len(sigs))
	for i, s := range sigs {
		out[i] = s.Kind + ":" + fmt.Sprint(s.Body["entity"])
	}
	return out
}

// **来源要精确到实体, 否则静音表下不了刀.**
//
// sensor.time 每分钟跳一次,
// 一天 1440 条, 永远不会导致任何通知 —— 是纯噪音的活样本.
// 而它跟一个真正的温度传感器共用 `ha.sensor/device.state`:
// 想让钟闭嘴, 就得把**所有** sensor 一起静掉, 连温度也不报了.
//
// 校准报告也一样: 按来源分组时看到的是"ha.sensor 一天 2000 条",
// 而真正该知道的是"是那个钟在吵".
func TestSourceIsPerEntityNotPerDomain(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	// 用两个真实体 —— sensor.time 现在被当成钟压掉了(见 isClockState),
	// 拿它当样本这条测试就变成了在测别的东西
	b.Step([]HAState{
		{EntityID: "sensor.washer_status", State: "idle", LastChanged: "2026-08-16T06:49:00+00:00"},
		{EntityID: "sensor.living_room_temp", State: "22.0", LastChanged: "2026-08-16T06:49:00+00:00"},
	})
	out := b.Step([]HAState{
		{EntityID: "sensor.washer_status", State: "running", LastChanged: "2026-08-16T06:50:00+00:00"},
		{EntityID: "sensor.living_room_temp", State: "26.0", LastChanged: "2026-08-16T06:50:00+00:00"},
	})
	if len(out) != 2 {
		t.Fatalf("产出 %d 条, 期望 2 条", len(out))
	}
	if out[0].Source == out[1].Source {
		t.Fatalf("两个不同实体的来源都是 %q —— 想让钟闭嘴就得把所有 sensor 一起静掉",
			out[0].Source)
	}
	var washer string
	for _, s := range out {
		if s.Body["entity"] == "sensor.washer_status" {
			washer = s.Source
		}
	}
	if !strings.Contains(washer, "washer") {
		t.Fatalf("洗衣机的来源是 %q —— 看不出是哪个实体在报", washer)
	}
}

// 来源变细了, 但**幂等键不能跟着变**: 它是"实体+变化时刻",
// 变了的话采集端重传会被当成新的
func TestIDStaysStableWhenSourceGetsFiner(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	st := []HAState{{EntityID: "binary_sensor.door", State: "on",
		LastChanged: "2026-08-16T06:49:00+00:00"}}
	b.Step(st)
	out := b.Step([]HAState{{EntityID: "binary_sensor.door", State: "off",
		LastChanged: "2026-08-16T06:50:00+00:00"}})
	if len(out) != 1 {
		t.Fatalf("产出 %d 条", len(out))
	}
	if !strings.HasPrefix(out[0].ID, "ha|binary_sensor.door|") {
		t.Fatalf("幂等键变了: %q —— 采集端重传会被当成新的", out[0].ID)
	}
}

// **钟不是事件源.**
//
// 十五分钟里 14 条信号, **13 条是 sensor.time**.
// 一天 1440 条, 每一条都会开一个窗口、叫醒一次主动进程 ——
// 而它报的不是"发生了什么", 它就是时钟本身.
//
// 这跟 sun_next_* 那条(时间戳是计划不是事件)是同一类, 只是形状不同:
// 那边是 ISO 时间戳, 这边是 "21:55" 和 "2026-08-16".
// 时钟信号必须显式识别, 否则常规数据集很容易遗漏这类噪音.
func TestClockIsNotAnEventSource(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	prime := []HAState{
		{EntityID: "sensor.time", State: "21:55", LastChanged: "2026-08-16T13:55:00+00:00"},
		{EntityID: "sensor.date", State: "2026-08-16", LastChanged: "2026-08-16T00:00:00+00:00"},
		{EntityID: "sensor.next_alarm", State: "07:30", LastChanged: "2026-08-16T13:00:00+00:00"},
	}
	b.Step(prime)
	out := b.Step([]HAState{
		{EntityID: "sensor.time", State: "21:56", LastChanged: "2026-08-16T13:56:00+00:00"},
		{EntityID: "sensor.date", State: "2026-08-17", LastChanged: "2026-08-17T00:00:00+00:00"},
		{EntityID: "sensor.next_alarm", State: "08:00", LastChanged: "2026-08-16T14:00:00+00:00"},
	})
	if len(out) != 0 {
		var got []string
		for _, s := range out {
			got = append(got, s.Body["entity"].(string)+"="+s.Body["state"].(string))
		}
		t.Fatalf("钟走了一格就报了 %d 条: %v —— 一天 1440 次唤醒, 全是钟",
			len(out), got)
	}
}

// **别把真事件误当成钟.**
//
// 判据是形状, 而形状会误伤: 一个真的温度传感器不会长成 "21:55",
// 但一个报"12:30"的**比赛比分**会. 所以只认时刻和日期这两种整形状,
// 不认任何带别的字符的东西
func TestRealEventsAreNotMistakenForClocks(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	prime := []HAState{
		{EntityID: "sensor.washer", State: "idle", LastChanged: "2026-08-16T13:00:00+00:00"},
		{EntityID: "sensor.score", State: "12:30 加时", LastChanged: "2026-08-16T13:00:00+00:00"},
	}
	b.Step(prime)
	out := b.Step([]HAState{
		{EntityID: "sensor.washer", State: "running", LastChanged: "2026-08-16T13:05:00+00:00"},
		{EntityID: "sensor.score", State: "13:30 加时", LastChanged: "2026-08-16T13:05:00+00:00"},
	})
	if len(out) != 2 {
		t.Fatalf("真事件被当成钟压掉了, 只剩 %d 条", len(out))
	}
}

// **上线风暴 —— 掉线风暴的另一半, 一直漏着.**
//
// 108 个实体在四分钟里产生 100 条信号,
// **89 条是 device.added**. 因为 HA 启动时实体是**陆续**出现的:
// 第一次轮询看到 12 个(那批被 primed 挡掉了), 后面几轮又冒出 96 个 ——
// 每一个都被报成"有个新设备上线了".
//
// 而用户根本不关心"你的 108 个设备上线了". 这跟仓库里已经有的
// 掉线风暴压制(HA 重启时一批实体变 unavailable)是**同一件事的两半**,
// 只做了一半:
//
//	掉线风暴  一批变 unavailable  = 我们看不见了, 不是它们坏了
//	上线风暴  一批冒出来          = 我们刚看见, 不是世界多了 89 个东西
func TestOnlineStormIsSuppressed(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	b.Step([]HAState{st("sensor.a", "1", "2026-08-16T06:00:00+00:00")}) // primed

	// HA 起来了, 一批实体同时冒出来
	var batch []HAState
	for i := 0; i < 30; i++ {
		batch = append(batch, st(fmt.Sprintf("light.demo_%d", i), "off",
			"2026-08-16T06:01:00+00:00"))
	}
	batch = append(batch, st("sensor.a", "1", "2026-08-16T06:00:00+00:00"))
	out := b.Step(batch)

	added := 0
	for _, s := range out {
		if s.Kind == "device.added" {
			added++
		}
	}
	if added > 1 {
		t.Fatalf("一批 30 个实体同时上线报了 %d 条 —— 那是一场风暴, "+
			"而用户不关心'你的设备上线了'", added)
	}
}

// **真的多了一个设备要照报** —— 那是他刚装上的东西, 他等着看它出现
func TestASingleNewDeviceIsStillReported(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	b.Step([]HAState{st("sensor.a", "1", "2026-08-16T06:00:00+00:00")})
	out := b.Step([]HAState{
		st("sensor.a", "1", "2026-08-16T06:00:00+00:00"),
		st("light.new_lamp", "off", "2026-08-16T06:01:00+00:00"),
	})
	if len(out) != 1 || out[0].Kind != "device.added" {
		t.Fatalf("刚装上的一个新设备没报出来: %+v", out)
	}
}

// **百分比量用相对阈值是错的.**
//
// 五分钟里 16 条信号中 **12 条是
// sensor.system_monitor_processor_use** —— CPU 使用率.
//
// 因为相对阈值对它完全不设防: CPU 从 2% 跳到 5% 是 **150%** 的相对变化,
// 远超 5% 的门槛, 而实际只差三个点, 没有任何人需要被告知.
// 折合一天 3456 条, 全来自一个传感器.
//
// 相对阈值本身是对的("一个功率计的 5 瓦和一个温度计的 5 度不是一回事"),
// 它只是不适合**量程已知**的量: 百分比天然是 0–100, 差几个点就是差几个点.
func TestPercentSensorsUseAbsoluteThreshold(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	n := 0
	pct := func(v string) HAState {
		n++
		return HAState{EntityID: "sensor.cpu", State: v,
			LastChanged: fmt.Sprintf("2026-08-16T06:%02d:00+00:00", n),
			Attributes:  map[string]any{"unit_of_measurement": "%"}}
	}
	b.Step([]HAState{pct("2")})
	if out := b.Step([]HAState{pct("5")}); len(out) != 0 {
		t.Fatalf("CPU 2%%→5%% 报了 %d 条 —— 相对变化 150%%, 实际只差三个点, "+
			"一天 3456 条全来自这一个传感器", len(out))
	}
	// 真的跳起来了还是要报 —— 那是"机器忙起来了"
	if out := b.Step([]HAState{pct("90")}); len(out) == 0 {
		t.Fatal("CPU 5%→90% 都不报了 —— 那是真的忙起来了")
	}
}

// **没有单位的量还是走相对阈值** —— 能耗、流量这类没有量程,
// 给它配绝对阈值就又变回了 IFTTT 的配置地狱
func TestUnitlessSensorsStayRelative(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	kwh := func(v string) HAState {
		return HAState{EntityID: "sensor.energy", State: v,
			LastChanged: "2026-08-16T06:0" + v[:1] + ":00+00:00",
			Attributes:  map[string]any{"unit_of_measurement": "kWh"}}
	}
	b.Step([]HAState{kwh("1")})
	if out := b.Step([]HAState{kwh("9")}); len(out) == 0 {
		t.Fatal("1kWh→9kWh 不报了 —— 那是真的开始耗电了")
	}
}

// **过程不是结果.**
//
// 门锁的状态机是
//
//	locked → unlocking → unlocked → locking → locked
//
// 而 semanticKind 里 `if cur == "unlocked" {opened} else {closed}` 把
// **unlocking 和 locking 都兜成了"锁上了"**.
//
// 后果不是多一条噪音, 是**报了一件没发生的事**: locking 是"正在锁",
// 锁可能卡住、可能最终没锁上 —— 而系统已经告诉用户"锁上了".
// 对着一件不可逆的事报错误的结论, 比不报更糟.
//
// 对照日跑出来的账本里: **6 条 lock.closed, 0 条 lock.opened**.
func TestTransitionalStatesAreNotFacts(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	b.Step([]HAState{st("lock.front", "locked", "2026-08-16T06:00:00+00:00")})

	for _, c := range []struct{ state, want string }{
		{"unlocking", ""},           // 正在开 —— 还没开
		{"unlocked", "lock.opened"}, // 开了
		{"locking", ""},             // 正在锁 —— 还没锁上
		{"locked", "lock.closed"},   // 锁上了
	} {
		out := b.Step([]HAState{st("lock.front", c.state,
			"2026-08-16T06:0"+string(rune('1'+len(c.state)%9))+":00+00:00")})
		got := ""
		if len(out) > 0 {
			got = out[0].Kind
		}
		if got != c.want {
			t.Errorf("锁处于 %q 时报成了 %q, 期望 %q", c.state, got, c.want)
		}
	}
}

// 别的域也一样: 窗帘/车库门的 opening/closing 同样是过程
func TestCoverTransitionsAreNotFacts(t *testing.T) {
	b := NewHABridge(HAConfig{Now: func() time.Time { return time.Now() }})
	b.Step([]HAState{st("cover.garage", "closed", "2026-08-16T06:00:00+00:00")})
	out := b.Step([]HAState{st("cover.garage", "opening", "2026-08-16T06:01:00+00:00")})
	if len(out) != 0 {
		t.Fatalf("车库门'正在开'就报了 %+v —— 它可能卡在半路", out)
	}
}

// **"以 -ing 结尾"这个形状判是错的 —— 钉住反例.**
//
// 第一版我按形状判(那几个过渡态长得很整齐: unlocking/locking/opening/
// closing), 而仓库里现有的两条测试当场把它打回来了:
// 洗衣机的 running、扫地机的 cleaning 同样以 -ing 结尾, 而它们是**终态** ——
// 洗衣机正在洗就是它现在的样子, 不是"正在变成某个状态".
//
// 误伤一个真事件比多报一条噪音糟得多: 压掉它, 用户永远不知道有过这件事.
func TestRunningIsAStateNotATransition(t *testing.T) {
	for _, steady := range []string{"running", "cleaning", "charging", "heating"} {
		if isTransitionalState(steady) {
			t.Errorf("%q 被当成了过渡态 —— 它是终态, 压掉它用户就永远不知道", steady)
		}
	}
	for _, moving := range []string{"unlocking", "locking", "opening", "closing"} {
		if !isTransitionalState(moving) {
			t.Errorf("%q 没被认出是过渡态 —— 它是'正在做', 不是'做完了'", moving)
		}
	}
}
