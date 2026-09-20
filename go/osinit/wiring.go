package osinit

import (
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 感知层的接线 —— **一处装好, 两处用**(宿主和测试).
//
// ── 为什么值得单独拎出来 ──
//
// "组件全测过, 把它们连起来的那截线没人管"已经出过三次:
//
//	S33  摘要投递的五步活在一个闭包里, 零测试
//	S37  失败通知送给了 current, 也就是**另一段对话**
//	S56  Presence 读 body["place"], 而手机只报经纬度 —— 真手机上那条路是断的
//
// 三次的形状一样: **每个组件自己都对, 而且接口对得上(都是 abi.Signal),
// 所以编译器什么都不说**. 错的是没有人把它们接起来, 症状全是静默的.
//
// 而"写条端到端测试"本身治不了这件事: 测试里再接一遍的话, 测的是
// **测试里那一份接线**, 宿主那份照样可以是错的. 所以接线必须只有一份,
// 两边都从这儿拿.

// SenseStack 接线要的零件. 允许缺 —— 缺哪个就少哪一段能力
type SenseStack struct {
	Log     *EventLog
	Places  *Places
	Watches *Watches
	Budget  *InterruptBudget

	Window        time.Duration
	Lateness      time.Duration
	MaxBatch      int
	BackfillQuiet time.Duration
	UrgentGrace   time.Duration

	// ToProcess 投给主动进程, 返回它收没收下
	ToProcess func(text string) bool
	// ToTerminal 收不下时摊在终端上
	ToTerminal func(d Digest, text string)
	// OnWatchHit 用户明确要盯的事命中了
	OnWatchHit func(line string)
	// OnSignal 每条信号还要给谁看一眼(可读视图之类)
	OnSignal func(abi.Signal)
	// OnStale / OnBack 采集端失联/回来
	OnStale func([]StaleCollector)
	OnBack  func([]string)

	// Respawn 投不进去时把主动进程重新拉起来 —— 见 DigestRouter.Respawn
	Respawn func() bool

	RecycleEvery int
	Recycle      func()
}

// Wired 接好的一套
type Wired struct {
	Bus      *SignalBus
	Router   *DigestRouter
	Presence *Presence
}

// BuildSenseStack 把零件接起来.
//
// **顺序和依赖都在这儿定死**, 调用方没有机会漏:
//
//	presence 要地点表   手机只报经纬度, 地名在这儿查(S56 那次断的就是它)
//	观察链要带上 places 和 presence  —— 每条信号都得让它们看一眼
//	router 要 presence  —— "家里有没有人"是判据要的上下文(S49)
func BuildSenseStack(s SenseStack) Wired {
	presence := NewPresence(nil)
	presence.UsePlaces(s.Places)

	router := &DigestRouter{
		Places: s.Places, Watches: s.Watches, Budget: s.Budget,
		Presence:     presence,
		ToProcess:    s.ToProcess,
		ToTerminal:   s.ToTerminal,
		OnWatchHit:   s.OnWatchHit,
		Respawn:      s.Respawn,
		RecycleEvery: s.RecycleEvery,
		Recycle:      s.Recycle,
	}
	more := []func(abi.Signal){presence.Observe}
	if s.OnSignal != nil {
		more = append(more, s.OnSignal)
	}
	bus := NewSignalBus(s.Log, router.Route, SignalOptions{
		Window: s.Window, Lateness: s.Lateness, MaxBatch: s.MaxBatch,
		BackfillQuiet: s.BackfillQuiet, UrgentGrace: s.UrgentGrace,
		Observe: ObserveChain(s.Places, more...),
		// 落账本的那份收一收 —— **精确值活在内存里, 跟着进程一起死**.
		//
		//	接在这儿而不是让调用方各自接: 这是一条安全边界, 而安全边界
		//	不能靠"每个调用方记得加一行". 忘了加的那台机器上, 一份
		//	逐分钟的行踪会安静地攒一年.
		Redact:  RedactForLedger(s.Places),
		OnStale: s.OnStale, OnBack: s.OnBack,
	})
	return Wired{Bus: bus, Router: router, Presence: presence}
}
