// unotify-demo — 把人类决策接到系统调用上的最小证明.
//
// 两种角色, 同一个二进制:
//
//	stub  : 给自己装 seccomp 过滤器 → 把监听 fd 交给监督者 → exec 目标程序.
//	        跟 landlock-run 一样是"自缚后 exec", 过滤器跨 execve 继承,
//	        所以目标再 fork 出什么也逃不掉.
//	监督者: 收被冻结的 openat, 交给裁决函数, 把结果送回内核.
//
// 裁决函数这里用一个简单的路径白名单代替"问人" —— 真实现里它会走
// ctx.Decide() 登记一条待决策, 推到手机上, 人点一下再回来.
// 关键在于**内核已经把进程冻在那儿了**, 等多久都行.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/neox-os/neox-os/confine"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--stub" {
		runStub()
		return
	}
	runSupervisor()
}

// runStub: 自缚 → 交 fd → exec
func runStub() {
	// fd 3 是父进程通过 ExtraFiles 传进来的 socket
	fd, err := confine.InstallSelfFilter()
	if err != nil {
		fmt.Fprintln(os.Stderr, "装过滤器失败:", err)
		os.Exit(2)
	}
	if err := confine.SendFD(3, fd); err != nil {
		fmt.Fprintln(os.Stderr, "交 fd 失败:", err)
		os.Exit(2)
	}
	// 装没装成必须看得见.
	//
	// 不打这一行的时候, 我看到"读 /etc/hostname 时 intercepted=0"
	// 只能猜: 是 filter 没装上, 还是 supervisor 没收到?
	// 两者的含义天差地别 —— 后者意味着**约束静默消失**(fail-open).
	fmt.Fprintf(os.Stderr, "[stub] 过滤器已装, 监听 fd 已交出\n")
	argv := os.Args[2:]
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "exec 失败:", err)
		os.Exit(2)
	}
}

func runSupervisor() {
	// 参数不对就说清用法, 不要下标越界 panic ——
	// 一个入口程序上来就 panic, 看的人只能去读源码才知道该传什么.
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr,
			"用法: %s <允许读的路径前缀> <要跑的程序> [参数...]\n"+
				"例:  %s /etc /bin/cat /etc/hostname\n"+
				"作用: 目标程序的 openat 会被内核冻住, 交给这里裁决, "+
				"路径不在前缀内就拒绝.\n",
			os.Args[0], os.Args[0])
		os.Exit(2)
	}
	allowPrefix := os.Args[1] // 允许读的路径前缀
	target := os.Args[2:]

	sp, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		die(err)
	}
	parent, child := sp[0], sp[1]

	self, _ := os.Executable()
	cmd := exec.Command(self, append([]string{"--stub"}, target...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{os.NewFile(uintptr(child), "sock")} // → 子进程的 fd 3
	if err := cmd.Start(); err != nil {
		die(err)
	}
	syscall.Close(child)

	listener, err := confine.RecvFD(parent)
	if err != nil {
		die(fmt.Errorf("没收到监听 fd: %w", err))
	}

	type auditRec struct {
		Call     string `json:"call"`
		Path     string `json:"path"`
		Decision string `json:"decision"`
	}
	var audit []auditRec

	// 执行底座: 加载器、动态库、locale 这些必须放行, 否则程序连
	// libc.so.6 都打不开, 根本起不来.
	// **这跟 landlock 那次踩的是同一个坑** —— 说明"执行底座"是所有
	// 强制机制的共同前置, 不是某一种机制的特例.
	runtimeBase := []string{"/usr/", "/lib/", "/lib64/", "/etc/", "/proc/", "/sys/", "/dev/"}
	isBase := func(p string) bool {
		for _, b := range runtimeBase {
			if strings.HasPrefix(p, b) {
				return true
			}
		}
		return false
	}

	// NEOX_DEMO_DELAY_MS 模拟"人在手机上想了多久".
	// 内核已经把进程冻住了, 这段时间进程一个指令都不会执行.
	delay, _ := strconv.Atoi(os.Getenv("NEOX_DEMO_DELAY_MS"))

	var intercepted int
	var totalNs int64

	sup := confine.NewSupervisor(listener, func(r confine.Request) confine.Decision {
		if isBase(r.Path) {
			return confine.DecisionAllow
		}
		t0 := time.Now()
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		intercepted++
		totalNs += time.Since(t0).Nanoseconds()
		// 真实现里这里是 ctx.Decide() —— 登记待决策, 推手机, 等人回.
		// 内核已经把进程冻住了, 等一小时也没关系.
		if strings.HasPrefix(r.Path, allowPrefix) {
			return confine.DecisionAllow
		}
		return confine.DecisionDeny
	})
	sup.OnEvent = func(r confine.Request, d confine.Decision) {
		if isBase(r.Path) {
			return // 底座不入审计, 否则会被几百条动态库淹没
		}
		s := "allow"
		if d == confine.DecisionDeny {
			s = "deny"
		}
		audit = append(audit, auditRec{Call: r.Call, Path: r.Path, Decision: s})
	}

	done := make(chan struct{})
	go func() { _ = sup.Serve(); close(done) }()

	err = cmd.Wait()
	_ = sup.Close()
	<-done

	avgUs := 0.0
	if intercepted > 0 {
		avgUs = float64(totalNs) / float64(intercepted) / 1000
	}
	out, _ := json.Marshal(map[string]any{
		"audit":       audit,
		"exitCode":    cmd.ProcessState.ExitCode(),
		"intercepted": intercepted,
		"avgDecideUs": avgUs,
	})
	fmt.Println("AUDIT " + string(out))
	if err != nil {
		os.Exit(cmd.ProcessState.ExitCode())
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
