package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

func foldWindow(t *testing.T, budget int) *Window {
	t.Helper()
	return NewWindow(engine.NewPageStore(), "任务原文", budget)
}

func bigResult(n int) string { return strings.Repeat("x", n) }

// 折叠必须只折结果, **动作要留着**.
//
// 上一版成对丢弃动作+结果, 模型完全不知道自己做过那件事, 于是重做一遍,
// 重做的结果又是一大坨, 立刻再次超限 —— 越省越糟.
func TestFoldKeepsActions(t *testing.T) {
	w := foldWindow(t, 4096)
	for i := 0; i < 12; i++ {
		w.AppendStep("read_file", map[string]any{"path": "f" + string(rune('a'+i))}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	msgs, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("超限了却没折叠")
	}
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	// 每一个动作都必须还在 —— 这是"我做过什么"的全部证据
	for i := 0; i < 12; i++ {
		want := "f" + string(rune('a'+i))
		if !strings.Contains(joined, want) {
			t.Fatalf("动作 %s 被丢了, 模型会重做一遍", want)
		}
	}
}

// 输入轮次永不折叠 —— 折掉它"再改一下"就没有指代对象了
func TestUserTurnsNeverFolded(t *testing.T) {
	w := foldWindow(t, 2048)
	w.AppendUserTurn("先建 notes.md")
	for i := 0; i < 10; i++ {
		w.AppendStep("read_file", map[string]any{"path": "big.log"}, "")
		w.AppendResult(bigResult(3000), "", "")
	}
	w.AppendUserTurn("再加一条")
	msgs, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("超限了却没折叠")
	}
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	for _, want := range []string{"先建 notes.md", "再加一条"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("用户说的 %q 被折掉了 —— 多轮记忆当场失效", want)
		}
	}
}

// 折叠了要说出来, 而且要说清"你做过这一步" ——
// 静默省略会让模型以为那步没有结果, 然后瞎猜
func TestFoldIsLoud(t *testing.T) {
	w := foldWindow(t, 2048)
	for i := 0; i < 10; i++ {
		w.AppendStep("read_file", map[string]any{"path": "a.log"}, "")
		w.AppendResult(bigResult(3000), "", "")
	}
	msgs, _ := w.Messages()
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "已省略") || !strings.Contains(joined, "确实做过") {
		t.Fatal("折叠没说出来, 或者没告诉它这步做过")
	}
	// 上下文放不下才折的, 叫它原样重读会再次被折 —— 那是死循环的邀请函.
	// 同一段可能被读 5 次, offset 在 1/401/1/801/1 之间来回跳.
	if !strings.Contains(joined, "一模一样的参数") {
		t.Fatal("必须明说原样重来没用, 否则模型会照做并死循环")
	}
}

