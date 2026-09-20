package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// day 造一天: 在 home 过夜, 白天在 work
func feedDay(r *Routine, who string, day time.Time, home, work [2]float64) {
	at := func(h, m int) int64 {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, time.Local).UnixMilli()
	}
	// 夜里在家(前一天 22:00 到今天 7:00 —— 这里简化成今天 0:00-7:00)
	r.Observe(who, home[0], home[1], at(0, 0))
	r.Observe(who, home[0], home[1], at(7, 0))
	// 白天在公司
	r.Observe(who, work[0], work[1], at(9, 20))
	r.Observe(who, work[0], work[1], at(17, 30))
	// 晚上回家
	r.Observe(who, home[0], home[1], at(19, 0))
	r.Observe(who, home[0], home[1], at(23, 30))
}

var (
	homePt = [2]float64{32.919, 117.357}
	workPt = [2]float64{31.8639, 117.2808}
)

// **这就是用户要的那件事**: 没人告诉过它哪儿是公司, 它自己看出来.
func TestRoutineInfersHomeAndWork(t *testing.T) {
	r := NewRoutine(nil, nil)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	for d := 0; d < 6; d++ {
		feedDay(r, "u1", start.AddDate(0, 0, d), homePt, workPt)
	}
	hs := r.Habits("u1")
	if len(hs) != 2 {
		t.Fatalf("该看出两个地方, 看出了 %d 个: %+v", len(hs), hs)
	}
	guesses := map[string]string{}
	for _, h := range hs {
		guesses[h.Guess] = h.Why
	}
	if _, ok := guesses["家"]; !ok {
		t.Fatalf("没认出家: %+v", hs)
	}
	if _, ok := guesses["上班的地方"]; !ok {
		t.Fatalf("没认出上班的地方: %+v", hs)
	}
	// **必须说得出理由** —— 说不出的话用户没法判断值不值得信,
	// 而一个不可信的主动提醒比不提醒更糟
	for kind, why := range guesses {
		if !strings.Contains(why, "天") {
			t.Fatalf("%s 说不出凭什么: %q", kind, why)
		}
	}
}

// 通勤路上那一串点不该变成"常去的地方".
//
//	**要按真实的采集节奏造数据**: 开车途中采集端每 120 米就报一次(见
//	Collector.kt), 所以一条通勤路是一串密集的点, 每个格子只占一两分钟.
//	第一版这个测试造的是"报一条然后 25 分钟没消息" —— 那在真实数据里
//	恰恰意味着人停在那儿了, 测的是一个不存在的场景.
func TestRoutineIgnoresTheCommute(t *testing.T) {
	r := NewRoutine(nil, nil)
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.Local)
	for d := 0; d < 10; d++ {
		day := base.AddDate(0, 0, d)
		// 半小时的车程, 一路上每 2 分钟报一次
		for i := 0; i < 15; i++ {
			r.Observe("u1", 32.90+float64(i)*0.004, 117.34+float64(i)*0.004,
				day.Add(time.Duration(i)*2*time.Minute).UnixMilli())
		}
		// 到了公司, 待一下午
		r.Observe("u1", workPt[0], workPt[1], day.Add(40*time.Minute).UnixMilli())
		r.Observe("u1", homePt[0], homePt[1], day.Add(9*time.Hour).UnixMilli())
	}
	for _, h := range r.Habits("u1") {
		if !sameGrid(h.Lat, h.Lon, workPt[0], workPt[1]) &&
			!sameGrid(h.Lat, h.Lon, homePt[0], homePt[1]) {
			t.Fatalf("通勤路上的一个点被算成了常去的地方: %+v", h)
		}
	}
}

// 一段异常长的沉默(关机、没网、用户关掉了采集)不该变成"他在那儿待了一天".
//
//	那条假事实会压过所有真实的停留 —— 而它跟"他真的宅了一天"在数据上
//	一模一样, 分不开就只能定个上限
func TestRoutineDropsSuspiciouslyLongGaps(t *testing.T) {
	r := NewRoutine(nil, nil)
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.Local)
	for d := 0; d < 6; d++ {
		day := base.AddDate(0, 0, d)
		r.Observe("u1", 30.1, 114.1, day.UnixMilli())
		// 30 小时之后才有下一条 —— 多半是手机关了
		r.Observe("u1", 30.5, 114.5, day.Add(30*time.Hour).UnixMilli())
	}
	for _, h := range r.Habits("u1") {
		if h.Total > maxStay {
			t.Fatalf("一段 30 小时的沉默被当成了停留: %+v", h)
		}
	}
}

