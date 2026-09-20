package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

func restoredText(t *testing.T, evs ...abi.Event) string {
	t.Helper()
	w := RestoreWindow(engine.NewPageStore(), evs, "接着干")
	msgs, _ := w.Messages()
	all := ""
	for _, m := range msgs {
		all += m.Content
		for _, c := range m.ToolCalls {
			all += c.Name + c.Arguments
		}
	}
	return all
}

// 用户中途说的话, 恢复出来只能有**一份**.
//
// 插话在账本里有两条记录: Send 投递时的 EvInputRecv, 和 agent 中途取到时的
// phase:"interrupt" —— 同一句话. 两条都恢复的话, 模型会看到用户把同一件事
// 说了两遍.
//
// 我一开始正是这么改的, 还写了个用例"证明"插话丢了 —— 而那个用例构造的
// 事件序列真实系统永远不会产生(只有 interrupt 没有 input.recv).
// 查了一遍才发现, 不是靠推断.
func TestUserInterjectionRestoredExactlyOnce(t *testing.T) {
	txt := "改主意了：改成统计 IP"
	all := restoredText(t,
		out2("p1", 1, map[string]any{"phase": "start", "task": "统计路径"}),
		abi.Event{PID: "p1", At: 2, Kind: abi.EvInputRecv,
			Payload: map[string]any{"text": txt}},
		out2("p1", 3, map[string]any{"phase": "interrupt", "text": txt}),
		out2("p1", 4, map[string]any{"phase": "reply", "text": "好"}),
	)
	switch n := strings.Count(all, "改成统计 IP"); n {
	case 0:
		t.Fatalf("用户中途说的话恢复之后没了:\n%s", all)
	case 1: // 对
	default:
		t.Fatalf("同一句话恢复出来 %d 份 —— 模型会以为用户说了两遍:\n%s", n, all)
	}
}

// 用户拒过的事要记着 —— 不记的话回来会为同一件事再申请一次,
// 而每一次申请都是一次打扰.
func TestRestoreKeepsDenials(t *testing.T) {
	all := restoredText(t,
		out2("p1", 1, map[string]any{"phase": "start", "task": "改配置"}),
		out2("p1", 2, map[string]any{"phase": "step", "tool": "write_file",
			"args": map[string]any{"path": "/etc/hosts"}}),
		out2("p1", 3, map[string]any{"phase": "denied", "scope": "/etc/hosts"}),
		out2("p1", 4, map[string]any{"phase": "reply", "text": "改不了"}),
	)
	if !strings.Contains(all, "被用户拒了") {
		t.Fatalf("用户拒过的事没记下来, 它会再申请一次:\n%s", all)
	}
	if !strings.Contains(all, "/etc/hosts") {
		t.Fatalf("拒的是什么没记下来, 等于没记:\n%s", all)
	}
	// 被拒那一步**不该**再写"结果没保留下来" —— 拒绝就是它的结果
	if strings.Contains(all, "结果没有保留下来") {
		t.Fatalf("把用户的决定盖成了'结果没保留', 决定被抹掉了:\n%s", all)
	}
}

// 没被拒的那些步, 占位照旧要有 —— 别为了让路把正常路径弄丢
func TestRestoreStillPlaceholdsNormalSteps(t *testing.T) {
	all := restoredText(t,
		out2("p1", 1, map[string]any{"phase": "start", "task": "看看"}),
		out2("p1", 2, map[string]any{"phase": "step", "tool": "list_dir",
			"args": map[string]any{"path": "src"}}),
		out2("p1", 3, map[string]any{"phase": "step", "tool": "read_file",
			"args": map[string]any{"path": "a.py"}}),
	)
	if n := strings.Count(all, "结果没有保留下来"); n != 2 {
		t.Fatalf("两个动作该有两条占位, 得到 %d:\n%s", n, all)
	}
}
