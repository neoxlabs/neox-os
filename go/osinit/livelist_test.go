package osinit

import (
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// **对话列表必须能看见"这一段会话里刚做的事".**
//
// 真机场景: 连做三件事之后想切回第一件, `/对话` 说"还没有聊过什么" ——
// 因为列表是**启动时算一次**的快照, 之后再也不更新.
//
// 而且失败是半静默的: 切换没成功, 但下一句话照样被当前进程收下,
// 用户以为自己切过去了、其实没有.
//
// 这条钉的是"事件日志是唯一真相源": 刚 Append 的事件必须立刻
// 出现在 Snapshot 里, 宿主才可能现算出正确的列表.
func TestSnapshotSeesEventsAppendedAfterStartup(t *testing.T) {
	l := NewEventLog(func() int64 { return 1 })

	// 开机时的快照 —— 空的
	if got := len(l.Snapshot()); got != 0 {
		t.Fatalf("开机快照该是空的, got %d", got)
	}
	// 会话过程中做了两件事
	l.Append("p1", abi.EvProcOutput, map[string]any{"phase": "start", "task": "写脚本"})
	l.Append("p2", abi.EvProcOutput, map[string]any{"phase": "start", "task": "查日志"})

	snap := l.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("会话中新起的对话在快照里看不到: %d 个 —— "+
			"拿开机快照当真相源就是这么丢的", len(snap))
	}
	for _, pid := range []abi.ProcessID{"p1", "p2"} {
		if len(snap[pid]) == 0 {
			t.Errorf("%s 的事件一条都没有", pid)
		}
	}
}

// 快照必须是拷贝 —— 调用方拿去遍历时我们还在往里写
func TestSnapshotIsACopy(t *testing.T) {
	l := NewEventLog(func() int64 { return 1 })
	l.Append("p1", abi.EvProcOutput, map[string]any{"n": 1})
	snap := l.Snapshot()
	l.Append("p1", abi.EvProcOutput, map[string]any{"n": 2})
	if len(snap["p1"]) != 1 {
		t.Fatal("快照被后续写入改了 —— 调用方遍历到一半会撞上并发写")
	}
}
