package osinit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func newObserveFixture(t *testing.T) (*OS, string, string) {
	t.Helper()
	o := New(Options{Mode: abi.ModeDev})
	srv, err := NewObserveServer(o, ObserveOptions{Addr: "127.0.0.1:0", Token: "tok"})
	if err != nil {
		t.Fatalf("起不来: %v", err)
	}
	srv.Start()
	t.Cleanup(func() { _ = srv.Close(); o.Shutdown("test") })
	return o, "http://" + srv.Addr(), "tok"
}

func TestObserveRequiresToken(t *testing.T) {
	_, base, _ := newObserveFixture(t)
	res, err := http.Get(base + "/processes")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("没 token 也放行了: %d", res.StatusCode)
	}
}

func TestObserveRefusesNonLoopback(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("test")
	if _, err := NewObserveServer(o, ObserveOptions{Addr: "0.0.0.0:0", Token: "tok"}); err == nil {
		t.Fatal("绑 0.0.0.0 应该被拒")
	}
}

// 远程放行必须是显式的: 同一个地址, 不带 AllowRemote 拒, 带了才放 ——
// 自托管(Docker/云主机)走的就是这条, 而它绝不能是缺省
func TestObserveRemoteNeedsExplicitOptIn(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("test")
	srv, err := NewObserveServer(o, ObserveOptions{Addr: "0.0.0.0:0", Token: "tok", AllowRemote: true})
	if err != nil {
		t.Fatalf("显式放行了还起不来: %v", err)
	}
	_ = srv.Close()
}

// 打进来的界面挂在根上, 但**数据仍在 token 后面**:
// 静态壳谁都拿得到(等价于下载一个客户端), API 一步都不让
func TestObserveServesEmbeddedUI(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	ui := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>neox</html>")}}
	srv, err := NewObserveServer(o, ObserveOptions{Addr: "127.0.0.1:0", Token: "tok", UI: ui})
	if err != nil {
		t.Fatalf("起不来: %v", err)
	}
	srv.Start()
	t.Cleanup(func() { _ = srv.Close(); o.Shutdown("test") })
	base := "http://" + srv.Addr()

	res, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "neox") {
		t.Fatalf("根路径该给界面, 得到 %d %q", res.StatusCode, body)
	}
	// 界面开着不等于门开着: API 没 token 照样 401
	apiRes, err := http.Get(base + "/processes")
	if err != nil {
		t.Fatal(err)
	}
	defer apiRes.Body.Close()
	if apiRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("界面挂上之后 API 裸奔了: %d", apiRes.StatusCode)
	}
}

// 没打界面的构建, 根路径就该 404 —— 一页空壳比 404 更糟, 它看着像坏了
func TestObserveNoUINoRoot(t *testing.T) {
	_, base, _ := newObserveFixture(t)
	res, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("没界面时根路径该 404, 得到 %d", res.StatusCode)
	}
}

func TestObserveRefusesAnonymous(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("test")
	if _, err := NewObserveServer(o, ObserveOptions{Addr: "127.0.0.1:0"}); err == nil {
		t.Fatal("没 token 应该起不来")
	}
}

