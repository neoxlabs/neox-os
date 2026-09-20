package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editBox(t *testing.T, files map[string]string) Toolbox {
	t.Helper()
	d := t.TempDir()
	for name, content := range files {
		full := filepath.Join(d, name)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Toolbox{Root: d}
}

func runEdit(t *testing.T, box Toolbox, path, old, new string) (string, error) {
	t.Helper()
	return editTool().Run(box, map[string]any{
		"path": path, "old_string": old, "new_string": new})
}

func TestEditReplacesOnce(t *testing.T) {
	box := editBox(t, map[string]string{"a.txt": "hello world\nbye world\n"})
	out, err := runEdit(t, box, "a.txt", "hello world", "你好世界")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"success"`) {
		t.Fatalf("应当是结构化的 success: %s", out)
	}
	got, _ := os.ReadFile(filepath.Join(box.Root, "a.txt"))
	if string(got) != "你好世界\nbye world\n" {
		t.Fatalf("只该改那一处: %q", got)
	}
}

// 找不到原文 → 明确报 string_not_found, 并要求重读后使用完全一致的原文.
// 这跟"文件不存在"是两回事, 处理方式完全不同, 不能混成一个错误.
func TestEditStringNotFound(t *testing.T) {
	box := editBox(t, map[string]string{"a.txt": "hello"})
	_, err := runEdit(t, box, "a.txt", "不存在的原文", "x")
	if !errors.Is(err, ErrStringNotFound) {
		t.Fatalf("应当报 string_not_found, got %v", err)
	}
	if !strings.Contains(err.Error(), "重新 read_file") {
		t.Fatalf("错误必须说清下一步怎么办: %v", err)
	}
}

// 原文不唯一 → 报 ambiguous, 提示多带上下文.
// **绝不能随便挑一处替换** —— 那会改错地方而且用户看不出来.
func TestEditAmbiguous(t *testing.T) {
	box := editBox(t, map[string]string{"a.txt": "x\nx\nx\n"})
	_, err := runEdit(t, box, "a.txt", "x", "y")
	if !errors.Is(err, ErrAmbiguousMatch) {
		t.Fatalf("应当报 ambiguous_match, got %v", err)
	}
	if !strings.Contains(err.Error(), "3 次") {
		t.Fatalf("要说清出现了几次: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(box.Root, "a.txt"))
	if string(got) != "x\nx\nx\n" {
		t.Fatal("不唯一时一个字节都不该改")
	}
}

// 文件不存在 → 必须说死"别重试 edit, 改用 write_file".
// 不说清楚模型会一直重试 edit —— 那永远不会成功.
func TestEditMissingFileTellsToWrite(t *testing.T) {
	box := editBox(t, nil)
	_, err := runEdit(t, box, "nope.txt", "a", "b")
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "write_file") {
		t.Fatalf("必须指向 write_file: %v", err)
	}
}

// old == new → already_done, 不写盘.
// 重复写入会让"改了没"变模糊, 也会白刷 mtime.
func TestEditAlreadyDone(t *testing.T) {
	box := editBox(t, map[string]string{"a.txt": "same"})
	full := filepath.Join(box.Root, "a.txt")
	before, _ := os.Stat(full)
	out, err := runEdit(t, box, "a.txt", "same", "same")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already_done") {
		t.Fatalf("应当是 already_done: %s", out)
	}
	after, _ := os.Stat(full)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("already_done 不该写盘")
	}
}

// 空 old_string → 明确拒绝并指向 write_file
func TestEditRejectsEmptyOld(t *testing.T) {
	box := editBox(t, map[string]string{"a.txt": "x"})
	if _, err := runEdit(t, box, "a.txt", "", "y"); err == nil ||
		!strings.Contains(err.Error(), "write_file") {
		t.Fatalf("空 old_string 应当被拒并指向 write_file: %v", err)
	}
}

// 多行原文照样能定位 —— 这是"多带几行上下文让它唯一"的前提
func TestEditMultilineAnchor(t *testing.T) {
	box := editBox(t, map[string]string{
		"a.txt": "foo\nbar\nbaz\nfoo\nqux\n"})
	if _, err := runEdit(t, box, "a.txt", "baz\nfoo", "baz\nFOO"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(box.Root, "a.txt"))
	if string(got) != "foo\nbar\nbaz\nFOO\nqux\n" {
		t.Fatalf("多行锚点定位错了: %q", got)
	}
}

// 只读工具才允许并发 —— 写操作并发会互相覆盖
func TestReadOnlyClassification(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	for _, c := range []struct {
		name string
		want bool
	}{
		{"list_dir", true}, {"read_file", true},
		{"write_file", false}, {"edit_file", false},
	} {
		tool, ok := ts.Get(c.name)
		if !ok {
			t.Fatalf("缺工具 %s", c.name)
		}
		if tool.ReadOnly() != c.want {
			t.Fatalf("%s ReadOnly=%v, want %v", c.name, tool.ReadOnly(), c.want)
		}
	}
}

// 缺参数必须报"缺了什么", 不能让它变成一句系统错误.
// 漏传 path 时若退化成 "read /agentwork: is a directory", 模型无法判断缺少哪个
// 参数, 只能瞎猜着重试 —— 白烧一轮.
func TestMissingArgIsExplicit(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	edit, _ := ts.Get("edit_file")
	err := edit.Validate(map[string]any{"old_string": "a", "new_string": "b"})
	if err == nil {
		t.Fatal("缺 path 应当被拒")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Fatalf("必须说清缺哪个参数: %v", err)
	}
	if !strings.Contains(err.Error(), "old_string") {
		t.Fatalf("还要说清这个工具要什么参数: %v", err)
	}
}

// list_dir 的 path 可省 —— 不给就是当前目录, 那是有意义的默认
func TestOptionalArgAccepted(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	ld, _ := ts.Get("list_dir")
	if err := ld.Validate(map[string]any{}); err != nil {
		t.Fatalf("list_dir 不给 path 应当允许: %v", err)
	}
}

// 空字符串跟没给等价 —— 模型经常传 "" 当作"没有"
func TestEmptyStringCountsAsMissing(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	rf, _ := ts.Get("read_file")
	if err := rf.Validate(map[string]any{"path": ""}); err == nil {
		t.Fatal("空 path 应当被当成没给")
	}
}

// 缺参数的报错要说清**它实际给了什么**.
//
// 只说"缺 pattern", 模型如果自以为给了(键名写错、值是空串)就没有任何
// 线索自纠, 只会原样再试 —— 连试 5 次会被停滞检测硬停.
// 对排查的人同样关键: 我看着"缺 pattern"和账本里"确实给了 pattern"
// 这两个矛盾事实, 连着推错了两次方向.
func TestMissingArgErrorShowsWhatWasGiven(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	se, _ := ts.Get("search")
	err := se.Validate(map[string]any{"query": "NEEDLE", "path": "site"})
	if err == nil {
		t.Fatal("键名写错了该被拒")
	}
	msg := err.Error()
	if !strings.Contains(msg, "query=NEEDLE") {
		t.Fatalf("没说它实际给了什么, 模型无从自纠: %s", msg)
	}
	if !strings.Contains(msg, "pattern") {
		t.Fatalf("没说缺什么: %s", msg)
	}
}

// 一个参数都没给也要说得明白, 不能是空荡荡一句
func TestNoArgsAtAllIsExplicit(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	se, _ := ts.Get("search")
	err := se.Validate(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "什么都没给") {
		t.Fatalf("空参数没说清: %v", err)
	}
}
