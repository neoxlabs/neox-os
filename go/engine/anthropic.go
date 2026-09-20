package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// Anthropic Messages 协议.
//
// ── 为什么 DeepSeek 走这条而不是 OpenAI 那条 ──
//
//	**官方的联网搜索只在这个口上**. 同一把 key、同一个模型:
//
//	  /chat/completions + tools:[{type:"web_search"}]   400, 只认 function
//	  /responses        + 同上                          静默接受, 一次都不搜
//	  /anthropic/v1/messages + web_search_20250305      真搜
//
//	见 nativesearch.go。协议换过来之后, 搜索不用配任何第三方 key。
//
// ── thinking 回传规则跟 OpenAI 那条是同一条 ──
//
//	这个口的规则是:
//
//	  开着 thinking, 带 tool_use 的那轮不回传 thinking 块   400
//	  开着 thinking, 纯文本那轮不带                          可以
//	  关着 thinking, 回传了 thinking 块                      可以(宽松)
//
//	正好是 abi.InferMessage.Reasoning 已经在守的那条("仅在带 tool_calls
//	时回传"), 一个字都不用改。
//
//	**签名它不校验**: 回包里的 signature 就是 message id, 填 "neox"、
//	填空串都照收。所以不用为了它在 ABI 上加一个字段 —— 那要一路改到
//	页表和恢复。真哪天它开始校验了, 表现是 400 而不是错答案。
type Anthropic struct {
	BaseURL string
	APIKey  string
	ModelID string
	Client  *http.Client
	// Think 先想再答. 缺省关 —— 见 OpenAICompatible.Think
	Think bool
	Temp  *float64
}

func NewAnthropic(baseURL, apiKey, model string) *Anthropic {
	return &Anthropic{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		ModelID: model,
		Client:  &http.Client{Timeout: 180 * time.Second},
	}
}

func (a *Anthropic) Model() string { return a.ModelID }

func (a *Anthropic) ContextTokens() int64 {
	if v, err := strconv.ParseInt(os.Getenv("NEOX_CONTEXT_TOKENS"), 10, 64); err == nil && v > 0 {
		return v
	}
	return knownContextTokens(a.ModelID)
}

// Models 模型名单还是走 OpenAI 那个口.
//
//	Anthropic 协议这边没有 /models, 而 DeepSeek 同一个域名下
//	/models 是通的 —— 用户换协议不该连"拉一份名单"都丢了。
func (a *Anthropic) Models(ctx context.Context) ([]string, error) {
	o := &OpenAICompatible{BaseURL: apiRoot(a.BaseURL), APIKey: a.APIKey, Client: a.Client}
	return o.Models(ctx)
}

// messagesURL 这条口在哪儿.
//
//	DeepSeek 把它挂在 /anthropic 下, 而用户填的 base 可能是
//	https://api.deepseek.com 也可能已经带了 /v1 或 /anthropic ——
//	三种都要落到同一个地址, 不然表现是 404 而不是"你填错了"。
func (a *Anthropic) messagesURL() string {
	base := strings.TrimRight(a.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	if strings.HasSuffix(base, "/anthropic") {
		return base + "/v1/messages"
	}
	if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Host, "api.deepseek.com") {
		return u.Scheme + "://" + u.Host + "/anthropic/v1/messages"
	}
	return base + "/v1/messages"
}

// apiRoot 去掉协议后缀, 拿回域名那一层(给 /models 用)
func apiRoot(base string) string {
	b := strings.TrimRight(base, "/")
	b = strings.TrimSuffix(b, "/v1")
	b = strings.TrimSuffix(b, "/anthropic")
	return b
}

