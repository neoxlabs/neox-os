package confine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 自检 —— 真做一次, 而不是问"支持不支持".
//
// ── 为什么探测不够 ──
//
// Probe 回答的是"这台机器有没有这个机制", 自检回答的是
// **"它现在真的拦得住吗"**. 这两件事会分开:
// 机制在、二进制在、探测全绿, 而规则写错一个字就什么都拦不住,
// 探测完全看不出来.
//
// landlock 自检启动进程, 尝试写入授权范围外的文件.
// 这项检查必须自动执行: 只靠手工验证, **约束退化时容易漏掉复验**.
//
// ── 每项都必须带对照组 ──
//
// 只看"写失败了"说明不了问题 —— 路径不存在、磁盘满、权限位不对,
// 都会写失败. 必须同时证明**不加约束时同样的操作会成功**,
// 才能断定是约束拦下来的.
//
// landlock 的对照组执行同一次写入但不施加约束, 必须写入成功才能排除其他失败原因.

// CheckResult 一项自检的结果
type CheckResult struct {
	Name string `json:"name"`
	// OK 这项真的拦住了 (或真的放行了)
	OK bool `json:"ok"`
	// Detail 说清楚看到了什么 —— 失败时要能据此定位, 不是一句 "failed"
	Detail string `json:"detail"`
	// Skipped 这台机器不具备条件, 不算失败
	Skipped bool `json:"skipped,omitempty"`
}

// SelfCheck 真跑一遍约束. 只在 Linux 上有意义.
func SelfCheck(paths LauncherPaths) []CheckResult {
	if _, err := os.Stat(paths.LandlockRun); err != nil {
		return []CheckResult{{
			Name: "landlock", Skipped: true,
			Detail: "施加者不在: " + paths.LandlockRun,
		}}
	}

	dir, err := os.MkdirTemp("", "neox-selfcheck")
	if err != nil {
		return []CheckResult{{Name: "setup", Detail: err.Error()}}
	}
	defer os.RemoveAll(dir)

	inside := filepath.Join(dir, "allowed")
	outside := filepath.Join(dir, "denied")
	_ = os.MkdirAll(inside, 0o755)
	_ = os.MkdirAll(outside, 0o755)
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("原文"), 0o644); err != nil {
		return []CheckResult{{Name: "setup", Detail: err.Error()}}
	}

	return []CheckResult{
		checkWriteOutsideDenied(paths, inside, target),
		checkWriteInsideAllowed(paths, inside),
		checkControl(target),
	}
}

// 范围外写必须被拦
func checkWriteOutsideDenied(paths LauncherPaths, inside, target string) CheckResult {
	out, err := runConfined(paths, inside,
		fmt.Sprintf("echo 越界 > %s", shQuote(target)))
	if err == nil && !strings.Contains(out, "denied") && !strings.Contains(out, "Permission") {
		return CheckResult{Name: "范围外写被拦", Detail: fmt.Sprintf(
			"**写成功了** —— 约束没拦住. 输出: %s", trim(out))}
	}
	return CheckResult{Name: "范围外写被拦", OK: true, Detail: "如期被拒"}
}

// 范围内写必须放行 —— 只会拦不会放的约束等于把进程废了
func checkWriteInsideAllowed(paths LauncherPaths, inside string) CheckResult {
	f := filepath.Join(inside, "ok.txt")
	out, err := runConfined(paths, inside, fmt.Sprintf("echo 允许 > %s", shQuote(f)))
	if err != nil {
		return CheckResult{Name: "范围内写放行", Detail: fmt.Sprintf(
			"**该允许的写被拦了** —— 约束定得太死, 进程干不了活: %v %s", err, trim(out))}
	}
	if _, statErr := os.Stat(f); statErr != nil {
		return CheckResult{Name: "范围内写放行", Detail: "没报错但文件也没写出来"}
	}
	return CheckResult{Name: "范围内写放行", OK: true, Detail: "如期写入"}
}

// 对照组: 不加约束时同样的写必须成功.
//
// 没有这一项, "写失败"可能是路径不存在、磁盘满、权限位不对 ——
// 断不定是约束拦的.
func checkControl(target string) CheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		fmt.Sprintf("echo 对照 > %s", shQuote(target)))
	if out, err := cmd.CombinedOutput(); err != nil {
		return CheckResult{Name: "对照组(无约束可写)", Detail: fmt.Sprintf(
			"**不加约束也写不了** —— 上面那条'被拦'不能算数, "+
				"可能是路径或权限的问题: %v %s", err, trim(string(out)))}
	}
	return CheckResult{Name: "对照组(无约束可写)", OK: true,
		Detail: "无约束时写得进去, 所以上面的拦截是约束干的"}
}

func runConfined(paths LauncherPaths, rw, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--ro", "/usr", "--ro", "/bin", "--ro", "/lib", "--ro", "/proc"}
	for _, p := range []string{"/lib64", "/sbin", "/etc"} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro", p)
		}
	}
	args = append(args, "--rw", rw, "--", "/bin/sh", "-c", script)
	out, err := exec.CommandContext(ctx, paths.LandlockRun, args...).CombinedOutput()
	return string(out), err
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
