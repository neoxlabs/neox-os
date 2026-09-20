package osinit

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 规律 —— **"大部分人每天都差不多"**.
//
// ── 这一层要回答的问题 ──
//
//	"我上班在哪" 用户可以自己说一句(remember_place). 但他不会想起来说
//	每一件事: 他几点出门、周末去哪、中午在哪吃. 而这些恰恰是"提前
//	提醒你"的全部依据.
//
//	所以这一层做三件事, 缺一件都不成立:
//
//	  ① 位置流 → **停留**       在一个地方待够久
//	  ② 停留 → **常去的地方**   哪几个格子反复出现, 分时段
//	  ③ 常去 → **偏差**         今天跟平时不一样
//
// ── 真正有用的是偏差, 不是预测 ──
//
//	"你通常 8:40 出门"这句话对用户没用 —— 他自己知道.
//	"今天 8:50 了你还在家"才是提醒的燃料.
//
//	所以这一层的产物不是一份作息表, 是一个能被规则引擎判的事实.
//
// ── 必须可解释 ──
//
//	说得出"过去 20 天里有 17 天这个点你在这儿". 说不出的话, 用户没法
//	判断值不值得信 —— 而一个不可信的主动提醒比不提醒更糟.
//
//	这也是**推导出来不自动命名**的理由: 它只建议("你最近老在这儿待着,
//	要记成公司吗"), 由人确认. 自动命名的话, 错了没有任何一处会说.

const (
	// stayGrid 多大算"同一个地方". 110 米 —— 跟采集端的移动门槛
	// (120 米)和地点半径(150 米)是同一个量级
	stayGrid = 1000.0
	// minStay 待多久才算一次停留.
	//
	//	20 分钟: 短于这个的多半是等红灯、堵车、路过. 把那些算成停留
	//	的话, 一条上班路上会冒出十几个"常去的地方"
	minStay = 20 * time.Minute
	// maxStay 一次停留最长算多久.
	//
	//	── 为什么要有上限 ──
	//
	//	**沉默即停留**(见 Observe): 采集端在人不动的时候不报, 所以
	//	"他一直在那儿"的证据就是"没有别的地方的信号". 但同一段沉默还有
	//	另一种解释: **手机关机了、没网了、被用户关掉了采集**.
	//
	//	两者在数据上一模一样. 分不开就只能定一个上限: 超过这个数的
	//	一段沉默不作为证据 —— 一次关机一天不该变成"他在家待了 24 小时"
	//	这条会污染整个作息推导的假事实.
	//
	//	20 小时: 装得下一个正常的夜晚(12h)和一个宅家的白天, 装不下
	//	一次关机
	maxStay = 20 * time.Hour
	// minDays 至少在多少个不同的日子出现过, 才敢说"你常去这儿".
	//
	//	**按天数不按次数**: 一天里报了 50 条位置不代表这是常去的地方,
	//	它可能只是那天在那儿开了一下午会
	minDays = 4
)

// Stay 一次停留.
type Stay struct {
	Who      string
	Lat, Lon float64
	From, To int64
}

func (s Stay) Dur() time.Duration {
	return time.Duration(s.To-s.From) * time.Millisecond
}

// spot 一个格子的累计
type spot struct {
	lat, lon float64
	// days 在哪几天出现过 —— 按天数判, 不按次数
	days map[string]bool
	// hours 按小时累计待了多久(秒). 分时段推导要靠它
	hours [24]int64
	total time.Duration
	last  int64
}

// Routine 规律表. 一个人一份.
type Routine struct {
	mu    sync.Mutex
	spots map[string]map[string]*spot // who → gridKey → spot
	open  map[string]*Stay            // who → 正在进行的那次停留
	log   *EventLog
	now   func() time.Time
}

func NewRoutine(log *EventLog, now func() time.Time) *Routine {
	if now == nil {
		now = time.Now
	}
	return &Routine{
		spots: map[string]map[string]*spot{},
		open:  map[string]*Stay{},
		log:   log, now: now,
	}
}