func (a *Anthropic) head(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	// 官方 Anthropic 收 x-api-key, 而兼容口(DeepSeek、各家网关)收 Bearer.
	// **两个都发**: 各认各的, 而多一个头不会被拒
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

// ── wire format ──────────────────────────────────────────────

type anBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

type anTool struct {
	Name   string          `json:"name"`
	Desc   string          `json:"description,omitempty"`
	Schema json.RawMessage `json:"input_schema"`
}

type anThinking struct {
	Type string `json:"type"`
}

type anRequest struct {
	Model     string      `json:"model"`
	System    string      `json:"system,omitempty"`
	Messages  []anMessage `json:"messages"`
	MaxTokens int         `json:"max_tokens"`
	Stream    bool        `json:"stream,omitempty"`
	Tools     []anTool    `json:"tools,omitempty"`
	Thinking  *anThinking `json:"thinking,omitempty"`
	Temp      *float64    `json:"temperature,omitempty"`
}

type anUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

type anResponse struct {
	Content    []anBlock `json:"content"`
	StopReason string    `json:"stop_reason"`
	Model      string    `json:"model"`
	Usage      anUsage   `json:"usage"`
	Error      *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// anMaxTokens 这个协议里 max_tokens 是**必填的**.
//
//	OpenAI 那条不填就是"由模型定", 照搬过来是 400。上层很多调用
//	确实不关心上限(p.MaxTokens=0), 给一个够用的数。
const anMaxTokens = 8192

// emptyInput tool_use 的 input 必须是个对象. 模型没给参数时
// Arguments 是空串, 而空串不是合法 JSON
var emptyInput = json.RawMessage(`{}`)

func (a *Anthropic) buildRequest(p abi.InferParams) anRequest {
	req := anRequest{Model: a.ModelID, System: p.System, Temp: a.Temp,
		MaxTokens: p.MaxTokens}
	if req.MaxTokens <= 0 {
		req.MaxTokens = anMaxTokens
	}
	if a.Think {
		req.Thinking = &anThinking{Type: "enabled"}
	} else {
		req.Thinking = &anThinking{Type: "disabled"}
	}
	for _, t := range p.Tools {
		schema := t.Params
		if len(schema) == 0 {
			schema = emptyInput
		}
		req.Tools = append(req.Tools, anTool{Name: t.Name, Desc: t.Desc, Schema: schema})
	}

	for _, m := range p.Messages {
		switch m.Role {
		case "system":
			// 这个协议里 system 不是一个角色, 是顶上那个字段
			if req.System != "" {
				req.System += "\n\n"
			}
			req.System += m.Content
		case "tool_result":
			blk := anBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content}
			// **连着的几条结果要并进同一条 user 消息**: 模型一轮发了两个
			// 调用, 协议要求它们的结果贴在紧接着的那一条里。拆成两条
			// user 消息的话, 中间那条的 tool_use 就对不上了
			if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "user" &&
				lastIsToolResult(req.Messages[n-1]) {
				req.Messages[n-1].Content = append(req.Messages[n-1].Content, blk)
				continue
			}
			req.Messages = append(req.Messages, anMessage{Role: "user", Content: []anBlock{blk}})
		case "assistant_reply":
			var blocks []anBlock
			// thinking 块**只在带 tool_use 那轮回传** —— 同 abi 那条规则.
			// 签名它不校验(见文件头), 但字段得在
			if len(m.ToolCalls) > 0 && strings.TrimSpace(m.Reasoning) != "" {
				blocks = append(blocks, anBlock{Type: "thinking",
					Thinking: m.Reasoning, Signature: "neox"})
			}
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				in := json.RawMessage(tc.Arguments)
				if !json.Valid(in) {
					in = emptyInput
				}
				blocks = append(blocks, anBlock{Type: "tool_use",
					ID: tc.ID, Name: tc.Name, Input: in})
			}
			if len(blocks) == 0 {
				// 空的 content 数组会被拒 —— 而"这一轮它什么都没说"
				// 本身是合法历史(被截断过). 丢掉这一条比丢掉整轮好
				continue
			}
			req.Messages = append(req.Messages, anMessage{Role: "assistant", Content: blocks})
		default:
			req.Messages = append(req.Messages, anMessage{Role: "user",
				Content: []anBlock{{Type: "text", Text: m.Content}}})
		}
	}
	return req
}

