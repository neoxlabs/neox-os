package osinit

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 感知层 · 信号总线.
//
// ── 它解决的是什么问题 ──
//
// 事件日志到现在为止只有出向: 进程做了什么 → 订阅者看见.
// 主动智能要的是入向: 外界发生了什么 → 进程被叫醒.
//
// 而入向有一个出向没有的难题: **量**. 一部手机一天几万个位置点/电量点,
// 而对话是一条一条的. 中间必须有一层, 否则两件事同时发生:
//
//	agent 每条信号醒一次     → 一天醒几万次
//	原始流直接进上下文       → 模型看到的是噪音的海, 判断力**下降**
//
// 注意第二条**跟省钱无关**. 就算推理不要钱, 把一条流塞进上下文
// 也会让模型看不清 —— 聚合是为了让它看得清.
//
// ── 为什么是"手搓的流处理"而不是 Flink ──
//
// Flink 的四个核心概念里我们只要三个:
//
//	事件时间 vs 处理时间   **要**. 手机离线补传是常态, 用到达时间会算错
//	窗口                  **要**. 这就是缓冲: 一个窗口醒一次
//	水位线                **要**(简化). "比 T 更老的不会再来了", 窗口才敢关
//	精确一次/检查点/状态后端  **不要**. 至少一次 + 按 ID 幂等去重就够
//
// 最后那一条正是 Flink 重的地方. 砍掉它, 剩下的东西几百行就能写完,
// 而且每一条都能解释给人听.

// ── 三条触发器 ──────────────────────────────────────────────
//
// "批量执行还是定期执行"这个问题的答案是: **都要, 外加穿透**.
//
//	① 时间到   窗口满了 → 关窗出摘要        管的是"别拖太久"
//	② 量到     攒够 N 条 → 提前关窗          管的是"暴增别憋成一个巨块"
//	③ 穿透     紧急的不等窗口, 立刻叫醒      管的是"来电话了"这类
//
// 少了 ③ 就只是个批处理器, 少了 ② 就会在信号暴增时产出一个模型读不完的摘要.

const (
	defaultWindow   = 5 * time.Minute
	defaultMaxBatch = 50
	// defaultLateness 容忍多久的乱序.
	//
	// 手机可能离线几小时后补传, 但**窗口不能等它几小时** —— 那样
	// "主动"就变成了"延迟主动". 所以水位线只等 2 分钟,
	// 更晚到的走迟到路径(见 EvSignalLate): 记下来、能查到, 但不回炉重开窗口.
	defaultLateness = 2 * time.Minute
	// dedupRetention 幂等键留多久. 比 lateness 宽一截即可 ——
	// 留太短会让补传的重复件漏过去, 留太长纯占内存
	dedupRetention = 30 * time.Minute

	// ── 补传 ──
	//
	// defaultBackfillQuiet 补传突发"安静"多久算结束.
	//
	// **补传和实时是两种时间观, 连关窗方式都不一样**:
	//
	//	实时  滚动窗口, 按时间片切     —— 事情在持续发生
	//	补传  会话窗口, 按"安静多久"切 —— 手机上线灌一批然后停
	//
	// 用滚动窗口切补传是错的: 一次补传可能跨 24 小时的事件时间,
	// 按 5 分钟切会切出 288 份摘要, 而它本来就是一件事("我离线了一天").
	defaultBackfillQuiet = 30 * time.Second
	// defaultBackfillMax 一次补传攒到这么多条就先出一份, 别憋成一个巨块
	defaultBackfillMax = 200
	// defaultUrgentGrace 紧急信号迟到多久之内**还算紧急**.
	//
	// ── 这条是真机重放逼出来的 ──
	//
	// 上一版的规则是"紧急信号先于水位线检查, 所以迟到了也照样穿透".
	// 那条在小尺度上是对的(晚 3 分钟的门锁异常仍然值得立刻知道),
	// 但它**没有上界**, 而没有上界就会出这种事:
	//
	//	手机离线一天 → 补传一条 20 小时前的 lock.opened
	//	→ 系统当场喊"**刚刚发生了一件需要立刻知道的事**"
	//
	// 对着昨天的事喊"刚刚", 一次就够毁掉这条通道的可信度 ——
	// 而通道的可信度是主动智能唯一的本钱.
	//
	// 所以紧急有时效: 过了这个点它不再是"刚刚", 转入补传.
	defaultUrgentGrace = 15 * time.Minute
	// maxClockSkew 可知时间最多能比现在快多少.
	//
	// ── 一个时钟不对的采集端会饿死所有别的采集端 ──
	//
	// 水位线是 max(见过的最大可知时间, 墙钟) - 容忍度. 那个 max 是为了
	// "没有新信号时窗口也能关", 但它有个副作用:
	// **任何一个采集端只要报出未来的时间戳, 就把水位线推到了未来**,
	// 于是别的采集端的正常信号全部变成"比水位线老" —— 全判迟到.
	//
	// 日尺度合成时撞到的: 合成器里 HA 桥接没注入时钟, 用了真实的 time.Now
	// (比模拟时间快 24 小时), 结果**手机的信号一条不剩全迟到**.
	// 那是合成器的锅, 但真实世界里一部时钟偏快的手机能做到同样的事,
	// 而表现是"另一个采集端突然全不工作了" —— 查起来会先怀疑那个受害者.
	//
	// 所以未来的时间戳要被夹回来, 而且要记下来.
	maxClockSkew = 5 * time.Minute
)

