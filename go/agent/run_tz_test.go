package agent

import (
	"testing"
	"time"
)

// Alpine 没有 zoneinfo, TZ=Asia/Shanghai 会被 musl 静默当成 UTC ——
// 文件不在就给固定偏移, 子进程 `date` 才跟 OS 对得上
func TestChildTZFallsBackToPOSIXOffset(t *testing.T) {
	old := zoneinfoDir
	zoneinfoDir = t.TempDir() + "/" // 一个 Alpine: 什么都没有
	t.Cleanup(func() { zoneinfoDir = old })
	now := time.Date(2026, 9, 11, 6, 40, 0, 0, time.UTC)
	cases := map[string]string{
		"Asia/Shanghai":     "<+08>-8",
		"Asia/Kolkata":      "<+0530>-5:30",
		"America/Sao_Paulo": "<-03>3",
	}
	for name, want := range cases {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := childTZ(loc, now); got != want {
			t.Errorf("%s → %q, 要 %q", name, got, want)
		}
	}
	if childTZ(time.UTC, now) != "UTC" {
		t.Error("UTC 就是 UTC")
	}
	// 装了 tzdata 的镜像: 用名字, 夏令时才准
	zoneinfoDir = old
	if loc, _ := time.LoadLocation("Asia/Shanghai"); exists(old+"Asia/Shanghai") && childTZ(loc, now) != "Asia/Shanghai" {
		t.Error("文件在就该用名字")
	}
}
