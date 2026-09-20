package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandOver没合进主干的活也跟着走(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()

	from, err := Assign(repo, "小登", root, false)
	if err != nil {
		t.Fatal(err)
	}
	to, err := Assign(repo, "小新", root, false)
	if err != nil {
		t.Fatal(err)
	}

	// 干到一半: 一次提交了的, 一次还没提交的 —— **后者正是要交接的那部分**
	write(t, from.Dir, "login.py", "半成品\n")
	if _, err := CommitTurn(from.Dir, "小登", from.Branch, "搭了个架子", 1); err != nil {
		t.Fatal(err)
	}
	write(t, from.Dir, "session.py", "还没提交的\n")

	got, err := HandOver(repo, from, to, "小登", "小新")
	if err != nil {
		t.Fatal(err)
	}

	// 两样都要在接手的人手上
	for _, name := range []string{"login.py", "session.py"} {
		if _, err := os.Stat(filepath.Join(to.Dir, name)); err != nil {
			t.Errorf("%s 没跟着交接过来 —— 那正是干到一半的那部分", name)
		}
	}
	if got.Commits < 2 {
		t.Errorf("接到的提交数不对: %d", got.Commits)
	}
	// 说清楚接到了什么 + 第一步干什么
	// **话要短**: 说清三件事就够 —— 谁交的、动过什么、别重来
	for _, want := range []string{"小登", "login.py", "别重来"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("交接说明里少了 %q:\n%s", want, got.Message)
		}
	}
}