// SignalBus 信号总线.
//
// 一台机器一条. 所有采集端(手机/家居/日历/来电监听)都往这里投,
// **OS 只认 Signal 这一种形状** —— 加一个新数据源不用改 OS 一行.
type SignalBus struct {
	mu sync.Mutex
	// now 可注入的时钟. 窗口和水位线都跟时间有关, 不注入就只能靠 sleep 测,
	// 那样的测试又慢又飘
	now      func() time.Time
	window   time.Duration
	maxBatch int
	lateness time.Duration

	// urgent 哪些 kind 可以穿透窗口 —— **这是策略, 不是采集端说了算**.
	//
	// 沿用"Sensor 只报事实不报判断"那条: 采集端报的是"有一通来电",
	// 至于这件事该不该立刻叫醒你, 是策略层的判断.
	//
	// 让采集端自己声明 urgent 的后果是可预见的: 每个采集端都会觉得
	// 自己的事最急 —— 跟"不能让当事人给自己定优先级"是同一类错.
	urgent map[string]bool

	onStale func([]StaleCollector)
	onBack  func([]string)
	// hb 采集端报到 —— 见 heartbeat.go. 整个感知层可以静默地死掉,
	// 这是唯一能发现它的东西
	hb heartbeats
	// mutes 静音表 —— 见 mute.go. 管的是"要不要为它开口",
	// 不是"要不要记下来": 静音的信号照样落账
	mutes map[string]*Mute
	// seen 幂等去重. 采集端重传是常态(断网补发、APP 重启)
	seen map[string]int64
	// windows 按窗口起点分桶. 桶的归属由**事件时间**决定
	windows map[int64][]abi.Signal
	// watermark 水位线: 比它更老的信号不再期待
	watermark int64

	// ── 补传 ──
	//
	// 迟到的信号原来只走 EvSignalLate: 记下来、查得到, 但**不进窗口
	// 也不产出摘要**. 那对实时路径是对的(重开已关的窗口会改写
	// 已经投出去的历史), 但它让"离线补传"这件事整个失效了 ——
	// 真机重放一天的轨迹, 7 条里 6 条判迟到, 于是没有任何人看得到.
	// 而离线补传正是手机采集端存在的理由之一.
	backfill      []abi.Signal
	backfillLast  int64 // 最后一条补传到达的**墙钟**时刻(不是事件时间)
	backfillQuiet time.Duration
	backfillMax   int
	urgentGrace   time.Duration

	observe func(abi.Signal)
	redact  func(abi.Signal) map[string]any
	emit    func(Digest)
	log     *EventLog
}

// SignalOptions 可调的三个旋钮. 零值表示用缺省
type SignalOptions struct {
	Window   time.Duration
	MaxBatch int
	Lateness time.Duration
	// Urgent 允许穿透窗口的 kind. nil 表示用缺省表
	Urgent []string
	// BackfillQuiet 补传突发安静多久算结束(会话窗口)
	BackfillQuiet time.Duration
	// BackfillMax 补传攒到多少条就先出一份
	BackfillMax int
	// UrgentGrace 紧急信号迟到多久之内还算紧急. 过了就转补传
	UrgentGrace time.Duration
	// Redact 落账本之前把 body 收一收. nil = 原样记.
	//
	//	── 为什么这道闸在"记"这一步, 不在"读"那一步 ──
	//
	//	账本是**只增不删**的, 那是这套系统的骨架 —— 也就是说,
	//	这一刻记下去的东西, 一年以后还在.
	//
	//	而感知层收的是位置、来电、心率: 一份精确到米、逐分钟的行踪记录,
	//	是这台机器上最危险的一个文件. 它一旦落进去, 后面任何"读的时候
	//	过滤一下"都是自欺 —— 文件还在那儿.
	//
	//	**内存里那份仍然是精确的**(观察链拿到的是原始信号), 于是判断
	//	不受影响: 地点匹配、在场推断、世界模型全都照旧. 粗糙的只有
	//	那份留下来的.
	Redact func(abi.Signal) map[string]any

	// Observe 每收下一条信号让宿主看一眼. **总线自己不解释 body** ——
	// 这个钩子是给宿主做增强用的(比如把坐标记下来, 好让用户能说
	// "这儿是公司"). 有它加数据源仍然不用改总线
	Observe func(abi.Signal)
	// Now 注入时钟, 测试用
	Now func() time.Time
	// OnStale 发现采集端太久没消息了.
	//
	// **必须由总线的心跳来调, 不能挂在摘要那条路上**: 采集端死了就没有
	// 信号, 没有信号就没有摘要 —— 那条路在最需要它的时候永远走不到,
	// 感知层越是彻底地瞎了越没人发现. 我第一版就是那么写的.
	OnStale func([]StaleCollector)
	// OnBack 它又回来了
	OnBack func([]string)
}

