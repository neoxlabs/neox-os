package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/neox-os/neox-os/engine"
	"strings"
	"testing"
)

// 结果被折出上下文之后, 重读**不算原地转**.
//
// 通则(stall.go 开头写着): 该判的不是"这次调用跟上次长得一样吗", 而是
// "这次调用还会不会带来新信息". 折叠就是另一种"世界变了" —— 结果被腾掉
// 之后, 同样的读确实带来新信息.
//
// 真机: 用户中途补了个**追溯性**要求("每片也统计 IP"), 它必须重读已读过的片,
// 而那些片的结果早被折掉了(它自己说"原文已被系统腾掉"). 系统先扔掉结果,
// 再因为它去重取而责备"第 2 次用同样的参数, 结果不会变, 基于已有结果继续"
// —— 那条建议根本没法执行. 而且第 3 次会直接停掉整轮.
func TestFoldedResultMakesRereadLegitimate(t *testing.T) {
	tr := newStallTracker()
	args := map[string]any{"path": "big.log", "offset": 1}
	// 第一次读
	if v, _ := tr.observeCall("c1", "read_file", args, Observation{Result: "一堆"}, false, false); v != stallNone {
		t.Fatal("第一次读就报了")
	}
	// 结果被折掉
	tr.forgetFolded(map[string]bool{"c1": true})
	// 重读: 不该报
	v, msg := tr.observeCall("c2", "read_file", args, Observation{Result: "一堆"}, false, false)
	if v != stallNone {
		t.Fatalf("结果已经被系统折掉了, 重读却被判成原地转: %s", msg)
	}
	// 再折再读, 还是不该报 —— 否则攒到第 3 次会直接停掉整轮
	tr.forgetFolded(map[string]bool{"c2": true})
	if v, msg := tr.observeCall("c3", "read_file", args, Observation{Result: "一堆"}, false, false); v != stallNone {
		t.Fatalf("第三次重读被停掉了 —— 系统自己造成的状况算在 agent 头上: %s", msg)
	}
}

// 反向: 结果还在上下文里, 重复调用照样要拦.
// 放宽过头会把真正的原地转洗白 —— 那比误报更糟.
func TestUnfoldedRepeatIsStillCaught(t *testing.T) {
	tr := newStallTracker()
	args := map[string]any{"path": "a.txt"}
	tr.observeCall("c1", "read_file", args, Observation{Result: "x"}, false, false)
	v, msg := tr.observeCall("c2", "read_file", args, Observation{Result: "x"}, false, false)
	if v != stallWarn {
		t.Fatalf("结果还在上下文里, 重复读该拦: %v %s", v, msg)
	}
	if !strings.Contains(msg, "第 2 次") {
		t.Fatalf("提醒文案变了: %s", msg)
	}
}

// 只摘掉被折的那一条, 别把别的调用一起清了.
// 清多了同样是把原地转洗白.
func TestForgetFoldedOnlyDropsTheFoldedOne(t *testing.T) {
	tr := newStallTracker()
	a := map[string]any{"path": "a.txt"}
	b := map[string]any{"path": "b.txt"}
	tr.observeCall("ca", "read_file", a, Observation{Result: "x"}, false, false)
	tr.observeCall("cb", "read_file", b, Observation{Result: "y"}, false, false)
	tr.forgetFolded(map[string]bool{"ca": true})
	// b 没被折 —— 再读 b 仍然该拦
	if v, _ := tr.observeCall("cb2", "read_file", b, Observation{Result: "y"}, false, false); v != stallWarn {
		t.Fatal("把没被折的那条也清了 —— 真正的原地转会被洗白")
	}
}

// 端到端: 折叠真的发生时, agent 循环必须把那些调用摘掉.
//
// **钉的是接线**. 上面那几条只验了 stallTracker 自己; 而我已经栽过两次
// "算对了但没接上"(commandPath 没进 commandEnv、插话框架话进了没人读的
// history) —— 两次都是单元测试全绿而真机是废的.
//
// 形状照真机来: 读了好几片不同的, 最早那片被挤出预算折掉, 然后**回头重读它**
// (典型场景是中途增加追溯性要求, 需要重读已读过的片).
func TestAgentForgetsFoldedCallsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat(strings.Repeat("内容", 60)+"\n", 4000)
	if err := os.WriteFile(filepath.Join(dir, "big.log"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := newFakeSys()
	w := NewWindow(engine.NewPageStore(), "读日志", 90000)
	first := map[string]any{"path": "big.log", "offset": 1}
	steps := []Step{{Tool: "read_file", Args: first, CallID: "c1"}}
	for i, off := range []int{200, 400, 600, 800} {
		steps = append(steps, Step{Tool: "read_file",
			Args:   map[string]any{"path": "big.log", "offset": off},
			CallID: fmt.Sprintf("d%d", i)})
	}
	// 回头重读第一片 —— 它的结果这时候已经被折掉了
	steps = append(steps, Step{Tool: "read_file", Args: first, CallID: "c9"})
	steps = append(steps, Step{Done: "读完了"})

	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: dir},
		// 真实路径里 LLM.once() **每步都调一次 Messages()**, 折叠就发生在那儿.
		// Scripted 不调, 所以得包一层 —— 否则一次都不会折, 那时候报停滞
		// 反而是**正确**行为, 测的是另一回事.
		Window: w, Model: &foldingModel{w: w, inner: &Scripted{Steps: steps}}}
	if err := ag.Run("分片读日志"); err != nil {
		t.Fatalf("跑挂了: %v", err)
	}
	if !w.FoldedCallIDs()["c1"] {
		t.Fatalf("第一片没被折掉, 这个用例什么都没验到: %v", w.FoldedCallIDs())
	}
	for _, p := range sys.phases() {
		if p == "stall" {
			t.Fatal("结果被系统折掉之后重读, 仍然被判成原地转")
		}
	}
}

// foldingModel 模拟 LLM.once() 的形状: 每步先拼一次消息(折叠就发生在那儿),
// 再交给脚本决定下一步.
type foldingModel struct {
	w     *Window
	inner Model
}

func (m *foldingModel) Name() string { return "folding" }
func (m *foldingModel) Next(task string, h []Observation) (Step, error) {
	m.w.Messages()
	return m.inner.Next(task, h)
}
