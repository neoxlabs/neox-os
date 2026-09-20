// abi-harness — 跨语言对拍用的 Go 服务端.
//
// 起一个 dev 模式 OS + 一个挂着的进程 + ABI 服务, 把 socket 路径和 token
// 用一行 JSON 打到 stdout, 然后一直服务. TS 侧用**真实的 AbiClient** 连上来.
//
// 这证明的是: 两个语言的实现认同一份线格式, 而不是各自跟自己对拍.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

func main() {
	sock := os.Args[1]

	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	started := make(chan struct{})
	pid, err := o.Spawn(
		abi.ProcessSpec{App: "parity", Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/in"}}},
		osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return "done", nil
		}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	<-started
	token, _ := o.TokenFor(pid)

	srv, err := osinit.NewAbiServer(sock, o.ResolveToken, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := srv.Listen(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// 自动解决出现的决策, 标明 by=go-side —— TS 侧据此确认决策真的过了 Go
	go func() {
		for {
			for _, p := range o.Decisions().Pending() {
				o.Decisions().Resolve(p.DID, "go说好", "go-side", map[string]any{"来自": "Go"})
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	out, _ := json.Marshal(map[string]any{"socket": sock, "token": token, "pid": pid})
	fmt.Println(string(out))
	os.Stdout.Sync()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	_ = srv.Close()
	o.Shutdown("bye")
}
