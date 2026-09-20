package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// checkPairing 原生协议最要命的一条不变量:
//
//	**每一条带 tool_calls 的 assistant 消息, 后面必须紧跟着
//	对应 id 的结果消息.**
//
// 少一条、对不上、或者出现一条没有归属的结果 —— 供应商都会拒掉
// **整个请求**. 而现象跟真实原因隔得很远: 比如折叠一触发就再也发不出去,
// 看着像折叠逻辑坏了, 其实是少了一条配对消息.
//
// 这是自造 JSON 协议完全没有的约束, 所以以前的测试一条都没覆盖到.
func checkPairing(t *testing.T, msgs []abi.InferMessage) {
	t.Helper()
	open := map[string]bool{}
	for i, m := range msgs {
		switch {
		case len(m.ToolCalls) > 0:
			if len(open) > 0 {
				t.Fatalf("第 %d 条又发起了调用, 而上一批 %v 还没有结果 —— 供应商会拒整个请求",
					i, open)
			}
			for _, c := range m.ToolCalls {
				if c.ID == "" {
					t.Fatalf("第 %d 条调用没有 id, 结果无从配对", i)
				}
				if open[c.ID] {
					t.Fatalf("第 %d 条出现了重复的 id %s", i, c.ID)
				}
				open[c.ID] = true
			}
		case m.Role == "tool_result":
			if !open[m.ToolCallID] {
				t.Fatalf("第 %d 条是一条**没有归属**的结果消息 (id=%s)", i, m.ToolCallID)
			}
			delete(open, m.ToolCallID)
		}
	}
	if len(open) > 0 {
		t.Fatalf("调用 %v 一直没有结果 —— 供应商会拒整个请求", open)
	}
}

func nativeWindow(t *testing.T, budget int) *Window {
	t.Helper()
	w := NewWindow(engine.NewPageStore(), "干活", budget)
	t.Cleanup(w.Release)
	return w
}

func TestNativeCallsAndResultsPairUp(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendStep("read_file", map[string]any{"path": "a.md"}, "call_01_x")
	w.AppendResult("文件内容", "", "call_01_x")
	w.AppendStep("write_file", map[string]any{"path": "b.md", "content": "x"}, "call_02_x")
	w.AppendResult("", "写不进去", "call_02_x")
	msgs, _ := w.Messages()
	checkPairing(t, msgs)

	// 结果确实走的是 tool_result 而不是普通消息
	n := 0
	for _, m := range msgs {
		if m.Role == "tool_result" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("两次调用应该有两条结果消息, got %d: %+v", n, msgs)
	}
}

// **折叠之后配对必须还成立.**
//
// 折叠通知原来是单独一条 user 消息 —— 自造协议下没问题, 原生协议下
// 那次调用就没有结果了, 整个请求被拒. 现象是"一折叠就再也发不出去",
// 跟折叠逻辑本身毫无关系, 极难查.
func TestFoldingKeepsPairingIntact(t *testing.T) {
	w := nativeWindow(t, 4000)
	for i := 0; i < 6; i++ {
		// id 必须**各不相同** —— 同一个 id 出现两次, 供应商侧的配对就废了
		id := fmt.Sprintf("call_1%d_x", i)
		w.AppendStep("read_file", map[string]any{"path": "big.log"}, id)
		w.AppendResult(strings.Repeat("很长的内容", 500), "", id)
	}
	msgs, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("这个预算下应该折叠了 —— 测试没测到目标场景")
	}
	checkPairing(t, msgs)

	// 折叠通知要真的出现在结果位上, 而不是消失
	found := false
	for _, m := range msgs {
		if m.Role == "tool_result" && strings.Contains(m.Content, "内容已省略") {
			found = true
		}
	}
	if !found {
		t.Fatal("折叠通知没有占住结果位 —— 模型会以为那一步没有结果")
	}
}

// 用户中途插话之后仍然成对
func TestPairingSurvivesUserTurns(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendStep("list_dir", map[string]any{"path": "."}, "call_04_x")
	w.AppendResult("a.md b.md", "", "call_04_x")
	w.AppendUserTurn("再看看 b.md")
	w.AppendStep("read_file", map[string]any{"path": "b.md"}, "call_05_x")
	w.AppendResult("内容", "", "call_05_x")
	w.AppendAssistantSaid("看完了")
	msgs, _ := w.Messages()
	checkPairing(t, msgs)
}

