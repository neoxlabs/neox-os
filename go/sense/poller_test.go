package sense

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func sigs(n int) []abi.Signal {
	out := make([]abi.Signal, n)
	for i := range out {
		out[i] = abi.Signal{ID: string(rune('a' + i%26))}
	}
	return out
}

// 投不出去时**要缓冲**: HA 的 last_changed 只保留最新一次,
// 这里丢了就永久没有了 —— 下一轮比出来是"没变化"(状态已经稳定),
// 谁也不知道中间开过门.
func TestBacklogKeepsWhatCouldNotBeDelivered(t *testing.T) {
	b := NewBacklog(100)
	b.Add(sigs(5))
	if b.Len() != 5 {
		t.Fatalf("缓冲里该有 5 条, 实际 %d", b.Len())
	}
	got := b.Take()
	if len(got) != 5 || b.Len() != 0 {
		t.Fatal("取走之后缓冲该空")
	}
}

// **有界, 满了丢最老的, 而且丢这件事要能被上报.**
//
// 无限缓冲的话, 采集器跟 OS 断开一天会吃光内存, 而且恢复时一次性
// 灌进去一天的信号 —— 那比丢掉更糟, 因为它们全是迟到的, 会把窗口冲垮.
func TestBacklogIsBoundedAndReportsDrops(t *testing.T) {
	b := NewBacklog(10)
	if d := b.Add(sigs(8)); d != 0 {
		t.Fatalf("没满却报丢了 %d", d)
	}
	d := b.Add(sigs(7)) // 8+7=15, 上限 10
	if d != 5 {
		t.Fatalf("该丢 5 条, 实际报丢 %d", d)
	}
	if b.Len() != 10 {
		t.Fatalf("缓冲该被压到上限 10, 实际 %d", b.Len())
	}
	if b.TotalDropped() != 5 {
		t.Fatal("累计丢弃数没记 —— 静默丢弃会让'为什么那段时间它什么都不知道'查不出来")
	}
}

// **积压超过一批的上限时, 采集端必须自己拆.**
//
// OS 侧给 /signals 加了条数上限(一批 1000), 理由是一次 53M 的补发
// 能把宿主的 RSS 从 7MB 顶到 498MB. 但上限一加, **我们自己的采集端
// 就撞上了它写在注释里的那条**: 拒绝必须让采集端能取得进展 ——
// 一次全发的话, 攒了两千条的手机会永远 413, 补发永远补不上,
// 而它自己看到的只是"投递失败", 一直重试, 一直没人知道.
func TestPostSplitsLargeBacklog(t *testing.T) {
	var batches []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sigs []abi.Signal
		json.NewDecoder(r.Body).Decode(&sigs)
		if len(sigs) > 1000 { // OS 侧的上限
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(map[string]any{"error": "一批信号条数超过上限"})
			return
		}
		batches = append(batches, len(sigs))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"n": len(sigs), "counts": map[string]int{"accepted": len(sigs)},
		})
	}))
	defer srv.Close()

	sigs := make([]abi.Signal, 2500)
	for i := range sigs {
		sigs[i] = abi.Signal{ID: fmt.Sprintf("s%d", i), Source: "phone.mk",
			Kind: "phone.moved", At: 1786780000000, KnownAt: 1786780000000}
	}
	counts, err := Sink{URL: srv.URL, Token: "t"}.Post(sigs)
	if err != nil {
		t.Fatalf("两千五百条的积压投不出去: %v", err)
	}
	if counts["accepted"] != 2500 {
		t.Fatalf("只投进去 %d 条, 该是 2500 —— 拆了但丢了一部分", counts["accepted"])
	}
	if len(batches) < 3 {
		t.Fatalf("只发了 %d 批 —— 没拆", len(batches))
	}
	for _, n := range batches {
		if n > 1000 {
			t.Fatalf("有一批 %d 条, 超过 OS 的上限", n)
		}
	}
}
