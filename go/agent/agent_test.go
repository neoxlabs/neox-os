package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// 主循环的测试.
//
// 这一整段以前**没有任何测试保护** —— Agent.ABI 是具体的 AbiClient,
// 跑主循环就得有真 socket、真进程、真模型. 于是像"空的 done 被当成完成"
// 这种 bug 只能靠真机偶然撞出来, 而真机撞不出来的分支
// (止损线到了、决策被拒、连续模型错误) 从来没人验证过.

func runAgent(t *testing.T, sys *fakeSys, steps []Step, task string) (*Agent, error) {
	t.Helper()
	ts := NewToolSet(DefaultTools())
	ag := &Agent{
		ABI:      sys,
		Model:    &Scripted{Steps: steps},
		Tools:    ts,
		Box:      Toolbox{Root: t.TempDir()},
		Window:   NewWindow(engine.NewPageStore(), task, 0),
		MaxSteps: 12,
	}
	return ag, ag.Run(task)
}

// 直接回话是最常见的一条路径, 不是例外
func TestReplyEndsTheTurn(t *testing.T) {
	sys := newFakeSys()
	_, err := runAgent(t, sys, []Step{{Reply: "你好"}}, "嗨")
	if err != nil {
		t.Fatal(err)
	}
	if !sys.saw("reply") {
		t.Fatalf("没走 reply: %v", sys.phases())
	}
}

// 空的 done 不是完成, 是什么都没说.
//
// 模型发 {"done":""} 时, 终端只能显示一个孤零零的完成标记,
// 用户的问题没有得到回答, 而系统却会把这一轮当成成功.
func TestEmptyDoneIsRejectedThenRetried(t *testing.T) {
	sys := newFakeSys()
	_, err := runAgent(t, sys, []Step{
		{Done: "   "},     // 空的 —— 该被拒
		{Reply: "机房在 B2"}, // 自纠之后给出真答案
	}, "机房在哪")
	if err != nil {
		t.Fatal(err)
	}
	if !sys.saw("empty_done") {
		t.Fatalf("空的 done 被当成完成放过去了: %v", sys.phases())
	}
	if !sys.saw("reply") {
		t.Fatal("没走到自纠之后的真回答")
	}
	if sys.saw("done") {
		t.Fatal("空的 done 不该产生一条 done 事件")
	}
}

// 预算耗尽时要立刻停. 这条路径只有把整个预算消耗完才会触发.
func TestBudgetExhaustedStopsImmediately(t *testing.T) {
	sys := newFakeSys()
	sys.spendErr = fmt.Errorf("预算用完了")
	_, err := runAgent(t, sys, []Step{
		{Tool: "list_dir", Args: map[string]any{"path": "."}},
	}, "看看目录")
	if err == nil {
		t.Fatal("止损线到了还在往下跑")
	}
	if !strings.Contains(err.Error(), "止损") {
		t.Fatalf("错误没说清是止损: %v", err)
	}
	if sys.saw("step") {
		t.Fatal("止损之后还调了工具")
	}
}

// 越界被人拒掉: 不许当成"工具失败"重试, 那会再打扰用户一次
func TestDeniedWriteDoesNotRetry(t *testing.T) {
	sys := newFakeSys()
	sys.canFn = func(axis abi.CapAxis, scope string) (bool, error) {
		return axis != abi.AxisWrite, nil // 写一律要问人
	}
	sys.decision = abi.DecisionResolution{Choice: "no", By: "test"}
	_, err := runAgent(t, sys, []Step{
		{Tool: "write_file", Args: map[string]any{"path": "越界.txt", "content": "x"}},
		{Reply: "被拒了, 告诉用户"},
	}, "写个文件")
	if err != nil {
		t.Fatal(err)
	}
	if !sys.saw("denied") {
		t.Fatalf("没记下被拒: %v", sys.phases())
	}
}

// 人批准了就该照做
func TestApprovedWriteProceeds(t *testing.T) {
	sys := newFakeSys()
	sys.canFn = func(axis abi.CapAxis, scope string) (bool, error) {
		return axis != abi.AxisWrite, nil
	}
	sys.decision = abi.DecisionResolution{Choice: "yes", By: "test"}
	_, err := runAgent(t, sys, []Step{
		{Tool: "write_file", Args: map[string]any{"path": "a.txt", "content": "内容"}},
		{Done: "写完了并回读确认"},
	}, "写个文件")
	if err != nil {
		t.Fatal(err)
	}
	if !sys.saw("approved") {
		t.Fatalf("批准没留痕: %v", sys.phases())
	}
	if !sys.saw("tool_ok") {
		t.Fatal("批准之后工具没真跑")
	}
}

