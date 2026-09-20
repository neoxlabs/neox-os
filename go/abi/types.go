// Package abi 是 Neox OS 的系统调用契约.
//
// 只有类型和常量, 没有实现. 它是 OS 与应用之间唯一的接口面.
//
// 这份 Go 实现跟 TypeScript 版 (packages/abi) 必须**逐字段一致** ——
// 两边通过 wire 格式对话, 字段名对不上就是静默的数据丢失.
// go/abi/parity_test.go 用 TS 生成的样本锁住这一点.
package abi

import "errors"

const Version = "0.0.1-unstable"

// ── 内核对象 ① Process ──────────────────────────────────────

type ProcessID = string

type ProcessState string

const (
	// StateCreated 已登记, 还没开始跑
	StateCreated ProcessState = "created"
	// StateRunning 正在占用计算
	StateRunning ProcessState = "running"
	// StateWaiting 在等外部事件 (决策/定时/回调). 不占计算, 但活着
	StateWaiting ProcessState = "waiting"
	// StateSuspended 被 OS 换出. 状态已落盘, 计算资源已归还
	StateSuspended ProcessState = "suspended"
	StateExited    ProcessState = "exited"
	StateFailed    ProcessState = "failed"
)

// IsTerminal 到这里就不会再变
func (s ProcessState) IsTerminal() bool {
	return s == StateExited || s == StateFailed
}