// TestObserveBackfillHasNoGap 这条是整个观察口的存在理由.
//
//	补齐和实时之间**不许有缝**. 缝的症状极其恶劣:
//	订阅方拿到的是一段看起来连续的流, 它自己不会知道少了一段,
//	于是界面上少了几句话, 而所有人都以为一切正常.
//
//	所以这里一边灌事件一边接上来, 断言收到的 seq 是 0..N 连续的.
func TestObserveBackfillHasNoGap(t *testing.T) {
	o, base, token := newObserveFixture(t)

	const pid = abi.ProcessID("p1")
	const total = 400

	// 先灌一半历史
	for i := 0; i < total/2; i++ {
		o.Log().Append(pid, abi.EvProcOutput, map[string]any{"n": i})
	}

	// 一边继续灌, 一边接上来 —— 这就是"补齐/实时交界"那一瞬
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := total / 2; i < total; i++ {
			o.Log().Append(pid, abi.EvProcOutput, map[string]any{"n": i})
			time.Sleep(200 * time.Microsecond)
		}
	}()

	req, _ := http.NewRequest("GET", base+"/stream?from="+string(pid)+":0", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	seen := map[int]bool{}
	deadline := time.After(5 * time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev abi.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				continue
			}
			if ev.PID != pid {
				continue
			}
			seen[ev.Seq] = true
			if len(seen) == total {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-deadline:
	}
	wg.Wait()

	var missing []int
	for i := 0; i < total; i++ {
		if !seen[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		head := missing
		if len(head) > 12 {
			head = head[:12]
		}
		t.Fatalf("流里有洞: 缺 %d 条 %v", len(missing), head)
	}
}

// TestObserveDedupesOverlap 重连补齐**必然重叠**, 重复要么不发要么可丢弃,
// 但绝不能因为怕重复而少发.
func TestObserveDedupesOverlap(t *testing.T) {
	o, base, token := newObserveFixture(t)
	const pid = abi.ProcessID("p1")
	for i := 0; i < 20; i++ {
		o.Log().Append(pid, abi.EvProcOutput, map[string]any{"n": i})
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/stream?from=%s:15", base, pid), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	got := []int{}
	scanner := bufio.NewScanner(res.Body)
	deadline := time.Now().Add(2 * time.Second)
	for scanner.Scan() && time.Now().Before(deadline) {
		line := scanner.Text()
		if strings.HasPrefix(line, ": live") {
			break
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev abi.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err == nil {
			got = append(got, ev.Seq)
		}
	}
	if len(got) != 5 {
		t.Fatalf("从 seq=15 续应该只收到 15..19 共 5 条, 收到 %d 条: %v", len(got), got)
	}
	for i, seq := range got {
		if seq != 15+i {
			t.Fatalf("续传位置不对: %v", got)
		}
	}
}

// TestObservePreflightAllowsAuthorization 预检必须放行 authorization.
//
//	不放行的症状极其难查: curl 全绿(它不发预检), 浏览器里只有一句
//	"Failed to fetch", 界面表现为"连上了但什么都没有".
func TestObservePreflightAllowsAuthorization(t *testing.T) {
	_, base, _ := newObserveFixture(t)
	req, _ := http.NewRequest(http.MethodOptions, base+"/processes", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5310")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	allowed := strings.ToLower(res.Header.Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowed, "authorization") {
		t.Fatalf("预检没放行 authorization: %q", allowed)
	}
}

// TestListIsStablyOrdered 进程表顺序必须稳定.
//
//	Go 的 map 迭代是随机的, 直接遍历返回等于"每次拉都换一个顺序".
//	后端自己看不出问题, 接上界面就是侧栏每几秒重排一次.
func TestListIsStablyOrdered(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("test")
	for i := 0; i < 8; i++ {
		if _, err := o.Spawn(
			abi.ProcessSpec{App: fmt.Sprintf("a%d", i), Name: fmt.Sprintf("n%d", i)},
			InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
				<-ctx.Done()
				return nil, nil
			}},
		); err != nil {
			t.Fatal(err)
		}
	}
	first := order(o)
	for round := 0; round < 40; round++ {
		if got := order(o); got != first {
			t.Fatalf("第 %d 次拉到的顺序变了:\n  %s\n  %s", round, first, got)
		}
	}
}

func order(o *OS) string {
	parts := []string{}
	for _, info := range o.List() {
		parts = append(parts, string(info.PID))
	}
	return strings.Join(parts, ",")
}

// TestForgetSurvivesRestart 删掉就得是真删掉.
//
//	**只清内存是假删**: 账本还在盘上, 下次开机 LoadEvents 一装,
//	那段历史原封不动回来了 —— 用户删了、界面上没了、第二天又出现了.
//	这个闸做的是变异验证: 把账本那一半去掉, 它必须红.
func TestForgetSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	store, err := OpenEventStore(path)
	if err != nil {
		t.Fatal(err)
	}
	o := New(Options{Mode: abi.ModeDev, EventStore: store})
	for i := 0; i < 5; i++ {
		o.Log().Append("keep", abi.EvProcOutput, map[string]any{"n": i})
		o.Log().Append("drop", abi.EvProcOutput, map[string]any{"n": i})
	}

	// 两半都做才叫删掉了
	o.Log().Forget("drop")
	if _, ferr := store.Forget(map[abi.ProcessID]bool{"drop": true}); ferr != nil {
		t.Fatal(ferr)
	}
	if len(o.Log().Replay("drop", 0)) != 0 {
		t.Fatal("内存里还留着")
	}
	store.Close()
	o.Shutdown("test")

	// 重新开机: 删掉的那段**不许复活**
	back, err := LoadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back["drop"]) != 0 {
		t.Fatalf("重启之后复活了 %d 条 —— 这就是假删", len(back["drop"]))
	}
	if len(back["keep"]) != 5 {
		t.Fatalf("误伤了别人的历史: keep 还剩 %d 条", len(back["keep"]))
	}
}