// 调用编号必须**跨轮逐字节稳定** —— 它进前缀, 一变缓存全废.
//
// 编号存的是**供应商给的那一个** (原因见下面那条测试), 所以稳定性
// 来自"页只增不改", 而不是来自每轮重新推导.
func TestCallIDsAreByteStable(t *testing.T) {
	w := nativeWindow(t, 0)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("call_2%d_x", i)
		w.AppendStep("read_file", map[string]any{"path": "x"}, id)
		w.AppendResult("y", "", id)
	}
	first, _ := w.Messages()
	for i := 0; i < 20; i++ {
		again, _ := w.Messages()
		if len(again) != len(first) {
			t.Fatal("消息条数不稳定")
		}
		for j := range first {
			if !msgEqual(first[j], again[j]) {
				t.Fatalf("第 %d 条不稳定 —— 前缀缓存会全废\n%+v\n%+v",
					j, first[j], again[j])
			}
		}
	}
}

// 追加新的一轮不许改动**已有前缀** —— 那是缓存能命中的全部前提
func TestAppendingDoesNotRewritePrefix(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendStep("read_file", map[string]any{"path": "a"}, "call_07_x")
	w.AppendResult("A", "", "call_07_x")
	before, _ := w.Messages()

	w.AppendStep("read_file", map[string]any{"path": "b"}, "call_08_x")
	w.AppendResult("B", "", "call_08_x")
	after, _ := w.Messages()

	if len(after) <= len(before) {
		t.Fatal("追加之后消息没变多")
	}
	for i := range before {
		if !msgEqual(before[i], after[i]) {
			t.Fatalf("第 %d 条前缀被改写了 —— 缓存全废\n之前 %+v\n之后 %+v",
				i, before[i], after[i])
		}
	}
}

// ── 工具声明 ──

// 声明必须字节稳定: 它进请求前缀, 跟系统段一样.
//
// 直接 json.Marshal 一个 map 就会踩这个坑 —— Go 的 map 序列化顺序
// 是随机的, 缓存会**每一轮都失效**, 而且失效是静默的:
// 只能从命中率突然掉到 0 才看得出来.
func TestToolSchemaIsByteStable(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	first, _ := json.Marshal(ToolDefs(ts))
	for i := 0; i < 100; i++ {
		again, _ := json.Marshal(ToolDefs(NewToolSet(DefaultTools())))
		if string(again) != string(first) {
			t.Fatal("工具声明不稳定 —— 前缀缓存会全废")
		}
	}
}

// 声明要跟工具表一一对上, 而且必需参数不能漏
func TestToolSchemaMatchesToolTable(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	defs := ToolDefs(ts)
	if len(defs) != len(ts.Names()) {
		t.Fatalf("声明 %d 个, 工具表 %d 个", len(defs), len(ts.Names()))
	}
	for _, d := range defs {
		tool, ok := ts.Get(d.Name)
		if !ok {
			t.Fatalf("声明里有个工具表没有的工具: %s", d.Name)
		}
		var sch struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(d.Params, &sch); err != nil {
			t.Fatalf("%s 的 schema 不是合法 JSON: %v", d.Name, err)
		}
		for _, k := range tool.ArgOrder {
			if _, ok := sch.Properties[k]; !ok {
				t.Errorf("%s 的参数 %s 没进声明 —— 模型不会传它", d.Name, k)
			}
		}
		// 可选参数不该出现在 required 里, 否则模型每次都被迫编一个值
		for _, r := range sch.Required {
			if tool.Optional[r] {
				t.Errorf("%s 把可选参数 %s 标成必填了", d.Name, r)
			}
		}
	}
}

// 参数解不开时的报错要指向**适配层**, 不要指向提示词.
//
// 原生协议下参数结构由供应商保证. 报成"模型格式错误"会让排查的人
// 去调提示词 —— 那个方向永远查不出来.
func TestBadArgumentsBlameTheRightLayer(t *testing.T) {
	_, err := stepFromCalls(abi.InferResult{
		ToolCalls: []abi.ToolCall{{ID: "c1", Name: "read_file", Arguments: "{坏的"}}})
	if err == nil {
		t.Fatal("参数解不开却没报错")
	}
	if !strings.Contains(err.Error(), "供应商") {
		t.Fatalf("报错没指向适配层, 会把人带去调提示词: %v", err)
	}
}