// 停滞硬停要真的中断, 不是只发个事件
func TestStallStopEndsTheRun(t *testing.T) {
	sys := newFakeSys()
	same := Step{Tool: "read_file", Args: map[string]any{"path": "不存在.txt"}}
	_, err := runAgent(t, sys, []Step{same, same, same, same, {Reply: "不该走到这"}}, "读文件")
	if err == nil {
		t.Fatal("原地转了四次还没被拦住")
	}
	if !strings.Contains(err.Error(), "停滞") {
		t.Fatalf("停的理由不对: %v", err)
	}
}

// 工具报错要进历史喂回去 —— 失败也是信息, 这是 ReAct 该有的行为
func TestToolErrorFeedsBack(t *testing.T) {
	sys := newFakeSys()
	_, err := runAgent(t, sys, []Step{
		{Tool: "read_file", Args: map[string]any{"path": "没有这个.txt"}},
		{Reply: "文件不存在, 已告知用户"},
	}, "读文件")
	if err != nil {
		t.Fatal(err)
	}
	if !sys.saw("tool_err") {
		t.Fatalf("工具错误没留痕: %v", sys.phases())
	}
	if !sys.saw("reply") {
		t.Fatal("出错之后没能继续")
	}
}

// 缺参数要在跑之前拒掉, 并且说清缺了什么
func TestMissingArgRejectedBeforeRunning(t *testing.T) {
	sys := newFakeSys()
	runAgent(t, sys, []Step{
		{Tool: "edit_file", Args: map[string]any{"old_string": "a", "new_string": "b"}},
		{Reply: "补上 path 再来"},
	}, "改文件")
	e := sys.last("tool_err")
	if e == nil {
		t.Fatal("缺参数没被拒")
	}
	if !strings.Contains(e["err"].(string), "path") {
		t.Fatalf("没说清缺哪个参数: %v", e["err"])
	}
}

// 并行批次要一次跑完并各自记账
func TestParallelBatchRecordsEach(t *testing.T) {
	sys := newFakeSys()
	_, err := runAgent(t, sys, []Step{
		{Tools: []ToolCall{
			{Tool: "list_dir", Args: map[string]any{"path": "."}},
			{Tool: "list_dir", Args: map[string]any{"path": "."}},
		}},
		{Done: "看完了"},
	}, "看目录")
	if err != nil {
		t.Fatal(err)
	}
	b := sys.last("batch")
	if b == nil {
		t.Fatalf("并行批次没留痕: %v", sys.phases())
	}
	if n, _ := b["n"].(int); n != 2 {
		t.Fatalf("批次记的数量不对: %v", b["n"])
	}
}

// 步数上限要兜住 —— 模型的开销预估不出来, 但跑飞了要能停
func TestMaxStepsStops(t *testing.T) {
	sys := newFakeSys()
	var steps []Step
	for i := 0; i < 50; i++ {
		// 每步换个路径, 免得被停滞检测先拦下 —— 这里要单测的是步数上限
		steps = append(steps, Step{Tool: "list_dir",
			Args: map[string]any{"path": strings.Repeat("a", i+1)}})
	}
	ts := NewToolSet(DefaultTools())
	ag := &Agent{ABI: sys, Model: &Scripted{Steps: steps}, Tools: ts,
		Box: Toolbox{Root: t.TempDir()}, MaxSteps: 5}
	if err := ag.Run("一直跑"); err == nil {
		t.Fatal("超过步数上限还在跑")
	}
}

