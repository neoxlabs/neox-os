package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 取回来的必须是**正文**, 不是原始 HTML —— 一个页面 95% 是标签和脚本,
// 原样进上下文挤掉的是前面的对话历史
func TestFetchExtractsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>标题在这</title>
			<style>.a{color:red}</style><script>var x=1;alert("正文")</script></head>
			<body><div class="nav-wrapper-xxl">导航</div>
			<p>第一段正文。</p><p>第二段正文。</p></body></html>`)
	}))
	defer srv.Close()

	got, err := doFetch(Toolbox{}, map[string]any{"url": srv.URL}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"标题在这", "第一段正文。", "第二段正文。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("正文丢了 %q: %q", want, got)
		}
	}
	// 脚本里那个 alert("正文") 一起被扔掉才算对 —— 它含"正文"两个字,
	// 用它做判据能同时验"内容被丢"而不只是"标签被剥"
	if strings.Contains(got, "alert") || strings.Contains(got, "var x") {
		t.Fatalf("script 的内容没被扔掉: %q", got)
	}
	if strings.Contains(got, "color:red") || strings.Contains(got, "nav-wrapper-xxl") {
		t.Fatalf("style/属性没被扔掉: %q", got)
	}
}

// 大页面要分片, 而且那句"接着读"必须说 fetch —— 说 read_file 是**教它做一件
// 做不成的事**: 它会照着调, 拿一句"文件不存在"再猜一轮
func TestFetchSliceSaysFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for i := 1; i <= 1000; i++ {
			fmt.Fprintf(w, "第%d行\n", i)
		}
	}))
	defer srv.Close()

	got, err := doFetch(Toolbox{}, map[string]any{"url": srv.URL}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "fetch url="+srv.URL) {
		t.Fatalf("没告诉它怎么接着读, 或者说成了别的工具: %.200q", got)
	}
	if strings.Contains(got, "read_file") {
		t.Fatalf("提示指向了 read_file, 那条路走不通: %.200q", got)
	}
}

// 非文本一律不进上下文. 判错方向的代价不对称: 少读一个 PDF 只是少一次调用,
// 把二进制塞进来是几万个垃圾 token
func TestFetchRefusesBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.7\x00\x00")
	}))
	defer srv.Close()

	_, err := doFetch(Toolbox{}, map[string]any{"url": srv.URL}, false)
	if err == nil {
		t.Fatal("PDF 该被拒")
	}
	if !strings.Contains(err.Error(), "application/pdf") {
		t.Fatalf("没说清是什么类型, 它没法判断下一步: %v", err)
	}
}

// 4xx/5xx 要翻成"下一步该怎么办", 而不是一句状态码 —— 否则它只会改参数重试
func TestFetchStatusIsActionable(t *testing.T) {
	for code, want := range map[int]string{
		http.StatusNotFound:            "别改参数重试",
		http.StatusTooManyRequests:     "换个来源",
		http.StatusInternalServerError: "对方服务器",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		_, err := doFetch(Toolbox{}, map[string]any{"url": srv.URL}, false)
		srv.Close()
		if err == nil {
			t.Fatalf("HTTP %d 该报错", code)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("HTTP %d 的建议不对: %v", code, err)
		}
	}
}

// JS 渲染的页面抓回来是空的. 这不能只回一句空 ——
// 说清是这一类, 它才会换来源而不是重试同一条
func TestFetchEmptyBodySaysWhy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><div id="root"></div><script>render()</script></body></html>`)
	}))
	defer srv.Close()

	_, err := doFetch(Toolbox{}, map[string]any{"url": srv.URL}, false)
	if err == nil {
		t.Fatal("抽不出正文该说出来")
	}
	if !strings.Contains(err.Error(), "JS") {
		t.Fatalf("没说清成因, 它会重试同一条: %v", err)
	}
}

// file:// 不能借 fetch 读本地文件 —— 那是 read 轴的事, 而 fetch 走的是 net 轴.
// 放过去等于用出网能力做了一次读文件
func TestFetchRejectsNonHTTPScheme(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://x.com/a", "notaurl"} {
		if _, err := doFetch(Toolbox{}, map[string]any{"url": u}, false); err == nil {
			t.Fatalf("%s 该被拒", u)
		}
	}
}

// net 轴的 scope 是主机名, 不是整条 URL.
// 不翻的话每取一个页面都要问一次人, 而且用户看不懂自己在批什么
func TestFetchScopeIsHost(t *testing.T) {
	tool := fetchTool(false)
	if tool.ScopeNorm == nil {
		t.Fatal("fetch 必须声明 ScopeNorm, 否则拿整条 URL 去比能力集永远不匹配")
	}
	got := toolScope(tool, map[string]any{"url": "https://pypi.org/simple/requests/"})
	if got != "pypi.org" {
		t.Fatalf("scope 该是主机名, 得到 %q", got)
	}
	// 翻不出主机名时返回空 —— 预检跳过, 由代理去拦.
	// 拿原值去问人的话, 用户看到的是一句他没法判断的字符串
	if s := toolScope(tool, map[string]any{"url": "::::"}); s != "" {
		t.Fatalf("翻不出主机名时不该拿原值去问人, 得到 %q", s)
	}
}

// 没有出网出口时的报错必须指向 request_access.
// 这是**唯一一个翻错就会白烧好几轮**的错: 翻成"网址写错了"的话,
// 它会去换 URL、换协议、加参数, 而真正该做的只有申请授权
func TestFetchNoEgressPointsAtRequestAccess(t *testing.T) {
	t.Setenv("http_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTPS_PROXY", "")

	err := fetchTransportErr(
		fmt.Errorf(`Get "https://pypi.org": dial tcp: lookup pypi.org: no such host`),
		"pypi.org")
	if !strings.Contains(err.Error(), "request_access") {
		t.Fatalf("没指向申请授权, 它只会去换网址: %v", err)
	}
	if !strings.Contains(err.Error(), "scope=pypi.org") {
		t.Fatalf("没说清申请什么: %v", err)
	}
}

// 走代理时的 403 是"能力集拒绝", 直连时的 403 是"对方网站拒绝" ——
// 混成一句话说, 就会教它去申请一条它其实已经有的能力
func TestProxyDetection(t *testing.T) {
	t.Setenv("http_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	if viaProxy() {
		t.Fatal("没配代理却说走代理")
	}
	t.Setenv("HTTPS_PROXY", "http://10.77.0.1:3128")
	if !viaProxy() {
		t.Fatal("大写的 HTTPS_PROXY 也得认 —— 少认一套就有一半情况判错")
	}
}

// 网上的图: 认图的机器该告诉它怎么看, 不认图的机器不能指向一个不存在的工具.
//
// 跟 read_file 撞到本地图片是同一条 —— 两处都要跟着这台机器变
func TestFetchImageHintFollowsTheMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "\x89PNG")
	}))
	defer srv.Close()

	_, err := doFetch(Toolbox{}, map[string]any{"url": srv.URL}, true)
	if err == nil || !strings.Contains(err.Error(), "view_image") {
		t.Fatalf("认图的机器该告诉它怎么看这张图: %v", err)
	}
	_, err = doFetch(Toolbox{}, map[string]any{"url": srv.URL}, false)
	if err == nil {
		t.Fatal("图片不该进上下文")
	}
	if strings.Contains(err.Error(), "view_image") {
		t.Fatalf("不认图的机器却指向了 view_image: %v", err)
	}
}
