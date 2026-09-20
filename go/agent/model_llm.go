package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// LLM 真模型驱动的"下一步决定者".
//
// 它**不自己连供应商** —— 走 ABI 的 infer, 由 OS 去调.
// 于是这个进程既拿不到 key, 也不需要出网能力: netns 里一张网卡都没有.
//
// 输出走结构化 JSON 而不是让模型自由发挥:
// 自由文本要靠正则去抠工具调用, 那是"伪装成模型能力问题"的解析 bug 的温床.
type LLM struct {
	ABI Syscalls
	// Tools 工具表 —— 提示词由它生成, 加工具不用改两处
	Tools *ToolSet
	// Budgeter 有它就按模型窗口自动定上下文预算
	Budgeter *Budgeter
	// Persona 这个 bot 是谁、负责什么. 空 = 用通用身份.
	//
	// 跟 Writable 一样是**环境事实**: 进程启动时定死, 逐字节不变,
	// 不破坏前缀缓存.
	Persona string
	// Enforced 这条边界是不是**真的有人挡**.
	//
	//	confined 那条路是 landlock/netns/cgroup 挡的, 是真的;
	//	dev(in-proc)那条路**什么都没有** —— 一条
	//	`echo x > /tmp/y` 写出去了. 说了做不到的话, 后果不是少一层保护,
	//	是用户以为有保护而其实没有.
	Enforced bool
	// Branch 它干活的那条 git 分支. 空 = 这个项目没走分支隔离.
	//
	//	**跟 Writable 一样是环境事实**: 进程启动时定死, 同一段对话里不变,
	//	所以进系统段那一层不会打断前缀缓存.
	Branch string
	// NoProject 没有项目目录 —— 生活助理那种. 提示词里就不讲 PLAN.md/CHARTER.md
	NoProject bool
	// ModelName 底下跑的是哪个模型. **不是秘密** —— OS 一直知道,
	// 而它被迫回答"不知道"看起来像在藏着掖着. 空 = 这台机器没配,
	// 那就照实说不知道.
	//
	// 跟 Writable 一样是环境事实: 换供应商才变, 而那是几个月一次的事
	ModelName string
	// streamSeq 这一轮的流水号 —— 见 nextStream
	streamSeq int
	// streamed 这一轮真的吐过增量吗.
	//
	//	**最终那条要带上它** —— 渲染端据此认出"这两条是同一句话",
	//	而不是靠猜. 猜的结果是同一句话在屏幕上出现两遍
	streamed bool
	// lastStream 这一轮的流水号
	lastStream string
	// Writable 可写范围. 明说边界比让它撞墙强 ——
	// 撞墙要多花一次往返, 而且会触发一次本来不必要的人工审批
	Writable string
	// Recent 这台机器上还有哪些对话. 空 = 不说这件事.
	//
	// 跟 Writable 一样是**环境事实**: 进程启动时定死一次, 同一段对话里
	// 逐字节不变, 所以不破坏前缀缓存. 见 layerRecent.
	Recent string
	// Corrections 用户明确改过的方向. 空 = 不说这件事.
	//
	// 跟 Recent 一样是环境事实: 进程启动时定死, 不破坏前缀缓存.
	// 流水里没有"这是对的还是错的", 所以必须单独喂.
	Corrections string
	// Known 他让你记住的事 —— 见 osinit/notes.go 和 layerKnown.
	//
	//	跟 Corrections 一样是环境事实: 进程启动时定死一次.
	//	**当轮说的话不靠它** —— 那些本来就在窗口里; 它管的是跨进程
	//	的那一半("我上个月跟你说过我老婆生日")
	Known string
	// Window 对话历史. 前缀稳定的唯一来源
	Window *Window
	// MaxTokens 单次回复上限
	MaxTokens int
	// LastUsage 上一次调用的用量 —— 缓存命中率靠它量
	lastUsage abi.InferResult

	prompt string
	// prevFP 上一轮请求的前缀指纹 —— 前缀漂移是**静默**的,
	// 不比对就永远发现不了（命中率可能从 99% 降到 15%）
	prevFP cacheFingerprint
}