// Serve: 一轮做完之后要待命等下一句, 而不是退出
func TestServeWaitsForNextTurn(t *testing.T) {
	sys := newFakeSys()
	sys.inbox = []abi.RecvResult{{Text: "再来一句"}}
	ts := NewToolSet(DefaultTools())
	win := NewWindow(engine.NewPageStore(), "第一句", 0)
	ag := &Agent{ABI: sys, Tools: ts, Box: Toolbox{Root: t.TempDir()},
		Window: win, MaxSteps: 5,
		Model: &Scripted{Steps: []Step{{Reply: "答第一句"}, {Reply: "答第二句"}}}}
	if err := ag.Serve("第一句"); err != nil {
		t.Fatal(err)
	}
	var replies int
	for _, p := range sys.phases() {
		if p == "reply" {
			replies++
		}
	}
	if replies != 2 {
		t.Fatalf("只回了 %d 句 —— 没有待命等下一句", replies)
	}
	// 第二轮的用户输入要进历史, 否则多轮记忆断了
	msgs, _ := win.Messages()
	var joined string
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "再来一句") {
		t.Fatal("第二轮的用户输入没进历史")
	}
}

// Serve: 单轮失败不该让整个对话死掉
func TestServeSurvivesOneBadTurn(t *testing.T) {
	sys := newFakeSys()
	sys.inbox = []abi.RecvResult{{Text: "第二句"}}
	ts := NewToolSet(DefaultTools())
	same := Step{Tool: "read_file", Args: map[string]any{"path": "x.txt"}}
	ag := &Agent{ABI: sys, Tools: ts, Box: Toolbox{Root: t.TempDir()},
		Window: NewWindow(engine.NewPageStore(), "第一句", 0), MaxSteps: 8,
		Model: &Scripted{Steps: []Step{same, same, same, same, {Reply: "第二轮好了"}}}}
	if err := ag.Serve("第一句"); err != nil {
		t.Fatalf("单轮失败把整个对话弄死了: %v", err)
	}
	if !sys.saw("turn_failed") {
		t.Fatalf("失败的那一轮没留痕: %v", sys.phases())
	}
	if !sys.saw("reply") {
		t.Fatal("失败之后没能接着聊")
	}
}

// 账本里的工具结果不该被截成 200 字.
//
// 截断属于呈现层: 终端渲染本来就又截了一次(70 字), 记录层那次是纯损失 ——
// 账本永久地少了证据. 最近四轮排查有三轮卡在这.
func TestLedgerKeepsEnoughOfTheResult(t *testing.T) {
	sys := newFakeSys()
	dir := t.TempDir()
	body := strings.Repeat("一行内容\n", 400) // 远超 200 字
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(body), 0o644)

	ag := &Agent{ABI: sys, Model: &Scripted{Steps: []Step{
		{Tool: "read_file", Args: map[string]any{"path": "big.txt"}},
		{Done: "读完了"},
	}}, Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: dir},
		Window: NewWindow(engine.NewPageStore(), "t", 0), MaxSteps: 5}
	if err := ag.Run("读"); err != nil {
		t.Fatal(err)
	}
	e := sys.last("tool_ok")
	got := e["result"].(string)
	if len(got) <= 300 {
		t.Fatalf("账本里只留了 %d 字节, 排查时什么都看不出来", len(got))
	}
}

// 但也不能无限 —— 账本每次启动都要整个读一遍
func TestLedgerResultIsBounded(t *testing.T) {
	huge := strings.Repeat("x", 500*1024)
	got := ledgerTrunc(huge)
	if len(got) > ledgerResultBytes+200 {
		t.Fatalf("账本里塞了 %d 字节, 启动会越来越慢", len(got))
	}
	// 截了要说出来, 否则排查的人以为看到的是全部
	if !strings.Contains(got, "原文共") {
		t.Fatal("截断没说出来")
	}
}

// 工具结果里要带上是哪次调用产生的 —— 只有结果没有参数, 对不上号
func TestLedgerRecordsArgsWithResult(t *testing.T) {
	sys := newFakeSys()
	ag := &Agent{ABI: sys, Model: &Scripted{Steps: []Step{
		{Tool: "list_dir", Args: map[string]any{"path": "."}},
		{Done: "看完了"},
	}}, Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: t.TempDir()},
		Window: NewWindow(engine.NewPageStore(), "t", 0), MaxSteps: 5}
	ag.Run("看")
	e := sys.last("tool_ok")
	if _, ok := e["args"]; !ok {
		t.Fatal("结果没带参数, 账本里对不上是哪次调用")
	}
}

