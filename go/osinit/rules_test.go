package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// rainy 造一个"未来有雨"的世界
func rainy(w *World, now time.Time, prob float64) {
	w.Observe(abi.Signal{
		Source: "weather", Kind: "weather.now", At: now.UnixMilli(),
		Body: map[string]any{"text": "多云 21°C", "rainProb": prob},
	})
}

func umbrella() Rule {
	return Rule{
		When:       []Cond{{Fact: "weather", Field: "rainProb", Op: ">", Value: 60}},
		Say:        "今天有雨，出门带伞",
		Why:        "降水概率过 60%",
		OncePerDay: true,
	}
}

// **边沿, 不是电平** —— 这是这一层里错了必然出人命的那条.
//
//	"降水概率 > 60"在雨停之前一直成立. 按电平触发的话, 天气每半小时
//	拉一次就响一次, 一个下雨天十几条"记得带伞" —— 而用户的反应不是
//	报 bug, 是把整个通知关掉.
func TestRuleFiresOnEdgeNotLevel(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	w := NewWorld(atClock(now))
	fired := 0
	r := NewRules(nil, w, atClock(now), func(Rule) { fired++ })
	if _, err := r.Add(umbrella()); err != nil {
		t.Fatal(err)
	}

	rainy(w, now, 82)
	r.Tick()
	if fired != 1 {
		t.Fatalf("条件成立了却没响: %d", fired)
	}
	// 还在下 —— 条件一直成立, 但不该再响
	for i := 0; i < 5; i++ {
		rainy(w, now, 85)
		r.Tick()
	}
	if fired != 1 {
		t.Fatalf("电平触发了: 响了 %d 次 —— 一个下雨天会响十几次", fired)
	}
}

// 同一天里掉下去又回来, 也不该再说一次: 概率在 58/62 之间抖一整天的话,
// 边沿本身会触发很多次, 对用户跟电平触发没有区别
func TestRuleSuppressedWithinTheDay(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	clock := now
	w := NewWorld(func() time.Time { return clock })
	fired := 0
	r := NewRules(nil, w, func() time.Time { return clock }, func(Rule) { fired++ })
	_, _ = r.Add(umbrella())

	rainy(w, clock, 82)
	r.Tick()
	clock = clock.Add(time.Hour)
	rainy(w, clock, 30) // 掉下去
	r.Tick()
	clock = clock.Add(time.Hour)
	rainy(w, clock, 75) // 又上来
	r.Tick()

	if fired != 1 {
		t.Fatalf("一天里说了 %d 次 —— 概率抖一天就是抖一天的通知", fired)
	}
}

// 第二天该重新算
func TestRuleFiresAgainNextDay(t *testing.T) {
	clock := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	w := NewWorld(func() time.Time { return clock })
	fired := 0
	r := NewRules(nil, w, func() time.Time { return clock }, func(Rule) { fired++ })
	_, _ = r.Add(umbrella())

	rainy(w, clock, 82)
	r.Tick()
	clock = clock.Add(20 * time.Hour)
	rainy(w, clock, 20) // 先掉下去, 好让边沿能再抬一次
	r.Tick()
	clock = clock.Add(4 * time.Hour)
	rainy(w, clock, 82)
	r.Tick()

	if fired != 2 {
		t.Fatalf("第二天没有重新算: 一共响了 %d 次", fired)
	}
}

// **事实不在就是不成立** —— 忽略那条谓词的话, "下雨且我在家"会在
// 天气拉不到的时候退化成"我在家", 于是在大晴天喊你带伞
func TestRuleMissingFactMeansFalse(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	fired := 0
	r := NewRules(nil, w, atClock(now), func(Rule) { fired++ })
	_, _ = r.Add(Rule{
		When: []Cond{
			{Fact: "weather", Field: "rainProb", Op: ">", Value: 60},
			{Fact: "place", Op: "contains", Value: "家"},
		},
		Say: "带伞",
	})
	rainy(w, now, 90) // 只有天气, 没有位置
	r.Tick()
	if fired != 0 {
		t.Fatal("缺一条事实也算成立了 —— 它会在大晴天喊你带伞")
	}
}

// 数值比要真的按数值比: 按字符串比的话 "9" > "60" 会成立,
// 否则会在"9% 的降水概率"下喊人带伞
func TestRuleComparesNumbersNumerically(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	fired := 0
	r := NewRules(nil, w, atClock(now), func(Rule) { fired++ })
	_, _ = r.Add(umbrella())
	rainy(w, now, 9)
	r.Tick()
	if fired != 0 {
		t.Fatal("9 被判成大于 60 —— 按字符串比了")
	}
}

// 时间窗: 早上那条规则不该在半夜响
func TestRuleRespectsWindow(t *testing.T) {
	night := time.Date(2026, 9, 10, 2, 0, 0, 0, time.Local)
	w := NewWorld(atClock(night))
	fired := 0
	r := NewRules(nil, w, atClock(night), func(Rule) { fired++ })
	x := umbrella()
	x.From, x.To = "07:00", "09:00"
	_, _ = r.Add(x)
	rainy(w, night, 90)
	r.Tick()
	if fired != 0 {
		t.Fatal("凌晨两点把人喊起来带伞")
	}
}

// 跨午夜的窗口是合法的(夜里 22:00-06:00) —— 不认的话它永远不成立
func TestRuleWindowCrossesMidnight(t *testing.T) {
	night := time.Date(2026, 9, 10, 23, 30, 0, 0, time.Local)
	w := NewWorld(atClock(night))
	fired := 0
	r := NewRules(nil, w, atClock(night), func(Rule) { fired++ })
	x := umbrella()
	x.From, x.To = "22:00", "06:00"
	_, _ = r.Add(x)
	rainy(w, night, 90)
	r.Tick()
	if fired != 1 {
		t.Fatal("22:00-06:00 这个窗口在 23:30 不成立 —— 跨午夜没认")
	}
}

// 一条什么条件都没有的规则会在每次求值时都成立 —— 那就是一天几十条
func TestRuleRejectsEmptyCondition(t *testing.T) {
	r := NewRules(nil, NewWorld(nil), nil, nil)
	if _, err := r.Add(Rule{Say: "喂"}); err == nil {
		t.Fatal("没有任何条件的规则被收下了")
	}
	if _, err := r.Add(Rule{When: []Cond{{Fact: "weather", Op: "~", Value: 1}}, Say: "喂"}); err == nil {
		t.Fatal("不认得的运算符被收下了 —— 它会静默地永远不成立")
	}
}

// 规则活得比进程长 —— 一次重启不该抹掉已经设定的规则
func TestRuleSurvivesRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	r := NewRules(log, NewWorld(nil), nil, nil)
	if _, err := r.Add(umbrella()); err != nil {
		t.Fatal(err)
	}

	var evs []abi.Event
	for _, b := range log.Snapshot() {
		evs = append(evs, b...)
	}
	next := NewRules(nil, NewWorld(nil), nil, nil)
	next.Restore(evs)

	got := next.List()
	if len(got) != 1 {
		t.Fatalf("重启之后规则不见了: %d 条", len(got))
	}
	if len(got[0].When) != 1 || got[0].When[0].Field != "rainProb" {
		t.Fatalf("规则装回来了但条件没了: %+v —— 那它永远不会成立", got[0])
	}
}
