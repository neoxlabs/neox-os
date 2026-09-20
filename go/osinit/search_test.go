package osinit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

type fakeSearcher struct {
	hits []abi.SearchHit
	err  error
	// gotQuery 记下它收到的词 —— 上限是不是真的由 OS 夹的, 只有在这儿看得出来
	gotQuery string
	gotLimit int
}

func (f *fakeSearcher) Name() string { return "fake" }
func (f *fakeSearcher) Search(_ context.Context, p abi.SearchParams) (abi.SearchResult, error) {
	f.gotQuery, f.gotLimit = p.Query, p.Limit
	if f.err != nil {
		return abi.SearchResult{}, f.err
	}
	return abi.SearchResult{Hits: f.hits, Provider: "fake"}, nil
}

type fakeViewer struct {
	text     string
	err      error
	gotQ     string
	gotBytes int
}

func (f *fakeViewer) Model() string { return "fake-vision" }
func (f *fakeViewer) See(_ context.Context, p abi.SeeParams) (abi.SeeResult, error) {
	f.gotQ, f.gotBytes = p.Question, len(p.DataB64)
	if f.err != nil {
		return abi.SeeResult{}, f.err
	}
	return abi.SeeResult{Text: f.text, Model: "fake-vision"}, nil
}

// searchHarness 起一个带(或不带)搜索服务的 OS + ABI 服务
func searchHarness(t *testing.T, s engine.Searcher, viewers ...engine.Viewer) (*OS, string, string) {
	t.Helper()
	var v engine.Viewer
	if len(viewers) > 0 {
		v = viewers[0]
	}
	o := devOS(func(op *Options) { op.Searcher = s; op.Viewer = v })
	started := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "demo"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return "done", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	token, _ := o.TokenFor(pid)

	p := sockPath(t)
	srv, err := NewAbiServer(p, o.ResolveToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.WithOS(o)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close(); o.Shutdown("t") })
	return o, p, token
}

