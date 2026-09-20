package osinit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 采集口挂在观察口上 —— **这一组测的是"那条路由到底存不存在"**.
//
//	这不是假想的失败: 手机端一直有一个 OsClient.signal() 在往
//	`<base>/signal` POST, 而 console 的路由表里根本没有这一条 ——
//	既没人调它, 调了也是 404. 整条感知链在出货的二进制上是断的,
//	而**编译器、单测、界面全都不会说一个字**.
//
//	组件各自的行为在 signals_test / senseapi_test 里已经测透了.
//	这里只测最容易漏、也最静默的那一段: 线接没接上.

func newSenseObserve(t *testing.T) (*SignalBus, string, string) {
	t.Helper()
	o := New(Options{Mode: abi.ModeDev})
	bus := NewSignalBus(o.Log(), func(Digest) {}, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second,
	})
	srv, err := NewObserveServer(o, ObserveOptions{
		Addr: "127.0.0.1:0", Token: "tok", Sense: bus,
	})
	if err != nil {
		t.Fatalf("起不来: %v", err)
	}
	srv.Start()
	t.Cleanup(func() { _ = srv.Close(); o.Shutdown("test") })
	return bus, "http://" + srv.Addr(), "tok"
}

func postJSON(t *testing.T, url, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("投不进去: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestObserveTakesSignals(t *testing.T) {
	bus, base, tok := newSenseObserve(t)
	code, out := postJSON(t, base+"/signal", tok,
		`{"id":"s1","source":"phone.test","kind":"place.arrived","at":`+
			fmt.Sprint(time.Now().UnixMilli())+`}`)
	if code != 200 {
		t.Fatalf("观察口不收信号: HTTP %d %v", code, out)
	}
	if out["result"] == "" || out["result"] == nil {
		t.Fatalf("收下了却不说去了哪儿: %v —— 采集端没法判断自己的时钟对不对", out)
	}
	// 真的进了总线, 不是被谁悄悄吃掉
	if bus.Pending() == 0 && bus.Watermark() == 0 {
		t.Fatal("HTTP 说收下了, 而总线里什么都没有 —— 这正是最难查的那种断法")
	}
}

func TestObserveTakesBatchAndHeartbeat(t *testing.T) {
	_, base, tok := newSenseObserve(t)
	now := time.Now().UnixMilli()
	code, out := postJSON(t, base+"/signals", tok, fmt.Sprintf(
		`[{"id":"a","source":"ha","kind":"door.opened","at":%d},`+
			`{"id":"b","source":"ha","kind":"door.closed","at":%d}]`, now, now))
	if code != 200 {
		t.Fatalf("批量口不通: HTTP %d %v", code, out)
	}
	if n, _ := out["n"].(float64); int(n) != 2 {
		t.Fatalf("投了两条, 它说 %v 条", out["n"])
	}
	// 心跳 —— 没有它, 感知层可以静默地死掉而没有任何一处会说
	code, out = postJSON(t, base+"/heartbeat?source=ha&everySec=60", tok, "")
	if code != 200 {
		t.Fatalf("心跳口不通: HTTP %d %v", code, out)
	}
}

// 采集口跟观察口共用一把锁 —— 它收的是位置和来电, 没有匿名模式
func TestObserveSenseNeedsToken(t *testing.T) {
	_, base, _ := newSenseObserve(t)
	for _, path := range []string{"/signal", "/signals", "/heartbeat?source=x&everySec=1"} {
		code, _ := postJSON(t, base+path, "wrong", `{"source":"x","kind":"y"}`)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s 拿错 token 也放进去了: HTTP %d —— 这个口子收的是位置和来电", path, code)
		}
	}
}

// 没接总线的机器上, 这三条路由**不该存在**.
//
//	404 和"收下了但没人处理"必须分得开: 后者会让采集端一直以为
//	一切正常, 而它投的每一条都进了黑洞
func TestObserveWithoutSenseHasNoIngest(t *testing.T) {
	_, base, tok := newObserveFixture(t)
	for _, path := range []string{"/signal", "/signals", "/heartbeat"} {
		code, _ := postJSON(t, base+path, tok, `{"source":"x","kind":"y"}`)
		if code != http.StatusNotFound {
			t.Fatalf("没接感知层, %s 却回了 HTTP %d —— 采集端会以为投进去了", path, code)
		}
	}
}
