package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// Searcher 谁来搜网.
//
//	**nil 是一个有意义的答案**: 这台机器不能搜网, 于是 agent 那边根本
//	不挂 web_search —— 摆一个配不出结果的工具, 模型会照着调, 拿到一句
//	"没配搜索", 而它已经对用户许过诺了。
type Searcher interface {
	Search(ctx context.Context, p abi.SearchParams) (abi.SearchResult, error)
	// Name 谁搜的. 进事件日志 —— 结果不对时要能分清是哪家的问题
	Name() string
}

// 搜网.
//
// ── 为什么必须能外挂 ──
//
//	供应商自带搜索的那几个(OpenAI 的 web_search、Gemini 的 grounding)
//	是各家的私货, 而这台机器上跑的 DeepSeek **没有**: 它的
//	`tools[0].type` 只认 `function`, 传 web_search 直接 400, 而
//	`web_search` / `enable_search` 这类字段被静默忽略 —— 加不加它都
//	答"不知道"。
//
//	所以搜索是一个**外部服务**。而"支持哪几家"不该写死在代码里:
//	用户换第五家就要等我们发版。
//
// ── 两档 ──
//
//	认识的那几家(brave / tavily / serper / bocha)各有各的端点、key 头、
//	回包形状 —— 这些是硬约定, 写错了表现成 401 或者"搜不到", 所以
//	照实写死。
//
//	**剩下的全部走自定义**: 填一个 URL 就行, 回包几种形状都试一遍。
//	认不出来就把原始回包的头一截报上去 —— 那比"搜不到"有用得多:
//	他一眼能看出是 key 不对还是字段名不一样。
type WebSearch struct {
	// Kind brave / tavily / serper / bocha. 空 = 自定义(只按 BaseURL 走)
	Kind string
	// BaseURL 端点. 认识的那几家可以留空(用各自的默认值);
	// **给了就以它为准** —— 外部接口随时会改, 不该等发版
	BaseURL string
	APIKey  string
	Client  *http.Client
}

const (
	// searchMaxHits 一次最多给几条. **上限由 OS 定, 不由进程定** ——
	// 它直接决定占多少上下文
	searchMaxHits = 5
	// searchSnippet 摘要裁到多少字. 各家给的长度差十倍, 不裁的话
	// 一次搜索吐几千字 —— 那已经不是"给它一张地图"
	searchSnippet = 300
)

// endpoints 认识的那几家的默认端点
var endpoints = map[string]string{
	"brave":  "https://api.search.brave.com/res/v1/web/search",
	"tavily": "https://api.tavily.com/search",
	"serper": "https://google.serper.dev/search",
	"bocha":  "https://api.bochaai.com/v1/web-search",
}

// keyHeader key 走哪个头 —— **各家的硬约定**. 写错了表现成 401,
// 而 401 看起来像"key 不对", 于是排查的人去换了一把新 key
var keyHeader = map[string]string{
	"brave":  "X-Subscription-Token",
	"serper": "X-API-KEY",
	"tavily": "Authorization",
	"bocha":  "Authorization",
}

func (w *WebSearch) Name() string {
	if w.Kind != "" {
		return w.Kind
	}
	return "自定义"
}

