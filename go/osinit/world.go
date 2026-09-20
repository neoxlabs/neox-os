package osinit

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 世界模型 —— **此刻的事实**, 不是流水.
//
// ── 为什么非有这一层不可 ──
//
//	感知层整套是一条**流**: 信号进来、聚成摘要、推给判断者. 于是 bot
//	永远是被动收 —— 它**问不了**.
//
//	而一个助理一半的问题是查询式的: "我现在在哪""家里有人吗"
//	"外面下雨吗""我下一个提醒是什么". 这些不是事件, 是状态.
//	拿流去答的话只有两条路, 而两条都是错的:
//
//	  把整条流塞进上下文    模型看到的是噪音的海, 判断力会下降(S1)
//	  让 bot 自己攒         状态放两处必然对不上, 而对不上是静默的
//
//	所以: **从账本投影出现状, 谁要谁来查**. 投影不另存真相 ——
//	它是账本的一个视图, 重启从账本重建, 跟内存里那份永远一致.
//
// ── 一条事实必须带有效期 ──
//
//	这是整个模型的关键, 也是最容易漏的一条: **天气一小时后就是废话,
//	位置五分钟后就不可信, 而"这儿是家"永远成立**.
//
//	没有有效期的话, 世界模型会拿三小时前的位置回答"我现在在哪" ——
//	而那个答案跟"不知道"相比是更糟的: 不知道会让人再问一次,
//	一个自信的错误答案不会.
//
//	所以过期的事实**不是降权, 是不再回答**. 要么说得出, 要么说不知道.

// Fact 一条现状.
type Fact struct {
	// Key 说的是什么 —— 信号的 kind, 或者 OS 自己推出来的那几条
	Key string `json:"key"`
	// Text 给人(和模型)看的一句话. **不是原始值**:
	// "在公司"而不是 31.86,117.28; "睡得不好"而不是整夜心率序列
	//
	// ── 它是**读的时候才算**的 ──
	//
	//	地名是逆地理编码后台查回来的(不能在信号路径上同步调, 见
	//	sense/nearby.go). 在信号进来那一刻算的话, 那时候缓存还是空的,
	//	于是这句话永远定死成"在一个还没起过名的地方" —— 而地名两秒后
	//	就到了, 只是再也没人算第二遍.
	//
	//	如果只在信号进入时计算, 地名缓存更新后这句话仍然不会改变.
	Text string `json:"text"`
	// Who 这是**谁的**事实. 空 = 屋子的(天气、门、客厅有没有人)
	Who string `json:"who,omitempty"`
	// Source 谁说的. 事实对不上的时候, 这是唯一能查下去的东西
	Source string `json:"source,omitempty"`
	// At 什么时候知道的
	At int64 `json:"at"`
	// Fields 原始字段 —— **给规则用的, 不给模型看**.
	//
	//	Text 是给人和模型看的结论("下午 3 点前后有雨"), 而条件触发
	//	要判的是一个数(rainProb > 60). 拿正则去匹配那句中文的话,
	//	换一句措辞规则就静默失效了.
	Fields map[string]any `json:"fields,omitempty"`
	// TTL 多久之后这条就不算数了. 0 = 永远成立(比如"这儿叫家")
	TTL time.Duration `json:"-"`
	// kind 原始的信号种类 —— 读的时候拿它重算那句话
	kind string
}

// Fresh 这条现在还算不算数 —— **只看它自己的有效期**.
//
//	World 那边还会再看一眼报它的采集端活着没有(见 World.fresh):
//	一个按移动触发的采集端只要还在报到, 它报的最后一个位置就仍然成立
func (f Fact) Fresh(now time.Time) bool {
	if f.TTL == 0 {
		return true
	}
	return now.Sub(time.UnixMilli(f.At)) <= f.TTL
}

// aliveGrace 采集端活着的时候, 一条事实最多还能算多久.
//
//	**上限仍然要有**: 一个活着的采集端也可能是"活着但瞎了"(定位权限被
//	撤了、GPS 关了) —— 它照样报到, 只是再也不报位置. 没有上限的话,
//	昨天的位置会一直被当成现在的.
const aliveGrace = 12 * time.Hour

