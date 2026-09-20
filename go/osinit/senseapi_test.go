package osinit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func newSense(t *testing.T, o SignalOptions) (*SenseAPI, *busHarness) {
	t.Helper()
	h := newBusHarness(t, o)
	api, err := NewSenseAPI(h.bus, SenseOptions{Addr: "127.0.0.1:0", Token: "tk"})
	if err != nil {
		t.Fatal(err)
	}
	api.Start()
	t.Cleanup(func() { _ = api.Close() })
	return api, h
}

func post(t *testing.T, api *SenseAPI, path, token string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "http://"+api.Addr()+path, bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// **没有 token 就起不来** —— 这个口子收的是位置和来电,
// 不能有"本地开发先不加鉴权"这种开口, 因为它一定会被带上线.
func TestSenseRefusesToStartWithoutToken(t *testing.T) {
	h := newBusHarness(t, SignalOptions{})
	if _, err := NewSenseAPI(h.bus, SenseOptions{Addr: "127.0.0.1:0"}); err == nil {
		t.Fatal("没配 token 也起来了 —— 采集口没有匿名模式")
	}
}

func TestSenseRejectsBadToken(t *testing.T) {
	api, h := newSense(t, SignalOptions{Window: time.Minute})
	code, _ := post(t, api, "/signal", "wrong",
		abi.Signal{ID: "a", Source: "phone.mk", Kind: "location"})
	if code != 401 {
		t.Fatalf("token 不对却返回 %d", code)
	}
	if h.bus.Pending() != 0 {
		t.Fatal("鉴权没过, 信号却进了总线")
	}
}

// 投一条要告诉采集端**它去了哪儿**.
// 静默处理会让手机端以为一切正常, 而实际上它的时钟慢了半小时.
func TestSenseReportsWhereTheSignalWent(t *testing.T) {
	api, h := newSense(t, SignalOptions{Window: time.Hour, Lateness: time.Minute})
	now := h.clk.now().UnixMilli()

	_, out := post(t, api, "/signal", "tk",
		abi.Signal{ID: "a", Source: "phone.mk", Kind: "location", At: now})
	if out["result"] != string(IngestAccepted) {
		t.Fatalf("普通信号该是 accepted, 实际 %v", out["result"])
	}

	// 同一个 ID 重传
	_, out = post(t, api, "/signal", "tk",
		abi.Signal{ID: "a", Source: "phone.mk", Kind: "location", At: now})
	if out["result"] != string(IngestDuplicate) {
		t.Fatalf("重传该是 duplicate, 实际 %v", out["result"])
	}

	// 来电走穿透
	_, out = post(t, api, "/signal", "tk",
		abi.Signal{ID: "b", Source: "phone.mk", Kind: "call.incoming", At: now})
	if out["result"] != string(IngestUrgent) {
		t.Fatalf("来电该是 urgent, 实际 %v", out["result"])
	}
}

// **批量端点不是优化, 是必需.**
//
// 手机离线两小时后一次补传几百条; 一条一个请求 = 几百次握手,
// 中间断一次就得从头对账.
//
// 而且一批里各条的下场可能完全不同 —— 早的判迟到, 晚的正常入窗 ——
// 所以必须逐条返回.
func TestSenseBatchReportsPerSignal(t *testing.T) {
	api, h := newSense(t, SignalOptions{
		Window: time.Minute, Lateness: 10 * time.Second})

	// 先让水位线往前走, 这样补传里早的那几条就会判迟到
	h.bus.Ingest(h.sig("phone.mk", "location", h.clk.now(), nil))
	h.clk.advance(30 * time.Minute)
	h.bus.Tick()

	now := h.clk.now()
	batch := []abi.Signal{
		{ID: "old1", Source: "phone.mk", Kind: "location",
			At: now.Add(-20 * time.Minute).UnixMilli()}, // 迟到
		{ID: "new1", Source: "phone.mk", Kind: "location", At: now.UnixMilli()},
		{ID: "new1", Source: "phone.mk", Kind: "location", At: now.UnixMilli()}, // 重复
		{ID: "bad", Kind: "location", At: now.UnixMilli()},                      // 缺 source
	}
	code, out := post(t, api, "/signals", "tk", batch)
	if code != 200 {
		t.Fatalf("批量投递返回 %d: %v", code, out)
	}
	counts, _ := out["counts"].(map[string]any)
	want := map[string]float64{
		string(IngestLate): 1, string(IngestAccepted): 1,
		string(IngestDuplicate): 1, "invalid": 1,
	}
	for k, v := range want {
		if counts[k] != v {
			t.Fatalf("一批里各条的下场没有分开报: counts=%v, 期望 %s=%v", counts, k, v)
		}
	}
	results, _ := out["results"].([]any)
	if len(results) != 4 {
		t.Fatalf("逐条结果该有 4 条, 实际 %d", len(results))
	}
}

// 缺字段的报错要**说清缺什么、该给什么** ——
// 这条错误信息是写采集端的人唯一的线索
func TestSenseValidationIsActionable(t *testing.T) {
	api, _ := newSense(t, SignalOptions{})
	_, out := post(t, api, "/signal", "tk", abi.Signal{Kind: "location"})
	msg := fmt.Sprint(out["error"])
	for _, want := range []string{"source", "phone.mk"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Fatalf("报错没说清缺什么/该给什么(缺 %q): %s", want, msg)
		}
	}
}

// 秒当成毫秒是这类接口最常见的错, 而且**症状极其隐蔽**:
// 时间戳解出来是 1970 年, 信号会被无声判成迟到, 采集端只看到"收到了".
func TestSenseCatchesSecondsMistakenForMillis(t *testing.T) {
	api, _ := newSense(t, SignalOptions{})
	_, out := post(t, api, "/signal", "tk", abi.Signal{
		Source: "phone.mk", Kind: "location",
		At: time.Now().Unix(), // 秒, 不是毫秒
	})
	if out["error"] == nil {
		t.Fatal("秒级时间戳没被挡下 —— 它会让所有信号无声地判成迟到")
	}
}

// health 不鉴权: 采集端要能分清"网断了"和"token 错了".
// 分不清的话手机端只能盲目重试
func TestSenseHealthNeedsNoToken(t *testing.T) {
	api, _ := newSense(t, SignalOptions{})
	resp, err := http.Get("http://" + api.Addr() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health 需要 token 了(%d) —— 采集端就没法区分网断和鉴权失败",
			resp.StatusCode)
	}
}

// **OS 不解释 body 的形状.**
//
// 一旦开始校验 body, 加一种传感器就要改 OS —— 那条"采集端可换、
// OS 只认一种形状"的边界就白切了.
func TestSenseAcceptsAnyBodyShape(t *testing.T) {
	api, _ := newSense(t, SignalOptions{Window: time.Hour})
	now := time.Now().UnixMilli()
	for i, body := range []map[string]any{
		{"lat": 31.8, "lon": 117.2},
		{"深度嵌套": map[string]any{"a": []any{1, 2, 3}}},
		{"任意中文键": "任意值"},
	} {
		_, out := post(t, api, "/signal", "tk", abi.Signal{
			ID: fmt.Sprintf("s%d", i), Source: "x", Kind: "y", At: now, Body: body})
		if out["result"] != string(IngestAccepted) {
			t.Fatalf("body 形状被拒了: %v", out)
		}
	}
}
