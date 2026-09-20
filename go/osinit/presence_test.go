package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func presenceAt(t *testing.T, now *time.Time) *Presence {
	t.Helper()
	return NewPresence(func() time.Time { return *now })
}

func pSig(src, kind, place string, at int64) abi.Signal {
	body := map[string]any{}
	if place != "" {
		body["place"] = place
	}
	return abi.Signal{ID: kind + place, Source: src, Kind: kind,
		At: at, KnownAt: at, Body: body}
}

// **"不知道"和"没人在家"必须分开.**
//
// 这是这块东西最要紧的一条判据. 默认成"没人在家"的话, 每一次开门
// 都会变成一次警报 —— 而一个刚装上系统、还没接手机的人, 家里**永远**
// 是"没人", 于是他第一天就会把通知关掉.
//
// 而通知一旦被关掉, 真正重要的那次也到不了他 —— 那正是整个打扰预算
// 存在的理由.
func TestUnknownIsNotAway(t *testing.T) {
	now := time.Now()
	p := presenceAt(t, &now)
	note := p.Note()
	if strings.Contains(note, "没人") || strings.Contains(note, "不在家") {
		t.Fatalf("一条位置信息都没有, 却说家里没人: %q —— "+
			"刚装上系统的人第一天就会把通知关掉", note)
	}
	if note != "" {
		t.Fatalf("不知道的时候该闭嘴, 而不是编一句: %q", note)
	}
}

// 手机说离开了家 —— 那就是没人在家, 而且要说清什么时候走的
func TestPhoneLeavingHomeMeansAway(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	p := presenceAt(t, &now)
	p.Observe(pSig("phone.mk", "place.left", "家",
		now.Add(-3*time.Hour).UnixMilli()))

	note := p.Note()
	if !strings.Contains(note, "没人") {
		t.Fatalf("手机离开家了却没说家里没人: %q", note)
	}
	// 断"说了多久", 不断具体的渲染格式 —— humanDuration 打的是 "3.0 小时"
	if !strings.Contains(note, "小时") {
		t.Fatalf("没说什么时候走的: %q —— 走了三分钟和走了三小时, "+
			"同一件事的分量完全不一样", note)
	}
}

// 回家了要翻过来 —— 不然它会一直以为家里没人
func TestComingHomeFlipsIt(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	p := presenceAt(t, &now)
	p.Observe(pSig("phone.mk", "place.left", "家", now.Add(-3*time.Hour).UnixMilli()))
	p.Observe(pSig("phone.mk", "place.arrived", "家", now.Add(-time.Minute).UnixMilli()))
	if note := p.Note(); strings.Contains(note, "没人") {
		t.Fatalf("人回来了还说没人: %q", note)
	}
}

// HA 那侧的 person/device_tracker 也算 —— 不是每个人都装我们的手机端
func TestHAPersonCountsToo(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	p := presenceAt(t, &now)
	p.Observe(abi.Signal{ID: "x", Source: "ha.person.mk", Kind: "device.state",
		At: now.Add(-time.Hour).UnixMilli(), KnownAt: now.Add(-time.Hour).UnixMilli(),
		Body: map[string]any{"entity": "person.mk", "state": "not_home"}})
	if note := p.Note(); !strings.Contains(note, "没人") {
		t.Fatalf("HA 说人不在家, 却没算进来: %q", note)
	}
}

// **陈旧的位置不能当现在用.**
//
// S42/S43 已经证明采集端会整段整段地瞎掉(手机没电、令牌过期).
// 拿一个三天前的"他离开了"当成"现在家里没人", 会让系统在他明明
// 在家的时候对着每一次开门喊警报 —— 而他没有任何办法知道为什么.
func TestStalePositionIsNotUsed(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	p := presenceAt(t, &now)
	p.Observe(pSig("phone.mk", "place.left", "家",
		now.Add(-72*time.Hour).UnixMilli()))
	if note := p.Note(); note != "" {
		t.Fatalf("拿三天前的位置当现在: %q —— 他可能早就回来了, "+
			"只是手机没电了", note)
	}
}

