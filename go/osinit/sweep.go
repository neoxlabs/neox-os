package osinit

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 开机清理残留的 socket.
//
// ── 长期运行时, /run/neox-os 里可能躺着 200 个残留 socket ──
//
// 每起一个进程留一个 <pid>.sock. 正常退出时 AbiServer.Close 会删掉它
// (defer 在 spawn 路径上), **但宿主被杀/崩溃/机器断电时那个 defer
// 根本没机会跑** —— 而那恰恰是长跑里一定会发生的.
//
// 一天下来: 对话进程 + 主动进程回收(每 10 份摘要换一个)几十个,
// 永远累积. /run 常是 tmpfs, inode 是有限的; 而且满地死 socket
// 会让"哪个是活的"这件事查不清.
//
// ── 判据是"连不连得上", 不是文件名或时间 ──
//
// 按 mtime 删会删掉一个刚起来还没接进程的; 按 pid 名字解析会在
// pid 复用时删错. **连一下**是唯一不会误删的判据:
// 连得上说明对端还活着, 连不上(ECONNREFUSED)说明它已经没了.
//
// 而且这条判据对"同一台机器上跑着另一个 OS 实例"也是对的 ——
// 那个实例的 socket 连得上, 不会被扫掉.
func SweepStaleSockets(dir string) (removed int, live int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0 // 目录还不存在 = 没什么可扫的
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		// 超时给得很短: 活着的对端在同一台机器上, 连不上就是连不上.
		// 给长了会让开机在一堆死 socket 上卡几十秒
		c, err := net.DialTimeout("unix", p, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			live++
			continue
		}
		if os.Remove(p) == nil {
			removed++
		}
	}
	return removed, live
}
