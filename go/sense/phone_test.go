package sense

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

const (
	office = 31.86000 // 用两个相距约 2 公里的点当"公司"和"家"
	home   = 31.88000
	lon0   = 117.28000
	minute = 60 * 1000
)

func fix(at int64, lat float64, acc float64) Fix {
	return Fix{At: at, Lat: lat, Lon: lon0, Acc: acc}
}

func kinds(sigs []abi.Signal) []string {
	out := make([]string, len(sigs))
	for i, s := range sigs {
		out[i] = s.Kind
	}
	return out
}

// **静止的手机不该产出任何信号 —— 哪怕坐标一直在变.**
//
// 城市里静止定位的误差是 10~50 米, 每一个定点的坐标都不一样.
// 按"坐标变了"报的话, 你坐着不动一天也能产出几千条.
func TestSittingStillProducesOneArrivalOnly(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	var got []abi.Signal
	// 半小时, 每 30 秒一个点, 每个点在 30 米内随机抖
	for i := int64(0); i < 60; i++ {
		jitter := float64(i%7) * 0.00005 // ~5 米一档, 最大 ~30 米
		got = append(got, p.Location(fix(i*30*1000, office+jitter, 15))...)
	}
	if len(got) != 1 || got[0].Kind != "place.arrived" {
		t.Fatalf("静止半小时该只产出一条 place.arrived, 实际 %v", kinds(got))
	}
}

// **等红灯、堵车、地铁进站不该变成"到达".**
//
// 没有 dwell 门槛的话, 一趟通勤会产出几十条假的到达/离开.
func TestPassingThroughProducesNothing(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	var got []abi.Signal
	// 一路向北, 每分钟挪 300 米(超出半径), 走 20 分钟
	for i := int64(0); i < 20; i++ {
		got = append(got, p.Location(fix(i*minute, office+float64(i)*0.0027, 15))...)
	}
	if len(got) != 0 {
		t.Fatalf("一路移动却报了 %v —— 通勤路上会产出几十条假到达", kinds(got))
	}
}

// 到达要在**到的时候**报, 离开时才报时长.
//
// 只报离开的话一切都晚一个停留; 只报到达的话永远不知道待了多久.
func TestArrivalAndDepartureAreSeparateSignals(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	var got []abi.Signal
	// 在公司待 40 分钟
	for i := int64(0); i <= 40; i++ {
		got = append(got, p.Location(fix(i*minute, office, 15))...)
	}
	if len(got) != 1 || got[0].Kind != "place.arrived" {
		t.Fatalf("待够之后该报到达: %v", kinds(got))
	}
	if got[0].At != 0 {
		t.Fatalf("到达的事件时间该是进入那一刻(0), 实际 %d", got[0].At)
	}

	// 走到 2 公里外的家
	got = append(got, p.Location(fix(41*minute, home, 15))...)
	if len(got) != 2 || got[1].Kind != "place.left" {
		t.Fatalf("离开该报 place.left: %v", kinds(got))
	}
	if stayed := got[1].Body["stayedMs"].(int64); stayed != 40*minute {
		t.Fatalf("时长算错了: %d, 期望 %d", stayed, 40*minute)
	}
	// 离开这条的事件时间是**走的那一刻**, 不是进入的时刻
	if got[1].At != 40*minute {
		t.Fatalf("离开的事件时间该是最后一次还在原地的时刻, 实际 %d", got[1].At)
	}
}

// **室内/隧道的低精度定点会造出假的进出 —— 这是最隐蔽的一种.**
//
// 进商场 GPS 失锁, 系统拿基站顶上, 精度掉到几百上千米. 那种点
// 看起来完全合法(有坐标有时间), 但跟真实位置差着几公里.
// 后果: 你坐着没动, 却被判成离开又到达, 一天全是假记录.
func TestLowAccuracyFixesCannotBreakAStay(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	var got []abi.Signal
	for i := int64(0); i <= 10; i++ {
		got = append(got, p.Location(fix(i*minute, office, 15))...)
	}
	// 一个飘出 3 公里、精度 1500 米的点
	got = append(got, p.Location(fix(11*minute, office+0.03, 1500))...)
	// 回到真实位置
	for i := int64(12); i <= 20; i++ {
		got = append(got, p.Location(fix(i*minute, office, 15))...)
	}

	for _, s := range got {
		if s.Kind == "place.left" {
			t.Fatal("一个 1500 米精度的飘点打断了停留 —— 一天会造出几十条假进出")
		}
	}
	if p.DroppedFixes() != 1 {
		t.Fatalf("扔掉的定点数该是 1, 实际 %d —— 这个数要能被问出来, "+
			"否则'定位坏了'和'算法在工作'两种情况长得一模一样", p.DroppedFixes())
	}
}

