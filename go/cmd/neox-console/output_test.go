package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitTurn把这一轮的活记进它自己的分支(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Dir, "login.py"), []byte("def login(): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := CommitTurn(plan.Dir, "小登", plan.Branch, "把登录接口搭出来", 7)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("有改动却没提交 —— 没提交的东西对别人等于不存在")
	}

	// **作者必须是这个 bot**: git log 里一眼看得出谁改的
	who, err := runGit(plan.Dir, "log", "-1", "--pretty=%an")
	if err != nil || strings.TrimSpace(who) != "小登" {
		t.Fatalf("提交作者是 %q, 该是小登", strings.TrimSpace(who))
	}
	// 尾注是给机器读的: 交接、统计、"第几轮做的"都靠它
	body, err := runGit(plan.Dir, "log", "-1", "--pretty=%B")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"把登录接口搭出来", "Neox-Bot: 小登", "Neox-Turn: 7"} {
		if !strings.Contains(body, want) {
			t.Errorf("提交里少了 %q:\n%s", want, body)
		}
	}
}

func TestCommitTurn没动过东西就不留空提交(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := CommitTurn(plan.Dir, "小登", plan.Branch, "光说了几句话", 1)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Fatal("什么都没改却提交了 —— 一串空提交会把真正干过活的那几条淹掉")
	}
}

func TestCommitTurn标题太长要截(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("这一轮我做了很多事情", 20)
	if _, err := CommitTurn(plan.Dir, "小登", plan.Branch, long+"\n第二行不该进标题", 1); err != nil {
		t.Fatal(err)
	}
	subject, err := runGit(plan.Dir, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatal(err)
	}
	subject = strings.TrimSpace(subject)
	if len([]rune(subject)) > 73 {
		t.Fatalf("标题 %d 字, 太长了: %s", len([]rune(subject)), subject)
	}
	if strings.Contains(subject, "第二行") {
		t.Fatal("第二行跑进标题了")
	}
}

func TestOutputOf读出来的是它干的那些不是整个项目史(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	// 项目本来就有的历史 —— 不该算成这个 bot 的产物
	if err := os.WriteFile(filepath.Join(repo, "早就有的.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "-c", "user.name=我", "-c", "user.email=me@x", "commit", "-m", "我自己写的"); err != nil {
		t.Fatal(err)
	}

	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Dir, "login.py"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitTurn(plan.Dir, "小登", plan.Branch, "搭登录", 1); err != nil {
		t.Fatal(err)
	}
	// 干到一半的: 还没提交
	if err := os.WriteFile(filepath.Join(plan.Dir, "半成品.py"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := OutputOf(plan.Dir, plan.Branch)
	if len(out.Commits) != 1 {
		t.Fatalf("该只有它自己那 1 条提交, 拿到 %d 条: %+v", len(out.Commits), out.Commits)
	}
	got := out.Commits[0]
	if got.Subject != "搭登录" || got.Files != 1 || got.Add != 2 {
		t.Fatalf("提交读得不对: %+v", got)
	}
	if len(out.Dirty) != 1 || out.Dirty[0].Path != "半成品.py" {
		t.Fatalf("没提交的那份该看得见: %+v", out.Dirty)
	}
}

func TestOutputOf带中文文件名不被转义(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "文案", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	// 这些字符串是要摆到界面上的 —— 转义成 \344\272 就没法看了
	if err := os.WriteFile(filepath.Join(plan.Dir, "发布稿.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := OutputOf(plan.Dir, plan.Branch)
	if len(out.Dirty) != 1 || out.Dirty[0].Path != "发布稿.md" {
		t.Fatalf("中文文件名没读对: %+v", out.Dirty)
	}
}