// defaultUrgentKinds 缺省的穿透表.
//
// 判据不是"这件事重要吗", 是**"晚 5 分钟说, 代价是不是不可逆"**:
//
//	来电进行中   晚 5 分钟说 = 电话已经挂了, 不可逆   → 穿透
//	门锁被打开   晚 5 分钟说 = 人已经进来了, 不可逆   → 穿透
//	电量 20%    晚 5 分钟说 = 没关系                → 不穿透
//	位置变了     晚 5 分钟说 = 没关系                → 不穿透
var defaultUrgentKinds = []string{
	"call.incoming",
	"call.missed",
	"lock.opened",
	"alarm.fired",
	"health.critical",
}

func NewSignalBus(log *EventLog, emit func(Digest), o SignalOptions) *SignalBus {
	b := &SignalBus{
		now:           o.Now,
		window:        o.Window,
		maxBatch:      o.MaxBatch,
		lateness:      o.Lateness,
		backfillQuiet: o.BackfillQuiet,
		backfillMax:   o.BackfillMax,
		urgentGrace:   o.UrgentGrace,
		urgent:        map[string]bool{},
		seen:          map[string]int64{},
		windows:       map[int64][]abi.Signal{},
		observe:       o.Observe,
		redact:        o.Redact,
		onStale:       o.OnStale,
		onBack:        o.OnBack,
		emit:          emit,
		log:           log,
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.window <= 0 {
		b.window = defaultWindow
	}
	if b.maxBatch <= 0 {
		b.maxBatch = defaultMaxBatch
	}
	if b.lateness <= 0 {
		b.lateness = defaultLateness
	}
	if b.backfillQuiet <= 0 {
		b.backfillQuiet = defaultBackfillQuiet
	}
	if b.backfillMax <= 0 {
		b.backfillMax = defaultBackfillMax
	}
	if b.urgentGrace <= 0 {
		b.urgentGrace = defaultUrgentGrace
	}
	kinds := o.Urgent
	if kinds == nil {
		kinds = defaultUrgentKinds
	}
	for _, k := range kinds {
		b.urgent[k] = true
	}
	return b
}

// Ingest 收一条信号.
//
// 返回它去了哪儿 —— 采集端要能知道自己发的东西被怎么处理了,
// 这跟"截断必须说出来"是同一条: 静默地把一条信号归入迟到,
// 会让采集端以为一切正常, 而实际上它的时钟慢了半小时.
type IngestResult string

const (
	// IngestAccepted 进了窗口, 等着关窗
	IngestAccepted IngestResult = "accepted"
	// IngestUrgent 穿透了, 已经立刻投出去
	IngestUrgent IngestResult = "urgent"
	// IngestDuplicate 这个 ID 见过了 —— 重传, 不重复计入
	IngestDuplicate IngestResult = "duplicate"
	// IngestLate 比水位线还老. **记下来了, 没丢**, 但不回炉重开窗口
	IngestLate IngestResult = "late"
	// IngestMuted 这个来源的这个种类被静音了 —— **落了账, 但不会为它开口**.
	// 采集端要能看见这个下场: 静默当成 accepted 的话, 采集端永远不知道
	// 自己在往一个黑洞里投
	IngestMuted IngestResult = "muted"
)

func (b *SignalBus) Ingest(s abi.Signal) IngestResult {
	return b.ingest(s, false)
}

// ingest replay=true 表示这条是**开机时从账本里捞回来的**, 不是采集端新投的.
//
// ── 两条路必须分开, 而且分法只有这一种是对的 ──
//
// 60 条独立信号会在账本里变成 150 条记录 —— 同一条出现 3 次,
// 每重启一次就多一份. 因为捞回来的信号走的是同一个 Ingest, 于是又落一次账.
//
// 最重的后果不是占地方, 是**报告会说谎**: 报告是校准唯一的依据,
// 而它把 60 条数成 150 条. 拿虚高 2.5 倍的数字去调阈值比不调更糟 ——
// 你会以为某个种类吵得要命, 然后把它静掉.
//
// **但不能用"去重挡住"来修**: 捞回来的信号如果被判成 duplicate,
// "捞回上次没来得及总结的 N 条"(S16)就变成一句空话, 而且没有任何报错 ——
// 那正是 S16 花力气解决的、最难发现的一种丢失.
//
// 所以: 重放的**不落账**(它本来就在账本里)、**不判重**(它必须被总结),
// 但**记进去重表**(采集端待会儿真重传时要挡得住).
func (b *SignalBus) ingest(s abi.Signal, replay bool) IngestResult {
	b.mu.Lock()
	nowMs := b.now().UnixMilli()

	// 事件时间缺省补成到达时间, 但**必须记下来这是补的**.
	//
	// 补出来的时间和采集端给的时间是两回事: 前者混进了网络延迟和
	// 补传间隔. 不标注的话, 事后追查"它当时到底知道什么"会得出错误结论.
	guessedTime := false
	if s.At <= 0 {
		s.At = nowMs
		guessedTime = true
	}
	// 可知时间缺省等于发生时间 —— 绝大多数信号"发生即可知".
	//
	// **时钟倒挂要纠正**: KnownAt < At 意味着"算出来的时刻早于发生的时刻",
	// 那只可能是采集端时钟出了问题. 照单全收的话它会绕过水位线,
	// 而我们看不出来.
	if s.KnownAt <= 0 || s.KnownAt < s.At {
		s.KnownAt = s.At
	}
	// **未来的时间戳夹回来** —— 否则一个时钟偏快的采集端会把水位线
	// 推到未来, 饿死所有别的采集端(见 maxClockSkew)
	if skew := s.KnownAt - nowMs; skew > maxClockSkew.Milliseconds() {
		b.record(abi.EvSignalLate, map[string]any{
			"id": s.ID, "source": s.Source, "kind": s.Kind,
			"at": s.At, "knownAt": s.KnownAt, "skewMs": skew,
			"msg": "可知时间在未来, 已夹回现在 —— 这个采集端的时钟不对",
		})
		s.KnownAt = nowMs
		if s.At > nowMs {
			s.At = nowMs
		}
	}
	if s.ID == "" {
		// 没给幂等键就按内容+时间造一个. 采集端该给, 不给也不能丢
		s.ID = fmt.Sprintf("%s|%s|%d", s.Source, s.Kind, s.At)
	}

	if _, dup := b.seen[s.ID]; dup && !replay {
		b.mu.Unlock()
		return IngestDuplicate
	}
	b.seen[s.ID] = s.At
	b.pruneSeen(nowMs)
	// **投信号本身就是报到** —— 一个一直在投的采集端不该还要额外打心跳,
	// 那等于让它为了证明活着而多打一次. 不带节奏: 节奏只由心跳那条路更新
	b.hb.beatWith(s.Source, 0, time.UnixMilli(nowMs), beatOK, "")

	if !replay {
		// **落进去的那份可以比手上这份粗糙** —— 见 SignalOptions.Redact
		body := s.Body
		if b.redact != nil {
			body = b.redact(s)
		}
		b.record(abi.EvSignal, map[string]any{
			"id": s.ID, "source": s.Source, "kind": s.Kind,
			"at": s.At, "knownAt": s.KnownAt, "body": body,
			"guessedTime": guessedTime,
		})
	}
	// 宿主看一眼这条信号. **放在去重之后**: 重传的同一条不该被当成
	// 新的观察, 否则"最近一次位置"会被一条补传的旧位置盖掉,
	// 而用户接着说"这儿是公司"就记错了地方.
	//
	// 总线自己仍然不解释 body —— 它只是把信号原样递给宿主.
	if b.observe != nil {
		b.observe(s)
	}

	// **静音判在落账之后** —— 静音管的是"要不要为它开口",
	// 不是"要不要记下来"(见 mute.go). 落在前面的话, 报告就再也
	// 看不见这个种类, "这条静音当初静得对不对"永远无法复核
	if b.mutedLocked(s) {
		b.mu.Unlock()
		return IngestMuted
	}

	// ── 触发器 ③: 穿透 ──
	//
	// 放在窗口逻辑**之前**: 紧急信号根本不进窗口, 也就不会在关窗时
	// 被再报一次. 报两次比晚报更糟 —— 用户会开始怀疑这个通道.
	// 紧急**且还新鲜**才穿透. 过了时效的紧急信号转补传 ——
	// 对着昨天的事喊"刚刚", 一次就够毁掉这条通道
	// 时效按**可知时间**算: 一件 20 分钟前发生、刚刚才算出来的事,
	// 对用户来说仍然是新闻
	if b.urgent[s.Kind] && nowMs-s.KnownAt <= b.urgentGrace.Milliseconds() {
		d := Digest{
			Reason: DigestUrgent, From: s.At, To: s.At, Count: 1,
			Items: []DigestItem{{
				Source: s.Source, Kind: s.Kind, N: 1,
				First: s.At, Last: s.At, Sample: s.Body,
			}},
		}
		// **穿透的摘要一样要落日志.**
		//
		// 头一版只有 closeWindow 里记, 穿透这条路径绕过了它 ——
		// 来电确实投到了终端, 而账本里 signal.digest 仍是 0.
		// 后果不是少一行日志: 事后追查"它当时到底通知过我什么"会得出
		// "从来没通知过"的结论, 而那正是账本存在的理由.
		b.record(abi.EvSignalDigest, map[string]any{
			"reason": string(DigestUrgent), "from": d.From, "to": d.To,
			"count": 1, "items": 1, "kind": s.Kind,
		})
		b.advanceWatermark(s.KnownAt, nowMs)
		due := b.closeDue()
		b.mu.Unlock()
		b.deliver(d)
		for _, x := range due {
			b.deliver(x)
		}
		return IngestUrgent
	}

	// ── 水位线 ──
	//
	// 比水位线还老 = 它该去的那个窗口已经关了. 重开窗口是错的:
	// 摘要已经投出去、agent 可能已经据此说过话了, 再改一次历史
	// 谁也说不清它到底看到过什么.
	// 迟到与否按**可知时间**判: 一条刚算出来的到达不该因为
	// 它描述的是 5 分钟前的事就被当成历史
	if s.KnownAt < b.watermark {
		b.record(abi.EvSignalLate, map[string]any{
			"id": s.ID, "source": s.Source, "kind": s.Kind,
			"at": s.At, "knownAt": s.KnownAt, "watermark": b.watermark,
			"lateMs": b.watermark - s.KnownAt,
			"msg":    "信号比水位线更老, 已记录但没有进窗口",
		})
		b.backfill = append(b.backfill, s)
		b.backfillLast = nowMs
		var flushed *Digest
		if len(b.backfill) >= b.backfillMax {
			d := b.flushBackfill()
			flushed = &d
		}
		b.mu.Unlock()
		if flushed != nil {
			b.deliver(*flushed)
		}
		return IngestLate
	}

	// 归窗同样按可知时间 —— 窗口分的是"我什么时候知道的",
	// 内容里的时间戳仍然是发生时刻
	start := b.windowStart(s.KnownAt)
	b.windows[start] = append(b.windows[start], s)

	// ── 触发器 ②: 量到就提前关窗 ──
	var early *Digest
	if len(b.windows[start]) >= b.maxBatch {
		d := b.closeWindow(start, DigestBatch)
		early = &d
	}
	b.advanceWatermark(s.KnownAt, nowMs)
	due := b.closeDue()
	b.mu.Unlock()

	if early != nil {
		b.deliver(*early)
	}
	for _, x := range due {
		b.deliver(x)
	}
	return IngestAccepted
}

// Tick 时间往前走了 —— 关掉所有到期的窗口.
//
// ── 为什么必须有它, 不能只在 Ingest 里关窗 ──
//
// 只在收到新信号时才关窗, 等于"没有新消息就永远不通知你".
// 而**最后一条信号往往正是要说的那件事**: 你到家了(位置信号停止变化),
// 然后就再没有信号了 —— 靠 Ingest 驱动的话, 那个窗口永远关不上.
func (b *SignalBus) Tick() {
	// 顺路查采集端还在不在 —— **这条必须在 Tick 里**, 不能在摘要回调里:
	// 采集端死了就没有信号, 没有信号就没有摘要
	if b.onStale != nil {
		if st := b.NewlyStale(); len(st) > 0 {
			b.onStale(st)
		}
	}
	if b.onBack != nil {
		if back := b.NewlyBack(); len(back) > 0 {
			b.onBack(back)
		}
	}
	b.mu.Lock()
	nowMs := b.now().UnixMilli()
	b.advanceWatermark(0, nowMs)
	due := b.closeDue()
	// 补传用**会话窗口**: 安静够久就认为这一波补完了.
	//
	// 手机上线时是一阵突发, 然后停. 等它停下来再summarize 一次,
	// 比按固定时间片切成很多份强得多 —— 那本来就是一件事("我离线了一天").
	var back *Digest
	if len(b.backfill) > 0 && nowMs-b.backfillLast >= b.backfillQuiet.Milliseconds() {
		d := b.flushBackfill()
		back = &d
	}
	b.mu.Unlock()
	for _, d := range due {
		b.deliver(d)
	}
	if back != nil {
		b.deliver(*back)
	}
}

// Start 让时间自己走. 返回停止函数.
//
// 一秒一跳 —— 窗口是分钟级的, 跳得再密也没有用, 而这是个常驻协程.
func (b *SignalBus) Start() (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				b.Tick()
			}
		}
	}()
	return func() { close(done) }
}