// 电量每 1% 报一次 = 一天一百多条, 而"87% → 86%"不含任何信息
func TestBatteryOnlyReportsLevelCrossings(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	var got []abi.Signal
	p.Battery(100, false) // 建立基线
	for pct := 99; pct >= 1; pct-- {
		got = append(got, p.Battery(pct, false)...)
	}
	// 从 100 掉到 1 跨六档: 50/30/20/10/5, 外加 5% 以下的那一档(level=0).
	//
	// 头一版我写的期望是 5 —— **错在期望, 不在代码**: 掉到 5% 以下
	// 是"快没电了", 那正是最该说一声的时刻, 不报才是漏.
	if len(got) != 6 {
		t.Fatalf("100→1 该报 6 条跨档(含 5%% 以下那档), 实际 %d 条: %v",
			len(got), kinds(got))
	}
	last := got[len(got)-1]
	if last.Body["level"].(int) != 0 {
		t.Fatalf("最后一条该是 5%% 以下那档, 实际 level=%v", last.Body["level"])
	}
}

// 充电时从 20 涨到 100 会跨四档, 但"电充上去了"是一件事不是四件事
func TestChargingDoesNotSpamLevels(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	p.Battery(20, false)
	got := p.Battery(20, true) // 插上电
	if len(got) != 1 || got[0].Kind != "battery.charging" {
		t.Fatalf("插电该报一条 battery.charging: %v", kinds(got))
	}
	var n int
	for pct := 21; pct <= 100; pct++ {
		n += len(p.Battery(pct, true))
	}
	if n != 0 {
		t.Fatalf("充电过程中报了 %d 条跨档 —— 充满是一件事不是四件事", n)
	}
}

// **离散事件一条不许省.**
//
// 降采样只针对连续量. 对来电这类事件做降采样, 等于把最该知道的丢了.
func TestDiscreteEventsAreNeverDownsampled(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	for i := int64(0); i < 5; i++ {
		got := p.Event("call.incoming", i*1000, map[string]any{"from": "138"})
		if len(got) != 1 {
			t.Fatal("来电被降采样掉了")
		}
	}
}

// ── 黄金向量: 给 Kotlin 那边对拍用 ──
//
// 这个仓库已经用同样的办法锁 Go/TS 两份 confine 实现(confine/testdata).
// 安卓端是第三份实现, 同样需要一个"契约的显式声明" ——
// 否则 Kotlin 那边写出来的降采样跟这里差一点, 而**差一点的表现是
// 信号多了几倍或少了几条**, 没有任何地方会报错.
func TestGoldenVectorMatches(t *testing.T) {
	raw, err := os.ReadFile("testdata/phone_trace.json")
	if err != nil {
		t.Skip("黄金向量还没生成")
	}
	var v struct {
		Config PhoneConfig `json:"config"`
		Trace  []Fix       `json:"trace"`
		Expect []struct {
			Kind string `json:"kind"`
			At   int64  `json:"at"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	p := NewPhoneFilter("phone.mk", v.Config)
	var got []abi.Signal
	for _, f := range v.Trace {
		got = append(got, p.Location(f)...)
	}
	if len(got) != len(v.Expect) {
		t.Fatalf("黄金向量对不上: 产出 %d 条(%v), 期望 %d 条",
			len(got), kinds(got), len(v.Expect))
	}
	for i, e := range v.Expect {
		if got[i].Kind != e.Kind || got[i].At != e.At {
			t.Fatalf("第 %d 条对不上: 得到 %s@%d, 期望 %s@%d",
				i, got[i].Kind, got[i].At, e.Kind, e.At)
		}
	}
	t.Logf("%d 个定点 → %d 条信号 (降噪 %.1f%%)",
		len(v.Trace), len(got), 100-float64(len(got))/float64(len(v.Trace))*100)
}

// **到达/离开必须带可知时间, 否则它们永远走不了实时通道.**
//
// place.arrived 的事件时间是"停留开始那一刻",
// 而这件事要到 dwell 之后才判得出来. 生产配置 dwell 5 分钟、
// 总线容忍 2 分钟 → 每一条"你到公司了"都比水位线老 3 分钟, 永远进补传.
func TestStaySignalsCarryKnownAt(t *testing.T) {
	p := NewPhoneFilter("phone.mk", PhoneConfig{})
	var got []abi.Signal
	for i := int64(0); i <= 40; i++ {
		got = append(got, p.Location(fix(i*minute, office, 15))...)
	}
	if len(got) != 1 {
		t.Fatal("没报到达")
	}
	arrived := got[0]
	if arrived.At != 0 {
		t.Fatalf("发生时间该是进入那一刻(0), 实际 %d", arrived.At)
	}
	// 可知时间该是"判出来的那一个定点"的时刻 = dwell 那一刻(第 5 分钟)
	if arrived.KnownAt != 5*minute {
		t.Fatalf("可知时间该是 %d(判出来那一刻), 实际 %d —— "+
			"不填的话每条到达都会被当成历史", 5*minute, arrived.KnownAt)
	}
	if arrived.KnownAt <= arrived.At {
		t.Fatal("可知时间必须晚于发生时间")
	}

	// 离开同理
	got = append(got, p.Location(fix(41*minute, home, 15))...)
	left := got[1]
	if left.KnownAt != 41*minute {
		t.Fatalf("离开的可知时间该是出簇那一刻(%d), 实际 %d", 41*minute, left.KnownAt)
	}
}
