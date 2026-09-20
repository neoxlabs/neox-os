package osinit

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// **组件全测过, 把它们连起来的那截线没人管 —— 这已经是第三次了.**
//
//	S33  摘要投递的五步(换地名/关注直说/破例/回收/兜底)活在一个闭包里, 零测试
//	S37  失败通知送给了 current, 也就是**另一段对话**
//	S56  Presence 读 body["place"], 而手机只报经纬度 —— 那条路在真手机上是断的
//
// 三次的形状一样: **每个组件自己都对, 而且接口对得上(都是 abi.Signal),
// 所以编译器什么都不说**. 错的是没有人把它们接起来, 症状全是静默的.
//
// 这条测试走完整的一条路: HTTP 入口 → 总线 → 观察链(地点/在家) → 窗口
// → 投递接线 → **主动进程会看到的那段正文**. 中间任何一段接错都变红.
func TestSignalReachesTheProactiveProcess(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	places := NewPlaces(log)
	places.Add("家", 31.88, 117.28, 0)

	var seen string
	st := BuildSenseStack(SenseStack{
		Log: log, Places: places,
		Watches: NewWatches(log), Budget: NewInterruptBudget(log, 3, nil),
		Window: 200 * time.Millisecond, Lateness: 20 * time.Millisecond,
		ToProcess: func(text string) bool { seen = text; return true },
	})
	api, err := NewSenseAPI(st.Bus, SenseOptions{Addr: "127.0.0.1:0", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	api.Start()
	defer api.Close()

	now := time.Now().UnixMilli()
	// ① 手机说离开家了 —— **只有经纬度**, 跟真手机投出来的一模一样
	postBatch(t, api, fmt.Sprintf(`[{"id":"l1","source":"phone.mk","kind":"place.left",
		"at":%d,"knownAt":%d,"body":{"lat":31.88,"lon":117.28,"stayedMs":3600000}}]`,
		now-60000, now-60000))
	// ② 然后门开了
	postBatch(t, api, fmt.Sprintf(`[{"id":"d1","source":"ha.lock.front","kind":"lock.opened",
		"at":%d,"knownAt":%d,"body":{"entity":"lock.front","name":"前门锁","state":"unlocked"}}]`,
		now, now))
	time.Sleep(400 * time.Millisecond)
	st.Bus.Tick()

	if seen == "" {
		t.Fatal("信号进来了, 主动进程什么都没收到 —— 这条路断在某一段")
	}
	if !strings.Contains(seen, "lock.opened") {
		t.Fatalf("门开了没进摘要:\n%s", seen)
	}
	// **S56 那一段**: 手机只报经纬度, 地名要在观察链里查出来
	if !strings.Contains(seen, "没人") {
		t.Fatalf("'家里现在没人'没跟着摘要走 —— S56 那条接缝又断了:\n%s", seen)
	}
}

// 关注命中要直接说, 而且不占额度 —— S33 那五步里的一步
func TestWatchHitRidesTheSameWiring(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	places := NewPlaces(log)
	places.Add("家", 31.88, 117.28, 0)
	watches := NewWatches(log)
	watches.UsePlaces(places)
	watches.Add("place.left", "家", "", "离开家告诉我")

	var hits []string
	st := BuildSenseStack(SenseStack{
		Log: log, Places: places, Watches: watches,
		Budget: NewInterruptBudget(log, 3, nil),
		Window: 200 * time.Millisecond, Lateness: 20 * time.Millisecond,
		OnWatchHit: func(s string) { hits = append(hits, s) },
		ToProcess:  func(string) bool { return true },
	})
	now := time.Now().UnixMilli()
	st.Bus.Ingest(abi.Signal{ID: "l2", Source: "phone.mk", Kind: "place.left",
		At: now, KnownAt: now,
		Body: map[string]any{"lat": 31.88, "lon": 117.28}})
	time.Sleep(400 * time.Millisecond)
	st.Bus.Tick()

	if len(hits) != 1 {
		t.Fatalf("他明确要盯的事没被直接说出来(命中 %d 次)", len(hits))
	}
}

func postBatch(t *testing.T, api *SenseAPI, body string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+api.Addr()+"/signals",
		bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// **编排漏了标记"这儿是家"的事件.**
//
// 整条链第一次自己跑完时抓到的: 有事那天 23 条信号、主动进程判了 20 次、
// 那件★也确确实实发生了(4 条 lock.opened), 而**"家里现在没人"一次都
// 没出现** —— 于是它一次都没开口, 验收判"开口 0 次, 期望 1 次".
//
// 问题不在判断, 在**编排**: 脚本喂了手机的坐标, 却从来没告诉 OS
// "这儿是家". 而 S56 那条判据是对的 —— 查不到就是查不到,
// 不能拿一个随便的坐标当家(用户还没认过家就误报, 他会当场关掉通知).
//
// 正常流程会将"这儿是家"落为一条 place.named.
// 验收重放一天真实的生活时也必须重放这条事件.
func TestSeedHomeMakesPresenceWork(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	// **标记"这儿是家"的操作** —— 落一条 place.named
	SeedPlace(log, "家", 31.88, 117.28)

	places := NewPlaces(log)
	places.Restore(log.Replay(signalPID, 0))
	if len(places.Known()) != 1 {
		t.Fatalf("认得 %d 个地方 —— 那一句没落下去", len(places.Known()))
	}

	var seen string
	st := BuildSenseStack(SenseStack{
		Log: log, Places: places,
		Watches: NewWatches(log), Budget: NewInterruptBudget(log, 3, nil),
		Window: 150 * time.Millisecond, Lateness: 20 * time.Millisecond,
		ToProcess: func(text string) bool { seen = text; return true },
	})
	now := time.Now().UnixMilli()
	// 手机真正投的样子: 只有经纬度
	st.Bus.Ingest(abi.Signal{ID: "l1", Source: "phone.mk", Kind: "place.left",
		At: now - 60000, KnownAt: now - 60000,
		Body: map[string]any{"lat": 31.88, "lon": 117.28}})
	st.Bus.Ingest(abi.Signal{ID: "d1", Source: "ha.lock.front", Kind: "lock.opened",
		At: now, KnownAt: now,
		Body: map[string]any{"entity": "lock.front", "state": "unlocked"}})
	time.Sleep(300 * time.Millisecond)
	st.Bus.Tick()

	if !strings.Contains(seen, "没人") {
		t.Fatalf("认过家了, 摘要里还是没有'家里现在没人':\n%s", seen)
	}
}

// 没认过家的时候照样是"不知道" —— 这条不能因为加了播种就松掉
func TestWithoutSeedItStaysUnknown(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	places := NewPlaces(log)
	var seen string
	st := BuildSenseStack(SenseStack{
		Log: log, Places: places,
		Watches: NewWatches(log), Budget: NewInterruptBudget(log, 3, nil),
		Window: 150 * time.Millisecond, Lateness: 20 * time.Millisecond,
		ToProcess: func(text string) bool { seen = text; return true },
	})
	now := time.Now().UnixMilli()
	st.Bus.Ingest(abi.Signal{ID: "l2", Source: "phone.mk", Kind: "place.left",
		At: now - 60000, KnownAt: now - 60000,
		Body: map[string]any{"lat": 31.88, "lon": 117.28}})
	st.Bus.Ingest(abi.Signal{ID: "d2", Source: "ha.lock.front", Kind: "lock.opened",
		At: now, KnownAt: now, Body: map[string]any{"entity": "lock.front"}})
	time.Sleep(300 * time.Millisecond)
	st.Bus.Tick()

	if strings.Contains(seen, "没人") {
		t.Fatalf("还没认过家就说家里没人:\n%s", seen)
	}
}
