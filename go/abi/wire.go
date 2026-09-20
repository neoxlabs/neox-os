package abi

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// ── 线格式 ──────────────────────────────────────────────────
//
// 帧: [4 字节大端长度][UTF-8 JSON]
//
//   - 长度前缀而不是换行分隔 —— JSON 里出现换行不用转义, 也不会粘包
//   - 有最大帧长上限, 否则一个坏长度就能让对端分配 4GB
//
// 请求都带 id, 回复**按 id 归位**.
// 不许按"最后一个请求"归位 —— 并发请求的回复允许乱序到达,
// 按顺序归位会把 A 的结果接到 B 上, 症状还会伪装成"对端返回了错数据".

const (
	MaxFrameBytes = 16 * 1024 * 1024
	WireVersion   = 1
)

var (
	ErrFrameTooLarge = errors.New("frame exceeds max")
	ErrShortFrame    = errors.New("incomplete frame")
)

type Method string

const (
	MHello  Method = "hello"
	MEmit   Method = "emit"
	MDecide Method = "decide"
	MCan    Method = "can"
	MSpend  Method = "spend"
	// MInfer 请求一次模型推理.
	//
	// **推理是 OS 提供的服务, 不是 agent 自己去连**. 三个理由:
	//
	//  1. API key 永远不进被约束的进程 —— 它拿不到, 也就漏不掉
	//  2. agent 完全不需要 net 能力 —— netns 里可以一张网卡都没有
	//  3. 连接池 / 前缀缓存 / token 记账都集中在一处
	//
	// 第 3 条尤其关键: token 由 **OS 按供应商回报的真实用量记账**,
	// agent 报不报、报多少都一样 —— 止损线躲不掉.
	MInfer Method = "infer"
	// MRecv 等下一句用户输入. **会阻塞**, 可能等很久.
	//
	// 有了它, 一次对话就是**一个活着的进程**, 而不是每句话起一个新的:
	//
	//  1. 多轮记忆天然成立 —— 历史就在它自己的地址空间里
	//  2. **前缀缓存跨轮保持** —— 每句话起新进程的话, 缓存每次都要重建
	//  3. 等待期间进程转 waiting, 不占计算 —— 等一小时和等一秒一样便宜
	//
	// 第 2 条是选这个设计而不是"把历史传给新进程"的决定性理由.
	MRecv Method = "recv"
	// MSearch 搜一次公开网络.
	//
	// **跟 MInfer 同一个理由, 不是"顺手也放这儿"**: 搜索 API 要 key,
	// 而凭据不进被约束的进程. 让 agent 自己去调等于把 key 发给它,
	// 那是这套设计里唯一不能破的一条.
	//
	// 反过来说, 有了这条方法, 搜索**不需要 net 能力**: 进程的 netns 里
	// 依然可以一张网卡都没有, 出网的只有 OS.
	//
	// 拿回来的是"标题 + 链接 + 摘要", 不是网页正文 ——
	// 正文要用 fetch 去取, 而那一步要过能力集. 两件事分开是刻意的:
	// **搜索不该成为绕过出网审批的旁路**.
	MSearch Method = "search"
	// MSee 让模型看一张图, 回答一个具体的问题.
	//
	// ── 为什么是"问一句", 不是"把图塞进对话" ──
	//
	// 把图片放进消息历史看起来更自然, 但那要改的是这套系统最贵的一段:
	// 页表、折叠、预算、前缀缓存指纹 —— 全是按"消息是文本"建的.
	// 而且主模型不认图的话, 整段对话都得换一家供应商.
	//
	// 走服务这条路, **进程侧一个字节都不用改**: 它送出去一张图和一个问题,
	// 收回来一段文字, 跟别的工具结果长得一样.
	//
	// **代价要说清楚**: 一次翻译会丢信息 —— 视觉模型没被问到的东西不会写.
	// 所以问题是必填的: 问"这张截图里的报错是什么"比"描述一下这张图"
	// 有用得多, 而后者恰恰是不给问题时模型会退化成的样子.
	//
	// 谁来看由 OS 定: 主模型自己认图就用它, 不认就用另配的视觉供应商.
	// 进程不知道也不需要知道 —— 跟 infer 一样.
	MSee Method = "see"
	// MRecall 在事件日志里查过去.
	//
	// 日志一直在写, 缺的是检索. 用户问"上周那个报表怎么算的",
	// 没有这条的话 agent 只能说"我没有跨会话的记忆" —— 那是把
	// "我这个进程看不见"说成了"这件事不存在".
	//
	// **不进系统提示词**: 会话中途才知道要查哪一条, 改提示词会
	// 作废整段前缀缓存. 结果走工具返回, 跟 search/see 同一条.
	//
	// 当前这段对话的历史进程本来就看得见, OS 会把它排除掉 ——
	// 查出来再喂一遍是噪音, 还会让它以为那段是"以前的事".
	MRecall Method = "recall"
	// MHistory 取一段已结束对话的事件, 用来接着聊.
	//
	// 只能取**自己被授权接续的那个** pid —— 由 OS 在 spawn 时定,
	// 不是进程说取谁就取谁. 不然一个进程能读到别人的对话.
	MHistory Method = "history"
)

