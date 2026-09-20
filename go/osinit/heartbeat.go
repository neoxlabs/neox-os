package osinit

import (
	"sort"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 采集端报到 —— 让"整个感知层静默地死掉"这件事能被发现.
//
// ── 长跑里真的发生了 ──
//
// HA 的令牌过期, 采集器老老实实每次都报错重试, 而 OS 那边**一个字都没有**:
// 已经八分钟没有任何信号, 用户看到的是"今天很安静".
//
// 这跟报告里那条"'今天真的很安静'和'日报坏了'长得一模一样"是同一个失败,
// 只是发生在更外面一层 —— 整个感知层可以静默地死掉.
//
// 而这正是"采集端在外面"那条边界的代价: 它可以死、可以重启、可以跑在
// 另一台机器上, 那 OS 就**必须**能发现它没了. 否则那条边界买来的自由,
// 是拿"出事没人知道"换的.
//
// ── 节奏由采集端自己报 ──
//
// OS 不该知道一个家居桥接多久拉一次: 一部手机十分钟传一次是正常的,
// 一个家居桥接十分钟不吭声就是坏了. 所以报到的时候把自己的节奏一起给,
// OS 只按**它自己的节奏**判.
//
// ── 投信号本身就是报到 ──
//
// 一个一直在投的采集端不该还要额外打心跳 —— 那等于让它为了证明活着
// 而多打一次. 心跳只是"没东西可投时也说一声我还在".
type collector struct {
	pace time.Duration
	last time.Time
	// told 这一次失联已经说过了.
	//
	// **说一次就够**: 一个采集端挂一整夜, 按窗口报的话就是几百行
	// "它还没回来" —— 而喊多了跟不喊一样, 用户会开始忽略它,
	// 那时候真出事也没人看. (跟打扰预算同一个道理, 只是不经过模型.)
	told bool
	// back 它回来了, 而且这件事还没说
	back bool
	// blindSince 它从什么时候开始拿不到数据的. 零值 = 一切正常.
	//
	// **心跳跳着不等于它在干活**: 那天 HA 令牌过期, 采集器每次都报错
	// 重试, 心跳照打(报到特意放在拉取之前, 好让"我还在, 只是投不出
	// 东西"能传出来) —— 于是失联那道闸看到的是"它很健康",
	// 而账本里一条信号都没有. **S42 只治了"死了", 没治"瞎了"**,
	// 而后者才是那天真正发生的.
	blindSince time.Time
	// why 它自己说的原因. **必须带**: 只说"它拿不到数据"的话,
	// 用户还是不知道该去修什么; 那天的原因是"HA 拒绝了 token",
	// 一句话就能定位
	why string
}

// StaleCollector 一个多久没消息的采集端
type StaleCollector struct {
	Source string
	Pace   time.Duration // 它自己说的节奏
	Silent time.Duration // 已经静了多久
	// Blind 它还在报到, 只是拿不到数据.
	//
	// **跟失联要分开报**: 两件事的下一步不一样 —— 一个是去看采集端
	// 还在不在(进程/网络), 一个是去看它的凭据. 混成一句话的话,
	// 用户会照着错的方向去查.
	Blind bool
	Why   string // 它自己说的原因
}

type heartbeats struct {
	mu  sync.Mutex
	all map[string]*collector
}

// 报到的三种含义. **分开是必须的** —— 见 beatWith 里的说明
type beatState int

const (
	beatAlive   beatState = iota // 我还在(不代表我拿得到数据)
	beatOK                       // 我拿到数据了
	beatFailing                  // 我还在, 但我拿不到数据
)

func (h *heartbeats) beat(source string, pace time.Duration, at time.Time) {
	h.beatWith(source, pace, at, beatOK, "")
}

func (h *heartbeats) beatWith(source string, pace time.Duration, at time.Time,
	state beatState, why string) {
	if source == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.all == nil {
		h.all = map[string]*collector{}
	}
	c, ok := h.all[source]
	if !ok {
		c = &collector{}
		h.all[source] = c
	}
	// 节奏只在采集端明说时更新 —— 投信号那条路不带节奏, 别把它清掉
	if pace > 0 {
		c.pace = pace
	}
	// ── "我还在" 和 "我还好" 是两件事 ──
	//
	// 采集器每轮**先报到再拉取**: 拉不动时也必须先报到. 于是每一轮
	// 都是"报到 → 拉失败 → 报失败",
	// 而报到那一下如果清掉"瞎了"的计时, 计时就永远停在零 ——
	// **90 秒的门槛永远到不了, 这道闸形同虚设**; OS 会一直不说话.
	//
	// 所以: 光报到只证明"我还在"(不碰瞎不瞎), 只有**真拿到数据**
	// 才证明"我还好".
	switch state {
	case beatFailing:
		if c.blindSince.IsZero() {
			c.blindSince = at // 连着失败才算瞎, 一次网抖不该喊
		}
		c.why = why
	case beatOK:
		c.blindSince, c.why = time.Time{}, ""
	}
	// 报过的这次好了 —— 要说一声, 而且"报过了"要清掉,
	// 否则第二次出事就永远没人知道
	if c.told && state == beatOK {
		c.told, c.back = false, true
	}
	c.last = at
}

// staleFactor 静默多久算失联 = 它自己节奏的几倍.
//
// 三倍: 一次拉取失败(网抖)不该报警, 连着三次就不是偶然了.
// 定得太小会在每次网络抖动时喊一声, 而**喊多了跟不喊一样** ——
// 用户会开始忽略它, 那时候真出事也没人看.
const staleFactor = 3

// minStaleSilence 再快的采集端, 也要静够这么久才算失联.
//
// 一个每秒一拉的桥接, 三秒不吭声完全可能只是一次 GC 或一次重连 ——
// 按纯倍数判会让它每天报警几十次.
const minStaleSilence = 90 * time.Second

func (h *heartbeats) stale(now time.Time) []StaleCollector {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []StaleCollector
	for src, c := range h.all {
		if c.pace <= 0 || c.last.IsZero() {
			continue // 没报过节奏的不判 —— 不知道它该多久一次
		}
		limit := time.Duration(staleFactor) * c.pace
		if limit < minStaleSilence {
			limit = minStaleSilence
		}
		if silent := now.Sub(c.last); silent > limit {
			out = append(out, StaleCollector{Source: src, Pace: c.pace, Silent: silent})
			continue
		}
		// 心跳还在跳, 但它一直拿不到数据 —— 用同一个门槛,
		// 理由也一样: 抖一次不该喊, 而喊多了跟不喊一样
		if !c.blindSince.IsZero() {
			if blind := now.Sub(c.blindSince); blind > limit {
				out = append(out, StaleCollector{Source: src, Pace: c.pace,
					Silent: blind, Blind: true, Why: c.why})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Silent > out[j].Silent })
	return out
}

// Heartbeat 采集端报到. pace 是它自己的节奏(多久一次)
func (b *SignalBus) Heartbeat(source string, pace time.Duration) {
	b.hb.beatWith(source, pace, b.now(), beatOK, "")
}

// HeartbeatAlive 只说"我还在" —— **不代表它拿得到数据**.
//
// 采集器每轮先报到再拉取, 用的就是这个: 拉不动的时候那句"我还在"
// 得先说出来, 但它不该顺手把"瞎了"的计时清零(那会让门槛永远到不了).
func (b *SignalBus) HeartbeatAlive(source string, pace time.Duration) {
	b.hb.beatWith(source, pace, b.now(), beatAlive, "")
}

// StaleCollectors 哪些采集端已经太久没消息了.
//
// **空不等于一切正常**: 从没报到过的来源不在这儿 —— 它可能根本就
// 没接上来, 那不是"它没了", 是"它从来没来过", 两件事的下一步不一样.
func (b *SignalBus) StaleCollectors() []StaleCollector {
	return b.hb.stale(b.now())
}

// NewlyStale 刚发现失联的那些 —— **取走就不再报**.
//
// 只报一次的代价是: 恢复时必须把"报过了"清掉(见 beat),
// 否则第二次失联就永远没人知道了 —— 那比一开始就不报更糟.
func (b *SignalBus) NewlyStale() []StaleCollector {
	now := b.now()
	all := b.hb.stale(now)
	b.hb.mu.Lock()
	var out []StaleCollector
	for _, s := range all {
		c := b.hb.all[s.Source]
		if c == nil || c.told {
			continue
		}
		c.told = true
		out = append(out, s)
	}
	b.hb.mu.Unlock()
	// **落账**: 校准报告要靠它算"这段账本里有多少时间是瞎的" ——
	// 不落的话报告会把一段瞎着的时间当成"很安静", 而安静率是整个
	// 感知层的验收判据(见 abi.EvCollectorDown)
	for _, s := range out {
		b.record(abi.EvCollectorDown, map[string]any{
			"source": s.Source, "why": s.Why, "blind": s.Blind,
			"silentMs": s.Silent.Milliseconds(),
		})
	}
	return out
}

// NewlyBack 刚回来的那些 —— 同样取走就不再报
func (b *SignalBus) NewlyBack() []string {
	b.hb.mu.Lock()
	var out []string
	for src, c := range b.hb.all {
		if c.back {
			c.back = false
			out = append(out, src)
		}
	}
	b.hb.mu.Unlock()
	sort.Strings(out)
	for _, src := range out {
		b.record(abi.EvCollectorUp, map[string]any{"source": src})
	}
	return out
}

// HeartbeatFailing 采集端报到, 但它拿不到数据. why 是它自己说的原因.
//
// **报到必须照打**: 不打的话这就退化成"失联", 而失联和"活着但瞎了"
// 的下一步不一样 —— 一个是去看进程还在不在, 一个是去看凭据.
func (b *SignalBus) HeartbeatFailing(source string, pace time.Duration, why string) {
	if why == "" {
		why = "(没说原因)"
	}
	b.hb.beatWith(source, pace, b.now(), beatFailing, why)
}

// Alive 这个采集端还在报到吗.
//
// ── 谁要它 ──
//
//	世界模型(见 World.alive). 采集端是**按移动触发**的 —— 人不动就不报,
//	于是一段沉默有两种解释: 他没动, 或者这个采集端死了.
//
//	分不出的话只能按最短的假设走(位置几分钟就过期), 而那的后果是
//	**人一坐下来 OS 就"忘了"他在哪** —— 而人不动恰恰是一天里的大部分时候.
//
//	心跳正好把两种解释分开: 它还在报到, 就说明没死; 那么它没报新位置,
//	就只能是人没挪.
//
// ── 从没报过节奏的一律算"不知道" ──
//
//	返回 false. 那时候我们没有依据说它活着 —— 而**在这件事上,
//	"不知道"要落到保守的那一侧**: 宁可让一条位置早点过期(用户再问一次),
//	也不要拿三小时前的位置自信地回答"你在公司".
func (b *SignalBus) Alive(source string) bool {
	return b.hb.aliveNow(source, b.now())
}

func (h *heartbeats) aliveNow(source string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.all[source]
	if !ok || c.pace <= 0 || c.last.IsZero() {
		return false
	}
	limit := time.Duration(staleFactor) * c.pace
	if limit < minStaleSilence {
		limit = minStaleSilence
	}
	return now.Sub(c.last) <= limit
}

// PaceOf / QuietOf 这个采集端自报的节奏、以及它安静了多久.
//
//	**给界面用的**: "还在报到"是一个布尔值, 而"它平时每 8 分钟一次,
//	已经 3 分钟没消息了"才让人判得出该不该担心.
func (b *SignalBus) PaceOf(source string) time.Duration {
	b.hb.mu.Lock()
	defer b.hb.mu.Unlock()
	if c, ok := b.hb.all[source]; ok {
		return c.pace
	}
	return 0
}

func (b *SignalBus) QuietOf(source string) time.Duration {
	b.hb.mu.Lock()
	defer b.hb.mu.Unlock()
	c, ok := b.hb.all[source]
	if !ok || c.last.IsZero() {
		return 0
	}
	return b.now().Sub(c.last)
}
