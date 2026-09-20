package osinit

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 整条链 —— **从一次 HTTP 到一条推送**.
//
// ── 为什么非要有这一组 ──
//
//	这个仓库里"组件全测过, 把它们连起来的那截线没人管"已经出过四次
//	(S33 / S37 / S56, 加上这次盘点抓到的"感知层压根没接进出货二进制").
//	每一次的形状都一样: 每个组件自己是对的, 接口也对得上, 所以编译器
//	什么都不说, 而**症状全是静默的** —— 没有报错, 只是某件事不发生.
//
//	所以这里不测组件, 测那截线: 一条信号从采集口进来, 最后有没有
//	变成一条用户看得见的 delivery.
//
// ── 为什么用 delivery 作为终点 ──
//
//	它是主动消息的唯一出口. 手机、桌面、以后的耳机全都只订这一种事件 ——
//	**在这儿断了, 就是"闹钟响了手机没动静"那一类故障**.

// chainRig 一套接好的真东西. 只有模型那一步是假的 ——
// 剩下每一段都是宿主真正跑的那一份
type chainRig struct {
	bus   *SignalBus
	world *World
	rules *Rules
	log   *EventLog
	base  string
	token string
	now   func() time.Time
}

func newChainRig(t *testing.T, clock *time.Time) *chainRig {
	t.Helper()
	o := New(Options{Mode: abi.ModeDev})
	t.Cleanup(func() { o.Shutdown("test") })
	log := o.Log()
	now := func() time.Time { return *clock }

	places := NewPlaces(log)
	world := NewWorld(now)
	deliveries := NewDeliveries(log)
	rules := NewRules(log, world, now, func(r Rule) {
		deliveries.Post(Deliver{
			From: "感知", Kind: DeliverProactive,
			Text: r.Say, Why: r.Why, Urgent: r.Urgent,
		})
	})

	wired := BuildSenseStack(SenseStack{
		Log: log, Places: places,
		Window: time.Minute, Lateness: 10 * time.Second,
		// 摘要那一路这组不关心 —— 这里测的是事实和规则那一路
		ToProcess:  func(string) bool { return true },
		ToTerminal: func(Digest, string) {},
		OnSignal: func(s abi.Signal) {
			world.Observe(s)
			rules.Tick()
		},
	})
	world.Use(places, wired.Presence, nil)

	srv, err := NewObserveServer(o, ObserveOptions{
		Addr: "127.0.0.1:0", Token: "tok",
		Sense: wired.Bus, Devices: NewDevices(log),
	})
	if err != nil {
		t.Fatalf("起不来: %v", err)
	}
	srv.Start()
	t.Cleanup(func() { _ = srv.Close() })

	return &chainRig{bus: wired.Bus, world: world, rules: rules, log: log,
		base: "http://" + srv.Addr(), token: "tok", now: now}
}