// edit_file 越界必须**先问人**, 不能直接撞内核.
//
// 原来轴判断自己列了 write/delete 两个前缀, 漏了 edit ——
// 于是 edit_file 被当成读预检, 越界时拿一个 EPERM 就完事,
// 用户从来没被问过. "越界不是崩、是问人"正是这个 OS 跟沙盒的根本区别.
func TestEditOutsideScopeAsksHuman(t *testing.T) {
	// **只列真实存在的工具.**
	//
	// 这里原来还列了 delete_file —— 工具表里根本没有它.
	// 按名字前缀判轴的年代, 一个不存在的工具照样能"判出轴"来,
	// 于是这条断言一直在验一个幻影, 什么都没保护住.
	// 覆盖面由下面的 TestEveryMutatingToolDeclaresItsAxis 全量保证.
	for _, tool := range []string{"write_file", "edit_file"} {
		sys := newFakeSys()
		var askedAxis abi.CapAxis
		sys.canFn = func(axis abi.CapAxis, scope string) (bool, error) {
			askedAxis = axis
			return false, nil // 一律说"不在能力集里", 逼出问人这一步
		}
		sys.decision = abi.DecisionResolution{Choice: "no", By: "test"}

		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("原文"), 0o644)
		ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
			Box: Toolbox{Root: dir}, MaxSteps: 4,
			Window: NewWindow(engine.NewPageStore(), "t", 0),
			Model: &Scripted{Steps: []Step{
				{Tool: tool, Args: map[string]any{
					"path": "a.txt", "content": "新", "old_string": "原文", "new_string": "新"}},
				{Reply: "被拒了"},
			}}}
		ag.Run("改文件")

		if askedAxis != abi.AxisWrite {
			t.Fatalf("%s 按 %q 轴预检 —— 写操作被当成读, 等于用读权限做了写",
				tool, askedAxis)
		}
		if !sys.saw("denied") {
			t.Fatalf("%s 越界没走问人这一步: %v", tool, sys.phases())
		}
	}
}

// 全量: 每个会改动世界的工具都必须声明自己要哪条能力.
//
// 这条替代了"按名字前缀判轴"那套. 前缀表的失败模式是**静默**的:
// 漏一个前缀, 那个工具就被归进"读", 越界时直接撞内核拿 EPERM,
// 用户从来不会被问 —— 而"越界不是崩、是问人"正是这个 OS 的立身之本.
//
// 声明式的失败模式是**响的**: 加了工具忘记声明, 这条测试当场红.
func TestEveryMutatingToolDeclaresItsAxis(t *testing.T) {
	for _, tool := range DefaultTools() {
		if !tool.Mutates {
			continue
		}
		if tool.Needs != abi.AxisWrite && tool.Needs != abi.AxisProc {
			t.Errorf("%s 会改动东西却声明成 %q 轴 —— 越界时不会问人",
				tool.Name, tool.Needs)
		}
		if tool.ScopeArg == "" {
			t.Errorf("%s 没说哪个参数是 scope, 预检拿不到东西可查", tool.Name)
			continue
		}
		found := false
		for _, k := range tool.ArgOrder {
			if k == tool.ScopeArg {
				found = true
			}
		}
		if !found {
			t.Errorf("%s 的 ScopeArg=%q 不在参数表里 —— 模型永远不会传这个键, "+
				"预检等于没有", tool.Name, tool.ScopeArg)
		}
	}
}

// 只读工具不该走写轴 —— 判得太宽会白白打扰用户
func TestReadToolsUseReadAxis(t *testing.T) {
	for _, tool := range []string{"read_file", "list_dir", "search", "find_files"} {
		sys := newFakeSys()
		var axis abi.CapAxis
		sys.canFn = func(a abi.CapAxis, scope string) (bool, error) { axis = a; return true, nil }
		ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
			Box: Toolbox{Root: t.TempDir()}, MaxSteps: 3,
			Window: NewWindow(engine.NewPageStore(), "t", 0),
			Model: &Scripted{Steps: []Step{
				{Tool: tool, Args: map[string]any{"path": ".", "pattern": "x"}},
				{Reply: "好"},
			}}}
		ag.Run("看看")
		if axis != abi.AxisRead {
			t.Fatalf("%s 按 %q 轴预检了", tool, axis)
		}
	}
}

