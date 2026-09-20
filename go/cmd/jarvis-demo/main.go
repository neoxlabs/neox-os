// jarvis-demo — 完整闭环: 内核冻结 → 策略 → 问人 → 醒来继续.
//
//	内核冻结 openat
//	  → confine.Supervisor 收到通知
//	    → osinit.Broker 查策略
//	      ├─ 能力集内 / 记住过 → 微秒级放行
//	      └─ 否则 → ctx.Decide() 登记待决策
//	                 → 打到 stdout (真实现里是推手机)
//	                 → 从 stdin 读回答 (真实现里是手机上点一下)
//	                   → 进程醒来, syscall 继续
//
// 进程在等人的整段时间里**冻在内核里**, 一个指令都不执行, 不占 CPU.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/confine"
	"github.com/neox-os/neox-os/osinit"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--stub" {
		fd, err := confine.InstallSelfFilter()
		if err != nil {
			die(err)
		}
		if err := confine.SendFD(3, fd); err != nil {
			die(err)
		}
		argv := os.Args[2:]
		die(syscall.Exec(argv[0], argv, os.Environ()))
		return
	}

	level := osinit.Autonomy(os.Args[1]) // autonomous | guarded | paranoid
	capScope := os.Args[2]               // 能力集: read:<这个前缀>
	target := os.Args[3:]

	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	policy := osinit.NewPolicyStore(nil)
	policy.SetAutonomy("jarvis", level)
	broker := osinit.NewBroker(o, policy)

	// 用一个挂着的进程代表"agent 本体"; 被约束的目标程序是它的工具调用
	started := make(chan struct{})
	pid, err := o.Spawn(
		abi.ProcessSpec{App: "jarvis", Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: capScope}}},
		osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
			close(started)
			<-ctx.Done()
			return "done", nil
		}})
	if err != nil {
		die(err)
	}
	<-started

	// 待决策一登记就打出来 —— 真实现里这里是推送到手机
	go func() {
		stop := o.Log().SubscribeAll(func(e abi.Event) {
			if e.Kind != abi.EvDecideRequest {
				return
			}
			m, _ := e.Payload.(map[string]any)
			pres, _ := m["present"].(abi.PresentSpec)
			fmt.Printf("ASK %v | %s\n", m["did"], pres.Title)
			os.Stdout.Sync()
		})
		defer stop()
		select {}
	}()

	// 从 stdin 读回答 —— 真实现里这里是手机上点一下
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			parts := strings.Fields(sc.Text())
			if len(parts) != 2 {
				continue
			}
			if o.Decisions().Resolve(parts[0], parts[1], "phone:刘", nil) {
				fmt.Printf("ANSWERED %s → %s\n", parts[0], parts[1])
				os.Stdout.Sync()
			}
		}
	}()

	// 起被约束的目标
	sp, _ := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	self, _ := os.Executable()
	cmd := exec.Command(self, append([]string{"--stub"}, target...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{os.NewFile(uintptr(sp[1]), "sock")}
	if err := cmd.Start(); err != nil {
		die(err)
	}
	syscall.Close(sp[1])

	listener, err := confine.RecvFD(sp[0])
	if err != nil {
		die(err)
	}

	// 执行底座必须放行 —— 否则连 libc 都打不开.
	// 这是所有强制机制的共同前置, 不是某一种机制的特例.
	base := []string{"/usr/", "/lib/", "/lib64/", "/etc/", "/proc/", "/sys/", "/dev/"}
	isBase := func(p string) bool {
		for _, b := range base {
			if strings.HasPrefix(p, b) {
				return true
			}
		}
		return false
	}

	sup := confine.NewSupervisor(listener, func(r confine.Request) confine.Decision {
		if isBase(r.Path) {
			return confine.DecisionAllow
		}
		v := broker.Decide(osinit.SyscallRequest{
			PID: pid, Call: r.Call, Axis: r.Axis, Path: r.Path})
		fmt.Printf("VERDICT %s(%s) → %s\n", r.Path, r.Axis, v)
		os.Stdout.Sync()
		if v == osinit.VerdictAllow {
			return confine.DecisionAllow
		}
		return confine.DecisionDeny
	})

	done := make(chan struct{})
	go func() { _ = sup.Serve(); close(done) }()
	_ = cmd.Wait()
	_ = sup.Close()
	<-done
	fmt.Printf("DONE exit=%d\n", cmd.ProcessState.ExitCode())
	o.Shutdown("bye")
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
