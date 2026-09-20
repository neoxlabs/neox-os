package osinit

import (
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 打扰预算 —— 主动智能能不能被容忍的关键.
//
// ── 为什么它必须在 OS 里, 而不是提示词里 ──
//
// 提示词里已经写了三条判据(不可逆 / 可行动 / 知情权), 它通常守得不错:
// 11 个摘要只开口 1 次. **但那是建议.**
//
// 这个仓库自己的教训写在 stall.go 里: "提示词只是建议, 模型卡住的时候
// 恰恰是它判断力最差的时候, 指望它自己看提示词不现实, 所以要在 loop 里硬拦."
// 打扰是同一类: 判断力一旦飘了, 后果不是多花几个 token, 是**用户把通知关掉**,
// 而通知一旦被关掉, 真正重要的那次也到不了他.
//
// 所以预算由 OS 记账, agent 花不起就得攒着.
//
// ── 破例资格不能让当事人自己定 ──
//
// 每个 agent 都会觉得自己的事最急 —— 跟"子 agent 预估不出自己的开销"
// 是同一类错. 所以"这条能不能突破额度"由**OS 按信号种类**判,
// 用的就是穿透表那一张(call.incoming / lock.opened / alarm.fired …).
// agent 在 why 里怎么写都不影响这个判定.
//
// ── 超额不是拒绝, 是延后 ──
//
// 直接丢掉是最糟的: 那条信息可能真的有用, 只是不够格**现在**打断你.
// 攒着 → 等你问、或者进日报. 于是预算管的是"什么时候说",
// 不是"说不说".

// Notice 一次打扰请求
type Notice struct {
	Text string
	Why  string
	At   int64
}

// NoticeVerdict 这条打扰的下场.
//
// 名字带 Notice 前缀是因为 policy.go 里已经有一个 Verdict(syscall 级的
// 放行/拒绝/问人). 撞名这件事本身说明 **Verdict 这个词太泛** ——
// 一个系统里可以有很多种"裁决", 光看类型名分不出是哪一层的.
type NoticeVerdict string

const (
	// NoticeDelivered 放行, 用户会看到
	NoticeDelivered NoticeVerdict = "deliver"
	// NoticeBreakthrough 破例放行(不可逆的事), 不占额度
	NoticeBreakthrough NoticeVerdict = "breakthrough"
	// NoticeDeferred 额度用完, 攒着 —— **不是丢掉**
	NoticeDeferred NoticeVerdict = "defer"
	// NoticeDuplicate 今天已经攒过一条一样的了
	NoticeDuplicate NoticeVerdict = "duplicate"
)

const (
	defaultPerDay = 3
	// maxDeferred 攒着的上限. 一天攒几百条的话, 日报本身就成了噪音.
	// **重放时守同一个上限** —— 不然一份大账本能让日报变成一堵墙
	maxDeferred = 50
	// urgentEcho 一条紧急摘要之后多久之内, 主动进程说的话算"因它而说".
	//
	// ── 为什么用时间关联, 而不是让 agent 声明 ──
	//
	// 理想情况是每条 notify 都带着"我是因为哪条摘要说的". 但那要求
	// agent 如实申报, 而这恰恰是不能交给它的那个判断(见上).
	//
	// 时间关联是**OS 单方面能算出来的**: 刚投过一条 lock.opened,
	// 紧接着它开口了, 那这句话就是因它而说. 会不会误判?
	// 会 —— 一条无关的话恰好在这个窗口里说出来就白得一次破例.
	// 但方向是安全的: 误判只发生在"刚刚真的出了不可逆的事"那几十秒里,
	// 而那时候多放一条比压一条强.
	urgentEcho = 90 * time.Second
)

// InterruptBudget 一天能打扰几次.
type InterruptBudget struct {
	// deliver 投递总线 —— 真送出去的那几条从这儿出去(见 notice.go).
	// 可以是 nil: 没接的机器行为跟以前一模一样
	deliver *Deliveries

	mu     sync.Mutex
	now    func() time.Time
	perDay int
	// day 当前是哪一天(本地时区的年月日). 跨天自动重置 ——
	// 用滑动 24 小时窗口的话, 昨晚的三次会一直压着今天上午
	day   string
	used  int
	spent []Notice
	// deferred 攒下的. **有界**: 一天攒几百条的话, 日报本身就成了噪音
	deferred []Notice
	// lastUrgentAt 最近一条紧急摘要投出去的时刻 —— 破例资格的依据
	lastUrgentAt int64
	log          *EventLog
}

/*
UseDeliveries 接上投递总线.

	**用 setter 不加构造参数**: NewInterruptBudget 有一堆调用点和测试,
	加一个参数就要全改一遍 —— 而那种改动里最容易出的错是位置串一格。
	没接的机器(测试里的那些)行为跟以前一模一样。
*/
func (b *InterruptBudget) UseDeliveries(d *Deliveries) { b.deliver = d }

func NewInterruptBudget(log *EventLog, perDay int, now func() time.Time) *InterruptBudget {
	if perDay <= 0 {
		perDay = defaultPerDay
	}
	if now == nil {
		now = time.Now
	}
	return &InterruptBudget{now: now, perDay: perDay, log: log}
}

// NoteUrgent 记下"刚投了一条紧急摘要". 由总线的投递方调用.
//
// 破例资格来自这里, 而不是来自 agent 的自述.
func (b *InterruptBudget) NoteUrgent(at int64) {
	b.mu.Lock()
	if at > b.lastUrgentAt {
		b.lastUrgentAt = at
	}
	b.mu.Unlock()
}

// Admit 这条打扰放不放行.
func (b *InterruptBudget) Admit(n Notice) NoticeVerdict {
	b.mu.Lock()
	defer b.mu.Unlock()
	nowT := b.now()
	if n.At == 0 {
		n.At = nowT.UnixMilli()
	}
	b.rollDay(nowT)

	// ── 破例 ──
	//
	// 刚出过不可逆的事(门锁、来电、警报), 这时候说的话不占额度.
	// 判据是 OS 手里的穿透表, 不是 agent 的说法.
	if b.lastUrgentAt > 0 && n.At-b.lastUrgentAt <= urgentEcho.Milliseconds() {
		b.spent = append(b.spent, n)
		b.record(NoticeBreakthrough, n, "紧接在一条紧急信号之后")
		return NoticeBreakthrough
	}

	// 今天已经攒过一条一样的, 不重复攒 —— 否则日报里会有五条"该吃药了"
	for _, d := range b.deferred {
		if sameNotice(d.Text, n.Text) {
			b.record(NoticeDuplicate, n, "今天已经攒过一条一样的")
			return NoticeDuplicate
		}
	}

	if b.used < b.perDay {
		b.used++
		b.spent = append(b.spent, n)
		b.record(NoticeDelivered, n, "")
		return NoticeDelivered
	}

	// 额度用完 —— **攒着, 不是丢掉**
	if len(b.deferred) < maxDeferred {
		b.deferred = append(b.deferred, n)
	}
	b.record(NoticeDeferred, n, "今天的打扰额度用完了")
	return NoticeDeferred
}

// SaidToday 今天已经说出口的那些.
//
// ── 为什么这个要给主动进程 ──
//
// 主动进程的上下文**线性无界增长**(40 份摘要 → prompt 从
// 1099 涨到 5112). 它每次只做一个判断, 却在积累几十份跟这次判断
// 无关的旧摘要 —— 而 S1 那条"把一条流塞进上下文, 模型看到的是噪音的海"
// 现在反过来咬它自己.
//
// 它真正需要的历史只有一条: **我今天说过什么**(提示词里写着
// "你已经说过的事再次发生不该再说"). 而那个 OS 手里就有.
//
// 把这条作为**事实**给它, 它就不再需要靠自己的上下文记住 ——
// 于是那个上下文可以被丢掉.
func (b *InterruptBudget) SaidToday() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay(b.now())
	out := make([]string, 0, len(b.spent))
	for _, n := range b.spent {
		out = append(out, n.Text)
	}
	return out
}