// fresh 这条事实现在还算不算数.
//
//	比 Fact.Fresh 多看一眼: 报它的那个采集端还活着吗.
//	活着的话, 它没报新的就说明**没有新的可报** —— 见 World.alive.
func (w *World) fresh(f Fact, now time.Time) bool {
	if f.Fresh(now) {
		return true
	}
	if w.alive == nil || f.Source == "" || !w.alive(f.Source) {
		return false
	}
	return now.Sub(time.UnixMilli(f.At)) <= aliveGrace
}

// ── 每类事实活多久 ──
//
//	**这张表是"能不能拿它回答问题"的判据, 不是缓存策略.**
//
//	数字都往短里给: 说不知道的代价是用户再问一次或者自己看一眼,
//	而拿一条过期事实自信地答错的代价, 是他从此不再信这套东西.
var factTTL = map[string]time.Duration{
	// 位置: 人走得快. 十分钟前在哪儿, 跟现在在哪儿是两个问题
	"place":    10 * time.Minute,
	"location": 10 * time.Minute,
	// 天气: 一小时一变, 而"现在要不要带伞"问的就是现在
	"weather": time.Hour,
	// 生理: 心率是瞬时的, 睡眠是昨晚的结论
	"heartrate": 15 * time.Minute,
	"sleep":     20 * time.Hour,
	// 设备状态: 门开着就一直开着, 直到有人关 —— 这类是**状态**不是采样,
	// 下一条同类信号才会推翻它
	"door":     0,
	"lock":     0,
	"presence": 0,
	"battery":  2 * time.Hour,
	// 日程 / 待办: 到点之前一直成立
	"calendar": 12 * time.Hour,
	"todo":     12 * time.Hour,
}

// factKind 一条信号算哪一类事实.
//
//	**同一件事的几种叫法要合成一个**: 采集端报 location(定期的位置),
//	也报 place.arrived / place.left(到了/离开). 三个都是"他在哪" ——
//	不合并的话世界模型里会同时挂着两条位置, 而它们互相矛盾的时候
//	没有任何一处说得清该信哪个.
//
//	否则"它现在知道什么"里会连着两行"在一个还没起过名的地方".
func factKind(kind string) string {
	head := kind
	if i := strings.Index(kind, "."); i > 0 {
		head = kind[:i]
	}
	if head == "location" {
		return "place"
	}
	return head
}

// ttlFor 按 kind 的**第一段**查 —— place.arrived 和 place.left 同一类.
//
//	查不到给一个短的缺省: 一个 OS 不认得的新 kind, 拿它去回答问题
//	本来就该更谨慎
func ttlFor(kind string) time.Duration {
	head := kind
	if i := strings.Index(kind, "."); i > 0 {
		head = kind[:i]
	}
	if d, ok := factTTL[head]; ok {
		return d
	}
	return 30 * time.Minute
}

// World 此刻的世界.
//
// ── 事实分两种 ──
//
//	**某个人的**   位置、心率、他的日程. 键是 (谁, 什么).
//	**屋子的**     天气、门开着没有、客厅有没有人. 键是 (空, 什么).
//
//	不分的话, 两个人的位置会互相盖掉 —— 而症状不是报错, 是**答错**:
//	他问"我在哪", 系统拿她十分钟前的位置回答, 而且答得很有把握.
//
//	归属从**信号是从哪台设备来的**推出来(见 Devices.Owner).
//	家里的传感器故意没有主人, 于是它们的事实屋里每个人都看得见.
type World struct {
	mu    sync.Mutex
	facts map[factKey]Fact
	now   func() time.Time

	places   *Places
	presence *Presence
	timers   *Timers
	// alive 这个采集端还活着吗 —— 通常接 SignalBus 的心跳表.
	//
	// ── 为什么有效期要看它 ──
	//
	//	采集端是**按移动触发**的(人不动就不报, 见 Collector.kt).
	//	于是"一段沉默"有两种解释: 他没动, 或者这个采集端死了.
	//
	//	分不出就只能按最短的假设走(位置 10 分钟过期), 而那的后果是:
	//	**人一坐下来, OS 就"忘了"他在哪** —— 而人不动恰恰是一天里的
	//	大部分时候, "它现在知道什么"里会一条位置都没有.
	//
	//	心跳正好把这两种解释分开了: 它还在报到, 就说明它没死 ——
	//	那么它没报新位置, 就只能是人没挪.
	alive func(source string) bool
	// nearby 说不出地名时兜底问一句"这儿大概是哪儿".
	//
	//	**只读缓存, 从不阻塞**: describe 跑在总线的同步观察链上,
	//	在那儿发一个 HTTP 请求的话, 每一条位置信号都要等一次网络往返 ——
	//	而手机补传时是一次几百条. 见 sense/nearby.go
	nearby func(lat, lon float64) (string, bool)
	// owner 这条信号是谁的 —— 通常接 Devices.OwnerOf
	owner func(source string) string
}

