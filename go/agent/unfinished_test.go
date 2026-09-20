package agent

import (
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 被打断的活要认得出来 —— 这是重启之后唯一能告诉用户"你还欠着什么"的依据.
//
// 判据是**事件日志里有 start 没有收尾**, 不是看磁盘、不是看时间戳.
// 如果干到一半被杀掉, 重启后新进程会做 16 轮文件系统考古
// 才得出"我不知道要做什么", 而任务原文一直在 start 事件里.

func outEv(pid abi.ProcessID, at int64, m map[string]any) abi.Event {
	return abi.Event{PID: pid, At: at, Kind: abi.EvProcOutput, Payload: m}
}

func convOf(t *testing.T, evs ...abi.Event) Conversation {
	t.Helper()
	cs := Conversations(map[abi.ProcessID][]abi.Event{"p1": evs})
	if len(cs) != 1 {
		t.Fatalf("要 1 段对话, 得到 %d", len(cs))
	}
	return cs[0]
}

func TestInterruptedTurnIsDetectedWithItsTask(t *testing.T) {
	c := convOf(t,
		outEv("p1", 1, map[string]any{"phase": "start", "task": "做一个记账小工具"}),
		outEv("p1", 2, map[string]any{"phase": "step", "n": 1}),
		outEv("p1", 3, map[string]any{"phase": "tool_ok"}),
	)
	if !c.Unfinished {
		t.Fatal("有 start 没收尾, 必须判成没干完")
	}
	// 光知道"没干完"没用, 得把**他要什么**带出来
	if c.Pending != "做一个记账小工具" {
		t.Fatalf("要带上任务原文, 得到 %q", c.Pending)
	}
	if c.PendingSteps != 2 {
		t.Fatalf("要说清干到第几步, 得到 %d", c.PendingSteps)
	}
}

// 反向: 干完了绝不能报. 误报比不报更糟 —— 每次开机都喊一句"你还欠着活",
// 喊多了真有欠账那次就没人看了.
func TestFinishedTurnIsNotReportedUnfinished(t *testing.T) {
	for _, end := range []string{"reply", "done"} {
		c := convOf(t,
			outEv("p1", 1, map[string]any{"phase": "start", "task": "做一个记账小工具"}),
			outEv("p1", 2, map[string]any{"phase": "step", "n": 1}),
			outEv("p1", 3, map[string]any{"phase": end, "text": "好了", "summary": "好了"}),
		)
		if c.Unfinished {
			t.Fatalf("%s 收尾了, 不该判成没干完", end)
		}
		if c.Pending != "" || c.PendingSteps != 0 {
			t.Fatalf("%s: 收尾之后残留 pending=%q steps=%d", end, c.Pending, c.PendingSteps)
		}
	}
}

// 一段对话里前面几轮都干完了、最后一轮被打断: 报的必须是**最后那一轮**的活,
// 不是第一轮的开场白. 拿开场白去接续等于让它重做已经做完的事.
func TestOnlyTheLastTurnDecidesUnfinished(t *testing.T) {
	c := convOf(t,
		outEv("p1", 1, map[string]any{"phase": "start", "task": "第一件事"}),
		outEv("p1", 2, map[string]any{"phase": "reply", "text": "第一件事好了"}),
		outEv("p1", 3, map[string]any{"phase": "start", "task": "第二件事"}),
		outEv("p1", 4, map[string]any{"phase": "step", "n": 1}),
	)
	if !c.Unfinished {
		t.Fatal("最后一轮被打断, 必须判成没干完")
	}
	if c.Pending != "第二件事" {
		t.Fatalf("要报最后那一轮的活, 得到 %q", c.Pending)
	}
	if c.Title != "第一件事" {
		t.Fatalf("标题仍是开场白, 得到 %q", c.Title)
	}
}