// 话送到死人身上, 宿主把它拉起来再投 —— 不接这条, 用户只能重启客户端.
func TestSayRevivesDeadProcess(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	got := make(chan string, 1)
	srv, err := NewObserveServer(o, ObserveOptions{
		Addr: "127.0.0.1:0", Token: "tok",
		OnRevive: func(dead abi.ProcessID) (abi.ProcessID, error) {
			return o.Spawn(abi.ProcessSpec{App: "chat", Name: "研究"},
				InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
					msg, _ := pc.Recv()
					got <- msg.Text
					return nil, nil
				}})
		},
	})
	if err != nil {
		t.Fatalf("起不来: %v", err)
	}
	srv.Start()
	t.Cleanup(func() { _ = srv.Close(); o.Shutdown("test") })

	dead, err := o.Spawn(abi.ProcessSpec{App: "chat", Name: "研究"},
		InprocBody{Entry: func(context.Context, ProcessContext) (any, error) {
			return "done", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, o, dead, abi.StateExited)

	raw, _ := json.Marshal(map[string]any{"pid": dead, "text": "还在吗"})
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Addr()+"/say", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true {
		t.Fatalf("该拉起来再投, 得到 %+v", body)
	}
	select {
	case text := <-got:
		if text != "还在吗" {
			t.Fatalf("话投偏了: %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("拉起来了但话没到新人手里")
	}
}

// TestForgetKeepsWritingAfterRewrite 重写账本之后还得写得进去.
//
//	rename 换掉的是 inode, 原来那个 fd 指向的是已经没有名字的旧文件 ——
//	不重开的话后面所有写入静默消失, 一点错都不报.
func TestForgetKeepsWritingAfterRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	store, err := OpenEventStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Append(abi.Event{PID: "drop", Seq: 0, Kind: abi.EvProcOutput})
	if _, ferr := store.Forget(map[abi.ProcessID]bool{"drop": true}); ferr != nil {
		t.Fatal(ferr)
	}
	store.Append(abi.Event{PID: "after", Seq: 0, Kind: abi.EvProcOutput})
	store.Close()
	back, _ := LoadEvents(path)
	if len(back["after"]) != 1 {
		t.Fatal("重写之后写入静默消失了 —— fd 指向了旧 inode")
	}
}

// /provider 出门那一趟: 两把 key 摘干净, 而"配没配"那两位得留下.
//
//	这条守的是一个真出过的洞: hideKeys 之前还留着一句 got.APIKey = "",
//	于是 SearchNative 永远算成 false —— 界面上继续催人去配一个第三方
//	搜索, 而这台机器本来就能搜。表现是"少了一行字", 没有任何报错。
func TestProviderHidesKeysButKeepsFlags(t *testing.T) {
	o := New(Options{VolumeRoot: t.TempDir()})
	srv, err := NewObserveServer(o, ObserveOptions{Addr: "127.0.0.1:0", Token: "tok",
		OnProvider: ProviderHooks{Get: func() ProviderConfig {
			return ProviderConfig{
				BaseURL: "https://api.deepseek.com", Model: "m",
				APIKey: "sk-secret", SearchKey: "sk-search",
			}
		}}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.Start()

	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.Addr()+"/provider", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if strings.Contains(string(raw), "sk-secret") || strings.Contains(string(raw), "sk-search") {
		t.Fatalf("key 漏出去了: %s", raw)
	}
	var got ProviderConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.SearchOn {
		t.Error("配了搜索 key, 界面该知道")
	}
	// DeepSeek 自己就会搜(走 Messages 那条口) —— 别再催人配第三方
	if !got.SearchNative {
		t.Error("供应商自带搜索没报出来")
	}
}