// factKey 谁的 + 什么. who 空 = 屋子的
type factKey struct {
	who  string
	what string
}

func NewWorld(now func() time.Time) *World {
	if now == nil {
		now = time.Now
	}
	return &World{facts: map[factKey]Fact{}, now: now}
}

// Use 把能回答问题的那几样接上. 允许缺 —— 缺哪个就少答哪一类问题
func (w *World) Use(p *Places, pr *Presence, t *Timers) {
	w.places, w.presence, w.timers = p, pr, t
}

// UseOwner 接上"这条信号是谁的". 不接 = 所有事实都算屋子的 ——
// 一个人住的机器上那是对的
func (w *World) UseOwner(f func(source string) string) { w.owner = f }

// UseNearby 接上"这儿大概是哪儿"的兜底. 不接就照实说"还没起过名"
func (w *World) UseNearby(f func(lat, lon float64) (string, bool)) { w.nearby = f }

// UseAlive 接上"这个采集端还活着吗" —— 见 World.alive.
// 不接的话一律按固定有效期判, 那是更保守也更常错的那一种
func (w *World) UseAlive(f func(source string) bool) { w.alive = f }

// Observe 每条信号都让它看一眼 —— 挂在总线的观察链上.
//
//	**只留同一类的最新一条**: 世界模型答的是"现在怎么样", 不是
//	"今天发生过什么"(后者账本里有, 而且那是另一个问题).
func (w *World) Observe(s abi.Signal) {
	if strings.TrimSpace(s.Kind) == "" {
		return
	}
	at := s.At
	if at == 0 {
		at = w.now().UnixMilli()
	}
	key := factKind(s.Kind)
	who := ""
	if w.owner != nil {
		who = w.owner(s.Source)
	}
	f := Fact{
		Key: key, Who: who, Source: s.Source, At: at,
		TTL: ttlFor(s.Kind), Fields: bodyOf(s),
		// **那句话延到读的时候再算** —— 见 Fact.Text 那段
		kind: s.Kind,
	}
	// 采集端可以自报有效期 —— 一份逐小时的天气预报知道自己什么时候作废,
	// 而 OS 只能猜. 它说了就听它的
	if v := asMillis(bodyOf(s)["validSec"]); v > 0 {
		f.TTL = time.Duration(v) * time.Second
	}
	k := factKey{who: who, what: key}
	w.mu.Lock()
	// **旧的不覆盖新的**: 补传是常态(手机离线两小时后一次性投几百条),
	// 而那批里最后到达的未必是最晚发生的
	if old, ok := w.facts[k]; ok && old.At > f.At {
		w.mu.Unlock()
		return
	}
	w.facts[k] = f
	w.mu.Unlock()
}

// say 把一条事实说成人话 —— **读的时候才算**, 见 Fact.Text.
//
//	不加锁: 调用方已经持锁, 而 describe 只读 places/nearby(它们各自
//	有自己的锁)
func (w *World) say(f Fact) Fact {
	if f.Text != "" {
		return f
	}
	f.Text = w.describe(abi.Signal{
		Source: f.Source, Kind: f.kind, At: f.At, Body: f.Fields,
	})
	return f
}

