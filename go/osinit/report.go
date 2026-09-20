package osinit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neox-os/neox-os/abi"
	"time"
)

// 校准报告 —— 把"这套阈值配得对不对"从肉眼变成可测量.
//
// ── 为什么需要它 ──
//
// 感知层现在所有的数都是**按道理定的**: 窗口 5 分钟、每天打扰 3 次、
// 停留 120 米 / 5 分钟、数值传感器 5%. 一个都没有被真实的一天验证过.
//
// 而"跑一天然后调"这件事, 如果办法是翻 jsonl, 那实际上不会发生 ——
// 账本一天几千行, 人看不出"哪个源在刷屏"这种事.
//
// 更要命的是有些问题**只在统计上看得见**:
//
//	一个从来没有导致过任何通知的信号种类   → 它是纯噪音, 该在采集端砍掉
//	迟到率 30%                            → 容忍度配小了, 或者采集端时钟不对
//	去重率 40%                            → 采集端在盲目重传
//	空窗率 5%                             → **它太吵了**, 而验收判据要的是"大部分时候安静"
//
// 单看任何一条日志都发现不了这些.
//
// ── 报告要给判断, 不能只给数字 ──
//
// "迟到 312 条"这个数对读的人没有用, 他不知道 312 算多还是算少.
// 所以每一条都要带上**该怎么办**. 不给建议的报告等于没写.

// minWindowsToJudge 少于这么多窗口就不对安静率下判断.
//
// 20 个窗口在生产配置(5 分钟一个)下大约是一个半小时 —— 够看出趋势,
// 又不至于要等一整天才有第一份可信的报告.
const minWindowsToJudge = 20

// saidLine 一次开口
type saidLine struct {
	Verdict string
	Text    string
	Why     string
}

type kindStat struct {
	Kind    string
	Source  string
	N       int
	Late    int
	Dup     int
	Notices int // 这一类信号导致过几次通知
}

// Report 一段账本的统计.
type Report struct {
	// Problems 账本自身的矛盾(撤了没设过的、id 撞了、同一个 id 设两次).
	//
	// 这一项是"跑完一段真实使用之后逐条比对账本"那件事的自动版 ——
	// 对一次是运气, 每次都对才是性质.
	Problems []Problem
	// LedgerState 账本重放出来的现状 = 重启之后**应该**是什么样
	State     LedgerState
	Signals   int
	Late      int
	Duplicate int
	Digests   map[string]int // reason → 条数
	Notices   map[string]int // verdict → 条数
	// Said 它到底说了什么. **数量判不出"该不该说"** ——
	// 调阈值时人要判的正是这个, 而那些话就躺在账本里, 报告不给看的话
	// 每次都要手工 grep, 而"手工 grep"正是这份报告存在的理由
	Said  []saidLine
	Wakes int
	Kinds []kindStat
	// Windows 关过多少个窗口(= 有内容的窗口数). 用来算"安静率"
	Windows int

	// ── 这段账本里有多少时间是瞎的 ──
	//
	// **安静率是整个感知层的验收判据**, 而感知层可以整段整段地瞎掉
	// (采集端死了、令牌过期等情况都会造成这种结果).
	// 那段时间里一条信号都没有, 报告看到的是**满分的安静率**.
	// 拿这样一份账本去调阈值, 每个结论都是歪的, 而且歪得看不出来:
	// 数字很好看.
	BlindMs   int64
	SpanMs    int64  // 这份账本一共覆盖多长时间
	BlindWhy  string // 最后一次是为什么瞎的 —— 唯一能让人去修的线索
	BlindOpen bool   // 到账本结束都还没回来
}

