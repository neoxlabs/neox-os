package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 供应商自己带的搜索 —— **服务端执行, 不用第三方 key**.
//
// ── 它藏在另一个口上 ──
//
//	DeepSeek 的 web_search 确实是官方能力, 但**不在 OpenAI 那个口**:
//
//	  /chat/completions + tools:[{type:"web_search"}]   400, 只认 function
//	  /responses        + 同上                          静默接受但不执行
//	                                                    (官方文档: "忽略")
//	  /anthropic/v1/messages + web_search_20250305      **真搜**
//
//	这个接口返回 server_tool_use / web_search_tool_result
//	两种块, 模型自己发了两次搜索、拿回真实网页、带着链接回答。
//
//	我查这件事的时候只试了前两个口就下了"官方没有"的结论, 而用户记得
//	它能用 —— 他是对的。
//
// ── 为什么是"单独打一次", 不是把工具注进主对话 ──
//
//	**DeepSeek 的服务端 web_search 把 `search` 这个名字占了**: 主对话里
//	同时有一个叫 search 的函数工具时, 整单拒收 ——
//
//	  "The tool search in your request conflicts with server side
//	   web_search calls"
//
//	而我们的内容搜索工具就叫 search。后果不是"搜不了", 是**这一轮整个
//	请求失败**, 而且之后每一轮都失败。
//
//	所以这条路走的是一次**独立的小对话**: 只带 web_search 一个工具、
//	只问一句、把结果摘出来。主对话一个字都不用改。
type NativeSearch struct {
	// BaseURL 供应商地址(比如 https://api.deepseek.com) —— 端点由它推出来
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (n *NativeSearch) Name() string { return "deepseek" }

// NativeSearchFor 这家供应商自己带搜索吗.
//
//	返回 nil = 不带, 调用方该退回外挂那条(见 NewSearcher)。
//
//	**判据看域名不看协议**: 用户常把 DeepSeek 配成 OpenAI 协议 +
//	baseUrl=api.deepseek.com, 按协议判会把它当成"OpenAI 那家",
//	而 OpenAI 那个口根本不搜。
func NativeSearchFor(baseURL, apiKey, model string) Searcher {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return nil
	}
	if !strings.EqualFold(u.Host, "api.deepseek.com") {
		return nil
	}
	return &NativeSearch{BaseURL: baseURL, APIKey: apiKey, Model: model}
}

func (n *NativeSearch) Search(ctx context.Context, p abi.SearchParams) (abi.SearchResult, error) {
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return abi.SearchResult{}, fmt.Errorf("搜什么?")
	}
	limit := p.Limit
	if limit <= 0 || limit > searchMaxHits {
		limit = searchMaxHits
	}
	u, err := url.Parse(n.BaseURL)
	if err != nil {
		return abi.SearchResult{}, err
	}
	endpoint := u.Scheme + "://" + u.Host + "/anthropic/v1/messages"

	body, _ := json.Marshal(map[string]any{
		"model":      n.Model,
		"max_tokens": 2048,
		"stream":     false,
		// max_uses: 一次问话最多搜几轮. 不设的话它会为一个简单问题
		// 连搜四五次 —— 每次都要钱, 而第一次通常就够了
		"tools": []any{map[string]any{
			"type": "web_search_20250305", "name": "web_search", "max_uses": 2,
		}},
		"messages": []any{map[string]any{
			"role": "user", "content": "Perform a web search for the query: " + query,
		}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return abi.SearchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	cli := n.Client
	if cli == nil {
		// 服务端搜索要真的去抓网页, 比一次普通推理慢得多
		cli = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return abi.SearchResult{}, fmt.Errorf("搜索连不上: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return abi.SearchResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return abi.SearchResult{}, fmt.Errorf("搜索返回 %d: %s",
			resp.StatusCode, trunc(strings.TrimSpace(string(raw)), 200))
	}

	var m struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			// web_search_tool_result 那种块里, content 是结果数组
			Content []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
				Page  string `json:"page_age"`
				Text  string `json:"encrypted_content"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return abi.SearchResult{}, err
	}
	var hits []abi.SearchHit
	var said string
	for _, b := range m.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			said = b.Text
		}
		if b.Type != "web_search_tool_result" {
			continue
		}
		for _, r := range b.Content {
			if r.URL == "" && r.Title == "" {
				continue
			}
			hits = append(hits, abi.SearchHit{
				Title: clean(r.Title), URL: r.URL})
			if len(hits) >= limit {
				break
			}
		}
	}
	if len(hits) == 0 {
		// **一条搜索块都没有 = 它没搜**, 不是没搜到.
		//
		//	这台机器上的 /responses 就是这样: 接受这个工具、status
		//	completed、而一次都不搜, 全靠硬猜(消耗了 5834 个推理
		//	token 编了一个假地址)。那种"支持"比 400 危险。
		if strings.TrimSpace(said) != "" {
			return abi.SearchResult{}, fmt.Errorf(
				"这个口没有真的去搜(回包里一条搜索结果都没有), 它只是凭记忆答了: %s —— "+
					"**别拿它当搜到的东西用**", trunc(said, 150))
		}
		return abi.SearchResult{}, fmt.Errorf("没搜到「%s」", query)
	}
	// **把它读完网页之后的那段话也带上**: 光给一列标题和链接的话,
	// 模型还要再 fetch 一遍才知道里面写了什么 —— 而那几个网页
	// 服务端刚刚已经读过了
	if strings.TrimSpace(said) != "" {
		hits[0].Snippet = clip(clean(said))
	}
	return abi.SearchResult{Hits: hits, Provider: n.Name()}, nil
}
