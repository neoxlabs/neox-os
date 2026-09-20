// neox-run — 在 Neox OS 上跑一个 agent, 端到端.
//
// 两种角色, 同一个二进制:
//
//	host  : 起 OS + ABI 服务 → 以 exec 起 agent 进程 (被内核约束)
//	        → 决策打到 stdout, 从 stdin 收回答
//	agent : 通过 ABI 连回 OS, 跑 ReAct 循环, 工具落到真文件系统
//
// 这不是模拟: agent 是**真的 OS 进程**, 被 landlock/cgroup/netns 约束,
// 只能通过 ABI socket 跟 OS 说话.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/confine"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--agent" {
		runAgent()
		return
	}
	runHost()
}

// ── agent 侧 ────────────────────────────────────────────────

func runAgent() {
	cli, err := osinit.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(2)
	}
	defer cli.Close()

	task := envOr("NEOX_TASK", "建一个最小前端工程")
	ts := agent.NewToolSet(agent.DefaultTools())
	// 页存储 + 地址空间: 历史只增不改, 前缀缓存才可能命中
	win := agent.NewWindow(engine.NewPageStore(), task, 0)
	defer win.Release()
	var model agent.Model = &agent.Scripted{Steps: agent.FrontendProject()}
	if os.Getenv("NEOX_MODEL") == "llm" {
		model = &agent.LLM{ABI: cli, Tools: ts, Writable: "site/ 目录"}
	}
	a := &agent.Agent{
		ABI:      cli,
		Model:    model,
		Tools:    ts,
		Box:      agent.Toolbox{Root: os.Getenv("NEOX_WORK")},
		Window:   win,
		MaxSteps: intOr(os.Getenv("NEOX_MAX_STEPS"), 16),
	}
	if err := a.Run(task); err != nil {
		fmt.Fprintln(os.Stderr, "agent 失败:", err)
		os.Exit(1)
	}
}

// ── host 侧 ─────────────────────────────────────────────────

func runHost() {
	work := os.Args[1] // 卷根
	mode := abi.Mode(envOr("NEOX_OS_MODE", string(abi.ModeConfined)))

	runtimeFS := confine.ResolveRuntimeFS(func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	// 供应商凭据只在 OS 手里. **走 env 不落盘, 也不进 agent 的环境**
	var prov engine.Provider
	if key := os.Getenv("NEOX_API_KEY"); key != "" {
		prov = engine.NewProvider(os.Getenv("NEOX_API_PROTOCOL"),
			envOr("NEOX_API_BASE", "https://api.deepseek.com"),
			key, envOr("NEOX_MODEL_ID", "deepseek-v4-flash"), false, nil)
	}

	o := osinit.New(osinit.Options{
		Mode: mode, VolumeRoot: work, RuntimeFS: runtimeFS,
		Spawner: hostSpawner, Provider: prov,
	})

	// 决策一登记就打出来 —— 真实现里这里是推手机
	stop := o.Log().SubscribeAll(func(e abi.Event) {
		m, _ := e.Payload.(map[string]any)
		switch e.Kind {
		case abi.EvDecideRequest:
			pres, _ := m["present"].(abi.PresentSpec)
			fmt.Printf("ASK %v | %s\n", m["did"], pres.Title)
		case abi.EvProcOutput:
			b, _ := json.Marshal(m)
			fmt.Println("OUT " + string(b))
		case abi.EvProcOutcome:
			b, _ := json.Marshal(e.Payload)
			fmt.Println("OUTCOME " + string(b))
		}
		os.Stdout.Sync()
	})
	defer stop()

	// 从 stdin 收回答 —— 真实现里这里是手机上点一下
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 2 && o.Decisions().Resolve(f[0], f[1], "phone:刘", nil) {
				fmt.Printf("ANSWERED %s → %s\n", f[0], f[1])
				os.Stdout.Sync()
			}
		}
	}()

	self, _ := os.Executable()
	// 能力集: 只给 site 目录的读写. 脚本里那步写 /etc 就会越界 → 问人
	pid, err := o.Spawn(abi.ProcessSpec{
		App: "frontend-builder", Name: "建前端工程",
		Caps: []abi.Capability{
			{Axis: abi.AxisWrite, Scope: "/site"},
			{Axis: abi.AxisRead, Scope: "/"},
		},
		Budget: &abi.Budget{Tokens: p64(100000)},
	}, osinit.ExecBody{
		Argv: []string{self, "--agent"},
		Cwd:  work,
		// 注意这里**没有** NEOX_API_KEY —— 凭据不进被约束的进程.
		// agent 要推理就走 ABI, 由 OS 代它去调.
		Env: map[string]string{
			"NEOX_WORK":      work,
			"NEOX_MODEL":     os.Getenv("NEOX_MODEL"),
			"NEOX_TASK":      os.Getenv("NEOX_TASK"),
			"NEOX_MAX_STEPS": os.Getenv("NEOX_MAX_STEPS"),
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "起进程失败:", err)
		os.Exit(1)
	}

	// 等进程进终态. 轮询而不是 select{} —— 后者会永久阻塞
	for {
		info, ok := o.Info(pid)
		if !ok {
			break
		}
		if info.State.IsTerminal() {
			fmt.Printf("STATE %s\n", info.State)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // 让最后几条事件打完
}

// hostSpawner 起被约束的 agent 进程, 并把 ABI 凭据注入 env.
//
// token 必须走 env 而不是命令行 —— 命令行在 /proc 里对同机进程可见.
func hostSpawner(argv []string, cwd string, env map[string]string) (osinit.Child, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	var envv []string
	for k, v := range env {
		envv = append(envv, k+"="+v)
	}
	cmd.Env = envv
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &hostChild{cmd: cmd, out: stdout}, nil
}

type hostChild struct {
	cmd *exec.Cmd
	out interface{ Read([]byte) (int, error) }
}

func (c *hostChild) OnLine(fn func(string)) {
	go func() {
		sc := bufio.NewScanner(c.out.(interface {
			Read([]byte) (int, error)
		}))
		for sc.Scan() {
			fn(sc.Text())
		}
	}()
}
func (c *hostChild) Wait() (int, string, error) {
	err := c.cmd.Wait()
	if err == nil {
		return 0, "", nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), "", nil
	}
	return -1, "", err
}
func (c *hostChild) Kill(string) {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func p64(v int64) *int64 { return &v }

func intOr(s string, d int) int {
	if s == "" {
		return d
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return d
	}
	return n
}

var _ = filepath.Join
var _ = context.Background
