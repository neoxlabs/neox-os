//go:build !linux

package confine

import "github.com/neox-os/neox-os/abi"

// 非 Linux 上没有 cgroup. 空实现只是让代码编得过 ——
// 真正的约束在 confined 模式下由启动自检拦住, 到不了这里.
func EnsureCgroup(root, slice string, lim abi.CgroupLimits) error { return nil }
func RemoveCgroup(root, slice string)                             {}

// SweepOrphanCgroups 非 Linux 上没有 cgroup, 什么都不用扫
func SweepOrphanCgroups(root string) int { return 0 }
