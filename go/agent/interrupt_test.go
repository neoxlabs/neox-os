package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/engine"
)

// 任务执行过程中也必须能接收新的指令.
//
// 分片读日志的长任务在第 488 行收到"停, 别读了"后, 仍可能读到第 1457 行
// 才停止 —— 那句话一直躺在收件箱里没人取, 而它每读
// 一片都在花钱. Serve 只在**两轮之间**调 Recv, 一轮跑起来进程就是聋的.
func TestUserCanInterruptMidTask(t *testing.T) {
	sys := newFakeSys()
	sys.interrupts = []string{"停，别读了，我改主意了。"}
	dir := t.TempDir()
	var steps []Step
	for i := 0; i < 5; i++ {
		name := "d" + string(rune('a'+i))
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, Step{Tool: "list_dir", Args: map[string]any{"path": name}})
	}
	steps = append(steps, Step{Done: "收到，停了"})
	m := &Scripted{Steps: steps}
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: dir}, Model: m}
	if err := ag.Run("分片读到底"); err != nil {
		t.Fatal(err)
	}
	e := sys.last("interrupt")
	if e == nil {
		t.Fatal("用户中途说的话没被听见 —— 它会一直干到自己觉得完了为止")
	}
	if !strings.Contains(str2(e["text"]), "别读了") {
		t.Fatalf("插话内容不对: %v", e["text"])
	}
}

// 那句话必须真的进到模型看得到的地方 —— 只发个事件等于没说.
func TestInterruptReachesTheModel(t *testing.T) {
	sys := newFakeSys()
	sys.interrupts = []string{"顺便也统计一下 IP"}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := NewWindow(engine.NewPageStore(), "干活", 1<<20)
	var seen []Observation
	rec := &recordingModel{inner: &Scripted{Steps: []Step{
		{Tool: "list_dir", Args: map[string]any{"path": "x"}, CallID: "c1"},
		{Done: "好"},
	}}, onNext: func(h []Observation) { seen = append([]Observation{}, h...) }}
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: dir},
		Window: w, Model: rec}
	if err := ag.Run("干活"); err != nil {
		t.Fatal(err)
	}
	_ = seen
	// **断言必须落在真正发出去的那份消息上.**
	//
	// 如果断言的是 history, 而 LLM.once() 只从 Window 拼消息、完全不读取
	// history, 测试仍会通过, 但插话不会被当成指令.
	msgs, _ := w.Messages()
	all := ""
	for _, m := range msgs {
		all += m.Content
	}
	if !strings.Contains(all, "统计一下 IP") {
		t.Fatal("插话没进真正发出去的消息 —— 模型看不到, 等于没说")
	}
	if !strings.Contains(all, "最新的指令") {
		t.Fatal("插话没被标成插话 —— 混在几十条工具结果里, 模型没理由当它是新指令")
	}
}

// **不许把 tool_call 和它的 result 切开.**
//
// 插话注入的是一条 user 消息. 插错位置(工具调用发出去了、结果还没回来)
// 会让供应商直接 400, 而且报的错跟真正的原因八竿子打不着 ——
// 那是 toolPairGuard 那一整套东西存在的理由.
func TestInterruptDoesNotBreakToolPairing(t *testing.T) {
	sys := newFakeSys()
	sys.interrupts = []string{"等一下，换个方向"}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := NewWindow(engine.NewPageStore(), "干活", 1<<20)
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: dir},
		Window: w, Model: &Scripted{Steps: []Step{
			{Tool: "list_dir", Args: map[string]any{"path": "x"}, CallID: "c1"},
			{Done: "好"},
		}}}
	if err := ag.Run("干活"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := w.Messages()
	// 每个 tool_call 后面必须跟得上它的 result
	open := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			open[c.ID] = true
		}
		if m.ToolCallID != "" {
			delete(open, m.ToolCallID)
		}
	}
	if len(open) > 0 {
		t.Fatalf("插话把工具调用和结果切开了, 有 %d 个调用没有结果: %v", len(open), open)
	}
}

type recordingModel struct {
	inner  Model
	onNext func([]Observation)
}

func (r *recordingModel) Name() string { return "recording" }
func (r *recordingModel) Next(task string, h []Observation) (Step, error) {
	if r.onNext != nil {
		r.onNext(h)
	}
	return r.inner.Next(task, h)
}

// "最新的指令"这句话只能挂在**最后一条**插话上.
//
// 连续收到几句插话(蚌埠在哪 / 你为啥这么快 / 你有什么工具)时,
// 每一句都可能在任务执行期间被收下. 而框架话原来是**跟内容一起存进页里**的,
// 也就是永久留在上下文里 —— 于是历史里有好几条都自称"这是最新的指令,
// 优先于你正在做的事, 然后照它做", 它每一轮都把那几个老问题再服务一遍:
// 问"你有什么工具", 它答完工具又"顺手把你前面几个问题一起答了".
//
// "最新"是个**位置**, 不是内容的一部分.
func TestOnlyTheLatestInterjectionIsMarkedLatest(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "干活", 1<<20)
	w.AppendInterrupt("蚌埠是哪里")
	w.AppendInterrupt("你为啥这么快")
	w.AppendInterrupt("你有什么工具")
	msgs, _ := w.Messages()
	all := ""
	for _, m := range msgs {
		all += m.Content
	}
	if n := strings.Count(all, "这是最新的指令"); n != 1 {
		t.Fatalf("有 %d 条都自称'最新的指令' —— 老问题每轮都会被再答一遍", n)
	}
	// 挂在最后那条上, 不是第一条
	iLast := strings.Index(all, "你有什么工具")
	iNote := strings.Index(all, "这是最新的指令")
	if iNote < iLast {
		t.Fatalf("那句话挂在了老的插话上:\n%s", all)
	}
	// 三句话本身都得留着 —— 它们构成完整的插话历史
	for _, q := range []string{"蚌埠是哪里", "你为啥这么快", "你有什么工具"} {
		if !strings.Contains(all, q) {
			t.Fatalf("用户说过的话丢了: %s", q)
		}
	}
}

// 一条插话时照旧要带上那句话 —— 别为了修重复把功能修没了(R23 验过它是必须的)
func TestSingleInterjectionStillMarked(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "干活", 1<<20)
	w.AppendInterrupt("顺便也统计一下 IP")
	msgs, _ := w.Messages()
	all := ""
	for _, m := range msgs {
		all += m.Content
	}
	if !strings.Contains(all, "这是最新的指令") {
		t.Fatalf("唯一那条插话没被标成插话:\n%s", all)
	}
}
