package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// fetch —— 取一个网页/接口回来读.
//
// ── 为什么不是"让它自己 run curl" ──
//
// `run curl` 本来就能出网, 而且审批那条路是通的. 但它有三个毛病, 每一个
// 都容易反复消耗推理轮次:
//
//	· 回来的是**原始 HTML**. 一个普通新闻页 200KB, 其中 95% 是 script/
//	  style/属性 —— 它会把上下文冲垮, 而挤掉的是前面的对话历史
//	· curl 的失败长得都一样. "Could not resolve host" 到底是域名不存在、
//	  还是没出网能力, 它分不出来, 于是去换 DNS、加 --insecure、换镜像 ——
//	  **缺的是授权, 而它没有办法说出这件事** (见 access.go 的同一条理由)
//	· 那条命令走的是 proc 轴. 用户被问的是"允许跑 curl -sL https://… 吗",
//	  他要在一条 shell 命令里辨认出这次真正的目标是哪个主机
//
// 走原生工具之后这三件事都归位: 正文抽出来再分片、失败翻成"下一步该怎么办"、
// 审批走 net 轴且 scope 就是主机名.
//
// ── 边界: 这里不放宽任何东西 ──
//
// fetch 一样出不了网, 除非能力集里有. 它走的是 OS 建好的 http_proxy,
// 跟 `run curl` 同一个出口、同一套规则 —— **代理才是强制点**, 这个文件
// 里的检查只是让它提前知道, 少吃一个 403.
//
// 所以"加了个联网工具"没有扩大攻击面: 出口还是那一个, 污点管控要做的
// 地方也还是那一个 (engine/provider.go 顶上那段说的).
const (
	fetchTimeout  = 45 * time.Second
	fetchMaxBytes = 2 << 20 // 抓回来的上限. 超出的部分连读都不读
)

// fetchTool 取网页.
//
// canSee = 这台机器认不认图. 同 readFileTool: 它只影响**撞上图片时说什么**.
// 不认图的机器上说"用 view_image"就是指向一个不存在的工具.
func fetchTool(canSee bool) Tool {
	return Tool{
		Name: "fetch",
		Desc: "取一个网址的内容(GET)。HTML 会抽成正文, 大页面分片",
		Args: map[string]string{
			"url":    "完整网址, 带 http:// 或 https://",
			"offset": "从第几行开始读, 1 起。不给就从头",
		},
		ArgOrder: []string{"url", "offset"},
		Optional: map[string]bool{"offset": true},
		// net 轴, scope 是**主机名**而不是整条 URL —— 能力集就是按主机名
		// 授的 (confine.HostMatches). 见 ScopeNorm 的说明.
		Needs: abi.AxisNet, ScopeArg: "url", ScopeNorm: hostOf,
		// 只读: GET 不改变世界, 所以可以进并发批次.
		// 而世界会变(同一个 URL 今天明天不一样), 所以 WorldSensitive.
		WorldSensitive: true,
		Timeout:        fetchTimeout,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			return doFetch(t, a, canSee)
		},
	}
}

