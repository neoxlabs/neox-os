//go:build linux

package confine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// cgroup v2 管理 —— **OS 自己建、自己设限额**.
//
// 这是"不借外部工具"那条决定的另一半. 先前只实现了"进程把自己写进
// cgroup.procs"这一半, 结果真机第一次跑就报:
//
//	cannot create /sys/fs/cgroup/neox-os/p1/cgroup.procs: Directory nonexistent
//
// 加入一个不存在的 cgroup 当然会失败. 创建和设限额必须在 spawn **之前**完成.

// EnsureCgroup 建好 cgroup 并写入限额. 幂等.
func EnsureCgroup(root, slice string, lim abi.CgroupLimits) error {
	dir := filepath.Join(root, slice)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建 cgroup 目录: %w", err)
	}

	// 父层级要先把控制器下放, 否则子 cgroup 里根本没有 memory.max 这些文件.
	// best-effort: 已经放过或没权限时不算失败 —— 真正的失败会在写限额时暴露.
	enableControllers(root, slice)

	if lim.MemoryMaxBytes != nil {
		if err := writeFile(filepath.Join(dir, "memory.max"),
			strconv.FormatInt(*lim.MemoryMaxBytes, 10)); err != nil {
			return err
		}
	}
	if lim.PidsMax != nil {
		if err := writeFile(filepath.Join(dir, "pids.max"),
			strconv.FormatInt(*lim.PidsMax, 10)); err != nil {
			return err
		}
	}
	if lim.CPUWeight != nil {
		if err := writeFile(filepath.Join(dir, "cpu.weight"),
			strconv.FormatInt(*lim.CPUWeight, 10)); err != nil {
			return err
		}
	}
	return nil
}

// enableControllers 沿着 slice 的**祖先**把控制器下放, 但**绝不碰叶子**.
//
// 两条 cgroup v2 的规矩合起来决定了这个写法:
//
//  1. 一个 cgroup 只有在**父**的 subtree_control 里写了 "+memory",
//     自己才会有 memory.max 这个文件. 所以祖先必须开.
//
//  2. **无内部进程规则**: 一旦某个 cgroup 自己的 subtree_control 非空,
//     它就成了"内部节点", 内核**拒绝往它里面放进程** ——
//     写 cgroup.procs 会得到一个很难懂的 EIO.
//     所以叶子绝不能开.
//
// 真机第一次跑就撞上了第 2 条: `echo: I/O error`.
func enableControllers(root, slice string) {
	want := "+memory +pids +cpu"
	segs := strings.Split(strings.Trim(slice, "/"), "/")

	cur := root
	_ = writeFile(filepath.Join(cur, "cgroup.subtree_control"), want)
	// 只走到倒数第二层 —— 最后一层是要装进程的叶子, 不能开
	for i, seg := range segs {
		if seg == "" || i == len(segs)-1 {
			continue
		}
		cur = filepath.Join(cur, seg)
		if err := os.MkdirAll(cur, 0o755); err != nil {
			return
		}
		_ = writeFile(filepath.Join(cur, "cgroup.subtree_control"), want)
	}
}

// RemoveCgroup 进程结束后清掉.
//
// 里面还有进程时内核会拒 —— 那就退一步重试几次, **不强删**.
// 只试一次是不够的: 进程刚退出时僵尸可能还没被收, rmdir 被拒,
// 后续又没有清扫的话目录就会一直残留. 曾经出现过 18 个空叶子、零个
// 活进程, 都是这种时序积累的结果.
func RemoveCgroup(root, slice string) {
	dir := filepath.Join(root, slice)
	for i := 0; i < 5; i++ {
		if err := os.Remove(dir); err == nil {
			return
		}
		time.Sleep(time.Duration(20*(i+1)) * time.Millisecond)
	}
}

// SweepOrphanCgroups 清掉上次留下的空叶子.
//
// 启动时做一次. **这是兜底, 不是主路径** —— 主路径是进程退出时删,
// 但那依赖时序对得上; 一旦某次没删成, 那个目录就永远留着了.
// 启动清扫是幂等的, 不管上次因为什么原因失败都能收干净.
//
// 只删**没有任何进程**的目录: cgroup.procs 空才算孤儿.
// 判错就是把别人正在用的 cgroup 删了, 所以宁可漏删.
func SweepOrphanCgroups(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	swept := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
		if err != nil || len(bytes.TrimSpace(procs)) > 0 {
			continue // 读不到或里面还有进程 —— 不碰
		}
		if os.Remove(dir) == nil {
			swept++
		}
	}
	return swept
}

func writeFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("打开 %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("写 %s = %q: %w", path, content, err)
	}
	return nil
}