type Request struct {
	ID     int             `json:"id"`
	Method Method          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type HelloParams struct {
	Token string `json:"token"`
	Wire  int    `json:"wire"`
}

type EmitParams struct {
	Payload any `json:"payload"`
}

type DecideParams struct {
	Request DecisionRequest `json:"request"`
}

type CanParams struct {
	Axis  CapAxis `json:"axis"`
	Scope string  `json:"scope"`
}

type SpendParams struct {
	Delta BudgetDelta `json:"delta"`
}

type InferMessage struct {
	// Role user | assistant_reply | tool_result | system 之外不接受.
	// 注意这里用 assistant_reply 而不是行业惯用的那个词 ——
	// OS 层不引入对话角色的词汇 (边界闸挡着), 到供应商适配层再翻译.
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls 这一步发起的工具调用 (Role=assistant_reply 时).
	//
	// **发起和结果必须成对**: 供应商要求每个 tool_calls 后面跟着
	// 对应 id 的结果消息, 少一条整个请求就被拒 —— 这是自造 JSON
	// 协议没有的约束, 折叠逻辑必须照顾到 (见 context.go).
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	// ToolCallID 这条是哪次调用的结果 (Role=tool_result 时必填)
	ToolCallID string `json:"toolCallId,omitempty"`
	// Reasoning 这一步的推理原文, 仅在**带 tool_calls 时**回传.
	//
	// ── 这是供应商的硬契约, 不是可选优化 ──
	//
	// DeepSeek thinking 协议是**双向**的:
	//
	//	带 tool_calls   必须回传 reasoning_content, 否则 400
	//	纯文本          禁止回传, 否则也是 400
	//
	// 少了它, 一段对话可能今天能发明天就 400 —— 因为供应商也会
	// 靠 tool_call id 认"这是不是我生成的那一轮", 而那个识别是有
	// 时效的. 靠它等于把正确性押在服务端缓存上.
	//
	// 6X 客户端把这条规则声明在 family yaml 的 thinking.passback,
	// 默认就是 with_tool_calls. 我们这边没有 family 表, 直接按
	// 这个默认实现 —— 对不需要回传的供应商它也无副作用.
	Reasoning string `json:"reasoning,omitempty"`
}

// ToolCall 一次工具调用.
//
// ID 由**我们**生成而不是收模型给的: 它要跨轮稳定 (前缀缓存的前提),
// 而且要能跟页表里的动作页一一对上.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments 原样的 JSON 字符串 —— 供应商就是这么给的, 也这么收.
	// 解析放到用的时候做, 中间不做一次多余的编解码往返.
	Arguments string `json:"arguments"`
}

// ToolDef 告诉模型有哪些工具可用.
//
// ── 为什么要走原生而不是在提示词里描述 ──
//
// 自造 JSON 协议 (让模型输出 {"tool":..,"args":..}) 的结构合法性
// 完全靠模型自觉, 可能在 JSON 后面多输出一句中文、
// 输出被截断成半个对象 —— 整步作废重来.
//
// 原生 tool calling 由供应商侧保证结构合法, 还能省掉提示词里那一大段
// 格式说明 (那段字节也占前缀缓存).
type ToolDef struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
	// Params JSON Schema. 原样透传给供应商.
	//
	// **必须字节稳定**: 它进的是请求的前缀, 变一个字节所有缓存全废.
	// 所以由工具表按固定顺序生成, 不许用 map 直接序列化 (Go 的 map
	// 序列化顺序是随机的, 那会让缓存每轮都失效).
	Params json.RawMessage `json:"params,omitempty"`
}

