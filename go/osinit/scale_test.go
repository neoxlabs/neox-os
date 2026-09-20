package osinit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 一天的量级. 三条断言各挡一种慢性失败.
//
// 数字定得宽松: 这是**合成**的一天, 精确阈值只能等真实数据.
// 它要挡的是"错了一个数量级"这类问题.
func TestOneSyntheticDayAtScale(t *testing.T) {
	start := time.Date(2026, 8, 15, 0, 0, 0, 0, time.Local)
	clk := &fakeClock{t: start}
	log := NewEventLog(func() int64 { return clk.now().UnixMilli() })
	var digests int
	bus := NewSignalBus(log, func(Digest) { digests++ }, SignalOptions{
		Window: 5 * time.Minute, Lateness: 2 * time.Minute, Now: clk.now,
	})

	st := SimulateDay(bus, start, clk.advance)
	st.Digests = digests

	evs := log.Replay(signalPID, 0)
	bytes := ledgerBytes(evs)
	rep := AnalyzeLedger(evs)

	t.Logf("一天: 定点 %d + 家居观察 %d → 信号 %d 条 → 摘要 %d 份 → 账本 %.1f KB",
		st.Fixes, st.HAObs, rep.Signals, st.Digests, float64(bytes)/1024)
	for _, k := range rep.Kinds {
		t.Logf("   %-26s %d", k.Source+"/"+k.Kind, k.N)
	}

	// ① 账本一天不该涨到 MB 级 —— 它每次启动要整个读一遍
	if bytes > 2<<20 {
		t.Fatalf("账本一天 %.1f MB —— 每次启动要整个读一遍, 几天就起不来了",
			float64(bytes)/(1<<20))
	}
	// ② 摘要数 = 常驻进程一天要做的推理次数, 每份都是一次"值不值得说"的判断
	if st.Digests > 200 {
		t.Fatalf("一天产出 %d 份摘要 —— 常驻进程要做这么多次推理", st.Digests)
	}
	// ③ 降噪比: 几十万次观察要压到几十条
	totalObs := st.Fixes + st.HAObs
	if rep.Signals > totalObs/1000 {
		t.Fatalf("%d 次观察产出 %d 条信号, 降噪不够 —— 采集端的阈值太松",
			totalObs, rep.Signals)
	}
}

// ledgerBytes 这些事件落盘要多少字节 —— 账本是 jsonl, 一行一条
func ledgerBytes(evs []abi.Event) int {
	n := 0
	for _, e := range evs {
		b, _ := json.Marshal(e)
		n += len(b) + 1
	}
	return n
}
