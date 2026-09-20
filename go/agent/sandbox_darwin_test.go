//go:build darwin

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// macOS 上 /usr/bin/git 是个 shim: 它每次都要调 xcrun 去找真正的可执行
// 文件, 而 xcrun 要往系统临时目录写一个 xcrun_db 缓存 —— 那个目录是
// confstr 拿的, 设 TMPDIR 没用.
//
// 拦掉的后果不是"少写一个缓存": 每条 git 命令都会先吐一行
// "couldn't create cache file … Operation not permitted". 这行字会把多轮处理
// 带偏, 甚至诱发用 grep -v xcrun 过滤它.
func TestSandbox放行xcrun的缓存(t *testing.T) {
	profile := sandboxProfile("/tmp/w")
	if !strings.Contains(profile, "xcrun_db") {
		t.Fatalf("xcrun 的缓存没放行, git 每一条都会吐一行权限错:\n%s", profile)
	}
	// 放行的**只是那一个缓存文件名**, 不是整个临时目录
	if strings.Contains(profile, `(subpath "/private/var/folders")`) {
		t.Fatal("把整个系统临时目录都放行了 —— 那比噪音严重得多")
	}
}

// 沙箱的本职: 工作区里写得进去, 外面写不出去.
func TestSandbox边界还在(t *testing.T) {
	if !sandboxAvailable() {
		t.Skip("这台机器没有 sandbox-exec")
	}
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "越界.txt")

	run := func(cmdline string) error {
		argv := sandboxWrap(work, []string{"/bin/sh", "-c", cmdline})
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = work
		return cmd.Run()
	}
	if err := run("echo ok > 里面.txt"); err != nil {
		t.Fatalf("工作区里都写不了: %v", err)
	}
	if err := run("echo bad > " + outside); err == nil {
		t.Fatal("写到工作区外面去了 —— 这道墙是整套隔离的底")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("外面那个文件真的被建出来了")
	}
}