// 一次折够到水位线 —— 刚好够会导致几乎每轮都触发一次, 缓存反复作废
func TestFoldsToWatermarkNotJustEnough(t *testing.T) {
	const budget = 8192
	w := foldWindow(t, budget)
	for i := 0; i < 10; i++ {
		w.AppendStep("read_file", map[string]any{"path": "a.log"}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	_, trim := w.Messages()
	remaining := 0
	for _, id := range w.space.PageIDs() {
		remaining += w.space.Store.BytesOf(id)
	}
	if remaining-trim.DroppedBytes > budget/2 {
		t.Fatalf("没折到水位线: 剩 %d, 水位 %d", remaining-trim.DroppedBytes, budget/2)
	}
}

// 没超限就一个字都不许动 —— 前缀必须逐字节稳定
func TestNoFoldUnderBudget(t *testing.T) {
	w := foldWindow(t, 64*1024)
	w.AppendStep("read_file", map[string]any{"path": "a.md"}, "")
	w.AppendResult("很短的结果", "", "")
	msgs, trim := w.Messages()
	if trim.Dropped != 0 {
		t.Fatal("没超限却折叠了")
	}
	if len(msgs) != 3 {
		t.Fatalf("消息数不对: %d", len(msgs))
	}
}

// 折叠过的历史再追加, 前面已折的部分要保持一致 —— 否则前缀每轮都在变
func TestFoldedPrefixStaysStable(t *testing.T) {
	w := foldWindow(t, 4096)
	for i := 0; i < 10; i++ {
		w.AppendStep("read_file", map[string]any{"path": "a.log"}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	first, _ := w.Messages()
	w.AppendStep("list_dir", map[string]any{"path": "."}, "")
	w.AppendResult("site/", "", "")
	second, _ := w.Messages()

	if len(second) <= len(first) {
		t.Fatal("追加之后消息没变多")
	}
	// 已折的那一段不该因为又追加了两页就重新变化
	same := 0
	for i := 0; i < len(first) && i < len(second); i++ {
		if msgEqual(first[i], second[i]) {
			same++
		} else {
			break
		}
	}
	if same < len(first)/2 {
		t.Fatalf("前缀只稳定了 %d/%d 条 —— 缓存会大面积作废", same, len(first))
	}
}

// 最新的结果永远不折 —— 折掉它模型会立刻重做, 陷入死循环.
//
// read_file 结果一到就被折时, 模型会连读三次同一段,
// 被停滞检测硬停, 最后只能报告每次都被截断而没有看到内容.
func TestNewestResultNeverFolded(t *testing.T) {
	w := foldWindow(t, 2048)
	for i := 0; i < 8; i++ {
		w.AppendStep("read_file", map[string]any{"path": "big.log"}, "")
		w.AppendResult(bigResult(3000), "", "")
	}
	w.AppendStep("read_file", map[string]any{"path": "big.log", "offset": 401}, "")
	w.AppendResult("这是刚要来的结果 FATAL 在第 777 行", "", "")

	msgs, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("该折的没折")
	}
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "FATAL 在第 777 行") {
		t.Fatal("刚到手的结果被折掉了 —— 模型会立刻重做, 死循环")
	}
}

// 就算最新那条自己就超预算, 也不能折掉它 —— 折了就没有任何前进的可能
func TestHugeNewestResultSurvives(t *testing.T) {
	w := foldWindow(t, 1024)
	w.AppendStep("read_file", map[string]any{"path": "a"}, "")
	w.AppendResult(bigResult(5000), "", "")
	w.AppendStep("read_file", map[string]any{"path": "b"}, "")
	w.AppendResult("唯一有用的那句话", "", "")
	msgs, _ := w.Messages()
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "唯一有用的那句话") {
		t.Fatal("最新结果在极小预算下也必须保留")
	}
}

// 折叠通知不能把"接着读别的部分"也劝退.
//
// 只写"**原样重读一遍没有意义**"会被理解成"别再碰这个文件":
// 第一轮读了 app.log 前 400 行后, 第二轮要求读 401-800 时,
// 模型可能忽略仍在上下文里的参数并拒绝继续读取.
//
// 措辞要分清两件事: **同样的参数**再来一遍没意义;
// 换个 offset 读别的部分, 恰恰是该做的.
func TestFoldNoticeDoesNotDiscourageReadingMore(t *testing.T) {
	w := foldWindow(t, 2048)
	for i := 0; i < 10; i++ {
		w.AppendStep("read_file", map[string]any{"path": "big.log"}, "")
		w.AppendResult(bigResult(3000), "", "")
	}
	w.AppendStep("read_file", map[string]any{"path": "big.log", "offset": 401}, "")
	w.AppendResult("最新的", "", "")

	var joined string
	msgs, _ := w.Messages()
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "换 offset") {
		t.Fatalf("没告诉它可以接着读别的部分:\n%.400s", joined)
	}
	// 通知要直接点出那次调用 —— 让模型"自己去旁边看"不够,
	// 否则会摸索一通然后读错文件.
	if !strings.Contains(joined, "你刚才执行了") {
		t.Fatal("没点出是哪次调用, 模型会以为自己不知道刚才在读什么")
	}
	// 仍然要拦住"一模一样再来一遍"
	if !strings.Contains(joined, "一模一样的参数") {
		t.Fatal("没拦住原样重来")
	}
}

// 折叠通知必须**指名道姓**说是哪一次调用.
//
// 只说"参数就在它旁边, 照着看就行"是在让模型自己找.
// 第一轮读了 bench/app.log 后, 折叠后的第二轮可能先 list_dir 摸索,
// 然后读成 site/big.log —— **文件都会搞混**.
// 已知信息必须直接提供, 不应让模型去旁边找.
func TestFoldNoticeNamesTheCall(t *testing.T) {
	w := foldWindow(t, 2048)
	for i := 0; i < 8; i++ {
		w.AppendStep("read_file", map[string]any{"path": "bench/app.log"}, "")
		w.AppendResult(bigResult(3000), "", "")
	}
	w.AppendStep("read_file", map[string]any{"path": "bench/app.log", "offset": 401}, "")
	w.AppendResult("最新的", "", "")

	var joined string
	msgs, trim := w.Messages()
	for _, m := range msgs {
		joined += m.Content + "\n"
	}
	if trim.Dropped == 0 {
		t.Fatal("该折的没折")
	}
	if !strings.Contains(joined, "你刚才执行了: read_file") {
		t.Fatalf("通知没说是哪次调用:\n%.400s", joined)
	}
	if !strings.Contains(joined, "bench/app.log") {
		t.Fatal("通知里没有路径, 模型会把文件搞混")
	}
}