// systemPrompt 缓存生成结果 —— 它必须字节稳定, 每次重算也没意义
func (m *LLM) systemPrompt() string {
	if m.prompt == "" {
		w := m.Writable
		if w == "" {
			w = "工作目录"
		}
		m.prompt = buildSystemPromptKnowing(m.Tools, m.Persona, w, m.Branch,
			m.Recent, m.Corrections, m.ModelName, m.Known, m.Enforced, !m.NoProject)
	}
	return m.prompt
}

func (m *LLM) Name() string { return "llm" }

// Streamed 这一轮吐过增量没有, 以及那条流叫什么.
//
//	**给最终那条 reply 用**: 带上流水号, 渲染端才认得出"屏幕上那条
//	正在长的消息, 就是这一句" —— 不带的话它只能猜, 而猜错的结果是
//	同一句话出现两遍.
func (m *LLM) Streamed() (string, bool) { return m.lastStream, m.streamed }

// Streamer 供应商支持边生成边给的时候, ABI 会实现它.
//
//	**可选**: 不支持就退回一次性返回, 而不是自己把整段切片假装成流 ——
//	假流式骗不过人, 它的节奏是均匀的, 真流式不是.
type Streamer interface {
	InferStream(p abi.InferParams, onDelta func(engine.StreamDelta)) (abi.InferResult, error)
	// EmitLive 只给此刻正看着的人, **不进账本** —— 见 abi.EvProcDelta.
	//
	//	增量必须走它: 走普通 Emit 的话, 一次回复往账本里塞上百条,
	//	而它们加起来跟最后那条 phase:reply 一模一样
	EmitLive(payload any)
}

// nextStream 这一轮的流水号 —— 渲染端靠它把增量拼成同一条消息.
//
//	**必须每轮一个**: 共用一个的话, 第二次回答会接在第一次那条后面 ——
//	渲染端看到的是同一个 id, 它只能认为那是同一段话还在长.
func (m *LLM) nextStream() string {
	m.streamSeq++
	return fmt.Sprintf("r%d", m.streamSeq)
}

// infer 一次推理 —— 供应商给得了增量就边收边吐.
func (m *LLM) infer(stream string, p abi.InferParams) (abi.InferResult, error) {
	sr, ok := m.ABI.(Streamer)
	if !ok || m.ABI == nil {
		return m.ABI.Infer(p)
	}
	sent := false
	// 增量也要拆信封 —— 见 livewrap.go. 不拆的话最后那条替换上来之前,
	// 屏幕上(和语音播报里)是一个字一个字长出来的 {"said":"…
	lw := newLiveUnwrap(func(text string) {
		sent = true
		sr.EmitLive(map[string]any{
			"stream": stream, "channel": "say", "text": text})
	})
	res, err := sr.InferStream(p, func(d engine.StreamDelta) {
		if d.Text == "" {
			return
		}
		lw.Write(d.Text)
	})
	lw.Close()
	m.streamed = sent
	return res, err
}

// systemPrompt 系统段.
//
// **必须字节稳定** —— 它是每次请求的前缀, 变一个字节所有缓存全废.
// 所以这里是个常量, 不拼时间、不拼随机 id、不拼机器名.

// emptyRetries 空回复时原样重试几次.
//
// 空回复 (finish=stop 但 content 为空) 是**供应商侧的偶发行为**, 不是
// 模型"想错了" —— 它压根没输出. 把它当错误喂回去让模型"重新输出一步",
// 等于用一整轮 (还带着变长的历史) 去处理一个重发就能解决的问题.
//
// V4 上这个现象大约每 5-8 步出现一次，不重试的话每次都白烧一轮.
const emptyRetries = 2

func (m *LLM) Next(task string, history []Observation) (Step, error) {
	var lastErr error
	for attempt := 0; attempt <= emptyRetries; attempt++ {
		s, err := m.once(task, history)
		if err == nil {
			return s, nil
		}
		lastErr = err
		if !isEmptyReply(err) {
			return Step{}, err // 真错误 (截断/格式), 交给上层
		}
		// 空回复: 原样重发. 不改请求 —— 改了就破坏前缀缓存
	}
	return Step{}, lastErr
}

var errEmptyReply = errors.New("模型返回了空回复")

func isEmptyReply(err error) bool { return errors.Is(err, errEmptyReply) }

