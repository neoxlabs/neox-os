package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func atClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// 过期的事实**不出现**, 不是降权.
//
//	拿三小时前的位置回答"我现在在哪", 比说"不知道"糟得多:
//	不知道会让人再问一次, 一个自信的错误答案不会
func TestWorldDropsStaleFacts(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{
		Source: "phone", Kind: "place.arrived",
		At:   now.Add(-3 * time.Hour).UnixMilli(),
		Body: map[string]any{"place": "公司"},
	})
	if got := w.Snapshot(""); len(got) != 0 {
		t.Fatalf("三小时前的位置还在回答现在: %+v", got)
	}
	if !strings.Contains(w.Text(""), "什么都不知道") {
		t.Fatalf("没有可用事实时该说不知道, 得到: %q —— 空着的话模型会自己填一个", w.Text(""))
	}
}

// 门开着就一直开着, 直到有人关它 —— 这类是**状态**不是采样
func TestWorldKeepsStateFactsForever(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{
		Source: "ha", Kind: "door.opened",
		At: now.Add(-8 * time.Hour).UnixMilli(),
	})
	if len(w.Snapshot("")) != 1 {
		t.Fatal("门的状态被当成过期采样丢了 —— 而没人关过它")
	}
}

// 采集端自报有效期就听它的 —— 一份逐小时的天气预报知道自己什么时候作废
func TestWorldHonorsDeclaredValidity(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{
		Source: "weather", Kind: "weather.now",
		At:   now.Add(-90 * time.Minute).UnixMilli(),
		Body: map[string]any{"text": "小雨", "validSec": 3 * 3600},
	})
	if len(w.Snapshot("")) != 1 {
		t.Fatal("采集端说了三小时有效, 却按缺省的一小时判过期了")
	}
}

// **旧的不覆盖新的**: 补传是常态, 而那批里最后到达的未必是最晚发生的
func TestWorldIgnoresOutOfOrderBackfill(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{Source: "phone", Kind: "place.arrived",
		At: now.Add(-1 * time.Minute).UnixMilli(), Body: map[string]any{"place": "公司"}})
	// 补传上来一条更早的
	w.Observe(abi.Signal{Source: "phone", Kind: "place.arrived",
		At: now.Add(-8 * time.Minute).UnixMilli(), Body: map[string]any{"place": "家"}})

	if got := w.Text(""); !strings.Contains(got, "公司") {
		t.Fatalf("补传的旧位置把新的盖掉了: %q", got)
	}
}

// 位置要说**地名**, 不说坐标 —— 坐标对用户是零信息, 而模型拿坐标会编地名
func TestWorldSaysPlaceNameNotCoordinates(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	places := NewPlaces(log)
	places.Observe(abi.Signal{Source: "phone", Kind: "location",
		At: now.UnixMilli(), Body: map[string]any{"lat": 31.86, "lon": 117.28}})
	if _, err := places.NameHere("家", 0); err != nil {
		t.Fatalf("起名失败: %v", err)
	}

	w := NewWorld(atClock(now))
	w.Use(places, nil, nil)
	w.Observe(abi.Signal{Source: "phone", Kind: "place.arrived",
		At: now.UnixMilli(), Body: map[string]any{"lat": 31.86, "lon": 117.28}})

	got := w.Text("")
	if !strings.Contains(got, "家") {
		t.Fatalf("认得这个地方却没说地名: %q", got)
	}
	if strings.Contains(got, "31.86") {
		t.Fatalf("把坐标报给了模型: %q —— 它会在上面编一个地名出来", got)
	}
}

// 认不出的地方就说认不出 —— 别报坐标, 也别编
func TestWorldAdmitsUnknownPlace(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{Source: "phone", Kind: "place.arrived",
		At: now.UnixMilli(), Body: map[string]any{"lat": 1.0, "lon": 2.0}})
	if got := w.Text(""); !strings.Contains(got, "还没起过名") {
		t.Fatalf("认不出的地方该照实说: %q", got)
	}
}

// "我今天还有什么事"的答案就在闹钟表里 —— bot 够不着的话只能说不知道,
// 而 OS 明明知道
func TestWorldKnowsNextReminder(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	timers := NewTimers(log, func() time.Time { return now }, func(Wake, int64) {})
	if _, err := timers.Set("", now.Add(45*time.Minute).UnixMilli(), "去拿快递"); err != nil {
		t.Fatalf("设不了: %v", err)
	}

	w := NewWorld(atClock(now))
	w.Use(nil, nil, timers)
	got := w.Text("")
	if !strings.Contains(got, "去拿快递") || !strings.Contains(got, "45") {
		t.Fatalf("下一个提醒没进世界模型: %q", got)
	}
}