func TestHandOver前一个人的产物不删(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	from, _ := Assign(repo, "小登", root, false)
	to, _ := Assign(repo, "小新", root, false)

	write(t, from.Dir, "a.py", "x\n")
	if _, err := CommitTurn(from.Dir, "小登", from.Branch, "干了点活", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := HandOver(repo, from, to, "小登", "小新"); err != nil {
		t.Fatal(err)
	}
	// 交接不是删除: 他做过的东西留在他名下, 那既是产物也是记录
	if !branchExists(repo, from.Branch) {
		t.Fatal("前一个人的分支被删了")
	}
	if CommitsBy(repo, "小登") == 0 {
		t.Fatal("前一个人的提交记录没了 —— 那是他干过活的唯一证据")
	}
}

// **判据是"还没合掉的", 不是"干过活的"** —— 合进主干就是"这摊交代完了",
// 那之后他本来就该能接新的. 按"干过活"判的话, 一个人提交过一次就永远
// 不能再接手别人的活.
func TestHandOver合掉之后就能接新的(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	from, _ := Assign(repo, "小登", root, false)
	to, _ := Assign(repo, "小新", root, false)

	// 小新干过一摊, 而且**已经合进主干了**
	write(t, to.Dir, "old.py", "小新以前干的\n")
	if _, err := CommitTurn(to.Dir, "小新", to.Branch, "以前那摊", 1); err != nil {
		t.Fatal(err)
	}
	if got, err := MergeUp(to.Project, to.Dir, to.Branch, "小新"); err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}

	write(t, from.Dir, "new.py", "小登干到一半的\n")
	if _, err := CommitTurn(from.Dir, "小登", from.Branch, "干到一半", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := HandOver(repo, from, to, "小登", "小新"); err != nil {
		t.Fatalf("合掉之后还是接不了: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to.Dir, "new.py")); err != nil {
		t.Fatal("没接过来")
	}
}

func TestHandOver接手的人自己有活时不许硬接(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	from, _ := Assign(repo, "小登", root, false)
	to, _ := Assign(repo, "小新", root, false)

	write(t, from.Dir, "a.py", "x\n")
	if _, err := CommitTurn(from.Dir, "小登", from.Branch, "小登干的", 1); err != nil {
		t.Fatal(err)
	}
	// 小新自己也在干活 —— 把基点挪走就是把他的活弄丢
	write(t, to.Dir, "b.py", "小新自己的\n")
	if _, err := CommitTurn(to.Dir, "小新", to.Branch, "小新干的", 1); err != nil {
		t.Fatal(err)
	}

	_, err := HandOver(repo, from, to, "小登", "小新")
	if err == nil {
		t.Fatal("把接手人自己的活覆盖掉了")
	}
	if !strings.Contains(err.Error(), "小新") {
		t.Errorf("没说清是谁手上有活: %v", err)
	}
	// 他的活必须还在
	if _, err := os.Stat(filepath.Join(to.Dir, "b.py")); err != nil {
		t.Fatal("接手人自己的活被弄丢了")
	}
}

func TestHandOver跨项目交不过去(t *testing.T) {
	needGit(t)
	atHome(t)
	root := t.TempDir()
	a := newRepo(t)
	b := newRepo(t)
	from, _ := Assign(a, "小登", root, false)
	to, _ := Assign(b, "小新", root, false)

	if _, err := HandOver(a, from, to, "小登", "小新"); err == nil {
		t.Fatal("跨项目也交接成功了 —— 那会把另一个项目的代码盖掉")
	}
}

/**
 * 交接那次提交的标题要**说出这次发生了什么**.
 *
 *	这是整份记录里唯一一次明确知道情况的提交: 谁把活交给了谁.
 *	而它原来写的是"交接前把手上的活收一下" —— "活到一半换人"
 *	那一轮的 git log 三条里两条是这种套话, 谁在什么时候干了什么一个字
 *	看不出来. 而 git log 正是接手的人第一眼要看的东西.
 */
func Test交接那次提交要说清交给了谁(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()

	from, err := Assign(repo, "小壬", root, false)
	if err != nil {
		t.Fatal(err)
	}
	to, err := Assign(repo, "小癸", root, false)
	if err != nil {
		t.Fatal(err)
	}
	// 手上有没提交的活 —— 交接最要紧的正是这部分, 也正是这次提交收的
	write(t, from.Dir, "干到一半.py", "# 还没写完\n")

	if _, err := HandOver(repo, from, to, "小壬", "小癸"); err != nil {
		t.Fatal(err)
	}
	title := lastTitle(t, to.Dir, to.Branch)
	if !strings.Contains(title, "小癸") {
		t.Errorf("标题里没说交给了谁: %q", title)
	}
	if strings.HasPrefix(title, "交接前把手上的活") {
		t.Errorf("还是那句套话: %q", title)
	}
}

func lastTitle(t *testing.T, dir, branch string) string {
	t.Helper()
	out, err := runGit(dir, "log", "-1", "--format=%s", branch)
	if err != nil {
		t.Fatalf("读不到提交标题: %v", err)
	}
	return strings.TrimSpace(out)
}

/**
 * 接手那一轮的提交也要说清它在干什么.
 *
 *	接手的人是被"[系统] 小壬这摊活交给你了"叫起来的, 而系统那一声按
 *	规矩不当提交标题(见 TurnNote.title). 小癸干完一整个 bookmarks
 *	包(13 个文件), git log 上写的是"同步前把手上的活收一下".
 *
 *	但这一次宿主知道它在干什么 —— 那就直接说, 用不着让模型猜, 也用不着
 *	在给它看的话里塞记号.
 */
func Test接手那一轮的标题由宿主先定下来(t *testing.T) {
	c := &components{}
	c.willBe("小癸", "接手小壬的活")

	// BeforeTurn 送来的是那句系统通知 —— 它不该盖掉宿主定好的
	c.nowDoing("小癸", "[系统] 小壬这摊活交给你了，他干的都在你分支上")
	if got := c.doing["小癸"]; got != "接手小壬的活" {
		t.Errorf("接手那一轮的标题成了 %q", got)
	}
	// **用一次就丢**: 粘在下一轮上的话, 之后每一次提交都写着"接手小壬的活"
	c.nowDoing("小癸", "再加个导出功能")
	if got := c.doing["小癸"]; got != "再加个导出功能" {
		t.Errorf("下一轮还粘着上一轮的标题: %q", got)
	}
	// 没定过的人照旧走原路
	c.nowDoing("小壬", "写个书签管理")
	if got := c.doing["小壬"]; got != "写个书签管理" {
		t.Errorf("没定过的人也被改了: %q", got)
	}
}
