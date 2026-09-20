package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 卷里装过的东西, 下一轮必须还找得到.
//
// 只在 /agentwork/.venv 里安装 openpyxl 后, 下一项任务会报告"openpyxl 没装,
// 也没有 pip 可以装". 对话里的路径说明不会保留, 装好的东西必须落在任何进程
// 都看得见的地方.

func mkexec(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeVenvIsOnPath(t *testing.T) {
	root := t.TempDir()
	if got := commandPath(root); strings.Contains(got, ".venv") {
		t.Fatalf("还没装就把 .venv 挂上了: %s", got)
	}
	mkexec(t, root+"/.venv/bin/python3")
	got := commandPath(root)
	if !strings.HasPrefix(got, root+"/.venv/bin:") {
		t.Fatalf("装好的 venv 必须排在最前, 否则系统 python 会先被找到: %s", got)
	}
	// 系统路径不能被顶掉 —— git/sh/编译器都在那儿
	if !strings.Contains(got, "/usr/bin") {
		t.Fatalf("系统路径丢了: %s", got)
	}
}

// 半个残破的 venv 挂在 PATH 最前面, 会把所有 python 命令一起带沟里 ——
// 那比找不到包糟得多. 判据是**解释器真的在那儿**, 不是目录在.
func TestBrokenVenvIsNotTrusted(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root+"/.venv/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	if got := commandPath(root); strings.Contains(got, ".venv") {
		t.Fatalf("空壳 venv(没有解释器)不该上 PATH: %s", got)
	}
}

// 顺序固定. 结果会影响每一条命令的解析, 抖动会让同一件事时好时坏.
func TestCommandPathOrderIsFixed(t *testing.T) {
	root := t.TempDir()
	mkexec(t, root+"/.venv/bin/python3")
	mkexec(t, root+"/.local/bin/x")
	mkexec(t, root+"/node_modules/.bin/y")
	want := strings.Join([]string{
		root + "/.venv/bin", root + "/.local/bin", root + "/node_modules/.bin",
	}, ":")
	for i := 0; i < 5; i++ {
		if got := commandPath(root); !strings.HasPrefix(got, want+":/usr/local/bin") {
			t.Fatalf("顺序不对或不稳定: %s", got)
		}
	}
}

// 有 venv 就要在环境里明说 —— pip 自己就是看这个变量判断在不在虚拟环境里的
func TestVirtualEnvIsAnnounced(t *testing.T) {
	root := t.TempDir()
	if joined := strings.Join(commandEnv(root), " "); strings.Contains(joined, "VIRTUAL_ENV") {
		t.Fatal("没有 venv 却声称在虚拟环境里")
	}
	mkexec(t, root+"/.venv/bin/python3")
	if joined := strings.Join(commandEnv(root), " "); !strings.Contains(joined, "VIRTUAL_ENV="+root+"/.venv") {
		t.Fatalf("有 venv 却没说: %s", joined)
	}
}

// 算得对还不够, 得真接进子命令的环境里.
//
// 这一条钉的是"写了但没接线": commandPath 单独测是绿的, 而 commandEnv
// 里还写着一条固定 PATH —— 那样卷里装的东西照样找不到, 而且测试全绿.
func TestCommandEnvActuallyUsesVolumePath(t *testing.T) {
	root := t.TempDir()
	mkexec(t, root+"/.venv/bin/python3")
	want := "PATH=" + commandPath(root)
	for _, kv := range commandEnv(root) {
		if kv == want {
			return
		}
	}
	t.Fatalf("子命令的 PATH 不是算出来的那条, 卷里装的东西等于没装:\n%v", commandEnv(root))
}
