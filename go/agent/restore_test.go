package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

func out(phase string, kv ...string) abi.Event {
	m := map[string]any{"phase": phase}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return abi.Event{Kind: abi.EvProcOutput, Payload: m}
}

func said(text string) abi.Event {
	return abi.Event{Kind: abi.EvInputRecv, Payload: map[string]any{"text": text}}
}

func restored(t *testing.T, evs []abi.Event) string {
	t.Helper()
	w := RestoreWindow(engine.NewPageStore(), evs, "兜底任务")
	msgs, _ := w.Messages()
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// 用户消息必须全保真 —— 它们构成对话的骨架
func TestUserTurnsRestored(t *testing.T) {
	got := restored(t, []abi.Event{
		out("start", "task", "建 notes.md"),
		said("再加一条"),
		said("最后一条改成别的"),
	})
	for _, want := range []string{"建 notes.md", "再加一条", "最后一条改成别的"} {
		if !strings.Contains(got, want) {
			t.Fatalf("用户说的 %q 没恢复出来:\n%s", want, got)
		}
	}
}

// agent 的回复和结论是**唯一保真留下来的思考产物** ——
// 工具结果原文没了, 丢掉结论等于把对话恢复成一串没有结果的动作.
func TestAssistantConclusionsRestored(t *testing.T) {
	got := restored(t, []abi.Event{
		out("step", "tool", "search"),
		out("done", "summary", "FATAL 在第 777 行"),
	})
	if !strings.Contains(got, "FATAL 在第 777 行") {
		t.Fatalf("结论丢了:\n%s", got)
	}
}

// 事件日志里的工具结果是截断的展示流, **不是上下文真相源**.
// 恢复时不许假装有原文 —— 那会造出一个"看着对但其实不对"的历史.
func TestToolResultsAreNotFaked(t *testing.T) {
	got := restored(t, []abi.Event{
		out("step", "tool", "read_file"),
		out("tool_ok", "tool", "read_file", "result", "被截到 200 字的展示文本"),
	})
	if strings.Contains(got, "被截到 200 字的展示文本") {
		t.Fatal("把展示流当成上下文原文塞回去了 —— 那是假的历史")
	}
	if !strings.Contains(got, "结果没有保留下来") {
		t.Fatalf("没说清结果丢了, 模型会以为那步没结果然后瞎猜:\n%s", got)
	}
}

// 动作后面必须跟一条结果占位 —— 一串没有结果的动作比明说"结果没了"更糊涂
func TestEveryActionHasAResultSlot(t *testing.T) {
	got := restored(t, []abi.Event{
		out("step", "tool", "search"),
		out("step", "tool", "read_file"),
	})
	if n := strings.Count(got, "结果没有保留下来"); n != 2 {
		t.Fatalf("两个动作只有 %d 条结果占位:\n%s", n, got)
	}
}

// 这次的新问题不许蒸发, 而且它要在**最后**.
//
// ── 判据换过一次 ──
//
// 早先钉的是"新问题必须是第一条(任务: 新问题)". 那条钉的形状恰恰是
// 供应商会拒的形状: 整个请求以 bot 的上一句回话结尾, 等于让模型接着
// 一条 assistant 说下去 —— 带 tools 时服务端会直接返回 400.
//
// 原来的顾虑仍然成立(新问题不能被旧任务顶掉), 但把它放在最后同样解决,
// 而且那本来就是它的位置: 它是最新的一轮.
func TestNewQuestionIsTheLastTurnAndOldTaskSurvives(t *testing.T) {
	w := RestoreWindow(engine.NewPageStore(),
		[]abi.Event{out("start", "task", "上次的任务"), said("嗯")}, "这次的新问题")
	msgs, _ := w.Messages()

	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "这次的新问题") {
		t.Fatalf("新问题不在最后一轮: role=%q %q", last.Role, last.Content)
	}
	var joined strings.Builder
	for _, m := range msgs {
		joined.WriteString(m.Content)
	}
	if !strings.Contains(joined.String(), "上次的任务") {
		t.Fatal("旧任务从历史里丢了")
	}
}

// **装回来的对话不许以 assistant 结尾.**
//
// 报错说的是"必须回传 reasoning_content",
// 而真实原因是请求以一条 assistant 收尾. 照着报错去补 id、补 reasoning
// 都修不好 —— 把真实报文拿去 API 上做对照实验才看清: 去掉全部工具调用
// 照样被拒, 砍掉最后那条 assistant 就过.
func TestRestoredWindowNeverEndsWithAssistant(t *testing.T) {
	for _, tail := range [][]abi.Event{
		{out("start", "task", "t"), said("我说完了")},                    // 以回话结尾
		{out("start", "task", "t"), out("step", "tool", "run")},      // 以动作结尾
		{out("start", "task", "t"), said("一"), said("二"), said("三")}, // 连着几句回话
	} {
		w := RestoreWindow(engine.NewPageStore(), tail, "接着答")
		msgs, _ := w.Messages()
		last := msgs[len(msgs)-1]
		if last.Role != "user" {
			t.Fatalf("装回来的对话以 %q 结尾 —— 带 tools 的请求会被整个拒掉", last.Role)
		}
		w.Release()
	}
}

