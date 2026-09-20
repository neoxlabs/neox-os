package sense

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func lamps() []HAState {
	return []HAState{
		{EntityID: "light.living", State: "off",
			Attributes: map[string]any{"friendly_name": "客厅灯"}},
		{EntityID: "light.bed", State: "off",
			Attributes: map[string]any{"friendly_name": "卧室灯"}},
		{EntityID: "lock.front", State: "locked",
			Attributes: map[string]any{"friendly_name": "大门锁"}},
	}
}

// **锁不归它调** —— 开灯错了再关掉, 开锁错了是另一回事
func TestActRefusesLocks(t *testing.T) {
	_, err := resolveAct(map[string]any{"act": "off", "name": "大门锁"}, lamps())
	if err == nil || !strings.Contains(err.Error(), "自己按") {
		t.Fatalf("锁竟然放行了: %v", err)
	}
	if _, err := resolveAct(map[string]any{"act": "on", "entity": "lock.front"}, lamps()); err == nil {
		t.Fatal("按实体 id 绕过了白名单")
	}
}

// 名字对不上唯一一个就不做 —— 挑一个执行比什么都不做糟
func TestActNeedsAUniqueMatch(t *testing.T) {
	if _, err := resolveAct(map[string]any{"act": "on", "name": "灯"}, lamps()); err == nil ||
		!strings.Contains(err.Error(), "说准一点") {
		t.Fatalf("两盏灯都叫灯, 它还是挑了一个: %v", err)
	}
	if _, err := resolveAct(map[string]any{"act": "on", "name": "洗碗机"}, lamps()); err == nil {
		t.Fatal("家里没有的东西也照做")
	}
	got, err := resolveAct(map[string]any{"act": "关", "name": "客厅灯"}, lamps())
	if err != nil {
		t.Fatal(err)
	}
	if got.Entity != "light.living" || got.Service != "turn_off" || got.Domain != "light" {
		t.Fatalf("%+v", got)
	}
}

// 听流: 只认冲着 ha 来的吆喝, 注释行和别的事件一概跳过
func TestListenOnlyRunsHomeActs(t *testing.T) {
	var mu sync.Mutex
	var done []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0ken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		// 注释(补齐完了/心跳)、别人的事件、冲着手机去的吆喝 —— 都不该执行
		w.Write([]byte(": live\n\n: beat\n\n"))
		w.Write([]byte(`data: {"kind":"signal.in","payload":{"kind":"location"}}` + "\n\n"))
		w.Write([]byte(`data: {"kind":"device.ask","payload":{"device":"phone-1","what":"location"}}` + "\n\n"))
		w.Write([]byte(`data: {"kind":"device.act","payload":{"device":"ha","act":"on","name":"客厅灯"}}` + "\n\n"))
		f.Flush()
		time.Sleep(150 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go Listen(ctx, Sink{URL: srv.URL, Token: "t0ken", HTTP: srv.Client()},
		lamps,
		func(a ActService) error {
			mu.Lock()
			done = append(done, a.Domain+"."+a.Service+" "+a.Entity)
			mu.Unlock()
			return nil
		}, nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(done)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(done) != 1 || done[0] != "light.turn_on light.living" {
		t.Fatalf("执行的是 %v", done)
	}
	_ = abi.EvDeviceAct
}
