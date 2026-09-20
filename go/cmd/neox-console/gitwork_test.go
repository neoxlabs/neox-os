package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 这一组测试跑的是**真的 git**: 隔离这件事的判据全在 git 的真实行为上
// (一个分支同时只能被一个 worktree 签出、空仓库建不了 worktree),
// 用假的 git 去验证等于在验证我对 git 的想象.
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("这台机器没有 git")
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("建不起仓库: %v", err)
	}
	return dir
}

// newRepoAt 在**指定路径**上重开一个仓库 —— 模拟"用户把那摊活删了重来"
func newRepoAt(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatalf("建不起仓库: %v", err)
	}
	return dir
}

func TestAssign两个bot各拿一块互不重叠(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	root := t.TempDir()

	a, err := Assign(repo, "小登", root, false)
	if err != nil {
		t.Fatalf("小登: %v", err)
	}
	b, err := Assign(repo, "报表", root, false)
	if err != nil {
		t.Fatalf("报表: %v", err)
	}
	if a.Mode != ModeWorktree || b.Mode != ModeWorktree {
		t.Fatalf("该走 worktree, 拿到 %q / %q (%s / %s)", a.Mode, b.Mode, a.Why, b.Why)
	}
	if a.Dir == b.Dir {
		t.Fatalf("两个人分到同一个目录 %s —— 隔离没了", a.Dir)
	}
	if a.Branch == b.Branch {
		t.Fatalf("两个人分到同一条分支 %s", a.Branch)
	}
	// **一个人的改动不该出现在另一个人脚底下**
	if err := os.WriteFile(filepath.Join(a.Dir, "只有小登的.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "只有小登的.txt")); !os.IsNotExist(err) {
		t.Fatal("小登写的文件出现在报表的工作目录里")
	}
}

func TestAssign同一个bot重启接着用原来那块(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	root := t.TempDir()

	first, err := Assign(repo, "小登", root, false)
	if err != nil {
		t.Fatal(err)
	}
	// 干到一半的活: 重启之后必须还在
	scratch := filepath.Join(first.Dir, "干到一半.txt")
	if err := os.WriteFile(scratch, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := Assign(repo, "小登", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Dir != first.Dir {
		t.Fatalf("重启之后换了地方: %s -> %s", first.Dir, again.Dir)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("没提交的活丢了: %v", err)
	}
}

/**
 * 分支前缀变化后, 老 worktree 仍可能签在 neox/… 上 —— 重启后必须
 * **装得回来**.
 *
 *	按新名字 worktree add 会撞 "already exists", spawn 失败, 交办时会只剩
 *	一个可用成员. 覆盖和报错都不对(那上面可能有没提交完的活), 应认出它并
 *	使用它签着的老分支.
 */
func TestAssign老前缀的worktree重启装得回来(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	root := t.TempDir()

	// 造一块"老前缀时代"的 worktree: 目录就在计划器会算出的那一格
	dir := filepath.Join(root, projectSlug(repo), botSlug("小规"))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "worktree", "add", "-b", "neox/小规", dir, "HEAD"); err != nil {
		t.Fatalf("造不出老 worktree: %v", err)
	}
	// 干到一半的活
	scratch := filepath.Join(dir, "干到一半.txt")
	if err := os.WriteFile(scratch, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := Assign(repo, "小规", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != ModeWorktree {
		t.Fatalf("装不回来了: mode=%q why=%q", plan.Mode, plan.Why)
	}
	if realPath(plan.Dir) != realPath(dir) {
		t.Fatalf("换了地方: %s -> %s", dir, plan.Dir)
	}
	if plan.Branch != "neox/小规" {
		t.Fatalf("该沿用老分支, 拿到 %q", plan.Branch)
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatalf("没提交的活丢了: %v", err)
	}
}

func TestAssign不是仓库时不许自作主张init(t *testing.T) {
	needGit(t)
	plain := t.TempDir()
	root := t.TempDir()

	plan, err := Assign(plain, "小登", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != ModeExclusive {
		t.Fatalf("没点头就 init 了别人的目录: %+v", plan)
	}
	if IsRepo(plain) {
		t.Fatal("在用户目录里凭空建了个 git 仓库")
	}
	if plan.Why == "" {
		t.Fatal("降级了却说不出为什么 —— 用户只会看到'怎么跟说好的不一样'")
	}
	// 比的是解过链接的路径: macOS 上 t.TempDir() 给的是 /var/..., 而
	// /var 是 /private/var 的链接 —— 同一块地方, 两个写法(见 realPath)
	if plan.Dir != realPath(plain) {
		t.Fatalf("独占模式下就该在项目目录里干活, 拿到 %s", plan.Dir)
	}
}

func TestAssign点头之后才init(t *testing.T) {
	needGit(t)
	plain := t.TempDir()
	root := t.TempDir()

	plan, err := Assign(plain, "小登", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != ModeWorktree {
		t.Fatalf("点了头还降级: %s", plan.Why)
	}
	if !IsRepo(plain) {
		t.Fatal("说好要 init 的")
	}
}

func TestAssign空仓库先补一个提交(t *testing.T) {
	needGit(t)
	// git init 之后什么都没提交 —— 这种仓库是**建不了 worktree** 的
	empty := t.TempDir()
	if _, err := runGit(empty, "init"); err != nil {
		t.Fatal(err)
	}
	plan, err := Assign(empty, "小登", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != ModeWorktree {
		t.Fatalf("空仓库没接住: %s", plan.Why)
	}
}

func TestAssign不碰用户没提交的改动(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	mine := filepath.Join(repo, "我正在改的.txt")
	if err := os.WriteFile(mine, []byte("我的"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Dirty(repo) {
		t.Fatal("这时候该是脏的")
	}
	if _, err := Assign(repo, "小登", t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(mine)
	if err != nil || string(got) != "我的" {
		t.Fatalf("用户手上的改动被动了: %v %q", err, string(got))
	}
}

func TestRelease收回目录但留下分支(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	// 它写过的东西 —— 提交进自己的分支
	if err := os.WriteFile(filepath.Join(plan.Dir, "产物.txt"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(plan.Dir, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(plan.Dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "活干完了"); err != nil {
		t.Fatal(err)
	}
	if err := Release(repo, "小登"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Dir); !os.IsNotExist(err) {
		t.Fatal("worktree 该收回")
	}
	// **分支必须还在**: 人删掉一个 bot 常常只是不想再看见它,
	// 而它写过的代码不该跟着消失
	if !branchExists(repo, plan.Branch) {
		t.Fatalf("分支 %s 跟着没了 —— 产物丢了", plan.Branch)
	}
	out, err := runGit(repo, "show", "--stat", "--oneline", plan.Branch)
	if err != nil || !strings.Contains(out, "产物.txt") {
		t.Fatalf("分支上找不到它的产物: %v %s", err, out)
	}
}

func TestBranchOf名字里的怪字符不进分支名(t *testing.T) {
	cases := map[string]string{
		"小登":       "bot/小登",
		"a b":      "bot/a-b",
		"x~y^z":    "bot/x-y-z",
		"  报表  ":   "bot/报表",
		"a:b?c[d]": "bot/a-b-c-d",
	}
	for raw, want := range cases {
		if got := BranchOf(raw); got != want {
			t.Errorf("BranchOf(%q) = %q, 要 %q", raw, got, want)
		}
	}
	/**
	 * **清完是空的那几个, 不许都叫同一个名字**.
	 *
	 *	这两条原来写死"neox/bot" —— 而那正是要修的东西: 名字全是表情或
	 *	符号的 bot 有两个, 就挤在同一条分支上, 甲的活和乙的活混进同一
	 *	条线, 合入时谁都看不出来.
	 *
	 *	所以这里断言的是**性质**, 不是那个字面值: 定、不撞、不带怪字符.
	 */
	for _, raw := range []string{"", "...", "🤖", "🎯"} {
		got := BranchOf(raw)
		if got != BranchOf(raw) {
			t.Errorf("BranchOf(%q) 两次不一样", raw)
		}
		if strings.ContainsAny(got, " ~^:?[]\\") {
			t.Errorf("BranchOf(%q) = %q 带着 git 不认的字符", raw, got)
		}
	}
	if BranchOf("🤖") == BranchOf("🎯") {
		t.Errorf("两个不同的名字挤在同一条分支: %s", BranchOf("🤖"))
	}
}

func TestProjectSlug带上父目录名免得撞车(t *testing.T) {
	// 一台机器上叫 oa 的项目可以有好几个 —— 光看最后一段会撞在一起,
	// 撞了之后两个项目的 bot 会共用一块地方
	a := projectSlug("/Users/x/AI/test-projects/oa")
	b := projectSlug("/Users/x/work/oa")
	if a == b {
		t.Fatalf("两个不同项目算出同一格: %s", a)
	}
	if !strings.Contains(a, "oa") {
		t.Fatalf("认不出是哪个项目: %s", a)
	}
}

/**
 * **死掉的 worktree 要清掉重来**.
 *
 *	项目被删了重建之后, 上一次留下的目录还在, 里面的 .git 指着一个
 *	不存在的仓库. 不清的话 worktree add 会因为"目录非空"失败, 整份计划
 *	掉回独占模式 —— 一个本该有自己分支的人从此跟别人抢同一个目录.
 *
 *	更糟的结果是拿着那份坏计划完成一整轮, 声称"已经写好并验证通过",
 *	而主干一个文件都没多.
 */
func TestAssign死掉的worktree清掉重来(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	first, err := Assign(repo, "小丙", root, false)
	if err != nil || first.Mode != ModeWorktree {
		t.Fatalf("%v %+v", err, first)
	}
	// 项目连 .git 一起没了, 又在原地重开了一摊
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	again := newRepoAt(t, repo)
	if partOf(again, first.Dir) {
		t.Fatal("仓库都换了, 那块地方还认得出是它的 worktree?")
	}

	got, err := Assign(again, "小丙", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeWorktree {
		t.Fatalf("没重新分一块, 掉回独占了: %+v", got)
	}
	if !partOf(again, got.Dir) {
		t.Fatalf("分到的还是块死的: %+v", got)
	}
}

// **活着的不许动** —— 那里面可能有它没提交的活
func TestAssign活着的worktree不碰(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	first, _ := Assign(repo, "小丁", root, false)
	if err := os.WriteFile(filepath.Join(first.Dir, "干到一半.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := Assign(repo, "小丁", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Dir != first.Dir {
		t.Fatalf("换了个地方: %s → %s", first.Dir, again.Dir)
	}
	if _, err := os.Stat(filepath.Join(first.Dir, "干到一半.txt")); err != nil {
		t.Fatal("把它没提交的活清掉了")
	}
}

/**
 * **人自己的仓库也要打点** —— 它走不到 Adopt.
 *
 *	`Adopt` 只处理还不是仓库的项目, 已经有历史的仓库也必须经过整理,
 *	否则工具缓存等规矩无法生效. 把 bot 放进一个有历史的项目, 48 个
 *	Library/Caches/…/*.pyc
 *	进了版本控制.
 *
 *	housekeep 必须放在每个项目都必经的地方; 只放在 Adopt 里, 已有历史的
 *	仓库仍会保留这 48 个文件.
 */
func Test人自己的仓库也要打点(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t) // 已经有历史的仓库, 走不到 Adopt
	root := t.TempDir()

	if _, err := Assign(repo, "小巳", root, true); err != nil {
		t.Fatalf("派不进去: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "info", "attributes")); err != nil {
		t.Errorf("规矩没打上: %v", err)
	}
	// 真正要挡住的那件事
	os.MkdirAll(filepath.Join(repo, "Library", "Caches"), 0o755)
	write(t, repo, "Library/Caches/x.pyc", "垃圾")
	out, _ := runGit(repo, "status", "--porcelain", "--untracked-files=all")
	if strings.Contains(out, "Library/") {
		t.Errorf("工具缓存还是会被收进去:\n%s", out)
	}
}

/**
 * **两个同名的项目不许共用一块地皮**.
 *
 *	worktree 放在 <root>/<项目名>/<人名> 下, 而项目名原来只取"父目录-
 *	目录名". 于是 ~/工作/公司/财务 和 ~/备份/公司/财务 算出来一模一样.
 *
 *	撞上之后不是"挤在一起"那么轻: Assign 里那段发现"这块地方不是这个
 *	仓库的 worktree"就会**清掉重来** —— 而被清掉的是另一个项目里那个人
 *	**没提交完的活**. 两个同名项目交替派活就是反复互删.
 */
func Test同名项目不许共用一块地皮(t *testing.T) {
	one := filepath.Join(t.TempDir(), "公司", "财务")
	two := filepath.Join(t.TempDir(), "公司", "财务")
	for _, d := range []string{one, two} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if projectSlug(one) == projectSlug(two) {
		t.Errorf("两个不同项目算出同一块地皮: %s", projectSlug(one))
	}
	// 同一个项目每次都要算出同一个 —— 否则重启之后满地都是孤儿目录
	if projectSlug(one) != projectSlug(one) {
		t.Error("同一个项目两次算出不一样的")
	}
	// 名字还得看得出是哪个项目 —— 一串纯哈希没法排查
	if !strings.Contains(projectSlug(one), "财务") {
		t.Errorf("看不出是哪个项目: %s", projectSlug(one))
	}
}

/**
 * **名字被清空之后, 兜底那个名字必须是定的**.
 *
 *	safeName 对全是符号的名字清洗后会得到空字符串, 如果兜底返回
 *	x<纳秒>, 就会**每次派活都算出一块新地皮** —— 上一次没提交完的活
 *	留在旧目录里成了孤儿, 而它自己一无所知.
 *
 *	BranchOf 同样的情况一律返回 neox/bot: 这样的 bot 有两个, 就挤在
 *	同一条分支上 —— 甲的活和乙的活混进同一条线, 合入时谁都看不出来.
 */
func Test名字被清空也要算得稳(t *testing.T) {
	// 同一个名字, 每次都要一样
	if safeName("🤖") != safeName("🤖") {
		t.Error("同一个名字两次算出不一样的地皮 —— 上次的活成孤儿了")
	}
	if BranchOf("🤖") != BranchOf("🤖") {
		t.Error("同一个名字两次算出不一样的分支")
	}
	// 不同的名字, 不许撞
	if safeName("🤖") == safeName("🎯") {
		t.Errorf("两个不同的名字共用一块地皮: %s", safeName("🤖"))
	}
	if BranchOf("🤖") == BranchOf("🎯") {
		t.Errorf("两个不同的名字挤在同一条分支: %s", BranchOf("🤖"))
	}
	// 正常名字照旧看得懂 —— 别把这条修成"一律哈希"
	if got := safeName("小甲"); got != "小甲" {
		t.Errorf("正常名字被改了: %s", got)
	}
	if got := BranchOf("小甲"); got != "bot/小甲" {
		t.Errorf("正常分支名被改了: %s", got)
	}
}