// AnalyzeLedger 从事件日志算出报告.
//
// **只读账本, 不需要跑起来** —— 于是它可以对着任何一天的历史数据跑,
// 包括别人给的、包括昨天的.
func AnalyzeLedger(events []abi.Event) Report {
	r := Report{Digests: map[string]int{}, Notices: map[string]int{}}
	r.State, r.Problems = CheckLedger(events)
	byKind := map[string]*kindStat{}
	get := func(src, kind string) *kindStat {
		k := src + "|" + kind
		if s, ok := byKind[k]; ok {
			return s
		}
		s := &kindStat{Kind: kind, Source: src}
		byKind[k] = s
		return s
	}

	// 通知归因: 一条通知算在**它之前最近一个摘要**里出现过的那些 kind 上.
	//
	// 这是近似 —— 精确归因要 agent 申报"我是因为哪条说的", 而那正是
	// 不能交给它的判断(它会说自己想说的那个理由). 近似的方向是安全的:
	// 它可能把功劳算给同一批里的邻居, 但不会凭空给一个没出现过的 kind.
	var lastDigestKinds []*kindStat

	var blindFrom, firstAt, lastAt int64
	for _, e := range events {
		if e.At > 0 {
			if firstAt == 0 {
				firstAt = e.At
			}
			lastAt = e.At
		}
		m, _ := e.Payload.(map[string]any)
		switch e.Kind {
		case abi.EvSignal:
			r.Signals++
			src, _ := m["source"].(string)
			kind, _ := m["kind"].(string)
			get(src, kind).N++
		case abi.EvCollectorDown:
			// 只落状态变化, 不落心跳 —— 见 abi.EvCollectorDown
			if blindFrom == 0 {
				// **起点要往回推**: 事件是 OS **发现**的那一刻落的,
				// 而它比真正断掉的时刻晚一个门槛(至少 90 秒).
				// 直接用事件时间的话, 一次两分半的故障会被报成一分钟 ——
				// 而门槛那个量级恰恰是最常见的短故障的量级,
				// 于是短故障会被系统性地少算一半以上.
				blindFrom = e.At - asMillis(m["silentMs"])
				if blindFrom <= 0 {
					blindFrom = e.At
				}
				if w, _ := m["why"].(string); w != "" {
					r.BlindWhy = w
				}
			}
		case abi.EvCollectorUp:
			if blindFrom > 0 {
				r.BlindMs += e.At - blindFrom
				blindFrom = 0
			}
		case abi.EvSignalLate:
			r.Late++
			src, _ := m["source"].(string)
			kind, _ := m["kind"].(string)
			get(src, kind).Late++
		case abi.EvSignalDigest:
			reason, _ := m["reason"].(string)
			r.Digests[reason]++
			if reason == string(DigestWindow) || reason == string(DigestBatch) {
				r.Windows++
			}
			lastDigestKinds = nil
			// 摘要事件里没带 kind 明细(那会让账本翻倍), 所以归因只能
			// 落到"最近见过的那些信号"上 —— 见上面的说明
		case abi.EvInterrupt:
			v, _ := m["verdict"].(string)
			r.Notices[v]++
			// **正文也收起来** —— 数量判不出"该不该说"
			if t := str(m["text"]); t != "" {
				r.Said = append(r.Said, saidLine{v, t, str(m["why"])})
			}
			for _, s := range lastDigestKinds {
				s.Notices++
			}
		case abi.EvWakeFired:
			r.Wakes++
		}
	}

	// 去重在总线里是**直接返回**的, 不落事件 —— 所以账本里数不出来.
	// 这一点必须说出来, 而不是报一个 0 让人以为"没有重传".
	for _, s := range byKind {
		r.Kinds = append(r.Kinds, *s)
	}
	sort.Slice(r.Kinds, func(i, j int) bool {
		if r.Kinds[i].N != r.Kinds[j].N {
			return r.Kinds[i].N > r.Kinds[j].N
		}
		return r.Kinds[i].Kind < r.Kinds[j].Kind
	})
	r.SpanMs = lastAt - firstAt
	// **还没回来的要算到现在.**
	//
	// 只算"配对上的"那些, 会把"从昨天瞎到现在"这种最严重的情况算成零 ——
	// 而那恰恰是最该被喊出来的一种.
	if blindFrom > 0 {
		end := time.Now().UnixMilli()
		if lastAt > end {
			end = lastAt
		}
		r.BlindMs += end - blindFrom
		r.BlindOpen = true
		if r.SpanMs < end-firstAt {
			r.SpanMs = end - firstAt
		}
	}
	return r
}