// 直接回话那一轮也要记账.
//
// 用量播报在循环末尾, 而 Reply 路径在前面就 return 了 ——
// **最常见的一条路径从来不记账**. 一段六轮对话只留下 1 条 usage,
// 5 个回复却只有 1 次推理, 数据自相矛盾, 查了才发现是漏发.
func TestReplyTurnStillReportsUsage(t *testing.T) {
	sys := newFakeSys()
	ag := &Agent{
		ABI:   sys,
		Model: &usageModel{Step{Reply: "你好"}},
		Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: t.TempDir()},
		Window: NewWindow(engine.NewPageStore(), "嗨", 0), MaxSteps: 3,
	}
	if err := ag.Run("嗨"); err != nil {
		t.Fatal(err)
	}
	if !sys.saw("usage") {
		t.Fatalf("直接回话那一轮没记账: %v", sys.phases())
	}
	if !sys.saw("reply") {
		t.Fatal("回话本身也没发")
	}
}

// usageModel 一个会报用量的假模型 —— Scripted 不带用量, 测不到记账
type usageModel struct{ step Step }

func (u *usageModel) Name() string { return "usage-fake" }
func (u *usageModel) Next(task string, history []Observation) (Step, error) {
	return u.step, nil
}
func (u *usageModel) LastUsage() abi.InferResult {
	return abi.InferResult{PromptTokens: 1200, CachedTokens: 1100, CompletionTokens: 40}
}

// **失败也要带上参数**.
//
// tool_ok 一直带着 args, tool_err 不带 —— 而失败恰恰最需要认清
// "是哪一次调用"的那种. 一组展开之后四行并排的 list_dir,
// 后面什么都没有, 看不出哪个路径没找到.
//
// 串行调用还能从前面那条 step 里认领参数; 并行批次不发 step,
// 于是那几行从头到尾都是空的.
func TestToolErrorCarriesTheArgs(t *testing.T) {
	sys := newFakeSys()
	runAgent(t, sys, []Step{
		{Tool: "read_file", Args: map[string]any{"path": "没有这个.txt"}},
		{Reply: "文件不存在"},
	}, "读文件")
	e := sys.last("tool_err")
	if e == nil {
		t.Fatal("工具错误没留痕")
	}
	args, ok := e["args"].(map[string]any)
	if !ok {
		t.Fatalf("失败没带参数 —— 界面上只剩一个工具名, 看不出是哪一次: %v", e)
	}
	if args["path"] != "没有这个.txt" {
		t.Fatalf("带的参数不对: %v", args)
	}
}

// 缺参数那条也要带 —— 它恰恰要让人看见"它当时传的是什么"
func TestRejectedCallStillShowsWhatItTried(t *testing.T) {
	sys := newFakeSys()
	runAgent(t, sys, []Step{
		{Tool: "edit_file", Args: map[string]any{"old_string": "a", "new_string": "b"}},
		{Reply: "补上 path"},
	}, "改文件")
	e := sys.last("tool_err")
	args, ok := e["args"].(map[string]any)
	if !ok || args["old_string"] != "a" {
		t.Fatalf("被拒的那次没留下它传了什么: %v", e)
	}
}

// **一个并行批次不该被当成"连着撞了四次墙"**.
//
// 并行读取四个不存在的文件时,
// 于是一个批次把停滞计数顶到 4, 这一轮当场被停掉 —— 而"一次查一批文件
// 在不在"恰恰是最常见的并行用法, 它还来不及从第一个失败里学到任何东西.
//
// 这一条走 Agent 这一层, 因为要钉的是**批次那条路上有没有划步**;
// 只测 tracker 的话, agent.go 里漏掉那一行也照样绿.
func TestParallelBatchIsOneStepForStall(t *testing.T) {
	sys := newFakeSys()
	batch := Step{Tools: []ToolCall{
		{ID: "1", Tool: "read_file", Args: map[string]any{"path": "a.md"}},
		{ID: "2", Tool: "read_file", Args: map[string]any{"path": "b.md"}},
		{ID: "3", Tool: "read_file", Args: map[string]any{"path": "c.md"}},
		{ID: "4", Tool: "read_file", Args: map[string]any{"path": "d.md"}},
	}}
	_, err := runAgent(t, sys, []Step{batch, {Reply: "四个都不在"}}, "并行读四个不存在的文件")
	if err != nil {
		t.Fatalf("一个批次全失败就把这一轮停了: %v", err)
	}
	if sys.saw("stall") {
		t.Fatalf("一个批次里的四次失败被当成了连着撞墙: %v", sys.last("stall"))
	}
	if !sys.saw("reply") {
		t.Fatal("被停在半路, 没能把结论说出来")
	}
}

