//go:build unix

package agent

import (
	"os/exec"
	"syscall"
)

// setPgid 让子进程自成一个进程组.
//
// 不这么做的话, 超时只能杀掉我们直接起的那个 /bin/sh, 而真正在干活的
// (go test、npm、python) 是 sh 的孩子, 会变成孤儿继续跑 ——
// 占着 cgroup 的配额, 而 agent 已经不再看它了.
//
// "看起来停了其实没停"是最难查的一类问题: 现象出现在别处 (下一条命令
// 莫名其妙内存不够、端口被占), 跟真正的原因隔着好几层.
func setPgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup 杀掉整个进程组.
//
// 负号的 pid 是"这个进程组的所有成员" —— 这是 kill(2) 的语义,
// 也是唯一能把 sh 的孙子辈一起收掉的办法.
//
// 直接上 SIGKILL, 不先 SIGTERM: 这条路径只在**超时**时走,
// 而超时意味着它已经不响应了. 再给它一次优雅退出的机会
// 只是多等一个宽限期, 然后还是得 KILL.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// 组杀失败 (进程组已经没了/权限问题) 就退回杀单个,
		// 总比什么都不杀强
		_ = cmd.Process.Kill()
	}
}

// KillTree 按进程组 id 收掉一整棵进程树.
//
// 给宿主用: 一段对话结束时, 把这段对话里起过的所有后台进程一起收掉 ——
// 那是提示词里对模型许过的话("你起的后台进程活不过这段对话"),
// 而在没有 pid 命名空间的宿主上, 这句话只能靠这一步变成真的.
//
// 已经没了的进程组会返回 ESRCH, 直接忽略 —— 那正是我们想要的状态.
func KillTree(pgid int) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
