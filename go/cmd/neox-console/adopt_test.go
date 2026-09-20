package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * 收进 git 这件事最要紧的一条: **第一份快照要有东西**.
 *
 *	原来那条路只补一个空提交, 于是每个 bot 的 worktree 从空提交长出来,
 *	里面一个文件都没有 —— 它对着一个空目录, 而用户的代码好端端躺在
 *	项目根上. 那比不用 git 更糟: 它看起来像"这个项目是空的".
 */
func TestAdopt现有的文件要进第一份快照(t *testing.T) {
	needGit(t)
	atHome(t)
	dir := t.TempDir()
	write(t, dir, "main.py", "print(1)\n")
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "app"), "server.py", "x\n")

	if err := Adopt(dir); err != nil {
		t.Fatal(err)
	}
	out, err := runGit(dir, "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main.py", "app/server.py"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s 没进第一份快照 —— bot 的 worktree 里就没有它:\n%s", want, out)
		}
	}
	// 分给一个 bot 之后, 他手上要看得见这些文件
	plan, err := Assign(dir, "小登", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != ModeWorktree {
		t.Fatalf("没走 worktree: %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(plan.Dir, "main.py")); err != nil {
		t.Fatal("bot 的工作区里是空的 —— 它会以为这个项目什么都没有")
	}
}

/**
 * **bot 自己的临时地方一定要挡住**.
 *
 *	TMPDIR 就指在工作区的 .tmp 里(见 agent/run.go): 它跑测试起的服务、
 *	装的依赖、写的草稿全落在那儿. .tmp/smoke/ 里的三个文件可能跟着一次
 *	提交进主干, pip 缓存也可能达到 7.7MB.
 */
func TestAdopt挡住bot自己的临时地方(t *testing.T) {
	needGit(t)
	atHome(t)
	dir := t.TempDir()
	write(t, dir, "app.py", "x\n")
	// **HOME 也被指到了工作区**: 工具往家里写的一切都落在这个项目里.
	//	npm 的 .npm/_cacache 一次可能带入 3400 个文件、100 万行,
	//	.git 涨到 52MB —— 而项目本身只有 3 个真实文件.
	for _, junk := range []string{".tmp/smoke", ".pytest_cache", "Library/Caches",
		".npm/_cacache/content-v2", ".cache/x", ".config/y", ".local/share"} {
		if err := os.MkdirAll(filepath.Join(dir, junk), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, junk), "垃圾.txt", "不该进版本控制\n")
	}
	if err := Adopt(dir); err != nil {
		t.Fatal(err)
	}
	out, _ := runGit(dir, "ls-tree", "-r", "--name-only", "HEAD")
	for _, junk := range []string{".tmp/", ".pytest_cache", "Library/", ".npm/", ".cache/", ".config/", ".local/"} {
		if strings.Contains(out, junk) {
			t.Errorf("%s 跟着进了第一份快照:\n%s", junk, out)
		}
	}
	if !strings.Contains(out, "app.py") {
		t.Errorf("该收的没收进来:\n%s", out)
	}
}

func TestAdopt先写gitignore(t *testing.T) {
	needGit(t)
	atHome(t)
	dir := t.TempDir()
	write(t, dir, "a.py", "x\n")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "left-pad"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "node_modules", "left-pad"), "index.js", "junk\n")

	if err := Adopt(dir); err != nil {
		t.Fatal(err)
	}
	out, _ := runGit(dir, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(out, "node_modules") {
		t.Errorf("装出来的东西进了第一个提交 —— 进去就再也拿不出来了:\n%s", out)
	}
	if !strings.Contains(out, ".gitignore") {
		t.Error(".gitignore 自己没进去")
	}
}

func TestAdopt已有的gitignore不覆盖(t *testing.T) {
	needGit(t)
	atHome(t)
	dir := t.TempDir()
	write(t, dir, ".gitignore", "我的规矩\n")
	if err := Adopt(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || !strings.Contains(string(got), "我的规矩") {
		t.Fatalf("把用户自己的 .gitignore 覆盖了: %q %v", got, err)
	}
}

func TestAdopt已经是仓库就一个字都不动(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	before, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := Adopt(repo); err != nil {
		t.Fatal(err)
	}
	after, _ := runGit(repo, "rev-parse", "HEAD")
	if before != after {
		t.Fatal("在用户自己的仓库上又提交了一次 —— 那是他的历史")
	}
}

// 在家目录上 git init 是灾难不是便利: 一个 add -A 会把整台机器的文档、
// 照片、密钥全扫进去, 而且从此每条 git 命令都要扫全盘.
func TestAdopt不许收家目录和根目录(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("这台机器没有家目录")
	}
	for _, bad := range []string{home, "/", "/usr"} {
		if err := Adopt(bad); err == nil {
			t.Errorf("%s 也收了", bad)
		}
	}
}