// describe 把一条信号说成人话.
//
//	**位置要说地名, 不说坐标**: "在公司"是判断的依据, 31.86,117.28
//	不是 —— 而地名只有用户给得出(见 Places).
func (w *World) describe(s abi.Signal) string {
	b := bodyOf(s)
	say := strings.TrimSpace(str(b["text"]))
	// ── 位置那句话是**补充**, 不是替代 ──
	//
	//	手机开车时会给位置带一句"在动（约 40 km/h）". 照"采集端给了话就用它
	//	的"那条走, "在公司"就被"停着"盖掉了 —— 而地名才是判断的依据.
	//	所以位置类: 地名在前, 那句话挂在后面.
	if say != "" && (strings.HasPrefix(s.Kind, "place.") || s.Kind == "location") {
		b2 := make(map[string]any, len(b))
		for k, v := range b {
			if k != "text" {
				b2[k] = v
			}
		}
		where := w.describe(abi.Signal{Source: s.Source, Kind: s.Kind, At: s.At, Body: b2})
		return where + "，" + say
	}
	// WiFi 名字本身什么都不说: "连着 一种_5G" 被它读成了"没连 WiFi"
	if s.Kind == "network.wifi" {
		if ssid := strings.TrimSpace(str(b["ssid"])); ssid != "" {
			return "连着 WiFi「" + ssid + "」"
		}
	}
	// 采集端自己给了一句话就用它的 —— 它比 OS 更懂自己报的是什么
	if say != "" {
		return say
	}
	switch {
	case strings.HasPrefix(s.Kind, "place."), s.Kind == "location":
		where := strings.TrimSpace(str(b["place"]))
		if where == "" && w.places != nil {
			where = w.places.Lookup(asFloat(b["lat"]), asFloat(b["lon"]))
		}
		if where == "" {
			// 用户没给这儿起过名 —— 退一步问问地图那儿大概是哪儿.
			//
			//	**这不是命名的替代品**: "这儿是公司"是一个关系, 能推出
			//	很多事; "在东海大道附近"只是一个地址, 什么都推不出来.
			//	它解决的只是"别说一句零信息的话"
			//
			//	**只说地址, 不挂"这儿还没起过名"**: 挂着那句, 它每次答
			//	"我在哪"都要先解释一遍"不是家也不是公司", 再问要不要起名 ——
			//	他在朋友家、在路上, 本来就不在任何起过名的地方.
			if w.nearby != nil {
				if near, ok := w.nearby(asFloat(b["lat"]), asFloat(b["lon"])); ok {
					if strings.HasSuffix(s.Kind, ".left") {
						return "刚离开" + near
					}
					return "在" + near
				}
			}
			// **认不出的地方就说认不出**, 不要报坐标: 用户看不懂坐标,
			// 而模型拿坐标只会编一个地名出来
			return "在一个还没起过名的地方"
		}
		if strings.HasSuffix(s.Kind, ".left") {
			return "刚离开" + where
		}
		return "在" + where
	}
	// 只有标题的通知("语音输入服务正在运行") —— 光一个 notice.posted 是噪音
	if s.Kind == "notice.posted" {
		if app := strings.TrimSpace(str(b["app"])); app != "" {
			if t := strings.TrimSpace(str(b["title"])); t != "" {
				return app + "：" + t
			}
			return app + " 来了条通知"
		}
	}
	// 认不出形状就照实说是什么事 —— 比编一句话强
	if v := strings.TrimSpace(str(b["state"])); v != "" {
		return s.Kind + " " + v
	}
	return s.Kind
}

