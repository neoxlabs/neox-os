// Package sense 是**采集端**, 不是 OS 的一部分.
//
// 边界在这里: 采集端在外面(可换、可以有很多), 信号总线在 OS 里(唯一).
// 它们之间只有一种形状 —— abi.Signal, 走 HTTP.
//
// 所以这个包**不 import osinit**, 也拿不到事件日志和进程表.
// 它能做的只有一件事: 把某个外部世界的东西, 翻译成信号投出去.
package sense

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// Home Assistant 桥接.
//
// ── 采集端是第一道闸, 不是一根管子 ──
//
// 这是整个感知层最容易做错的地方. HA 的 state_changed 什么都报:
// 功率计每秒变一次、温度传感器每几秒抖一下、CPU 占用一直在动.
// 原样转发的后果是可以算出来的:
//
//	一台普通的 HA 有 200~500 个实体, 其中十几个是高频数值传感器
//	→ 一天几十万条信号
//	→ 事件日志是 append-only 且要落盘的, 账本几天就爆
//	→ 而摘要里全是"power 变了 8000 次", 模型看到的是噪音的海
//
// **所以降采样必须发生在这一侧.** 不是为了省钱(大模型不贵),
// 是因为送上去的东西决定了模型能不能看清 —— 送 8000 条抖动上去,
// 就是在用噪音淹掉那条"门开了".
//
// ── 为什么是轮询 + 比对, 而不是订阅 WebSocket ──
//
// HA 的 WS 能推 state_changed, 看起来更"实时". 但:
//
//	① 它推的是**每一次微小变化**, 我们照样要在本地过滤一遍 ——
//	   实时性买到的是更多噪音, 不是更早知道那件重要的事
//	② 轮询+比对天然是**电平触发**的: 只在"跟上次不一样"时才报,
//	   漏一次轮询不会丢状态(下一次照样比出来), 而漏一个 WS 事件会
//	③ 零依赖. 这个仓库只有一个 x/sys, 为一个采集器引 websocket 库不值
//
// 代价要说清楚: 轮询间隔就是最坏延迟. 门锁被打开这类事最坏晚一个间隔
// 才知道 —— 所以间隔可配, 而且缺省取 10 秒而不是分钟级.
type HAConfig struct {
	// BaseURL 形如 http://127.0.0.1:8123
	BaseURL string
	// Token HA 的长期访问令牌
	Token string
	// Interval 轮询间隔. 它就是最坏延迟, 缺省 10 秒
	Interval time.Duration
	// Now 注入时钟(测试用). nil = time.Now
	Now func() time.Time
	// NumericEpsilon 数值传感器要变化多少才算"变了".
	//
	// **这一个数挡掉的噪音比其它所有规则加起来都多.**
	// 缺省 0.05 = 5%: 温度 22.0→22.1 不报, 22.0→24.0 报.
	NumericEpsilon float64
}

// HAState HA 的 /api/states 返回的一条.
//
// 只取我们要的字段 —— 多解析一个字段就多一处会随 HA 版本坏掉的地方.
type HAState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
	LastUpdated string         `json:"last_updated"`
}

// HABridge 一次轮询的状态机. 拿上一次的快照跟这一次比.
type HABridge struct {
	cfg HAConfig
	// last 上一次见到的状态. 电平触发的记忆
	last map[string]HAState
	// primed 头一次轮询过了没有.
	//
	// **头一次不报.** 第一次轮询时所有实体都是"新的", 原样报出去
	// 就是一场 300 条的开机风暴, 而那 300 条没有一条是"刚刚发生的事" ——
	// 它们只是"现在的状态". 事实和事件不是一回事.
	primed bool
}

