package engine

// 感知层的接缝 —— **现在只是壳子**.
//
// 放在这里是为了让分级这件事有一个明确的归属方: 决定"什么该成为页、
// 什么只进日志"的是感知层, 不是页存储.
// 页存储只管热/温 (常驻/换出), 冷信号**根本不该进页存储**.
//
// 之所以先放壳子而不是直接做: 主动智能的准入条件是打扰预算和信任,
// 那要等对话链路先跑通. 但接缝现在就留好, 免得将来又长出第二套存储.

// SignalTier 一条信号该被留到哪一级.
type SignalTier string

const (
	// TierSignalHot 进某个 agent 的常驻上下文 —— 每次推理都带着.
	// 极少数信号配得上这一级 (今天的日程、当前位置).
	TierSignalHot SignalTier = "hot"
	// TierSignalWarm 进页存储的 swap —— 可检索、引用到才换入.
	// 大多数信号在这一级.
	TierSignalWarm SignalTier = "warm"
	// TierSignalCold **只进事件日志, 不进页存储**.
	// 高频低价值的东西 (每一次门锁开合) 属于这里.
	// 分错级的代价是直接的: 冷信号进了页存储, 几周就吃满盘.
	TierSignalCold SignalTier = "cold"
)

// Signal 一条感知信号.
//
// 铁律: **只报事实, 不报判断**.
//
//	航班 CA1234 延误 2 小时
//	提醒用户改约晚饭不属于事实信号
//
// 一旦传感器开始输出判断, 智能就被写进了规则, 整套东西退化成 IFTTT.
// 判断永远留给 agent.
type Signal struct {
	// Source 哪个传感器. 用于溯源和撤回
	Source string `json:"source"`
	// Kind 信号种类. agent 按它声明订阅
	Kind string `json:"kind"`
	// At 事实发生的时刻 (不是收到的时刻)
	At int64 `json:"at"`
	// Fact 结构化事实. 不存渲染文案
	Fact any `json:"fact"`
	// Tier 该留到哪一级. 由感知层判, 不由传感器判
	Tier SignalTier `json:"tier"`
}

// Sensor 一个信号源. 壳子: 目前没有实现, 也不该急着加.
type Sensor interface {
	// Name 传感器名
	Name() string
	// Start 开始产信号. 关闭 done 即停
	Start(emit func(Signal), done <-chan struct{}) error
}