// deliveries 账本里已经投出去的那几条
func (r *chainRig) deliveries() []map[string]any {
	var out []map[string]any
	for _, bucket := range r.log.Snapshot() {
		for _, e := range bucket {
			if e.Kind != abi.EvDelivery {
				continue
			}
			if p, ok := e.Payload.(map[string]any); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// TestChainRainToPush 一条天气信号 → 一条"带伞"的推送.
//
//	天气变化触发出门提醒: "快下雨了要提醒我带伞".
//	整条链上任何一段断了, 这个测试就红.
func TestChainRainToPush(t *testing.T) {
	// **跟着真实时钟走**: 总线自己用的是 time.Now(BuildSenseStack 没有
	// 注入口), 而一条"来自未来"的信号会被它按时钟不对处理 ——
	// 那正是这组测试最容易假绿的地方
	clock := time.Now()
	rig := newChainRig(t, &clock)

	// 把天气提醒条件转换成一条规则, 才能验证自然语言意图贯穿整条链
	if _, err := rig.rules.Add(Rule{
		When: []Cond{{Fact: "weather", Field: "rainProb", Op: ">", Value: 60}},
		// 时间窗那一条在 rules_test 里单独测 —— 这组测的是链, 不是窗口
		Say:        "今天有雨，出门带伞",
		Why:        "降水概率 82%，而你通常这个点出门",
		OncePerDay: true,
		Raw:        "下雨提醒我带伞",
	}); err != nil {
		t.Fatalf("规则加不上: %v", err)
	}

	// 天气接入器拉到的那条, 从采集口投进来(接入器走的是同一条路)
	body, _ := json.Marshal(abi.Signal{
		ID: "w1", Source: "weather", Kind: "weather.now",
		At: clock.UnixMilli(),
		Body: map[string]any{
			"text": "多云 21°C，15 点前后有雨（82%）", "rainProb": 82.0,
		},
	})
	code, out := postJSON(t, rig.base+"/signal", rig.token, string(body))
	if code != 200 {
		t.Fatalf("投不进去: HTTP %d %v", code, out)
	}

	got := rig.deliveries()
	if len(got) != 1 {
		t.Fatalf("整条链断了: 天气进去了, 推送没出来。已投出 %d 条 %v", len(got), got)
	}
	if got[0]["text"] != "今天有雨，出门带伞" {
		t.Fatalf("推出去的不是规则说的那句话: %v", got[0])
	}
	// **理由必须跟着走**: 一次说不出理由的打扰, 用户唯一的处置是
	// 把整个通道关掉
	if got[0]["why"] == nil || got[0]["why"] == "" {
		t.Fatalf("推送没带理由: %v", got[0])
	}
}

// 一整天的雨只说一次 —— 天气每半小时拉一次, 而条件一直成立
func TestChainRainSaysItOnce(t *testing.T) {
	clock := time.Now()
	rig := newChainRig(t, &clock)
	_, _ = rig.rules.Add(Rule{
		When: []Cond{{Fact: "weather", Field: "rainProb", Op: ">", Value: 60}},
		Say:  "带伞", Why: "要下雨", OncePerDay: true,
	})

	// 三小时, 每半小时一次 —— 就是接入器真实的节奏.
	// 从三小时前往现在走, 不是从现在往未来走
	clock = clock.Add(-3 * time.Hour)
	for i := 0; i < 6; i++ {
		body, _ := json.Marshal(abi.Signal{
			ID: fmt.Sprintf("w%d", i), Source: "weather", Kind: "weather.now",
			At:   clock.UnixMilli(),
			Body: map[string]any{"text": "有雨", "rainProb": 80.0},
		})
		if code, out := postJSON(t, rig.base+"/signal", rig.token, string(body)); code != 200 {
			t.Fatalf("第 %d 次投不进去: %d %v", i, code, out)
		}
		clock = clock.Add(30 * time.Minute)
	}

	if n := len(rig.deliveries()); n != 1 {
		t.Fatalf("一个下雨天推了 %d 条「带伞」—— 用户会把整个通知关掉", n)
	}
}

// 大晴天不该有任何推送 —— **沉默是常态**
func TestChainStaysQuietWhenNothingHolds(t *testing.T) {
	clock := time.Now()
	rig := newChainRig(t, &clock)
	_, _ = rig.rules.Add(Rule{
		When: []Cond{{Fact: "weather", Field: "rainProb", Op: ">", Value: 60}},
		Say:  "带伞", Why: "要下雨", OncePerDay: true,
	})

	body, _ := json.Marshal(abi.Signal{
		ID: "w1", Source: "weather", Kind: "weather.now", At: clock.UnixMilli(),
		Body: map[string]any{"text": "晴 26°C", "rainProb": 5.0},
	})
	postJSON(t, rig.base+"/signal", rig.token, string(body))

	if n := len(rig.deliveries()); n != 0 {
		t.Fatalf("大晴天推了 %d 条 —— 这比不推糟得多", n)
	}
}

// 位置信号进来之后, "我现在在哪"要答得出 —— 这是**拉**的那一路,
// 跟推的那一路共用同一条总线
func TestChainLocationBecomesAskableFact(t *testing.T) {
	clock := time.Now()
	rig := newChainRig(t, &clock)

	body, _ := json.Marshal(abi.Signal{
		ID: "p1", Source: "phone", Kind: "place.arrived", At: clock.UnixMilli(),
		Body: map[string]any{"place": "公司"},
	})
	if code, _ := postJSON(t, rig.base+"/signal", rig.token, string(body)); code != 200 {
		t.Fatal("位置投不进去")
	}
	if got := rig.world.Text(""); got == "" ||
		!containsAll(got, "公司", "phone") {
		t.Fatalf("信号进来了, 但问'现在什么情况'答不出: %q", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}