// SaidNote 贴给主动进程的一句话. 空的就不贴 —— 每份摘要多一段
// 没内容的话是纯噪音
func (b *InterruptBudget) SaidNote() string {
	said := b.SaidToday()
	if len(said) == 0 {
		return ""
	}
	return "\n\n（你今天已经跟他说过这些，**别重复**，除非这次真的不一样：" +
		strings.Join(said, "；") + "）"
}

// Deferred 取出攒着的(日报, 或者用户主动问的时候).
//
// 取走就清空: 说过一次就不该再说第二次.
//
// **取走这件事要落账.** 不落的话, 重放只看得见 defer 事件,
// 于是昨天日报里已经报过的那些会原样再报一遍 —— 而日报的全部价值
// 在于"一天一次、可预期", 重复的日报比不发更糟.
func (b *InterruptBudget) Deferred() []Notice {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.deferred
	b.deferred = nil
	if len(out) > 0 && b.log != nil {
		b.log.Append(signalPID, abi.EvInterrupt, map[string]any{
			"verdict": string(noticeFlushed), "n": len(out),
		})
	}
	return out
}

// noticeFlushed 攒着的被取走了(进了日报, 或者用户问了).
//
// 它不是一次打扰的下场, 所以不在 NoticeVerdict 那组常量里 ——
// 那组是"这条打扰放不放行", 而这条记的是"那些攒着的被交付了".
const noticeFlushed NoticeVerdict = "flushed"

