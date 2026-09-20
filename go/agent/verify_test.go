package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 验证器的选择必须**确定性** —— 结果会进上下文, 抖动会让同一次改动
// 每次看起来都不一样, 而且前缀缓存跟着废
func TestVerifierChoiceIsDeterministic(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644)
	first := verifierFor(root, "a.go")
	if first == nil {
		t.Fatal("有 go.mod 却没给出 go 的验证器")
	}
	for i := 0; i < 50; i++ {
		again := verifierFor(root, "a.go")
		if again == nil || again.cmd != first.cmd || again.what != first.what {
			t.Fatal("同样的输入给出了不同的验证器")
		}
	}
}

// 没有项目标记就不验 —— 在一个没有 go.mod 的目录里跑 go build
// 只会得到一堆跟这次改动无关的噪音
func TestNoProjectMarkerNoVerifier(t *testing.T) {
	if v := verifierFor(t.TempDir(), "a.go"); v != nil {
		t.Fatalf("没有 go.mod 却要跑 %s", v.cmd)
	}
}

// 认不出来的文件类型不验. **验不了就闭嘴**, 不是报错
func TestUnknownExtensionIsSilent(t *testing.T) {
	for _, f := range []string{"README.md", "data.csv", "photo.png", "noext"} {
		if v := verifierFor(t.TempDir(), f); v != nil {
			t.Errorf("%s 不该有验证器, 却给了 %s", f, v.cmd)
		}
	}
}

// **只报跟这次改动有关的错.**
//
// 项目里本来就有的编译错误会把信号淹掉: 模型看到一屏跟自己无关的报错,
// 要么去修不该它修的东西, 要么直接放弃.
func TestOnlyRelevantErrorsSurvive(t *testing.T) {
	out := strings.Join([]string{
		"# example/pkg",
		"other.go:12:3: undefined: somethingElse",
		"stats.go:7:2: syntax error: unexpected }",
		"vendor/lib/deep.go:99:1: expected declaration",
	}, "\n")
	got := relevantLines(out, "calc/stats.go")
	if !strings.Contains(got, "stats.go:7:2") {
		t.Fatalf("自己改的文件那条没留下: %q", got)
	}
	if strings.Contains(got, "other.go") || strings.Contains(got, "deep.go") {
		t.Fatalf("无关的错误漏进来了: %q", got)
	}
}

// 按文件名匹配而不是完整路径 —— 编译器报的路径形态各不相同
// (相对/绝对/带包名前缀), 按完整路径比会一条都留不下
func TestMatchesByBaseNameNotFullPath(t *testing.T) {
	got := relevantLines("/abs/build/calc/stats.go:3:1: bad", "calc/stats.go")
	if got == "" {
		t.Fatal("绝对路径形态的报错没匹配上 —— 实际项目里这是常态")
	}
}

// 路径来自模型, 必须当成不可信输入 —— 它会被拼进 sh -c
func TestPathIsQuotedIntoShell(t *testing.T) {
	v := verifierFor(t.TempDir(), "a'; rm -rf /; echo '.py")
	if v == nil {
		t.Skip("这个扩展名没有验证器")
	}
	if strings.Contains(v.cmd, "; rm -rf /;") && !strings.Contains(v.cmd, `'\''`) {
		t.Fatalf("路径没被引号包住, 能注入: %s", v.cmd)
	}
}

// 端到端: 写一个语法坏的 Python 文件, 警告必须跟在写入结果后面.
//
// 这条走的是真的 loop (callTool → autoVerify → execCommand),
// 不是单测 verifierFor —— 前者验"逻辑对不对", 这条验
// **它真的挂在那条路径上**.
func TestBadSyntaxIsCaughtRightAfterWrite(t *testing.T) {
	if _, err := execCommand(Toolbox{Root: t.TempDir()},
		map[string]any{"cmd": "which python3"}); err != nil {
		t.Skip("这台机器上没有 python3")
	}
	dir := t.TempDir()
	sys := newFakeSys()
	sys.canFn = func(abi.CapAxis, string) (bool, error) { return true, nil }
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: dir}, MaxSteps: 2,
		Model: &Scripted{Steps: []Step{
			{Tool: "write_file", Args: map[string]any{
				"path": "broken.py", "content": "def f(:\n  pass\n"}},
			{Done: "写完了"},
		}}}
	ag.Run("写个文件")

	e := sys.last("tool_ok")
	got, _ := e["result"].(string)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("语法坏了却没有警告 —— 自动验证没挂在写路径上: %q", got)
	}
	if !strings.Contains(got, "broken.py") {
		t.Fatalf("警告里没指明是哪个文件: %q", got)
	}
}

// 语法没问题就**一个字都不说** —— 每次都说"检查通过"是纯噪音,
// 而且会挤掉真正有用的上下文
func TestCleanFileSaysNothing(t *testing.T) {
	if _, err := execCommand(Toolbox{Root: t.TempDir()},
		map[string]any{"cmd": "which python3"}); err != nil {
		t.Skip("这台机器上没有 python3")
	}
	dir := t.TempDir()
	sys := newFakeSys()
	sys.canFn = func(abi.CapAxis, string) (bool, error) { return true, nil }
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: dir}, MaxSteps: 2,
		Model: &Scripted{Steps: []Step{
			{Tool: "write_file", Args: map[string]any{
				"path": "fine.py", "content": "def f():\n    return 1\n"}},
			{Done: "写完了"},
		}}}
	ag.Run("写个文件")
	got, _ := sys.last("tool_ok")["result"].(string)
	if strings.Contains(got, "顺带跑了") {
		t.Fatalf("没问题也说话了 —— 纯噪音: %q", got)
	}
}