// hostOf 从 URL 里取主机名. 取不出来就返回空 ——
// **不猜**: 一个翻不出主机名的字符串拿去问人, 用户批的是个他看不懂的东西.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func doFetch(t Toolbox, a map[string]any, canSee bool) (string, error) {
	raw := strings.TrimSpace(argStr(a, "url"))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q 不是一个完整网址。要带上 http:// 或 https://, "+
			"例如 https://example.com/page", raw)
	}
	// scheme 白名单: file:// 会绕过 net 轴去读本地文件(那是 read 轴的事),
	// 而 ftp/gopher 之类代理根本不认. 都当场说清楚, 别让它去撞.
	if u.Scheme != "http" && u.Scheme != "https" {
		if u.Scheme == "file" {
			return "", fmt.Errorf("fetch 不读本地文件。本地路径用 read_file")
		}
		return "", fmt.Errorf("fetch 只支持 http/https, 不支持 %s://", u.Scheme)
	}

	ctx := t.Ctx
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", fmt.Errorf("%s: %v", raw, err)
	}
	// 明说自己是谁. 不伪装成浏览器 —— 一个装成 Chrome 的 agent, 出问题时
	// 对面的运维和我们自己都查不清是谁在抓
	req.Header.Set("User-Agent", "neox-os-agent/1.0")
	req.Header.Set("Accept", "text/html,text/plain,application/json;q=0.9,*/*;q=0.1")

	resp, err := fetchClient().Do(req)
	if err != nil {
		return "", fetchTransportErr(err, u.Hostname())
	}
	defer resp.Body.Close()

	// 代理拒绝 = 没有这条能力. 这是**唯一一个必须翻对的错**:
	// 翻错了它就会去换 URL、换协议、加参数, 白烧好几轮,
	// 而真正该做的事只有一件 —— 申请出网.
	if resp.StatusCode == http.StatusForbidden && viaProxy() {
		return "", fmt.Errorf("连 %s 被出口挡住了(403)。这不是对方网站拒绝你, "+
			"是你**没有连它的能力**。用 request_access axis=net scope=%s 申请, "+
			"别换个写法重试", u.Hostname(), u.Hostname())
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s 返回 HTTP %d。%s", raw, resp.StatusCode,
			httpHint(resp.StatusCode))
	}

	ctype := resp.Header.Get("Content-Type")
	if !textual(ctype) {
		next := "要的是文件就 run curl -o 下载下来"
		if canSee && strings.HasPrefix(strings.ToLower(firstToken(ctype)), "image/") {
			// **网上的图现在也看得了** —— 但得先落到盘上:
			// view_image 读的是文件, 而这台机器的出口只有一个,
			// 让它自己 curl -o 下来比再造一条"取图"的路干净
			next = "要的是这张图就先 run curl -o 存到 .tmp/, 再用 view_image 看"
		}
		return "", fmt.Errorf("%s 是 %s, 不是文本 —— 读进来对你没用还会挤爆上下文。%s",
			raw, firstToken(ctype), next)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("%s: 读回包失败: %v", raw, err)
	}
	truncated := len(body) > fetchMaxBytes
	if truncated {
		body = body[:fetchMaxBytes]
	}

	text := strings.TrimSpace(string(body))
	if isHTML(ctype, text) {
		text = htmlToText(text)
	}
	if text == "" {
		// **空不等于失败, 但也不能只回一句空**: 大量页面正文是 JS 渲染出来的,
		// 抓回来确实什么都没有. 说清楚是这一类, 它才会换个来源而不是重试同一条.
		return "", fmt.Errorf("%s 取回来了(HTTP %d), 但抽不出正文 —— "+
			"多半是正文由 JS 渲染的页面。换一个来源, 或者找这个站的 API/RSS",
			raw, resp.StatusCode)
	}
	if truncated {
		text += fmt.Sprintf("\n\n(原始内容超过 %d KB, 只取了前面这一段)", fetchMaxBytes/1024)
	}
	return sliceOf("fetch", "url", raw, text, argInt(a, "offset")), nil
}

// fetchClient 出网用的客户端.
//
// **必须走 http_proxy**: 进程的 netns 里没有默认路由也没有 DNS, 唯一可达
// 的地址就是 OS 起的那个代理 (netproxy.go). 自己解析域名的话,
// 报出来的会是一句 "no such host" —— 那正是 `run curl` 当年那个坑,
// 排查了两轮才定位到"断在最后一米".
//
// ProxyFromEnvironment 认 http_proxy/https_proxy/no_proxy 四个变量,
// 跟 run 传给子命令的是同一套 (passThroughEnv), 两条路不会分叉.
func fetchClient() *http.Client {
	return &http.Client{
		Timeout:   fetchTimeout,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
		// 重定向跟随, 但**跨主机的那一跳一样要过代理**, 所以能力集照样
		// 拦得住 —— 一个只批了 a.com 的授权不会因为 302 就变成批了 b.com.
		// 这里只限次数: 循环重定向不该把一次调用挂满 45 秒.
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向超过 5 次, 停了")
			}
			return nil
		},
	}
}