type ProcessSpec struct {
	App    string            `json:"app"`
	Name   string            `json:"name,omitempty"`
	Caps   []Capability      `json:"caps"`
	Budget *Budget           `json:"budget,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

type ProcessInfo struct {
	PID       ProcessID       `json:"pid"`
	Spec      ProcessSpec     `json:"spec"`
	State     ProcessState    `json:"state"`
	CreatedAt int64           `json:"createdAt"`
	ChangedAt int64           `json:"changedAt"`
	LogLength int             `json:"logLength"`
	Outcome   *ProcessOutcome `json:"outcome,omitempty"`
}

type ProcessOutcome struct {
	OK    bool          `json:"ok"`
	Value any           `json:"value,omitempty"`
	Error *OutcomeError `json:"error,omitempty"`
}

type OutcomeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ── 内核对象 ② 事件日志 ─────────────────────────────────────

type EventKind string

const (
	EvProcState      EventKind = "proc.state"
	EvProcOutput     EventKind = "proc.output"
	EvDecideRequest  EventKind = "decide.requested"
	EvDecideResolved EventKind = "decide.resolved"
	EvCapUsed        EventKind = "cap.used"
	EvCapDenied      EventKind = "cap.denied"
	EvBudgetSpent    EventKind = "budget.spent"
	EvProcOutcome    EventKind = "proc.outcome"
	// EvInputRecv 进程收到一句用户输入 —— 多轮对话里一轮的分界点.
	// 事件日志靠它能重放出完整对话, 而不只是工具调用流水.
	EvInputRecv EventKind = "input.recv"

	// EvCorrection 用户明确改过方向.
	//
	// 事件日志里只有它做了什么, 没有"这是对的还是错的".
	// "不是这样, 是那样" / "算了还是按分存" 是唯一带标签的监督信号,
	// 也是唯一不能从流水里推断出来的东西 —— 所以要单独记.
	//
	// 写入必须是自动的 (OS 在收话时认), 不能靠 agent 自己记笔记:
	// 那种失败模式是忘了记, 而且没有任何机制能发现它忘了.
	EvCorrection EventKind = "user.correction"

	// ── 感知层 ──
	//
	// 事件日志到现在为止只有**出向**(进程 → 订阅者). 这两条是入向:
	// 外界发生的事进到同一条总线上, 于是"它自己知道"和"你问它"
	// 走的是同一套存储、同一套订阅、同一套重放.

	// EvSignal 一条原始信号落库. 每一条都记 —— 聚合会丢细节,
	// 而事后追查("那天几点它到底看到了什么")只能靠原始条目.
	EvSignal EventKind = "signal.in"
	// EvSignalDigest 一个窗口关闭, 产出摘要.
	//
	// **给模型看的是摘要, 不是流.** 不是为了省钱(大模型不贵),
	// 是因为一条流塞进上下文之后模型看到的是噪音的海, 判断力会下降.
	EvSignalDigest EventKind = "signal.digest"
	// ── 定时唤醒 ──
	//
	// 闹钟必须活得比进程长, 而事件日志就是它的载体 ——
	// 跟决策活得比进程长是同一条, 同一个办法.
	EvWakeSet       EventKind = "wake.set"
	EvWakeFired     EventKind = "wake.fired"
	EvWakeCancelled EventKind = "wake.cancelled"

	// EvPlaceNamed 用户给一个坐标起了名字.
	//
	// 落日志是因为**地点是用户教的**: 丢了要重教一遍,
	// 那比丢一条提醒更让人恼火.
	EvPlaceNamed EventKind = "place.named"

	// EvWatchSet / EvWatchRemoved 用户定的关注("以后我到家你就说一声").
	//
	// **必须落盘**: 这条关注要跨越进程生命周期, 下一段对话是一个全新进程.
	// 不落的话, 这个功能跟 agent 嘴上答应一句没有区别 —— 而那正是
	// 否则系统里没有可恢复的关注, 只能留下无法兑现的口头承诺.
	EvWatchSet     EventKind = "watch.set"
	EvWatchRemoved EventKind = "watch.removed"

	// EvDailyReport 日报发过了(或者今天没什么可发).
	//
	// **没发也要记**: "今天真的很安静"和"日报坏了"两种情况, 用户看到的
	// 都是"没收到日报", 只有账本能分开.
	//
	// 而且这个状态必须落盘 —— 不落的话每次重启都重新武装,
	// 一天重启五次就发五份日报, 而日报的全部价值在于"一天一次、可预期".
	EvDailyReport EventKind = "daily.report"

	// EvInterrupt 一次打扰请求的下场(放行/破例/攒着/重复).
	//
	// **每一次都要记, 包括没放行的那些**: "今天很安静"和"额度早就用完、
	// 后面全在攒"这两种情况, 用户看到的都是"它没怎么说话",
	// 只有账本能把它们分开.
	EvInterrupt EventKind = "interrupt.verdict"

	/**
	 * EvDelivery 一条**要送到人跟前**的东西.
	 *
	 *	── 跟上面那几种的分工 ──
	 *
	 *	interrupt.verdict / wake.fired / daily.report 记的是
	 *	**发生了什么**(账本), 而这一条记的是**要送到人跟前**(投递意图).
	 *
	 *	它们不是一回事: 一条 interrupt.verdict 的 verdict 可能是
	 *	defer(攒着不说), 那就不该有投递; 而一个 remind_me 到点走的是
	 *	proc.output, 账本里根本没有"这条要通知他"的痕迹.
	 *
	 *	── 为什么必须统一成一种 ──
	 *
	 *	不统一的话, 每一端(手机、桌面、以后的手表)都要认三种以上的
	 *	事件形状, 而**加第四种主动来源时必然漏掉某一端** ——
	 *	否则 remind_me 到点时, 账本里有、界面上有,
	 *	而手机端只监听那三种, 于是 App 没开 = 完全收不到,
	 *	**而闹钟存在的全部理由就是"我怕自己忘"**.
	 *
	 *	这跟 /signal 那个入口是同一个设计的反向: 入向 OS 只认一种
	 *	Signal, 出向订阅方只认一种 Delivery.
	 */
	EvDelivery EventKind = "delivery"
	// EvMuteSet / EvMuteRemoved 把某个来源的某个种类静音/解除.
	//
	// 校准报告认得出噪音种类("从没导致过任何通知"), 但它给的建议是
	// "在采集端砍掉" —— **而采集端是装在别人手机里的 APK、或者别人
	// 跑着的 HA 桥, 那是个改不动的地方**. 所以闭嘴这件事必须 OS 侧也能做.
	EvMuteSet     EventKind = "signal.muted"
	EvMuteRemoved EventKind = "signal.unmuted"

	// EvPlaceForgotten 忘掉一个地方.
	//
	// **教错了要能改**: 一个记错的"家"会让所有跟到家有关的判断都错 ——
	// 到家提醒在他还在路上时响、"我在哪"答错、通勤时间从错的起点算.
	// 而这些一个都不会报错.
	EvPlaceForgotten EventKind = "place.forgotten"

	// EvZoneSet 这台机器算在哪个时区.
	//
	// ── 为什么它必须是一条落账的设定, 不是环境变量 ──
	//
	// 时区不是显示格式, 它进**判断**: 日报几点发、规则的"晚上七点到
	// 十点"算哪一段、闹钟"明天早上八点"是哪一刻、停留算在哪一天.
	// Docker 里默认是 UTC, 于是这些全部差 8 小时 —— **而一处都不会
	// 报错**: 日报照发, 只是在凌晨五点发; 规则照判, 只是判错了时段.
	//
	// 靠 TZ 环境变量的话, 这件事就只有开容器的人改得动, 而他改完
	// 还得记得重启. 落账之后它跟地点、规则一样是这台 OS 的一份设定:
	// 界面上改得动, 手机报到时也能顶上来.
	EvZoneSet EventKind = "zone.set"

	// EvDeviceAsk OS 反过来问设备要一样东西 —— **不进账本**.
	//
	// ── 为什么需要反向这一条 ──
	//
	//	采集端是**按变化触发**的: 人没挪超过 120 米就一条都不报.
	//	那对省电是对的, 对"我现在在哪条路"却是致命的 —— 查询时
	//	这句话时, OS 手里最新的一条是三分钟前的, 而它只能照实说
	//	"我永远比你慢半拍".
	//
	//	三分钟的误差在市区里是两个路口。答一个过期的路名, 比说
	//	"不知道"糟 —— 他会照着走。
	//
	//	所以要有一条**问**的路: 他真问起来的时候, OS 让手机立刻取一次.
	//	平时照旧不问 —— 这条路一天走不了几次, 而按变化触发那套省下的
	//	是一整天的电.
	//
	// ── 为什么不进账本 ──
	//
	//	它是一句**吆喝**, 不是一件发生过的事: 手机取到了就照常发一条
	//	正常的位置信号, 那条才是事实. 把吆喝也记下来, 账本里就多了一半
	//	没有信息量的行. 跟 EvProcDelta 同一条道理.
	//
	//	断线的设备收不到也没关系: 它下次报到时带的就是新位置.
	EvDeviceAsk EventKind = "device.ask"

	// EvDeviceAct OS 让某台设备**去做一件事** —— 跟 EvDeviceAsk 同一条路,
	// 同样**不进账本**.
	//
	// ── 为什么控制要走这条路, 而不是 OS 直接去调 ──
	//
	//	家里的 Home Assistant 在他家的路由器后面, 而 OS 跑在机房.
	//	要 OS 直接调, 就得把 HA 暴露到公网(或者打一条隧道进他家局域网)
	//	—— 那是为了开一盏灯, 把整个家开一道口子.
	//
	//	反过来就不用: 采集端本来就在家里、本来就连着 OS(它一直在投信号).
	//	让它顺便**听一句吆喝**, 收到就在本地调 HA —— 家里只有出站连接,
	//	一个端口都不用开.
	//
	// ── 为什么不进账本 ──
	//
	//	跟 EvDeviceAsk 同理: 这是一句吆喝, 不是一件发生过的事.
	//	灯真的亮了, 采集端下一轮比对就会报一条 light.on 的信号 ——
	//	**那条才是事实**. 记吆喝等于把"我让它做"当成"它做了",
	//	而这两件事恰恰在出问题的时候不一样.
	EvDeviceAct EventKind = "device.act"

	// EvTaskAdded / EvTaskDone / EvTaskDropped 一件待办或者一个日程.
	//
	//	**待办和日程是同一个东西, 差一个时间**: "下周三三点开会"和
	//	"记得买牛奶"在存储上没有区别, 一句话、归谁、什么时候、做没做.
	//	分成两套的代价这个仓库已经付过一次(四个"以后提醒我"的工具):
	//	模型每一轮都要在两个里挑, 挑错不报错, 只是那件事再也找不着.
	//
	//	划掉**不删**: 划掉的东西他还要能看见, 而且"我今天干了什么"
	//	要靠它答. 扔掉才是真删(记错了、不做了).
	//
	//	见 osinit/agenda.go
	EvTaskAdded   EventKind = "task.added"
	EvTaskDone    EventKind = "task.done"
	EvTaskDropped EventKind = "task.dropped"

	// EvNoteSet / EvNoteForgot 它记住的一件事.
	//
	// ── 为什么要有一个不限种类的记忆 ──
	//
	// 在它之前, 每一种"记住的东西"都是一个手写的登记簿: 地点、人、
	// 设备、规则、关注 —— 各自一对事件、各自一份 Add/Forget/Restore/List、
	// 各自一条 HTTP 路由. 于是"记一下我老婆生日"这种最普通的要求
	// **一件都办不到**, 而办不到的原因跟这件事本身没关系, 是因为
	// 没人为它写过第七个登记簿.
	//
	// 那五个登记簿留着是对的: 它们各自带着索引和语义(地点要按距离查、
	// 规则要按条件触发), 那些不是"存一条字符串"能替代的.
	//
	// 这一对管的是**剩下的全部**: 一个键、一句话、归谁. 没有索引,
	// 也不参与任何判断 —— 它唯一的用处是下次开机时它还知道.
	//
	// 只增不删: 忘掉是**再落一条**, 不是把旧的抹掉. 见 osinit/notes.go
	EvNoteSet    EventKind = "note.set"
	EvNoteForgot EventKind = "note.forgot"

	// EvStayEnded 一次停留结束了(在某个地方待够久之后离开).
	//
	// **规律是攒出来的, 一次重启不该清零**: 攒够"在 4 个不同的日子待过
	// 同一个地方"要好几天, 而进程一周会重启好几次. 清零的话它永远看不出
	// 任何规律, 而且没有任何一处会说.
	//
	// 只落停留, 不落每条位置 —— 后者一天几十条, 而它们里面有信息量的
	// 只有"在哪儿待了多久"这一件事.
	EvStayEnded EventKind = "stay.ended"

	// EvProcDelta 边生成边吐的那一小段字.
	//
	// ── 它跟别的事件不是一类: **它不进账本** ──
	//
	// 一次回复会吐上百段. 落账的话账本每条回复膨胀一百倍, 而且每个
	// 客户端一重连就要把那一百条再重放一遍 —— 而它们加起来的信息量,
	// 跟最后那一条 proc.output(phase:reply) **一模一样**.
	//
	// 那一条才是真相: 重启后重建对话窗口(agent/restore.go)、
	// 事后召回(recall)读的都是它.
	//
	// 所以这一类只给**此刻正看着的人**: 让他早两秒看见字, 仅此而已.
	// 断线重连的人看到的是完整那条, 不缺任何东西.
	//
	// 跟心跳不落账是同一条道理("落账的话一天几千条, 那正是感知层
	// 最该避免的东西").
	EvProcDelta EventKind = "proc.delta"

	// EvPersonKnown 屋里认得一个人了(或者他改了名字).
	//
	// **这不是鉴权**: 谁拿到这台 OS 的 token 谁就有全部权限, 加了人的
	// 概念之后还是这样. 它解决的是**归属** —— "这条位置是谁的"、
	// "这条通知该给谁". 一台家用的 OS 上不止一个人, 没有归属的话
	// "我现在在哪"这个问题没有唯一答案, 而系统会给一个.
	EvPersonKnown EventKind = "person.known"

	// EvDeviceForgotten / EvPersonForgotten 删掉一台设备 / 一个人.
	//
	// **必须有**: 换手机、卖掉一块表、一个人搬走了 —— 删不掉的话,
	// 设备列表里会永远躺着一台三年前的手机, 而 OS 还在等它报到.
	// 用一条新事件而不是从账本里抹掉旧的: 账本只增不删是这套系统的骨架.
	EvDeviceForgotten EventKind = "device.forgotten"
	EvPersonForgotten EventKind = "person.forgotten"

	// EvRuleSet / EvRuleRemoved 一条条件触发规则("下雨且我在家就提醒我带伞").
	//
	// **规则活得比进程长** —— 跟闹钟同一条道理: 已设定的规则
	// 不该因为一次重启就没了, 而他不会知道.
	EvRuleSet     EventKind = "rule.set"
	EvRuleRemoved EventKind = "rule.removed"
	// EvRuleFired 一条规则从不成立变成了成立.
	//
	// 落账是为了事后能答"它为什么那时候喊我" —— 一个说不出理由的
	// 主动提醒, 用户唯一能做的处置就是把整个通道关掉.
	EvRuleFired EventKind = "rule.fired"

	// EvDeviceDeclared 一台设备报到: 我是谁, 我能感知什么, 我能怎么告诉你.
	//
	// ── 为什么设备要落账本 ──
	//
	// 采集端原来是**匿名**的: 第一次见到 source 这个字符串就建一条,
	// OS 对它一无所知 —— 不知道它能不能出声、有没有屏、报的是米还是英尺.
	// 于是"怎么告诉用户"只能写死成"推安卓通知", 而下一个接进来的东西
	// 可能是一副耳机(只能出声)或者一块 ESP32(只有一颗灯).
	//
	// **设备自己声明能力, OS 据此选怎么说** —— 换个硬件不用改 OS 一行,
	// 跟"OS 只认 Signal 这一种形状"是同一条边界, 只是方向反过来.
	//
	// 落账本而不是只放内存: 重启之后 OS 得知道屋里有哪些设备,
	// 而不是等它们各自下一次报到 —— 一台一天只报一次的设备(比如
	// 体重秤), 重启后能失踪一整天.
	EvDeviceDeclared EventKind = "device.declared"

	// EvCollectorDown / EvCollectorUp 一路采集**整段地瞎了 / 回来了**.
	//
	// **只落状态变化, 不落心跳**: 心跳是每 10 秒一次的"我还在",
	// 落账的话一天几千条, 那正是感知层最该避免的东西.
	// 而"从这一刻起看不见了"是稀有且要紧的事实 —— 校准报告要靠它
	// 判断"这段账本里有多少时间是瞎的", 否则它会把一段瞎着的时间
	// 当成"很安静", 而安静率是整个感知层的验收判据.
	EvCollectorDown EventKind = "collector.down"
	EvCollectorUp   EventKind = "collector.up"

	// EvSignalLate 信号迟到了(事件时间比水位线还老).
	//
	// **迟到的不丢**: 手机离线之后补传是常态, 丢了就等于"那段时间没发生过".
	// 但也不能装作没迟到 —— 它进不了已经关掉的窗口, 这件事必须看得见.
	EvSignalLate EventKind = "signal.late"
)

type Event struct {
	// Seq 进程内单调递增, 从 0 开始
	Seq     int       `json:"seq"`
	PID     ProcessID `json:"pid"`
	At      int64     `json:"at"`
	Kind    EventKind `json:"kind"`
	Payload any       `json:"payload"`
}

// ── 内核对象 ③ 能力 ─────────────────────────────────────────

type CapAxis string

const (
	AxisRead   CapAxis = "read"
	AxisWrite  CapAxis = "write"
	AxisNet    CapAxis = "net"
	AxisProc   CapAxis = "proc"
	AxisSecret CapAxis = "secret"
)

type Capability struct {
	Axis  CapAxis `json:"axis"`
	Scope string  `json:"scope"`
}

// ── 内核对象 ④ 预算 ─────────────────────────────────────────
//
// 注意语义: 这是**止损上限**, 不是预先分配的配额.
// agent 的开销预估不出来, 按预估分配是空谈.

type Budget struct {
	// ActiveMs 总**活跃**时长上限. waiting/suspended 不计入
	ActiveMs *int64 `json:"activeMs,omitempty"`
	Tokens   *int64 `json:"tokens,omitempty"`
	NetCalls *int64 `json:"netCalls,omitempty"`
}

type BudgetSpent struct {
	ActiveMs int64 `json:"activeMs"`
	Tokens   int64 `json:"tokens"`
	NetCalls int64 `json:"netCalls"`
}

// BudgetDelta 一次消耗上报. 指针是为了区分"没报"和"报了 0"
type BudgetDelta struct {
	ActiveMs *int64 `json:"activeMs,omitempty"`
	Tokens   *int64 `json:"tokens,omitempty"`
	NetCalls *int64 `json:"netCalls,omitempty"`
}

// ── 决策 ────────────────────────────────────────────────────

type DecisionID = string

type DecisionRequest struct {
	Present PresentSpec `json:"present"`
	// Scope 这次授权针对**哪个资源** (路径/主机名).
	//
	// 光有 Present 里的文案是不够的: 批准之后 OS 要知道到底授了什么,
	// 从标题里抠字符串是猜. 用户答了 yes,
	// 而 landlock 的规则在进程启动时就定死、只能收紧不能放宽,
	// 于是那次调用照样被内核拒: **问了人、人说可以、还是做不到**.
	// 有了这个字段, 批准才能被记进对话的授权表, 下个进程带着它起来.
	Scope string `json:"scope,omitempty"`
	// Axis 授的是**哪条能力轴**. 缺省 write.
	//
	// 光有 Scope 不够: `registry.npmjs.org` 既可能是要授出网,
	// 也可能被当成一个路径去授写权限 —— 授错轴等于授了完全不同的东西.
	// 缺省 write 是因为历史上只有写会走到审批, 而不是因为 write 更安全;
	// 新的轴一律显式写出来.
	Axis      CapAxis          `json:"axis,omitempty"`
	OnTimeout *DecisionTimeout `json:"onTimeout,omitempty"`
	Urgency   string           `json:"urgency,omitempty"` // low|normal|high
}

type DecisionTimeout struct {
	AfterMs int64  `json:"afterMs"`
	Choose  string `json:"choose"`
}

type PendingDecision struct {
	DID         DecisionID      `json:"did"`
	PID         ProcessID       `json:"pid"`
	Request     DecisionRequest `json:"request"`
	RequestedAt int64           `json:"requestedAt"`
}

type DecisionResolution struct {
	DID    DecisionID     `json:"did"`
	Choice string         `json:"choice"`
	Values map[string]any `json:"values,omitempty"`
	// By 谁解决的. "timeout" 表示 OS 按 onTimeout 兜底 —— 不许静默默认
	By string `json:"by"`
	At int64  `json:"at"`
}

// ── 呈现协议 · 描述语义不描述像素 ────────────────────────────
//
// 白名单封闭. 进程只能从中选, 不能生成任意代码.
// 这既是安全边界, 也是"任意客户端都能渲染"的保证 ——
// ESP32 只实现 choice/approve 两种也能当管家终端.

type PresentSpec struct {
	Kind    string          `json:"kind"` // choice|approve|form|progress|table|doc|media
	Title   string          `json:"title"`
	Detail  string          `json:"detail,omitempty"`
	Options []PresentOption `json:"options,omitempty"`
	Fields  []PresentField  `json:"fields,omitempty"`
	Diff    string          `json:"diff,omitempty"`
	Ratio   *float64        `json:"ratio,omitempty"`
	Note    string          `json:"note,omitempty"`
	Columns []string        `json:"columns,omitempty"`
	Rows    [][]string      `json:"rows,omitempty"`
	Body    string          `json:"body,omitempty"`
	// VolumePath / MIME 用于 media
	VolumePath string `json:"volumePath,omitempty"`
	MIME       string `json:"mime,omitempty"`
}

type PresentOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Detail      string `json:"detail,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

type PresentField struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Type     string          `json:"type"` // text|number|bool|select
	Options  []PresentOption `json:"options,omitempty"`
	Required bool            `json:"required,omitempty"`
}

