package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=x", "GIT_AUTHOR_EMAIL=x@y",
		"GIT_COMMITTER_NAME=x", "GIT_COMMITTER_EMAIL=x@y")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

/**
 * "文件不存在"的时候要说清**它在谁手上**.
 *
 *	当前分支按契约去读 app/server.js 时, 不能只提示"别重试同一路径——先
 *	list_dir 看看真实的文件名". 路径一个字都没错, 只是对应分支还没合上来.
 *	否则就会去 list_dir、去 mkdir app、去写一个假 server 自测.
 */
func Test文件不存在时说清在谁手上(t *testing.T) {
	project := t.TempDir()
	gitAt(t, project, "init", "-q", "-b", "master")
	write := func(rel, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(project, rel)), 0o755)
		os.WriteFile(filepath.Join(project, rel), []byte(body), 0o644)
	}
	write("CHARTER.md", "章程")
	gitAt(t, project, "add", "-A")
	gitAt(t, project, "commit", "-qm", "第一份快照")

	// 小己在自己分支上写了 app/server.js, 还没合
	gitAt(t, project, "checkout", "-q", "-b", "neox/小己")
	write("app/server.js", "// 后端")
	gitAt(t, project, "add", "-A")
	gitAt(t, project, "commit", "-qm", "后端")
	gitAt(t, project, "checkout", "-q", "master")

	plans := map[string]Plan{
		"小己": {Project: project, Branch: "neox/小己"},
		"小戊": {Project: project, Branch: "neox/小戊"},
	}
	ask := whoHas(project, "master", "小戊", plans)

	got := ask("app/server.js")
	if !strings.Contains(got, "小己") {
		t.Errorf("没说清在谁手上: %q", got)
	}
	if !strings.Contains(got, "还没合") {
		t.Errorf("没说清是还没合上来: %q", got)
	}

	// 主干上有的, 该让它自己拉 —— 那是最省事的一条路
	if got := ask("CHARTER.md"); !strings.Contains(got, "sync_down") {
		t.Errorf("主干上有的没让它 sync_down: %q", got)
	}
	// 真的谁那儿都没有 —— 一个字都别多说, 免得把它往错的方向指
	if got := ask("app/谁都没有.js"); got != "" {
		t.Errorf("谁那儿都没有却编了一句: %q", got)
	}
	// 自己手上的不算 —— 它要的是"别人那儿"
	if got := ask("../外面"); got != "" {
		t.Errorf("越界路径也答了: %q", got)
	}
}