func (m *LLM) once(task string, history []Observation) (Step, error) {
	// 历史由 Window 管 —— 它保证 messages 只增不改,
	// 于是这一轮的前缀跟上一轮逐字节相同, 缓存才可能命中.
	msgs, trim := m.Window.Messages()
	// 上下文回收要看得见.
	//
	// 折叠是 OS 在回收一种资源, 而它原来**完全静默**: 账(折了几条、省了多少
	// 字节)在这儿算出来, 转手就被 `_` 扔掉. 于是"这一轮为什么变笨了"
	// 没有任何一处能回答 —— 跟当初前缀缓存悄悄失效是同一类问题,
	// 而那一次的结论就是: **查不出来的根本原因是我们是瞎的.**
	//
	// 只在真折了的时候说 —— 没折还报一句是纯噪音, 会把真事淹掉.
	if trim.Dropped > 0 && m.ABI != nil {
		m.ABI.Emit(map[string]any{"phase": "context_fold",
			"dropped": trim.Dropped, "droppedBytes": trim.DroppedBytes,
			"budgetBytes": m.Window.BudgetBytes(),
			"msg": fmt.Sprintf("上下文超预算, 折掉 %d 条旧的工具结果(省 %d 字节)",
				trim.Dropped, trim.DroppedBytes)})
	}

	sys := m.systemPrompt()
	tools := ToolDefs(m.Tools)
	sent := requestBytes(sys, tools, msgs)
	// **仪表装在请求真正发出去的这一处.**
	// 装在别处 = 客户端那个从没生效过的 sanitizeToolPairs.
	if d := m.prevFP.compare(fingerprintOf(sys, tools, msgs)); d != nil {
		if d.FirstDivergentIdx >= 0 && d.FirstDivergentIdx < len(msgs) {
			d.DivergentRole = msgs[d.FirstDivergentIdx].Role
		}
		pl := d.payload()
		pl["why"] = d.why()
		m.ABI.Emit(pl)
	}
	m.prevFP = fingerprintOf(sys, tools, msgs)

	// ── 边生成边吐 ──
	//
	//	**不流式的代价全在人这一侧**: 一句"现在几点"要等整段生成完才
	//	一次性蹦出来, 而模型那边其实第一个字早就出来了. 用户看到的是
	//	一个卡住的界面, 而它其实在正常工作.
	//
	//	带工具的 bot 原来走的都是这条非流式的路(不带工具的那条早就流式了),
	//	而这台机器上**六个 bot 全都带工具** —— 也就是说流式从来没生效过.
	//
	//	增量走一个单独的通道(say.delta): 老客户端不认它, 照旧只看
	//	phase:reply 那条最终结果, 一个字都不会重复; 认它的客户端边收边画.
	stream := m.nextStream()
	m.lastStream, m.streamed = stream, false
	res, err := m.infer(stream, abi.InferParams{
		System: sys, Messages: msgs,
		// 额度要给足: 推理型模型的 reasoning **也算 completion tokens**,
		// 给少了会出现"回复完全是空的"—— 推理还没写完就被截断了.
		// 写整个文件的那一步最费额度: 推理 + 完整文件内容都算在里面.
		// 给到 16k —— 截断的代价是整整一轮白跑, 比多给点额度贵得多.
		MaxTokens: orDefault(m.MaxTokens, 16000),
		// 原生工具协议 —— 结构合法性由供应商保证, 不靠模型自觉.
		// 不再设 JSONOut: 两套结构化机制叠在一起各家行为不一.
		Tools: tools,
	})
	if err != nil {
		return Step{}, err
	}
	m.lastUsage = res
	// 换算系数按实际用量确定，不猜 —— 中文和代码差一倍多，拍一个数两头都错
	if m.Budgeter != nil {
		m.Budgeter.Observe(sent, res.PromptTokens)
	}

	content := strings.TrimSpace(res.Content)

	// **先看 finish_reason, 再看内容**.
	//
	// 被截断的输出既可能是空的, 也可能是半截 JSON —— 两种都不是"格式错误".
	// 早先只在空回复时判断截断, 于是写大文件时被截断的半截 JSON
	// 被报成"输出不是合法 json", 查错方向直接跑偏.
	// **同一个诊断错误犯了两次, 所以这次按 finish_reason 统一判.**
	// **有话就把话交出去**: 原来一律整步作废 —— 它说了半屏, 用户一个字
	// 都没看到, 而重来一次多半又是同样的长度. 带着工具调用的那种不同:
	// 调用本身可能是残的, 那条还是作废
	if res.FinishReason == "length" {
		if content != "" && len(res.ToolCalls) == 0 {
			return Step{Reply: unwrapEnvelope(content)}, nil
		}
		return Step{}, truncatedErr(res)
	}
	// ── 原生工具调用 ──
	//
	// 走这条路时结构由供应商保证, 我们**不再解析自由文本**.
	// 模型可能一边说话一边动手 (content 和 tool_calls 同时有),
	// 那时 content 是它的想法, 不是回复 —— 放进 Thought.
	if len(res.ToolCalls) > 0 {
		return stepFromCalls(res)
	}

	if content == "" {
		return Step{}, fmt.Errorf("%w (finish=%s, 推理 %d tokens)",
			errEmptyReply, res.FinishReason, res.ReasoningTokens)
	}

	// 没有工具调用 = 它在说话.
	//
	// **不再按 JSON 解析**: 那是自造协议时代的做法, 而现在
	// "要不要动手"由 tool_calls 的有无表达, 不由文本内容表达.
	// 继续解析的话, 一句正常的中文回复会被当成"格式错误"打回去.
	// **把工具调用写成文字 = 什么都没发生**, 这件事必须当场吵.
	//
	// 静默收下的话: 用户看到一坨 JSON, 而它以为自己动过手了 —— 那次
	// recruit / 那次写文件根本没发生, 而账本里没有任何地方说得出来.
	//
	// 一律**不去把那段文字解析成调用**: 猜出来的工具调用比明确失败危险
	// 得多(parseStep 那段注释里已经写死了这条). 这里只负责让它自己重来.
	if why := textToolCall(content); why != "" {
		return Step{}, fmt.Errorf("%s", why)
	}
	return Step{Reply: unwrapEnvelope(content)}, nil
}