// ── 约束 ────────────────────────────────────────────────────

// Requirement 是**需求**不是机制 —— 同一需求可由不同内核机制满足.
// 写死机制名会让我们在明明能强制的机器上拒绝启动
// 例如 linuxkit 内核可能无 landlock 但有 BPF-LSM.
type Requirement string

const (
	ReqFSEnforce     Requirement = "fs-enforce"
	ReqResourceLimit Requirement = "resource-limit"
	ReqNetIsolate    Requirement = "net-isolate"
	ReqSyscallNotify Requirement = "syscall-notify"
)

type Mechanism string

const (
	MechLandlock         Mechanism = "landlock"
	MechBPFLSM           Mechanism = "bpf-lsm"
	MechCgroup2          Mechanism = "cgroup2"
	MechNetns            Mechanism = "netns"
	MechSeccomp          Mechanism = "seccomp"
	MechSeccompUserNotif Mechanism = "seccomp-user-notif"
)

type FSRule struct {
	Path   string   `json:"path"`
	Access []string `json:"access"` // read|write, 已排序
}

type NetRule struct {
	Host string `json:"host"`
	Port string `json:"port,omitempty"`
}

type CgroupLimits struct {
	MemoryMaxBytes *int64 `json:"memoryMaxBytes,omitempty"`
	CPUWeight      *int64 `json:"cpuWeight,omitempty"`
	PidsMax        *int64 `json:"pidsMax,omitempty"`
}