// 空参数要给空对象而不是 null —— null 会让供应商判整条调用格式错误,
// 而"这个工具不需要参数"是完全正常的 (list_dir 不给 path 就是当前目录)
func TestEmptyArgsSerializeAsObject(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendStep("list_dir", nil, "call_09_x")
	w.AppendResult("a b", "", "call_09_x")
	msgs, _ := w.Messages()
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 {
			if m.ToolCalls[0].Arguments == "null" || m.ToolCalls[0].Arguments == "" {
				t.Fatalf("空参数序列化成了 %q —— 供应商会判格式错误",
					m.ToolCalls[0].Arguments)
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(m.ToolCalls[0].Arguments), &v); err != nil {
				t.Fatalf("参数不是合法 JSON 对象: %q", m.ToolCalls[0].Arguments)
			}
		}
	}
}

// 没有工具调用 = 它在说话, **不该再按 JSON 解析**.
//
// 那是自造协议时代的做法. 继续解析的话, 一句正常的中文回复会被当成
// "格式错误"打回去 —— 而模型完全没做错任何事.
func TestPlainTextIsAReplyNotAFormatError(t *testing.T) {
	s, err := stepFromCalls(abi.InferResult{Content: "我看了一下，这个函数没问题。"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tools) != 0 || s.Tool != "" {
		t.Fatal("没有 tool_calls 却解析出了工具调用")
	}
}

// ── 调用 id 必须沿用原值, 不能按位置重新生成 ──
//
// 按位置推导 id (call_0/call_1…) 看似可行, 因为 id 只是重建这段对话时的
// 配对键, 用哪个值似乎都行. **这个假设是错的.**
//
// 供应商靠 tool_call id 认"这一轮是不是自己生成的". id 不是它给的,
// 它就把这条 assistant 消息当成外部拼出来的, 转而要求交出
// reasoning_content —— 报出来的错是
// "The reasoning_content in the thinking mode must be passed back",
// **跟真实原因(id 不对)完全不是一回事**, 只按这句错误信息排查无法定位问题。
//
// 同一段对话只更换 id 时, 供应商 id 通过, 重建的 id 被拒, 重建的 id
// 再带上 reasoning_content 才通过.
func TestNeverFabricateCallIDs(t *testing.T) {
	w := nativeWindow(t, 0)
	// 没有 id 的动作页 —— 从事件日志恢复出来的历史就是这样
	w.AppendStep("read_file", map[string]any{"path": "a"}, "")
	w.AppendResult("内容", "", "")
	msgs, _ := w.Messages()
	for i, m := range msgs {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("第 %d 条给没有 id 的动作编了一个 id (%s) —— "+
				"供应商会把整条消息当成外部拼的, 然后要 reasoning_content",
				i, m.ToolCalls[0].ID)
		}
		if m.Role == "tool_result" {
			t.Fatalf("第 %d 条发了一条结果消息, 而没有任何调用可以配对", i)
		}
	}
	// 退回文本形式之后内容不能丢 —— 模型还得知道自己做过什么
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "read_file") {
		t.Fatal("退回文本形式之后动作没了, 模型不知道自己做过什么")
	}
}

// 恢复出来的历史 + 本轮新发生的调用混在一起时, 配对仍然成立.
//
// 这是最容易出事的组合: 前半段是文本 (没有 id), 后半段是原生 (有 id).
// 混错了就会出现"一条没有归属的结果消息", 整个请求被拒.
func TestRestoredHistoryAndLiveCallsMixSafely(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendStep("list_dir", map[string]any{"path": "."}, "") // 恢复来的
	w.AppendResult("a.md", "", "")
	w.AppendUserTurn("接着看")
	w.AppendStep("read_file", map[string]any{"path": "a.md"}, "call_90_x") // 本轮新的
	w.AppendResult("内容", "", "call_90_x")
	checkPairing(t, mustMsgs(w))
}

func mustMsgs(w *Window) []abi.InferMessage {
	m, _ := w.Messages()
	return m
}

