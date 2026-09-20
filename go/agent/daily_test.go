package agent

import (
	"strings"
	"testing"
	"time"
)

// 没有闹钟服务的机器上**不许有这个工具**.
//
//	提供一个必然失败的工具比不提供更糟: 模型会调用它, 然后声称已经
//	"我记下了" —— 那是这个仓库里最糟的一类失败(对用户许了做不到的诺)
func TestDailyOnlyWhenAvailable(t *testing.T) {
	off := NewToolSet(WithDaily(DefaultTools(), func(int64, string) error { return nil }, nil))
	if _, ok := off.Get("every_day"); ok {
		t.Error("没有闹钟服务却挂出了 every_day")
	}
	on := NewToolSet(WithDaily(DefaultTools(),
		func(atMs int64, text string) error { return nil },
		func(atMs int64, what string) error { return nil }))
	if _, ok := on.Get("remind_me"); !ok {
		t.Error("有闹钟服务却没挂出 remind_me")
	}
	// **"每天几点"是 remind_me 的一个参数, 不是第二个工具**:
	// 四个"以后提醒我"的工具加起来 2752 字节, 而模型每轮都要在里面挑
	if _, ok := on.Get("every_day"); ok {
		t.Error("every_day 又单独成一个工具了 —— 它该并进 remind_me")
	}
	// **轮询那个工具不许再回来**: 为了盯住一小时每 10 分钟醒一次,
	// 一天会产生 144 次空转, 而账本里还会躺着三条几乎一样的巡检
	if _, ok := on.Get("check_every"); ok {
		t.Error("check_every 又回来了 —— 轮询这个形态本身就是错的")
	}
}

func daily(t *testing.T, at, what string) (string, error, int64) {
	t.Helper()
	var got int64
	ts := NewToolSet(WithDaily(DefaultTools(),
		func(int64, string) error { return nil },
		func(ms int64, w string) error { got = ms; return nil }))
	tool, _ := ts.Get("remind_me")
	out, err := tool.Run(Toolbox{}, map[string]any{"daily": at, "text": what})
	return out, err, got
}

// 定的是**下一个** 06:40, 而且是本地时间 —— 时区不对的话它会差几个小时,
// 而一处都不会报错(见 osinit/zone.go)
func TestEveryDayPicksNextLocalOccurrence(t *testing.T) {
	_, err, ms := daily(t, "06:40", "算今天该几点出门")
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(ms)
	if at.Hour() != 6 || at.Minute() != 40 {
		t.Fatalf("定在了 %s, 该是 06:40", at.Format("15:04"))
	}
	if !at.After(time.Now()) {
		t.Fatalf("定在了过去: %s", at.Format("01-02 15:04"))
	}
	if at.Sub(time.Now()) > 24*time.Hour {
		t.Fatalf("定到了一天以后: %s", at.Format("01-02 15:04"))
	}
}

// **只收 HH:MM**: "6" 是六点还是六分钟后, 模型和人会各理解一半,
// 而这条设定活得比对话长 —— 理解错了要等到第二天才发现
func TestEveryDayRejectsVagueTime(t *testing.T) {
	for _, bad := range []string{"6", "早上六点", "25:00", "06:99", ""} {
		if _, err, _ := daily(t, bad, "看一眼"); err == nil {
			t.Errorf("%q 该被拒", bad)
		}
	}
}

// what 是**给提醒任务自己的题目**, 不是给用户的回复
func TestEveryDayNeedsWhat(t *testing.T) {
	if _, err, _ := daily(t, "06:40", "  "); err == nil {
		t.Fatal("what 空着该被拒 —— 到点了看什么?")
	}
}

// 回执要说清第一次是什么时候 —— 否则提醒内容会变成"明早叫你",
// 而其实定到了后天
func TestEveryDaySaysWhenFirst(t *testing.T) {
	out, err, _ := daily(t, "06:40", "算今天该几点出门")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "第一次是") {
		t.Errorf("回执没说第一次什么时候: %s", out)
	}
}
