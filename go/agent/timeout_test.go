package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/engine"
)

// slowTool 一个永远不回来的工具, 模拟卡死的 I/O
func slowTool(honorCtx bool) Tool {
	return Tool{
		Name: "slow_probe", Desc: "卡住不动",
		Args: map[string]string{"path": "随便"}, ArgOrder: []string{"path"},
		Optional: map[string]bool{"path": true},
		Run: func(t Toolbox, a map[string]any) (string, error) {
			if honorCtx && t.Ctx != nil {
				<-t.Ctx.Done()
				return "", t.Ctx.Err()
			}
			time.Sleep(10 * time.Second)
			return "永远等不到", nil
		},
	}
}

func timeoutAgent(t *testing.T, sys *fakeSys, steps []Step, tools []Tool) *Agent {
	t.Helper()
	return &Agent{
		ABI: sys, Model: &Scripted{Steps: steps},
		Tools: NewToolSet(tools), Box: Toolbox{Root: t.TempDir()},
		Window:   NewWindow(engine.NewPageStore(), "t", 0),
		MaxSteps: 6, ToolTimeout: 80 * time.Millisecond,
	}
}

// 卡死的工具不许把整轮拖住 —— 现在一个工具卡住就永远卡住, 没有任何兜底
func TestStuckToolTimesOut(t *testing.T) {
	sys := newFakeSys()
	ag := timeoutAgent(t, sys, []Step{
		{Tool: "slow_probe", Args: map[string]any{"path": "x"}},
		{Reply: "那个工具卡住了, 已告知用户"},
	}, append(DefaultTools(), slowTool(false)))

	start := time.Now()
	if err := ag.Run("试试"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("卡了 %v —— 超时闸没生效", d)
	}
	if !sys.saw("tool_err") {
		t.Fatalf("超时没报成工具错误: %v", sys.phases())
	}
}

// 超时的错误要写成"下一步该怎么办", 不是一句 context deadline exceeded
func TestTimeoutErrorIsActionable(t *testing.T) {
	sys := newFakeSys()
	ag := timeoutAgent(t, sys, []Step{
		{Tool: "slow_probe", Args: map[string]any{"path": "x"}},
		{Reply: "好"},
	}, append(DefaultTools(), slowTool(false)))
	ag.Run("试试")

	e := sys.last("tool_err")
	msg := e["err"].(string)
	if strings.Contains(msg, "deadline exceeded") {
		t.Fatalf("把 Go 的原话丢给模型了: %s", msg)
	}
	if !strings.Contains(msg, "缩小范围") || !strings.Contains(msg, "不要原样重来") {
		t.Fatalf("没告诉它下一步怎么办: %s", msg)
	}
	// **不许替它猜原因.** 这句话原来一律说"可能路径指向一个很大的目录树",
	// 套到非文件工具上就是纯误导 —— request_access 超时时它照样这么说,
	// 而真实原因是"人还没回答".
	if strings.Contains(msg, "目录树") || strings.Contains(msg, "挂载点") {
		t.Fatalf("替工具猜了原因, 对非文件类工具是误导: %s", msg)
	}
}

// 超时之后对话要能继续 —— 一次工具卡住不该让整轮死掉
func TestRunContinuesAfterTimeout(t *testing.T) {
	sys := newFakeSys()
	ag := timeoutAgent(t, sys, []Step{
		{Tool: "slow_probe", Args: map[string]any{"path": "x"}},
		{Tool: "list_dir", Args: map[string]any{"path": "."}},
		{Done: "换了个办法, 成了"},
	}, append(DefaultTools(), slowTool(false)))
	if err := ag.Run("试试"); err != nil {
		t.Fatal(err)
	}
	if !sys.saw("done") {
		t.Fatalf("超时之后没能继续: %v", sys.phases())
	}
}

// 我们自己写的循环要真的能被取消 —— 不是只放弃等待
func TestOurOwnLoopsHonorCancellation(t *testing.T) {
	sys := newFakeSys()
	ag := timeoutAgent(t, sys, []Step{
		{Tool: "slow_probe", Args: map[string]any{"path": "x"}},
		{Reply: "好"},
	}, append(DefaultTools(), slowTool(true))) // 这个工具会听 Ctx
	start := time.Now()
	ag.Run("试试")
	if d := time.Since(start); d > time.Second {
		t.Fatalf("听 Ctx 的工具也拖了 %v", d)
	}
}

// search 走到一半被取消: 把已经找到的交出去, 并说清没走完.
// 直接丢掉等于白跑, 而部分结果对模型往往已经够用.
func TestCanceledSearchReturnsPartialResults(t *testing.T) {
	d := t.TempDir()
	for i := 0; i < 50; i++ {
		os.WriteFile(filepath.Join(d, "f"+strings.Repeat("x", i)+".txt"),
			[]byte("命中 HIT\n"), 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一开始就取消
	hits, _, truncated, err := runSearch(Toolbox{Ctx: ctx}, d, "site", regexp.MustCompile("HIT"))
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("取消了却没说没走完 —— 模型会把部分结果当全部")
	}
	_ = hits
}

// 缺省超时要够宽 —— 定太紧会把正常的大目录搜索砍掉
func TestDefaultTimeoutIsGenerous(t *testing.T) {
	if defaultToolTimeout < 10*time.Second {
		t.Fatalf("缺省超时 %v 太紧, 正常的大目录搜索会被误杀", defaultToolTimeout)
	}
}
