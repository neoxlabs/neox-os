// neox-init — 镜像的 ENTRYPOINT. 容器里这是 PID 1.
//
// 只做一件事: 读 env → Boot(). 任何"启动前的小聪明"都不该加在这里,
// 否则自检就不是第一件事了.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/boot"
)

func main() {
	mode := abi.Mode(envOr("NEOX_OS_MODE", string(abi.ModeConfined)))
	volume := envOr("NEOX_OS_VOLUME", "/work")

	res, err := boot.Boot(boot.Options{
		Mode:                  mode,
		VolumeRoot:            volume,
		InstallSignalHandlers: true,
		// 只有真的是 PID 1 时才收僵尸 —— 不是 1 号进程时收别人的孩子是越权
		ReapZombies: os.Getpid() == 1,
	})
	if err != nil {
		// 自检失败就是启动失败. 非 0 退出让编排层看得见,
		// 而不是起一个残废的 OS.
		fmt.Fprintf(os.Stderr, "[boot] 启动失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[boot] PID=%d 收僵尸=%v\n", os.Getpid(), os.Getpid() == 1)

	// 常驻. 真正的退出走信号 → Boot 里装的 handler
	block := make(chan os.Signal, 1)
	signal.Notify(block, syscall.SIGTERM, syscall.SIGINT)
	<-block
	res.Shutdown("signal")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