type ConfinementPlan struct {
	// FS 来自用户授予的能力
	FS []FSRule `json:"fs"`
	// RuntimeFS 是 OS 恒定授予的只读执行底座, 不来自任何能力.
	// 没有它进程连自己的可执行文件都读不到 —— 真机第一次跑就撞上.
	RuntimeFS []string `json:"runtimeFs"`
	// RuntimeDevices 必须**可写**的设备节点, 同样恒定授予.
	//
	// 为什么不能并进 RuntimeFS: 那些是只读的, 而 /dev/null 必须能写 ——
	// `> /dev/null` 是写操作. 缺少它时会出现: 起 20 个后台进程,
	// 20 个全挂在 `cannot open /dev/null: Permission denied` 上.
	// shell 的作业控制、几乎每个构建脚本都要它.
	//
	// 为什么逐个列而不是整个 /dev: 整个 /dev 可写等于把裸磁盘
	// (/dev/vda) 和内存 (/dev/mem) 一起给出去. 逐个列出后应满足:
	// /dev/null 写得进, /dev/vda 读不到.
	RuntimeDevices  []string      `json:"runtimeDevices"`
	Net             []NetRule     `json:"net"`
	AllowSubprocess bool          `json:"allowSubprocess"`
	Cgroup          CgroupLimits  `json:"cgroup"`
	Requires        []Requirement `json:"requires"`
}