// Observe 一条位置事实.
//
//	**只认带坐标的**: 别的信号(门、电量)跟规律无关, 而且混进来会把
//	"停留"这个概念搅浑.
func (r *Routine) Observe(who string, lat, lon float64, at int64) {
	if lat == 0 && lon == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	cur := r.open[who]
	// 还在同一个格子 —— 往后延
	if cur != nil && sameGrid(cur.Lat, cur.Lon, lat, lon) {
		if at > cur.To {
			cur.To = at
		}
		return
	}
	// ── 换地方了 = 上一次停留到此刻为止 ──
	//
	//	**沉默即停留**. 第一版写反了: 按"信号持续到达才算还在那儿"来判,
	//	而采集端**恰恰在人不动的时候不报**(120 米的移动门槛, 见
	//	Collector.kt). 于是一个在家待了一夜的人只会产生两条信号 ——
	//	到家那条和第二天出门那条 —— 而按那种判法, 每一次停留的时长
	//	都是 0, 什么规律都推不出来.
	//
	//	所以离开不是一个事件, 是**下一个地方的第一条信号**.
	if cur != nil {
		cp := *cur
		if at > cp.To {
			cp.To = at
		}
		r.closeLocked(who, cp)
	}
	r.open[who] = &Stay{Who: who, Lat: lat, Lon: lon, From: at, To: at}
}

// closeLocked 收掉一次停留, 够久才记进规律
func (r *Routine) closeLocked(who string, s Stay) {
	delete(r.open, who)
	if s.Dur() < minStay {
		// 等红灯、路过 —— 不算
		return
	}
	if s.Dur() > maxStay {
		// 多半是关机/没网的一段沉默, 不是真的在那儿待了那么久 ——
		// 见 maxStay. 收下的话它会压过所有真实的停留
		return
	}
	byGrid := r.spots[who]
	if byGrid == nil {
		byGrid = map[string]*spot{}
		r.spots[who] = byGrid
	}
	k := stayKey(s.Lat, s.Lon)
	sp := byGrid[k]
	if sp == nil {
		sp = &spot{lat: s.Lat, lon: s.Lon, days: map[string]bool{}}
		byGrid[k] = sp
	}
	from := time.UnixMilli(s.From)
	sp.days[from.Format("2006-01-02")] = true
	sp.total += s.Dur()
	sp.last = s.To
	// 按小时摊开 —— 一次 9:40 到 12:10 的停留要落在 9/10/11/12 上,
	// 全记在开始那个小时的话, "他中午在哪"永远推不出来
	for t := from; t.Before(time.UnixMilli(s.To)); t = t.Add(time.Hour) {
		end := t.Add(time.Hour)
		if e := time.UnixMilli(s.To); e.Before(end) {
			end = e
		}
		sp.hours[t.Hour()] += int64(end.Sub(t).Seconds())
	}
	if r.log != nil {
		r.log.Append(signalPID, abi.EvStayEnded, map[string]any{
			"who": who, "lat": s.Lat, "lon": s.Lon,
			"from": s.From, "to": s.To,
		})
	}
}

// Flush 把还开着的那次也收掉 —— 查询之前要走一遍, 否则"今天刚待了
// 三小时的那个地方"不算数
func (r *Routine) Flush() {
	now := r.now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	for who, s := range r.open {
		if s == nil {
			continue
		}
		cp := *s
		// **算到此刻**, 不是算到最后一条信号 —— 他现在就在那儿,
		// 而那正是"今天在这儿待了一下午"要被数上的那一段
		if now > cp.To {
			cp.To = now
		}
		r.closeLocked(who, cp)
	}
}

// Habit 一个常去的地方.
type Habit struct {
	Lat, Lon float64
	// Days 在多少个不同的日子出现过
	Days int
	// Total 一共待了多久
	Total time.Duration
	// Guess OS 猜它是什么: 家 / 上班的地方 / 常去的地方
	Guess string
	// Why 凭什么这么猜 —— **必须说得出**, 见开头那段
	Why string
}