// **按天数不按次数**: 一天里报了 50 条位置不代表这是常去的地方 ——
// 它可能只是那天在那儿开了一下午会
func TestRoutineCountsDaysNotSamples(t *testing.T) {
	r := NewRoutine(nil, nil)
	day := time.Date(2026, 9, 1, 9, 0, 0, 0, time.Local)
	for i := 0; i < 50; i++ {
		r.Observe("u1", 31.0, 117.0, day.Add(time.Duration(i)*5*time.Minute).UnixMilli())
	}
	if hs := r.Habits("u1"); len(hs) != 0 {
		t.Fatalf("一天之内报了 50 条就被当成常去的地方: %+v", hs)
	}
}

// 两个人的规律不能串 —— 他的公司不该出现在她的常去列表里
func TestRoutineKeepsPeopleApart(t *testing.T) {
	r := NewRoutine(nil, nil)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	other := [2]float64{30.5, 114.3}
	for d := 0; d < 6; d++ {
		feedDay(r, "u1", start.AddDate(0, 0, d), homePt, workPt)
		feedDay(r, "u2", start.AddDate(0, 0, d), homePt, other)
	}
	for _, h := range r.Habits("u2") {
		if sameGrid(h.Lat, h.Lon, workPt[0], workPt[1]) {
			t.Fatal("他的公司出现在了她的常去列表里")
		}
	}
}

// 规律**是攒出来的, 一次重启不该清零** —— 攒够要好几天,
// 而进程一周会重启好几次
func TestRoutineSurvivesRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	r := NewRoutine(log, nil)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	for d := 0; d < 6; d++ {
		feedDay(r, "u1", start.AddDate(0, 0, d), homePt, workPt)
	}
	r.Flush()

	var evs []abi.Event
	for _, b := range log.Snapshot() {
		evs = append(evs, b...)
	}
	next := NewRoutine(nil, nil)
	next.Restore(evs)
	if len(next.Habits("u1")) != 2 {
		t.Fatalf("重启之后规律没了 —— 那它永远攒不够: %+v", next.Habits("u1"))
	}
}

// 推导出来的**不自动命名** —— 它引着人去确认, 而不是替他决定.
// 自动命名的话, 错了没有任何一处会说
func TestRoutineSuggestsRatherThanNames(t *testing.T) {
	r := NewRoutine(nil, nil)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	for d := 0; d < 6; d++ {
		feedDay(r, "u1", start.AddDate(0, 0, d), homePt, workPt)
	}
	got := r.Text("u1", func(lat, lon float64) string { return "" })
	if !strings.Contains(got, "还没起名") {
		t.Fatalf("没提示用户去命名: %q", got)
	}
	if !strings.Contains(got, "看着像") {
		t.Fatalf("说得太肯定了 —— 它是猜的: %q", got)
	}
}

// **装回来的不许再记一遍账** —— 不摘的话每次开机 stay.ended 翻一倍:
// 版本迭代八九次可从 1 条涨到 8192 条, 而 sense 桶一过压紧闸,
// 开机就变成平方级
func TestRoutineRestoreDoesNotRelog(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	r := NewRoutine(log, time.Now)
	base := time.Now().Add(-24 * time.Hour).UnixMilli()
	// 造两次真实的停留: 换地方就收一次
	r.Observe("他", 32.963, 117.350, base)
	r.Observe("他", 32.963, 117.350, base+3*3600*1000)
	r.Observe("他", 32.919, 117.357, base+4*3600*1000)
	r.Flush()
	evs := log.Replay(signalPID, 0)
	if len(evs) == 0 {
		t.Fatal("真的停留该落账")
	}
	for i := 0; i < 3; i++ {
		r2 := NewRoutine(log, time.Now)
		r2.Restore(evs)
	}
	if got := log.Replay(signalPID, 0); len(got) != len(evs) {
		t.Fatalf("装回三次之后账本从 %d 条涨到 %d 条", len(evs), len(got))
	}
}

// 压紧要节流: 过了线之后每写一条都压一遍 = O(n²), 开机会卡死
func TestSenseCompactionIsThrottled(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	for i := 0; i < compactAfterSenseEvents+3000; i++ {
		log.Append(signalPID, abi.EvStayEnded, map[string]any{
			"who": "他", "lat": 32.9, "lon": 117.3, "from": int64(i), "to": int64(i + 1)})
	}
	start := time.Now()
	for i := 0; i < 5000; i++ {
		log.Append(signalPID, abi.EvStayEnded, map[string]any{
			"who": "他", "lat": 32.9, "lon": 117.3, "from": int64(i), "to": int64(i + 1)})
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("五千条写了 %v —— 压紧没节流, 开机会卡死", d)
	}
}
