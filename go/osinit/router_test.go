package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func routerParts(t *testing.T) (*Places, *Watches, *InterruptBudget) {
	t.Helper()
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	p := NewPlaces(log)
	p.Add("公司", 31.86, 117.28, 0)
	return p, NewWatches(log), NewInterruptBudget(log, 3, nil)
}

func arrivedDigest() Digest {
	now := time.Now().UnixMilli()
	return Digest{Reason: DigestWindow, From: now, To: now, Count: 1,
		Items: []DigestItem{{Source: "phone.mk", Kind: "place.arrived", N: 1,
			First: now, Last: now,
			Sample: map[string]any{"lat": 31.86, "lon": 117.28, "acc": 5.0}}}}
}

// **投递之前坐标要换成地名.**
//
// 模型对 "lat=31.86001" 能做的判断为零 —— 它不知道那是公司、是家、
// 还是医院, 于是三条判据一条都过不了.
func TestRouterAnnotatesBeforeDelivering(t *testing.T) {
	p, w, b := routerParts(t)
	var sent string
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		ToProcess: func(text string) bool { sent = text; return true }}
	r.Route(arrivedDigest())

	if !strings.Contains(sent, "公司") {
		t.Fatalf("投给主动进程的文本里没有地名, 模型判不出任何东西:\n%s", sent)
	}
}

// **用户明确要盯的事直接说, 不占额度、也不过模型.**
//
// 跟闹钟一个道理: 预算防的是"它没事找事", 而这件事是他自己让做的.
// 一个他明确要求的通知被额度挡下来, 他会觉得系统坏了.
func TestWatchHitsBypassBudgetAndModel(t *testing.T) {
	p, w, b := routerParts(t)
	w.Add("place.arrived", "公司", "", "到公司告诉我")
	for i := 0; i < 3; i++ { // 额度先用光
		b.Admit(Notice{Text: string(rune('a' + i))})
	}
	var said []string
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		OnWatchHit: func(s string) { said = append(said, s) },
		ToProcess:  func(string) bool { return true }}
	r.Route(arrivedDigest())

	if len(said) != 1 {
		t.Fatalf("额度用光之后, 用户明确要盯的事被挡下来了(说了 %d 次)", len(said))
	}
	if used, _, _ := b.Stats(); used != 3 {
		t.Fatalf("关注命中占掉了额度(用了 %d 次, 该还是 3)", used)
	}
}

// **破例资格由 OS 判, 不问 agent.**
//
// 每个 agent 都觉得自己的事最急. 刚投过一条紧急摘要这件事,
// 只有 OS 知道 —— 所以它要在这儿记下来.
func TestUrgentDigestGrantsBreakthrough(t *testing.T) {
	p, w, b := routerParts(t)
	for i := 0; i < 3; i++ {
		b.Admit(Notice{Text: string(rune('a' + i))}) // 额度用光
	}
	d := arrivedDigest()
	d.Reason = DigestUrgent
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		ToProcess: func(string) bool { return true }}
	r.Route(d)

	if v := b.Admit(Notice{Text: "门锁开了"}); v != NoticeBreakthrough {
		t.Fatalf("紧急摘要之后紧接着的那句话判成了 %s —— 破例资格没记上", v)
	}
}

// **主动进程没起来时要摊在终端上, 不能静默丢.**
//
// 静默丢的话, "今天很安静"和"主动进程崩了"长得一模一样.
func TestFallsBackToTerminalWhenNoProcess(t *testing.T) {
	p, w, b := routerParts(t)
	var onScreen string
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		ToProcess: func(string) bool { return false }, // 进程没起来/收不下
		ToTerminal: func(d Digest, text string) {
			// 兜底那行要说清是哪种摘要、几条信号 —— 走到这条路的时候
			// 主动进程正好是坏的, 那是最需要线索的时刻
			onScreen = string(d.Reason) + "|" + text
		}}
	r.Route(arrivedDigest())

	if !strings.Contains(onScreen, "公司") {
		t.Fatalf("主动进程收不下时这份摘要被静默丢了 —— " +
			"'今天很安静'和'主动进程崩了'从此长得一样")
	}
	if !strings.HasPrefix(onScreen, string(DigestWindow)) {
		t.Fatalf("兜底那行没说清是哪种摘要: %s", onScreen)
	}
}

// 回收: 每 N 份摘要换一个主动进程 —— 它的上下文是线性无界增长的(S22).
// **只有真收下了才算一份**, 否则进程没起来时会把回收计数空转掉
func TestRecyclesOnlyAfterAcceptedDigests(t *testing.T) {
	p, w, b := routerParts(t)
	recycled := 0
	accept := true
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		RecycleEvery: 2,
		ToProcess:    func(string) bool { return accept },
		ToTerminal:   func(Digest, string) {},
		Recycle:      func() { recycled++ }}

	accept = false
	r.Route(arrivedDigest())
	r.Route(arrivedDigest())
	if recycled != 0 {
		t.Fatalf("没人收下也在数, 回收了 %d 次", recycled)
	}
	accept = true
	r.Route(arrivedDigest())
	r.Route(arrivedDigest())
	if recycled != 1 {
		t.Fatalf("收下两份之后回收了 %d 次, 该是 1 次", recycled)
	}
}

