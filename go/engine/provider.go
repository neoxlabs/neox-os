package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 供应商适配 —— **只有引擎持有凭据**.
//
// agent 进程永远拿不到 API key: 它只能通过 ABI 请求推理.
// 好处不只是保密:
//
//	· agent 完全不需要 net 能力, netns 里可以一张网卡都没有
//	· 连接复用 / 前缀缓存 / token 记账集中在一处
//	· 出网只有这一个口子, 将来做污点管控只需要盯这一个地方
//
// 注意最后一条的另一面 (FUSION_ANALYSIS §0.3):
// **这个口子同时是最大的外泄面** —— 注入攻击可以把窃取的内容编码进
// 下一次 prompt. 内容管控要做在这里, 因为再往外就是 TLS 了.

// Provider 一个模型供应商
type Provider interface {
	Infer(ctx context.Context, p abi.InferParams) (abi.InferResult, error)
	Model() string
}

// ContextSized 供应商知道自己的窗口就实现它.
//
// 单独一个接口而不是塞进 Provider: 窗口大小**不是协议的一部分**,
// 供应商 API 里没有这个字段. 谁知道谁说, 不知道的就别假装知道 ——
// 让 OS 用保守缺省, 比编一个数字安全.
type ContextSized interface {
	ContextTokens() int64
}

// Catalog 这个供应商都有哪些模型.
//
//	**模型名是抄不对的东西**: deepseek-v4 不是合法模型名(只有 -pro/-flash),
//	而拼错的后果是一次真请求打过去才报错. 供应商自己就有一份名单,
//	问它一句比让人照着文档手敲可靠得多.
type Catalog interface {
	Models(ctx context.Context) ([]string, error)
}

// OpenAICompatible 兼容 OpenAI chat completions 协议的供应商.
//
// DeepSeek / 多数国内外供应商都用这套 wire format. 它是公开知识,
// 攥着没意义, 所以直接实现而不是包一层.
type OpenAICompatible struct {
	BaseURL string
	APIKey  string
	ModelID string
	Client  *http.Client
	// Think 让模型先想再答. **缺省关掉**.
	//
	// ── 为什么默认关 ──
	//
	//	量过: 同一句"1+1=?", 关掉 completion 是 1 个 token, 开着是 34–40 ——
	//	三十几倍, 而那三十几个 token 是要等的时间. 助理绝大多数时候
	//	在做的是"提醒我 5:30 打卡"这种事, 想不想都是同一个答案.
	//
	//	用户的原话: "我要的是快"。真要它想的时候(排查、算路程、要方案),
	//	设置里开一下 —— 那是他自己的决定, 不是我们替他默认.
	//
	// ── 为什么要显式发"关" ──
	//
	//	不发这个字段的话, 想不想由模型自己定, 而 V4 系列默认是想的.
	//	"我们没要求"和"我们要求关掉"在结果上差三十倍.
	Think bool
	// Temp 采样温度. nil = 不提这件事(由供应商自己定).
	//
	// ── 为什么要有它 ──
	//
	//	基准台上量出来的: 同一个二进制、同一批 67 条题, 跑两遍差 0.52 分
	//	(满分 8)。而我改一版提示词的效果多半就在这个数量级 —— 也就是说
	//	**不定住温度, 这台基准量不出我做的任何一件事**.
	//
	//	对产品本身也一样: 他问"5:30 提醒我打卡", 不需要创造性,
	//	需要每次都一样。
	Temp *float64
	// noThinkField 这家供应商不认 thinking 这个字段 —— 撞过一次 400 就记下,
	// 之后不再发. **不能每次都试**: 每次都要多付一次往返
	noThinkField atomic.Bool
}

func NewOpenAICompatible(baseURL, apiKey, model string) *OpenAICompatible {
	return &OpenAICompatible{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		ModelID: model,
		// 超时给得宽: 模型一次往返 500ms–5s 是常态, 长上下文更久.
		// 但不能没有 —— 挂死的请求会把 agent 永远钉在那儿.
		Client: &http.Client{Timeout: 180 * time.Second},
	}
}

func (o *OpenAICompatible) Model() string { return o.ModelID }