// 每条事实都要带"什么时候知道的" —— 一条 40 分钟前的和一条刚到的,
// 对判断的分量完全不同.
//
// ── 而且必须是**钟点, 不是"几分钟前"** ──
//
//	相对时间在上下文里会烂: "40 分钟前"这句话在它被生成的那一刻是对的,
//	而工具结果会留在上下文里 —— 二十轮之后它还写着"40 分钟前", 而实际
//	已经两小时了。模型照读不误, 于是拿一条老位置回答"你现在在哪",
//	答得很有把握。
//
//	用户的原话: "它经常用上下文上已经存在的老的错误的数据偷偷直接
//	给我回"。
//
//	绝对时间不会烂, 而每一轮用户消息尾巴上都挂着"[现在 …]"
//	(见 agent.nowNote), 模型一减就知道多旧。
func TestWorldTextCarriesAge(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{Source: "ha", Kind: "door.opened",
		At: now.Add(-40 * time.Minute).UnixMilli()})
	got := w.Text("")
	if !strings.Contains(got, "08:20") {
		t.Fatalf("没说这条是什么时候知道的: %q", got)
	}
	if strings.Contains(got, "分钟前") || strings.Contains(got, "小时前") {
		t.Fatalf("用了相对时间 —— 它在上下文里会烂成谎话: %q", got)
	}
	if !strings.Contains(got, "ha") {
		t.Fatalf("没说这条是谁报的: %q —— 事实对不上的时候这是唯一能查下去的东西", got)
	}
}

// **人一坐下来, OS 不该就"忘了"他在哪.**
//
//	采集端是按移动触发的(人不动就不报). 于是一段沉默有两种解释:
//	他没动, 或者采集端死了. 心跳把两者分开 —— 它还在报到, 就说明
//	它没报新位置只是因为没有新的可报.
func TestWorldTrustsALiveCollector(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	beating := true
	w.UseAlive(func(source string) bool { return beating })

	// 半小时前报的位置 —— 早过了 10 分钟的有效期
	w.Observe(abi.Signal{Source: "phone", Kind: "place.arrived",
		At:   now.Add(-30 * time.Minute).UnixMilli(),
		Body: map[string]any{"place": "公司"}})

	if got := w.Text(""); !strings.Contains(got, "公司") {
		t.Fatalf("采集端还活着, 它报的最后一个位置却被判过期了: %q\n"+
			"—— 而人不动恰恰是一天里的大部分时候", got)
	}

	// 采集端不报到了 —— 那这条位置就不能再算数
	beating = false
	if got := w.Text(""); strings.Contains(got, "公司") {
		t.Fatalf("采集端已经不报到了, 半小时前的位置还在回答现在: %q", got)
	}
}

// 活着也有上限 —— 一个"活着但瞎了"的采集端(定位权限被撤了)照样报到,
// 只是再也不报位置. 没有上限的话, 昨天的位置会一直被当成现在的
func TestWorldStillCapsALiveCollector(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	w.UseAlive(func(string) bool { return true })
	w.Observe(abi.Signal{Source: "phone", Kind: "place.arrived",
		At:   now.Add(-20 * time.Hour).UnixMilli(),
		Body: map[string]any{"place": "公司"}})
	if got := w.Text(""); strings.Contains(got, "公司") {
		t.Fatalf("20 小时前的位置还在回答现在: %q", got)
	}
}

// **世界模型是账本的一个投影 —— 那它就必须能从账本重建.**
//
//	不重建的话, 重启之后 OS 就不知道人在哪了, 而且**不会自己好起来**:
//	采集端按移动触发, 人不动就不报, 于是它可能一整天都不知道.
//	因此系统可能整天都不知道人在哪.
func TestWorldRebuildsFromLedger(t *testing.T) {
	now := time.Now()
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second})
	bus.Ingest(abi.Signal{ID: "l1", Source: "phone", Kind: "place.arrived",
		At:   now.Add(-3 * time.Minute).UnixMilli(),
		Body: map[string]any{"place": "公司"}})

	var evs []abi.Event
	for _, b := range log.Snapshot() {
		evs = append(evs, b...)
	}
	// 重启: 一个全新的世界模型, 只有账本
	next := NewWorld(atClock(now))
	if n := next.Restore(evs); n == 0 {
		t.Fatal("账本里有位置, 一条都没装回来")
	}
	if got := next.Text(""); !strings.Contains(got, "公司") {
		t.Fatalf("重启之后不知道人在哪了: %q", got)
	}
}

// 只读最近的 —— 再往前的事实无论如何都不该回答"现在怎么样",
// 读多了只是白扫账本
func TestWorldRestoreIgnoresAncientHistory(t *testing.T) {
	now := time.Now()
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	bus := NewSignalBus(log, func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second})
	bus.Ingest(abi.Signal{ID: "old", Source: "phone", Kind: "place.arrived",
		At:   now.Add(-30 * time.Hour).UnixMilli(),
		Body: map[string]any{"place": "上个月那个地方"}})

	var evs []abi.Event
	for _, b := range log.Snapshot() {
		evs = append(evs, b...)
	}
	next := NewWorld(atClock(now))
	next.Restore(evs)
	if got := next.Text(""); strings.Contains(got, "上个月") {
		t.Fatalf("30 小时前的位置被装回来当成现在: %q", got)
	}
}

