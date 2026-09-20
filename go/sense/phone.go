package sense

import (
	"fmt"
	"math"
	"sort"

	"github.com/neox-os/neox-os/abi"
)

// 手机端降采样.
//
// ── 为什么这个算法必须跑在手机上, 而不是 OS 里 ──
//
// 一部手机开着定位, 原始定点是**每几秒一个**. 一天就是两万多个点,
// 而事件日志是 append-only 且要落盘的:
//
//	原始点直接进总线  →  账本一天涨几十 MB, 而且每次启动要整个读一遍
//	→ 几天之后开机要读几百 MB
//
// 更要命的是信噪比: 你在公司坐了四小时, 那是**一件事**("在公司"),
// 不是 2400 件事. 把 2400 个点送上去, 模型看到的是噪音的海.
//
// **所以上传的应该是「19:00–19:40 在公司」, 不是 2400 个 GPS 点.**
//
// ── 这个包为什么用 Go 写 ──
//
// 真正跑这段逻辑的是安卓上的 Kotlin. 这里的 Go 实现有三个用途,
// 每一个都不是"重复劳动":
//
//	① 可执行的规格. 语义写成能跑的代码, 而不是一段文档
//	② 黄金向量. 同一条轨迹的输入输出落成样本, Kotlin 那边照它对拍 ——
//	   这个仓库已经在用同样的办法锁 Go/TS 两份实现(见 confine/testdata)
//	③ 模拟采集端. 回放一条轨迹就能验整条链路, 不用等 APP 写完

// Fix 一次定位.
type Fix struct {
	At  int64   // 事件时间, 毫秒
	Lat float64 // 纬度
	Lon float64 // 经度
	// Acc 水平精度, 米. **这个字段决定了一半的正确性**, 见 PhoneConfig.MaxAccuracy
	Acc float64
}

type PhoneConfig struct {
	// StayRadius 多近算"还在原地", 米. 缺省 120.
	//
	// 不能太小: 静止的手机定位每次都不一样(城市里误差 10~50 米是常态),
	// 半径设成 20 米的话, 你坐着不动也会被判成一直在"离开-到达".
	StayRadius float64
	// StayDwell 待多久才算"停留", 毫秒. 缺省 5 分钟.
	//
	// 没有它的话, 等红灯、堵车、地铁进站都会变成一次"到达" ——
	// 一天几十条假的"你到了某地".
	StayDwell int64
	// MaxAccuracy 精度差过这个值的定点**直接扔掉**, 米. 缺省 200.
	//
	// ── 这一条是室内和隧道的解药 ──
	//
	// 进了商场或隧道, GPS 失锁, 系统会拿基站粗定位顶上, 精度掉到
	// 几百米甚至几公里. 那种点跟真实位置差得远, 而它**看起来完全合法**:
	// 有坐标、有时间戳.
	//
	// 后果很具体: 你在公司坐着没动, 一个 1500 米精度的点飘出去,
	// 于是判成"离开公司" → 下一个点飘回来 → "到达公司".
	// 一天下来全是假的进出记录, 而真实世界里你一步没挪.
	MaxAccuracy float64
	// BatteryLevels 电量到哪几档才报. 缺省 100/50/30/20/10/5.
	//
	// 每 1% 报一次 = 一天一百多条, 而"从 87% 变成 86%"不含任何信息.
	BatteryLevels []int
}

func (c *PhoneConfig) fill() {
	if c.StayRadius <= 0 {
		c.StayRadius = 120
	}
	if c.StayDwell <= 0 {
		c.StayDwell = 5 * 60 * 1000
	}
	if c.MaxAccuracy <= 0 {
		c.MaxAccuracy = 200
	}
	if c.BatteryLevels == nil {
		c.BatteryLevels = []int{100, 50, 30, 20, 10, 5}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(c.BatteryLevels)))
}

// PhoneFilter 手机端的状态机. 一部手机一个.
type PhoneFilter struct {
	cfg    PhoneConfig
	source string

	// ── 停留检测 ──
	//
	// 在线算法, 不能回看历史: 手机上没有那个内存, 而且要**边走边报**.
	anchor  *Fix  // 当前这簇的锚点
	since   int64 // 进入这一簇的时刻
	arrived bool  // "到达"报过了没有
	lastAt  int64 // 这一簇里最后一个点的时刻

	// 电量: 上次报的是哪一档
	lastLevel  int
	charging   bool
	haveCharge bool
	dropped    int // 因为精度太差扔掉的定点数
}

func NewPhoneFilter(source string, cfg PhoneConfig) *PhoneFilter {
	cfg.fill()
	return &PhoneFilter{cfg: cfg, source: source, lastLevel: -1}
}

// DroppedFixes 因为精度太差被扔掉的定点数 —— 这个数要能被问出来.
//
// 一部手机如果 90% 的定点都被扔掉, 那不是算法在工作, 是这台设备的
// 定位坏了/权限被限制了. 两种情况的表现一模一样(信号很少),
// 只有这个计数能把它们分开.
func (p *PhoneFilter) DroppedFixes() int { return p.dropped }