// 只有 start 没有任何来回 = 空壳.
// 告诉用户"已恢复"然后它什么都不记得, 比直接开新的更糟.
func TestEmptyShellNotWorthResuming(t *testing.T) {
	if HasConversation([]abi.Event{out("start", "task", "x")}) {
		t.Fatal("空壳不该算可恢复的对话")
	}
	if !HasConversation([]abi.Event{said("你好")}) {
		t.Fatal("用户说过话就该算")
	}
	if !HasConversation([]abi.Event{out("done", "summary", "做完了")}) {
		t.Fatal("有结论就该算")
	}
}

// 恢复出来的历史同样只追加 —— 接着聊时前缀要稳定
func TestRestoredWindowStillAppendOnly(t *testing.T) {
	w := RestoreWindow(engine.NewPageStore(),
		[]abi.Event{out("start", "task", "t"), said("第一句")}, "t")
	first, _ := w.Messages()
	w.AppendUserTurn("第二句")
	second, _ := w.Messages()
	if len(second) <= len(first) {
		t.Fatal("追加之后没变长")
	}
	for i := range first {
		if !msgEqual(first[i], second[i]) {
			t.Fatalf("第 %d 条被改写了 —— 恢复后的前缀不稳定, 缓存会全废", i)
		}
	}
}

func atEv(ev abi.Event, at int64) abi.Event { ev.At = at; return ev }

// 清单要按最近活动排 —— 用户找的是"刚才那段"
func TestConversationsSortedByRecency(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {atEv(said("老的"), 100)},
		"p2": {atEv(said("最新的"), 900)},
		"p3": {atEv(said("中间的"), 500)},
	})
	if len(got) != 3 || got[0].Thread != "p2" || got[2].Thread != "p1" {
		t.Fatalf("排序不对: %+v", got)
	}
}

// 开场白就是标题 —— pid 用户认不出来, 开场白能认出来
func TestTitleComesFromFirstThingSaid(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {out("start", "task", "排查 big.log 的 FATAL"), said("再看看别的")},
	})
	if got[0].Title != "排查 big.log 的 FATAL" {
		t.Fatalf("标题不对: %q", got[0].Title)
	}
}

// 轮数要算上开场那一轮 —— 一句话的和聊了半小时的分量完全不同
func TestTurnsCounted(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {out("start", "task", "开场"), said("第二轮"), said("第三轮")},
	})
	if got[0].Turns != 3 {
		t.Fatalf("轮数 %d, 期望 3", got[0].Turns)
	}
}

// 空壳不进清单 —— 挑中它等于挑了个什么都不记得的对话
func TestEmptyShellsExcludedFromList(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {abi.Event{Kind: abi.EvProcState}},
		"p2": {said("真聊过")},
	})
	if len(got) != 1 || got[0].Thread != "p2" {
		t.Fatalf("空壳混进清单了: %+v", got)
	}
}

// 没有开场白也要有个能看的标题, 不能是空字符串
func TestTitleNeverEmpty(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {out("done", "summary", "做完了")},
	})
	if got[0].Title == "" {
		t.Fatal("标题是空的, 清单里会显示成一片空白")
	}
}

// 带 thread 标签的创建事件
func threadEv(pid abi.ProcessID, thread string, at int64) abi.Event {
	return abi.Event{PID: pid, At: at, Kind: abi.EvProcState,
		Payload: map[string]any{"state": "created",
			"labels": map[string]any{"thread": thread}}}
}

func pidEv(pid abi.ProcessID, ev abi.Event, at int64) abi.Event {
	ev.PID, ev.At = pid, at
	return ev
}

// 接续产生的新进程要**并进原来那段**, 不能另起一条.
//
// 接续话题 B 之后, 清单里不能冒出一段"新对话"
// (标题是我接续时问的那句话), 其实是 B 的续集.
// 再接几次, 清单全是碎片, 用户认不出哪个是正主.
func TestResumedProcessMergesIntoOriginalThread(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {pidEv("p1", out("start", "task", "排查 FATAL"), 100),
			pidEv("p1", said("再看看"), 200)},
		// p2 是接续 p1 产生的新进程
		"p2": {threadEv("p2", "p1", 300),
			pidEv("p2", out("start", "task", "编号是多少?"), 310)},
	})
	if len(got) != 1 {
		t.Fatalf("接续被算成了另一段对话, 清单碎成 %d 条: %+v", len(got), got)
	}
	if got[0].Thread != "p1" {
		t.Fatalf("并错了线头: %q", got[0].Thread)
	}
	if got[0].Title != "排查 FATAL" {
		t.Fatalf("标题该是原对话的开场白, 拿到 %q", got[0].Title)
	}
	// 接续时用的 pid 要是最后那个 —— 从它往后接
	if got[0].PID != "p2" {
		t.Fatalf("该从最后一个进程往后接, 拿到 %q", got[0].PID)
	}
}