// advanceWatermark 水位线 = max(见过的最大事件时间, 墙钟) - 容忍度.
//
// **两个来源都要**, 各自补对方的短板:
//
//	只看事件时间   没有新信号时水位线不动 → 窗口永远关不上(见 Tick 的说明)
//	只看墙钟       采集端时钟偏快时, 未来的信号会把窗口冲垮
//
// 取两者的较大值再减容忍度, 是"宁可晚关一点, 也不要关不上".
func (b *SignalBus) advanceWatermark(eventAt, nowMs int64) {
	base := nowMs
	if eventAt > base {
		base = eventAt
	}
	if w := base - b.lateness.Milliseconds(); w > b.watermark {
		b.watermark = w
	}
}

// closeDue 关掉所有"结束时间已经在水位线之下"的窗口.
//
// 空窗口不产出摘要 —— **安静的时候就该什么都不发生**.
// 这条看着显然, 但它正是整个感知层的验收判据: 好的主动智能
// 大部分时候不打扰你, 而不是"它做了多少事".
func (b *SignalBus) closeDue() []Digest {
	var starts []int64
	for st := range b.windows {
		if st+b.window.Milliseconds() <= b.watermark {
			starts = append(starts, st)
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	out := make([]Digest, 0, len(starts))
	for _, st := range starts {
		out = append(out, b.closeWindow(st, DigestWindow))
	}
	return out
}

func (b *SignalBus) windowStart(at int64) int64 {
	w := b.window.Milliseconds()
	return at - at%w
}

// closeWindow 关一个窗口, 产出摘要. 调用方必须持锁
func (b *SignalBus) closeWindow(start int64, reason DigestReason) Digest {
	sigs := b.windows[start]
	delete(b.windows, start)
	d := summarize(sigs, reason)
	d.From, d.To = start, start+b.window.Milliseconds()
	b.record(abi.EvSignalDigest, map[string]any{
		"reason": string(reason), "from": d.From, "to": d.To,
		"count": d.Count, "items": len(d.Items),
	})
	return d
}

// flushBackfill 把攒着的迟到信号summarize 成一份"历史". 调用方必须持锁.
//
// **跟实时摘要明确分开**: 它们已经发生过了, 用户此刻做不了任何事.
// 混进实时摘要里的话, agent 会分不清"现在门开着"和"昨天门开过".
func (b *SignalBus) flushBackfill() Digest {
	sigs := b.backfill
	b.backfill = nil
	d := summarize(sigs, DigestBackfill)
	for _, s := range sigs {
		if d.From == 0 || s.At < d.From {
			d.From = s.At
		}
		if s.At > d.To {
			d.To = s.At
		}
	}
	b.record(abi.EvSignalDigest, map[string]any{
		"reason": string(DigestBackfill), "from": d.From, "to": d.To,
		"count": d.Count, "items": len(d.Items),
	})
	return d
}

// PendingBackfill 还攒着多少条补传 —— 给运维看的
func (b *SignalBus) PendingBackfill() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.backfill)
}

