package osinit

import (
	"fmt"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/sense"
)

// 合成一天 —— **这套东西从来没在日尺度上跑过**.
//
// 到 S11 为止所有验证都是分钟级的: 喂十几条信号看它反应对不对.
// 而感知层真正的失败方式是**慢性的**:
//
//	账本一天涨多少?      它每次启动要整个读一遍, 涨到几百 MB 就起不来了
//	一天产出多少摘要?    每一份都是常驻进程的一次推理
//	哪个源在刷屏?        分钟级的测试里每个源都只有几条, 看不出比例
//
// 这些问题要跑满一天才暴露, 而那时候已经晚了.
//
// **它不是真实数据**: 作息是编的, 传感器抖动幅度是猜的.
// 能排掉的是"量级错了一个数量级"这类问题, 排不掉"阈值差 20%" ——
// 后者只能等真实的一天.
//
// 抽成导出函数是因为测试和 --sense-simday 都要用它:
// 两份"同一个模拟"的实现必然会漂, 而漂了之后测出来的数就不是命令跑的那个.

// DayStats 合成一天的产出
type DayStats struct {
	Fixes   int // 手机定点数
	HAObs   int // 家居观察数(轮询次数 × 实体数)
	Signals int // 进了总线的信号
	Digests int // 产出的摘要 = 常驻进程一天要做的推理次数
}

// SimulateDay 把合成的一天喂进一条真实的总线.
//
// 作息: 在家 8h → 通勤 → 公司 9h → 通勤 → 在家.
// 手机每 5 秒一个定点; 家居每 10 秒轮询 50 个实体(其中 10 个高频功率计).
func SimulateDay(bus *SignalBus, start time.Time, advance func(time.Duration)) DayStats {
	const (
		dayMs      = 24 * 3600 * 1000
		fixEveryMs = 5 * 1000
		haEveryMs  = 10 * 1000
		haEntities = 50
	)
	var st DayStats
	phone := sense.NewPhoneFilter("phone.mk", sense.PhoneConfig{})
	// **时钟要注入**: 不注入的话桥接用真实的 time.Now, 而模拟的时间是
	// 一天前 —— 于是 HA 信号的可知时间比模拟时间快 24 小时,
	// 把水位线推到未来, 所有手机信号全被判迟到.
	// 这不只是合成器的问题, 见 signals.go 的 maxClockSkew.
	simNow := start
	ha := sense.NewHABridge(sense.HAConfig{
		NumericEpsilon: 0.05,
		Now:            func() time.Time { return simNow },
	})
	home, office := 31.88, 31.86

	var pending []abi.Signal
	for ms := int64(0); ms < dayMs; ms += fixEveryMs {
		at := start.UnixMilli() + ms
		hour := (ms / 3600000) % 24

		lat := home
		switch {
		case hour >= 9 && hour < 18:
			lat = office
		case hour == 8 || hour == 18: // 通勤中
			lat = home + (office-home)*float64(ms%3600000)/3600000
		}
		// 静止时的定位抖动 —— 城里 10~50 米是常态, 不带抖动的合成
		// 会让降采样看起来比实际好得多
		jitter := float64((ms/fixEveryMs)%7)*0.00005 - 0.00015
		pending = append(pending, phone.Location(
			sense.Fix{At: at, Lat: lat + jitter, Lon: 117.28, Acc: 15})...)
		st.Fixes++

		if ms%(5*60*1000) == 0 {
			pct := 100 - int(ms/(dayMs/100))
			pending = append(pending, phone.Battery(pct, hour >= 23)...)
		}
		if ms%haEveryMs == 0 {
			states := synthHA(at, int(ms/haEveryMs), haEntities)
			st.HAObs += len(states)
			pending = append(pending, ha.Step(states)...)
		}

		for _, s := range pending {
			bus.Ingest(s)
			st.Signals++
		}
		pending = pending[:0]

		simNow = start.Add(time.Duration(ms+fixEveryMs) * time.Millisecond)
		advance(time.Duration(fixEveryMs) * time.Millisecond)
		if ms%(60*1000) == 0 {
			bus.Tick()
		}
	}
	bus.Tick()
	return st
}

// synthHA 一屋子设备: 10 个高频功率计(每轮抖 0.5%, 纯噪音)
// + 39 个安静的灯 + 1 扇每两小时开一次的门
func synthHA(at int64, round, n int) []sense.HAState {
	ts := time.UnixMilli(at).UTC().Format("2006-01-02T15:04:05Z")
	out := make([]sense.HAState, 0, n)
	for i := 0; i < 10; i++ {
		out = append(out, sense.HAState{
			EntityID:    fmt.Sprintf("sensor.power%d", i),
			State:       fmt.Sprintf("%.2f", 100.0+float64(round%3)*0.5),
			LastChanged: ts,
		})
	}
	for i := 0; i < n-11; i++ {
		out = append(out, sense.HAState{
			EntityID: fmt.Sprintf("light.l%d", i), State: "off",
			LastChanged: "2026-08-15T00:00:00Z",
		})
	}
	// 门每两小时开一次. **last_changed 要贴着变化那一刻** ——
	// 头一版我写的是整点, 于是每条门信号的事件时间都比实际老最多两小时,
	// 报告里表现为"23 条全部迟到". 那是合成数据的锅, 但它顺带挖出了
	// HA 桥接真的漏了可知时间(见 ha.go 的 KnownAt).
	door := "off"
	if round%720 < 3 {
		door = "on"
	}
	out = append(out, sense.HAState{
		EntityID: "binary_sensor.front_door", State: door,
		LastChanged: ts,
	})
	return out
}