type InferParams struct {
	// System 系统段. **必须字节稳定**, 由引擎统一组装 —— 变一个字节缓存全废
	System    string         `json:"system,omitempty"`
	Messages  []InferMessage `json:"messages"`
	MaxTokens int            `json:"maxTokens,omitempty"`
	// JSONOut 要求模型输出 JSON. 结构化输出比让它自由发挥可靠得多.
	//
	// 有 Tools 时不该再设它: 两套结构化机制叠在一起, 供应商的行为
	// 各家不一 (有的直接拒, 有的让 tool_calls 失效).
	JSONOut bool `json:"jsonOut,omitempty"`
	// Tools 可用的工具. 空 = 这次不许调工具, 只能说话.
	Tools []ToolDef `json:"tools,omitempty"`
}

// SearchParams 搜一次.
type SearchParams struct {
	Query string `json:"query"`
	// Limit 要几条. 0 = 由 OS 定 —— **上限也由 OS 定**:
	// 一次搜索的结果条数直接决定占多少上下文, 那不能由进程说了算
	Limit int `json:"limit,omitempty"`
}

// SearchHit 一条结果.
//
// 刻意只有三个字段. 各家搜索 API 还会给出排名、发布日期、站点图标、
// 相关问题…… 那些进了上下文就是纯噪音, 而**噪音的代价是挤掉真正的内容**.
type SearchHit struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// Snippet 摘要. 各家给的长度不一, 由适配层裁到同一个量级 ——
	// 不裁的话一次搜索能吐出几千字, 那已经不是"给它一张地图"了
	Snippet string `json:"snippet,omitempty"`
}

type SearchResult struct {
	Hits []SearchHit `json:"hits"`
	// Provider 谁搜的. 进事件日志 —— 结果不对时要能分清是哪家的问题
	Provider string `json:"provider,omitempty"`
}

// SeeParams 看一张图.
type SeeParams struct {
	// MediaType image/png · image/jpeg · image/webp · image/gif
	MediaType string `json:"mediaType"`
	// DataB64 图片本身. **走 base64 而不是路径**: 路径是进程那一侧的,
	// OS 未必看得见同一个文件系统(将来引擎可能在别的机器上);
	// 而且传路径等于给了一条"让 OS 替我读任意文件"的旁路
	DataB64 string `json:"dataB64"`
	// Question 要从这张图里知道什么. **必填** —— 见 MSee 的说明:
	// 不问具体问题, 拿回来的就是一段没有重点的泛泛描述
	Question string `json:"question"`
}

type SeeResult struct {
	Text string `json:"text"`
	// Model 谁看的. 进事件日志 —— 看错了要能分清是哪个模型的问题
	Model string `json:"model,omitempty"`
	/**
	 * PromptTokens/CompletionTokens 这一眼花了多少.
	 *
	 *	**看图是要花钱的, 而且不便宜**(一张图几千 token). 这笔钱若
	 *	不记录, 就不进花费页、不算进每轮上限、判据也看不见 —— 一轮
	 *	刚看完一张图仍可能只报 spent: 354.
	 *
	 *	成本必须可追溯. 一笔查不到的开销比数字大一点更糟:
	 *	账目对不上时无法知道差在哪儿.
	 */
	PromptTokens     int64 `json:"promptTokens,omitempty"`
	CompletionTokens int64 `json:"completionTokens,omitempty"`
}

// RecallParams 查一次过去.
type RecallParams struct {
	Query string `json:"query"`
	// Limit 要几条. 0 = 由 OS 定. **上限也由 OS 定**:
	// 一次查出来的条数直接决定占多少上下文, 那不能由进程说了算
	Limit int `json:"limit,omitempty"`
}