func (b *SignalBus) deliver(d Digest) {
	if b.emit != nil && d.Count > 0 {
		b.emit(d)
	}
}

// record 往事件日志里记. 感知层的条目挂在一个固定的伪进程号上 ——
// 它不属于任何一个对话, 是**这台机器**看到的东西.
const signalPID = "sense"

func (b *SignalBus) record(kind abi.EventKind, payload map[string]any) {
	if b.log != nil {
		b.log.Append(signalPID, kind, payload)
	}
}

func (b *SignalBus) pruneSeen(nowMs int64) {
	cut := nowMs - dedupRetention.Milliseconds()
	for id, at := range b.seen {
		if at < cut {
			delete(b.seen, id)
		}
	}
}

// LatenessTooSmallFor 容忍度够不够一个上传周期这么长.
//
// ── 这条是真机演示时撞出来的 ──
//
// 采集端是**批量上传**的(手机每 10 秒发一批, 省电也省握手). 也就是说
// 一条信号从"算出来"到"到达总线"天然差一个上传周期.
//
// 而水位线是 现在 - 容忍度. 容忍度比上传周期还小的话:
//
//	每一条信号到达时都已经比水位线老 → **全部判成迟到** →
//	全部走历史通道 → 主动进程收到的每一条都是"这些已经发生过了"
//
// 演示时就是这么翻车的: 容忍度配了 4 秒, 手机每 10 秒传一批,
// 于是连"你刚到公司"都成了历史. 而且它**看起来完全正常** ——
// 信号都在、账本都有, 只是全在错误的那条通道上.
//
// 所以启动时要能自检一句. 不做成硬性拒绝: 有的采集端是实时推的,
// 那时候小容忍度是对的.
func (b *SignalBus) LatenessTooSmallFor(uploadPeriod time.Duration) bool {
	return b.lateness < uploadPeriod
}