// 一批接一批地全挂, **还是要停** —— 那才是真的在原地转.
//
// 这一条钉的是批次那条路上**有没有划步**: 不划的话, 每一批的失败都会被
// 当成"同一步里的重复"吞掉, 于是无论撞多少批墙都不会停. 那比误报更糟:
// 误报是多停一次, 这个是永远不停.
func TestBatchAfterBatchStillStallsThroughAgent(t *testing.T) {
	sys := newFakeSys()
	batch := func(n string) Step {
		return Step{Tools: []ToolCall{
			{ID: n + "1", Tool: "read_file", Args: map[string]any{"path": n + "a.md"}},
			{ID: n + "2", Tool: "read_file", Args: map[string]any{"path": n + "b.md"}},
		}}
	}
	_, err := runAgent(t, sys, []Step{
		batch("p"), batch("q"), batch("r"), batch("s"), {Reply: "不该走到这儿"},
	}, "一批接一批地撞墙")
	if err == nil {
		t.Fatal("撞了四批墙还不停 —— 停滞检测在批次这条路上整个失效了")
	}
	if !sys.saw("stall") {
		t.Fatalf("没发停滞: %v", sys.phases())
	}
}

/**
 * 超时之后往哪儿走, 得看它跑的是什么活.
 *
 *	连续三次超时都发生在 npm install, 而那句"别原样重来, 缩小范围"对
 *	装依赖是反的: 没有范围可缩, 于是它去查 registry、查 cache、试参数,
 *	一轮的时间就这么没了. 下载缓存是增量的, 重来一次就接着下.
 */
func Test下东西超时了要让它接着下(t *testing.T) {
	got := timeoutAdvice(map[string]any{"cmd": "npm install 2>&1 | tail -25"})
	if !strings.Contains(got, "接着下") {
		t.Errorf("没告诉它重来就行:\n%s", got)
	}
	if strings.Contains(got, "缩小范围") {
		t.Errorf("对装依赖说了'缩小范围' —— 没有范围可缩:\n%s", got)
	}
	for _, cmd := range []string{"pip install -r req.txt", "uv sync", "go mod download"} {
		if !fetching(cmd) {
			t.Errorf("%q 也是在下东西", cmd)
		}
	}
	// 别的活照旧 —— 那条建议本身是对的
	other := timeoutAdvice(map[string]any{"cmd": "grep -r x /"})
	if !strings.Contains(other, "缩小范围") {
		t.Errorf("普通命令超时的建议丢了:\n%s", other)
	}
	if !strings.Contains(timeoutAdvice(nil), "缩小范围") {
		t.Error("没有 cmd 的工具(比如 request_access)超时时该走缺省那条")
	}
}

/**
 * 调不通模型的时候, **人下一步该干什么**.
 *
 *	这一轮就此结束, 而用户读到的是供应商的原话: "context deadline
 *	exceeded"、"429 Too Many Requests"、"invalid api key". 他既不知道
 *	是自己的网、是额度、还是配置错了, 也不知道要不要再说一句.
 *
 *	长会话里这是最可能撞上的一件事 —— 几个小时下来网抖一次几乎必然.
 */
func Test调不通模型要说人该干什么(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"Post \"https://api/x\": context deadline exceeded", "网"},
		{"dial tcp: connection refused", "网"},
		{"429 Too Many Requests", "限流"},
		{"401 unauthorized: invalid api key", "key"},
		{"402 insufficient balance", "额度"},
	} {
		got := whatNow(errors.New(c.raw))
		if !strings.Contains(got, c.want) {
			t.Errorf("%q → %q，没说到「%s」", c.raw, got, c.want)
		}
	}
	// 网和限流那两种要说清活没丢 —— 那是他此刻最想知道的
	for _, raw := range []string{"context deadline exceeded", "429 rate limit"} {
		if !strings.Contains(whatNow(errors.New(raw)), "收好了") {
			t.Errorf("%q 没说清手上的活没丢", raw)
		}
	}
	// **认不出来就别编** —— 给一条针对不了的建议比不给更糟
	if got := whatNow(errors.New("模型说了句谁也看不懂的话")); got != "" {
		t.Errorf("认不出来还编了一句: %q", got)
	}
}