// 没有对应动作时也要说得通, 不能拼出一句半截话
func TestFoldNoticeWithoutAction(t *testing.T) {
	w := foldWindow(t, 1024)
	for i := 0; i < 6; i++ {
		w.AppendResult(bigResult(2000), "", "") // 只有结果, 没有动作
	}
	w.AppendResult("最新", "", "")
	var joined string
	msgs, _ := w.Messages()
	for _, m := range msgs {
		joined += m.Content
	}
	if strings.Contains(joined, "执行了:  ") || strings.Contains(joined, "执行了: —") {
		t.Fatalf("拼出了半截话:\n%.200s", joined)
	}
}

// 预算比一次工具结果还小 = 干不了活, 必须说出来.
//
// 真机对照: 4000 字节预算下, 第一轮读了 bench/app.log, 第二轮先 list_dir
// 摸索一通然后读成 site/big.log —— 文件都搞混了.
// 同一个任务换成按模型窗口自动算的真实预算(256KB), 三次分片路径全对,
// 一次折叠都没触发. 那不是折叠逻辑的错, 是**预算配错了**.
//
// 静默接受的后果是: 看的人以为模型不行.
func TestStarvedBudgetIsReported(t *testing.T) {
	w := foldWindow(t, 4000) // 比 read_file 单次分片上限还小
	w.AppendUserTurn("读点东西")
	if _, tr := w.Messages(); !tr.Starved {
		t.Fatal("预算小到干不了活却不吭声")
	}
}

// 够用的预算不该报警 —— 误报会让人开始忽略这条
func TestWorkableBudgetNotReported(t *testing.T) {
	w := foldWindow(t, minWorkableBudget)
	w.AppendUserTurn("读点东西")
	if _, tr := w.Messages(); tr.Starved {
		t.Fatal("预算够用却报了警")
	}
}

// msgEqual 消息相等 —— 带上 tool_calls 一起比.
//
// 原来用 == 直接比结构体, 加了 ToolCalls 切片之后编译不过.
// 用 reflect.DeepEqual 而不是只比 Content: **前缀稳定要连
// tool_calls 的 id 一起稳定**, 只比 Content 会漏掉 id 变了这种情况,
// 而 id 一变缓存就全废.
func msgEqual(a, b abi.InferMessage) bool { return reflect.DeepEqual(a, b) }

// **失败结果不许被折掉.**
//
// 折叠原来对成功和失败一视同仁. 但两者价值完全不对称:
// 成功的结果是文件内容(几万字节, 需要时重读一次就有);
// 失败的原因只有几百字节, 而且**重做一遍只会再失败一次**.
//
// 折掉失败的后果是模型看不到自己上次为什么没成 —— 一个
// string_not_found 被折掉之后它会拿同一个 old_string 再试,
// 而这正是错误递进策略要防的事. 折叠把那份记忆抹掉 = 把策略架空.
func TestFailureResultsSurviveFolding(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "改代码", 4000)
	defer w.Release()

	// 先来一次失败, 再堆一堆成功的大结果把预算撑爆
	w.AppendStep("edit_file", map[string]any{
		"path": "a.go", "old_string": "func old()"}, "call_f")
	w.AppendResult("", "string_not_found: a.go 里找不到那段原文", "call_f")
	for i := 0; i < 6; i++ {
		id := "call_ok" + string(rune('0'+i))
		w.AppendStep("read_file", map[string]any{"path": "big.log"}, id)
		w.AppendResult(strings.Repeat("很长的内容", 500), "", id)
	}

	msgs, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("这个预算下应该折叠了 —— 测试没测到目标场景")
	}
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "string_not_found") {
		t.Fatal("失败原因被折掉了 —— 模型会拿同一个 old_string 再试一遍")
	}
}