func TestAdopt太大的不硬收(t *testing.T) {
	needGit(t)
	atHome(t)
	dir := t.TempDir()
	if _, err := runGit(dir, "init"); err != nil {
		t.Fatal(err)
	}
	// 单个大文件就够说明问题: 判据是"这一 add 会收进来多少"
	big := make([]byte, 1<<20)
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(dir, "big"+string(rune('a'+i))+".bin"), big, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, bytes, err := wouldAdd(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 4 || bytes < 4<<20 {
		t.Fatalf("量错了: %d 个 / %d 字节", files, bytes)
	}
}

/**
 * **锁文件不该成为冲突源**.
 *
 *	三个人各自在自己的 worktree 里 npm install 时, package-lock.json
 *	各不相同, 每个人合入都要解一次锁文件的冲突 —— 而那是一份几千行
 *	的生成物, 解它既没意义也解不对.
 */
func TestAdopt锁文件合并不冲突(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := t.TempDir()
	write(t, repo, "package.json", "{\"name\":\"x\"}\n")
	write(t, repo, "package-lock.json", "{\"lockfileVersion\":3}\n")
	if err := Adopt(repo); err != nil {
		t.Fatal(err)
	}
	// 两个人各自动了锁文件
	a, _ := Assign(repo, "小前", t.TempDir(), false)
	b, _ := Assign(repo, "小接", t.TempDir(), false)
	write(t, a.Dir, "package-lock.json", "{\"lockfileVersion\":3,\"who\":\"小前\"}\n")
	if _, err := CommitTurn(a.Dir, "小前", a.Branch, "装了个包", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeUp(a.Project, a.Dir, a.Branch, "小前"); err != nil {
		t.Fatal(err)
	}
	write(t, b.Dir, "package-lock.json", "{\"lockfileVersion\":3,\"who\":\"小接\"}\n")
	if _, err := CommitTurn(b.Dir, "小接", b.Branch, "也装了个包", 1); err != nil {
		t.Fatal(err)
	}
	// 第二个人合入: 锁文件两边都改过, **不该冲突**
	got, err := MergeUp(b.Project, b.Dir, b.Branch, "小接")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) > 0 {
		t.Fatalf("锁文件还是冲突了: %v", got.Conflicts)
	}
	// 而手写的那份该冲突还得冲突 —— 那不是生成物
	body, _ := os.ReadFile(filepath.Join(repo, ".gitattributes"))
	if !strings.Contains(string(body), "package-lock.json") {
		t.Errorf(".gitattributes 没写对:\n%s", body)
	}
	if out, _ := runGit(repo, "config", "merge.ours.driver"); strings.TrimSpace(out) != "true" {
		t.Error("ours 驱动没配 —— .gitattributes 里写了也不生效, 而且一点声音都没有")
	}
}

/**
 * **本来就有历史的仓库也要打点**.
 *
 *	有历史的 git 仓库也不等于已经完成收拾. 人自己建的仓库没有本项目的
 *	规矩 —— 把 bot 放进有底子的项目时可能出现:
 *
 *	    版本控制里有 48 个垃圾文件, 比如
 *	    Library/Caches/com.apple.python/…/_bootlocale.cpython-39.pyc
 *
 *	HOME 被指进工作区(隔离要的), 工具往家里写的缓存落在项目里, 宿主
 *	收活时 git add -A 一并收走. bot 放进自己的项目后, 第一件事就是
 *	避免这些文件进入版本控制.
 */
func Test有底子的仓库也要打点(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "pay.py", "def charge(a):\n    return a\n")
	// **他自己的 .gitignore** —— 一个字都不许动
	write(t, repo, ".gitignore", "# 我自己写的\n*.log\n")
	if _, err := runGit(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "-c", "user.name=我", "-c", "user.email=me@x",
		"commit", "-qm", "先把收款那块搭起来"); err != nil {
		t.Fatal(err)
	}

	if err := Adopt(repo); err != nil {
		t.Fatalf("收不进来: %v", err)
	}

	// 他的 .gitignore 原样
	mine, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if got := string(mine); got != "# 我自己写的\n*.log\n" {
		t.Errorf("动了他的 .gitignore: %q", got)
	}
	// 规矩落在 .git/info 下 —— 那儿不进版本控制
	for _, name := range []string{"exclude", "attributes"} {
		if _, err := os.Stat(filepath.Join(repo, ".git", "info", name)); err != nil {
			t.Errorf(".git/info/%s 没写: %v", name, err)
		}
	}
	// 真正要挡住的那件事: 工具缓存不许被 add 进来
	os.MkdirAll(filepath.Join(repo, "Library", "Caches"), 0o755)
	write(t, repo, "Library/Caches/x.pyc", "垃圾")
	os.MkdirAll(filepath.Join(repo, ".npm"), 0o755)
	write(t, repo, ".npm/y", "垃圾")
	out, _ := runGit(repo, "status", "--porcelain", "--untracked-files=all")
	if strings.Contains(out, "Library/") || strings.Contains(out, ".npm/") {
		t.Errorf("工具缓存还是会被收进去:\n%s", out)
	}
	// 锁文件的合并策略也要有 —— 少了每次合入都要解一次几千行的冲突
	if got, _ := runGit(repo, "config", "merge.ours.driver"); strings.TrimSpace(got) != "true" {
		t.Errorf("merge.ours.driver 没配上: %q", got)
	}
}
