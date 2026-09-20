package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * **"谁手上还有没合的"是这一页最要紧的一格**.
 *
 *	一条干完了却没合上去的分支, 在别的地方看跟"什么都没干"长得一模一样 ——
 *	那是最容易丢活的地方.
 */
func TestProject看得见谁手上还有没合的(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	a, _ := Assign(repo, "小登", root, false)
	b, _ := Assign(repo, "小勤", root, false)

	write(t, a.Dir, "login.py", "干完了\n")
	if _, err := CommitTurn(a.Dir, "小登", a.Branch, "搭登录", 1); err != nil {
		t.Fatal(err)
	}
	write(t, b.Dir, "notice.py", "干到一半\n") // 没提交

	got := projectOf(repo, map[string]Plan{"小登": a, "小勤": b})
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	byBot := map[string]ProjectMember{}
	for _, one := range got.Members {
		byBot[one.Bot] = one
	}
	if byBot["小登"].Unmerged != 1 {
		t.Errorf("没数出小登手上那条没合的: %+v", byBot["小登"])
	}
	if byBot["小勤"].Dirty == 0 {
		t.Errorf("没看见小勤手上没提交的活: %+v", byBot["小勤"])
	}
	if got.Head == nil || got.Main == "" {
		t.Errorf("主干那一行是空的: %+v", got)
	}
}

func TestProject不是仓库就明说(t *testing.T) {
	needGit(t)
	atHome(t)
	got := projectOf(t.TempDir(), nil)
	if !strings.Contains(got.Error, "git") {
		t.Fatalf("没说清为什么看不出状态: %+v", got)
	}
}

/**
 * 撤一次合入 —— **用 revert 不用 reset**.
 *
 *	历史不改写: 别人的 worktree 已经从主干拉过东西了, 抹掉一段历史等于
 *	让他们下一次 sync_down 撞上一堆莫名其妙的冲突.
 */
func TestRevert撤掉之后历史还在(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, _ := Assign(repo, "小登", t.TempDir(), false)
	write(t, plan.Dir, "bad.py", "闯祸的那一版\n")
	if _, err := CommitTurn(plan.Dir, "小登", plan.Branch, "加了个会闯祸的东西", 1); err != nil {
		t.Fatal(err)
	}
	if got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小登"); err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}
	before, _ := runGit(repo, "rev-list", "--count", "HEAD")

	head := lineOf(repo, mainBranch(repo))
	said, err := RevertOn(repo, head.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "撤掉了") {
		t.Errorf("回执没说清撤了什么: %s", said)
	}
	// 文件没了, 但历史多了一条 —— 不是少了一条
	if out, _ := runGit(repo, "ls-files"); strings.Contains(out, "bad.py") {
		t.Error("撤了却还在")
	}
	after, _ := runGit(repo, "rev-list", "--count", "HEAD")
	if strings.TrimSpace(after) <= strings.TrimSpace(before) {
		t.Errorf("历史被改写了: %s → %s", before, after)
	}
}

// 主干脏着的时候不许撤: 那上面是**你自己**没提交的改动, revert 会跟它
// 搅在一起, 出了事分不清是谁弄的
func TestRevert主干脏着不许撤(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	write(t, repo, "我的草稿.txt", "还没想好\n")
	head := lineOf(repo, mainBranch(repo))
	_, err := RevertOn(repo, head.Hash)
	if err == nil || !strings.Contains(err.Error(), "没提交") {
		t.Fatalf("%v", err)
	}
	// 我的草稿必须还在
	if out, _ := runGit(repo, "status", "--porcelain"); !strings.Contains(out, "草稿") {
		t.Fatal("把用户没提交的东西弄没了")
	}
}

/**
 * **撤销必须精确到你按的那一条**.
 *
 *	带 --no-merges 去查一个合并提交, git 会一路往前走到第一条非合并
 *	提交 —— 于是你按的是"撤这条", 撤掉的是另一条. 那种错没人能在事后
 *	看出来.
 */