// Models 问供应商要一份模型名单 (GET /models, OpenAI 那套 wire format).
//
//	返回的是**它自己认的名字**, 所以拿这份名单里的任何一个填进去都不会
//	拼错. 排一下序 —— 各家返回的顺序不一样, 而一列乱序的名字没法扫.
func (o *OpenAICompatible) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	res, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("列模型失败 (HTTP %d): %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("这个地址返回的不是模型名单: %w", err)
	}
	out := make([]string, 0, len(body.Data))
	for _, entry := range body.Data {
		if strings.TrimSpace(entry.ID) != "" {
			out = append(out, entry.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ContextTokens 模型窗口.
//
// **这个数字不能靠猜**. 官方文档和实际行为可能对不上
// (有的模型标 272K, 实际窗口达到 372K), 所以:
//
//	· 已知的写进表里, 并且注明是从哪儿来的
//	· 不认识的返回 0, 让上层用保守缺省 —— 编一个数字比不知道更危险
//	· 环境变量能覆盖, 因为供应商随时会改而我们不该等发版
func (o *OpenAICompatible) ContextTokens() int64 {
	if v, err := strconv.ParseInt(os.Getenv("NEOX_CONTEXT_TOKENS"), 10, 64); err == nil && v > 0 {
		return v
	}
	return knownContextTokens(o.ModelID)
}

// knownContextTokens 已知模型的窗口. 只写**我们真的验证过**的.
//
// ── 这两个数原来都是错的, 而且错得不小 ──
//
// 原来写 v4 系列 128000("公开口径")、其余 deepseek 64000. 注释上写着
// "只写我们真的验证过的", 而这两个数**一个都没验过** —— 是照文档抄的.
//
// 用户一眼看出不对之后, 我没在"他说的"和"我抄的"之间挑一个, 直接问了
// 供应商: 发一个明显超限的请求, 报错里会写明上限.
//
//	deepseek-v4-flash   This model's maximum context length is 1048576 tokens
//	deepseek-v4-pro     同上
//	deepseek-chat       同上
//	deepseek-reasoner   同上
//
// (顺带问出来: "deepseek-v4"本身不是合法模型名, 只有 -pro 和 -flash.)
//
// 少算 8 倍的后果不只是"没用满": 上下文预算是按窗口算的, 预算小 → 折叠早,
// 而**每折一次就改写一次历史、打断一次前缀缓存**. 前面好几轮在治的
// "每步都折一次"那类毛病, 有一部分根子就在这个数字上.
func knownContextTokens(model string) int64 {
	switch {
	case strings.HasPrefix(model, "deepseek-"):
		// 上面四个都返回同一个上限, 没有再分档的依据 ——
		// 如果出现窗口更小的型号, 应按接口返回的上限更新, 不要猜一个数.
		return 1048576
	}
	return 0 // 不认识就说不知道
}

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ReasoningContent 推理型模型 (DeepSeek V4 等) 把思考过程单独放这里.
	//
	// **回传是有条件的**: 带 tool_calls 的 assistant 消息必须带上它,
	// 纯文本消息必须不带 —— 两个方向都会被拒 (DeepSeek thinking
	// 协议双向 400 规则). 原来这里注释写的是"只读不写", 那只在
	// 没有工具调用的年代成立.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ToolCalls assistant 这一步发起的调用
	ToolCalls []oaToolCall `json:"tool_calls,omitempty"`
	// ToolCallID role=tool 时必填 —— 说明这条是哪次调用的结果.
	// **少一条或者对不上, 供应商直接拒整个请求**
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function oaFunc `json:"function"`
}

type oaFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type oaRequest struct {
	Model     string      `json:"model"`
	Messages  []oaMessage `json:"messages"`
	MaxTokens int         `json:"max_tokens,omitempty"`
	Stream    bool        `json:"stream"`
	// StreamOptions 要用量就得显式问 —— 不带它最后一帧没有 usage
	StreamOptions *oaStreamOptions `json:"stream_options,omitempty"`
	// ResponseFormat 结构化输出. 比让模型自由发挥可靠得多
	ResponseFormat *oaFormat `json:"response_format,omitempty"`
	// Tools 原生工具协议. 结构合法性由供应商保证, 不靠模型自觉
	Tools []oaTool `json:"tools,omitempty"`
	// Thinking 想还是不想. nil = 不提这件事(由模型自己定)
	Thinking *oaThinking `json:"thinking,omitempty"`
	// Temperature 采样温度. nil = 不提, 由供应商自己定
	Temperature *float64 `json:"temperature,omitempty"`
}

// oaThinking DeepSeek 的思考开关. 接口接受 {"type":"disabled"},
// 而 {"thinking":false} 会返回 400("invalid type: boolean")
type oaThinking struct {
	Type string `json:"type"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaFormat struct {
	Type string `json:"type"`
}

type oaResponse struct {
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
		// CompletionTokens **已包含 reasoning_tokens** —— 计费要按它算全量,
		// 只算可见输出会把推理型模型的成本严重低估
		CompletionTokens        int64 `json:"completion_tokens"`
		PromptCacheHitTokens    int64 `json:"prompt_cache_hit_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// buildRequest 把 ABI 参数翻成 wire format.
//
//	抽出来是因为流式和非流式**必须共用同一份组装**:
//	两份各写一遍, 迟早有一份漏掉 reasoning_content 那条双向规则,
//	而那种错只在带工具调用的对话里才炸.
//
// thinkField 这次请求要不要带 thinking. 撞过 400 之后就不带了
func (o *OpenAICompatible) thinkField() *oaThinking {
	if o.noThinkField.Load() {
		return nil
	}
	if o.Think {
		return &oaThinking{Type: "enabled"}
	}
	return &oaThinking{Type: "disabled"}
}

// dropThinking 这家不认 thinking —— 记下来, 下次不发.
//
//	返回 true 表示"值得原样再试一次". 只认报错文本里明确提到
//	thinking 的那种, 别的 400 照常报上去
func (o *OpenAICompatible) dropThinking(body string) bool {
	if o.noThinkField.Load() || !strings.Contains(body, "thinking") {
		return false
	}
	o.noThinkField.Store(true)
	return true
}

func (o *OpenAICompatible) buildRequest(p abi.InferParams) oaRequest {
	msgs := make([]oaMessage, 0, len(p.Messages)+1)
	if p.System != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: p.System})
	}
	for _, m := range p.Messages {
		// 回传时**不带 reasoning_content** —— 供应商会拒
		// ABI 层刻意不用行业惯用的那个角色词 (边界闸挡着),
		// 到这一层才翻译成 wire format 认识的名字
		role := m.Role
		switch role {
		case "assistant_reply":
			role = "assistant"
		case "tool_result":
			role = "tool"
		}
		om := oaMessage{Role: role, Content: m.Content, ToolCallID: m.ToolCallID}
		// reasoning_content 出站回传: **带 tool_calls 才回传, 纯文本一律不回传**.
		//
		// DeepSeek thinking 协议双向 400 规则 —— 两个方向都会拒.
		// 只做一半是历史上真出过的错(6X 那边 commit a1140034 只修一半),
		// 所以这里的条件必须**同时**管住两侧, 不能写成"有就带上".
		if len(m.ToolCalls) > 0 {
			om.ReasoningContent = m.Reasoning
		}
		for _, tc := range m.ToolCalls {
			c := oaToolCall{ID: tc.ID, Type: "function"}
			c.Function.Name = tc.Name
			c.Function.Arguments = tc.Arguments
			om.ToolCalls = append(om.ToolCalls, c)
		}
		msgs = append(msgs, om)
	}

	req := oaRequest{Model: o.ModelID, Messages: msgs, MaxTokens: p.MaxTokens,
		Thinking: o.thinkField(), Temperature: o.Temp}
	for _, t := range p.Tools {
		req.Tools = append(req.Tools, oaTool{Type: "function", Function: oaFunc{
			Name: t.Name, Description: t.Desc, Parameters: t.Params}})
	}
	// **两套结构化机制不能叠**: 各家对 tools + json_object 同时出现的
	// 行为不一 (有的直接拒, 有的让 tool_calls 静默失效).
	// 有 tools 时以 tools 为准 —— 它才是我们要的那套.
	if p.JSONOut && len(req.Tools) == 0 {
		req.ResponseFormat = &oaFormat{Type: "json_object"}
	}
	return req
}