// Restore 开机时把今天的额度和攒着的读回来.
//
// ── 不读的后果, 三条都很实 ──
//
//	① **额度归零**: 一天重启三次就打扰九次. 而 S23 已经证明这台机器
//	   经常被杀(开机扫出 199 个残留 socket). 后果不是多花几个 token,
//	   是用户把通知关掉 —— 通知一旦被关掉, 真正重要的那次也到不了他.
//	   那正是这个预算存在的全部理由.
//
//	② **"今天说过什么"全忘**: SaidToday 是 S22 建起来的事实,
//	   主动进程靠它才不用把几十份旧摘要背在上下文里. 一重启就没了,
//	   于是提示词里"你已经说过的事再次发生不该再说"落空 ——
//	   它会把门锁开了那条再说一遍.
//
//	③ **攒着的全丢**: "超额不是拒绝, 是延后"是这套设计唯一明确许下的
//	   承诺, 丢了就变成了静默丢弃 —— 而用户永远不会知道.
//
// ── 两条重放规矩 ──
//
// **额度和"说过什么"只算今天的**(按事件自己的时间判, 不是按现在),
// 跟 rollDay 同一条: 按自然日, 不是滑动 24 小时, 否则昨晚十点的三次
// 会一直压到今晚十点, 而用户的感受是"今天它一次都没提醒我".
//
// **攒着的跨天留着** —— 它们还没被说过, 跨天不是"过期"的理由.
func (b *InterruptBudget) Restore(events []abi.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	today := b.now().Format("2006-01-02")
	// **不清空**: 调用方是"每个桶喂一遍"的循环(账本按进程分桶,
	// timers/places/watches 都这么恢复, 因为"不假设它在哪个桶里").
	// 在这儿重置的话, sense 那个桶只要不是最后一个, 读到的东西
	// 就会被后面的空桶抹干净 —— 而它看起来完全正常.
	b.day = today

	for _, e := range events {
		if e.Kind != abi.EvInterrupt {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		v := NoticeVerdict(str(m["verdict"]))
		if v == noticeFlushed {
			// 取走过的不许复活 —— 已经进过日报了
			b.deferred = nil
			continue
		}
		text := str(m["text"])
		// 事件自己的时间才是"哪一天"的依据. 拿现在去判的话,
		// 一份从别的机器/别的日子拷回来的账本会被整段算成今天
		sameDay := time.UnixMilli(e.At).Format("2006-01-02") == today
		switch v {
		case NoticeDelivered:
			if sameDay {
				b.used++
				b.spent = append(b.spent, Notice{Text: text, At: e.At})
			}
		case NoticeBreakthrough:
			// 破例不占额度(当时就没占), 但它**说出去了** ——
			// 不算进"今天说过什么"的话, 重启后它会再说一遍门开了
			if sameDay {
				b.spent = append(b.spent, Notice{Text: text, At: e.At})
			}
		case NoticeDeferred:
			if len(b.deferred) < maxDeferred {
				b.deferred = append(b.deferred, Notice{Text: text, At: e.At})
			}
		}
	}
}

// Stats 今天用了几次 / 攒了几条 —— 这两个数要能被问出来.
//
// 只有它们能分开两种长得一样的情况: "今天很安静"和"额度早就用完了,
// 后面全在攒". 用户看到的都是"它没怎么说话".
func (b *InterruptBudget) Stats() (used, quota, deferred int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay(b.now())
	return b.used, b.perDay, len(b.deferred)
}

// rollDay 跨天重置. 调用方必须持锁.
//
// 按**自然日**而不是滑动 24 小时: 滑动窗口的话, 昨晚十点的三次
// 会一直压到今晚十点 —— 而用户的感受是"今天它一次都没提醒我".
func (b *InterruptBudget) rollDay(t time.Time) {
	d := t.Format("2006-01-02")
	if d != b.day {
		// 跨天时把没说的攒项留着(它们还没被说过), 只重置额度
		b.day, b.used, b.spent = d, 0, nil
	}
}

func (b *InterruptBudget) record(v NoticeVerdict, n Notice, why string) {
	if b.log == nil {
		return
	}
	/*
		真送出去的那两种才进投递总线.

			defer(攒着)和 duplicate(重复)的意思恰恰是"这次不打扰你" ——
			送出去的话整套打扰预算就白做了。
			flushed(攒的被取走了)也不送: 它已经进日报了, 再送一遍是两条。

			**破例的才 urgent**: 正常放行的是它判断值得说, 破例的是
			它判断值得**现在就说** —— 后者才配吵醒人。
	*/
	if b.deliver != nil && (v == NoticeDelivered || v == NoticeBreakthrough) {
		b.deliver.Post(Deliver{
			Kind:   DeliverProactive,
			Text:   n.Text,
			Why:    n.Why,
			Urgent: v == NoticeBreakthrough,
		})
	}
	b.log.Append(signalPID, abi.EvInterrupt, map[string]any{
		"verdict": string(v), "text": n.Text, "why": n.Why,
		"reason": why, "used": b.used, "quota": b.perDay,
		"deferred": len(b.deferred),
	})
}

// sameNotice 两条打扰是不是同一件事.
//
// 只做很粗的判断(去掉空白之后相等): 更聪明的相似度判断会引入
// "为什么这条被当成重复了"这种查不清的问题, 而**查不出来是最重的罪**.
func sameNotice(a, b string) bool {
	return strings.Join(strings.Fields(a), "") == strings.Join(strings.Fields(b), "")
}