// ── 一次决定 = 一条消息, 哪怕里面有好几个调用 ──
//
// 模型发的是**一条**带 3 个 tool_calls
// 的消息, 而我按调用逐个落页, 重建时就变成了 3 条各带 1 个.
//
// id 全都是它给的、也都对得上, 但**形状不对** —— 供应商同样判定
// "这不是我生成的", 报的还是那句 reasoning_content.
// 所以对得上 id 不够, 还得还原成它当初发出来的样子.
func TestBatchRebuildsAsOneMessage(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendBatch([]PageCall{
		{ID: "call_a", Tool: "run", Args: map[string]any{"cmd": "python3 t.py"}},
		{ID: "call_b", Tool: "read_file", Args: map[string]any{"path": "a.py"}},
		{ID: "call_c", Tool: "read_file", Args: map[string]any{"path": "b.py"}},
	}, "我先跑一下测试再看源码")
	w.AppendResult("退出码 1", "", "call_a")
	w.AppendResult("源码 A", "", "call_b")
	w.AppendResult("源码 B", "", "call_c")

	msgs, _ := w.Messages()
	checkPairing(t, msgs)

	n, calls := 0, 0
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 {
			n++
			calls += len(m.ToolCalls)
		}
	}
	if n != 1 {
		t.Fatalf("一次决定被拆成了 %d 条 assistant 消息 —— 形状跟模型发的不一样, 会被拒", n)
	}
	if calls != 3 {
		t.Fatalf("3 个调用只重建出 %d 个", calls)
	}
}

// 批次里只要有一个没有 id, **整批**退回文本.
//
// 只发有 id 的那几个会让形状跟模型当初发的不一样 —— 形状不对
// 跟 id 不对是同一类拒绝.
func TestPartialIDsFallBackWholeBatch(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendBatch([]PageCall{
		{ID: "call_a", Tool: "run", Args: map[string]any{"cmd": "x"}},
		{ID: "", Tool: "read_file", Args: map[string]any{"path": "a"}},
	}, "")
	w.AppendResult("结果", "", "call_a")
	for i, m := range mustMsgs(w) {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("第 %d 条只把有 id 的那个发了出去 —— 形状对不上, 一样会被拒", i)
		}
	}
}

// ── DeepSeek thinking 协议双向 400 规则 ──
//
// 带 tool_calls 的 assistant 消息**必须**回传 reasoning_content,
// 纯文本消息**必须不**回传. 两个方向都会被拒.
//
// 只做一半是真出过的错 (6X 那边 commit a1140034 只修一半),
// 所以这条测试两边都验.
//
// 为什么不能靠"用真 id 就不用回传": 即使当前请求能通过, 供应商识别
// "这是不是我生成的那一轮"是有时效的 —— 那等于把正确性押在服务端
// 缓存上, 同一段对话可能今天能发明天就 400.
func TestReasoningPassedBackOnlyWithToolCalls(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendBatch([]PageCall{
		{ID: "call_r", Tool: "run", Args: map[string]any{"cmd": "ls"}},
	}, "我要先看看目录里有什么")
	w.AppendResult("a.md", "", "call_r")
	w.AppendAssistantSaid("目录里只有 a.md")

	for i, m := range mustMsgs(w) {
		switch {
		case len(m.ToolCalls) > 0:
			if m.Reasoning == "" {
				t.Fatalf("第 %d 条带 tool_calls 却没回传推理 —— DeepSeek 会 400", i)
			}
		default:
			if m.Reasoning != "" {
				t.Fatalf("第 %d 条是纯文本却回传了推理 —— 这个方向同样会 400", i)
			}
		}
	}
}

