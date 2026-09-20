package main

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

// "我今天收到什么消息" —— 账本里一直有, 它原来只能 run grep
func TestHistoryReadsSignalsFromLedger(t *testing.T) {
	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	c := &components{os: o}
	log := o.Log()
	n := time.Now()
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location())
	put := func(kind string, body map[string]any, at time.Time) {
		log.Append(abi.ProcessID("sense"), abi.EvSignal, map[string]any{
			"at": float64(at.UnixMilli()), "kind": kind, "body": body,
		})
	}
	put("notice.posted", map[string]any{"app": "微信", "title": "牧之", "text": "库迪拿回来了"}, day.Add(9*time.Hour))
	for i := 0; i < 5; i++ { // 电量一天几百条, 连着一样的要并行
		put("battery.level", map[string]any{"pct": 61, "text": "电量 61%"}, day.Add(10*time.Hour))
	}
	put("location", map[string]any{"lat": 32.9, "lon": 117.3}, day.Add(11*time.Hour))
	put("place.arrived", map[string]any{"place": "公司"}, day.Add(12*time.Hour))

	out, err := c.history("", "今天", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `微信「牧之」: 库迪拿回来了`) {
		t.Fatalf("消息没还原出来: %q", out)
	}
	if !strings.Contains(out, "（×5）") {
		t.Fatalf("一样的没并行: %q", out)
	}
	if strings.Contains(out, "32.9") {
		t.Fatalf("位置该走 where(day=), 不该混进来: %q", out)
	}
	if !strings.Contains(out, "到了公司") {
		t.Fatalf("到达没还原: %q", out)
	}
	only, err := c.history("通知", "今天", 0)
	if err != nil || strings.Contains(only, "电量") {
		t.Fatalf("按种类筛没生效: %q %v", only, err)
	}
	if out, _ := c.history("通知", "昨天", 0); !strings.Contains(out, "没有这类记录") {
		t.Fatalf("昨天没有就该说没有: %q", out)
	}
}
