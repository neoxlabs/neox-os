package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 时区改完, time.Local 跟着变 —— **只改这一处**是有意的:
// 时间进判断的地方二十多处, 穿一个 Location 到每一处去, 漏一处就是
// 静默错误, 而那正是要修的病
func TestZoneChangesLocal(t *testing.T) {
	defer restoreLocal(t)
	z := NewZone(nil)
	if err := z.Set("Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	if got := time.Now().Location().String(); got != "Asia/Shanghai" {
		t.Fatalf("time.Local 没跟着变: %s", got)
	}
	// 同一刻在两个时区里是不同的钟点 —— 这才是它会改掉判断的地方
	at := time.UnixMilli(1789030000000)
	sh := at.Format("15:04")
	if err := z.Set("America/New_York"); err != nil {
		t.Fatal(err)
	}
	if ny := time.UnixMilli(1789030000000).Format("15:04"); ny == sh {
		t.Fatalf("换了时区钟点没变: %s", ny)
	}
}

// 认不出来的当场拒, 而且把认得的几个报出来 ——
// 只说"不认识"的话, 调用方只能再猜一次
func TestZoneRejectsGarbage(t *testing.T) {
	defer restoreLocal(t)
	z := NewZone(nil)
	err := z.Set("UTC+8")
	if err == nil {
		t.Fatal("UTC+8 不是 IANA 名字, 该拒")
	}
	if !contains(err.Error(), "Asia/Shanghai") {
		t.Fatalf("报错里没给出能用的例子: %v", err)
	}
	// 拒掉之后**不能已经改了一半**
	if z.Name() != "UTC" {
		t.Fatalf("拒掉了却把时区改了: %s", z.Name())
	}
}

// 跨重启活着 —— 不然重启一次它又回到 UTC, 日报在凌晨发,
// 而一处都不会报错
func TestZoneSurvivesRestart(t *testing.T) {
	defer restoreLocal(t)
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	z := NewZone(log)
	if err := z.Set("Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	again := NewZone(log)
	again.Restore(flatten(log))
	if again.Name() != "Asia/Tokyo" {
		t.Fatalf("重启后时区丢了: %s", again.Name())
	}
	if !again.Fixed() {
		t.Fatal("重启后'是明确设过的'这件事丢了 —— 手机一报到就会把它顶掉")
	}
}

// 手机报来的自己采纳 —— **让机器自己知道的事别推给人**:
// Docker 里默认 UTC, 而用户手里只有一个手机
func TestZoneAdoptsFromDevice(t *testing.T) {
	defer restoreLocal(t)
	z := NewZone(nil)
	if !z.Adopt("Asia/Shanghai") {
		t.Fatal("手机报的时区没采纳")
	}
	if z.Name() != "Asia/Shanghai" {
		t.Fatalf("采纳了却没生效: %s", z.Name())
	}
	// 同一个再报一遍不算变 —— 每条信号都带 tz, 每条都落账的话一天几百条
	if z.Adopt("Asia/Shanghai") {
		t.Fatal("同一个时区又采纳了一遍")
	}
	// 垃圾值不采纳, 也不把已经对的顶掉
	if z.Adopt("上海") || z.Name() != "Asia/Shanghai" {
		t.Fatalf("垃圾时区被采纳了: %s", z.Name())
	}
}

// 明确设过之后, 手机报的不再顶掉它 —— 他出差两周,
// 家里的日报不该跟着漂过去
func TestFixedZoneWinsOverDevice(t *testing.T) {
	defer restoreLocal(t)
	z := NewZone(nil)
	if err := z.Set("Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	if z.Adopt("America/New_York") {
		t.Fatal("明确设过的时区被手机顶掉了")
	}
	if z.Name() != "Asia/Shanghai" {
		t.Fatalf("被顶掉了: %s", z.Name())
	}
}

// 采纳只落一条账 —— 手机每次报到都带 tz
func TestZoneAdoptLogsOnlyOnChange(t *testing.T) {
	defer restoreLocal(t)
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	z := NewZone(log)
	for i := 0; i < 5; i++ {
		z.Adopt("Asia/Shanghai")
	}
	n := 0
	for _, e := range flatten(log) {
		if e.Kind == abi.EvZoneSet {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一个时区落了 %d 条账, 该只有 1 条", n)
	}
}

// 兜底成 UTC 一定要说出来 —— 一台以为自己在 UTC 的机器,
// 它的每一条时间判断都是错的, 而没有任何一处会报错
func TestZoneSaysWhenItFellBackToUTC(t *testing.T) {
	defer restoreLocal(t)
	z := NewZone(nil)
	if !contains(z.Note(), "差") {
		t.Fatalf("兜底成 UTC 没说清后果: %q", z.Note())
	}
	z.Set("Asia/Shanghai")
	if contains(z.Note(), "没设") {
		t.Fatalf("设过了还在说没设: %q", z.Note())
	}
}

// TZ 环境变量当兜底, **但顶不掉设过的**: 开容器的人的意思
// 不该盖掉用户在界面上按下去的那一下
func TestEnvIsOnlyAFallback(t *testing.T) {
	defer restoreLocal(t)
	z := NewZone(nil)
	z.Set("Asia/Tokyo")
	z.UseEnv("Asia/Shanghai")
	if z.Name() != "Asia/Tokyo" {
		t.Fatalf("TZ 把设过的顶掉了: %s", z.Name())
	}

	z2 := NewZone(nil)
	if !z2.UseEnv("Asia/Shanghai") || z2.Name() != "Asia/Shanghai" {
		t.Fatalf("什么都没设时 TZ 没生效: %s", z2.Name())
	}
}

// restoreLocal 测试改的是 time.Local 这个全局 —— 用完放回去,
// 否则后面的测试跟着漂
func restoreLocal(t *testing.T) {
	t.Helper()
	was := time.Local
	t.Cleanup(func() { time.Local = was })
}

// 定死了要能松开 —— 没有这一条的话, 用户点过一次之后永远回不到自动:
// 他搬了城市, 手机报的新时区被自己两个月前那一下挡着,
// 而界面上看不出是被什么挡着的
func TestFollowLetsDeviceWinAgain(t *testing.T) {
	defer restoreLocal(t)
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	z := NewZone(log)
	z.Set("Asia/Shanghai")
	z.Follow()
	if z.Fixed() {
		t.Fatal("松不开")
	}
	// 松开之后**不猜**: 时区保持不变, 等下一次心跳来顶
	if z.Name() != "Asia/Shanghai" {
		t.Fatalf("松开的时候自己改了时区: %s", z.Name())
	}
	if !z.Adopt("America/New_York") || z.Name() != "America/New_York" {
		t.Fatalf("松开之后手机还是顶不掉: %s", z.Name())
	}
	// 跨重启也得记着"现在是跟着走的"
	again := NewZone(log)
	again.Restore(flatten(log))
	if again.Fixed() {
		t.Fatal("重启后又变成定死的了")
	}
}
