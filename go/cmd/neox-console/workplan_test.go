package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这一组把 NEOX_HOME 挪到临时目录: 算工作区会真的建目录,
// 而在真人的 ~/.neox-os 里建东西是测试不该干的事.
func atHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("NEOX_HOME", home)
	return home
}

func TestPlan同一个bot问几次都是同一个答案(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	w := newWorkPlanner(func(string) bool { return false })

	p := persona{name: "小登", app: "custom-xiaodeng", work: repo}
	first, err := w.plan(p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.plan(p)
	if err != nil {
		t.Fatal(err)
	}
	// 能力、提示词、界面三处各问一次 —— 答案分叉的话, 它照着提示词去写,
	// 被能力拦下, 而用户在界面上找原因
	if first.Dir != again.Dir || first.Branch != again.Branch {
		t.Fatalf("同一个 bot 两次答案不同: %+v vs %+v", first, again)
	}
}

func TestPlan没派项目的用匿名目录(t *testing.T) {
	home := atHome(t)
	w := newWorkPlanner(nil)

	got, err := w.plan(persona{name: "小记", app: "custom-xiaoji"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Dir, realPath(filepath.Join(home, "work"))) {
		t.Fatalf("匿名目录该在 ~/.neox-os/work 底下, 拿到 %s", got.Dir)
	}
	if _, err := os.Stat(got.Dir); err != nil {
		t.Fatalf("目录没建出来: %v", err)
	}
	if got.Branch != "" {
		t.Fatal("匿名目录不该有分支 —— 那儿只有它一个人, 不需要隔离")
	}
}

func TestPlan独占模式下第二个人当场被挡住(t *testing.T) {
	needGit(t)
	atHome(t)
	// 不是 git 仓库, 又不许 init —— 只能独占
	plain := t.TempDir()
	w := newWorkPlanner(func(string) bool { return false })

	if _, err := w.plan(persona{name: "小登", app: "a", work: plain}); err != nil {
		t.Fatal(err)
	}
	_, err := w.plan(persona{name: "报表", app: "b", work: plain})
	if err == nil {
		t.Fatal("两个人分到同一个目录了 —— 会互相覆盖, 而且查不出原因")
	}
	// 挡住的时候要**说得出是谁占着、为什么、怎么办**
	msg := err.Error()
	for _, want := range []string{"小登", "git"} {
		if !strings.Contains(msg, want) {
			t.Errorf("挡住的理由里没有 %q: %s", want, msg)
		}
	}
}

func TestPlan有git时两个人都能进同一个项目(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	w := newWorkPlanner(func(string) bool { return false })

	a, err := w.plan(persona{name: "小登", app: "a", work: repo})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.plan(persona{name: "报表", app: "b", work: repo})
	if err != nil {
		t.Fatalf("有 git 就该能一起干: %v", err)
	}
	if a.Dir == b.Dir {
		t.Fatal("两个人还是分到了同一个目录")
	}
	if a.Branch == b.Branch || a.Branch == "" {
		t.Fatalf("分支没分开: %q / %q", a.Branch, b.Branch)
	}
}

func TestPlan点头之后才在别人目录里init(t *testing.T) {
	needGit(t)
	atHome(t)
	plain := t.TempDir()

	no := newWorkPlanner(func(string) bool { return false })
	got, err := no.plan(persona{name: "小登", app: "a", work: plain})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeExclusive || IsRepo(plain) {
		t.Fatal("没点头就把用户的目录变成了 git 仓库")
	}

	yes := newWorkPlanner(func(string) bool { return true })
	got, err = yes.plan(persona{name: "小登", app: "a", work: plain})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeWorktree {
		t.Fatalf("点了头还降级: %s", got.Why)
	}
}

func TestForget不动它没提交的活(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	w := newWorkPlanner(func(string) bool { return false })

	got, err := w.plan(persona{name: "小登", app: "a", work: repo})
	if err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(got.Dir, "干到一半.txt")
	if err := os.WriteFile(half, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 换工作区、重启都会走到 forget —— 那两件事都不该删它的活
	w.forget("小登")
	if _, err := os.Stat(half); err != nil {
		t.Fatalf("没提交的活被 forget 删了: %v", err)
	}
	again, err := w.plan(persona{name: "小登", app: "a", work: repo})
	if err != nil {
		t.Fatal(err)
	}
	if again.Dir != got.Dir {
		t.Fatalf("重新算之后换了地方: %s -> %s", got.Dir, again.Dir)
	}
}

/**
 * **算好的计划要先查一眼还成不成立**.
 *
 *	最危险的一类情况是项目目录被删掉重建, 同名的 bot 起回来,
 *	拿到缓存里那份旧计划 —— 它的工作区 .git 指着一个**已经不存在的
 *	仓库**, 一跑 git 就是 "fatal: not a git repository".
 *
 *	而它照样读文件、照样写代码、照样说"已经写好并验证通过" ——
 *	什么都到不了主干, 而没有一处会说这件事. 场景那边看到的是
 *	"files: 0, bad: []": 什么都没产出, 一条不变量都没响.
 */
func Test项目没了之后不再用那份旧计划(t *testing.T) {
	needGit(t)
	home := atHome(t)
	repo := newRepo(t)
	w := newWorkPlanner(nil)
	w.root = filepath.Join(home, "worktrees")

	first, err := w.plan(persona{name: "小丙", app: "x", work: repo})
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != ModeWorktree {
		t.Fatalf("第一次就没走 worktree: %+v", first)
	}
	// 项目连同它的 .git 一起没了 —— 那份 worktree 从此指着空气
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if IsRepo(first.Dir) {
		t.Fatal("仓库都删了, 那个 worktree 还认得出自己是仓库?")
	}
	// 项目目录重新建起来(用户重开了一摊, 或者场景重跑)
	repo2 := newRepoAt(t, repo)

	again, err := w.plan(persona{name: "小丙", app: "x", work: repo2})
	if err != nil {
		t.Fatal(err)
	}
	if !IsRepo(again.Dir) {
		t.Fatalf("还是把那份坏计划给了它: %+v", again)
	}
}

// 好好的计划不该被白白重算 —— 重算会真的去建 worktree
func Test计划还成立就接着用(t *testing.T) {
	needGit(t)
	home := atHome(t)
	repo := newRepo(t)
	w := newWorkPlanner(nil)
	w.root = filepath.Join(home, "worktrees")
	first, err := w.plan(persona{name: "小丁", app: "x", work: repo})
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.plan(persona{name: "小丁", app: "x", work: repo})
	if err != nil {
		t.Fatal(err)
	}
	if again.Dir != first.Dir {
		t.Fatalf("好好的计划被重算了: %s → %s", first.Dir, again.Dir)
	}
}

/**
 * **换了个项目, 就得重新算一份计划**.
 *
 *	缓存原来只按名字取, 一个字都不问"这次派的是哪儿":
 *
 *	    if got, ok := w.byBot[p.name]; ok && stillGood(got) { return got }
 *
 *	于是把一个人从财务挪到 OA, 拿回来的还是财务那份 —— **它继续在财务
 *	那边改文件, 而人以为它在 OA**. 两边都不报错, 只有去看文件才发现.
 *
 *	人明天开两个项目, 让同一个人两边帮忙是很自然的事.
 */
func Test换了项目要重新算计划(t *testing.T) {
	needGit(t)
	atHome(t)
	one, two := newRepo(t), newRepo(t)
	w := newWorkPlanner(func(string) bool { return true })

	first, err := w.plan(persona{name: "小甲", app: "a", work: one})
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.plan(persona{name: "小甲", app: "a", work: two})
	if err != nil {
		t.Fatal(err)
	}
	if second.Project == first.Project {
		t.Errorf("换了项目还是老那份: %s", second.Project)
	}
	if !strings.HasPrefix(realPath(second.Project), realPath(two)) {
		t.Errorf("新计划没指向新项目: %s", second.Project)
	}
	// 还派回原来那个项目 —— 该拿回原来那份, 别每次都重算
	back, err := w.plan(persona{name: "小甲", app: "a", work: one})
	if err != nil {
		t.Fatal(err)
	}
	if back.Project != first.Project {
		t.Errorf("派回原项目却换了一份计划: %s", back.Project)
	}
}