func lastIsToolResult(m anMessage) bool {
	return len(m.Content) > 0 && m.Content[len(m.Content)-1].Type == "tool_result"
}

// finishOf 收尾原因翻成上层认的那几个词.
//
//	上层按 "length" 判截断(model_llm.go), 按非空判"它到底为什么不说话".
//	原样透传 "max_tokens" 的话那个判断静默失效。
func finishOf(stop string) string {
	switch stop {
	case "tool_use":
		return "tool_calls"
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	}
	return stop
}

// promptOf 进账的 prompt token 有多少.
//
//	**两家语义不一样**: 官方 Anthropic 的 input_tokens **不含**命中缓存
//	的那部分, 而 DeepSeek 这个口是含的(一次小请求 input 137 /
//	cache_read 128 —— 不含的话总量就成了 265, 不可能)。
//
//	照一家写死, 另一家的账要么少算要么翻倍。按大小判: input 已经
//	盖过缓存那部分就说明它是全量。
func promptOf(u anUsage) int64 {
	cached := u.CacheRead + u.CacheWrite
	if u.Input >= cached {
		return u.Input
	}
	return u.Input + cached
}

func (a *Anthropic) Infer(ctx context.Context, p abi.InferParams) (abi.InferResult, error) {
	body, err := json.Marshal(a.buildRequest(p))
	if err != nil {
		return abi.InferResult{}, err
	}
	var raw []byte
	var status int
	// 重试口径跟 OpenAI 那条一致 —— 见那边的说明
	for attempt := 1; ; attempt++ {
		httpReq, rerr := http.NewRequestWithContext(ctx, http.MethodPost,
			a.messagesURL(), bytes.NewReader(body))
		if rerr != nil {
			return abi.InferResult{}, rerr
		}
		a.head(httpReq)
		resp, derr := a.Client.Do(httpReq)
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

	var out anResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return abi.InferResult{}, fmt.Errorf("供应商回包解不开 (HTTP %d): %s",
			status, trunc(string(raw), 200))
	}
	if out.Error != nil {
		return abi.InferResult{}, fmt.Errorf("供应商拒绝: %s", out.Error.Message)
	}
	if status != http.StatusOK {
		return abi.InferResult{}, fmt.Errorf("供应商异常 (HTTP %d): %s",
			status, trunc(string(raw), 200))
	}

	res := abi.InferResult{
		Model:            out.Model,
		FinishReason:     finishOf(out.StopReason),
		PromptTokens:     promptOf(out.Usage),
		CompletionTokens: out.Usage.Output,
		CachedTokens:     out.Usage.CacheRead,
	}
	var text, think strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			think.WriteString(b.Thinking)
		case "tool_use":
			res.ToolCalls = append(res.ToolCalls, abi.ToolCall{
				ID: b.ID, Name: b.Name, Arguments: string(b.Input)})
		}
	}
	res.Content = text.String()
	res.Reasoning = think.String()
	return res, nil
}

// ── 流式 ────────────────────────────────────────────────────

type anEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Block *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message *struct {
		Model string  `json:"model"`
		Usage anUsage `json:"usage"`
	} `json:"message"`
	Usage *anUsage `json:"usage"`
}