// viaProxy 这个进程是不是被 OS 安排走代理出网.
//
// 用它来分辨 403 的两种成因: 走代理时的 403 几乎一定是能力集拒绝,
// 直连时的 403 是对方网站自己拒绝. 混成一句话说, 就会教它去申请
// 一条它其实已经有的能力.
func viaProxy() bool {
	for _, k := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return true
		}
	}
	return false
}

// fetchTransportErr 连不上时的翻译.
//
// 三种成因表现得很像, 而下一步完全不同:
//
//	没有代理也没有路由   →  它根本没有出网能力, 该去申请
//	代理在但连不上       →  OS 侧的问题, 重试没用, 该告诉用户
//	域名不存在/超时      →  换个地址或者稍后再试
func fetchTransportErr(err error, host string) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout"):
		return fmt.Errorf("连 %s 超时(%s)。别原样重试第二次 —— "+
			"要么换一个来源, 要么告诉用户这个站现在打不开", host, fetchTimeout)
	case !viaProxy() && (strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "server misbehaving") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "connection refused")):
		// 没有代理 = OS 没给这个进程出网. 这条最容易被误读成"网址写错了"
		return fmt.Errorf("连不上 %s, 而且这个进程**没有出网的出口** —— "+
			"用 request_access axis=net scope=%s 申请, 批准之后下次说话就能取了。"+
			"不要换网址重试(原始错误: %s)", host, host, trimErr(msg))
	default:
		return fmt.Errorf("连 %s 失败: %s", host, trimErr(msg))
	}
}

// httpHint 状态码翻成下一步.
func httpHint(code int) string {
	switch code {
	case http.StatusNotFound:
		return "这个地址不存在。别改参数重试同一条 —— 先确认链接是从哪儿来的"
	case http.StatusUnauthorized:
		return "要登录/凭据才能看。你没有它, 直接告诉用户"
	case http.StatusTooManyRequests:
		return "被限流了。换个来源, 别连着重试"
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "是对方服务器出问题, 不是你的调用写错了。可以稍后再试一次, 但别连着试"
	}
	return "不是你能改参数解决的, 换个来源或者告诉用户"
}

func trimErr(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func firstToken(ctype string) string {
	if i := strings.IndexAny(ctype, ";"); i >= 0 {
		ctype = ctype[:i]
	}
	ctype = strings.TrimSpace(ctype)
	if ctype == "" {
		return "未知类型"
	}
	return ctype
}

// textual 这个 Content-Type 值不值得读进上下文.
//
// **白名单而不是黑名单**: 认识的才读. 反过来做的话, 每出现一种没见过的
// 二进制格式就是一次上下文被垃圾字节冲垮, 而那个代价比"少读一个页面"大得多
// —— 跟 isBinary 那里是同一条不对称.
func textual(ctype string) bool {
	c := strings.ToLower(firstToken(ctype))
	if c == "未知类型" {
		// 没给 Content-Type 的站不少, 按文本收 —— 后面还有一道
		// isBinary 兜着(抽正文之前先看有没有 NUL)
		return true
	}
	if strings.HasPrefix(c, "text/") {
		return true
	}
	switch c {
	case "application/json", "application/xml", "application/xhtml+xml",
		"application/javascript", "application/rss+xml", "application/atom+xml",
		"application/ld+json", "application/x-ndjson", "application/yaml":
		return true
	}
	return false
}

func isHTML(ctype, body string) bool {
	if strings.Contains(strings.ToLower(firstToken(ctype)), "html") {
		return true
	}
	// 没给类型时看开头 —— 但只看开头一小段, 一个 JSON 里出现 "<html"
	// 不该把它当网页去剥标签
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	head = strings.ToLower(head)
	return strings.Contains(head, "<!doctype html") || strings.Contains(head, "<html")
}