// RecallHit 一条对得上的过去.
//
// 刻意只有四个字段. 事件日志里还有 seq / pid / 原始 payload,
// 那些进了上下文就是给模型看账本, 而它要的是"那件事是什么".
type RecallHit struct {
	// When 绝对时间 (UTC). 相对说法("4 分钟前")会在压缩/重放时失真
	When string `json:"when"`
	// Title 那段对话的开场白 —— 用户认的是这句话, 不是 pid
	Title string `json:"title"`
	// Kind 这条是哪一类: 输入 / 回复 / 执行 / 指派
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type RecallResult struct {
	Hits []RecallHit `json:"hits"`
}

// RecvParams 收下一句输入
type RecvParams struct {
	// TimeoutMs 等多久. 0 = 一直等 (进程就该这样待命)
	TimeoutMs int64 `json:"timeoutMs,omitempty"`
}

// HistoryParams 要哪个进程的历史. 空 = OS 在 spawn 时指定的那个
type HistoryParams struct {
	PID ProcessID `json:"pid,omitempty"`
}

type HistoryResult struct {
	Events []Event `json:"events"`
}

type RecvResult struct {
	Text string `json:"text"`
	// From 谁说的 —— 审计用. 将来多端接入时能分清是手机还是终端
	From string `json:"from,omitempty"`
	// Closed 对话结束了, 别再等了
	Closed bool `json:"closed,omitempty"`
	// Relay 这一轮是**同一会话中的其他成员交办的**, 不是外部输入.
	//
	//	用来卡住转包的链子: 被交办的人不能再往下转. 没有这个标记的话
	//	A 交给 B、B 交给 C、C 又交回 A —— 一屋子 bot 自己聊到预算烧光.
	Relay bool `json:"relay,omitempty"`
	// Quiet 这一轮是**机器自己叫醒的**, 没有人在等回话.
	//
	//	── 为什么必须标出来 ──
	//
	//	定时任务到点时, OS 把题目当成一条"收到的话"投给 bot ——
	//	于是界面上它长得跟人输入的字一模一样, 会被误认为是人工发出的消息.
	//
	//	更糟的是回话: 它每次醒来回一句"（非窗口期，不动。）",
	//	而那是**任务回执**, 不是对人说的话. 一天几十条堆在对话里,
	//	用户打开那个 bot 看到的就是一屏"不动".
	//
	//	所以这一位从投递一路带到回话上: 界面据此**两头都不画**.
	//	它真有要紧的事要说, 走的是投递通道(deliveries), 那条照样到人跟前.
	Quiet bool `json:"quiet,omitempty"`
	// Voice 他在**用耳朵听**你说话.
	//
	// ── 为什么这一位要一路带到模型跟前 ──
	//
	//	同一句话, 打字看和念出来听是两种东西: 屏幕上三行扫一眼就过去了,
	//	念出来是二十秒 —— 而他多半在开车、在做饭、手上腾不出来.
	//
	//	**念一大段没人受得了**。所以这一轮的回话要短, 而且不能分点、
	//	不能列清单(念出来是"一、二、三"这种, 听着像念文件).
	//
	//	不做成一份单独的提示词: 那就是两套提示词, 而这个仓库刚花了
	//	一整轮把两套并成一套. 它是**这一轮的一个事实**, 跟着这一句
	//	进来、跟着这一轮出去。
	Voice bool `json:"voice,omitempty"`
}

type InferResult struct {
	Content string `json:"content"`
	// Reasoning 模型的推理过程 (V4 等模型单独给一个字段).
	// 它**不参与任何判定**, 只进事件日志供人观察 ——
	// 把推理内容喂回决策逻辑是让模型自己给自己背书.
	Reasoning        string `json:"reasoning,omitempty"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	// CachedTokens 命中前缀缓存的部分 —— 引擎那套页表设计的直接度量
	CachedTokens int64  `json:"cachedTokens"`
	Model        string `json:"model"`
	// FinishReason 供应商给的收尾原因 (stop / length / …).
	// 必须带回来: 空回复到底是"模型没话说"还是"被截断了"完全是两回事,
	// 分不清就会把截断误报成"模型输出格式不对".
	FinishReason string `json:"finishReason,omitempty"`
	// ReasoningTokens 推理占了多少 —— 排查"输出被推理吃光"时要看它
	ReasoningTokens int64 `json:"reasoningTokens,omitempty"`
	// ToolCalls 模型这一步要调的工具. 走原生协议时结构由供应商保证合法.
	//
	// 可能同时有 Content 和 ToolCalls (模型一边说话一边动手),
	// 也可能两者都空 —— 那是**被截断了**, 要靠 FinishReason 分辨,
	// 不能当成"模型没话说".
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
}

type Response struct {
	ID     int        `json:"id"`
	Result *Result    `json:"result,omitempty"`
	Error  *WireError `json:"error,omitempty"`
}

type Result struct {
	Kind       string              `json:"kind"` // hello|ok|bool|decision|infer|recv|history|search|see|recall
	PID        ProcessID           `json:"pid,omitempty"`
	ABIVersion string              `json:"abiVersion,omitempty"`
	Value      *bool               `json:"value,omitempty"`
	Resolution *DecisionResolution `json:"resolution,omitempty"`
	Infer      *InferResult        `json:"infer,omitempty"`
	Recv       *RecvResult         `json:"recv,omitempty"`
	History    *HistoryResult      `json:"history,omitempty"`
	Search     *SearchResult       `json:"search,omitempty"`
	See        *SeeResult          `json:"see,omitempty"`
	Recall     *RecallResult       `json:"recall,omitempty"`
	// HasVision 这台机器认不认图, hello 时告诉进程. 同 HasSearch.
	HasVision bool `json:"hasVision,omitempty"`
	// HasSearch 这台机器有没有配搜索服务, hello 时告诉进程.
	//
	// **由 OS 说, 不由进程猜**: 跟 ContextTokens 同一条理由.
	// 进程据此决定要不要把 web_search 挂进工具表 —— 摆一个配不出结果的
	// 工具, 模型会照着调、拿到一句"没配搜索", 而它已经对用户许过诺了.
	HasSearch bool `json:"hasSearch,omitempty"`
	// ContextTokens 模型窗口, hello 时告诉进程.
	//
	// **agent 自己不查表** —— 它根本不知道自己在用哪个模型,
	// 推理是 OS 提供的服务, 窗口就该由 OS 说.
	// 进程自己维护一张模型→窗口的表, 换个模型就会悄悄用错数字.
	ContextTokens int64 `json:"contextTokens,omitempty"`
}

type ErrorCode string

const (
	ErrUnauthenticated ErrorCode = "unauthenticated"
	ErrWireVersion     ErrorCode = "wire_version"
	ErrBudgetExceeded  ErrorCode = "budget_exceeded"
	ErrProcessGone     ErrorCode = "process_gone"
	ErrBadRequest      ErrorCode = "bad_request"
	// ErrInferFailed 供应商侧失败 (网络/限流/拒绝). 跟我们自己的错分开,
	// 否则 agent 分不清"是我请求不对"还是"是外面出问题了"
	ErrInferFailed ErrorCode = "infer_failed"
	// ErrSearchFailed 搜索侧失败. 跟 infer_failed 分开: 一台机器可以
	// 配了推理没配搜索, 混成一个码的话, "这台机器没有搜索"会被读成
	// "推理挂了" —— 而后者会让进程去重试一件永远不会成的事
	ErrSearchFailed ErrorCode = "search_failed"
	// ErrVisionFailed 看图侧失败. 跟 search/infer 分开, 同一条理由:
	// "这台机器不认图"和"推理挂了"的下一步完全不同
	ErrVisionFailed ErrorCode = "vision_failed"
	ErrInternal     ErrorCode = "internal"
)

type WireError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// EncodeFrame 把消息编成一帧.
func EncodeFrame(msg any) ([]byte, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxFrameBytes {
		return nil, fmt.Errorf("%w: %d", ErrFrameTooLarge, len(body))
	}
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out, nil
}

// Decoder 增量解帧.
//
// TCP/unix socket 不保证一次读就是一帧 —— 可能半帧, 也可能几帧粘在一起.
// 必须自己缓冲按长度切, 不能假设一次读就是一条消息.
type Decoder struct {
	buf []byte
}

// Push 喂入原始字节, 吐出这一批能完整解出的所有帧体 (未解析的 JSON).
//
// 坏长度直接返回错误, **不试图恢复** —— 恢复就是给攻击者留缝.
func (d *Decoder) Push(chunk []byte) ([][]byte, error) {
	d.buf = append(d.buf, chunk...)
	var out [][]byte
	for {
		if len(d.buf) < 4 {
			break
		}
		n := binary.BigEndian.Uint32(d.buf[:4])
		if n > MaxFrameBytes {
			return out, fmt.Errorf("%w: %d", ErrFrameTooLarge, n)
		}
		if uint32(len(d.buf)) < 4+n {
			break
		}
		body := make([]byte, n)
		copy(body, d.buf[4:4+n])
		d.buf = d.buf[4+n:]
		out = append(out, body)
	}
	return out, nil
}

// Pending 还缓着多少字节 —— 诊断用
func (d *Decoder) Pending() int { return len(d.buf) }