func (a *Anthropic) InferStream(ctx context.Context, p abi.InferParams, onDelta func(StreamDelta)) (abi.InferResult, error) {
	req := a.buildRequest(p)
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return abi.InferResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.messagesURL(), bytes.NewReader(body))
	if err != nil {
		return abi.InferResult{}, err
	}
	a.head(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return abi.InferResult{}, fmt.Errorf("请求供应商失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return abi.InferResult{}, fmt.Errorf("供应商返回 %d: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	out := abi.InferResult{Model: a.ModelID}
	var text, think strings.Builder
	// 工具调用的参数是一小截一小截拼出来的, 按块下标攒.
	// **漏了这段的表现不是报错**: 上层拿到一个"空回复", 重试三次,
	// 而错误信息跟真正的原因一个字都不沾 —— OpenAI 那条上真撞过
	type pend struct {
		call abi.ToolCall
		args strings.Builder
	}
	calls := map[int]*pend{}
	var order []int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue // event: 那几行不用管, 类型在 JSON 里也有
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev anEvent
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				if ev.Message.Model != "" {
					out.Model = ev.Message.Model
				}
				out.PromptTokens = promptOf(ev.Message.Usage)
				out.CachedTokens = ev.Message.Usage.CacheRead
			}
		case "content_block_start":
			if ev.Block != nil && ev.Block.Type == "tool_use" {
				p := &pend{call: abi.ToolCall{ID: ev.Block.ID, Name: ev.Block.Name}}
				calls[ev.Index] = p
				order = append(order, ev.Index)
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			if ev.Delta.PartialJSON != "" {
				if p := calls[ev.Index]; p != nil {
					p.args.WriteString(ev.Delta.PartialJSON)
				}
				continue
			}
			d := StreamDelta{Text: ev.Delta.Text, Reasoning: ev.Delta.Thinking}
			if d.Text == "" && d.Reasoning == "" {
				continue
			}
			text.WriteString(d.Text)
			think.WriteString(d.Reasoning)
			onDelta(d)
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				out.FinishReason = finishOf(ev.Delta.StopReason)
			}
			// 出账的 token 只在这一帧给全
			if ev.Usage != nil && ev.Usage.Output > 0 {
				out.CompletionTokens = ev.Usage.Output
			}
		}
	}
	// 按帧里的先后交上去 —— map 的遍历顺序会把模型自己定的批次顺序打乱
	for _, i := range order {
		if p := calls[i]; p != nil && p.call.Name != "" {
			p.call.Arguments = p.args.String()
			if !json.Valid([]byte(p.call.Arguments)) {
				p.call.Arguments = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, p.call)
		}
	}
	out.Content = text.String()
	out.Reasoning = think.String()
	if serr := scanner.Err(); serr != nil {
		// 已经吐出去的那半截照样交上去 —— 上层要能说清"说到一半断了"
		return out, fmt.Errorf("流中断: %w", serr)
	}
	return out, nil
}

// ── 挑一条协议 ──────────────────────────────────────────────

// Protocol 用户在设置里挑的那一条. "" = 自己认
const (
	ProtoAuto      = ""
	ProtoOpenAI    = "openai"
	ProtoAnthropic = "anthropic"
)

// WantsAnthropic 这个地址该走 Messages 协议吗.
//
//	**认域名不认协议名**: 用户常把 DeepSeek 配成"OpenAI 兼容 +
//	baseUrl=api.deepseek.com", 按用户填的协议名判, 就会把它留在那条
//	没有官方搜索的口上。
//
//	自己认只管我们验过的那几家; 别的一律按用户挑的来 —— 猜错一个
//	自建网关的协议, 表现是整台机器不能推理。
func WantsAnthropic(protocol, baseURL string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case ProtoAnthropic:
		return true
	case ProtoOpenAI:
		return false
	}
	base := strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if strings.HasSuffix(base, "/anthropic") || strings.Contains(base, "/anthropic/") {
		return true
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Host) {
	case "api.deepseek.com", "api.anthropic.com":
		return true
	}
	return false
}

// NewProvider 按协议接一个供应商.
//
//	两条路的开关(想不想、温度)在这里一起拧, 免得加一条协议就要在
//	每个入口再抄一遍 —— 抄漏的那一处表现是"这个二进制不听设置"。
func NewProvider(protocol, baseURL, apiKey, model string, think bool, temp *float64) Provider {
	if WantsAnthropic(protocol, baseURL) {
		p := NewAnthropic(baseURL, apiKey, model)
		p.Think, p.Temp = think, temp
		return p
	}
	p := NewOpenAICompatible(baseURL, apiKey, model)
	p.Think, p.Temp = think, temp
	return p
}
