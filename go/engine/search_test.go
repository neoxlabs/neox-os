package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 三家的回包摘成同一个形状 —— 换一家搜索供应商不该让上层看出区别
func TestThreeProvidersSameShape(t *testing.T) {
	cases := []struct{ kind, body string }{
		{"brave", `{"web":{"results":[
			{"title":"标题甲","url":"https://a.example/1","description":"摘要 <strong>甲</strong>"}]}}`},
		{"tavily", `{"results":[
			{"title":"标题甲","url":"https://a.example/1","content":"摘要 甲"}]}`},
		{"serper", `{"organic":[
			{"title":"标题甲","link":"https://a.example/1","snippet":"摘要 甲"}]}`},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, c.body)
		}))
		ws := &WebSearch{Kind: c.kind, BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
		res, err := ws.Search(context.Background(), abi.SearchParams{Query: "甲"})
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if len(res.Hits) != 1 {
			t.Fatalf("%s: 该有 1 条, 得到 %d", c.kind, len(res.Hits))
		}
		h := res.Hits[0]
		if h.Title != "标题甲" || h.URL != "https://a.example/1" {
			t.Fatalf("%s: 摘错了: %+v", c.kind, h)
		}
		// Brave 会在命中词上包 <strong> —— 那几个标签进上下文没有意义,
		// 而且模型见到 HTML 片段会以为自己拿到的是网页正文
		if strings.Contains(h.Snippet, "<strong>") {
			t.Fatalf("%s: 高亮标签没剥掉: %q", c.kind, h.Snippet)
		}
		if res.Provider != c.kind {
			t.Fatalf("%s: 没记下是谁搜的 —— 结果不对时分不清是哪家的问题", c.kind)
		}
	}
}

// 回包结构变了必须报错, **不能返回空**.
//
// 空结果("这个词没搜到")和"我们的适配失效了"是两件事. 混在一起的话,
// 一次接口改版会表现成"最近搜什么都搜不到", 而没有任何地方会说为什么
func TestWireChangeIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"web":{"results":"这里本该是个数组"}}`)
	}))
	defer srv.Close()
	ws := &WebSearch{Kind: "brave", BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := ws.Search(context.Background(), abi.SearchParams{Query: "x"})
	if err == nil {
		t.Fatal("回包结构变了该报错, 不能静默返回空")
	}
	if !strings.Contains(err.Error(), "改了接口") {
		t.Fatalf("没说清是接口变了, 排查会走错方向: %v", err)
	}
}

// 401/403 是 key 的问题, 不是搜索词的问题 —— 说不清的话, 排查的人会去改搜索词
func TestBadKeySaysKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	ws := &WebSearch{Kind: "brave", BaseURL: srv.URL, APIKey: "bad", Client: srv.Client()}
	_, err := ws.Search(context.Background(), abi.SearchParams{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "NEOX_SEARCH_KEY") {
		t.Fatalf("没指向 key: %v", err)
	}
}

// 条数上限由 OS 定, 不由进程定 —— 它直接决定占多少上下文
func TestLimitCappedByOS(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		// 供应商即使多给, 我们也只留上限那么多
		var results []map[string]string
		for i := 0; i < 50; i++ {
			results = append(results, map[string]string{
				"title": "t", "url": "https://x/1", "content": "c"})
		}
		out, _ := json.Marshal(map[string]any{"results": results})
		w.Write(out)
	}))
	defer srv.Close()
	ws := &WebSearch{Kind: "tavily", BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	res, err := ws.Search(context.Background(), abi.SearchParams{Query: "x", Limit: 999})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) > searchMaxHits {
		t.Fatalf("进程说要 999 条就真给 999 条: 得到 %d", len(res.Hits))
	}
	if n, _ := got["max_results"].(float64); int(n) > searchMaxHits {
		t.Fatalf("往上游要的条数也没被夹住: %v", got["max_results"])
	}
}

// 摘要要裁到同一个量级, 而且**不能把多字节字符切成半个**
func TestSnippetClipKeepsRunesWhole(t *testing.T) {
	long := strings.Repeat("中", searchSnippet+50)
	got := clip(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("没裁: %d 字符", len([]rune(got)))
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("把多字节字符切成半个了")
	}
	if len([]rune(got)) > searchSnippet+1 {
		t.Fatalf("裁得不够: %d 字符", len([]rune(got)))
	}
}

// 没配 key 就返回 nil —— **nil 是一个有意义的答案**:
// 调用方据此不给 agent 挂 web_search 工具
func TestNoKeyMeansNoSearcher(t *testing.T) {
	t.Setenv("NEOX_SEARCH_KEY", "")
	t.Setenv("NEOX_SEARCH_API", "brave")
	if s := SearcherFromEnv(); s != nil {
		t.Fatal("没 key 却装配出了搜索服务 —— 那会变成一个配不出结果的工具")
	}
	t.Setenv("NEOX_SEARCH_KEY", "k")
	s := SearcherFromEnv()
	if s == nil || s.Name() != "brave" {
		t.Fatalf("有 key 该装配出来: %v", s)
	}
	// 不认识的一家 + 没给 URL = 不猜
	t.Setenv("NEOX_SEARCH_API", "某家没听过的")
	if s := SearcherFromEnv(); s != nil {
		t.Fatal("不认识的供应商不该猜一个端点出来")
	}
	// 但给了 URL 就认 —— 外部接口随时会改, 不该等发版
	t.Setenv("NEOX_SEARCH_URL", "https://my.example/search")
	t.Setenv("NEOX_SEARCH_API", "serper")
	if s := SearcherFromEnv(); s == nil {
		t.Fatal("给了 URL 该认")
	}
}

// key 走哪个头是各家的硬约定, 写错了表现成 401 —— 而 401 看起来像"key 不对"
func TestKeyGoesToTheRightPlace(t *testing.T) {
	for _, c := range []struct{ kind, header string }{
		{"brave", "X-Subscription-Token"},
		{"serper", "X-API-KEY"},
		{"tavily", "Authorization"},
	} {
		var seen http.Header
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Clone()
			fmt.Fprint(w, `{"web":{"results":[]},"results":[],"organic":[]}`)
		}))
		ws := &WebSearch{Kind: c.kind, BaseURL: srv.URL, APIKey: "k-secret", Client: srv.Client()}
		_, _ = ws.Search(context.Background(), abi.SearchParams{Query: "x"})
		srv.Close()
		if !strings.Contains(seen.Get(c.header), "k-secret") {
			t.Fatalf("%s: key 没进 %s 头: %v", c.kind, c.header, seen)
		}
	}
}