// 反过来: 成功的大结果该折还是要折, 否则预算根本压不下来
func TestSuccessResultsStillFold(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "读文件", 4000)
	defer w.Release()
	for i := 0; i < 6; i++ {
		id := "c" + string(rune('0'+i))
		w.AppendStep("read_file", map[string]any{"path": "big.log"}, id)
		w.AppendResult(strings.Repeat("很长的内容", 500), "", id)
	}
	_, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("成功的大结果一条都没折 —— 预算压不下来")
	}
}

// **折叠要真的省内存, 不能只省请求.**
//
// 折叠原来只把结果从请求里拿掉, 页仍然被 Retain 着 —— 页表一页都删不掉.
// 一个跑几天的常驻对话请求是有界的、**内存是无界的**.
//
// 真机量到过: 活字节 3013 / 硬上限 2000 / 删除 0 / StillOver=true.
func TestFoldingActuallyFreesMemory(t *testing.T) {
	store := engine.NewPageStore()
	store.SetCapacity(engine.Capacity{SoftBytes: 4000, HardBytes: 8000})
	w := NewWindow(store, "干活", 6000)
	defer w.Release()

	for i := 0; i < 12; i++ {
		id := "c" + string(rune('a'+i))
		w.AppendStep("read_file", map[string]any{"path": "f" + string(rune('a'+i))}, id)
		// 内容各不相同 —— 一样的内容会被去重成一页, 那测的是去重不是回收
		w.AppendResult(strings.Repeat(string(rune('A'+i)), 2000), "", id)
	}
	if _, trim := w.Messages(); trim.Dropped == 0 {
		t.Fatal("这个预算下该折叠 —— 测试没测到目标场景")
	}
	// 折过的页引用必须归零 —— **那才是"能被回收"的定义**.
	//
	// 不要断言 Deleted > 0: 删除只在总量还超硬上限时才发生, 而降级
	// (无损, 进 swap) 往往已经把总量压下去了. 断言删除次数等于
	// 把"回收策略的实现细节"钉进测试, 那是错的钉法.
	for id := range w.foldedPages {
		if n := store.Refs(id); n != 0 {
			t.Fatalf("折过的页还有 %d 个引用 —— 页表永远删不掉它, 内存无界", n)
		}
	}
	rep, err := w.Reclaim()
	if err != nil {
		t.Fatalf("交还之后还压不下去: %v", err)
	}
	if rep.Demoted == 0 && rep.Deleted == 0 {
		t.Fatal("回收一页都没动 —— 折叠只省了请求, 没省内存")
	}
}

// **一次折了就永远折.**
//
// 折叠原来每轮重算. 预算一涨, 已经折过的页可能又"不折了" ——
// 那会改写前缀, 缓存整段作废, 而且完全静默.
func TestFoldingIsStickyAcrossBudgetChanges(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "干活", 6000)
	defer w.Release()
	for i := 0; i < 10; i++ {
		id := "c" + string(rune('a'+i))
		w.AppendStep("read_file", map[string]any{"path": "f" + string(rune('a'+i))}, id)
		w.AppendResult(strings.Repeat(string(rune('A'+i)), 2000), "", id)
	}
	first, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("没折叠, 测不到目标场景")
	}
	// 预算放大到装得下全部 —— 已经折过的**不许回来**
	w.maxTailBytes = 10_000_000
	again, _ := w.Messages()
	if len(again) != len(first) {
		t.Fatalf("预算变了之后消息条数变了: %d → %d", len(first), len(again))
	}
	for i := range first {
		if !msgEqual(first[i], again[i]) {
			t.Fatalf("第 %d 条变了 —— 折过的页又回来了, 前缀被改写, 缓存整段作废\n之前 %+v\n之后 %+v",
				i, first[i], again[i])
		}
	}
}

// 折叠通知在页交还之后仍然说得清"你刚才执行了什么" ——
// 那些信息在折的时候就记进账本了, 不再依赖页还在
func TestFoldNoticeSurvivesPageDrop(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "干活", 6000)
	defer w.Release()
	for i := 0; i < 10; i++ {
		id := "c" + string(rune('a'+i))
		w.AppendStep("read_file", map[string]any{"path": "bench/app.log"}, id)
		w.AppendResult(strings.Repeat(string(rune('A'+i)), 2000), "", id)
	}
	w.Messages()            // 第一次: 折 + 交还
	msgs, _ := w.Messages() // 第二次: 页已经没了
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "内容已省略") {
			found = true
			if !strings.Contains(m.Content, "read_file") {
				t.Fatalf("通知里没说清是哪次调用: %q", m.Content)
			}
		}
	}
	if !found {
		t.Fatal("页交还之后折叠通知消失了 —— 模型会以为那一步没有结果")
	}
	checkPairing(t, msgs)
}

