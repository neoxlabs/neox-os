package osinit

import (
	"testing"
	"time"
)

// 周期闹钟: 响完自己排下一次, 而且**钉在原来的时刻上**.
func TestRepeatKeepsItsHour(t *testing.T) {
	base := time.Date(2026, 9, 10, 6, 59, 0, 0, time.UTC)
	now := base
	var fired []Wake
	tm := NewTimers(nil, func() time.Time { return now },
		func(w Wake, lateMs int64) { fired = append(fired, w) })

	// 每天 7:00
	day := int64(24 * time.Hour / time.Millisecond)
	at := base.Add(time.Minute).UnixMilli()
	if _, err := tm.SetEvery("", at, day, "看一眼今天什么情况", true); err != nil {
		t.Fatal(err)
	}

	// 走到 7:00 —— 响一次
	now = base.Add(time.Minute)
	tm.Tick()
	if len(fired) != 1 {
		t.Fatalf("到点该响一次, 响了 %d 次", len(fired))
	}
	if !fired[0].Think {
		t.Error("think 没传下去 —— 到点该起判断而不是直接说原话")
	}

	// 下一次必须还是 7:00, 不是"从现在起 24 小时"
	p := tm.Pending()
	if len(p) != 1 {
		t.Fatalf("周期闹钟响完该自己排下一次, 现在有 %d 个", len(p))
	}
	nextH := time.UnixMilli(p[0].At).UTC().Hour()
	if nextH != 7 {
		t.Errorf("下一次跑到了 %d 点 —— 周期要从「本该响的那一刻」往后推, "+
			"不是从「现在」往后推, 否则每天都会漂", nextH)
	}
}

// **睡了三天再开机, 不许连响三遍**.
//
//	那三天的判断早就没意义了, 而连响三遍"该出门了"是纯噪音 ——
//	一次性闹钟的"晚响比不响强"在周期上不成立: 周期的意义是
//	"下一次", 不是"补齐所有错过的".
func TestRepeatDoesNotStorm(t *testing.T) {
	base := time.Date(2026, 9, 10, 7, 0, 0, 0, time.UTC)
	now := base.Add(-time.Second)
	var fired int
	tm := NewTimers(nil, func() time.Time { return now },
		func(w Wake, lateMs int64) { fired++ })

	day := int64(24 * time.Hour / time.Millisecond)
	if _, err := tm.SetEvery("", base.UnixMilli(), day, "早上看一眼", true); err != nil {
		t.Fatal(err)
	}

	// 直接跳到三天后
	now = base.Add(72*time.Hour + time.Minute)
	tm.Tick()

	if fired != 1 {
		t.Errorf("睡三天开机响了 %d 次 —— 该只响最近的那一次", fired)
	}
	p := tm.Pending()
	if len(p) != 1 {
		t.Fatalf("该还剩一个待响, 现在 %d 个", len(p))
	}
	// 下一次必须在将来
	if p[0].At <= now.UnixMilli() {
		t.Error("排出来的下一次还在过去 —— 那会在下一跳里再响一遍, 无限循环")
	}
	if h := time.UnixMilli(p[0].At).UTC().Hour(); h != 7 {
		t.Errorf("补过之后跑到了 %d 点, 该还是 7 点", h)
	}
}

// 一分钟以下的周期要当场拒 —— think 那条每响一次是一轮真钱的推理
func TestRepeatTooFastRejected(t *testing.T) {
	now := time.Now()
	tm := NewTimers(nil, func() time.Time { return now }, nil)
	_, err := tm.SetEvery("", now.Add(time.Minute).UnixMilli(), 10_000, "x", true)
	if err == nil {
		t.Fatal("10 秒的周期该被拒 —— 一天 8640 轮推理")
	}
}

// 老接口一个字没改: Set 出来的就是一次性 + 说原话
func TestSetStaysOneShot(t *testing.T) {
	now := time.Now()
	var fired []Wake
	tm := NewTimers(nil, func() time.Time { return now },
		func(w Wake, l int64) { fired = append(fired, w) })
	if _, err := tm.Set("t", now.Add(time.Minute).UnixMilli(), "上车"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	tm.Tick()
	if len(fired) != 1 || fired[0].Think || fired[0].Every != 0 {
		t.Error("Set 出来的必须还是一次性 + 说原话 —— 老账本读回来也是这个")
	}
	if len(tm.Pending()) != 0 {
		t.Error("一次性的响完不该再排下一次")
	}
}