// Snapshot 此刻的事实 —— **过期的不出现**.
//
//	不是降权, 是不出现: 要么说得出, 要么说不知道. 一个自信的错误答案
//	比"不知道"糟得多 —— 后者会让人再问一次.
//	who 空 = 只看屋子的那些; 给了名字就是**他的 + 屋子的**.
//	不给"屋子的"是不行的: 一个人问"要不要带伞", 而天气不属于任何人
func (w *World) Snapshot(who string) []Fact {
	now := w.now()
	w.mu.Lock()
	out := make([]Fact, 0, len(w.facts)+3)
	for k, f := range w.facts {
		if !w.fresh(f, now) {
			continue
		}
		// 别人的事实不给 —— 这不是保密(见 people.go 那段), 是**别答错**:
		// 拿她的位置回答他"我在哪", 不会报错, 只会答错
		if k.who != "" && k.who != who {
			continue
		}
		out = append(out, w.say(f))
	}
	w.mu.Unlock()

	out = append(out, w.osFacts(now)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// osFacts OS 自己就知道的那几条 —— 它们不来自任何采集端, 也不属于某个人
func (w *World) osFacts(now time.Time) []Fact {
	var out []Fact
	if w.presence != nil {
		if note := strings.TrimSpace(w.presence.Note()); note != "" {
			out = append(out, Fact{Key: "home", Text: note, Source: "OS",
				At: now.UnixMilli()})
		}
	}
	if w.timers != nil {
		if next, ok := nextWake(w.timers, now); ok {
			out = append(out, Fact{Key: "next", Text: next, Source: "OS",
				At: now.UnixMilli()})
		}
	}
	return out
}

// nextWake 下一个提醒是什么、什么时候.
//
//	**这条必须在世界模型里**: "我今天还有什么事"是最常问的一句, 而
//	答案就在闹钟表里 —— bot 够不着它的话只能回答"我不知道",
//	而 OS 明明知道.
func nextWake(t *Timers, now time.Time) (string, bool) {
	var soonest *Wake
	for _, w := range t.Pending() {
		if w.At < now.UnixMilli() {
			continue
		}
		if soonest == nil || w.At < soonest.At {
			cp := w
			soonest = &cp
		}
	}
	if soonest == nil {
		return "", false
	}
	when := time.UnixMilli(soonest.At)
	left := when.Sub(now)
	text := wakeLabel(*soonest)
	return fmt.Sprintf("%s（%s，还有 %s）", text,
		when.Format("15:04"), roundDur(left)), true
}

// wakeLabel 一个闹钟在"现在什么情况"里怎么称呼.
//
// ── 巡检的题目不是给人看的 ──
//
//	Think 那种闹钟的 Text 是**给判断者的题目**, 可能长这样:
//	"早上到校巡检：今天若是工作日(date +%u 为 1-5)，读工作区 SCHEDULE.md
//	和 ALARM.md。任务 A…" —— 三百多字. 它每次问"我在哪"都整段出现在
//	where 的结果里, 于是一句"在家"后面跟着一篇作文, 模型还会把它当成
//	"他今天要做的事"复述给他.
//
//	所以: 巡检只报第一句的名目, 截到二十个字. 题目全文在它醒来那一刻
//	才需要, 那条路(Timers 到点)拿的是原文, 不走这里.
func wakeLabel(w Wake) string {
	text := strings.TrimSpace(w.Text)
	if text == "" {
		return "一件事"
	}
	if !w.Think {
		return text
	}
	for _, stop := range []string{"：", ":", "。", "，", "(", "（", "\n"} {
		if i := strings.Index(text, stop); i > 0 {
			text = text[:i]
		}
	}
	if r := []rune(text); len(r) > 20 {
		text = string(r[:20]) + "…"
	}
	return "例行巡检「" + text + "」"
}

// roundDur 人说时间的方式 —— 到分钟为止.
// "还有 47 分钟"有用, "还有 47 分 13 秒"是噪音
func roundDur(d time.Duration) time.Duration { return d.Round(time.Minute) }

// Text 世界模型的一句话版本 —— 进提示词、进工具返回.
//
//	**说不出就说"不知道"**: 空着的话模型会自己填一个,
//	而它填的东西看起来跟真的一模一样.
func (w *World) Text(who string) string {
	facts := w.Snapshot(who)
	if len(facts) == 0 {
		return "现在什么都不知道 —— 还没有任何采集端报过数据。"
	}
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "· %s", f.Text)
		// 什么时候知道的要写出来 —— **一条 40 分钟前的事实和一条
		// 刚到的事实, 对判断的分量完全不同**
		if f.Source != "OS" {
			fmt.Fprintf(&b, "（%s，%s）", f.Source, agoText(w.now(), f.At))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// TextAll 屋里所有人的那一份 —— **按人分组**.
//
//	给 bot 的 what_now 用. 为什么不是"当前说话人的那一份": bot 不知道
//	正在跟它说话的是谁(一句话进来只有文字, 没有身份). 而**把两个人的
//	位置混成一列是最坏的**: 它会说"你在公司", 而那是她.
//
//	分组之后反倒多了一种能力: "我老婆到家没有"这句话答得了.
//
//	name 把人的 id 翻成名字 —— 一个显示成 uuid 的人在回答里没法认.
func (w *World) TextAll(name func(id string) string) string {
	now := w.now()
	w.mu.Lock()
	byWho := map[string][]Fact{}
	for k, f := range w.facts {
		if w.fresh(f, now) {
			byWho[k.who] = append(byWho[k.who], w.say(f))
		}
	}
	w.mu.Unlock()

	var b strings.Builder
	// 屋子的先说 —— 天气和门是每个人都要的背景
	if house := byWho[""]; len(house) > 0 {
		b.WriteString("屋里:\n")
		writeFacts(&b, house, now)
	}
	whos := make([]string, 0, len(byWho))
	for who := range byWho {
		if who != "" {
			whos = append(whos, who)
		}
	}
	sort.Strings(whos)
	for _, who := range whos {
		label := who
		if name != nil {
			label = name(who)
		}
		fmt.Fprintf(&b, "%s:\n", label)
		writeFacts(&b, byWho[who], now)
	}
	// OS 自己知道的那几条(在场、下一个提醒)照旧跟在后面
	for _, f := range w.osFacts(now) {
		fmt.Fprintf(&b, "· %s\n", f.Text)
	}
	if b.Len() == 0 {
		return "现在什么都不知道 —— 还没有任何采集端报过数据。"
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeFacts(b *strings.Builder, facts []Fact, now time.Time) {
	sort.Slice(facts, func(i, j int) bool { return facts[i].Key < facts[j].Key })
	for _, f := range facts {
		fmt.Fprintf(b, "· %s（%s，%s）\n", f.Text, f.Source, agoText(now, f.At))
	}
}

// agoText 这条事实是什么时候知道的 —— **写钟点, 不写"几分钟前"**.
//
// ── 相对时间在上下文里会变成谎话 ──
//
//	"10 分钟前"这句话, 在它被生成的那一刻是对的。而工具结果会**留在
//	上下文里**: 二十轮之后它还写着"10 分钟前", 而实际已经两小时了 ——
//	模型照读不误, 于是它拿一条两小时前的位置回答"你现在在哪", 答得
//	很有把握。
//
//	用户的原话: "它经常用上下文上已经存在的老的错误的数据偷偷直接
//	给我回"。
//
//	**绝对时间不会烂**: "21:45"这句话一年后还是对的。而每一轮用户消息
//	的尾巴上都挂着"[现在 …]"(见 agent.nowNote), 模型一减就知道多旧。
//
//	代价是它要自己减一次 —— 而那件事它做得比"记住这句话是什么时候
//	说的"可靠得多。
func agoText(now time.Time, at int64) string {
	t := time.UnixMilli(at)
	// 隔天的要带日期: 光看"08:15"分不出是今天早上还是昨天早上,
	// 而那两件事差 24 小时
	if t.YearDay() != now.YearDay() || t.Year() != now.Year() {
		return t.Format("01-02 15:04")
	}
	return t.Format("15:04")
}

// worldLookback 重启时往回读多久的信号.
//
//	12 小时: 跟 aliveGrace 一样 —— 再往前的事实无论如何都不该回答
//	"现在怎么样". 读多了只是白扫账本.
const worldLookback = 12 * time.Hour

// Restore 从账本重建.
//
// ── 为什么非有不可 ──
//
//	世界模型是账本的一个**投影** —— 那就意味着它必须能从账本重建.
//	没有这一步的话, 它只能靠"还没被总结的那几条信号"碰运气地长回来,
//	而那些多半已经总结过了.
//
//	如果不从账本恢复, 重启之后打开"我"那一页, "它现在知道什么"会是空的 ——
//	而账本里明明有八分钟前的位置. 更糟的是它**不会自己好起来**:
//	采集端是按移动触发的, 人不动就不报, 于是 OS 可能一整天都不知道
//	他在哪.
//
//	这跟"闹钟活得比进程长"是同一类问题, 只是这次丢的是"现在".
//
// ── 只读最近的 ──
//
//	按时间从旧到新喂进去, 让 Observe 里那道"旧的不覆盖新的"自己去挑.
func (w *World) Restore(evs []abi.Event) int {
	cut := w.now().Add(-worldLookback).UnixMilli()
	n := 0
	for _, e := range evs {
		if e.Kind != abi.EvSignal {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		at := int64Of(m["at"])
		if at < cut {
			continue
		}
		body, _ := m["body"].(map[string]any)
		w.Observe(abi.Signal{
			ID: str(m["id"]), Source: str(m["source"]), Kind: str(m["kind"]),
			At: at, Body: body,
		})
		n++
	}
	return n
}

// bodyOf 信号的 body —— OS 不解释它的形状, 但要取得到几个约定字段
func bodyOf(s abi.Signal) map[string]any {
	if s.Body == nil {
		return map[string]any{}
	}
	return s.Body
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