// 批次里提前中断, 不许留下没有结果的调用.
//
// 真机路径: 3 个并发调用, 停滞判定在第 1 个上判死并 return ——
// 后两个的结果就永远不会落页. 那不是"这一轮失败"而已: 历史是累积的,
// **之后每一轮都会把这段坏历史重发一遍**, 供应商见一次拒一次,
// 这个对话就永久废掉了.
func TestInterruptedBatchLeavesNoUnansweredCall(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendBatch([]PageCall{
		{ID: "call_x", Tool: "run", Args: map[string]any{"cmd": "a"}},
		{ID: "call_y", Tool: "run", Args: map[string]any{"cmd": "b"}},
		{ID: "call_z", Tool: "run", Args: map[string]any{"cmd": "c"}},
	}, "并发跑三条")
	// 只落了第一个结果就被中断
	w.AppendResult("结果 A", "", "call_x")

	msgs := mustMsgs(w)
	checkPairing(t, msgs)

	// 占位必须紧跟在那条 assistant 后面, 中间不许夹别的消息 ——
	// 有的供应商要求 assistant(tool_calls) 与其结果紧邻
	for i, m := range msgs {
		if len(m.ToolCalls) == 0 {
			continue
		}
		for j := range m.ToolCalls {
			nxt := i + 1 + j
			if nxt >= len(msgs) || msgs[nxt].Role != "tool_result" {
				t.Fatalf("第 %d 条调用后面没有紧跟结果", i)
			}
		}
	}
}

// 兜底不许改动已经有结果的调用 —— 补占位只补真缺的那些
func TestPairingGuardDoesNotTouchAnsweredCalls(t *testing.T) {
	w := nativeWindow(t, 0)
	w.AppendBatch([]PageCall{
		{ID: "call_p", Tool: "run", Args: map[string]any{"cmd": "a"}},
	}, "跑一条")
	w.AppendResult("真结果", "", "call_p")
	for _, m := range mustMsgs(w) {
		if m.Role == "tool_result" && m.ToolCallID == "call_p" &&
			!strings.Contains(m.Content, "真结果") {
			t.Fatalf("已有结果被占位覆盖了: %q", m.Content)
		}
	}
}

// 动作页被换出之后, 结果就成了孤儿 —— 必须丢掉.
//
// 页表按页换出, 它不认识"动作和结果是一对"这回事. 留一条没有归属的
// 结果消息在请求里, 供应商会拒掉**整个请求**.
func TestOrphanResultIsDropped(t *testing.T) {
	msgs := repairPairs([]abi.InferMessage{
		{Role: "user", Content: "任务"},
		{Role: "tool_result", ToolCallID: "已经没了的调用", Content: "结果"},
	})
	for i, m := range msgs {
		if m.Role == "tool_result" {
			t.Fatalf("第 %d 条孤儿结果没被丢掉 —— 整个请求会被拒", i)
		}
	}
	checkPairing(t, msgs)
}

// 既没内容也没调用的消息一律不发 —— 供应商会拒
func TestEmptyMessagesAreDropped(t *testing.T) {
	msgs := repairPairs([]abi.InferMessage{
		{Role: "user", Content: "任务"},
		{Role: "assistant_reply", Content: ""},
		{Role: "user", Content: "接着说"},
	})
	if len(msgs) != 2 {
		t.Fatalf("空消息没被丢掉: %+v", msgs)
	}
}

// 两个方向同时出问题时也要修好
func TestBothDirectionsRepairedTogether(t *testing.T) {
	msgs := repairPairs([]abi.InferMessage{
		{Role: "user", Content: "任务"},
		{Role: "assistant_reply", ToolCalls: []abi.ToolCall{{ID: "有调用没结果"}}},
		{Role: "tool_result", ToolCallID: "有结果没调用", Content: "孤儿"},
	})
	checkPairing(t, msgs)
}

// 真实场景: 动作页真的被换出去, Messages() 仍然要产出合法请求.
//
// 上面那条是手工拼的消息数组, 这条走真的页表 —— 两者缺一不可:
// 手工的验修复逻辑, 这条验**修复逻辑真的在那条路径上**.
func TestEvictedActionPageStillProducesValidRequest(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "干活", 0)
	defer w.Release()

	w.AppendBatch([]PageCall{{ID: "call_e", Tool: "run", Args: map[string]any{"cmd": "x"}}}, "推理")
	w.AppendResult("结果", "", "call_e")

	// 把动作页换出去 (页表按页降级, 它不认识"动作和结果是一对")
	ids := w.space.PageIDs()
	evicted := false
	for _, id := range ids {
		p, err := store.Get(id)
		if err != nil || p.Kind != engine.KindOutput {
			continue
		}
		if store.Evict(id) {
			evicted = true
		}
	}
	if !evicted {
		t.Skip("这个页表实现换不出去, 场景不适用")
	}
	checkPairing(t, mustMsgs(w))
}