// EffectiveDelay 一条普通信号从发生到被投出去, 最坏要等多久.
//
// ── 这个数必须能被问出来 ──
//
// **它不等于窗口长度**: 窗口关不关取决于水位线, 而水位线 = 现在 - 容忍度.
// 所以配了 10 秒的窗口、2 分钟的容忍度, 实际最坏要等 2 分 10 秒.
//
// 这是流处理的正确语义, 但不说清楚会误导配置: 窗口配 10 秒,
// 等了 15 秒仍没有摘要时, 很容易误以为窗口出了问题.
//
// 谁配这套东西谁都会踩一次, 所以让它能被打印出来.
func (b *SignalBus) EffectiveDelay() time.Duration {
	return b.window + b.lateness
}

// Watermark 当前水位线 —— 排查"为什么这条被判成迟到"时要看它
func (b *SignalBus) Watermark() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watermark
}

// Pending 还没关窗的信号条数 —— 给运维看的
func (b *SignalBus) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, s := range b.windows {
		n += len(s)
	}
	return n
}

// ── 摘要 ────────────────────────────────────────────────────

type DigestReason string

const (
	// DigestWindow 窗口到时间了
	DigestWindow DigestReason = "window"
	// DigestBatch 攒够了条数, 提前关
	DigestBatch DigestReason = "batch"
	// DigestUrgent 穿透, 没等窗口
	DigestUrgent DigestReason = "urgent"
	// DigestBackfill 补传的历史 —— **已经发生过了**.
	//
	// 它跟前三种在语义上是另一类: 前三种说的是"现在", 这一种说的是"过去".
	// 混在一起的话, agent 会分不清"现在门开着"和"昨天门开过" ——
	// 而那两件事该做的反应完全不同.
	DigestBackfill DigestReason = "backfill"
)

// Digest 一个窗口的摘要 —— **这才是给模型看的东西**.
type Digest struct {
	Reason DigestReason `json:"reason"`
	From   int64        `json:"from"`
	To     int64        `json:"to"`
	Count  int          `json:"count"`
	Items  []DigestItem `json:"items"`
}