// UnwrapForAudit 给运维/测试量这条路用.
func UnwrapForAudit(s string) string { return unwrapEnvelope(s) }

// unwrapEnvelope 模型偶尔会把一句普通回答包在结构化信封里.
//
//	模型可能返回: {"said":"还是没给出版本号——他第二次确认了…"}
//	原样交上去, 用户在界面上看到的就是一坨带转义的 JSON.
//
//	**只拆一层, 而且只拆"单键字符串"这一种**: 多个字段说明它真的
//	想表达结构(那时候原样保留才对), 拆错比不拆糟 —— 会把内容吞掉.
//	**信封后面还能跟着正文**. 真机截图里抓到的原文:
//
//	    {"said":"已把这句话原样转给 OA助手：「…」"}
//
//	    他说会再转给屋里另一个人去建 ping.txt。
//
//	原来的判据是"整段以 } 结尾" —— 后面多了一段话, 这条就整个失效,
//	于是那串 JSON 原样摆在用户眼前, 而下面那句话看着像是另一个人说的.
//	改用 json.Decoder 只解**开头那一个值**, 剩下的原样接回去.
//
// ── 一层不够, 空的也要拆, 没收尾的也要拆 ──
//
//	2026-09-11 线上账本里还漏着三种, 全是"只拆一层、只拆有内容的、只拆
//	收了尾的"那几道保守闸放过去的:
//
//	  {"said":""}                     巡检醒来决定不说话 —— 用户看到一串 JSON(11 次)
//	  {"said":"{\"said\":\"…\"}"}     包了两层, 拆一层剩一层
//	  {"said":"行，不扯了。…            输出被截断, 没有收尾的 "}
//
//	保守的理由是"别把正常回答切坏". 但**以 {"said":" 开头的不可能是一句
//	正常的人话** —— 这个起手式本身就是信封. 所以: 最多拆三层; 空信封 =
//	这一轮不说话; 没收尾的照拆, 只要看不出后面还有别的字段.
func unwrapEnvelope(content string) string {
	out := content
	for i := 0; i < 3; i++ {
		next, ok := unwrapOnce(out)
		if !ok {
			break
		}
		out = next
	}
	return out
}