func (o *OpenAICompatible) Infer(ctx context.Context, p abi.InferParams) (abi.InferResult, error) {
	req := o.buildRequest(p)
	body, err := json.Marshal(req)
	if err != nil {
		return abi.InferResult{}, err
	}

	// ── 带退避的重试 ──
	//
	// 一次限流或一次网络抖动不该把整轮的活废掉. 但**重试错东西比不重试
	// 更糟**: 400/401/403/404 重试一百次也是同一个错, 只是把快速失败
	// 拖成一分钟, 而且有些供应商对它们也计费.
	//
	// 整个请求(发出去 + 读完响应体)一起重试 —— 只重发不重读的话,
	// 半截的响应体会被当成"回包解不开", 而那个错是不可重试的.
	var raw []byte
	var status int
	for attempt := 1; ; attempt++ {
		httpReq, rerr := http.NewRequestWithContext(ctx, http.MethodPost,
			o.BaseURL+"/chat/completions", bytes.NewReader(body))
		if rerr != nil {
			return abi.InferResult{}, rerr
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)

		resp, derr := o.Client.Do(httpReq)
		if derr != nil {
			r := classifyTransport(derr)
			if !r.yes || attempt >= retryMaxAttempts {
				return abi.InferResult{}, fmt.Errorf("请求供应商失败: %w", derr)
			}
			if serr := sleepCtx(ctx, backoffFor(attempt, 0)); serr != nil {
				return abi.InferResult{}, serr
			}
			continue
		}
		status = resp.StatusCode
		raw, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		// ── 这家不认 thinking 那个字段 ──
		//
		//	400 而且报错里点了 thinking 的名 —— 把它摘掉原样再发一次,
		//	并且记下来以后都不发. **只重试这一次**: classifyHTTP 一律
		//	不重试 400 是对的(重试一百次也是同一个错), 而这一次是
		//	带着**不同的请求体**去的
		if status == http.StatusBadRequest && o.dropThinking(string(raw)) {
			if b, merr := json.Marshal(o.buildRequest(p)); merr == nil {
				body = b
				continue
			}
		}
		r := classifyHTTP(status, resp.Header)
		if !r.yes {
			break
		}
		if attempt >= retryMaxAttempts {
			return abi.InferResult{}, fmt.Errorf(
				"供应商%s, 退避重试 %d 次仍然失败 (HTTP %d): %s",
				r.why, retryMaxAttempts, status, trunc(string(raw), 200))
		}
		if serr := sleepCtx(ctx, backoffFor(attempt, r.after)); serr != nil {
			return abi.InferResult{}, serr
		}
	}

	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return abi.InferResult{}, fmt.Errorf("供应商回包解不开 (HTTP %d): %s",
			status, trunc(string(raw), 200))
	}
	if out.Error != nil {
		return abi.InferResult{}, fmt.Errorf("供应商拒绝: %s", out.Error.Message)
	}
	if status != http.StatusOK || len(out.Choices) == 0 {
		return abi.InferResult{}, fmt.Errorf("供应商异常 (HTTP %d): %s",
			status, trunc(string(raw), 200))
	}

	var calls []abi.ToolCall
	for _, c := range out.Choices[0].Message.ToolCalls {
		calls = append(calls, abi.ToolCall{
			ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return abi.InferResult{
		ToolCalls:        calls,
		Content:          out.Choices[0].Message.Content,
		Reasoning:        out.Choices[0].Message.ReasoningContent,
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
		CachedTokens:     out.Usage.PromptCacheHitTokens,
		Model:            out.Model,
		FinishReason:     out.Choices[0].FinishReason,
		ReasoningTokens:  out.Usage.CompletionTokensDetails.ReasoningTokens,
	}, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── 流式 ────────────────────────────────────────────────────

// StreamDelta 一小段增量.
//
//	Text 和 Reasoning 分开给: 推理内容**不参与任何判定**, 界面上也该
//	跟正文分开显示. 混成一条流回去, 上层就再也分不开了.
type StreamDelta struct {
	Text      string
	Reasoning string
}

// Streaming 供应商支持流式就实现它.
//
//	跟 ContextSized 一样是**可选能力**: 不支持的供应商不用假装支持,
//	上层拿不到这个接口就退回一次性返回, 而不是自己把整段切片假装成流.
//	假流式骗不过人 —— 它的节奏是均匀的, 真流式不是.
type Streaming interface {
	InferStream(ctx context.Context, p abi.InferParams, onDelta func(StreamDelta)) (abi.InferResult, error)
}

// oaStreamDeltaCall 流式里的工具调用**是碎的**.
//
//	第一帧带 index + id + name, 之后几十帧只带 index 和 arguments 的
//	一小截 —— 参数是一个字符一个字符拼出来的. 所以只能按 index 攒.
type oaStreamDeltaCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string              `json:"content"`
			ReasoningContent string              `json:"reasoning_content"`
			ToolCalls        []oaStreamDeltaCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Model string `json:"model"`
}

// InferStream 流式推理.
//
//	**没有重试**. 非流式那条可以整个请求重发, 因为它要么全有要么全无;
//	流式一旦吐出去一半, 重发就会让用户看见同一段话说两遍 —— 那比
//	直接报错糟得多. 断在半路就把已经拿到的部分和错误一起交上去,
//	由上层决定怎么说.
func (o *OpenAICompatible) InferStream(ctx context.Context, p abi.InferParams, onDelta func(StreamDelta)) (abi.InferResult, error) {
	req := o.buildRequest(p)
	req.Stream = true
	// 要用量就得显式问: 不带这个, 最后一帧不带 usage, 记账就成了估算
	req.StreamOptions = &oaStreamOptions{IncludeUsage: true}
	body, err := json.Marshal(req)
	if err != nil {
		return abi.InferResult{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return abi.InferResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)

	resp, err := o.Client.Do(httpReq)
	if err != nil {
		return abi.InferResult{}, fmt.Errorf("请求供应商失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return abi.InferResult{}, fmt.Errorf("供应商返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out abi.InferResult
	out.Model = o.ModelID
	var text, reasoning strings.Builder
	// ── 工具调用要按 index 攒起来 ──
	//
	//	**这一处漏了会静默地废掉所有带工具的 bot**: 模型发起调用时
	//	finish_reason 是 tool_calls 而 content 是空的, 于是上层拿到一个
	//	"空回复", 重试三次, 整轮失败 —— 而错误信息说的是"模型返回了空回复",
	//	跟真正的原因(我们把调用丢了)一个字都不沾.
	//
	//	因此"你好"这种纯说话的可以成功, 而"我在哪条路旁边"
	//	(要调 what_now)会因调用丢失而整轮失败.
	calls := map[int]*abi.ToolCall{}
	var order []int

	scanner := bufio.NewScanner(resp.Body)
	// 单行上限调大: 一帧里可能塞着一整段推理内容, 默认 64K 会被截断,
	// 而截断在这里表现为"流突然停了", 不会报错
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk oaStreamChunk
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue // 一帧解不开不该拖垮整条流
		}
		if chunk.Usage != nil {
			out.PromptTokens = chunk.Usage.PromptTokens
			out.CompletionTokens = chunk.Usage.CompletionTokens
			out.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				out.FinishReason = choice.FinishReason
			}
			for _, tc := range choice.Delta.ToolCalls {
				c := calls[tc.Index]
				if c == nil {
					c = &abi.ToolCall{}
					calls[tc.Index] = c
					order = append(order, tc.Index)
				}
				// id 和 name 只在第一帧来; 参数是一截一截拼的
				if tc.ID != "" {
					c.ID = tc.ID
				}
				if tc.Function.Name != "" {
					c.Name = tc.Function.Name
				}
				c.Arguments += tc.Function.Arguments
			}
			delta := StreamDelta{Text: choice.Delta.Content, Reasoning: choice.Delta.ReasoningContent}
			if delta.Text == "" && delta.Reasoning == "" {
				continue
			}
			text.WriteString(delta.Text)
			reasoning.WriteString(delta.Reasoning)
			onDelta(delta)
		}
	}
	// **攒好的工具调用要按帧里的顺序交上去**: 模型发的是一批,
	// 而批次里的先后是它自己定的 —— map 遍历顺序会把它打乱
	sort.Ints(order)
	for _, i := range order {
		if c := calls[i]; c != nil && c.Name != "" {
			out.ToolCalls = append(out.ToolCalls, *c)
		}
	}
	if serr := scanner.Err(); serr != nil {
		out.Content = text.String()
		out.Reasoning = reasoning.String()
		// 已经吐出去的那部分照样交上去 —— 上层要能说清"说到一半断了"
		return out, fmt.Errorf("流中断: %w", serr)
	}
	out.Content = text.String()
	out.Reasoning = reasoning.String()
	return out, nil
}