// DigestItem 同一个来源同一种事, 聚成一条.
//
// **保留 Sample**: 只给"位置变了 37 次"的话, 模型没有任何具体的东西
// 可以判断 —— 它需要一个锚. 给最后一条的原文, 因为对状态类信号
// (位置/电量/开关)来说, **最新的那条就是当前状态**.
type DigestItem struct {
	Source string         `json:"source"`
	Kind   string         `json:"kind"`
	N      int            `json:"n"`
	First  int64          `json:"first"`
	Last   int64          `json:"last"`
	Sample map[string]any `json:"sample,omitempty"`
}

func summarize(sigs []abi.Signal, reason DigestReason) Digest {
	d := Digest{Reason: reason, Count: len(sigs)}
	type key struct{ src, kind string }
	idx := map[key]int{}
	for _, s := range sigs {
		k := key{s.Source, s.Kind}
		if i, ok := idx[k]; ok {
			it := &d.Items[i]
			it.N++
			if s.At < it.First {
				it.First = s.At
			}
			if s.At >= it.Last {
				it.Last, it.Sample = s.At, s.Body
			}
			continue
		}
		idx[k] = len(d.Items)
		d.Items = append(d.Items, DigestItem{
			Source: s.Source, Kind: s.Kind, N: 1,
			First: s.At, Last: s.At, Sample: s.Body,
		})
	}
	// 排序 —— 摘要会进上下文, 而同样的一批信号必须生成同样的字节.
	// 顺序随 map 遍历漂的话, 前缀缓存会莫名其妙地断
	sort.Slice(d.Items, func(i, j int) bool {
		if d.Items[i].Source != d.Items[j].Source {
			return d.Items[i].Source < d.Items[j].Source
		}
		return d.Items[i].Kind < d.Items[j].Kind
	})
	return d
}

// Text 摘要渲染成给模型看的一段话.
//
// 为什么不直接把 JSON 丢过去: 模型读结构化文本比读 JSON 稳,
// 而且这段话要跟提示词里那些"环境事实"读起来是一家人.
func (d Digest) Text() string {
	head := "这段时间外界发生的事"
	switch d.Reason {
	case DigestUrgent:
		head = "**刚刚发生了一件需要立刻知道的事**"
	case DigestBatch:
		head = "外界的事情来得比较密, 先给你一批"
	case DigestBackfill:
		// **措辞必须把"已经发生过了"说死.**
		//
		// 补传摘要跟实时摘要长得一模一样的话, agent 会把昨天的门锁
		// 当成现在的门锁 —— 而那两件事该做的反应完全不同.
		head = "以下是补传上来的**历史**(采集端之前离线, 这些事已经发生过了, " +
			"不需要现在处理; 用户问起'我今天去过哪儿'这类问题时用它答)"
	}
	// 跨天的摘要只显示时分秒会让人读错日期 —— 补传天生跨天
	stamp := tsShort
	if d.To-d.From > 12*3600*1000 {
		stamp = tsFull
	}
	s := fmt.Sprintf("%s(%s — %s, 共 %d 条):\n",
		head, stamp(d.From), stamp(d.To), d.Count)
	for _, it := range d.Items {
		s += fmt.Sprintf("- %s · %s × %d", it.Source, it.Kind, it.N)
		if it.N > 1 {
			s += fmt.Sprintf(" (%s—%s)", tsShort(it.First), tsShort(it.Last))
		}
		if len(it.Sample) > 0 {
			s += fmt.Sprintf("  最新: %v", sortedKV(it.Sample))
		}
		s += "\n"
	}
	return s
}

// humanValue 值渲染成给模型看的样子.
//
// ── 整数必须按整数打 ──
//
// 信号过一趟 JSON 之后, 所有数字都是 float64, 而 %v 对 float64 用的是 %g:
// 值一大就变成科学计数法. 摘要可能出现
//
//	stayedMs=3.75e+06
//
// 终端和模型摘要都需要避免这种表示, 但模型读 "3.75e+06" 尤其容易出错:
// 它可能照着这个数字去算, 也可能干脆当成一段乱码跳过.
//
// 同一类错误在两个地方各犯一次, 说明这不是"某处写漏了", 而是
// **JSON 边界必然产生的一种损失**, 凡是把过了 JSON 的数字渲染给别人看的
// 地方都要过一道.
func humanValue(v any) string {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprint(v)
}

// humanKV 一个键值对渲染成给模型看的样子.
//
// ── 时长要换算成人话 ──
//
// 摘要里的 stayedMs=75216 虽然可计算, 但**每一次
// 让它算的东西, 都是一次它可能算错、而且没人会发现的地方** ——
// 而"在公司待了多久"正是它判断值不值得说的依据之一.
//
// 跟科学计数法那次(stayedMs=3.75e+06)是同一类: 给模型看的东西,
// 该在这一侧就变成它不用再加工的形状.
func humanKV(k string, v any) string {
	if strings.HasSuffix(k, "Ms") {
		if f, ok := v.(float64); ok {
			return strings.TrimSuffix(k, "Ms") + "=" + humanDuration(int64(f))
		}
		if n, ok := v.(int64); ok {
			return strings.TrimSuffix(k, "Ms") + "=" + humanDuration(n)
		}
	}
	return k + "=" + humanValue(v)
}

func humanDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	}
}

