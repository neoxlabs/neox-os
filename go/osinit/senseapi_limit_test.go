package osinit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func limitAPI(t *testing.T) (*SenseAPI, *SignalBus) {
	t.Helper()
	bus := NewSignalBus(nil, func(Digest) {}, SignalOptions{})
	api, err := NewSenseAPI(bus, SenseOptions{Addr: "127.0.0.1:0", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	api.Start()
	t.Cleanup(func() { api.Close() })
	return api, bus
}

func postRaw(t *testing.T, api *SenseAPI, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+api.Addr()+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func batchJSON(n int, pad int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"x%d","source":"phone.mk","kind":"phone.moved",`+
			`"at":1786780000000,"knownAt":1786780000000,"body":{"pad":"%s"}}`,
			i, strings.Repeat("p", pad))
	}
	b.WriteByte(']')
	return b.String()
}

// **一次补发能把宿主撑爆.**
//
// 一个 53M 的请求(手机断网一周之后补 20 万条)把宿主的 RSS
// 从 7MB 顶到 498MB, **而且不降回去** —— 那些信号还全躺在内存日志里.
//
// 而"断网补发是常态"是这套设计**自己写在去重注释里的**预期场景.
// 宿主一死, 所有对话和全部状态跟着走 —— 这是最坏的一种死法.
func TestOversizedBodyIsRejected(t *testing.T) {
	api, bus := limitAPI(t)
	code, out := postRaw(t, api, "/signals", batchJSON(4000, 2000)) // 远超字节上限
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求返回 %d, 该是 413", code)
	}
	if bus.Pending() != 0 {
		t.Fatalf("超大请求还是被收下了 %d 条", bus.Pending())
	}
	// **拒绝必须让采集端能取得进展.**
	//
	// 只说"太大了"的话, 采集端会拿同一批一直重试 —— 补发永远补不上,
	// 而那比直接丢还糟: 它会一直重试, 一直失败, 一直没人知道.
	if out["limit"] == nil || out["hint"] == nil {
		t.Fatalf("拒绝里没告诉采集端怎么拆: %+v", out)
	}
}

// 条数也要有上限 —— 一条一字节的一百万条同样能把内存日志顶满
func TestTooManySignalsInOneBatchIsRejected(t *testing.T) {
	api, bus := limitAPI(t)
	code, out := postRaw(t, api, "/signals", batchJSON(maxBatchSignals+1, 0))
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超量批次返回 %d, 该是 413", code)
	}
	if bus.Pending() != 0 {
		t.Fatal("超量批次被部分收下了 —— 半批进去比整批拒了更难查")
	}
	if out["limit"] == nil {
		t.Fatalf("没告诉采集端一批最多几条: %+v", out)
	}
}

// 正常大小的批次一切照旧 —— 上限不能把真实的补发挡在外面
func TestNormalBatchStillAccepted(t *testing.T) {
	api, bus := limitAPI(t)
	code, out := postRaw(t, api, "/signals", batchJSON(maxBatchSignals, 0))
	if code != 200 {
		t.Fatalf("正常批次返回 %d", code)
	}
	// 判"处理过了", 不判 Pending —— 这批的时间戳是过去的, 会被判成迟到,
	// 迟到的走补传那条路, 本来就不进待处理窗口
	if n, _ := out["n"].(float64); int(n) != maxBatchSignals {
		t.Fatalf("处理了 %v 条, 期望 %d", out["n"], maxBatchSignals)
	}
	counts, _ := out["counts"].(map[string]any)
	if len(counts) == 0 || counts["invalid"] != nil {
		t.Fatalf("正常批次没被正常处理: %+v", counts)
	}
	_ = bus
}

// 单条那个口子同样要挡 —— 它收的是同一个 body
func TestOversizedSingleSignalIsRejected(t *testing.T) {
	api, _ := limitAPI(t)
	big := fmt.Sprintf(`{"id":"x","source":"s","kind":"k","at":1786780000000,`+
		`"knownAt":1786780000000,"body":{"pad":"%s"}}`, strings.Repeat("p", maxBodyBytes+1))
	code, _ := postRaw(t, api, "/signal", big)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大单条返回 %d, 该是 413", code)
	}
}
