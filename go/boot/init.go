// Package boot 是 Neox OS 的 PID 1.
//
// 这是"启动"这件事的落点. 镜像的 ENTRYPOINT 就是它, 容器里它是 1 号进程.
//
// PID 1 的职责跟普通进程不同, 这三件不做会出真事故:
//
//  1. **收僵尸**. PID 1 是所有孤儿进程的父亲. 不 reap 就会攒僵尸, 最后
//     pid 耗尽. TypeScript 版做不到这条 —— Node 没暴露 waitpid, 只能
//     外挂 tini. Go 直接调 wait4, **这是换语言兑现的第一张支票**.
//  2. **转发信号**. docker stop 发 SIGTERM 给 PID 1. 不处理的话默认行为
//     是直接死, 进程来不及落盘. 必须捕获 → 有序关机.
//  3. **启动即自检**. confined 模式下内核不支持强制约束 → **不启动**,
//     而不是启动了再说. 一个自称能隔离而实际没有的 OS 最危险.
//
// 跟 Linux 内核的关系: 我们不写内核. Linux 提供 namespace / cgroup /
// landlock / seccomp 这些**机制**, Neox OS 提供**策略**.
// 这是 Android 跟 Linux 的同一种关系.
package boot

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/confine"
	"github.com/neox-os/neox-os/osinit"
)

type Options struct {
	Mode       abi.Mode
	VolumeRoot string
	// Require 自检要求的强制需求. 缺省是我们真正会施加的那三项
	Require []abi.Requirement
	Log     func(string)
	// InstallSignalHandlers 测试注入: 不真的挂信号处理器
	InstallSignalHandlers bool
	// ReapZombies 是否收僵尸. 只有真的是 PID 1 时才该开
	ReapZombies bool
	Probe       func([]abi.Requirement) abi.EnforcementProbe
}

type Result struct {
	OS       *osinit.OS
	Shutdown func(reason string)
}

var defaultRequire = []abi.Requirement{
	abi.ReqFSEnforce, abi.ReqResourceLimit, abi.ReqNetIsolate,
}

// Boot 开机.
//
// 顺序是刻意的: 自检 → 建 OS → 装信号 → 收僵尸.
// 自检不过就在建 OS 之前返回错误 —— **半启动状态不存在**.
func Boot(opts Options) (*Result, error) {
	log := opts.Log
	if log == nil {
		log = func(s string) { fmt.Fprintln(os.Stdout, s) }
	}
	require := opts.Require
	if require == nil {
		require = defaultRequire
	}
	probe := opts.Probe
	if probe == nil {
		probe = func(r []abi.Requirement) abi.EnforcementProbe {
			return confine.Probe(r, confine.RealDeps())
		}
	}

	log(fmt.Sprintf("[boot] Neox OS 启动 · mode=%s volume=%s", opts.Mode, opts.VolumeRoot))

	// ── 1. 启动自检 ──
	if opts.Mode == abi.ModeConfined {
		p := probe(require)
		if !p.Usable {
			// fail-closed: 不降级到 dev, 不打个 warning 继续. 直接不启动.
			log("[boot] ✗ 内核无法强制约束: " + p.Reason)
			return nil, fmt.Errorf("boot aborted: %s", p.Reason)
		}
		log(fmt.Sprintf("[boot] ✓ 内核自检通过: %v", p.Mechanisms))
	} else {
		log("[boot] ⚠ dev 模式 — 进程不受内核约束, 只能跑 inproc, 不要用于生产")
	}

	// ── 2. 建 OS ──
	o := osinit.New(osinit.Options{Mode: opts.Mode, VolumeRoot: opts.VolumeRoot})
	log("[boot] ✓ OS 就绪 · ABI " + o.ABIVersion())

	// ── 3. 有序关机 ──
	var once sync.Once
	shutdown := func(reason string) {
		once.Do(func() { // 连按两次 Ctrl-C 不该走两遍关机
			log(fmt.Sprintf("[boot] 关机中 (%s) — 停 %d 个进程", reason, len(o.List())))
			o.Shutdown(reason)
			log("[boot] 已关机")
		})
	}

	if opts.InstallSignalHandlers {
		sigc := make(chan os.Signal, 4)
		signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			s := <-sigc
			shutdown(s.String())
			os.Exit(0)
		}()

		// ── 4. 收僵尸 ──
		if opts.ReapZombies {
			go reapLoop(log)
			log("[boot] ✓ 僵尸回收已启动 (wait4)")
		}
	}

	return &Result{OS: o, Shutdown: shutdown}, nil
}

// reapLoop 收僵尸.
//
// PID 1 是孤儿进程的父亲: 任何进程的父进程先死了, 它的子进程就会被
// 重新挂到 PID 1 名下. 不 reap 的话这些进程退出后会一直是僵尸态,
// 占着 pid 表项, 直到 pid 耗尽.
//
// 做法: 监听 SIGCHLD, 每次醒来把所有已退出的子进程收干净.
// 必须用循环而不是收一个就走 —— 信号会合并, 一个 SIGCHLD 可能对应
// 好几个已退出的子进程.
func reapLoop(log func(string)) {
	sigc := make(chan os.Signal, 16)
	signal.Notify(sigc, syscall.SIGCHLD)
	for range sigc {
		for {
			var status syscall.WaitStatus
			// -1 = 任意子进程; WNOHANG = 没有已退出的就立刻返回
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if err != nil || pid <= 0 {
				break // 收干净了 (或没有子进程)
			}
			log(fmt.Sprintf("[boot] 收僵尸 pid=%d status=%d", pid, status.ExitStatus()))
		}
	}
}
