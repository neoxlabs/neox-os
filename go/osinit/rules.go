package osinit

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 条件触发 —— "当…的时候告诉我".
//
// ── 触发器原来只有一个维度 ──
//
//	到点(At / Every). 于是"我一到家就跟我说"勉强做得到(Watch 那个
//	两字段匹配器), 而"明天要下雨且我 8 点有会就提前叫我"**系统里
//	没有任何东西能表达它**.
//
//	差的不是一个功能, 是一层: 一条规则要能说清四件事 ——
//
//	  什么成立   事实谓词. rainProb > 60
//	  什么时候   时间窗. 工作日 07:00-09:00
//	  变化的那下 边沿, 不是电平
//	  别老说     抑制
//
// ── 为什么必须是**边沿**触发 ──
//
//	这是这一层里最容易写错、而且错了必然出人命的一条.
//
//	"降水概率 > 60" 这个条件在雨停之前**一直成立**. 按电平触发的话,
//	它每次求值都会响一次 —— 天气每半小时拉一次, 于是一个下雨天
//	能收到十几条"记得带伞".
//
//	这个问题通常不会以单条错误反馈出现, 而会让人**把整个通知关掉**. 而那一关,
//	真正要紧的那次也到不了他.
//
//	所以: 只在**从不成立变成成立**的那一刻响.
//
// ── 为什么还要抑制 ──
//
//	光有边沿不够. 概率在 58 和 62 之间抖一整天的话, 边沿会触发很多次 ——
//	而那对用户跟电平触发没有区别. 所以再加一条: 同一条规则一天最多
//	说一次(可配).

// Cond 一条谓词: 世界模型里的某个数 / 某句话, 跟一个值比.
type Cond struct {
	// Fact 看哪条事实. 就是 kind 的第一段("weather" / "place")
	Fact string `json:"fact"`
	// Field 看它 body 里的哪个字段. 空 = 看那句话(Text)
	Field string `json:"field,omitempty"`
	// Op 怎么比: > < >= <= == != contains
	Op string `json:"op"`
	// Value 跟什么比. 数字或字符串
	Value any `json:"value"`
}

// Rule 一条规则.
type Rule struct {
	ID string `json:"id"`
	// When 全部成立才算成立 —— **只有"且", 没有"或"**.
	//
	//	"或"能用两条规则表达, 而一旦支持了嵌套, 这里就变成了一门
	//	小语言: 要有括号、优先级、错误信息, 还要有人去测它.
	//	而真实的规则几乎全是"且".
	When []Cond `json:"when"`
	// From / To 时间窗, "07:00" 形式. 都空 = 不限
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Days 星期几(0=周日). 空 = 每天
	Days []int `json:"days,omitempty"`
	// Say 成立时说什么
	Say string `json:"say"`
	// Why 凭哪条判据 —— **必须有**, 见 Deliver.Why
	Why string `json:"why,omitempty"`
	// Urgent 要不要现在就吵醒他
	Urgent bool `json:"urgent,omitempty"`
	// For 这条规矩是**谁的**. 空 = 屋子的, 对每个人各判一遍.
	//
	//	"下雨提醒我带伞"是他的; "门开了告诉我"是屋子的. 分不开的话,
	//	她的规则会拿他的位置去判 —— 而那种错不会报错, 只会在错的
	//	时刻喊错的人
	For string `json:"for,omitempty"`
	// OncePerDay 一天最多说一次. 缺省 true —— 见开头那段"为什么还要抑制"
	OncePerDay bool `json:"oncePerDay"`
	// Raw 用户的原话, 一字不改. 他以后问"我让你盯什么了"要照这个答
	Raw string `json:"raw,omitempty"`
}

// ruleState 一条规则此刻的样子 —— **不进账本, 从账本重建**
type ruleState struct {
	on      bool   // 上一次求值成不成立(边沿要靠它)
	saidDay string // 最后一次说是哪天(抑制要靠它)
}

// Rules 规则表.
type Rules struct {
	mu    sync.Mutex
	list  []Rule
	state map[string]*ruleState
	seq   int

	log   *EventLog
	now   func() time.Time
	world *World
	// fire 成立时怎么说出去
	fire func(Rule)
}

func NewRules(log *EventLog, w *World, now func() time.Time, fire func(Rule)) *Rules {
	if now == nil {
		now = time.Now
	}
	return &Rules{state: map[string]*ruleState{}, log: log, now: now, world: w, fire: fire}
}

