package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ** 必须能吃**零段** —— src/**/*.go 要能匹配 src/main.go.
// 漏了这条, 用户写出最自然的模式却找不到最顶层的文件.
func TestDoubleStarMatchesZeroSegments(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"src/**/*.go", "src/main.go", true},
		{"src/**/*.go", "src/a/b/deep.go", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"**", "任意/深/的/路径.txt", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.name); got != c.want {
			t.Fatalf("%q vs %q = %v, 期望 %v", c.pat, c.name, got, c.want)
		}
	}
}

// 单个 * 不许跨目录 —— 跨了就跟 ** 没区别, 用户没法表达"只在这一层"
func TestSingleStarDoesNotCrossSlash(t *testing.T) {
	if globMatch("*.go", "a/b.go") {
		t.Fatal("单个 * 跨了目录分隔符")
	}
	if globMatch("src/*.go", "src/a/b.go") {
		t.Fatal("单个 * 跨了目录分隔符")
	}
	if !globMatch("src/*.go", "src/main.go") {
		t.Fatal("同一层反而匹配不上")
	}
}

// 后缀和字符集要正常工作
func TestPatternForms(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"**/*_test.go", "agent/glob_test.go", true},
		{"**/*_test.go", "agent/glob.go", false},
		{"**/README*", "docs/README.md", true},
		{"**/[ab].txt", "x/a.txt", true},
		{"**/[ab].txt", "x/c.txt", false},
		{"**/*.test.ts", "src/deep/x.test.ts", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.name); got != c.want {
			t.Fatalf("%q vs %q = %v, 期望 %v", c.pat, c.name, got, c.want)
		}
	}
}

func globDir(t *testing.T, names ...string) string {
	t.Helper()
	d := t.TempDir()
	for _, n := range names {
		full := filepath.Join(d, n)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("x"), 0o644)
	}
	return d
}

// 真在磁盘上走一遍
func TestGlobWalksRealTree(t *testing.T) {
	d := globDir(t, "main.go", "a/b/deep.go", "a/notes.md", "node_modules/pkg/x.go")
	hits, _, err := runGlob(Toolbox{}, d, "**/*.go")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(hits, " ")
	if !strings.Contains(joined, "main.go") || !strings.Contains(joined, "deep.go") {
		t.Fatalf("漏了文件: %v", hits)
	}
	if strings.Contains(joined, "notes.md") {
		t.Fatalf("匹配了不该匹配的: %v", hits)
	}
	// node_modules 不该被翻 —— 翻它会淹掉真正的结果
	if strings.Contains(joined, "node_modules") {
		t.Fatalf("翻了 node_modules: %v", hits)
	}
}

// 顺序稳定 —— 文件系统顺序不保证, 不排会让同一次查询每次结果不同
func TestGlobOrderStable(t *testing.T) {
	d := globDir(t, "z.go", "a.go", "m/y.go", "b.go")
	first, _, _ := runGlob(Toolbox{}, d, "**/*.go")
	for i := 0; i < 20; i++ {
		got, _, _ := runGlob(Toolbox{}, d, "**/*.go")
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatal("同一次查询结果顺序不稳定")
		}
	}
}

// 没找到时最常见的错法是忘了 ** —— 要专门点出来
func TestMissingDoubleStarIsCalledOut(t *testing.T) {
	got := formatGlob("*.go", "site", nil, false)
	if !strings.Contains(got, "没有 **") {
		t.Fatalf("没提醒它漏了 **:\n%s", got)
	}
	// 已经写了 ** 的就别啰嗦
	if strings.Contains(formatGlob("**/*.go", "site", nil, false), "没有 **") {
		t.Fatal("已经写了 ** 还在提醒")
	}
}

// 截断要说出来 —— 静默截断会让模型把"只有这些"当结论
func TestGlobTruncationIsLoud(t *testing.T) {
	got := formatGlob("**/*", ".", []string{"a"}, true)
	if !strings.Contains(got, "还有没列出来的") {
		t.Fatalf("截断没说出来:\n%s", got)
	}
}

// find_files 是只读的, 否则并行编排会把它退回串行
func TestGlobIsReadOnly(t *testing.T) {
	if !globTool().ReadOnly() {
		t.Fatal("find_files 必须只读")
	}
	// 停滞检测按**声明**判会不会改动世界, 不按名字猜 —— 名字前缀那套
	// 已经漏过一次 (run 被判成只读)
	if globTool().Mutates {
		t.Fatal("find_files 被声明成会改动世界, 停滞检测会判错")
	}
}
