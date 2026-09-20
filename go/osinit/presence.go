package osinit

import (
	"fmt"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 家里现在有没有人 —— 判据要的上下文.
//
// ── 为什么需要它 ──
//
// S48 在真 HA 上重放了一天, 主动进程判了 15 次、一次都没开口.
// 其中有一件我认为它该说: **白天没人在家的时候, 门开了.**
//
// 而它不可能知道 —— OS 从来没告诉过它"家里没人". 它看到的只是
// lock.opened, 而门开了这件事一天发生好几次. 三条判据
// (不可逆 / 可行动 / 知情权)它一条也判不出来, 因为**判据要的上下文
// 根本不存在**.
//
// 这不是模型的问题, 是感知层缺一块: 手机那侧有"地点"(到家/离开公司),
// 家居这侧没有对应的"家里有没有人", 两边的信息各走各的.
//
// ── 做成环境事实, 不做成新工具 ──
//
// 跟"今天说过什么"一样: OS 手里有的东西, 贴给它就行,
// 不需要新系统调用、也不需要它自己去查. **能做成环境事实的,
// 就不要做成记忆.**
//
// ── 三条判据 ──
//
//	① "不知道"和"没人"必须分开
//	② 陈旧的位置不能当现在用
//	③ 来源要说出来 —— 判错时才查得到根

// presenceState 一个来源最近一次说了什么
type presenceState struct {
	home bool
	at   int64
	why  string // 谁说的
	// place 这条结论是从哪个**地点**推出来的. 空 = 不是从地点推的
	// (HA 的 person 实体自己就说 home/not_home).
	//
	//	**记下来是为了在前提消失时作废**: 用户删掉记错的"家"之后,
	//	"家里现在有人"这条推论还挂在那儿 —— 前提没了, 结论还在.
	//	而它不会报错, 只会一直答错. 见 Note() 里那次复查.
	place string
}

// Presence 家里有没有人.
type Presence struct {
	mu  sync.Mutex
	now func() time.Time
	// places 认得的地点 —— **手机只报经纬度, 地名要在这儿查**.
	//
	// 这条接缝差点是断的: S49 读的是 body["place"], 而手机采集器
	// 只报 lat/lon; 地名是 places.Annotate 在**摘要**上贴的,
	// 而这里挂在**投递路径**上, 那时候还没贴.
	// 于是 S49 只在我手工投一条带 place 的信号时成立 —— 真手机接上去,
	// "家里有没有人"永远是"不知道", 而且是**静默的**:
	// 系统照常跑, 只是那句话永远不出现.
	places *Places
	// by 按来源记 —— 手机一个、HA 的每个 person 一个.
	// **任何一个说"在家"就是有人**: 漏报一个人在家的代价(该说的没说)
	// 比误报小得多
	by map[string]presenceState
}

// staleAfter 位置多久不更新就不算数了.
//
// **S42/S43 已经证明采集端会整段整段地瞎掉**(手机没电、令牌过期).
// 拿一个三天前的"他离开了"当成"现在家里没人", 会让系统在他明明在家的
// 时候对着每一次开门喊警报 —— 而他没有任何办法知道为什么.
//
// 12 小时: 比一个工作日短一点. 再长的话"昨天出门"就能压到今天,
// 再短的话一次睡整觉(手机不动)就会让它变成"不知道".
const staleAfter = 12 * time.Hour

func NewPresence(now func() time.Time) *Presence {
	if now == nil {
		now = time.Now
	}
	return &Presence{now: now, by: map[string]presenceState{}}
}

// UsePlaces 装上地点表 —— 没有它就只认 body 里明说的地名
func (p *Presence) UsePlaces(pl *Places) {
	p.mu.Lock()
	p.places = pl
	p.mu.Unlock()
}

// Observe 每条信号看一眼. 跟 places.Observe 挂在同一条路上
func (p *Presence) Observe(s abi.Signal) {
	home, why, place, ok := p.read(s)
	if !ok {
		return
	}
	at := s.At
	if at <= 0 {
		at = s.KnownAt
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// 同一个来源只认更新的那条 —— 补传的旧位置不该盖掉新的
	if cur, had := p.by[s.Source]; had && cur.at > at {
		return
	}
	p.by[s.Source] = presenceState{home: home, at: at, why: why, place: place}
}

// readPresence 这条信号说没说"某人在家/不在家".
//
// 两条来路: 手机的地点信号(place.arrived/left + 地点叫"家"),
// 和 HA 的 person/device_tracker 实体(state = home/not_home).
// **不是每个人都装我们的手机端**, 所以两条都要认.
func (p *Presence) read(s abi.Signal) (home bool, why, place string, ok bool) {
	place, _ = s.Body["place"].(string)
	if place == "" {
		// **手机只报经纬度** —— 地名在这儿查. 查不到就是查不到,
		// 不能拿一个随便的坐标当"家"(那样第一次出门就会误报)
		place = p.lookupPlace(s)
	}
	switch s.Kind {
	case "place.arrived":
		if isHomePlace(place) {
			return true, "手机说你到家了", place, true
		}
	case "place.left":
		if isHomePlace(place) {
			return false, "手机说你离开家了", place, true
		}
	}
	// HA 那侧: person.xxx / device_tracker.xxx
	entity, _ := s.Body["entity"].(string)
	state, _ := s.Body["state"].(string)
	if hasPrefix(entity, "person.") || hasPrefix(entity, "device_tracker.") {
		switch state {
		case "home":
			return true, "HA 的 " + entity + " 说在家", "", true
		case "not_home", "away":
			return false, "HA 的 " + entity + " 说不在家", "", true
		}
	}
	return false, "", "", false
}

func isHomePlace(p string) bool { return p == "家" || p == "home" }

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// Note 贴给主动进程的一句话. **不知道就返回空** ——
// 编一句"家里没人"的代价是每次开门都变成警报
func (p *Presence) Note() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	cut := now.Add(-staleAfter).UnixMilli()

	var anyFresh bool
	var newest presenceState
	for _, st := range p.by {
		if st.at < cut {
			continue // 陈旧的不当现在用
		}
		// ── 前提没了, 结论也不算 ──
		//
		//	用户删掉一个记错的"家"之后, "家里现在有人"这条推论**还在**:
		//	它是几小时前从那个地点推出来的, 而删地点不会回头去改它.
//	地点表里一条都没有了, 而它还在说
		//	"家里现在有人：手机说你到家了".
		//
		//	**读的时候复查一遍**, 而不是删的时候去改这里: 同一份判断
		//	只有一处, 而且自愈 —— 他重新教一个"家", 这条又成立了
		if st.place != "" && p.places != nil && !p.places.Has(st.place) {
			continue
		}
		anyFresh = true
		// **任何一个说"在家"就是有人** —— 漏报"有人在家"的代价
		// (该说的没说)比误报小得多
		if st.home {
			newest = st
			return fmt.Sprintf("\n\n（家里现在有人：%s）", newest.why)
		}
		if st.at > newest.at {
			newest = st
		}
	}
	if !anyFresh {
		return "" // 不知道就闭嘴
	}
	return fmt.Sprintf("\n\n（**家里现在没人**：%s，%s前）",
		newest.why, humanDuration(now.UnixMilli()-newest.at))
}

// lookupPlace 拿信号里的经纬度去地点表里查地名. 调用方不持锁
func (p *Presence) lookupPlace(s abi.Signal) string {
	p.mu.Lock()
	pl := p.places
	p.mu.Unlock()
	if pl == nil {
		return ""
	}
	lat, ok1 := floatOf(s.Body["lat"])
	lon, ok2 := floatOf(s.Body["lon"])
	if !ok1 || !ok2 {
		return ""
	}
	return pl.Lookup(lat, lon)
}

// Restore 开机时把"家里有没有人"读回来.
//
// ── 不读的后果 ──
//
// 这个状态是从信号**流过时**攒出来的(Observe). 而重启之后,
// RecoverPending 只捞"最后一份摘要之后"的信号 —— 三小时前那条
// "离开家了"早就被总结过了, 不会重放.
//
// 于是升级/崩溃/断电之后, "家里有没有人"回到"不知道",
// 那件★(没人在家时门开了)不会被说 —— **而 S49 的全部价值就在那一件上**.
// 症状照例是静默的: 系统照常跑, 只是那句话不再出现.
//
// 这是"重启之后没被恢复的状态"的**第四例**(S26 额度、S30 静音表、
// S31 去重表、现在这一个). 而 S32 那道闸看不出来 —— 它查的是
// "每种事件有没有人读", 而 place.left 是 EvSignal、有人读;
// **漏的是"从事件推导出来的状态有没有人恢复"**.
//
// 陈旧那条判据照样管用: 走的是同一个 Note(), 三天前的位置恢复回来
// 也还是"不知道" —— 重启不该让一条过期的位置突然变得可信.
func (p *Presence) Restore(events []abi.Event) {
	for _, e := range events {
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
		// **走同一条 Observe** —— "同一个来源只认更新的那条"这类判据
		// 只有一份, 重放和实时用的是同一套(重写一遍必然漂移)
		p.Observe(s)
	}
}
