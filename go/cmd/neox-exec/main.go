// neox-exec — 把**任意一个程序**当成 OS 进程跑起来.
//
// ── 为什么必须有它 ──
//
// 在这之前, 两个入口 (neox-run / neox-chat) 起的都是**它们自己**
// (`self --agent`). 那说明这个 OS 只托管得了"我们自己写的那个 Go agent" ——
// 那不是操作系统, 是一个自带沙盒的应用.
//
// **一个 OS 的定义性能力是: 它能跑别人写的程序.**
//
// 这个入口收下 argv 就起, 不关心它是 Go 还是 Node 还是 Python:
// 进程只要会说 ABI (4 字节大端长度 + JSON 一帧), 就能拿到能力、
// 请求授权、让 OS 代调推理 —— 进程自身既没有 key, 也无法越过笼子.
//
//	neox-exec --work /agentwork --write /site -- node agent.mjs
//
// 决策打到 stdout, 回答从 stdin 收 (`<did> <选项>`), 跟 neox-run 一致.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/confine"
	"github.com/neox-os/neox-os/engine"
	"github.com/neox-os/neox-os/osinit"
)

func main() {
	cfg := parseArgs(os.Args[1:])
	if len(cfg.argv) == 0 {
		fmt.Fprintln(os.Stderr,
			"用法: neox-exec [--work 目录] [--read 范围] [--write 范围] [--net 主机] [--proc] -- 程序 参数…")
		os.Exit(2)
	}
	_ = os.MkdirAll(cfg.work, 0o755)

	// 推理是 OS 的服务 —— key 只在这里, 进程拿不到
	var prov engine.Provider
	if key := os.Getenv("NEOX_API_KEY"); key != "" {
		prov = engine.NewProvider(os.Getenv("NEOX_API_PROTOCOL"),
			envOr("NEOX_API_BASE", "https://api.deepseek.com"), key,
			envOr("NEOX_MODEL_ID", "deepseek-v4-flash"), false, nil)
	}

	o := osinit.New(osinit.Options{
		VolumeRoot: cfg.work,
		Provider:   prov,
		RuntimeFS: confine.ResolveRuntimeFS(func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		}),
	})

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
		_ = os.Stdout.Sync()
	})
	defer stop()

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 2 && o.Decisions().Resolve(f[0], f[1], "stdin", nil) {
				fmt.Printf("ANSWERED %s → %s\n", f[0], f[1])
				_ = os.Stdout.Sync()
			}
		}
	}()

	pid, err := o.Spawn(abi.ProcessSpec{
		App: "exec", Name: cfg.argv[0],
		Caps:   cfg.caps,
		Budget: &abi.Budget{Tokens: p64(int64(cfg.budget))},
	}, osinit.ExecBody{
		Argv: cfg.argv,
		Cwd:  cfg.work,
		// **没有 NEOX_API_KEY** —— 凭据不进被约束的进程.
		// 要推理就走 ABI, 由 OS 代它去调.
		Env: cfg.env,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "起进程失败:", err)
		os.Exit(1)
	}
	fmt.Printf("SPAWNED %s\n", pid)

	for {
		info, ok := o.Info(pid)
		if !ok {
			break
		}
		if info.State.IsTerminal() {
			fmt.Printf("STATE %s\n", info.State)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type config struct {
	work   string
	caps   []abi.Capability
	argv   []string
	env    map[string]string
	budget int
}

// parseArgs 手写而不是用 flag 包: `--` 之后的东西要**原样**交给目标程序,
// flag 包会去解析它们.
func parseArgs(args []string) config {
	c := config{
		work: envOr("NEOX_WORK_ROOT", "/agentwork"),
		env:  map[string]string{},
		// 缺省什么能力都不给 —— 缺省必须是安全的那个
		budget: 2000000,
	}
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--":
			c.argv = append(c.argv, args[i+1:]...)
			return c
		case "--work":
			c.work = next()
		case "--read":
			c.caps = append(c.caps, abi.Capability{Axis: abi.AxisRead, Scope: next()})
		case "--write":
			c.caps = append(c.caps, abi.Capability{Axis: abi.AxisWrite, Scope: next()})
		case "--net":
			c.caps = append(c.caps, abi.Capability{Axis: abi.AxisNet, Scope: next()})
		case "--proc":
			c.caps = append(c.caps, abi.Capability{Axis: abi.AxisProc, Scope: "*"})
		case "--budget":
			if n, err := strconv.Atoi(next()); err == nil {
				c.budget = n
			}
		case "--env":
			if kv := strings.SplitN(next(), "=", 2); len(kv) == 2 {
				c.env[kv[0]] = kv[1]
			}
		}
	}
	return c
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func p64(v int64) *int64 { return &v }