// Add 加一条.
func (r *Rules) Add(rule Rule) (Rule, error) {
	if len(rule.When) == 0 {
		return Rule{}, fmt.Errorf(
			"when 是空的 —— 一条什么条件都没有的规则会在每次求值时都成立, " +
				"那就是一天几十条通知")
	}
	if strings.TrimSpace(rule.Say) == "" {
		return Rule{}, fmt.Errorf("say 是空的 —— 成立的时候对用户说什么?")
	}
	for i, c := range rule.When {
		if strings.TrimSpace(c.Fact) == "" {
			return Rule{}, fmt.Errorf("第 %d 条谓词没说看哪个事实", i+1)
		}
		switch c.Op {
		case ">", "<", ">=", "<=", "==", "!=", "contains":
		default:
			return Rule{}, fmt.Errorf(
				"第 %d 条谓词的 op %q 不认得。只有: > < >= <= == != contains", i+1, c.Op)
		}
	}
	if _, err := parseHM(rule.From); err != nil {
		return Rule{}, fmt.Errorf("from %q 不是 HH:MM", rule.From)
	}
	if _, err := parseHM(rule.To); err != nil {
		return Rule{}, fmt.Errorf("to %q 不是 HH:MM", rule.To)
	}

	r.mu.Lock()
	r.seq++
	if rule.ID == "" {
		rule.ID = fmt.Sprintf("r%d", r.seq)
	}
	r.list = append(r.list, rule)
	r.mu.Unlock()

	if r.log != nil {
		r.log.Append(signalPID, abi.EvRuleSet, ruleToMap(rule))
	}
	return rule, nil
}

// Remove 撤一条
func (r *Rules) Remove(id string) bool {
	r.mu.Lock()
	found := false
	out := r.list[:0]
	for _, x := range r.list {
		if x.ID == id {
			found = true
			continue
		}
		out = append(out, x)
	}
	r.list = out
	delete(r.state, id)
	r.mu.Unlock()
	if found && r.log != nil {
		r.log.Append(signalPID, abi.EvRuleRemoved, map[string]any{"id": id})
	}
	return found
}

// List 现在有哪几条 —— 用户问"我让你盯什么了"照这个答
func (r *Rules) List() []Rule {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]Rule(nil), r.list...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Tick 求一遍值. 挂在信号进来之后 —— 事实变了才可能有规则变.
func (r *Rules) Tick() {
	if r.world == nil {
		return
	}
	now := r.now()
	today := now.Format("2006-01-02")

	r.mu.Lock()
	rules := append([]Rule(nil), r.list...)
	r.mu.Unlock()

	// **每个人一份事实**: 一条规则只该看它主人的那一份. 共用一份的话,
	// 她的"到家提醒"会被他的位置触发
	byPerson := map[string]map[string]Fact{}
	factsFor := func(who string) map[string]Fact {
		if m, ok := byPerson[who]; ok {
			return m
		}
		m := map[string]Fact{}
		for _, f := range r.world.Snapshot(who) {
			m[f.Key] = f
		}
		byPerson[who] = m
		return m
	}

	for _, rule := range rules {
		ok := r.holds(rule, factsFor(rule.For), now)

		r.mu.Lock()
		st := r.state[rule.ID]
		if st == nil {
			st = &ruleState{}
			r.state[rule.ID] = st
		}
		was := st.on
		st.on = ok
		said := st.saidDay
		r.mu.Unlock()

		// ── 只在**变成成立**的那一刻响 ──
		//
		//	"降水概率 > 60"在雨停之前一直成立. 按电平触发的话, 天气每
		//	半小时拉一次就响一次 —— 一个下雨天十几条"记得带伞",
		//	而用户的反应是把整个通道关掉.
		if !ok || was {
			continue
		}
		// 抑制: 概率在 58 和 62 之间抖一整天的话, 边沿也会触发很多次
		if rule.OncePerDay && said == today {
			continue
		}

		r.mu.Lock()
		st.saidDay = today
		r.mu.Unlock()

		if r.log != nil {
			r.log.Append(signalPID, abi.EvRuleFired, map[string]any{
				"id": rule.ID, "say": rule.Say, "why": rule.Why,
			})
		}
		if r.fire != nil {
			r.fire(rule)
		}
	}
}

// holds 这条规则此刻成不成立
func (r *Rules) holds(rule Rule, facts map[string]Fact, now time.Time) bool {
	if !inWindow(rule, now) {
		return false
	}
	for _, c := range rule.When {
		f, ok := facts[c.Fact]
		if !ok {
			// **事实不在就是不成立, 不是"忽略这条谓词"**.
			//
			//	忽略的话, 一条"下雨且我在家"的规则会在天气拉不到的时候
			//	退化成"我在家" —— 于是它在一个大晴天喊你带伞.
			return false
		}
		if !match(c, f) {
			return false
		}
	}
	return true
}