// 没有 proc 能力就静默跳过 —— 这只是没有这项增强, 不是这一步失败了
func TestNoProcCapabilitySkipsSilently(t *testing.T) {
	dir := t.TempDir()
	sys := newFakeSys()
	sys.canFn = func(axis abi.CapAxis, _ string) (bool, error) {
		return axis != abi.AxisProc, nil // 有写权限, 没有起进程的权限
	}
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: dir}, MaxSteps: 2,
		Model: &Scripted{Steps: []Step{
			{Tool: "write_file", Args: map[string]any{
				"path": "broken.py", "content": "def f(:\n"}},
			{Done: "写完了"},
		}}}
	ag.Run("写个文件")
	got, _ := sys.last("tool_ok")["result"].(string)
	if strings.Contains(got, "顺带跑了") || strings.Contains(got, "⚠") {
		t.Fatalf("没有 proc 能力却跑了验证: %q", got)
	}
}

// 写完一个文件顺手验的那句话, **不许断言它不可能知道的事**.
//
// 全新的空项目里, 刚写下第一个文件时, go build 当然不过 (main 还没写).
// 而那句话说的是"那是本来就有的, 不是你这次改出来的" ——
// 前一秒那儿什么都没有, 这是纯粹的假话.
//
// 更糟的一种: 它改了 notes.go 的函数签名, main.go 因此编译不过.
// 报错行里没有 notes.go, 于是这条会说"不是你改出来的, 别去动它" ——
// 项目就这么坏着交付, 而它以为自己被明确告知过不用管.
func TestVerifyNeverClaimsTheErrorIsNotYours(t *testing.T) {
	note := elsewhereNote("go build", "notes.go", "")
	for _, lie := range []string{"本来就有的，不是你这次改出来的", "别去动它"} {
		if strings.Contains(note, lie) {
			t.Fatalf("这句话断言了它查不实的事: %q", note)
		}
	}
	// 该说的还得说: 别去修跟自己无关的报错
	if !strings.Contains(note, "先别管它") {
		t.Fatalf("完全不给指引的话, 模型会去修不该它修的东西: %q", note)
	}
	// 而且要把不确定性说出来
	if !strings.Contains(note, "可能") {
		t.Fatalf("把猜测说成了事实: %q", note)
	}
}

// 报错落在**它这次也动过的别的文件**上时, 必须指过去.
//
// 这是跨文件影响唯一能查实的判据: 我们没有项目的历史, 但我们知道自己动过谁.
func TestVerifyPointsAtYourOtherFile(t *testing.T) {
	note := elsewhereNote("go build", "notes.go", "main.go")
	if !strings.Contains(note, "main.go") || !strings.Contains(note, "你这次也动过") {
		t.Fatalf("没指向它自己动过的那个文件: %q", note)
	}
	if strings.Contains(note, "先别管它") {
		t.Fatalf("跨文件影响却让它别管: %q", note)
	}
}

// touchedIn 只认这次会话动过的文件, 而且顺序要稳.
func TestTouchedInFindsOnlyWhatYouTouched(t *testing.T) {
	a := &Agent{lastVerified: map[string]time.Time{
		"notes.go": time.Now(), "main.go": time.Now(), "readme.md": time.Now(),
	}}
	out := "# notepad\n./main.go:12:2: undefined: Save\n./vendor/x/y.go:3: bad"
	got := a.touchedIn(out, "notes.go")
	if got != "main.go" {
		t.Fatalf("认成了 %q —— 只有 main.go 既在报错里又是它动过的", got)
	}
	// 它刚改的那个文件不算 —— 那一条走的是另一条路(直接把报错给它)
	if a.touchedIn("./notes.go:1:1: bad", "notes.go") != "" {
		t.Error("把它刚改的文件也算进'别的文件'里了")
	}
	// 稳定: 同一份输入两次说法必须一样
	multi := "./main.go:1: x\n./readme.md:1: y"
	if a.touchedIn(multi, "notes.go") != a.touchedIn(multi, "notes.go") {
		t.Error("同一次报错每次说法都不一样")
	}
}

/**
 * **验不了 ≠ 你写错了**.
 *
 *	每个 .jsx 都可能收到一句"**你改的这个文件有问题**",
 *	而那只是 node --check 不认 .jsx 这个后缀.
 *	漏报它接着干, 误报它停下来找一个不存在的毛病.
 */
func Test不给jsx挂一个验不了它的检查器(t *testing.T) {
	root := t.TempDir()
	if v := verifierFor(root, "src/App.jsx"); v != nil {
		t.Errorf("给 .jsx 挂了 %q —— node --check 根本不认这个后缀", v.cmd)
	}
	// 认得的照旧要验
	if v := verifierFor(root, "tool.js"); v == nil {
		t.Error(".js 反而不验了")
	}
	if v := verifierFor(root, "tool.cjs"); v == nil {
		t.Error(".cjs 也该验")
	}
}

func Test工具自己验不了的时候闭嘴(t *testing.T) {
	for _, out := range []string{
		"[退出码 1]\nTypeError [ERR_UNKNOWN_FILE_EXTENSION]: Unknown file extension \".jsx\"",
		"[退出码 127]\nsh: npx: command not found",
		"[退出码 1]\nError: Cannot find module 'typescript'",
	} {
		if !cannotCheck(out) {
			t.Errorf("把'我验不了'当成了'你写错了':\n%s", out)
		}
	}
	// 真的语法错不许被当成"验不了"
	if cannotCheck("[退出码 1]\nSyntaxError: Unexpected token '}'") {
		t.Error("真的语法错被当成'验不了'吞掉了 —— 那是漏报")
	}
}