// Location 喂一个定点, 吐出值得上报的信号(通常是零条).
//
// ── 为什么"到达"和"离开"要分开报 ──
//
// 一段停留只有在**结束时**才能完整地知道(待了多久). 但那时候再说
// "你刚才在公司待了 40 分钟"已经晚了 —— 主动智能要的是"你到公司了"
// 这件事在**你到的时候**就知道.
//
// 所以分成两条:
//
//	place.arrived  待够 dwell 就报, 此刻还不知道会待多久
//	place.left     离开时报, 带上完整的时长
//
// 只报后者的话, 一切都晚一个停留; 只报前者的话, 永远不知道待了多久.
func (p *PhoneFilter) Location(f Fix) []abi.Signal {
	// 精度闸放最前面: 一个飘了 1500 米的点不该有资格打断一段停留
	if f.Acc > p.cfg.MaxAccuracy {
		p.dropped++
		return nil
	}

	if p.anchor == nil {
		p.anchor, p.since, p.lastAt, p.arrived = &f, f.At, f.At, false
		return nil
	}

	if metersBetween(*p.anchor, f) <= p.cfg.StayRadius {
		p.lastAt = f.At
		if !p.arrived && f.At-p.since >= p.cfg.StayDwell {
			p.arrived = true
			return []abi.Signal{{
				ID:     fmt.Sprintf("%s|arrived|%d", p.source, p.since),
				Source: p.source, Kind: "place.arrived", At: p.since,
				// **可知时间是现在, 不是 since** —— 这一条是真机逼出来的.
				//
				// "你到了"这件事要到停留够久(dwell)才判得出来. 不填它的话,
				// 这条信号比水位线老一整个 dwell(生产配置 5 分钟, 而容忍度
				// 只有 2 分钟), 于是**每一条"你到公司了"都被判成历史**,
				// 主动进程收到的是"这些已经发生过了, 不需要现在处理" ——
				// 而这恰恰是最该实时说的一类.
				KnownAt: f.At,
				Body: map[string]any{
					"lat": round5(p.anchor.Lat), "lon": round5(p.anchor.Lon),
					"acc": p.anchor.Acc,
				},
			}}
		}
		return nil
	}

	// 出簇了
	var out []abi.Signal
	if p.arrived {
		out = append(out, abi.Signal{
			ID:     fmt.Sprintf("%s|left|%d", p.source, p.since),
			Source: p.source, Kind: "place.left",
			// 同理: "他走了"是这一个定点出簇时才判得出来的
			KnownAt: f.At,
			// **事件时间用离开的那一刻**, 不是进入的时刻:
			// 这条信号说的是"他走了", 那件事发生在现在
			At: p.lastAt,
			Body: map[string]any{
				"lat": round5(p.anchor.Lat), "lon": round5(p.anchor.Lon),
				"stayedMs": p.lastAt - p.since,
			},
		})
	}
	// **没待够就出簇的, 一个字都不报.**
	//
	// 这是降噪的大头: 走路、开车、坐地铁的路上会不停地出簇进簇,
	// 每一次都报的话, 一趟通勤就是几十条"到达/离开".
	p.anchor, p.since, p.lastAt, p.arrived = &f, f.At, f.At, false
	return out
}

// Battery 电量变化. 只有跨档和充电状态变化才报.
func (p *PhoneFilter) Battery(pct int, charging bool) []abi.Signal {
	var out []abi.Signal
	lvl := levelOf(pct, p.cfg.BatteryLevels)

	if p.haveCharge && charging != p.charging {
		out = append(out, abi.Signal{
			ID:     fmt.Sprintf("%s|charge|%v|%d", p.source, charging, pct),
			Source: p.source, Kind: "battery.charging",
			Body: map[string]any{"charging": charging, "pct": pct},
		})
	}
	p.charging, p.haveCharge = charging, true

	// 只在**往下掉**跨档时报: 充电时从 20 一路涨到 100 会跨四档,
	// 而"电充上去了"是一件事不是四件事
	if lvl != p.lastLevel && (p.lastLevel == -1 || lvl < p.lastLevel) && !charging {
		p.lastLevel = lvl
		out = append(out, abi.Signal{
			ID:     fmt.Sprintf("%s|battery|%d", p.source, lvl),
			Source: p.source, Kind: "battery.level",
			Body: map[string]any{"level": lvl, "pct": pct},
		})
		return out
	}
	if lvl > p.lastLevel {
		p.lastLevel = lvl // 充上电了, 重新武装下一次下跌
	}
	return out
}

// Event 手机上的离散事件(来电/未接/闹钟).
//
// 这类**一条不许省**: 它们本来就是稀疏的, 而且正是最该知道的那些.
// 降采样只针对连续量, 对离散事件做降采样等于把要报的东西丢了.
func (p *PhoneFilter) Event(kind string, at int64, body map[string]any) []abi.Signal {
	return []abi.Signal{{
		ID:     fmt.Sprintf("%s|%s|%d", p.source, kind, at),
		Source: p.source, Kind: kind, At: at, Body: body,
	}}
}

// levelOf 电量落在哪一档
func levelOf(pct int, levels []int) int {
	for _, l := range levels {
		if pct >= l {
			return l
		}
	}
	return 0
}

// metersBetween 球面距离(haversine).
//
// 不用平面近似: 平面近似在高纬度会系统性偏大, 而"半径 120 米"
// 这种阈值对误差很敏感 —— 偏 20% 就等于换了个算法.
func metersBetween(a, b Fix) float64 {
	const R = 6371000.0
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLon := (b.Lon - a.Lon) * rad
	la1, la2 := a.Lat*rad, b.Lat*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(la1)*math.Cos(la2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * R * math.Asin(math.Min(1, math.Sqrt(h)))
}

// round5 坐标留 5 位小数 ≈ 1 米.
//
// 再多的位数没有意义(GPS 本身没那个精度), 但会让**同一个地方每次
// 序列化出不同的字节** —— 而这些信号最终会进上下文, 那是前缀缓存的载体.
func round5(v float64) float64 { return math.Round(v*1e5) / 1e5 }
