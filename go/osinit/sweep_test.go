package osinit

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// **死 socket 要清掉, 活的一个都不许碰.**
//
// /run/neox-os 里可能躺着 200 个残留. 正常退出时 Close 会删,
// 但宿主被杀/崩溃/断电时那个 defer 根本没机会跑 ——
// 而那恰恰是长跑里一定会发生的.
func TestSweepRemovesDeadKeepsLive(t *testing.T) {
	dir := t.TempDir()

	// 三个死的: 有文件, 没人监听
	for _, n := range []string{"p1.sock", "p2.sock", "p3.sock"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 一个活的: 真的在监听
	livePath := filepath.Join(dir, "p9.sock")
	ln, err := net.Listen("unix", livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	removed, live := SweepStaleSockets(dir)
	if removed != 3 {
		t.Fatalf("清掉 %d 个, 该是 3 个", removed)
	}
	if live != 1 {
		t.Fatalf("认出 %d 个活的, 该是 1 个", live)
	}
	// **活的绝不能被删** —— 删掉的话那个进程从此跟 OS 失联,
	// 而它自己不会知道, 表现是"某个 agent 突然不响应了"
	if _, err := os.Stat(livePath); err != nil {
		t.Fatal("把活着的 socket 删了 —— 那个进程会从此跟 OS 失联")
	}
	for _, n := range []string{"p1.sock", "p2.sock", "p3.sock"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Fatalf("%s 还在", n)
		}
	}
}

// 不是 .sock 的一律不碰 —— 这个目录以后可能放别的东西
func TestSweepIgnoresNonSockets(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "notes.txt")
	os.WriteFile(keep, []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "p1.sock"), nil, 0o600)

	removed, _ := SweepStaleSockets(dir)
	if removed != 1 {
		t.Fatalf("清了 %d 个", removed)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("把不相干的文件删了")
	}
}

// 目录不存在不该出错 —— 头一次开机就是这样
func TestSweepOnMissingDir(t *testing.T) {
	removed, live := SweepStaleSockets("/nonexistent/neox-os-xyz")
	if removed != 0 || live != 0 {
		t.Fatalf("空目录扫出了东西: %d %d", removed, live)
	}
}
