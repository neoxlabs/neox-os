package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func blindEv(kind abi.EventKind, at int64, p map[string]any) abi.Event {
	return abi.Event{PID: signalPID, At: at, Kind: kind, Payload: p}
}

// **报告会拿一段瞎着的时间当"很安静".**
//
// 安静率是整个感知层的验收判据("大部分时候安静"). 而 S42/S43 之后我们
// 知道: 感知层可以整段整段地瞎掉(采集端死了、令牌过期). 那段时间里
// 一条信号都没有 —— 报告看到的是**满分的安静率**.
//
// 拿一份中间瞎了半小时的账本去调阈值, 每一个结论都是歪的,
// 而且歪得看不出来: 数字很好看.
func TestReportCountsBlindTime(t *testing.T) {
	base := time.Date(2026, 8, 16, 9, 0, 0, 0, time.Local).UnixMilli()
	evs := []abi.Event{
		blindEv(abi.EvSignal, base, map[string]any{
			"source": "ha.door", "kind": "door.opened", "at": float64(base)}),
		blindEv(abi.EvCollectorDown, base+60_000, map[string]any{
			"source": "ha", "why": "HA 拒绝了 token"}),
		blindEv(abi.EvCollectorUp, base+1_860_000, map[string]any{"source": "ha"}),
		blindEv(abi.EvSignal, base+1_900_000, map[string]any{
			"source": "ha.door", "kind": "door.closed", "at": float64(base + 1_900_000)}),
	}
	txt := AnalyzeLedger(evs).Text()
	if !strings.Contains(txt, "瞎") {
		t.Fatalf("报告没提这段账本里有一半时间是瞎的:\n%s", txt)
	}
	if !strings.Contains(txt, "30") && !strings.Contains(txt, "半小时") {
		t.Fatalf("没说瞎了多久 —— 用户没法判断这份报告能信几分:\n%s", txt)
	}
	if !strings.Contains(txt, "token") {
		t.Fatalf("没说为什么瞎的 —— 那是唯一能让人去修的线索:\n%s", txt)
	}
}

// 一直好好的就不该提 —— 每份报告多一段没内容的话是纯噪音
func TestHealthyLedgerSaysNothingAboutBlindness(t *testing.T) {
	base := time.Now().UnixMilli()
	txt := AnalyzeLedger([]abi.Event{
		blindEv(abi.EvSignal, base, map[string]any{
			"source": "ha.door", "kind": "door.opened", "at": float64(base)}),
	}).Text()
	if strings.Contains(txt, "瞎") {
		t.Fatalf("一直好好的却提了瞎:\n%s", txt)
	}
}

// **还没回来的要算到现在** —— 只算"配对上的"那些, 会把"从昨天瞎到现在"
// 这种最严重的情况算成零
func TestStillBlindCountsUntilNow(t *testing.T) {
	base := time.Now().Add(-2 * time.Hour).UnixMilli()
	evs := []abi.Event{
		blindEv(abi.EvSignal, base, map[string]any{
			"source": "ha.door", "kind": "door.opened", "at": float64(base)}),
		blindEv(abi.EvCollectorDown, base+60_000, map[string]any{
			"source": "ha", "why": "进程没了"}),
	}
	r := AnalyzeLedger(evs)
	if r.BlindMs < 90*60*1000 {
		t.Fatalf("还没回来的只算了 %d 分钟 —— "+
			"'从昨天瞎到现在'会被算成零", r.BlindMs/60000)
	}
}

// **"瞎了多久"要从真正断掉的那一刻算, 不是从 OS 发现的那一刻.**
//
// 失联事件是 OS **发现**时才落的, 而它比真正断掉晚一个门槛(至少 90 秒).
// 采集器断了 2 分半时, 报告可能只说"1 分钟" —— 少掉的正好是门槛.
// 而门槛那个量级恰恰是最常见的短故障的量级, 于是短故障会被系统性地
// 少算一半以上, 而报告是用来调阈值的.
func TestBlindTimeCountsFromTheRealStart(t *testing.T) {
	base := time.Date(2026, 8, 16, 9, 0, 0, 0, time.Local).UnixMilli()
	evs := []abi.Event{
		blindEv(abi.EvSignal, base, map[string]any{
			"source": "ha.door", "kind": "door.opened", "at": float64(base)}),
		// 十分钟那一刻发现的, 而它已经静了 5 分钟 —— 真正断掉是第 5 分钟
		blindEv(abi.EvCollectorDown, base+600_000, map[string]any{
			"source": "ha", "silentMs": float64(300_000)}),
		blindEv(abi.EvCollectorUp, base+900_000, map[string]any{"source": "ha"}),
	}
	r := AnalyzeLedger(evs)
	if got := r.BlindMs / 60000; got != 10 {
		t.Fatalf("算出瞎了 %d 分钟, 该是 10 分钟(第 5 分钟断的, 第 15 分钟回来) —— "+
			"少算的正好是那道门槛", got)
	}
}