func unwrapOnce(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		return content, false
	}
	var fields map[string]any
	dec := json.NewDecoder(strings.NewReader(trimmed))
	if err := dec.Decode(&fields); err == nil {
		// 后面跟着的正文原样留住 —— 拆信封不是删内容
		rest := strings.TrimSpace(trimmed[dec.InputOffset():])
		said, ok := envelopeText(fields)
		if !ok {
			return content, false
		}
		switch {
		case rest == "":
			return said, true
		case strings.TrimSpace(said) == "":
			return rest, true
		}
		return said + "\n\n" + rest, true
	}
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		// **只在解不开时才救**. 解得开但字段不止一个是另一回事 ——
		// 那种信封拆了会吞掉别的字段, TestUnwrapEnvelope 钉着这条.
		if said, ok := salvageEnvelope(trimmed); ok {
			return said, true
		}
	}
	return content, false
}

// envelopeText 单键信封里的那句话. ok=false = 这不是信封, 别拆.
//
//	**只认"单键字符串"这一种**: 多个字段说明它真的想表达结构,
//	拆错比不拆糟 —— 会把别的字段吞掉. 空字符串**也是**信封:
//	那是它决定这一轮不说话.
func envelopeText(fields map[string]any) (string, bool) {
	if len(fields) != 1 {
		return "", false
	}
	for key, value := range fields {
		switch key {
		case "said", "reply", "text", "answer", "message", "content":
			if said, ok := value.(string); ok {
				return strings.TrimSpace(said), true
			}
		}
	}
	return "", false
}

// salvageEnvelope 模型退回旧的 JSON 协议, **而且吐的 JSON 还是坏的**时救一次.
//
// ── 运行数据表明需要兜底 ──
//
// 一台跑着的机器, 57 条回话里有 3 条长这样(5.3%): 正文里带着没转义的
// 引号或者一个坏转义, 于是 json.Unmarshal 当场失败, unwrapEnvelope
// 原样返回 —— **用户在界面上看到的是一串 {"said":"…"} 字面量**.
//
// 严格解析仍然是主路. 这里只在**形状明白无误**时救: 开头是单键信封的
// 起手式, 结尾是 "}. 救不出来就原样返回 —— 宁可漏一条, 不能把一段
// 正常的回答切坏.
//
// 治标不治本? 是的. 但"本"在模型那边(它偶尔退回旧协议), 我们改不了;
// 而这条兜底的代价是几十行, 换的是用户不会看见一串 JSON.
var moreFields = regexp.MustCompile(`","[a-zA-Z_][a-zA-Z0-9_]*"\s*:`)

func salvageEnvelope(trimmed string) (string, bool) {
	for _, key := range envelopeKeys {
		head := `{"` + key + `":"`
		if !strings.HasPrefix(trimmed, head) {
			continue
		}
		inner := trimmed[len(head):]
		// 收了尾的去掉尾巴; 没收尾的(输出被截断)照拆 —— 见 unwrapEnvelope
		inner = strings.TrimSuffix(inner, `"}`)
		// 看着还有别的字段就不救 —— 救了会把后面几个字段一起吞进正文.
		// (坏 JSON 解不开, 我们只能靠形状判断, 那就宁可保守.)
		if moreFields.MatchString(inner) {
			return "", false
		}
		return strings.TrimSpace(unescapeLoose(inner)), true
	}
	return "", false
}

var envelopeKeys = []string{"said", "reply", "text", "answer", "message", "content"}

// unescapeLoose 只还原**确定认得的**那几个转义, 其余原样留着.
//
// 不用 strconv.Unquote: 它在遇到坏转义时整段失败 —— 而我们正是因为
// 那段有坏转义才走到这儿的.
func unescapeLoose(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			// 认不得就原样留着 —— 猜一个的话会把内容改坏
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}

// stepFromCalls 把供应商给的 tool_calls 变成一步.
//
// 参数解不开**不算模型的错**: 走原生协议时结构由供应商保证, 解不开
// 说明是适配层出了问题. 错误话术要说清这一点, 否则排查的人会去调
// 提示词 —— 那个方向永远查不出来.
func stepFromCalls(res abi.InferResult) (Step, error) {
	s := Step{Thought: strings.TrimSpace(res.Content), Reasoning: res.Reasoning}
	for _, c := range res.ToolCalls {
		args, err := parseArgs(c.Arguments)
		if err != nil {
			return Step{}, fmt.Errorf(
				"供应商给的工具参数解不开 (%s): %v。原文: %s —— "+
					"这不是提示词的问题, 原生协议下参数结构该由供应商保证",
				c.Name, err, trunc(c.Arguments, 200))
		}
		s.Tools = append(s.Tools, ToolCall{Tool: c.Name, Args: args, ID: c.ID})
	}
	// 只有一个就走单调用那条路 —— 批次路径要处理并发和去重,
	// 单个调用没必要绕一圈
	if len(s.Tools) == 1 {
		s.Tool, s.Args, s.CallID = s.Tools[0].Tool, s.Tools[0].Args, s.Tools[0].ID
		s.Tools = nil
	}
	return s, nil
}