type EnforcementProbe struct {
	Platform string `json:"platform"`
	// Mechanisms 内核支持**且我们已接入**的. 只支持不接入不算数 —— 那是 fail-open
	Mechanisms []Mechanism `json:"mechanisms"`
	// DetectedNotWired 探到但没接入的, 只用于诊断
	DetectedNotWired []Mechanism   `json:"detectedNotWired"`
	Satisfied        []Requirement `json:"satisfied"`
	Missing          []Requirement `json:"missing"`
	Usable           bool          `json:"usable"`
	Reason           string        `json:"reason,omitempty"`
	// Limits 机制自述的**强制上限**. 探到"支持"不等于"全都拦得住":
	// 老内核的 landlock 只能部分强制 (老 ABI 没有 refer/truncate 那些访问位).
	//
	// 这个必须如实上报. 我们以为关住了、实际只关住一部分, 比明说
	// "只能关住这些"危险得多 —— 后者用户还能自己决定要不要跑.
	Limits []string `json:"limits,omitempty"`
}

// ── 模式 ────────────────────────────────────────────────────

type Mode string

const (
	// ModeConfined 生产. 内核必须能强制约束, 否则拒绝启动进程
	ModeConfined Mode = "confined"
	// ModeDev 开发. 允许同堆闭包, 约束退化为记账
	ModeDev Mode = "dev"
)

