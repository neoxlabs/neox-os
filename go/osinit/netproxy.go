package osinit

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/confine"
)

// 出网代理 —— 能力集在这里才真正落地.
//
// ── 它是出口不是关卡 ──
//
// 进程所在的 netns 里没有默认路由, 唯一可达的地址就是这个代理.
// 所以它不是"一道可以绕过去的检查", 而是**唯一的出口**:
// 不走它的流量不会被拒绝, 而是根本发不出去.
//
// ── 为什么按主机名而不是 IP ──
//
// 能力集里写的是 registry.npmjs.org 这样的名字. 按 IP 拦的话
// CDN 换一次 IP 就要么拦错要么漏 —— 而漏的时候一声不吭.
// 代理天然拿到主机名 (CONNECT host:443 / 明文请求的 Host 头),
// 所以判断的对象跟授权的对象是同一个东西, 中间没有翻译损失.
//
// ── DNS 在这一侧解析 ──
//
// 进程侧没有 DNS. 名字由这里解析, 于是进程连"能查什么域名"都不知道 ——
// 少一条侧信道, 也少一种"解析到别的 IP 再直连"的绕法(反正它没有路由).

// netProxy 一个进程的出口. 一个进程一个 /30, 所以监听地址即身份 ——
// 不需要按源 IP 查表, 也就没有"查错了串权限"这种失败模式.
type netProxy struct {
	pid abi.ProcessID
	ln  net.Listener
	// rulesOf **每次连接都现查**, 不是开机抄一份.
	//
	//	── 为什么不能冻 ──
	//
	//	文件系统那边规则确实是启动时定死的(landlock 只能收紧不能放宽),
	//	而出网这边根本没有这个限制 —— 代理是我们自己的用户态代码。
	//	把规则抄成一份快照, 等于让网络白白继承了 landlock 的毛病:
	//	用户当场批了域名, 进程却要等到下一轮起新进程才连得出去。
	//	如果规则没有即时生效, 连接只能提示"你随便回一句我就能拉了" ——
	//	这会把实现细节泄露到对话里, 使用者完全没有理由理解它。
	rulesOf func() []abi.NetRule
	onDeny  func(host string)

	mu     sync.Mutex
	closed bool
}

// startNetProxy 在 addr 上起代理. 起不来就报错 —— 调用方必须据此拒绝启动,
// 不能让进程带着一个不通的出口跑起来.
func startNetProxy(pid abi.ProcessID, addr string, rulesOf func() []abi.NetRule,
	onDeny func(host string)) (*netProxy, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("出网代理起不来 (%s): %w", addr, err)
	}
	p := &netProxy{pid: pid, ln: ln, rulesOf: rulesOf, onDeny: onDeny}
	go p.serve()
	return p, nil
}

func (p *netProxy) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	_ = p.ln.Close()
}

func (p *netProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			p.mu.Lock()
			done := p.closed
			p.mu.Unlock()
			if done {
				return
			}
			continue
		}
		go p.handle(c)
	}
}

func (p *netProxy) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	host, port := splitHostPort(req)
	if !p.allows(host, port) {
		if p.onDeny != nil {
			p.onDeny(host)
		}
		// **说清楚为什么, 并说清下一步.**
		//
		// 这条消息最终会出现在 agent 的工具结果里 (npm/curl 会把它打出来).
		// 只回 403 的话它看到的是一句 "403 Forbidden", 然后大概率会去
		// 换镜像源、加 --registry、重试 —— 全是白费, 因为问题不在那儿.
		fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n"+
			"Connection: close\r\n\r\n"+
			"%s 不在你的出网授权里。\n"+
			"这不是网络故障，也不是镜像源的问题——换源、重试、加参数都没有用。\n"+
			"要连它就得让用户授权 net:%s。\n", host, host)
		return
	}

	if req.Method == http.MethodConnect {
		p.tunnel(c, net.JoinHostPort(host, port))
		return
	}
	p.forward(c, req)
}

// tunnel CONNECT —— https 走这条. 我们只看得到主机名, 看不到内容,
// 这是刻意的: 代理是出口不是审查器, 不解密用户的流量.
func (p *netProxy) tunnel(c net.Conn, target string) {
	up, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n连不上 %s: %v\n",
			target, err)
		return
	}
	defer up.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// forward 明文 HTTP
func (p *netProxy) forward(c net.Conn, req *http.Request) {
	req.RequestURI = ""
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL.Host == "" {
		req.URL.Host = req.Host
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n%v\n", err)
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(c)
}

// allows 主机名匹配能力集.
//
// 支持 `*.example.com` 这一种通配, 刻意只支持这一种:
// 通配写法越多, "我以为授的是什么"和"实际授了什么"就越容易分家.
//
// 裸 `*` = 任意主机. 那是"完全放开出网", 该由用户明确授出来,
// 不该是某个模式匹配的副作用.
func (p *netProxy) allows(host, port string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	rules := []abi.NetRule(nil)
	if p.rulesOf != nil {
		rules = p.rulesOf()
	}
	for _, r := range rules {
		if !hostMatches(strings.ToLower(r.Host), host) {
			continue
		}
		// 没写端口 = 不限端口. 写了就必须相等
		if r.Port != "" && r.Port != port {
			continue
		}
		return true
	}
	return false
}

func hostMatches(pattern, host string) bool {
	if pattern == "*" || pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		// **只匹配子域, 不匹配裸域**: `*.example.com` 不该放行 example.com.
		// 这两个在实际部署里经常是不同的东西 (一个是 CDN 一个是 API),
		// 顺手放行等于扩大了用户没打算给的范围.
		return strings.HasSuffix(host, suffix)
	}
	return false
}

// splitHostPort 从请求里取出目标.
//
// CONNECT 的 URL.Host 带端口, 普通请求的 Host 头可能不带 —— 要按 scheme 补.
func splitHostPort(req *http.Request) (host, port string) {
	raw := req.Host
	if req.Method == http.MethodConnect && req.URL != nil && req.URL.Host != "" {
		raw = req.URL.Host
	}
	if h, p, err := net.SplitHostPort(raw); err == nil {
		return h, p
	}
	if req.Method == http.MethodConnect || (req.URL != nil && req.URL.Scheme == "https") {
		return raw, "443"
	}
	return raw, "80"
}

// nsName 空指针安全 —— 没有出网授权时 netns 就是 nil
func nsName(n *confine.NetNamespace) string {
	if n == nil {
		return ""
	}
	return n.Name
}
