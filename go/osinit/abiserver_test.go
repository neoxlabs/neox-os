package osinit

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// sun_path 有 104/108 字节上限 —— 路径必须短, 不能塞进深目录
func sockPath(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "nx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "s")
}

func TestSocketPathTooLongFailsEarly(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", MaxSocketPath)
	if _, err := NewAbiServer(long, nil, nil); err == nil {
		t.Fatal("过长路径必须提前炸, 不留给 bind 报天书")
	}
}

// harness: 起 OS + 一个挂着的进程 + ABI 服务
func harness(t *testing.T) (*OS, *AbiServer, string, string, abi.ProcessID) {
	t.Helper()
	o := devOS()
	started := make(chan struct{})
	pid, err := o.Spawn(
		abi.ProcessSpec{App: "demo", Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/in"}}},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			close(started)
			<-ctx.Done() // 挂着不退, 让外部客户端代表它调 ABI
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
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close(); o.Shutdown("t") })
	return o, srv, p, token, pid
}

// 极简客户端 —— 只为测服务端; 真客户端在 TS 侧 (跨语言对拍)
type testClient struct {
	conn net.Conn
	dec  abi.Decoder
	mu   sync.Mutex
	next int
	want map[int]chan abi.Response
}

func dial(t *testing.T, path string) *testClient {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	tc := &testClient{conn: c, next: 1, want: map[int]chan abi.Response{}}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				frames, derr := tc.dec.Push(buf[:n])
				if derr != nil {
					return
				}
				for _, body := range frames {
					var res abi.Response
					_ = json.Unmarshal(body, &res)
					tc.mu.Lock()
					ch := tc.want[res.ID]
					delete(tc.want, res.ID)
					tc.mu.Unlock()
					if ch != nil {
						ch <- res
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = c.Close() })
	return tc
}

func (c *testClient) call(method abi.Method, params any) chan abi.Response {
	c.mu.Lock()
	id := c.next
	c.next++
	ch := make(chan abi.Response, 1)
	c.want[id] = ch
	c.mu.Unlock()

	raw, _ := json.Marshal(params)
	frame, _ := abi.EncodeFrame(abi.Request{ID: id, Method: method, Params: raw})
	c.mu.Lock()
	_, _ = c.conn.Write(frame)
	c.mu.Unlock()
	return ch
}

func (c *testClient) hello(t *testing.T, token string) abi.Response {
	t.Helper()
	select {
	case r := <-c.call(abi.MHello, abi.HelloParams{Token: token, Wire: abi.WireVersion}):
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("hello 超时")
		return abi.Response{}
	}
}

func TestFakeTokenRejected(t *testing.T) {
	_, _, path, _, _ := harness(t)
	c := dial(t, path)
	res := c.hello(t, "假的")
	if res.Error == nil || res.Error.Code != abi.ErrUnauthenticated {
		t.Fatalf("假 token 必须被拒: %+v", res)
	}
}