// 轮数要跨进程累计 —— 否则接续之后显示"1 轮", 用户以为聊过的都没了
func TestTurnsAccumulateAcrossThread(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {pidEv("p1", out("start", "task", "开场"), 100),
			pidEv("p1", said("第二轮"), 150)},
		"p2": {threadEv("p2", "p1", 300),
			pidEv("p2", out("start", "task", "第三轮"), 310)},
	})
	if got[0].Turns != 3 {
		t.Fatalf("跨进程轮数没累计: %d", got[0].Turns)
	}
}

// 各自独立的对话不许被并到一起
func TestUnrelatedThreadsStaySeparate(t *testing.T) {
	got := Conversations(map[abi.ProcessID][]abi.Event{
		"p1": {pidEv("p1", said("话题一"), 100)},
		"p2": {pidEv("p2", said("话题二"), 200)},
	})
	if len(got) != 2 {
		t.Fatalf("不相干的对话被并成了 %d 条", len(got))
	}
}

// 装回来的动作必须带 id, 而且 reasoning 不许是空的.
//
// ── 这条是真机抓的 ──
//
// 原来传的是空 id(原始 id 从没进过账本). 后果不是"少了点上下文",
// 是**整轮直接死掉**:
//
//	供应商拒绝: The `reasoning_content` in the thinking mode
//	must be passed back to the API.
//	模型连续 3 次出错 → turn_failed
//
// 而报出来的错跟真实原因完全不是一回事 —— 它说缺 reasoning,
// 实际是 id 不对. 会话重启之后第一句话就撞上, 用户看到的是
// "它一句话都不回".
func TestRestoredStepsCarryIDAndReasoning(t *testing.T) {
	w := RestoreWindow(engine.NewPageStore(), []abi.Event{
		{Kind: abi.EvInputRecv, Payload: map[string]any{"text": "建个记事本"}},
		{Kind: abi.EvProcOutput, Payload: map[string]any{
			"phase": "step", "tool": "write_file",
			"args": map[string]any{"path": "notes.go"}}},
		{Kind: abi.EvProcOutput, Payload: map[string]any{
			"phase": "step", "tool": "run",
			"args": map[string]any{"cmd": "go test ./..."}}},
	}, "接着干")
	defer w.Release()

	msgs, _ := w.Messages()
	ids := map[string]bool{}
	calls := 0
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			calls++
			if c.ID == "" {
				t.Fatal("装回来的动作没有 tool_call id —— 供应商会拒掉整个请求")
			}
			if ids[c.ID] {
				t.Fatalf("两个动作用了同一个 id %q", c.ID)
			}
			ids[c.ID] = true
		}
		if len(m.ToolCalls) > 0 && strings.TrimSpace(m.Reasoning) == "" {
			t.Fatal("装回来的动作 reasoning 是空的 —— 思考模式下会被拒")
		}
	}
	if calls != 2 {
		t.Fatalf("装回了 %d 个动作, 应该是 2 个", calls)
	}
}

// 同一段历史装回来两次必须逐字节一样.
//
// id 用随机数的话, 每次重启前缀都变 —— 而这段历史是整个请求的开头,
// 它一变, 这段对话的缓存从此再也命不中.
func TestRestoreIsByteStable(t *testing.T) {
	evs := []abi.Event{
		{Kind: abi.EvInputRecv, Payload: map[string]any{"text": "建个记事本"}},
		{Kind: abi.EvProcOutput, Payload: map[string]any{
			"phase": "step", "tool": "run", "args": map[string]any{"cmd": "go test"}}},
	}
	dump := func() string {
		w := RestoreWindow(engine.NewPageStore(), evs, "接着干")
		defer w.Release()
		msgs, _ := w.Messages()
		out := ""
		for _, m := range msgs {
			out += m.Role + "|" + m.Content + "|" + m.Reasoning
			for _, c := range m.ToolCalls {
				out += "|" + c.ID + c.Name + c.Arguments
			}
			out += "\n"
		}
		return out
	}
	if dump() != dump() {
		t.Fatal("同一段历史两次装回来不一样 —— 这段对话的缓存从此永远命不中")
	}
}