// ── 感知层 · 信号 ────────────────────────────────────────────

// Signal 外界发生的一件事.
//
// ── 只报事实, 不报判断 ──
//
// 这条是死规矩. 采集端输出的是"发生了什么", **不是"该做什么"**:
//
	//	来电 138xxxx, 未接              ← 事实
	//	提醒用户回电                     ← 判断
//
// 一旦采集端开始输出判断, 智能就被写进了规则, 整套东西退化成 IFTTT ——
// 那正是"主动智能"和"自动化脚本"的分界. 判断永远留给 agent.
//
// 连"这条急不急"也不由采集端说 —— 见 SignalBus 的 urgent 策略表.
type Signal struct {
	// ID 采集端给的幂等键.
	//
	// 手机断网重传是常态, 同一件事会到达多次. 没有它就只能靠
	// "内容一样"去猜, 而两次真实的心跳内容本来就可能一样.
	ID string `json:"id"`
	// Source 谁报的. 形如 phone.mk / ha.livingroom / calendar
	Source string `json:"source"`
	// Kind 什么事. 形如 location / battery / call.incoming / device.state
	Kind string `json:"kind"`
	// At **事件时间**, 不是到达时间. 毫秒.
	//
	// ── 这个区分是整个感知层的地基 ──
	//
	// 手机离线之后补传: 11:00 发生的事 11:30 才到. 用到达时间的话,
	// "11 点提醒你 12 点的火车"这件事从根上就是错的 —— 系统会以为
	// 那件事 11:30 才发生.
	//
	// 采集端不给就由总线按到达时间补, 并且这件事要记下来(补的和真的不是一回事).
	At int64 `json:"at"`
	// KnownAt **这件事什么时候才算得出来**. 0 = 跟 At 相同(绝大多数信号).
	//
	// ── 为什么需要第二个时间戳 ──
	//
	// 手机的 place.arrived(你到了某地)事件时间是**停留开始
	// 那一刻**, 而"你到了"这件事要到停留够久(dwell)之后才判得出来.
	// 生产配置 dwell 5 分钟、水位线容忍 2 分钟 →
	// **每一条"你到公司了"都比水位线老 3 分钟, 永远走补传通道**,
	// 主动进程收到的是"这些已经发生过了, 不需要现在处理".
	// 而那恰恰是最该实时说的一类.
	//
	// 两个设计各自都对(内容要用发生时刻, 水位线要用现在减容忍度),
	// 撞在一起才出事. 所以把两个时间分开:
	//
	//	At       发生的时刻    内容用它 —— "你 19:00 到的公司"
	//	KnownAt  可知的时刻    窗口/水位线/紧急时效用它 —— "我 19:05 才知道"
	//
	// **KnownAt 不是"上传时刻"**: 一部离线一天的手机补传上来, 那些信号
	// 当时就算出来了, 只是发不出去 —— 它们的 KnownAt 是当时, 所以照样
	// 走补传通道. 把 KnownAt 填成上传时刻的话, 补传会伪装成实时.
	KnownAt int64 `json:"knownAt,omitempty"`
	// Body 结构化内容. 采集端自己的形状, 总线不解释
	Body map[string]any `json:"body,omitempty"`
}

// ErrNotReady 这个数据源**还没准备好**, 不是坏了.
//
//	区别是实打实的, 而且要给到用户面前:
//
//	  坏了     去看凭据、看进程、看网 —— 需要人动手
//	  没准备好 缺一个前提, 而那个前提可能自己会来
//	           (天气要先知道你在哪儿, 而位置等下一次定位就有了)
//
//	把"没准备好"报成"坏了"的后果不是多一条通知, 是**通知失去意义**:
//	一台刚装好的机器会立刻推一条"采集端坏了", 而它其实一切正常.
//	用户学到的那一课是"这些警报不用看".
var ErrNotReady = errors.New("还没准备好")
