package agent

import (
	"github.com/neox-os/neox-os/abi"
	"testing"
	"time"
)

// 等待人工决策的工具不设时限.
//
// 时间闸砍的是"机器不响应", 不是"人还没回答". 混了就砍掉了这个 OS
// 最核心的那条性质: 进程可以等人几小时而不占计算.
//
// 决策弹出来、还没得到回答时, 30 秒通用时限不能把工具
// 砍了, 而决策还挂在那儿 —— agent 当它失败, 接着去瞎试别的.
func TestHumanWaitingToolsHaveNoDeadline(t *testing.T) {
	for _, tl := range DefaultTools() {
		if tl.Name == "request_access" {
			if !tl.WaitsForHuman {
				t.Fatal("request_access 要等人回答, 必须声明 WaitsForHuman, 否则会被时限砍掉")
			}
			return
		}
	}
	t.Fatal("工具表里没有 request_access —— 撞了墙 agent 没有任何办法自救")
}

// 反过来: 会真跑东西的工具**必须**有时限, 否则一次卡死能挂住整轮
func TestExecutingToolsKeepADeadline(t *testing.T) {
	for _, tl := range DefaultTools() {
		if tl.WaitsForHuman {
			continue
		}
		if tl.Name == "run" && tl.Timeout <= time.Minute {
			t.Fatalf("run 的时限 %v 太短, 一次 go test 就会被砍", tl.Timeout)
		}
	}
}

// TestResolveScopeMakesPathsAbsolute 预检要看的 scope 必须跟工具真正
// 会去动的东西是同一个.
//
//	相对路径直接拿去比能力集(绝对路径)一比一个不中, 表现为
//	"授了整个工作目录还是每写一个文件都要问一次人".
func TestResolveScopeMakesPathsAbsolute(t *testing.T) {
	a := &Agent{Box: Toolbox{Root: "/work"}}
	write := Tool{Name: "write_file", Needs: abi.AxisWrite, ScopeArg: "path"}
	if got := a.resolveScope(write, map[string]any{"path": "hello.py"}); got != "/work/hello.py" {
		t.Fatalf("相对路径没解成绝对: %q", got)
	}
	if got := a.resolveScope(write, map[string]any{"path": "/etc/hosts"}); got != "/etc/hosts" {
		t.Fatalf("绝对路径不该被改: %q", got)
	}
	// 命令行和主机名照原样 —— 它们不是路径
	run := Tool{Name: "run", Needs: abi.AxisProc, ScopeArg: "cmd"}
	if got := a.resolveScope(run, map[string]any{"cmd": "ls -la"}); got != "ls -la" {
		t.Fatalf("命令行被当成路径解了: %q", got)
	}
}