// **同一件事的几种叫法要合成一条.**
//
//	采集端报 location(定期的位置), 也报 place.arrived / place.left.
//	三个都是"他在哪" —— 不合并的话世界模型里同时挂着两条位置,
//	而它们互相矛盾的时候没有任何一处说得清该信哪个.
//	结果会连续出现两行"在一个还没起过名的地方".
func TestWorldMergesLocationAndPlace(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{Source: "phone", Kind: "location", At: now.UnixMilli(),
		Body: map[string]any{"lat": 1.0, "lon": 2.0}})
	w.Observe(abi.Signal{Source: "phone", Kind: "place.arrived",
		At: now.UnixMilli(), Body: map[string]any{"place": "公司"}})

	got := w.Snapshot("")
	n := 0
	for _, f := range got {
		if f.Key == "place" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一个人同时挂着 %d 条位置: %+v", n, got)
	}
}

// **那句话要读的时候才算.**
//
//	地名是逆地理编码后台查回来的(不能在信号路径上同步调). 在信号进来
//	那一刻算的话, 那时候缓存还是空的, 于是这句话永远定死成
//	"在一个还没起过名的地方" —— 而地名两秒后就到了, 只是再也没人算第二遍.
func TestWorldTextPicksUpLateGeocoding(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	ready := false
	w.UseNearby(func(lat, lon float64) (string, bool) {
		if !ready {
			return "", false
		}
		return "蚌山区东海大道", true
	})
	w.Observe(abi.Signal{Source: "phone", Kind: "location", At: now.UnixMilli(),
		Body: map[string]any{"lat": 32.919, "lon": 117.357}})

	if got := w.Text(""); !strings.Contains(got, "还没起过名") {
		t.Fatalf("地名还没查到, 它却说得出来: %q", got)
	}
	// 后台那次查回来了
	ready = true
	got := w.Text("")
	if !strings.Contains(got, "东海大道") {
		t.Fatalf("地名到了却没用上: %q —— 那句话是在信号进来那一刻定死的", got)
	}
	// 有地址了就只说地址 —— 挂着"还没起过名", 它每次都要解释一遍
	// "不是家也不是公司"
	if strings.Contains(got, "起过名") {
		t.Fatalf("有地址了还在说没起过名: %q", got)
	}
}

// "连着 一种_5G" 被它读成了"没连 WiFi"
func TestWorldTextSaysWifi(t *testing.T) {
	now := time.Now()
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{Source: "phone", Kind: "network.wifi", At: now.UnixMilli(),
		Body: map[string]any{"ssid": "一种_5G", "text": "连着 一种_5G"}})
	if got := w.Text(""); !strings.Contains(got, "WiFi「一种_5G」") {
		t.Fatalf("%q", got)
	}
}

// 隔天的要带日期 —— 光看"08:15"分不出是今天早上还是昨天早上,
// 而那两件事差 24 小时
func TestWorldTextMarksAnotherDay(t *testing.T) {
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
	w := NewWorld(atClock(now))
	w.Observe(abi.Signal{Source: "ha", Kind: "door.opened",
		At: now.Add(-20 * time.Hour).UnixMilli()})
	if got := w.Text(""); !strings.Contains(got, "09-09") {
		t.Fatalf("昨天的事实没带日期, 看着跟今天早上一样: %q", got)
	}
}

// 巡检的题目是给判断者的, 三百字 —— 进"现在什么情况"只报名目
func TestWakeLabelShortensThinkingWakes(t *testing.T) {
	long := "早上到校巡检：今天若是工作日(date +%u 为 1-5)，读工作区 SCHEDULE.md 和 ALARM.md。任务 A…"
	if got := wakeLabel(Wake{Text: long, Think: true}); got != "例行巡检「早上到校巡检」" {
		t.Fatalf("%q", got)
	}
	// 给人看的原话一个字都不能动
	if got := wakeLabel(Wake{Text: "该出门了。路上 30 分钟"}); got != "该出门了。路上 30 分钟" {
		t.Fatalf("%q", got)
	}
}

// 手机开车时会给位置带一句"在动（约 40 km/h）" —— 那是补充, 地名不能被它盖掉
func TestLocationTextAddsToPlaceName(t *testing.T) {
	now := time.Date(2026, 9, 11, 8, 10, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	places := NewPlaces(log)
	places.Add("家", 32.963, 117.3507, 600)
	w := NewWorld(atClock(now))
	w.Use(places, nil, nil)
	w.Observe(abi.Signal{Source: "phone", Kind: "location", At: now.UnixMilli(),
		Body: map[string]any{"lat": 32.963, "lon": 117.3507, "speed": 11.2, "text": "在动（约 40 km/h）"}})
	got := w.Text("")
	if !strings.Contains(got, "在家，在动（约 40 km/h）") {
		t.Fatalf("地名被那句话盖掉了: %q", got)
	}
	// 别的信号给了话照旧用它的
	w.Observe(abi.Signal{Source: "phone", Kind: "bluetooth.audio", At: now.UnixMilli(),
		Body: map[string]any{"name": "BYD", "car": true, "text": "连着车载蓝牙「BYD」（多半在开车）"}})
	if got := w.Text(""); !strings.Contains(got, "连着车载蓝牙「BYD」") {
		t.Fatalf("%q", got)
	}
}