// **真手机上那条路是断的.**
//
// S49 的在家判断读的是 body["place"], 而**手机采集器只报经纬度**:
// 地名是 OS 侧 places.Annotate 在**摘要**上贴的, 而 Presence.Observe
// 挂在投递路径上 —— 那时候还没贴.
//
// 于是 S49 只在我手工投一条带 place 的信号时成立. 真手机接上去,
// "家里有没有人"永远是"不知道", 而整个 S49 就白做了 ——
// 而且它是**静默的**: 系统照常跑, 只是那句话永远不出现.
func TestPresenceWorksWithRealPhoneSignals(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	places := NewPlaces(nil)
	places.Add("家", 31.88, 117.28, 0)

	p := NewPresence(func() time.Time { return now })
	p.UsePlaces(places)

	// 手机真正投出来的样子: 只有经纬度, 没有地名
	p.Observe(abi.Signal{ID: "l1", Source: "phone.mk", Kind: "place.left",
		At:   now.Add(-2 * time.Hour).UnixMilli(),
		Body: map[string]any{"lat": 31.88, "lon": 117.28, "stayedMs": 3600000}})

	if note := p.Note(); !strings.Contains(note, "没人") {
		t.Fatalf("真手机的信号(只有经纬度)判不出在不在家: %q", note)
	}
}

// 离开的是公司, 不是家 —— 那跟"家里有没有人"无关
func TestLeavingSomewhereElseIsNotAboutHome(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	places := NewPlaces(nil)
	places.Add("家", 31.88, 117.28, 0)
	places.Add("公司", 31.86, 117.28, 0)

	p := NewPresence(func() time.Time { return now })
	p.UsePlaces(places)
	p.Observe(abi.Signal{ID: "l2", Source: "phone.mk", Kind: "place.left",
		At:   now.Add(-time.Hour).UnixMilli(),
		Body: map[string]any{"lat": 31.86, "lon": 117.28}})

	if note := p.Note(); note != "" {
		t.Fatalf("离开公司被当成了家里没人: %q", note)
	}
}

// 没认过"家"这个地方时, 照样是"不知道" —— 不能拿一个随便的坐标当家
func TestUnnamedHomeStaysUnknown(t *testing.T) {
	now := time.Now()
	p := NewPresence(func() time.Time { return now })
	p.UsePlaces(NewPlaces(nil)) // 一个地点都没认过
	p.Observe(abi.Signal{ID: "l3", Source: "phone.mk", Kind: "place.left",
		At:   now.Add(-time.Hour).UnixMilli(),
		Body: map[string]any{"lat": 31.88, "lon": 117.28}})
	if note := p.Note(); note != "" {
		t.Fatalf("还没认过家, 却说家里没人: %q", note)
	}
}

// **半夜重启一次, "家里有没有人"就回到了"不知道".**
//
// Presence 的状态是从信号**流过时**攒出来的(Observe). 而重启之后:
// RecoverPending 只捞"最后一份摘要之后"的信号 —— 三小时前那条
// "离开家了"早就被总结过了, 不会重放.
//
// 于是升级/崩溃/断电之后, 那件★(没人在家时门开了)不会被说 ——
// **而 S49 的全部价值就在那一件上**. 症状照例是静默的: 系统照常跑,
// 只是那句话不再出现.
//
// 这是"重启之后没被恢复的状态"的**第四例**(S26 额度、S30 静音表、
// S31 去重表、现在 presence). 而 S32 那道闸看不出来 ——
// 它查的是"每种事件有没有人读", 而 place.left 是 EvSignal, 有人读;
// **漏的是"从事件推导出来的状态有没有人恢复"**.
func TestPresenceSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	places := NewPlaces(log)
	places.Add("家", 31.88, 117.28, 0)

	// 三小时前离开家 —— 而且这条早就被总结过了(后面有一份摘要)
	log.Append(signalPID, abi.EvSignal, map[string]any{
		"id": "l1", "source": "phone.mk", "kind": "place.left",
		"at":   float64(now.Add(-3 * time.Hour).UnixMilli()),
		"body": map[string]any{"lat": 31.88, "lon": 117.28},
	})
	log.Append(signalPID, abi.EvSignalDigest, map[string]any{"n": 1})

	// 重启: 全新的 Presence, 只有账本
	p := NewPresence(func() time.Time { return now })
	p.UsePlaces(places)
	p.Restore(log.Replay(signalPID, 0))

	if note := p.Note(); !strings.Contains(note, "没人") {
		t.Fatalf("重启之后回到了'不知道': %q —— "+
			"那件'没人在家时门开了'从此不会被说, 而它是 S49 的全部价值", note)
	}
}