// 这一轮的账要留在窗口上 —— 别为了看一眼再算一遍.
//
// `Messages()` **是有副作用的**: 它决定折哪些页, 并把折掉的页交还给页表.
// 原来 agent 循环里为了看一眼 Starved 就又调了一次, 那一次会照着第一次之后
// 的状态再折一轮, 而它的返回值(包括给模型的折叠通知)转手就被丢掉.
func TestLastTrimIsTheAccountOfTheRequestActuallySent(t *testing.T) {
	w := foldWindow(t, 4096)
	if got := w.LastTrim(); got.Dropped != 0 || got.DroppedBytes != 0 {
		t.Fatalf("还没算过账就有数: %+v", got)
	}
	for i := 0; i < 12; i++ {
		w.AppendStep("read_file", map[string]any{"path": "f" + string(rune('a'+i))}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	_, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("超限了却没折叠")
	}
	if got := w.LastTrim(); got != trim {
		t.Fatalf("窗口记的账跟真正发出去那次对不上: %+v vs %+v", got, trim)
	}
}

// 把 Messages() 当取值器再调一次, 不该再折一轮.
//
// 折叠是**幂等的**才对: 该折的上一次已经折完并交还了, 再问一次只是重新
// 组装同一批消息. 如果第二次还在折, 说明有东西在被反复回收 ——
// 那正是"为了看一眼 Starved 又调一次"会踩到的坑.
func TestAskingAgainDoesNotFoldAgain(t *testing.T) {
	w := foldWindow(t, 4096)
	for i := 0; i < 12; i++ {
		w.AppendStep("read_file", map[string]any{"path": "f" + string(rune('a'+i))}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	first, t1 := w.Messages()
	if t1.Dropped == 0 {
		t.Fatal("第一次就该折")
	}
	second, t2 := w.Messages()
	if t2.Dropped != 0 {
		t.Fatalf("再问一次又折了 %d 条 —— 折叠不是幂等的", t2.Dropped)
	}
	// 而且两次组装出来的东西必须一样, 否则"发出去的"和"检查的"不是一个
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("两次组装结果不同: %d 条 vs %d 条", len(first), len(second))
	}
}

// 折叠必须报出来.
//
// 折叠是 OS 在回收一种资源, 而它原来**完全静默**: 账在 Messages() 里算出来,
// 在调用点被 `_` 扔掉. 于是"这一轮为什么变笨了"没有任何一处能回答 ——
// 跟前缀缓存悄悄失效是同一类问题, 而那次的结论就是
// **查不出来的根本原因是我们是瞎的**.
func TestFoldIsReported(t *testing.T) {
	sys := newFakeSys() // Infer 会直接报错, 而折叠事件在推理之前就发了
	w := foldWindow(t, 4096)
	for i := 0; i < 12; i++ {
		w.AppendStep("read_file", map[string]any{"path": "f" + string(rune('a'+i))}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	m := &LLM{ABI: sys, Tools: NewToolSet(DefaultTools()), Window: w}
	_, _ = m.once("任务", nil)

	got := sys.last("context_fold")
	if got == nil {
		t.Fatal("折了却一个字都没说 —— 上下文回收是静默的, 变笨了查不出根")
	}
	// 光说"折了"没用: 折了多少、省了多少、预算多大, 三个数缺一不可 ——
	// 少了预算就没法判断是配小了还是活确实大
	for _, k := range []string{"dropped", "droppedBytes", "budgetBytes"} {
		if got[k] == nil {
			t.Fatalf("折叠事件缺 %s: %v", k, got)
		}
	}
}

// 没折就别说话. 每轮都报一句是纯噪音, 会把真事淹掉.
func TestNoFoldNoNoise(t *testing.T) {
	sys := newFakeSys()
	w := foldWindow(t, 1<<20)
	w.AppendStep("read_file", map[string]any{"path": "a"}, "")
	w.AppendResult("小结果", "", "")
	m := &LLM{ABI: sys, Tools: NewToolSet(DefaultTools()), Window: w}
	_, _ = m.once("任务", nil)
	if sys.saw("context_fold") {
		t.Fatalf("没折却报了一句: %v", sys.last("context_fold"))
	}
}
