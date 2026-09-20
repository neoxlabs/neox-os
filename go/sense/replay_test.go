package sense

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"
)

// 用**真实的 HA 历史**验降噪 —— 第一次有真实数据.
//
// ── 为什么这份数据虽然"空"却值钱 ──
//
// 样本是一台几乎没接设备的 HA(14 个实体, 12 天), 看起来没什么可验的.
// 但它里面藏着 HA 最典型的一种行为, 而且是**合成数据造不出来的**:
//
//	sun.sun 写了 1359 行, 状态从头到尾只有 below_horizon / above_horizon
//
// 也就是说 HA 的 states 表**每次属性变化也写一行** —— 太阳高度角每几分钟
// 变一次, 状态一天只翻两次. 一个"订阅 state_changed 就转发"的桥接,
// 会把这 1359 次当成 1359 件事报上去.
//
// 这正是 worthReporting 里"状态字符串没变就不报"那条挡的东西.
// 之前只有我编的合成数据能证明它有用, 现在有真的了.
//
// ── 这不能替代真实的一天 ──
//
// 它验的是"属性抖动会不会被挡住", 验不了"停留半径该是 120 米还是 80 米"
// —— 后者要真实的设备和真实的作息.

type haChange struct {
	Entity string `json:"entity"`
	State  string `json:"state"`
	TS     int64  `json:"ts"`
}

func loadRealHA(t *testing.T) []haChange {
	t.Helper()
	raw, err := os.ReadFile("testdata/ha_real.json")
	if err != nil {
		t.Skip("没有真实 HA 样本(tools/ha_export.py 生成)")
	}
	var v struct {
		Changes []haChange `json:"changes"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	sort.Slice(v.Changes, func(i, j int) bool { return v.Changes[i].TS < v.Changes[j].TS })
	return v.Changes
}

// ReplayHA 把变更日志重放成"轮询看到的快照序列", 喂给桥接.
//
// **必须重建成快照**: 桥接是轮询模型(拿这一次的全量跟上一次比),
// 而 HA 的表是变更日志. 直接把日志当快照喂进去, 验的就是另一个东西了.
func replayHA(t *testing.T, changes []haChange, pollMs int64) (polls, obs int, sigs int) {
	p, o, n, _ := replayHADetail(t, changes, pollMs)
	return p, o, n
}

func replayHADetail(t *testing.T, changes []haChange, pollMs int64) (
	polls, obs, sigs int, byEntity map[string]int) {
	t.Helper()
	cur := map[string]string{}
	i := 0
	simNow := time.UnixMilli(changes[0].TS)
	b := NewHABridge(HAConfig{
		NumericEpsilon: 0.05,
		Now:            func() time.Time { return simNow },
	})
	byEntity = map[string]int{}
	end := changes[len(changes)-1].TS
	for ts := changes[0].TS; ts <= end; ts += pollMs {
		for i < len(changes) && changes[i].TS <= ts {
			cur[changes[i].Entity] = changes[i].State
			i++
		}
		if len(cur) == 0 {
			continue
		}
		states := make([]HAState, 0, len(cur))
		stamp := time.UnixMilli(ts).UTC().Format("2006-01-02T15:04:05Z")
		for e, s := range cur {
			states = append(states, HAState{EntityID: e, State: s, LastChanged: stamp})
		}
		simNow = time.UnixMilli(ts)
		polls++
		obs += len(states)
		for _, sig := range b.Step(states) {
			sigs++
			byEntity[sig.Kind+" "+str2(sig.Body["entity"])]++
		}
	}
	return polls, obs, sigs, byEntity
}

func str2(v any) string { s, _ := v.(string); return s }

// **真实 HA 的属性抖动必须被挡住.**
//
// 1474 次写入里 1359 次是 sun.sun 的属性更新(状态没变). 一个
// "订阅 state_changed 就转发"的桥接会把它们当成 1359 件事 ——
// 而真实的状态变化只有日出日落, 12 天里二十几次.
func TestRealHAHistoryIsHeavilyFiltered(t *testing.T) {
	changes := loadRealHA(t)
	polls, obs, sigs, byEntity := replayHADetail(t, changes, 10_000) // 10 秒一轮, 跟生产一致

	days := float64(changes[len(changes)-1].TS-changes[0].TS) / 86400000
	t.Logf("真实 HA %.1f 天: %d 条写入 → 轮询 %d 次 / 观察 %d 次 → 信号 %d 条 (%.1f 条/天)",
		days, len(changes), polls, obs, sigs, float64(sigs)/days)

	// **把产出的明细列出来** —— 这才是校准要看的东西:
	// 一个天天变但没人需要知道的实体, 就是该在采集端砍掉的那种
	keys := make([]string, 0, len(byEntity))
	for k := range byEntity {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return byEntity[keys[i]] > byEntity[keys[j]] })
	for _, k := range keys {
		t.Logf("   %4d  %s", byEntity[k], k)
	}

	// ① 降噪要真的发生 —— 1474 条写入不该产出上千条信号
	if sigs > len(changes)/10 {
		t.Fatalf("%d 条写入产出 %d 条信号 —— 属性抖动没被挡住", len(changes), sigs)
	}
	// ② 但不能一条不剩: 日出日落是真实的状态变化, 必须穿过去.
	//    **只断言"信号少"是个坏闸** —— 把什么都丢掉的实现照样能过
	if sigs == 0 {
		t.Fatal("一条都没产出 —— 日出日落是真实的状态变化, 不该被滤掉")
	}
	// ③ 一天几条是合理量级. 一台空 HA 一天报几十条就说明阈值有问题
	if perDay := float64(sigs) / days; perDay > 20 {
		t.Fatalf("一台几乎没接设备的 HA 一天报 %.0f 条 —— 阈值太松", perDay)
	}
}

// 轮询间隔不该改变**产出的信号数量级**.
//
// 桥接是电平触发的: 隔多久看一眼, 看到的"状态变化"应该差不多 ——
// 除非间隔长到跨过了一次完整的变化(开了又关).
// 这条验的是"轮询而不是订阅"那个选择站不站得住.
func TestPollIntervalDoesNotChangeSignalVolume(t *testing.T) {
	changes := loadRealHA(t)
	_, _, fast := replayHA(t, changes, 10_000)
	_, _, slow := replayHA(t, changes, 60_000)

	t.Logf("10 秒一轮 → %d 条; 60 秒一轮 → %d 条", fast, slow)
	if fast == 0 || slow == 0 {
		t.Fatal("有一侧一条都没产出")
	}
	ratio := float64(fast) / float64(slow)
	if ratio > 2 || ratio < 0.5 {
		t.Fatalf("轮询间隔差 6 倍, 信号数差 %.1f 倍 —— "+
			"说明桥接不是电平触发的, '轮询而不是订阅'那个选择就站不住了", ratio)
	}
}
