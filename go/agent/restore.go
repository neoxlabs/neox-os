package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// 从事件日志恢复一段对话.
//
// ── 恢复的是对话, 不是上下文 ──
//
// 这一点必须说死, 否则会造出一个"看着对但其实不对"的历史.
//
// 事件日志里的工具结果是 **trunc(out, 200)** —— 它是给人看的展示流,
// 不是上下文的真相源. 上下文的真相源是页表, 而页表随进程一起没了.
//
// 所以恢复出来的历史里:
//
//	输入原文         全保真   (input.recv)
//	agent 做过什么   全保真   (step: tool + args 原样)
//	agent 的回复/结论 全保真  (reply / done)
//	工具结果原文     **没有** —— 只留"做过这一步, 结果没了"
//
// 这跟上下文折叠的语义**完全一致**, 不是巧合: 两者都是
// "动作留着、结果丢掉". 所以提示词里已经写好的那段
// ("原样重做一遍没有意义…换个更窄的读法") 对恢复场景同样适用,
// 不需要再教模型一套新说法.
//
// 反过来说: 如果哪天真要完整恢复上下文, 那要落盘的是**页表**,
// 而不是把事件日志的截断长度调大 —— 后者只会让展示流变臃肿,
// 还是换不来保真.

// RestoreWindow 用事件日志重建一个 Window.
//
// task 是**这次**要干的事(新来的那句话), 不是日志里的旧任务.
//
// 这一点栽过一次: 早先我把 Window 的 task 设成日志里记的旧任务, 结果
// `Serve(first)` 的第一轮是靠 task 字段把话传给模型的, 新问题就此蒸发 ——
// 接续后若询问"编号和行号是多少", 模型可能复读上一段历史里的
// "嗨！有什么可以帮你的".
//
// 根治不是在 Serve 里加个特例, 而是消掉特例本身:
// **旧任务本来就是一轮用户输入, 那就按用户输入存进历史.**
// 于是 task 字段的含义在"新对话"和"接续对话"里完全一致, 没有分叉的余地.
// restoredNoResult 从记录恢复时, 动作后面那条结果占位.
//
// 动作后面必须跟一条结果, 否则模型会看到一串没有结果的动作 ——
// 那比明说"结果没了"更糊涂.
// restoredReasoning 装回来的那一步, 推理原文的位置上放什么.
//
// **不能是空的** —— 空的话供应商会拒掉整个请求(见上面那段).
// 既然非放不可, 就放一句对模型有用的话: 告诉它这一步是历史,
// 不是它刚刚想的.
const restoredReasoning = "(这一步是从记录里装回来的, 当时的推理原文没有保留)"

const restoredNoResult = "这一步的结果没有保留下来(对话是从记录里恢复的) —— " +
	"这一步你确实做过, 只是那份原文不在了。"