// Habits 这个人常去哪几个地方, 最常去的在前.
//
//	who 空 = 屋子的(没有主人的设备报的)
func (r *Routine) Habits(who string) []Habit {
	r.Flush()
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Habit
	for _, sp := range r.spots[who] {
		if len(sp.days) < minDays {
			// 去过几次不算常去 —— 按天数判, 一天报 50 条不算 50 次
			continue
		}
		h := Habit{Lat: sp.lat, Lon: sp.lon, Days: len(sp.days), Total: sp.total}
		h.Guess, h.Why = guessKind(sp)
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

// guessKind 这个地方大概是什么.
//
//	**只看时段, 不看别的**: 夜里在的地方是家, 工作日白天在的地方是
//	上班的地方. 这个判据粗, 但它**说得出理由** —— 而说不出理由的
//	精确判据在这儿没有价值(用户没法判断值不值得信).
func guessKind(sp *spot) (string, string) {
	var night, work int64
	for h := 0; h < 24; h++ {
		switch {
		case h >= 22 || h < 6:
			night += sp.hours[h]
		case h >= 9 && h < 17:
			work += sp.hours[h]
		}
	}
	days := len(sp.days)
	switch {
	case night > work && night > 0:
		return "家", fmt.Sprintf("%d 天里, 夜里(22:00-06:00)你都在这儿", days)
	case work > 0:
		return "上班的地方", fmt.Sprintf("%d 天里, 白天(09:00-17:00)你都在这儿", days)
	}
	return "常去的地方", fmt.Sprintf("去过 %d 天", days)
}

// Text 说成人话 —— 给 bot 的工具返回.
//
//	**带上"还没起过名"这句**: 推导出来的东西不自动命名(见开头),
//	它要引着人去确认
func (r *Routine) Text(who string, named func(lat, lon float64) string) string {
	hs := r.Habits(who)
	if len(hs) == 0 {
		return "还看不出规律 —— 位置数据太少（至少要在 " +
			fmt.Sprint(minDays) + " 个不同的日子待过同一个地方）。"
	}
	var b strings.Builder
	for _, h := range hs {
		name := ""
		if named != nil {
			name = named(h.Lat, h.Lon)
		}
		if name != "" {
			fmt.Fprintf(&b, "· %s（你起的名）—— %s\n", name, h.Why)
			continue
		}
		fmt.Fprintf(&b, "· 一个还没起名的地方，看着像%s —— %s\n", h.Guess, h.Why)
	}
	return strings.TrimRight(b.String(), "\n")
}

// TextAll 屋里每个人的规律 —— **按人分组**.
//
//	理由跟 World.TextAll 一样: bot 不知道正在跟它说话的是谁(一句话进来
//	只有文字, 没有身份). 混成一列的话它会说"你常去这儿", 而那是她.
func (r *Routine) TextAll(name func(id string) string,
	named func(lat, lon float64) string) string {
	r.Flush()
	r.mu.Lock()
	whos := make([]string, 0, len(r.spots))
	for who := range r.spots {
		whos = append(whos, who)
	}
	r.mu.Unlock()
	sort.Strings(whos)

	var b strings.Builder
	for _, who := range whos {
		body := r.Text(who, named)
		if strings.HasPrefix(body, "还看不出规律") {
			continue
		}
		label := who
		if who == "" {
			label = "屋里"
		} else if name != nil {
			label = name(who)
		}
		fmt.Fprintf(&b, "%s:\n%s\n", label, body)
	}
	if b.Len() == 0 {
		return "还看不出规律 —— 位置数据太少（至少要在 " +
			fmt.Sprint(minDays) + " 个不同的日子待过同一个地方）。"
	}
	return strings.TrimRight(b.String(), "\n")
}

// Restore 从账本装回来.
//
//	**规律是攒出来的, 一次重启不该清零** —— 攒够 minDays 要好几天,
//	而进程一周会重启好几次. 清零的话它永远看不出任何规律,
//	而且没有任何一处会说.
func (r *Routine) Restore(evs []abi.Event) {
	// ── 装回来的一律不记账 ──
	//
	//	跟 Places.Restore 同一条规矩(那边为这个栽过一次). 不摘的话,
//	**每次开机 stay.ended 都翻一倍** —— 按小时数统计出来是
	//	1 → 3 → 4 → 24 → 96 → 896 → 7168, 今天发了八九次版就涨到 8192 条.
	//
	//	而它不只是占地方: sense 桶一过 compactAfterSenseEvents,
	//	Append 里那道压紧就对整桶跑一遍 —— 装回时每条停留写一次、
	//	压一次, 于是开机变成平方级. 测试实例 3.2 万条时直接卡死在
	//	HTTP 起来之前(没有接口、没有 bot, 日志停在横幅那儿).
	r.mu.Lock()
	log := r.log
	r.log = nil
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.log = log
		r.mu.Unlock()
	}()
	for _, e := range evs {
		if e.Kind != abi.EvStayEnded {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		lat, lon := asFloat(m["lat"]), asFloat(m["lon"])
		if lat == 0 && lon == 0 {
			continue
		}
		r.mu.Lock()
		r.closeLocked(str(m["who"]), Stay{
			Who: str(m["who"]), Lat: lat, Lon: lon,
			From: int64Of(m["from"]), To: int64Of(m["to"]),
		})
		r.mu.Unlock()
	}
}

func sameGrid(lat1, lon1, lat2, lon2 float64) bool {
	return stayKey(lat1, lon1) == stayKey(lat2, lon2)
}

func stayKey(lat, lon float64) string {
	return fmt.Sprintf("%.0f,%.0f",
		math.Round(lat*stayGrid), math.Round(lon*stayGrid))
}