func NewHABridge(cfg HAConfig) *HABridge {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.NumericEpsilon <= 0 {
		cfg.NumericEpsilon = 0.05
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &HABridge{cfg: cfg, last: map[string]HAState{}}
}

func (b *HABridge) Interval() time.Duration { return b.cfg.Interval }

// Step 收下一次全量快照, 吐出**值得上报的那几条**信号.
//
// 分成纯函数是有意的: 轮询、重试、投递都可以在外面, 而
// "什么算值得上报"这件唯一有判断的事可以被完整地测.
// maxNewPerPoll 一次拉取里冒出几个新实体还算"真的多了设备".
//
// 定在 3: 同时装三个设备是罕见的, 而一次集成加载动辄几十个.
// 定得再大会把"他一口气装了一套灯"也压掉, 而那是他等着看的东西.
const maxNewPerPoll = 3

func (b *HABridge) Step(states []HAState) []abi.Signal {
	// 排序 —— 同样的一次快照必须产出同样顺序的信号.
	// 摘要最终要进上下文, 而那是前缀缓存的载体
	sort.Slice(states, func(i, j int) bool { return states[i].EntityID < states[j].EntityID })

	// ── 上线风暴 ──
	//
	// **108 个实体在四分钟里产生 100 条信号,
	// 89 条是 device.added.** 因为 HA 启动时实体是**陆续**出现的:
	// 第一次轮询看到十几个(那批被 primed 挡掉), 后面几轮又冒出九十几个,
	// 每一个都被报成"有个新设备上线了".
	//
	// 而用户根本不关心"你的 108 个设备上线了".
	//
	// 这跟上面那条掉线风暴是**同一件事的两半**, 而只做了一半:
	//
	//	掉线风暴  一批变 unavailable  = 我们看不见了, 不是它们坏了
	//	上线风暴  一批冒出来          = 我们刚看见, 不是世界多了 89 个东西
	//
	// 判据是**一次拉取里冒出来几个**: 一个是"他刚装上一盏灯"(该报),
	// 一批是"一整套东西进入视野"(不该报). 阈值定在 3: 同时装三个设备
	// 是罕见的, 而一次集成加载动辄几十个.
	newThisPoll := 0
	for _, s := range states {
		if _, seen := b.last[s.EntityID]; !seen && b.primed {
			newThisPoll++
		}
	}
	storm := newThisPoll > maxNewPerPoll

	var out []abi.Signal
	for _, s := range states {
		prev, seen := b.last[s.EntityID]
		b.last[s.EntityID] = s
		if !b.primed {
			continue
		}
		if !seen {
			if storm {
				continue // 一整套东西进入视野 —— 那是我们的视野变了
			}
			// 新出现的实体: 报, 但这是"上线"不是"变化"
			out = append(out, b.toSignal(s, "device.added"))
			continue
		}
		if kind, ok := b.worthReporting(prev, s); ok {
			out = append(out, b.toSignal(s, kind))
		}
	}
	b.primed = true
	return out
}

// worthReporting 这一次变化值不值得占用一条信号.
//
// 返回的 kind 是**事实的种类**, 不是判断. "门锁开了"是事实;
// "该提醒主人"是判断, 那个留给 agent.
func (b *HABridge) worthReporting(prev, cur HAState) (string, bool) {
	// ── HA 重启风暴 ──
	//
	// HA 重启/集成断线时, 全部实体会在同一秒变成 unavailable,
	// 恢复时再一起变回来. 原样报的话就是两场 300 条的风暴,
	// 而它们**没有一条对应真实世界的变化** —— 灯没动, 只是我们看不见了.
	//
	// 这是最常见的一种假信号, 而且最像真的: 时间戳、实体、
	// 状态转换全都合法.
	if isUnknownState(cur.State) || isUnknownState(prev.State) {
		return "", false
	}

	// ── 状态是时间戳的实体, 报的是"计划"不是"发生" ──
	//
	// **这条是真实 HA 数据给出的第一个校准结论.**
	//
	// 一台几乎没接设备的 HA, 11 天产出 97 条信号, 其中 **60 条**是
	// sensor.sun_next_dawn / _dusk / _rising … 这类"下次日出在几点".
	// 它们每天重算一次, 于是每天报 6 条 —— 而"下次日出时间变了"
	// 这件事, 没有任何人需要被告知.
	//
	// 判据不是实体名(那要维护一张永远不全的黑名单), 是**状态的形状**:
	// 一个状态是未来时刻的实体, 说的是日程不是事件. 它变了只说明重算了一次.
	//
	// 真正的日出(sun.sun 从 below_horizon 变 above_horizon)照样报 ——
	// 那是状态变了, 不是时间戳变了.
	if isTimestampState(cur.State) {
		return "", false
	}
	// 钟同理 —— 真数据里它是最大的一股噪音(十五分钟 14 条信号里 13 条
	// 是 sensor.time). 见 isClockState
	if isClockState(cur.State) {
		return "", false
	}
	// **过程不是结果.**
	//
	// 门锁的状态机是
	// locked → unlocking → unlocked → locking → locked.
	// 而这些 -ing 的状态说的是"正在做", 不是"做完了" ——
	// 锁可能卡住、可能最终没锁上, 车库门可能停在半路.
	//
	// 报它们不是多一条噪音, 是**报了一件没发生的事**:
	// 对着一件不可逆的事给出错误的结论, 比不报更糟.
	// 对照日的账本里就是这么来的: 6 条 lock.closed, 0 条 lock.opened.
	if isTransitionalState(cur.State) {
		return "", false
	}

	if prev.State == cur.State {
		// 状态字符串没变. 属性变了也不报 ——
		// 亮度从 254 抖到 255、媒体播放器的进度条, 这类东西一天几万条
		return "", false
	}

	// ── 数值传感器: 只有变化够大才算变了 ──
	//
	// 这一条挡掉的噪音比其它所有规则加起来都多. 功率计的读数
	// 每一秒都不一样, 而"每一秒都不一样"不是信息.
	if a, okA := asFloat(prev.State); okA {
		if c, okC := asFloat(cur.State); okC {
			if !numericChanged(a, c, b.cfg.NumericEpsilon,
				attrString(cur.Attributes, "unit_of_measurement")) {
				return "", false
			}
			return "sensor.changed", true
		}
	}

	return semanticKind(cur.EntityID, prev.State, cur.State), true
}

// semanticKind 把 HA 的实体+状态转换翻成信号种类.
//
// ── 这算不算"采集端在报判断" ──
//
// 不算, 而且这个区分很重要:
//
//	"lock.opened"      ← 事实: 锁开了. 采集端知道, OS 不知道
//	"该提醒主人回家了"  ← 判断: 留给 agent
//
// 为什么非要翻: OS 的紧急策略表是**按 kind 匹配**的. 全都报成
// device.state 的话, 策略表就只能在 OS 里再解析一次 entity_id ——
// 那等于把 HA 的领域知识搬进内核, 换一种家居协议就要改内核.
//
// **谁认识这个世界谁来翻译**, 这是采集端存在的理由.
func semanticKind(entityID, prev, cur string) string {
	domain, _, _ := strings.Cut(entityID, ".")
	switch domain {
	case "lock":
		if cur == "unlocked" {
			return "lock.opened"
		}
		return "lock.closed"
	case "binary_sensor":
		// 门/窗/移动. HA 用 on/off 表示"触发/未触发"
		if strings.Contains(entityID, "door") || strings.Contains(entityID, "window") {
			if cur == "on" {
				return "door.opened"
			}
			return "door.closed"
		}
		if strings.Contains(entityID, "motion") || strings.Contains(entityID, "occupancy") {
			return "presence.motion"
		}
	case "person", "device_tracker":
		// 谁回家了/走了 —— 这是感知层最有用的信号之一
		return "presence.changed"
	case "alarm_control_panel":
		if cur == "triggered" {
			return "alarm.fired"
		}
	}
	return "device.state"
}

func (b *HABridge) toSignal(s HAState, kind string) abi.Signal {
	at := parseHATime(s.LastChanged)
	if at == 0 {
		at = parseHATime(s.LastUpdated)
	}
	return abi.Signal{
		// ── 可知时间是**轮询到的这一刻**, 不是 last_changed ──
		//
		// 这一条是日尺度合成跑出来的: 一天下来门的信号 23 条**全部判迟到**.
		//
		// 轮询源同样需要区分事件时间和可知时间:
		// HA 用 last_changed 当事件时间是对的(灯确实是 19:00:03 开的),
		// 但我们是**轮询才发现**的 —— 两者差一个轮询周期. 再加上
		// 桥接重启、HA 那边时间戳偏旧, 差得更多.
		//
		// 不填的话这些信号比水位线老, 全走历史通道, 主动性对家居整个失效.
		KnownAt: b.cfg.Now().UnixMilli(),
		// ── 幂等键含 last_changed ──
		//
		// 轮询天生会重复看到同一个状态. 用"实体+状态变化时刻"当键,
		// 重复轮询产出的是同一个 ID, 总线那边直接判重 ——
		// **两侧各有一道去重, 而且判据不同**(这边按变化时刻, 那边按 ID),
		// 任何一侧漏了另一侧还兜得住.
		ID: fmt.Sprintf("ha|%s|%s", s.EntityID, s.LastChanged),
		// ── 来源精确到**实体**, 不是域 ──
		//
		// sensor.time 每分钟跳一次,
		// 一天 1440 条, 永远不会导致任何通知 —— 纯噪音的活样本.
		// 而域粒度下它跟一个真温度传感器共用 ha.sensor/device.state:
		// **想让钟闭嘴, 就得把所有 sensor 一起静掉**, 连温度也不报了.
		//
		// 校准报告也一样: 按来源分组看到的是"ha.sensor 一天 2000 条",
		// 而真正该知道的是"是那个钟在吵".
		//
		// senseapi 那儿举的例子本来就是 ha.livingroom —— 一开始就是
		// 按"哪个东西在报"设计的, 是这个桥接做粗了.
		Source: "ha." + s.EntityID,
		Kind:   kind,
		// **事件时间用 HA 的 last_changed, 不是轮询时刻.**
		//
		// 这是"事件时间 vs 处理时间"最直接的一个实例: 灯是 19:00:03 开的,
		// 我们 19:00:09 才轮询到. 用轮询时刻的话, 整条时间线都会
		// 系统性地晚 0~一个间隔 —— 而"几点开的灯"正是这类信号唯一的价值.
		At: at,
		Body: map[string]any{
			"entity": s.EntityID,
			"state":  s.State,
			"name":   attrString(s.Attributes, "friendly_name"),
			"unit":   attrString(s.Attributes, "unit_of_measurement"),
		},
	}
}

// ── 小工具 ──────────────────────────────────────────────────

// isClockState 状态本身就是"现在几点/今天几号".
//
// **十五分钟里 14 条信号, 13 条是 sensor.time.**
// 一天 1440 条, 每一条都会开一个窗口、叫醒一次主动进程 ——
// 而它报的不是"发生了什么", 它就是时钟本身.
//
// 这跟 isTimestampState 是同一条道理(计划不是事件), 只是形状不同:
// 那边是 ISO 时间戳(下次日出), 这边是 "21:55" 和 "2026-08-16".
// 时钟信号必须显式识别, 否则常规数据集很容易遗漏这类噪音.
//
// **判据只认这两种整形状**: 一个报 "12:30 加时" 的比赛比分不该被压掉,
// 所以带任何别的字符都不算. 误伤一个真事件, 比多报一条钟糟得多.
func isClockState(s string) bool {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"15:04", "15:04:05", "2006-01-02"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// isTimestampState 状态本身就是一个时刻.
//
// HA 里这类实体很多(下次日出、下次备份、上次成功备份…), 它们报的是
// **计划**: "下次日出在 07:12". 变了只说明重算了一次, 世界没有发生什么.
//
// 用形状判而不是用实体名: 黑名单永远不全, 而且换一套集成就要重写.
func isTimestampState(s string) bool {
	if len(s) < 19 {
		return false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// isTransitionalState 正在变, 还没变完.
//
// ── 这里**不能**用形状判, 跟别的规则不一样 ──
//
// 第一版我按"以 -ing 结尾"判, 理由是那几个过渡态长得很整齐
// (unlocking / locking / opening / closing). 而仓库里现有的两条测试
// 当场把它打回来了: 洗衣机的 **running**、扫地机的 **cleaning**
// 同样以 -ing 结尾, 而它们是**终态** —— 洗衣机正在洗就是它现在的样子,
// 不是"正在变成某个状态".
//
// 误伤一个真事件比多报一条噪音糟得多(压掉它, 用户永远不知道有过这件事),
// 所以这里用**明确的一小组**. 这跟"黑名单永远不全"不矛盾:
// HA 的过渡态是**它自己的状态机定义的**, 成对出现、数量有限、很少变;
// 而"哪些实体是噪音"是开放集合, 那才需要形状判.
var transitionalStates = map[string]bool{
	"unlocking": true, "locking": true, // 锁
	"opening": true, "closing": true, // 窗帘/车库门
	"arming": true, "disarming": true, "pending": true, // 警戒
}

func isTransitionalState(s string) bool {
	return transitionalStates[strings.TrimSpace(s)]
}

// isUnknownState HA 表示"我看不见这个设备"的两个状态.
// 它们不是世界的变化, 是我们视野的变化
func isUnknownState(s string) bool {
	return s == "unavailable" || s == "unknown" || s == ""
}

func asFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// changedEnough 相对变化超过阈值才算变了.
//
// 用相对而不是绝对: 一个功率计的 5 瓦和一个温度计的 5 度完全不是一回事,
// 而我们没法给每种传感器配一个绝对阈值(那又变成了 IFTTT 的配置地狱).
//
// 零附近退化成绝对比较 —— 相对变化在 0 附近会炸(0 → 0.1 是无穷大的相对变化),
// 而那恰恰是"设备刚开始耗电"这类真实事件, 不能被当成噪音也不能被放大成风暴.
// percentPoints 百分比量要变几个点才算变了.
//
// **五分钟里 16 条信号中 12 条是
// sensor.system_monitor_processor_use** —— CPU 使用率, 折合一天 3456 条,
// 全来自一个传感器.
//
// 因为相对阈值对它完全不设防: CPU 从 2% 跳到 5% 是 **150%** 的相对变化,
// 远超 5% 的门槛, 而实际只差三个点, 没有任何人需要被告知.
//
// 10 个点: 电量从 80 掉到 70、湿度从 40 升到 50、CPU 从 20 冲到 80 ——
// 这些是人会想知道的; 而三五个点的抖动不是.
const percentPoints = 10.0

// numericChanged 这个数值变化值不值得报.
//
// **量程已知的量用绝对阈值, 量程未知的用相对阈值.**
//
// 相对阈值本身是对的("一个功率计的 5 瓦和一个温度计的 5 度不是一回事"),
// 它只是不适合百分比: 百分比天然是 0–100, 差几个点就是差几个点,
// 而在小数值那一头相对变化会炸(2→5 是 150%).
//
// 判据用 HA 自己给的单位, 不是实体名 —— 黑名单永远不全,
// 而换一套集成就要重写.
func numericChanged(a, c, eps float64, unit string) bool {
	if strings.TrimSpace(unit) == "%" {
		return math.Abs(c-a) >= percentPoints
	}
	return changedEnough(a, c, eps)
}

func changedEnough(a, c, eps float64) bool {
	base := math.Max(math.Abs(a), math.Abs(c))
	if base < 1e-9 {
		return math.Abs(c-a) > 1e-9
	}
	return math.Abs(c-a)/base >= eps
}

func domainOf(entityID string) string {
	d, _, _ := strings.Cut(entityID, ".")
	if d == "" {
		return "unknown"
	}
	return d
}

func attrString(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

// parseHATime HA 给的是 RFC3339. 解不出来返回 0 —— 让总线去补,
// 并且总线会把"时间是补的"这件事标出来
func parseHATime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// ParseStates 解 /api/states 的返回
func ParseStates(raw []byte) ([]HAState, error) {
	var out []HAState
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("HA 的 /api/states 解不开: %w", err)
	}
	return out, nil
}