func tsShort(ms int64) string { return time.UnixMilli(ms).Format("15:04:05") }

// tsFull 跨天时用. 只给时分秒的话, 一份跨三天的补传摘要读起来
// 像是同一天里发生的事
func tsFull(ms int64) string { return time.UnixMilli(ms).Format("01-02 15:04") }

// sortedKV 按键名排序输出 —— 同样的内容必须生成同样的字节, 见 summarize
func sortedKV(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, humanKV(k, m[k]))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

// RecoverPending 把"进程死掉时还攒在窗口里"的信号捞回来.
//
// ── 这一类失败完全没被碰过 ──
//
// 窗口里攒着的信号**只在内存里**. 进程一重启(升级、崩溃、机器重启),
// 最多一个窗口的信号就没了 —— 而它们**在账本里**, 只是永远不会有人
// 去总结它们.
//
// 这是最难发现的一种丢失: 没有报错、账本完整、事后翻也能翻到那些信号,
// 只是"它们从来没被送到过任何人面前". 跟闹钟那条是同一类
// (活不过进程), 而窗口一直没做.
//
// ── 怎么精确地知道是哪些 ──
//
// **最后一条摘要之后的所有信号, 恰好就是没被总结过的那些.**
//
// 因为窗口一关就落一条 signal.digest, 穿透也落. 所以账本里
// "最后一条 digest 之后的 signal" = 当时还挂在窗口里的 + 刚进来的.
// 不需要额外记状态, 顺序本身就是答案.
//
// ── 捞回来之后走哪条路 ──
//
// 走**补传**(它们本来就已经发生过了), 不是实时窗口:
// 重启可能隔了很久, 把半小时前的信号当成"刚刚发生"再报一次,
// 就是对着旧事喊"刚刚" —— 那正是 S4b 花力气避免的事.
//
// 而补传通道现成的: 这些信号的可知时间早于水位线, Ingest 自己就会
// 把它们归进补传. 所以这里只要**原样重放**, 不需要特判.
func (b *SignalBus) RecoverPending(events []abi.Event) int {
	lastDigest := -1
	for i, e := range events {
		if e.Kind == abi.EvSignalDigest {
			lastDigest = i
		}
	}
	// **先把水位线拉到现在.**
	//
	// 不拉的话, 新总线的水位线还是 0, 于是**重放的第一条会被当成实时** ——
	// 它进了窗口, 而那个窗口一算就是"早就该关了", 立刻产出一份
	// 长得像实时的摘要. 后面几条才走补传.
	//
	// 现象很怪: 同一批捞回来的信号, 第一条报成"这段时间外界发生的事",
	// 其余报成"这些已经发生过了". 这种首条与后续不一致的现象
	// 容易被当成偶发而放过.
	//
	// 恢复那一刻我们**已经知道**接下来重放的全是历史, 那就说出来.
	b.mu.Lock()
	b.advanceWatermark(0, b.now().UnixMilli())
	b.mu.Unlock()

	n := 0
	for _, e := range events[lastDigest+1:] {
		if e.Kind != abi.EvSignal {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		s := abi.Signal{
			ID: str(m["id"]), Source: str(m["source"]), Kind: str(m["kind"]),
			At: asMillis(m["at"]), KnownAt: asMillis(m["knownAt"]),
		}
		if body, ok := m["body"].(map[string]any); ok {
			s.Body = body
		}
		if s.ID == "" || s.Kind == "" {
			continue
		}
		// **幂等键要保住原来那个**: 捞回来的信号跟原件是同一件事.
		// 重新生成 ID 的话, 万一它其实已经被总结过(比如崩在了
		// 落 digest 事件的前一刻), 用户就会看到重复的一条.
		b.ingest(s, true)
		n++
	}
	return n
}

func str(v any) string { s, _ := v.(string); return s }

// RestoreSeen 开机时把去重表读回来.
//
// **"断网补发是常态"是去重存在的全部理由**(seen 那儿的注释就这么写的).
// 而我们自己一重启那张表就空了 —— 采集端此刻重传的那一批会被当成新的:
// 账本里多一份、摘要里多一遍, 用户听到两次"你到公司了".
//
// 这是"重启之后没被恢复的状态"的第三例(S26 打扰额度、S30 静音表,
// 现在是去重表). 共同的形状是: **一个只活在内存里的判断依据,
// 而它挡的事情恰恰在重启前后最容易发生.**
//
// 守同一个保留期: 一份跑了一年的账本会把几十万个幂等键全装进内存,
// 而超过保留期的那些早就不起作用了(pruneSeen 本来就会删掉它们).
func (b *SignalBus) RestoreSeen(events []abi.Event) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	cut := b.now().UnixMilli() - dedupRetention.Milliseconds()
	n := 0
	for _, e := range events {
		if e.Kind != abi.EvSignal {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		id := str(m["id"])
		at := asMillis(m["at"])
		if id == "" || at < cut {
			continue
		}
		b.seen[id] = at
		n++
	}
	return n
}

// SeenSize 去重表里现在有多少个键 —— 要能被问出来, 否则
// "它是不是在无限长大"没有任何办法回答
func (b *SignalBus) SeenSize() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.seen)
}