func (w *WebSearch) Search(ctx context.Context, p abi.SearchParams) (abi.SearchResult, error) {
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return abi.SearchResult{}, fmt.Errorf("搜什么?")
	}
	limit := p.Limit
	if limit <= 0 || limit > searchMaxHits {
		limit = searchMaxHits
	}
	base := strings.TrimSpace(w.BaseURL)
	if base == "" {
		base = endpoints[w.Kind]
	}
	if base == "" {
		return abi.SearchResult{}, fmt.Errorf(
			"不认识 %q 这家搜索服务, 也没给地址 —— 在设置里填一个搜索接口的 URL",
			w.Kind)
	}

	req, err := w.build(ctx, base, query, limit)
	if err != nil {
		return abi.SearchResult{}, err
	}
	cli := w.Client
	if cli == nil {
		cli = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return abi.SearchResult{}, fmt.Errorf("搜索服务连不上: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return abi.SearchResult{}, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden:
		// **401/403 是 key 的问题, 不是搜索词的问题** —— 说不清的话,
		// 排查的人会去改搜索词
		return abi.SearchResult{}, fmt.Errorf(
			"搜索服务拒了这把 key(HTTP %d) —— 检查 NEOX_SEARCH_KEY 或设置页里那一格",
			resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return abi.SearchResult{}, fmt.Errorf("搜索服务返回 %d: %s",
			resp.StatusCode, trunc(strings.TrimSpace(string(raw)), 200))
	}

	hits, ok := parseHits(raw, limit)
	if !ok {
		// **回包结构变了必须报错, 不能返回空.**
		//
		//	空结果("这个词没搜到")和"我们的适配失效了"是两件事。混在
		//	一起的话, 一次接口改版会表现成"最近搜什么都搜不到",
		//	而没有任何地方会说为什么。
		return abi.SearchResult{}, fmt.Errorf(
			"%s 改了接口(回包里找不到结果数组), 前 200 字: %s",
			w.Name(), trunc(strings.TrimSpace(string(raw)), 200))
	}
	return abi.SearchResult{Hits: hits, Provider: w.Name()}, nil
}

// build 一次请求. 认识的那几家照各自的约定, 其余走通用那份
func (w *WebSearch) build(ctx context.Context, base, query string, limit int) (*http.Request, error) {
	if w.Kind == "brave" {
		// Brave 是 GET —— 认识的这几家里唯一一个
		q := url.Values{}
		q.Set("q", query)
		q.Set("count", fmt.Sprintf("%d", limit))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		w.auth(req)
		return req, nil
	}
	// 其余都是 POST。**各家的字段名全给上** —— 不认的那几个会被忽略,
	// 而少给一个的代价是"它只回默认条数"或者直接 400
	body := map[string]any{
		"query": query, "q": query,
		"count": limit, "num": limit, "max_results": limit,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	w.auth(req)
	return req, nil
}

func (w *WebSearch) auth(req *http.Request) {
	if w.APIKey == "" {
		return
	}
	h := keyHeader[w.Kind]
	if h == "" {
		// 自定义那一档: 两个都给。**多带一个不认识的头没有副作用**,
		// 而少带的话它直接 403
		req.Header.Set("Authorization", "Bearer "+w.APIKey)
		req.Header.Set("X-API-KEY", w.APIKey)
		return
	}
	if h == "Authorization" {
		req.Header.Set(h, "Bearer "+w.APIKey)
		return
	}
	req.Header.Set(h, w.APIKey)
}

// parseHits 几种形状都认.
//
//	ok=false 表示**一个结果数组都没找到**, 或者那个键在但不是数组 ——
//	那是接口变了, 不是没搜到。这两件事必须分开, 见 Search 里那段。
func parseHits(raw []byte, limit int) ([]abi.SearchHit, bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	for _, get := range []func(map[string]any) (any, bool){
		func(m map[string]any) (any, bool) { // Brave
			web, ok := m["web"].(map[string]any)
			if !ok {
				return nil, false
			}
			return web["results"], true
		},
		func(m map[string]any) (any, bool) { // 博查
			data, ok := m["data"].(map[string]any)
			if !ok {
				return nil, false
			}
			if pages, ok := data["webPages"].(map[string]any); ok {
				return pages["value"], true
			}
			v, ok := data["results"]
			return v, ok
		},
		func(m map[string]any) (any, bool) { v, ok := m["results"]; return v, ok },
		func(m map[string]any) (any, bool) { v, ok := m["organic"]; return v, ok },
	} {
		v, present := get(m)
		if !present {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			// 键在、但不是数组 = **接口变了**, 不是没搜到
			return nil, false
		}
		return hitsFrom(list, limit), true
	}
	return nil, false
}

func hitsFrom(list []any, limit int) []abi.SearchHit {
	out := make([]abi.SearchHit, 0, len(list))
	for _, one := range list {
		row, ok := one.(map[string]any)
		if !ok {
			continue
		}
		h := abi.SearchHit{
			Title:   clean(firstStr(row, "name", "title")),
			URL:     firstStr(row, "url", "link"),
			Snippet: clip(clean(firstStr(row, "snippet", "content", "description", "summary"))),
		}
		if h.Title == "" && h.URL == "" {
			continue
		}
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// tags Brave 会在命中词上包 <strong> —— 那几个标签进上下文没有意义,
// 而且模型见到 HTML 片段会以为自己拿到的是网页正文
var tags = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

func clean(s string) string {
	return strings.TrimSpace(tags.ReplaceAllString(s, ""))
}

// clip 裁到同一个量级, **按字符不按字节** —— 按字节切会把一个汉字
// 切成半个, 屏幕上是一个乱码方块
func clip(s string) string {
	r := []rune(s)
	if len(r) <= searchSnippet {
		return s
	}
	return string(r[:searchSnippet]) + "…"
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// SearcherFromEnv 从环境变量搭一个 —— 命令行起的那些不经过设置页.
//
//	NEOX_SEARCH_API  哪一家(brave/tavily/serper/bocha), 或者留空走自定义
//	NEOX_SEARCH_URL  端点. 认识的那几家可以不给; **不认识的必须给**
//	NEOX_SEARCH_KEY  凭据
func SearcherFromEnv() Searcher {
	return NewSearcher(
		os.Getenv("NEOX_SEARCH_API"),
		os.Getenv("NEOX_SEARCH_URL"),
		os.Getenv("NEOX_SEARCH_KEY"))
}

// NewSearcher 搭一个. **配不全就返回 nil**.
//
//	nil 是一个有意义的答案: 调用方据此不给 agent 挂 web_search ——
//	摆一个必然失败的工具比没有更糟, 它会照着调然后对用户许诺。
//
//	不认识的一家又没给 URL 时**不猜一个端点出来**: 猜出来的那个会
//	一直 404, 而模型只看得到"搜不到"。
func NewSearcher(kind, base, key string) Searcher {
	kind = strings.ToLower(strings.TrimSpace(kind))
	base = strings.TrimSpace(base)
	key = strings.TrimSpace(key)
	_, known := endpoints[kind]
	if !known && base == "" {
		return nil
	}
	// 认识的那几家都要 key —— 没给的话装出来也是一个必然 401 的工具.
	// 自建的(自定义 + 给了 URL)可以不要 key, SearXNG 就是这样
	if key == "" && kind != "" {
		return nil
	}
	if key == "" && base == "" {
		return nil
	}
	return &WebSearch{Kind: kind, BaseURL: base, APIKey: key}
}