// strList 事件里的字符串数组 —— 过了一趟 JSON 之后是 []any
func strList(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// argsAt 批次里第 i 个调用的参数. 取不到就给空 ——
// 参数丢了动作还在, 比整个动作消失强得多
func argsAt(v any, i int) map[string]any {
	raw, _ := v.([]any)
	if i >= len(raw) {
		return nil
	}
	m, _ := raw[i].(map[string]any)
	return m
}

// RestoreWindow 把一段旧对话装回一个新窗口, 末尾接上这次要问的话.
//
// ── 这一轮的话必须在**最后**, 不能只在开头 ──
//
// 早先的排法是: Window 的 task 设成这次的新问题(于是 Messages() 的第一条
// 是「任务: 新问题」), 旧任务和整段历史接在后面. 结果整个请求
// **以 bot 的上一句回话结尾** —— 等于让模型接着一条 assistant 说下去.
//
// 这条路径会直接失败: 带 tools 的请求会被供应商拒绝, 而它报的是
//
//	The `reasoning_content` in the thinking mode must be passed back to the API.
//
// **跟真实原因完全不是一回事**. 我照着那句话查了两轮(给装回来的动作补 id、
// 补 reasoning), 都没修好 —— 直到把真实报文拿去 API 上做对照实验才看清:
// 去掉全部工具调用照样被拒, 而砍掉最后那条 assistant 就过.
//
// 所以现在: 开头那句「任务: …」用**旧任务**(它是这段对话的由来),
// 这次的新问题作为最后一轮用户输入接在末尾 —— 那也正是它本来的位置.
func RestoreWindow(store *engine.PageStore, events []abi.Event, task string) *Window {
	// 开头那句用旧任务; 没有旧任务(极少见)就退回用这次的
	head := task
	for _, ev := range events {
		if m, ok := payload(ev); ok && str(m, "phase") == "start" {
			if t := str(m, "task"); t != "" {
				head = t
			}
			break
		}
	}
	w := NewWindow(store, head, 0)
	if _, sum := lastCompacted(events); sum != "" {
		w.AppendUserTurn(compactNote + sum)
	}
	// pending 上一条动作还欠一个结果占位.
	//
	// 之所以要等一下: 紧跟着的那条事件可能是"用户拒了这次操作",
	// 那才是这一步真正的结果 —— 写成"结果没保留下来"就把用户的决定抹掉了.
	// ── 装回来的动作必须带 id 和 reasoning ──
	//
	// 供应商靠 tool_call id 认"这条 assistant 消息是不是我自己生成的".
	// 而**原始 id 没有进账本**(step 事件只记了 tool/args/thought), 装回来时
	// 我们手里根本没有它 —— 于是原来传的是空 id.
	//
// 后果不是"少了点上下文", 而是**整轮直接死掉**:
	//
	//	供应商拒绝: The `reasoning_content` in the thinking mode
	//	must be passed back to the API.
	//	模型连续 3 次出错 → turn_failed
	//
	// 报出来的错跟真实原因完全不是一回事(它说缺 reasoning, 实际是 id 不对),
	// 所以这条已经在 AppendStep 的注释里精确验过一次:
	//
	//	用供应商给的 id            → 通过
	//	换成我们编的 call_0        → 拒绝
	//	我们编的 id + 带 reasoning → 通过
	//
	// 装回来的时候第一条走不通(id 丢了), 所以走第三条: 编一个稳定的 id,
	// 并且把"这是装回来的"写进 reasoning —— 它同时是给模型看的说明.
	//
	// **id 要稳**: 用递增序号而不是随机数, 否则同一段历史每次装回来
	// 都是不同的字节, 前缀缓存整段作废.
	restoredN := 0
	restoredCall := func() string {
		restoredN++
		return fmt.Sprintf("restored_%d", restoredN)
	}

	// ── 压紧过的那一截不用再装回来 ──
	//
	//	不认这条的话, 压紧只活在内存里: 重启一次 historyOf 把原文全量装回
	//	(那个 bot 从开机到现在的每一轮), 于是压了等于没压 —— 而线上每次
	//	发版都是一次重启.
	//
	//	**认最后一条**: 中间压过好几次的话, 只有最后那份摘要是当下的.
	events = events[compactedAt(events)+1:]

	pending := false
	flushPending := func() {
		if pending {
			w.AppendResult("", restoredNoResult, "")
			pending = false
		}
	}
	for _, ev := range events {
		switch ev.Kind {
		case abi.EvInputRecv:
			flushPending()
			if m, ok := payload(ev); ok {
				if t := str(m, "text"); t != "" {
					w.AppendUserTurn(t)
				}
			}
		case abi.EvProcOutput:
			m, ok := payload(ev)
			if !ok {
				continue
			}
			switch str(m, "phase") {
			case "step":
				tool := str(m, "tool")
				if tool == "" {
					continue
				}
				// 上一步的占位先补上 —— 它没有被拒
				flushPending()
				args, _ := m["args"].(map[string]any)
				w.AppendBatch([]PageCall{{ID: restoredCall(), Tool: tool, Args: args}},
					restoredReasoning)
				// 动作后面必须跟一条结果占位, 否则模型会看到
				// 一串没有结果的动作 —— 那比明说"结果没了"更糊涂.
				// **但先别写**: 下一条事件可能是"用户拒了这次操作",
				// 那才是这一步真正的结果.
				pending = true
			// **"interrupt" 不在这儿恢复 —— 它已经有一份了.**
			//
			// 我一开始以为用户中途插的话恢复时丢了, 还写了用例"证明"它 ——
			// 而那个用例构造的事件序列**真实系统永远不会产生**: 插话总是先经
			// Send 落成 EvInputRecv(上面已经恢复), agent 中途取到它时再记一条
			// interrupt. 两条记的是同一句话.
			//
			// 照我原来的改法, 那句话会在恢复出来的历史里**出现两次** ——
			// 模型看到用户把同一件事说了两遍. 查了才发现, 不是靠推断.
			case "denied":
				// **用户拒过的事要记着**.
				//
				// 不记的话, 回来之后它会为同一件事再申请一次 ——
				// 而每一次申请都是一次打扰. 用户已经说过不行了.
				//
				// 这条正是上一步的**真实结果**, 所以占在那个位置上,
				// 而不是另起一条无主的记录.
				pending = false
				w.AppendResult("", "这一步被用户拒了(越界: "+str(m, "scope")+
					")。**别再为同一件事申请一次** —— 他已经说过不行了, "+
					"换一条不越界的路, 或者告诉他你只能做到哪儿。", "")
			case "batch":
				flushPending()
				// **并发批次也要恢复**.
				//
				// 这里原来只认 "step"(单工具那条路), 而 agent 一次调多个
				// 互不依赖的工具时发的是 "batch" —— 那条事件里 tools 和 args
				// 都带着, 只是没人读.
				//
				// 后果落在"隔一段时间回来继续"上: 用批次做过的活全部消失,
				// 它回来会以为自己没做过 —— 要么重做一遍(白花钱),
				// 要么把没做的说成做了.
				//
				// 一个动作配一条结果占位, 跟单工具那条一样: 没有结果的动作
				// 会留下没有归属的调用, 而那是供应商拒掉整个请求的原因.
				for i, tool := range strList(m["tools"]) {
					if tool == "" {
						continue
					}
					w.AppendBatch([]PageCall{{ID: restoredCall(), Tool: tool,
						Args: argsAt(m["args"], i)}}, restoredReasoning)
					w.AppendResult("", restoredNoResult, "")
				}
			case "reply", "done":
				flushPending()
				text := str(m, "text")
				if text == "" {
					text = str(m, "summary")
				}
				if text != "" {
					w.AppendAssistantSaid(text)
				}
			}
		}
	}
	flushPending()
	// **这次要问的话放在最后**. 不放的话请求以 assistant 结尾,
	// 供应商会拒掉整个请求(见上面那段).
	if strings.TrimSpace(task) != "" {
		w.AppendUserTurn(task)
	}
	return w
}

// lastCompacted 最后一次压紧在第几条、摘要是什么. 没压过就是 (-1, "").
func lastCompacted(events []abi.Event) (int, string) {
	at, sum := -1, ""
	for i, ev := range events {
		if ev.Kind != abi.EvProcOutput {
			continue
		}
		m, ok := payload(ev)
		if !ok || str(m, "phase") != "compacted" {
			continue
		}
		if s := str(m, "summary"); s != "" {
			at, sum = i, s
		}
	}
	return at, sum
}

// compactedAt 同上, 只要位置 —— 从它后面一条开始装
func compactedAt(events []abi.Event) int {
	at, _ := lastCompacted(events)
	return at
}

// HasConversation 这段日志里有没有值得恢复的东西.
//
// 只有 start 没有任何来回的, 恢复出来是个空壳, 不如不恢复 ——
// 给用户看"已恢复"然后它什么都不记得, 比直接开新的更糟.
func HasConversation(events []abi.Event) bool {
	for _, ev := range events {
		if ev.Kind == abi.EvInputRecv {
			return true
		}
		if m, ok := payload(ev); ok {
			switch str(m, "phase") {
			case "reply", "done", "step":
				return true
			}
		}
	}
	return false
}

func payload(ev abi.Event) (map[string]any, bool) {
	m, ok := ev.Payload.(map[string]any)
	return m, ok
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// ── 对话清单 ────────────────────────────────────────────────

// Conversation 一段对话的摘要, 给人挑用的.
//
// **pid 不能当门面**: 用户认不出 p7 是哪一段.
// 能认出来的是开场白 —— 那是这段对话的天然标题, 不用另存一份.
type Conversation struct {
	// Thread 对话身份. **跟进程身份不是一回事** —— 每次接续都是一个
	// 新进程, 但它们是同一段对话. 混成一个的症状是清单里全是碎片.
	Thread string
	// PID 这段对话最后一个进程. 接续时从它往后接
	PID abi.ProcessID
	// Title 开场白. 这段对话的第一句输入
	Title string
	// Turns 对话包含的用户回合数 —— 一句话和聊了半小时的, 分量完全不同
	Turns int
	// LastAt 最后一次活动
	LastAt int64
	// Unfinished 最后一轮**开了头但没有收尾** —— 进程被杀、机器重启、
	// 断电. 判据是事件日志里有 start 没有 reply/done, 不是猜的.
	Unfinished bool
	// Pending 那一轮用户交代的原话. **这才是重启之后最值钱的一句**:
	// 磁盘上的半成品能反推出"改了什么", 反推不出"他要什么".
	Pending string
	// PendingSteps 中断时干到第几步. 用来说明这活是刚起头还是快完了
	PendingSteps int
}

// Conversations 账本里所有值得接续的对话, 最近的在前.
//
// 按 **thread** 归并: 接续产生的新进程要并进原来那段, 而不是另起一条.
func Conversations(byPID map[abi.ProcessID][]abi.Event) []Conversation {
	// 先把每个进程归到它的对话上
	merged := map[string][]abi.Event{}
	for pid, evs := range byPID {
		th := threadOf(evs)
		if th == "" {
			th = string(pid) // 没有线头标签 = 它自己就是线头
		}
		merged[th] = append(merged[th], evs...)
	}

	var out []Conversation
	for thread, evs := range merged {
		if !HasConversation(evs) {
			continue // 空壳不进清单 —— 挑中它等于挑了个什么都不记得的
		}
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].At < evs[j].At })
		c := Conversation{Thread: thread, PID: evs[len(evs)-1].PID,
			LastAt: evs[len(evs)-1].At}
		for _, ev := range evs {
			switch ev.Kind {
			case abi.EvInputRecv:
				c.Turns++
				if c.Title == "" {
					c.Title = str(mustPayload(ev), "text")
				}
			case abi.EvProcOutput:
				m, ok := payload(ev)
				if !ok {
					continue
				}
				switch str(m, "phase") {
				case "start":
					if c.Title == "" {
						c.Title = str(m, "task")
					}
					c.Turns++
					// 开了一轮. 在收到收尾之前, 它就是"没干完"的那一轮.
					c.Unfinished, c.Pending, c.PendingSteps = true, str(m, "task"), 0
				case "step", "batch", "tool_ok":
					if c.Unfinished {
						c.PendingSteps++
					}
				case "reply", "done":
					// 收尾了. **只认这两个**: 它们是 agent 真正给出答复的
					// 唯二出口 (撞边界时的 finalSummary 也从这里出去).
					c.Unfinished, c.Pending, c.PendingSteps = false, "", 0
				}
			}
		}
		if c.Title == "" {
			c.Title = "(没有开场白)"
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	return out
}

func mustPayload(ev abi.Event) map[string]any {
	m, _ := payload(ev)
	return m
}

// threadOf 这个进程属于哪段对话. 空 = 它自己就是线头.
//
// 跟 osinit.ThreadOf 是同一套判断. 两处都要读同一个标签,
// 不一致会让"清单归并"和"接续时取哪些事件"对不上 ——
// 那种错的症状是清单显示一段, 接进去只有半段.
func threadOf(evs []abi.Event) string {
	for _, ev := range evs {
		if ev.Kind != abi.EvProcState {
			continue
		}
		m, ok := ev.Payload.(map[string]any)
		if !ok {
			continue
		}
		switch labels := m["labels"].(type) {
		case map[string]any:
			if t, ok := labels["thread"].(string); ok {
				return t
			}
		case map[string]string:
			return labels["thread"]
		}
	}
	return ""
}
