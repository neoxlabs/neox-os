package osinit

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func proxyOn(t *testing.T, rules []abi.NetRule) (*netProxy, string, *[]string) {
	t.Helper()
	var denied []string
	p, err := startNetProxy("p1", "127.0.0.1:0", func() []abi.NetRule { return rules },
		func(h string) { denied = append(denied, h) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, p.ln.Addr().String(), &denied
}

// 直接对代理说话, 拿到第一行状态和整个响应体
func ask(t *testing.T, addr, req string) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	return string(b)
}

// 不在能力集里的主机必须被挡, 而且**要说清楚为什么和下一步**.
//
// 只回一句 403 的话, agent 看到的是"被拒绝了", 然后它唯一想得出来的
// 自救办法就是换镜像源、加 --registry、重试 —— 全是白费,
// 因为问题不在源上. 错误信息是提示词的一部分.
func TestProxyDeniesUngrantedHost(t *testing.T) {
	_, addr, denied := proxyOn(t, []abi.NetRule{{Host: "registry.npmjs.org"}})

	got := ask(t, addr, "CONNECT evil.example.com:443 HTTP/1.1\r\nHost: evil.example.com:443\r\n\r\n")
	if !strings.Contains(got, "403") {
		t.Fatalf("没授权的主机应该被挡: %q", got)
	}
	if !strings.Contains(got, "evil.example.com") {
		t.Fatal("没说是哪个主机被挡了 —— agent 无从判断该申请什么")
	}
	if !strings.Contains(got, "request_access") && !strings.Contains(got, "授权") {
		t.Fatalf("没给出下一步, agent 只会去换镜像源瞎重试: %q", got)
	}
	if len(*denied) != 1 || (*denied)[0] != "evil.example.com" {
		t.Fatalf("被挡的目标必须记账, 用户日后要能回答'它到底想连哪儿': %v", *denied)
	}
}

// 授权过的主机要真的放行 —— 只会拦不会放的代理等于把进程废了
func TestProxyAllowsGrantedHost(t *testing.T) {
	// 起一个真的上游, 免得测试依赖外网
	up := &http.Server{Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "上游回话了") })}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go up.Serve(ln)
	t.Cleanup(func() { _ = up.Close() })

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_, addr, _ := proxyOn(t, []abi.NetRule{{Host: host}})

	got := ask(t, addr, fmt.Sprintf(
		"GET http://%s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
		net.JoinHostPort(host, port), net.JoinHostPort(host, port)))
	if !strings.Contains(got, "上游回话了") {
		t.Fatalf("授权过的主机没放行: %q", got)
	}
}

// 端口也是能力的一部分: 授了 :443 就不该顺带放行 :22
func TestProxyHonorsPort(t *testing.T) {
	_, addr, _ := proxyOn(t, []abi.NetRule{{Host: "example.com", Port: "443"}})
	if got := ask(t, addr,
		"CONNECT example.com:22 HTTP/1.1\r\nHost: example.com:22\r\n\r\n"); !strings.Contains(got, "403") {
		t.Fatalf("授的是 443, 22 不该通: %q", got)
	}
}

// 通配只匹配子域, **不匹配裸域**.
//
// `*.example.com` 顺手放行 example.com 是在扩大用户没打算给的范围 ——
// 这两个在实际部署里经常是不同的东西 (一个 CDN 一个 API).
func TestWildcardDoesNotCoverBareDomain(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"*.example.com", "cdn.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "notexample.com", false},
		{"*.example.com", "evil-example.com", false},
		{"example.com", "example.com", true},
		{"example.com", "cdn.example.com", false},
		{"*", "任何东西.com", true},
	}
	for _, c := range cases {
		if got := hostMatches(c.pattern, c.host); got != c.want {
			t.Errorf("hostMatches(%q, %q) = %v, want %v", c.pattern, c.host, got, c.want)
		}
	}
}

// 空能力集 = 一个都不放行. 缺省必须是安全的那个
func TestEmptyRulesDenyAll(t *testing.T) {
	p := &netProxy{}
	if p.allows("registry.npmjs.org", "443") {
		t.Fatal("没有任何出网授权时必须全拒 —— 缺省不安全是最糟的")
	}
}

// 尾点和大小写不该成为绕过手段.
//
// `REGISTRY.npmjs.org.` 跟 `registry.npmjs.org` 在 DNS 里是同一个东西,
// 按字符串比就会漏 —— 而漏的时候一声不吭.
func TestHostNormalization(t *testing.T) {
	p := &netProxy{rulesOf: func() []abi.NetRule { return []abi.NetRule{{Host: "registry.npmjs.org"}} }}
	for _, h := range []string{
		"registry.npmjs.org", "REGISTRY.NPMJS.ORG", "Registry.Npmjs.Org.",
	} {
		if !p.allows(h, "443") {
			t.Errorf("%q 应该匹配上 —— 大小写/尾点不是不同的主机", h)
		}
	}
}

// 中途批的域名要**立刻**生效 —— 不许等下一轮起新进程.
//
// 这条守的是一个用户完全没理由理解的行为: 他明明批了, 界面也说批了,
// 而 bot 回一句"这一轮还连不出去, 你随便说句话我就能拉了"。
// 那是实现细节(规则被开机抄成了一份快照)漏进了对话。
func TestGrantTakesEffectWithinTheSameRun(t *testing.T) {
	live := []abi.NetRule{{Host: "already.example.com"}}
	p, _, _ := proxyOn(t, nil)
	p.rulesOf = func() []abi.NetRule { return live }

	if p.allows("api.open-meteo.com", "443") {
		t.Fatal("还没批就放行了")
	}
	// 用户当场批了
	live = append(live, abi.NetRule{Host: "api.open-meteo.com"})
	if !p.allows("api.open-meteo.com", "443") {
		t.Fatal("批过了还是连不出去 —— 规则被冻在了开机那一刻")
	}
}