// parseStep 把模型的输出解成一步.
//
// ── "解不开"要分清是**语法**还是**结构** ──
//
// 这是同一类误诊第三次出现了(前两次是截断和空回复), 所以这次拆开:
//
//	语法不合法   括号没配平、引号没闭合 → 让它重发
//	结构不对     语法没问题, 但字段形状错了 → **告诉它哪个字段该是什么形状**
//
// 模型可能发送 {"args":["a.js","b.md"], ...} —— args 给成了数组.
// JSON 本身完全合法, 报成"不是合法 json" 会让它去修语法,
// 而真正要改的是**把 args 写成对象**. 说错原因比不说更糟:
// 它会照着错的方向改, 然后再错一次.
//
// 一律**不猜、不用正则去抠** —— 猜出来的工具调用比明确失败危险得多.
func parseStep(content string) (Step, error) {
	var probe any
	if err := json.Unmarshal([]byte(content), &probe); err != nil {
		return Step{}, fmt.Errorf(
			"输出不是合法 json (%v)。只输出一个 json 对象, 不要用代码块包裹, "+
				"不要在前后加解释文字。原文: %s", err, trunc(content, 200))
	}

	var s Step
	err := json.Unmarshal([]byte(content), &s)
	if err == nil {
		return s, nil
	}

	// 语法没问题却解不进 Step —— 一定是某个字段形状不对.
	// 把**是哪个字段、该是什么形状**说清楚.
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return Step{}, fmt.Errorf(
			"json 语法没问题, 但字段 %q 的形状不对: 你给的是 %s, 这里要的是 %s。%s",
			te.Field, te.Value, te.Type, shapeHint(te.Field))
	}
	return Step{}, fmt.Errorf("json 语法没问题, 但结构不对: %v。原文: %s",
		err, trunc(content, 200))
}

// shapeHint 每个字段该长什么样. 只说错的那一个 ——
// 把整个协议再贴一遍等于让它从头猜.
func shapeHint(field string) string {
	switch field {
	case "args":
		return `args 是一个对象, 键是参数名: {"path":"a.js","old_string":"…"}。` +
			`要一次调多个工具就用 tools 数组, 每项各带自己的 args。`
	case "tools":
		return `tools 是一个数组, 每项形如 {"tool":"read_file","args":{…}}。`
	case "tool", "reply", "done", "thought":
		return field + " 是一个字符串。"
	}
	return "对照输出格式那一节改。"
}