func TestMustHelloFirst(t *testing.T) {
	_, _, path, _, _ := harness(t)
	c := dial(t, path)
	select {
	case res := <-c.call(abi.MEmit, abi.EmitParams{Payload: 1}):
		if res.Error == nil || res.Error.Code != abi.ErrUnauthenticated {
			t.Fatalf("没 hello 就调方法必须被拒: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时")
	}
}

func TestWireVersionMismatchRejected(t *testing.T) {
	_, _, path, token, _ := harness(t)
	c := dial(t, path)
	select {
	case res := <-c.call(abi.MHello, abi.HelloParams{Token: token, Wire: 999}):
		if res.Error == nil || res.Error.Code != abi.ErrWireVersion {
			t.Fatalf("线格式版本不匹配必须被拒: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时")
	}
}

func TestEmitAndCanOverSocket(t *testing.T) {
	o, _, path, token, pid := harness(t)
	c := dial(t, path)
	if res := c.hello(t, token); res.Result == nil || res.Result.PID != pid {
		t.Fatalf("hello 应当回 pid: %+v", res)
	}

	<-c.call(abi.MEmit, abi.EmitParams{Payload: map[string]any{"step": "跨进程输出"}})
	r1 := <-c.call(abi.MCan, abi.CanParams{Axis: abi.AxisRead, Scope: "/in/a.txt"})
	r2 := <-c.call(abi.MCan, abi.CanParams{Axis: abi.AxisWrite, Scope: "/in/a.txt"})
	if r1.Result == nil || !*r1.Result.Value {
		t.Fatal("授了的应当能过")
	}
	if r2.Result == nil || *r2.Result.Value {
		t.Fatal("没授的不该过")
	}

	kinds := map[abi.EventKind]bool{}
	for _, e := range o.Log().Replay(pid, 0) {
		kinds[e.Kind] = true
	}
	for _, k := range []abi.EventKind{abi.EvProcOutput, abi.EvCapUsed, abi.EvCapDenied} {
		if !kinds[k] {
			t.Fatalf("缺事件 %s", k)
		}
	}
}

// 并发请求按 id 归位 —— 回复乱序也不串味
func TestConcurrentRequestsAddressedByID(t *testing.T) {
	_, _, path, token, _ := harness(t)
	c := dial(t, path)
	c.hello(t, token)

	type want struct {
		ch  chan abi.Response
		exp bool
	}
	var ws []want
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			ws = append(ws, want{c.call(abi.MCan, abi.CanParams{Axis: abi.AxisRead, Scope: "/in/x"}), true})
		} else {
			ws = append(ws, want{c.call(abi.MCan, abi.CanParams{Axis: abi.AxisWrite, Scope: "/in/x"}), false})
		}
	}
	for i, w := range ws {
		select {
		case res := <-w.ch:
			if res.Result == nil || *res.Result.Value != w.exp {
				t.Fatalf("第 %d 个回复串味了: %+v", i, res.Result)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 个请求超时", i)
		}
	}
}

// decide 挂很久, 期间别的调用照常 —— 单连接不能被长请求堵死
func TestDecideDoesNotBlockConnection(t *testing.T) {
	o, _, path, token, _ := harness(t)
	c := dial(t, path)
	c.hello(t, token)

	pendingCh := c.call(abi.MDecide, abi.DecideParams{
		Request: abi.DecisionRequest{Present: abi.PresentSpec{Kind: "approve", Title: "要继续吗"}}})

	// 等 OS 侧登记上
	deadline := time.Now().Add(2 * time.Second)
	for len(o.Decisions().Pending()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// decide 还挂着, 但别的调用不受影响
	select {
	case res := <-c.call(abi.MCan, abi.CanParams{Axis: abi.AxisRead, Scope: "/in/y"}):
		if res.Result == nil || !*res.Result.Value {
			t.Fatal("长请求期间其它调用应当照常")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("被 decide 堵死了")
	}
	select {
	case <-pendingCh:
		t.Fatal("decide 不该提前返回")
	default:
	}

	d := o.Decisions().Pending()[0]
	o.Decisions().Resolve(d.DID, "yes", "phone:测试", nil)
	select {
	case res := <-pendingCh:
		if res.Result == nil || res.Result.Resolution.Choice != "yes" ||
			res.Result.Resolution.By != "phone:测试" {
			t.Fatalf("%+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("解决后 decide 没回")
	}
}

// 进程终态后 token 立即失效
func TestTokenDiesWithProcess(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	pid, _ := o.Spawn(abi.ProcessSpec{App: "demo"},
		InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
			return "done", nil
		}})
	waitState(t, o, pid, abi.StateExited)
	token, _ := o.TokenFor(pid)
	if _, _, ok := o.ResolveToken(token); ok {
		t.Fatal("终态进程的 token 必须失效")
	}
}
