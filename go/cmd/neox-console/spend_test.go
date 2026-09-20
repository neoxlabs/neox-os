package main

import (
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func usageEvent(pid abi.ProcessID, at int64, prompt, completion, cached float64) abi.Event {
	// **float64**: 从账本装回来的事件都过了一趟 JSON, 数字全是 float64.
	// 只认 int 的话症状是"重启前有数、重启后全是 0"
	return abi.Event{PID: pid, At: at, Kind: abi.EvProcOutput, Payload: map[string]any{
		"phase": "usage", "prompt": prompt, "completion": completion, "cached": cached,
	}}
}

func TestSpend一个人的花费跨重启合起来算(t *testing.T) {
	// 进程会死, 对话不死: 同一个 bot 的花费散在它历次进程上
	events := []abi.Event{
		usageEvent("p1.1", 100, 1000, 100, 0),
		{PID: "p2.1", At: 200, Kind: abi.EvProcState, Payload: map[string]any{"state": "created"}},
		usageEvent("p2.1", 300, 2000, 200, 500),
	}
	who := map[abi.ProcessID]string{"p1.1": "小登", "p2.1": "小登"}
	got := spendFrom(events, func(pid abi.ProcessID) (string, string, string) {
		return who[pid], "/AI/oa", "neox/小登"
	})
	if len(got.Bots) != 1 {
		t.Fatalf("同一个人被算成了 %d 个", len(got.Bots))
	}
	row := got.Bots[0]
	if row.Prompt != 3000 || row.Completion != 300 || row.Cached != 500 {
		t.Fatalf("合起来算错了: %+v", row)
	}
	if row.Calls != 2 || row.Starts != 1 {
		t.Fatalf("次数不对: %+v", row)
	}
	if row.FirstAt != 100 || row.LastAt != 300 {
		t.Fatalf("头尾时间不对: %+v", row)
	}
	if got.Total.Prompt != 3000 {
		t.Fatalf("合计算错: %+v", got.Total)
	}
}

func TestSpend花得最多的排最前(t *testing.T) {
	// 这一页是拿来做决定的, 而决定通常关于最贵的那个
	events := []abi.Event{
		usageEvent("a", 1, 100, 0, 0),
		usageEvent("b", 2, 9000, 0, 0),
		usageEvent("c", 3, 500, 0, 0),
	}
	names := map[abi.ProcessID]string{"a": "小", "b": "大", "c": "中"}
	got := spendFrom(events, func(pid abi.ProcessID) (string, string, string) {
		return names[pid], "", ""
	})
	order := []string{got.Bots[0].Bot, got.Bots[1].Bot, got.Bots[2].Bot}
	if order[0] != "大" || order[1] != "中" || order[2] != "小" {
		t.Fatalf("排序不对: %+v", order)
	}
}

func TestSpend认不出是谁的事件不算进任何人头上(t *testing.T) {
	// 宁可漏一条, 不能记到别人账上 —— 记错了比没记更难发现
	events := []abi.Event{usageEvent("鬼", 1, 999, 999, 0)}
	got := spendFrom(events, func(abi.ProcessID) (string, string, string) { return "", "", "" })
	if len(got.Bots) != 0 || got.Total.Prompt != 0 {
		t.Fatalf("认不出主人的花费被算进去了: %+v", got)
	}
}

func TestSpend一轮和一次调用不是一回事(t *testing.T) {
	// 一轮里会叫很多次模型(每一步一次). 两个数都要有:
	// "轮"是人感受到的用量, "次"是花钱的单位
	events := []abi.Event{
		{PID: "p", At: 1, Kind: abi.EvProcOutput, Payload: map[string]any{"phase": "start"}},
		usageEvent("p", 2, 10, 1, 0),
		usageEvent("p", 3, 10, 1, 0),
		usageEvent("p", 4, 10, 1, 0),
		{PID: "p", At: 5, Kind: abi.EvProcOutput, Payload: map[string]any{"phase": "start"}},
		usageEvent("p", 6, 10, 1, 0),
	}
	got := spendFrom(events, func(abi.ProcessID) (string, string, string) { return "小登", "", "" })
	row := got.Bots[0]
	if row.Turns != 2 {
		t.Errorf("轮数不对: %d", row.Turns)
	}
	if row.Calls != 4 {
		t.Errorf("调用次数不对: %d", row.Calls)
	}
}