func orDefault(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

// LastUsage 上一次推理的用量 —— 实现 UsageReporter
func (m *LLM) LastUsage() abi.InferResult { return m.lastUsage }

// truncatedErr 输出被 max_tokens 截断 —— **但要说清是哪一种截断**.
//
// ── 两种截断, 两条完全不同的出路 ──
//
//	推理吃光了额度   它一个字正文都没吐出来. 出路是**直接给结论/直接动手**,
//	                 或者把这一步拆小. 跟"内容"毫无关系.
//	正文太长         半截 JSON / 半个文件. 出路才是分几次写.
//
// 原来只有后一句. 真机日志里 **6 次截断, 6 次都是推理吃光了额度**
// (已出 16000, 其中推理 16000 —— 正文是 0), 而它每次都被告知
// "把文件拆小分几次写" —— 它根本没在写文件. 报错报的不是真实发生的那件事,
// 于是给出的动作也是错的, 而每错一次的代价是整整一轮 16000 输出 token.
//
// 这跟 errkind.go 那条是同一件事: **每一种情况给一个能直接照做的动作**,
// 说错了比不说更糟 —— 模型会照着错的方向再撞一次.
func truncatedErr(res abi.InferResult) error {
	// **按占比判, 不按绝对量.**
	//
	// "正文少于 200 token 就算推理占满"这个绝对阈值会在单元测试中通过，
	// 因为测试数据容易恰好落在阈值两侧。额度为 120 时:
	// 推理 33、正文 87, 占比才 27%, 却被报成"额度全花在推理上了".
	// **绝对阈值在小额度下必然误判** —— 而这条错误消息本身就是这一轮在修的东西.
	//
	// 真实案例里推理占 100%(16000/16000); 正文太长那次占 1.9%(300/16000).
	// 两者差着两个数量级, 九成这条线放哪儿都分得开.
	const reasoningDominates = 0.9
	body := res.CompletionTokens - res.ReasoningTokens
	if res.ReasoningTokens > 0 && res.CompletionTokens > 0 &&
		float64(res.ReasoningTokens)/float64(res.CompletionTokens) >= reasoningDominates {
		return fmt.Errorf(
			"输出被 max_tokens 截断 (已出 %d tokens, 其中推理 %d, 正文 %d) —— "+
				"**额度全花在推理上了, 一个字答案都没出来**。别在脑子里把整件事推演完: "+
				"直接给结论, 或者直接调一个工具做**下一小步**, 剩下的下一轮再想。",
			res.CompletionTokens, res.ReasoningTokens, body)
	}
	/**
	 * **说清它是在哪一步上被截断的**.
	 *
	 *	"正文太长, 把它拆小分几次写"是对的, 但它不知道**哪一份**太长.
	 *	连续执行到第十轮时会出现这种情况: 文件越堆越大，它
	 *	在一次 write_file 里被截断, 而报错一个字都没提是哪个文件 ——
	 *	于是它只能猜.
	 *
	 *	被截断的时候供应商仍然会把工具名给回来(参数是半截的). 那一份
	 *	现成的信息说出来, 出路就从"拆小"变成了可以直接照做的一步。
	 */
	if at := truncatedAt(res); at != "" {
		return fmt.Errorf(
			"输出被 max_tokens 截断 (已出 %d tokens, 其中推理 %d) —— "+
				"你是在 %s 上被截断的, 那一份正文太长。**别整份重写**: "+
				"先写前面一部分, 剩下的用 edit_file 一段段补上去。",
			res.CompletionTokens, res.ReasoningTokens, at)
	}
	return fmt.Errorf(
		"输出被 max_tokens 截断 (已出 %d tokens, 其中推理 %d) —— 正文太长, "+
			"把它拆小分几次写。",
		res.CompletionTokens, res.ReasoningTokens)
}

// truncatedAt 被截断的那一步在干什么 —— "write_file(README.md)".
//
//	参数是半截的, 所以路径能挖出来就挖, 挖不出来只说工具名.
//	**不许猜**: 挖错一个路径比不说更糟.
func truncatedAt(res abi.InferResult) string {
	if len(res.ToolCalls) == 0 {
		return ""
	}
	call := res.ToolCalls[len(res.ToolCalls)-1]
	if call.Name == "" {
		return ""
	}
	if at := strings.Index(call.Arguments, `"path"`); at >= 0 {
		rest := call.Arguments[at+len(`"path"`):]
		if colon := strings.Index(rest, ":"); colon >= 0 {
			rest = strings.TrimSpace(rest[colon+1:])
			if strings.HasPrefix(rest, `"`) {
				if end := strings.Index(rest[1:], `"`); end >= 0 {
					return call.Name + "(" + rest[1:1+end] + ")"
				}
			}
		}
	}
	return call.Name
}

// textToolCall 这段回复是不是"把工具调用写成了文字". 是就返回该说的话.
//
// 判据是**形状**: 一个 json 对象, 里面带着调用该有的键. 不按关键词乱猜 ——
// 一句正常的话里提到 tool_calls 是完全可能的(比如它在跟你解释协议).
func textToolCall(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "{") {
		return ""
	}
	// **解得开、而且顶层真有那个键才算**.
	//
	//	原来是在整段文字里找子串: 它引一段 JSON 给用户讲工具怎么调,
	//	整条回复就被作废了 —— 而那句话他再也看不到.
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) != nil {
		return ""
	}
	for _, key := range []string{"tool_calls", "invoke", "tool", "tools"} {
		if _, ok := obj[key]; !ok {
			continue
		}
		return "你把工具调用**写成了文字**, 所以什么都没发生 —— 那次动作丢了。" +
			"要动手就直接发起工具调用(原生 tool_call), 不要输出 json 文本。" +
			"这一步重来。"
	}
	return ""
}
