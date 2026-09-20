package agent

import (
	"path/filepath"
	"testing"
)

// 分支隔离把工作位置从项目根移到各自的 worktree.
// 旧地址仍会出现在历史对话、PLAN.md 和同事消息里,
// 写的都是项目根下的路径.
//
// 按历史里的 /Users/…/wiki/CHARTER.md 修改时, 能力会拦下请求; 若改为申请
// 写整个项目根, 授权后隔离就失效了.
func TestResolve旧地址落到自己那份副本上(t *testing.T) {
	work := "/Users/x/.neox-os/worktrees/wiki/研究"
	box := Toolbox{Root: work, Elsewhere: "/Users/x/AI/wiki"}

	cases := map[string]string{
		// 项目根下的绝对路径 = 同一个文件的旧地址
		"/Users/x/AI/wiki/CHARTER.md":          filepath.Join(work, "CHARTER.md"),
		"/Users/x/AI/wiki/app/routes/leave.py": filepath.Join(work, "app/routes/leave.py"),
		// 项目根本身
		"/Users/x/AI/wiki": work,
		// 相对路径照旧
		"CHARTER.md": filepath.Join(work, "CHARTER.md"),
		// 自己工作区里的绝对路径不动
		filepath.Join(work, "a.txt"): filepath.Join(work, "a.txt"),
	}
	for in, want := range cases {
		if got := box.resolve(in); got != want {
			t.Errorf("resolve(%q) = %q, 要 %q", in, got, want)
		}
	}
}

func TestResolve别人的东西不许被改写(t *testing.T) {
	work := "/Users/x/.neox-os/worktrees/wiki/研究"
	box := Toolbox{Root: work, Elsewhere: "/Users/x/AI/wiki"}

	// **只落项目根底下的**: 别的地方的绝对路径原样留着, 该被能力拦的
	// 还是要被拦 —— 把任意路径都往自己工作区里折, 等于让它读写错文件
	// 而毫不知情
	for _, p := range []string{
		"/Users/x/AI/oa/PLAN.md",             // 另一个项目
		"/Users/x/AI/wiki-backup/CHARTER.md", // 名字像但不是(按路径段比, 不是前缀)
		"/etc/hosts",
		"/Users/x/.neox-os/worktrees/wiki/构建/a.txt", // 同事的工作区
	} {
		if got := box.resolve(p); got != p {
			t.Errorf("resolve(%q) 被改写成了 %q —— 它会读写到自己都不知道的地方", p, got)
		}
	}
}

func TestResolve没走隔离时什么都不做(t *testing.T) {
	box := Toolbox{Root: "/w"}
	if got := box.resolve("/Users/x/AI/wiki/a.txt"); got != "/Users/x/AI/wiki/a.txt" {
		t.Errorf("没有第二个地址却改写了路径: %q", got)
	}
}