func inWindow(rule Rule, now time.Time) bool {
	if len(rule.Days) > 0 {
		wd := int(now.Weekday())
		hit := false
		for _, d := range rule.Days {
			if d == wd {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	from, _ := parseHM(rule.From)
	to, _ := parseHM(rule.To)
	if from == 0 && to == 0 {
		return true
	}
	mins := now.Hour()*60 + now.Minute()
	if to == 0 {
		return mins >= from
	}
	if from == 0 {
		return mins <= to
	}
	// **跨午夜要认**: 22:00-06:00 是一个合法的窗口(夜里), 而
	// from > to 时按"或"算 —— 不认的话这个窗口永远不成立
	if from > to {
		return mins >= from || mins <= to
	}
	return mins >= from && mins <= to
}

// parseHM "07:30" → 450 分钟. 空串合法(= 不限)
func parseHM(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, err
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%s 不是一个时刻", s)
	}
	if h == 0 && m == 0 {
		// 00:00 跟"没填"在分钟数上撞了. 给它 1 分钟, 差别看不出来,
		// 而"没填"这个语义得留住
		return 1, nil
	}
	return h*60 + m, nil
}

// match 一条谓词成不成立.
//
//	**数值比要真的按数值比**: 按字符串比的话 "9" > "60" 会成立,
//	结果就是"它在 9% 的降水概率下喊你带伞"
func match(c Cond, f Fact) bool {
	if c.Field == "" {
		// 没指字段就比那句话 —— 只支持 contains / == / !=,
		// 拿一句中文去比大小是没有意义的
		text := f.Text
		want := fmt.Sprint(c.Value)
		switch c.Op {
		case "contains":
			return strings.Contains(text, want)
		case "==":
			return text == want
		case "!=":
			return text != want
		}
		return false
	}
	got, ok := f.Fields[c.Field]
	if !ok {
		return false
	}
	// 两边都是数才按数比
	gn, gok := numOf(got)
	wn, wok := numOf(c.Value)
	if gok && wok {
		switch c.Op {
		case ">":
			return gn > wn
		case "<":
			return gn < wn
		case ">=":
			return gn >= wn
		case "<=":
			return gn <= wn
		case "==":
			return gn == wn
		case "!=":
			return gn != wn
		}
		return false
	}
	gs, ws := fmt.Sprint(got), fmt.Sprint(c.Value)
	switch c.Op {
	case "==":
		return gs == ws
	case "!=":
		return gs != ws
	case "contains":
		return strings.Contains(gs, ws)
	}
	// **数值运算符碰上非数值就是不成立**, 不是按字符串凑合:
	// 凑合的结果是一条永远成立(或永远不成立)的规则, 而它看起来是对的
	return false
}

func numOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case bool:
		// true/false 当 1/0 —— "rainNow == 1"这种写法读起来别扭,
		// 但它让 body 里的布尔字段能被规则用上
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// Restore 从账本装回来.
//
//	**规则活得比进程长** —— 它和闹钟一样, "下雨提醒我带伞"这类规则
//	不该因为一次重启就消失, 否则规则创建者不会得到预期提醒.
//
//	状态(上一次成不成立、今天说过没有)**不装回来**: 它是从当下的事实
//	算出来的, 而重启之后事实本来就要重新拉一遍. 装回一个旧的"已经成立"
//	反而会让边沿判断错过第一次真正的变化.
func (r *Rules) Restore(evs []abi.Event) {
	// **装回来的一律不记账** —— 跟 Places.Restore 同一个坑:
	// Remove 会往账本里再写一条, 于是每次开机 rule.removed 翻一倍,
	// 而新写的那几条排在末尾, 顺序重放时盖掉它们前面的 rule.set.
	// 见 osinit/restore_writeback_test.go
	log := r.log
	r.log = nil
	defer func() { r.log = log }()
	for _, e := range evs {
		p, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		switch e.Kind {
		case abi.EvRuleSet:
			if rule, ok := ruleFromMap(p); ok {
				r.mu.Lock()
				r.list = append(r.list, rule)
				r.seq++
				r.mu.Unlock()
			}
		case abi.EvRuleRemoved:
			r.Remove(str(p["id"]))
		}
	}
}

func ruleToMap(x Rule) map[string]any {
	when := make([]any, 0, len(x.When))
	for _, c := range x.When {
		when = append(when, map[string]any{
			"fact": c.Fact, "field": c.Field, "op": c.Op, "value": c.Value,
		})
	}
	days := make([]any, 0, len(x.Days))
	for _, d := range x.Days {
		days = append(days, d)
	}
	return map[string]any{
		"id": x.ID, "when": when, "from": x.From, "to": x.To, "days": days,
		"say": x.Say, "why": x.Why, "urgent": x.Urgent, "for": x.For,
		"oncePerDay": x.OncePerDay, "raw": x.Raw,
	}
}

func ruleFromMap(p map[string]any) (Rule, bool) {
	id := str(p["id"])
	if id == "" {
		return Rule{}, false
	}
	x := Rule{
		ID: id, From: str(p["from"]), To: str(p["to"]),
		Say: str(p["say"]), Why: str(p["why"]), Raw: str(p["raw"]),
		For: str(p["for"]),
	}
	x.Urgent, _ = p["urgent"].(bool)
	x.OncePerDay, _ = p["oncePerDay"].(bool)
	if xs, ok := p["when"].([]any); ok {
		for _, it := range xs {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			x.When = append(x.When, Cond{
				Fact: str(m["fact"]), Field: str(m["field"]),
				Op: str(m["op"]), Value: m["value"],
			})
		}
	}
	if xs, ok := p["days"].([]any); ok {
		for _, it := range xs {
			if n, ok := numOf(it); ok {
				x.Days = append(x.Days, int(n))
			}
		}
	}
	if len(x.When) == 0 {
		return Rule{}, false
	}
	return x, true
}