// 陈旧的照样不算 —— 重启不该让一条过期的位置突然变得可信
func TestRestoreRespectsStaleness(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	places := NewPlaces(log)
	places.Add("家", 31.88, 117.28, 0)
	log.Append(signalPID, abi.EvSignal, map[string]any{
		"id": "old", "source": "phone.mk", "kind": "place.left",
		"at":   float64(now.Add(-72 * time.Hour).UnixMilli()),
		"body": map[string]any{"lat": 31.88, "lon": 117.28},
	})
	p := NewPresence(func() time.Time { return now })
	p.UsePlaces(places)
	p.Restore(log.Replay(signalPID, 0))
	if note := p.Note(); note != "" {
		t.Fatalf("重启把三天前的位置当成了现在: %q", note)
	}
}

// 恢复出来的要是**最后一条**, 不是账本里第一条 ——
// 出门又回来的话, 装回"出门了"就等于凭空说家里没人
func TestRestoreTakesTheLatest(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	places := NewPlaces(log)
	places.Add("家", 31.88, 117.28, 0)
	for _, c := range []struct {
		kind string
		ago  time.Duration
	}{{"place.left", 3 * time.Hour}, {"place.arrived", time.Hour}} {
		log.Append(signalPID, abi.EvSignal, map[string]any{
			"id": c.kind, "source": "phone.mk", "kind": c.kind,
			"at":   float64(now.Add(-c.ago).UnixMilli()),
			"body": map[string]any{"lat": 31.88, "lon": 117.28},
		})
	}
	p := NewPresence(func() time.Time { return now })
	p.UsePlaces(places)
	p.Restore(log.Replay(signalPID, 0))
	if note := p.Note(); strings.Contains(note, "没人") {
		t.Fatalf("他回来了, 恢复出来却说没人: %q", note)
	}
}

// 删掉那个记错的"家"之后, 从它推出来的结论也得跟着不算数.
//
//	地点表里一条都没有时, what_now 仍可能在说
//	"家里现在有人：手机说你到家了" —— 前提没了, 结论还在.
//	而它不会报错, 只会一直答错(所有跟"家里有没有人"有关的判断都跟着错)
func TestForgettingHomeInvalidatesPresence(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local)
	places := NewPlaces(nil)
	places.Add("家", 31.88, 117.28, 0)

	p := NewPresence(func() time.Time { return now })
	p.UsePlaces(places)
	p.Observe(abi.Signal{ID: "a1", Source: "phone.mk", Kind: "place.arrived",
		At:   now.Add(-time.Hour).UnixMilli(),
		Body: map[string]any{"lat": 31.88, "lon": 117.28}})
	if note := p.Note(); !strings.Contains(note, "有人") {
		t.Fatalf("到家没判出来: %q", note)
	}

	places.Forget("家")
	if note := p.Note(); note != "" {
		t.Fatalf("「家」都删了, 还在说: %q", note)
	}

	// **自愈**: 他重新教一个"家", 这条又该成立 —— 删的时候去改状态
	// 的话就没有这一半了
	places.Add("家", 31.88, 117.28, 0)
	if note := p.Note(); !strings.Contains(note, "有人") {
		t.Fatalf("重新教了「家」之后又判不出来了: %q", note)
	}
}
