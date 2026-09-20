package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hasPython(t *testing.T) {
	t.Helper()
	if _, err := runIn(t.TempDir(), "python3", "-c", "pass"); err != nil {
		t.Skip("这台机器上没有 python3")
	}
}

/**
 * 主干上的 cli.py 写着 `import budget`, 而 budget.py 没有写出来 ——
 * 测试全过(没有一个测试 import cli),
 * 项目卡显示"都合上了", 而 `python3 -c "import cli"` 当场炸.
 */
func TestSmoke认得出主干起不来(t *testing.T) {
	hasPython(t)
	dir := t.TempDir()
	write(t, dir, "cli.py", "import budget\n")
	write(t, dir, "ledger.py", "def add(a, b):\n    return a + b\n")

	bad := smokeOf(dir)
	if len(bad) != 1 {
		t.Fatalf("该报一条, 报了 %d 条: %v", len(bad), bad)
	}
	if !strings.Contains(bad[0], "cli.py") || !strings.Contains(bad[0], "budget") {
		t.Errorf("没说清是哪儿断了: %v", bad)
	}
	// 说给人听的那一段要点出"你可能是对着假的验的"
	if note := smokeNote(bad); !strings.Contains(note, "主干") || !strings.Contains(note, "stub") {
		t.Errorf("这段话没说清发生了什么:\n%s", note)
	}
	if smokeNote(nil) != "" {
		t.Error("没毛病却说了一段")
	}
}

/**
 * **判得窄一点**: 缺第三方依赖不是这次合并造成的.
 *
 *	报了只会天天响, 而天天响的警报等于没有警报.
 */
func TestSmoke缺第三方依赖不算(t *testing.T) {
	hasPython(t)
	dir := t.TempDir()
	write(t, dir, "app.py", "import requests\n")
	if bad := smokeOf(dir); len(bad) != 0 {
		t.Fatalf("把环境的事算到这次合并头上了: %v", bad)
	}
}

func TestSmoke语法坏了也要报(t *testing.T) {
	hasPython(t)
	dir := t.TempDir()
	write(t, dir, "broken.py", "def f(:\n")
	bad := smokeOf(dir)
	if len(bad) != 1 || !strings.Contains(bad[0], "broken.py") {
		t.Fatalf("%v", bad)
	}
}

/**
 * **不继承用户 shell 的 PYTHONPATH** —— 那正是这次要查的那种假绿:
 * 有人为了验证往里塞过一个 stub.
 */
func TestSmoke不吃外面塞进来的PYTHONPATH(t *testing.T) {
	hasPython(t)
	dir := t.TempDir()
	stub := t.TempDir()
	write(t, dir, "cli.py", "import budget\n")
	write(t, stub, "budget.py", "# 临时 stub\n")
	t.Setenv("PYTHONPATH", stub)

	if bad := smokeOf(dir); len(bad) == 0 {
		t.Fatal("被一个 stub 骗过去了 —— 而那正是要查的东西")
	}
}

func TestSmoke测试文件不算顶层模块(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "app.py", "x = 1\n")
	write(t, dir, "test_app.py", "import pytest\n")
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "pkg"), "__init__.py", "")
	got := topPyModules(dir)
	want := map[string]bool{"app": true, "pkg": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("挑错了: %v", got)
	}
}