// 一条 Observe 的路: 每条信号都要让 places 看一眼(好让用户能说"这儿是公司"),
// 再写一行进可读视图(好让用户能问"我今天去过哪儿")
func TestObserveFeedsPlacesAndView(t *testing.T) {
	p, _, _ := routerParts(t)
	seen := 0
	obs := ObserveChain(p, func(abi.Signal) { seen++ })
	obs(abi.Signal{ID: "s1", Source: "phone.mk", Kind: "phone.moved",
		Body: map[string]any{"lat": 31.86, "lon": 117.28}})
	if seen != 1 {
		t.Fatal("可读视图那一路没被喂到 —— 用户问'我今天去过哪儿'会答不出来")
	}
}

// **"家里有没有人"要跟着摘要走.**
//
// S48 量出来的缺口: 主动进程看到 lock.opened, 判不出这是不是要紧的事,
// 因为它不知道家里有没有人 —— 而 OS 知道.
func TestPresenceRidesAlongWithTheDigest(t *testing.T) {
	p, w, b := routerParts(t)
	pres := NewPresence(time.Now)
	pres.Observe(abi.Signal{ID: "l1", Source: "phone.mk", Kind: "place.left",
		At:   time.Now().Add(-2 * time.Hour).UnixMilli(),
		Body: map[string]any{"place": "家"}})

	var sent string
	r := DigestRouter{Places: p, Watches: w, Budget: b, Presence: pres,
		ToProcess: func(text string) bool { sent = text; return true }}
	r.Route(arrivedDigest())

	if !strings.Contains(sent, "没人") {
		t.Fatalf("摘要里没带上'家里没人'这个事实:\n%s", sent)
	}
}

// **主动进程死了之后, 没有人把它拉起来.**
//
// 它会死: 撞止损线(每进程 200 万 token)、崩溃、被 OOM 杀、机器换页.
// 死了之后 o.Send 永远失败 —— 摘要**从此只会摊在终端上**,
// 而人不在终端边的时候等于全丢了.
//
// 更糟的是它是**永久的**: 回收只在"投递成功够 N 次"之后触发,
// 而投递已经永远失败, 所以那条路也走不到. 感知层降级成一个
// 只会往空气里打印的东西, 而且没有任何一处会说一声.
func TestDeadProactiveProcessGetsRespawned(t *testing.T) {
	p, w, b := routerParts(t)
	alive := false
	respawns := 0
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		ToProcess:  func(string) bool { return alive },
		ToTerminal: func(Digest, string) {},
		Respawn: func() bool {
			respawns++
			alive = true // 拉起来了
			return true
		}}
	r.Route(arrivedDigest())

	if respawns != 1 {
		t.Fatalf("投不进去却没试着拉起来(%d 次) —— "+
			"感知层从此只会往空气里打印", respawns)
	}
}

// **拉起来之后要把这一份补投进去** —— 不然那一份就白丢了,
// 而它可能正是要紧的那一条
func TestRespawnRedeliversThatDigest(t *testing.T) {
	p, w, b := routerParts(t)
	alive := false
	var got string
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		ToProcess: func(text string) bool {
			if !alive {
				return false
			}
			got = text
			return true
		},
		ToTerminal: func(Digest, string) {},
		Respawn:    func() bool { alive = true; return true }}
	r.Route(arrivedDigest())

	if got == "" {
		t.Fatal("拉起来了却没把这一份补投进去 —— 那一份白丢了")
	}
}

// **拉不起来就摊在终端上, 而且只试一次** ——
// 每份摘要都试一遍等于把一个坏掉的东西反复重启, 而日志会被刷满
func TestRespawnFailureFallsBackToTerminal(t *testing.T) {
	p, w, b := routerParts(t)
	tries, onScreen := 0, 0
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		ToProcess:  func(string) bool { return false },
		ToTerminal: func(Digest, string) { onScreen++ },
		Respawn:    func() bool { tries++; return false }}
	r.Route(arrivedDigest())
	r.Route(arrivedDigest())

	if onScreen != 2 {
		t.Fatalf("拉不起来时没摊在终端上(%d 次)", onScreen)
	}
	if tries != 2 {
		t.Fatalf("试了 %d 次 —— 每份摘要该试一次, 而不是一次都不试或者反复试", tries)
	}
}

// **重拉"成功"了但投递还是失败 —— 那一份必须摊到终端上.**
//
// 整条链自跑时撞到的: 主动进程因 cgroup 没权限起来就死, 重拉的那个
// 也一样 —— 而 Send 在它**还没来得及死**的那几十毫秒里返回成功,
// 于是摘要被投进一个马上就要死的进程的收件箱: **既不到主动进程,
// 也不到终端**. 比 S61 之前更糟(之前至少摊在终端上).
func TestRespawnedButStillUndeliverableFallsBack(t *testing.T) {
	p, w, b := routerParts(t)
	onScreen := 0
	r := DigestRouter{Places: p, Watches: w, Budget: b,
		ToProcess:  func(string) bool { return false }, // 拉起来了也投不进去
		ToTerminal: func(Digest, string) { onScreen++ },
		Respawn:    func() bool { return true }} // 它说拉起来了
	r.Route(arrivedDigest())

	if onScreen != 1 {
		t.Fatalf("重拉说成功了、投递却失败, 那一份既没到进程也没到终端(%d) —— "+
			"比不重拉更糟", onScreen)
	}
}
