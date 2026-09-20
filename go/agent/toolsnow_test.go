package agent

import (
	"context"
	"testing"
	"time"

	"github.com/neox-os/neox-os/engine"
)

/**
 * 工具表原来只在**进程启动时**定一次.
 *
 *	于是用户在设置里打开"这个模型能看图"之后, 已经在跑的 bot 一辈子都
 *	拿不到 view_image —— 它收到图只会说"我看不了", 而设置页上明明写着
 *	能看. 两边都"对", 只差一次重启, 这种不一致最费劲.
 */
func Test每轮重新问一次工具表(t *testing.T) {
	sys := newFakeSys()
	// 一句话就结束这一轮; Serve 收不到下一句就返回
	rounds := 0
	ag := &Agent{
		ABI:   sys,
		Model: &Scripted{Steps: []Step{{Reply: "好"}}},
		Tools: NewToolSet(nil),
		Box:   Toolbox{Root: t.TempDir()},
		ToolsNow: func() []Tool {
			rounds++
			if rounds == 1 {
				return []Tool{{Name: "read_file"}}
			}
			// 第二轮起多一个 —— 模拟"用户中途在设置里打开了看图"
			return []Tool{{Name: "read_file"}, {Name: "view_image"}}
		},
		Window: NewWindow(engine.NewPageStore(), "嗨", 0),
	}
	_ = ag.Serve("嗨")
	if rounds < 1 {
		t.Fatal("一次都没问 —— 设置改完永远不生效")
	}
	if _, ok := ag.Tools.Get("read_file"); !ok {
		t.Fatal("第一轮的工具表没装上")
	}
}

// 模型那一侧要跟着换: 只换 Agent.Tools 的话, 提示词里还是旧的那份 ——
// 于是模型看不见新工具的说明, 却在工具表里"有"它
func Test工具表换了提示词那侧也要换(t *testing.T) {
	sys := newFakeSys()
	llm := &LLM{ABI: sys, Tools: NewToolSet(nil)}
	ag := &Agent{
		ABI: sys, Model: llm, Tools: NewToolSet(nil), Box: Toolbox{Root: t.TempDir()},
		ToolsNow: func() []Tool { return []Tool{{Name: "view_image"}} },
	}
	_ = ag.Serve("")
	if _, ok := llm.Tools.Get("view_image"); !ok {
		t.Fatal("模型那侧的工具表没跟着换")
	}
	if llm.Tools != ag.Tools {
		t.Fatal("两侧拿的不是同一份 —— 迟早会分叉")
	}
}

/**
 * **工具的取消要从进程自己的 ctx 上派生**.
 *
 *	从 context.Background() 派生会导致进程被杀掉(按停、换房间、
 *	关客户端)时, 取消传不到正在跑的工具里 —— 一条 sleep 90 照样跑完.
 *
 *	结果是按了"停"后, 新进程起来了, 旧的还在 running, 它的 sleep 90
 *	还在系统进程表里 —— 一个人变成两个, 其中一个没人管.
 */
func Test进程被杀时正在跑的工具也要停(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	slow := Tool{
		Name: "慢活", Timeout: time.Minute,
		Run: func(box Toolbox, _ map[string]any) (string, error) {
			close(started)
			select {
			case <-box.Ctx.Done():
				close(stopped)
				return "", box.Ctx.Err()
			case <-time.After(30 * time.Second):
				return "跑完了", nil
			}
		},
	}
	ag := &Agent{ABI: newFakeSys(), Box: Toolbox{Ctx: ctx, Root: t.TempDir()}}
	go func() {
		<-started
		cancel() // 相当于用户按了停
	}()
	if _, err := ag.runWithTimeout(slow, nil); err == nil {
		t.Fatal("进程都被杀了, 工具却说它跑完了")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("取消没传到工具里 —— 它还在跑")
	}
}