func TestRevert撤的就是你按的那一条(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, _ := Assign(repo, "小登", t.TempDir(), false)

	// 造一条合并提交在主干上: 先各自动一处, 再合
	write(t, repo, "主干.txt", "主干这边改的\n")
	commit(t, repo, "主干上的改动")
	write(t, plan.Dir, "分支.txt", "分支这边改的\n")
	if _, err := CommitTurn(plan.Dir, "小登", plan.Branch, "分支上的改动", 1); err != nil {
		t.Fatal(err)
	}
	// 第一次会被"先验一遍"拦下(主干带进了别人改的东西), 第二次放行 ——
	// 那条规矩只说一次, 见 merge.go
	if _, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小登"); err != nil {
		t.Fatal(err)
	}
	if got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小登"); err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}
	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	head = strings.TrimSpace(head)

	// 主干最后那一条是合并提交的话, commitAt 要照实认出来
	exact := commitAt(repo, head, false)
	if exact == nil || !strings.HasPrefix(head, exact.Hash) {
		t.Fatalf("查到的不是同一条: %+v (要 %s)", exact, head[:8])
	}
	// 而 lineOf 是给"主干最近"那一格用的, 它跳过合并 —— 两者不该混
	if shown := lineOf(repo, mainBranch(repo)); shown != nil && shown.Merge {
		t.Errorf("「主干最近」那一格显示的是一条合并记录: %+v", shown)
	}
}

/**
 * **交接要留得下来**: 它改变的是"这个项目里谁负责什么" —— 那是项目
 * 层面的事实, 不该只留在两个人的聊天记录里. 三天之后想知道"登录这块
 * 现在归谁", 没人记得清是哪一天哪一句.
 */
func TestProject看得见交接过谁(t *testing.T) {
	needGit(t)
	atHome(t) // 它自己会把 NEOX_HOME 指到一个临时目录
	repo := newRepo(t)
	root := t.TempDir()
	from, _ := Assign(repo, "小登", root, false)
	to, _ := Assign(repo, "小新", root, false)
	write(t, from.Dir, "login.py", "干到一半\n")
	if _, err := CommitTurn(from.Dir, "小登", from.Branch, "搭登录", 1); err != nil {
		t.Fatal(err)
	}
	got, err := HandOver(repo, from, to, "小登", "小新")
	if err != nil {
		t.Fatal(err)
	}
	noteHandoff(repo, got)

	report := projectOf(repo, map[string]Plan{"小登": from, "小新": to})
	if len(report.Handoffs) != 1 {
		t.Fatalf("项目卡上看不见交接: %+v", report.Handoffs)
	}
	one := report.Handoffs[0]
	if one.From != "小登" || one.To != "小新" || one.Files == 0 {
		t.Errorf("交接那一条不对: %+v", one)
	}
	// 别的项目的交接不算在这个项目头上
	if other := projectOf(newRepo(t), nil); len(other.Handoffs) != 0 {
		t.Errorf("串项目了: %+v", other.Handoffs)
	}
}

/**
 * 撤销那条提交的标题要说中文.
 *
 *	git revert --no-edit 生成的是 `Revert "原标题"` —— 整套东西的记录
 *	都在说中文, 而这一条冒出来一个英文动词. "合错了要撤得回来"
 *	时 git log 的头一条就会是这个样子. git log 是给人看的, 而"我什么
 *	时候撤过什么"恰恰是事后最要紧的那几条之一.
 */
func Test撤销那条提交说中文(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)

	write(t, repo, "temperature.py", "def c2f(c): return c*9/5+32\n")
	if _, err := runGit(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "-c", "user.name=小子", "-c", "user.email=z@x",
		"commit", "-qm", "写个温度换算的小工具"); err != nil {
		t.Fatal(err)
	}
	head, _ := runGit(repo, "rev-parse", "HEAD")

	if _, err := RevertOn(repo, strings.TrimSpace(head)); err != nil {
		t.Fatalf("撤不回来: %v", err)
	}
	title, _ := runGit(repo, "log", "-1", "--format=%s")
	title = strings.TrimSpace(title)
	if strings.HasPrefix(title, "Revert ") {
		t.Errorf("还是 git 的缺省英文标题: %q", title)
	}
	if !strings.Contains(title, "温度换算") {
		t.Errorf("没说清撤的是哪一条: %q", title)
	}
	// 撤销本身要真的生效 —— 别为了标题把功能改坏了
	if _, err := os.Stat(filepath.Join(repo, "temperature.py")); err == nil {
		t.Error("标题改对了, 东西却没撤掉")
	}
}