// Text 报告正文. **每一条数字后面都要跟一句"该怎么办"** ——
// "迟到 312 条"对读的人没有用, 他不知道 312 算多还是算少.
func (r Report) Text() string {
	var b strings.Builder
	b.WriteString("── 感知层校准报告 ─────────────────────\n\n")

	if r.Signals == 0 {
		b.WriteString("账本里一条信号都没有。要么采集端没接上, 要么这份账本不是感知层的。\n")
		return b.String()
	}

	fmt.Fprintf(&b, "信号 %d 条 · 摘要 %d 份 · 通知 %d 次 · 闹钟响 %d 次\n\n",
		r.Signals, sumOf(r.Digests), sumOf(r.Notices), r.Wakes)

	// ── 按种类 ──
	b.WriteString("按种类(信号数 / 迟到 / 导致过几次通知):\n")
	for _, k := range r.Kinds {
		flag := ""
		// **从来没导致过任何通知的种类是纯噪音候选.**
		//
		// 判据不是"它少", 是"它从没让任何人做过任何事" ——
		// 一天两条但每次都值得说的信号是好信号; 一天两千条而
		// 一次通知都没引发的, 是在花钱买噪音.
		if k.Notices == 0 && k.N >= 20 {
			// **建议要指向一个动得了的地方.**
			//
			// 原来这里写的是"考虑在采集端就砍掉" —— 而采集端是装在
			// 别人手机里的 APK、或者别人跑着的 HA 桥, 那是个改不动的地方.
			// 于是这条建议永远只能看着, 报告到动作这一段是断的.
			flag = fmt.Sprintf("   ← 从没导致过通知。要让它闭嘴: /静音 %s/%s",
				k.Source, k.Kind)
		}
		if k.Late*2 > k.N && k.N >= 10 {
			flag = "   ← 一半以上迟到, 看采集端的时钟或上传周期"
		}
		fmt.Fprintf(&b, "  %-24s %5d  迟到 %4d  通知 %2d%s\n",
			k.Source+"/"+k.Kind, k.N, k.Late, k.Notices, flag)
	}

	// ── 这段账本里有多少时间是瞎的 ──
	//
	// **放在迟到率之前**: 后面所有的数字(安静率、每种信号多少条)
	// 都建立在"这段时间我们看得见"之上. 先说清楚看不见了多久,
	// 后面那些数字才知道该打几折.
	if r.BlindMs > 0 {
		pct := 0.0
		if r.SpanMs > 0 {
			pct = float64(r.BlindMs) / float64(r.SpanMs) * 100
		}
		fmt.Fprintf(&b, "\n⚠ 这段账本里有 %s(%.0f%%)感知层是**瞎的**",
			humanDuration(r.BlindMs), pct)
		if r.BlindOpen {
			b.WriteString("，而且到账本结束都没回来")
		}
		b.WriteString("\n")
		if r.BlindWhy != "" {
			fmt.Fprintf(&b, "  原因: %s\n", r.BlindWhy)
		}
		b.WriteString("  ← 那段时间一条信号都没有, **下面所有的数字都要打折看** ——\n" +
			"     尤其是安静率: 它会把'看不见'算成'很安静'\n")
	}

	// ── 迟到率 ──
	lateRate := float64(r.Late) / float64(r.Signals+r.Late) * 100
	fmt.Fprintf(&b, "\n迟到率 %.1f%%", lateRate)
	switch {
	case lateRate > 30:
		b.WriteString("  ← **太高**。绝大多数信号进了历史通道, 主动性基本失效。\n" +
			"           先查两件事: 容忍度是不是小于采集端的上传周期; 采集端时钟对不对\n")
	case lateRate > 5:
		b.WriteString("  ← 偏高。多半是某个采集端的上传周期接近容忍度\n")
	default:
		b.WriteString("  ← 正常\n")
	}

	// ── 安静率: 这是整个感知层的验收判据 ──
	//
	// **但它只是一半.** 一个从不开口的系统安静率满分, 而它没有任何用处;
	// 一个见门就喊的系统也能通过"该说的说了"那一半, 而它会在第一周
	// 就被关掉通知. 两边都要看:
	//
	//	该说的说了没有   —— 拿一天有事发生的活动去跑(HouseholdDay)
	//	不该说的忍住没   —— 拿一天全是琐事的去跑(QuietDay)
	//
	// 这份报告只量得到后一半(它看不出"哪件事本来该说") ——
	// 所以**安静率高不等于验收通过**, 这句话必须写在这儿, 否则
	// 一个坏掉的、永远沉默的系统会拿着满分的报告蒙混过去.
	if total := sumOf(r.Notices); r.Windows > 0 {
		quiet := float64(r.Windows-total) / float64(r.Windows) * 100
		if quiet < 0 {
			quiet = 0
		}
		fmt.Fprintf(&b, "安静率 %.0f%% (%d 个窗口里开口 %d 次)",
			quiet, r.Windows, total)
		switch {
		// ── 样本不够就不下判断 ──
		//
		// 这一条是拿真实账本跑出来的: 一份只有 4 个窗口的演示数据,
		// 报告当场断言"**它太吵了**" —— 而 4 个窗口里开口 1 次
		// 什么也说明不了.
		//
		// **给了没依据的建议, 跟不给建议一样等于没写**, 而且更糟:
		// 一个会因为四个数据点就喊狼来了的报告, 第三次之后就没人看了.
		case r.Windows < minWindowsToJudge:
			fmt.Fprintf(&b, "  ← 样本太少(不足 %d 个窗口), 先跑久一点再看这个数\n",
				minWindowsToJudge)
		case quiet < 80:
			b.WriteString("  ← **它太吵了**。验收判据是'大部分时候安静',\n" +
				"           不是'它做了多少事'。先看上面哪个种类在刷屏\n")
		default:
			b.WriteString("  ← 正常\n")
		}
		// **安静率高不等于验收通过.**
		//
		// 一个从不开口的系统安静率满分, 而它没有任何用处.
		// 这份报告只量得到一半 —— 它看不出"哪件事本来该说".
		// 不写这句的话, 一个坏掉的、永远沉默的系统会拿着满分的报告
		// 蒙混过去, 而那正是最难发现的一种坏.
		if total == 0 && r.Windows >= minWindowsToJudge {
			b.WriteString("  ← 注意: **一次都没开口不等于对**。一个坏掉的、\n" +
				"           永远沉默的系统也是这个数。这份报告看不出\n" +
				"           '哪件事本来该说' —— 那一半要拿一天有事发生的\n" +
				"           活动去验(见 sense.HouseholdDay)\n")
		}
	}

	// ── 打扰预算 ──
	if len(r.Notices) > 0 {
		fmt.Fprintf(&b, "\n通知去向: ")
		for _, v := range []NoticeVerdict{NoticeDelivered, NoticeBreakthrough,
			NoticeDeferred, NoticeDuplicate} {
			fmt.Fprintf(&b, "%s=%d ", v, r.Notices[string(v)])
		}
		b.WriteString("\n")
		// **它到底说了什么** —— 一次打扰值不值得, 只有看见那句话才判得出来
		if len(r.Said) > 0 {
			b.WriteString("\n它说了这些:\n")
			for _, sd := range r.Said {
				mark := " "
				switch NoticeVerdict(sd.Verdict) {
				case NoticeBreakthrough:
					mark = "!" // 破例: 它绕过了额度, 凭什么得说清楚
				case NoticeDeferred:
					mark = "·" // 攒着的: "它想说但没说"的那一批
				}
				fmt.Fprintf(&b, "  %s [%s] %s\n", mark, sd.Verdict, cut(sd.Text, 90))
				if sd.Why != "" {
					fmt.Fprintf(&b, "        理由: %s\n", cut(sd.Why, 80))
				}
			}
		}
		if d := r.Notices[string(NoticeDeferred)]; d > sumOf(r.Notices)/2 {
			b.WriteString("  ← 一半以上被攒下了。要么额度配小了, 要么它判断'值得说'\n" +
				"     的标准太松 —— 看日报里那些事你当时想不想知道\n")
		}
	}

	// ── 自洽性 ──
	//
	// **放在最后而且要显眼**: 前面那些数是给人调阈值的, 这一段是
	// "有没有坏". 坏了的话调阈值毫无意义.
	if len(r.Problems) > 0 {
		fmt.Fprintf(&b, "\n⚠ 账本里有 %d 处矛盾:\n", len(r.Problems))
		for _, p := range r.Problems {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	} else {
		fmt.Fprintf(&b, "\n自洽性: 没有矛盾。重放出来现在该有 %d 个闹钟 / "+
			"%d 条关注 / %d 个地点\n",
			len(r.State.Wakes), len(r.State.Watches), len(r.State.Places))
	}

	// ── 数不出来的东西也要说 ──
	//
	// 报一个 0 会让人以为"没有重传", 而真相是这个数根本没记.
	// 静默的空白比一个诚实的"数不出来"危险得多.
	b.WriteString("\n(去重次数账本里没有记 —— 总线判重之后直接返回, 不落事件。\n" +
		" 要看重传情况得在采集端那侧数)\n")
	return b.String()
}

func sumOf(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// cut 报告里给人看的一行不能太长 —— 一屏放不下的报告没人看完.
// (agent 包里有同名的, 但那是另一个包的私有工具, 复制一份比
// 为了省八行去动包边界划算)
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