// 没配搜索时: 握手就说清楚, 而且调了要报**专用的错码**.
//
// 错码跟 infer_failed 分开是关键: 一台机器可以配了推理没配搜索,
// 混成一个码的话"这台机器没有搜索"会被读成"推理挂了" ——
// 而后者会让进程去重试一件永远不会成的事
func TestNoSearcherSaysSoAtHandshake(t *testing.T) {
	_, path, token := searchHarness(t, nil)
	c := dial(t, path)
	res := c.hello(t, token)
	if res.Result == nil || res.Result.HasSearch {
		t.Fatalf("没配搜索却说有: %+v", res.Result)
	}

	select {
	case r := <-c.call(abi.MSearch, abi.SearchParams{Query: "x"}):
		if r.Error == nil || r.Error.Code != abi.ErrSearchFailed {
			t.Fatalf("该报 search_failed: %+v", r)
		}
		if r.Error.Message == "" || !strings.Contains(r.Error.Message, "没有配置搜索服务") {
			t.Fatalf("要说清是这台机器没配, 不是搜索失败了: %q", r.Error.Message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时")
	}
}

// 配了就能搜, 而且**握手时就告诉进程** —— 进程据此决定挂不挂 web_search 工具
func TestSearchOverABI(t *testing.T) {
	fs := &fakeSearcher{hits: []abi.SearchHit{
		{Title: "甲", URL: "https://a/1", Snippet: "摘要"}}}
	o, path, token := searchHarness(t, fs)
	c := dial(t, path)
	res := c.hello(t, token)
	if res.Result == nil || !res.Result.HasSearch {
		t.Fatalf("配了搜索却没在握手时说: %+v", res.Result)
	}

	select {
	case r := <-c.call(abi.MSearch, abi.SearchParams{Query: "找点东西", Limit: 3}):
		if r.Error != nil {
			t.Fatalf("不该出错: %+v", r.Error)
		}
		if r.Result == nil || r.Result.Search == nil || len(r.Result.Search.Hits) != 1 {
			t.Fatalf("结果不对: %+v", r.Result)
		}
		if r.Result.Search.Hits[0].URL != "https://a/1" {
			t.Fatalf("链接串了: %+v", r.Result.Search.Hits[0])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时")
	}
	if fs.gotQuery != "找点东西" || fs.gotLimit != 3 {
		t.Fatalf("参数没原样传下去: %q / %d", fs.gotQuery, fs.gotLimit)
	}

	// **搜索词离开了这台机器, 这件事必须留痕**.
	//
	// 它不做逐次审批(那样的搜索没人用得下去), 所以事后能不能查清
	// "它替我搜过什么"就全靠这条日志 —— 没有它, 这个功能就是个黑盒
	found := false
	for _, evs := range o.Log().Snapshot() {
		for _, e := range evs {
			m, ok := e.Payload.(map[string]any)
			if ok && m["phase"] == "search" && m["query"] == "找点东西" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("搜索没进事件日志 —— 事后查不出它替用户搜过什么")
	}
}

// 搜索侧的错误要原样传上去: "key 不对"和"限流了"的下一步不同
func TestSearchErrorReachesProcess(t *testing.T) {
	fs := &fakeSearcher{err: errors.New("brave 拒绝了这个 key (HTTP 401)")}
	_, path, token := searchHarness(t, fs)
	c := dial(t, path)
	c.hello(t, token)
	select {
	case r := <-c.call(abi.MSearch, abi.SearchParams{Query: "x"}):
		if r.Error == nil || !strings.Contains(r.Error.Message, "401") {
			t.Fatalf("错误被吞了: %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时")
	}
}

// 不认图的机器: 握手就说清楚, 调了报**专用错码**.
//
// 跟搜索同一条理由 —— "这台机器不认图"和"看图失败了"的下一步完全不同:
// 后者会让 agent 换张图重试, 而那件事永远不会成
func TestNoViewerSaysSoAtHandshake(t *testing.T) {
	_, path, token := searchHarness(t, nil)
	c := dial(t, path)
	res := c.hello(t, token)
	if res.Result == nil || res.Result.HasVision {
		t.Fatalf("不认图却说认: %+v", res.Result)
	}
	select {
	case r := <-c.call(abi.MSee, abi.SeeParams{
		MediaType: "image/png", DataB64: "QUJD", Question: "这是什么"}):
		if r.Error == nil || r.Error.Code != abi.ErrVisionFailed {
			t.Fatalf("该报 vision_failed: %+v", r)
		}
		if !strings.Contains(r.Error.Message, "不认图") {
			t.Fatalf("要说清是这台机器不认图: %q", r.Error.Message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时")
	}
}

func TestSeeOverABI(t *testing.T) {
	fv := &fakeViewer{text: "报错是 permission denied"}
	o, path, token := searchHarness(t, nil, fv)
	c := dial(t, path)
	res := c.hello(t, token)
	if res.Result == nil || !res.Result.HasVision {
		t.Fatalf("认图却没在握手时说: %+v", res.Result)
	}
	select {
	case r := <-c.call(abi.MSee, abi.SeeParams{
		MediaType: "image/png", DataB64: "QUJDREVG", Question: "报错原文是什么"}):
		if r.Error != nil {
			t.Fatalf("不该出错: %+v", r.Error)
		}
		if r.Result == nil || r.Result.See == nil ||
			r.Result.See.Text != "报错是 permission denied" {
			t.Fatalf("结果不对: %+v", r.Result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时")
	}
	if fv.gotQ != "报错原文是什么" {
		t.Fatalf("问题没原样传下去: %q", fv.gotQ)
	}

	// 留痕要记问题和模型, **不记图片本身** ——
	// 账本是给人翻的, 一张 base64 图会把它冲垮
	for _, evs := range o.Log().Snapshot() {
		for _, e := range evs {
			m, ok := e.Payload.(map[string]any)
			if !ok || m["phase"] != "see" {
				continue
			}
			if m["question"] != "报错原文是什么" {
				t.Fatalf("没记下问的是什么: %v", m)
			}
			for k, v := range m {
				if s, isStr := v.(string); isStr && strings.Contains(s, "QUJDREVG") {
					t.Fatalf("图片数据进了账本(%s) —— 它会把账本冲垮", k)
				}
			}
			return
		}
	}
	t.Fatal("看图没进事件日志")
}

func TestRecallOverABI(t *testing.T) {
	o, path, token := searchHarness(t, nil)
	o.Log().Append("old", abi.EvProcState, map[string]any{
		"labels": map[string]string{"thread": "t-old"},
	})
	o.Log().Append("old", abi.EvProcOutput, map[string]any{
		"phase": "start", "task": "做个记账小工具",
	})
	o.Log().Append("old", abi.EvInputRecv, map[string]any{
		"text": "账本用分不要用元",
	})

	c := dial(t, path)
	c.hello(t, token)
	// 当前这段也说了同一句话 —— 查出来的不该是它
	<-c.call(abi.MEmit, abi.EmitParams{Payload: map[string]any{
		"phase": "reply", "text": "账本用分不要用元"}})

	select {
	case r := <-c.call(abi.MRecall, abi.RecallParams{Query: "用分", Limit: 5}):
		if r.Error != nil {
			t.Fatalf("不该出错: %+v", r.Error)
		}
		if r.Result == nil || r.Result.Recall == nil || len(r.Result.Recall.Hits) != 1 {
			t.Fatalf("该只命中过去那段: %+v", r.Result)
		}
		h := r.Result.Recall.Hits[0]
		if h.Title != "做个记账小工具" || h.Kind != "用户说" {
			t.Fatalf("命中错了: %+v", h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时")
	}

	found := false
	for _, evs := range o.Log().Snapshot() {
		for _, e := range evs {
			m, ok := e.Payload.(map[string]any)
			if ok && m["phase"] == "recall" && m["query"] == "用分" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("查过去没进事件日志 —— 事后查不出它翻过用户的历史")
	}
}

func TestRecallEmptyQueryIsRejected(t *testing.T) {
	_, path, token := searchHarness(t, nil)
	c := dial(t, path)
	c.hello(t, token)
	select {
	case r := <-c.call(abi.MRecall, abi.RecallParams{Query: "  "}):
		if r.Error == nil || r.Error.Code != abi.ErrBadRequest {
			t.Fatalf("空词该拒: %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时")
	}
}
