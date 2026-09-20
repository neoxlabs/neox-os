package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	if _, err := runGit(dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", msg); err != nil {
		t.Fatal(err)
	}
}

func TestMergeUp干完的活自己合进主干(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	write(t, plan.Dir, "login.py", "def login(): pass\n")
	commit(t, plan.Dir, "搭登录")

	got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小登")
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("该合进去的: %s", got.Message)
	}
	// 主干上要真的有这个文件 —— 不然"合进去了"就是句空话
	if _, err := os.Stat(filepath.Join(repo, "login.py")); err != nil {
		t.Fatalf("主干上没有它的产物: %v", err)
	}
}

func TestMergeUp冲突了不停下来等人而是让它自己解(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	write(t, repo, "共用的.py", "原来这样\n")
	commit(t, repo, "起个头")

	a, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Assign(repo, "报表", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	// 两个人改同一行 —— 这正是"分头干"最常撞上的那件事
	write(t, a.Dir, "共用的.py", "小登改的\n")
	commit(t, a.Dir, "小登改了")
	write(t, b.Dir, "共用的.py", "报表改的\n")
	commit(t, b.Dir, "报表改了")

	if got, err := MergeUp(a.Project, a.Dir, a.Branch, "小登"); err != nil || !got.OK {
		t.Fatalf("先到的该顺利合进去: %v %+v", err, got)
	}

	got, err := MergeUp(b.Project, b.Dir, b.Branch, "报表")
	if err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatal("冲突了却说合成功了")
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != "共用的.py" {
		t.Fatalf("没说清哪个文件冲突: %+v", got)
	}
	// **冲突要落在它自己的工作区里**: 它有写权限, 读得到改得动
	body, err := os.ReadFile(filepath.Join(b.Dir, "共用的.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<<<<<<<") {
		t.Fatal("冲突标记不在它自己的文件里 —— 那它拿什么解")
	}
	// 主干**不许被动过**: 没解完就合上去, 主干上就是一堆冲突标记
	main, err := os.ReadFile(filepath.Join(repo, "共用的.py"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(main), "<<<<<<<") {
		t.Fatal("主干上出现了冲突标记")
	}

	// ── 它解冲突的方式是**改文件**(那是它做得到的), 收尾归宿主 ──
	write(t, b.Dir, "共用的.py", "两边的意思都留下\n")
	again, err := MergeUp(b.Project, b.Dir, b.Branch, "报表")
	if err != nil {
		t.Fatal(err)
	}
	if !again.OK {
		t.Fatalf("解完了还合不上: %s", again.Message)
	}
	final, err := os.ReadFile(filepath.Join(repo, "共用的.py"))
	if err != nil || !strings.Contains(string(final), "两边的意思都留下") {
		t.Fatalf("主干上不是解完的那一版: %q", string(final))
	}
}

// **进程不能直接提交**:
//
// worktree 里的 .git 只是个指针文件, 真正的 git 目录在项目根的
// .git/worktrees/<它> 里, 而那不在它的写能力范围内. `git add` 会得到
// "Operation not permitted", 直接放开整个项目根的写权限就会破坏隔离。
//
// 放开那个权限等于让它能改别人的分支引用, 隔离就白做了. 所以反过来:
// 它只管改文件, git 的事由宿主代理.
func TestMergeUp手上没提交的活由宿主替它收(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	write(t, plan.Dir, "半成品.py", "wip\n")

	got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小登")
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("该替它收起来再合的: %s", got.Message)
	}
	if _, err := os.Stat(filepath.Join(repo, "半成品.py")); err != nil {
		t.Fatal("它改的东西没合进主干")
	}
	// 收的那一笔要记在它名下 —— 谁改的就是谁改的
	who, err := runGit(repo, "log", "-1", "--pretty=%an", "--", "半成品.py")
	if err != nil || strings.TrimSpace(who) != "小登" {
		t.Fatalf("作者是 %q, 该是小登", strings.TrimSpace(who))
	}
}

func TestMergeUp六个人同时合不会互相踩(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()

	names := []string{"甲", "乙", "丙", "丁", "戊", "己"}
	plans := make([]Plan, 0, len(names))
	for _, name := range names {
		plan, err := Assign(repo, name, root, false)
		if err != nil {
			t.Fatal(err)
		}
		// 各改各的文件: 内容上不冲突, 冲的是**同时合进主干**这件事本身
		write(t, plan.Dir, name+".txt", name+"写的\n")
		commit(t, plan.Dir, name+"干完了")
		plans = append(plans, plan)
	}

	var wg sync.WaitGroup
	results := make([]MergeResult, len(plans))
	for i, plan := range plans {
		wg.Add(1)
		go func(i int, plan Plan) {
			defer wg.Done()
			// 撞上"有人同时在合"时重试 —— 工具的说明里也是这么告诉模型的
			for attempt := 0; attempt < 5; attempt++ {
				got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, names[i])
				if err == nil && got.OK {
					results[i] = got
					return
				}
				results[i] = got
			}
		}(i, plan)
	}
	wg.Wait()

	for i, got := range results {
		if !got.OK {
			t.Errorf("%s 没合进去: %s", names[i], got.Message)
		}
		if _, err := os.Stat(filepath.Join(repo, names[i]+".txt")); err != nil {
			t.Errorf("主干上没有 %s 的产物", names[i])
		}
	}
}

func TestSyncDown拿主干上的新东西不用先合自己(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	a, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Assign(repo, "报表", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	write(t, a.Dir, "db.py", "共用的建表\n")
	commit(t, a.Dir, "建表")
	if got, err := MergeUp(a.Project, a.Dir, a.Branch, "小登"); err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}

	// 报表手上有自己的半成品(已提交), 它只想拿到别人刚合进去的建表
	write(t, b.Dir, "report.py", "我的半成品\n")
	commit(t, b.Dir, "半成品")
	got, err := SyncDown(b.Project, b.Dir, b.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("同步没成: %s", got.Message)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "db.py")); err != nil {
		t.Fatal("别人合进主干的东西没同步过来")
	}
	// 它自己的半成品**不许被同步弄丢**
	if _, err := os.Stat(filepath.Join(b.Dir, "report.py")); err != nil {
		t.Fatal("自己的活丢了")
	}
	// 主干上**不该**出现它的半成品 —— 同步是单向的
	if _, err := os.Stat(filepath.Join(repo, "report.py")); !os.IsNotExist(err) {
		t.Fatal("同步把它的半成品推上主干了")
	}
}

// **每个人只验自己那份, 合起来坏了没人会发现** —— 这是多人干活最经典的坑.
//
// MergeUp 的第 1 步已经把主干合进它自己了, 所以它的 worktree 此刻就是
// 合并之后的状态: 在那儿验一遍, 就是在验主干将要变成的样子.
func TestMergeUp带进别人的改动时要求先验一遍(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	a, _ := Assign(repo, "小登", root, false)
	b, _ := Assign(repo, "报表", root, false)

	// 小登先合进去一点东西 —— 于是主干动了
	write(t, a.Dir, "db.py", "小登建的表\n")
	commit(t, a.Dir, "建表")
	if got, err := MergeUp(a.Project, a.Dir, a.Branch, "小登"); err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}

	// 报表这时候要合: 它手上这份还没见过小登的改动
	write(t, b.Dir, "report.py", "报表写的\n")
	commit(t, b.Dir, "写报表")

	first, err := MergeUpChecked(b.Project, b.Dir, b.Branch, "报表", TurnNote{})
	if err != nil {
		t.Fatal(err)
	}
	if first.OK {
		t.Fatal("带进了别人的改动却没要求验一遍")
	}
	if !strings.Contains(first.Message, "合并之后") {
		t.Fatalf("没说清它现在这份是什么: %s", first.Message)
	}

	// **同一件事只说一次**: 项目里可能根本没有测试, 一个提不出证据就
	// 永远合不进去的规矩, 会把人逼到去伪造证据
	again, err := MergeUpChecked(b.Project, b.Dir, b.Branch, "报表", TurnNote{})
	if err != nil {
		t.Fatal(err)
	}
	if !again.OK {
		t.Fatalf("拦了第二次: %s", again.Message)
	}
}

func TestMergeUp这一轮测试没过就不许合(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()
	a, _ := Assign(repo, "小登", root, false)
	b, _ := Assign(repo, "报表", root, false)

	write(t, a.Dir, "db.py", "小登建的表\n")
	commit(t, a.Dir, "建表")
	if _, err := MergeUp(a.Project, a.Dir, a.Branch, "小登"); err != nil {
		t.Fatal(err)
	}
	write(t, b.Dir, "report.py", "报表写的\n")
	commit(t, b.Dir, "写报表")

	// 它这一轮跑过测试, 没过 —— 这时候合上去, 坏的是所有人的主干
	got, err := MergeUpChecked(b.Project, b.Dir, b.Branch, "报表",
		TurnNote{Verified: []verifyRun{{Cmd: "go test ./...", Exit: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatal("测试没过还是合进去了")
	}
	if !strings.Contains(got.Message, "go test") {
		t.Fatalf("没说清是哪条没过: %s", got.Message)
	}
	// 主干不许被动过
	if _, err := os.Stat(filepath.Join(repo, "report.py")); !os.IsNotExist(err) {
		t.Fatal("没过的东西合进主干了")
	}
}

func TestLastFailure看最后一条不是有没有失败过(t *testing.T) {
	// 改一次跑一次是常态: 前面红后面绿说明它修好了 ——
	// 拿早先那次红去拦人, 它会以为怎么修都没用
	fixed := []verifyRun{{Cmd: "go test", Exit: 1}, {Cmd: "go test", Exit: 0}}
	if lastFailure(fixed) != nil {
		t.Error("修好了还被当成没过")
	}
	broke := []verifyRun{{Cmd: "go test", Exit: 0}, {Cmd: "go test", Exit: 2}}
	if got := lastFailure(broke); got == nil || got.Exit != 2 {
		t.Errorf("最后一条红的没认出来: %+v", got)
	}
	if lastFailure(nil) != nil {
		t.Error("什么都没跑却说失败了")
	}
}

/**
 * git log 得说得出**谁在什么时候干了什么**.
 *
 *	三个 bot 完成后, 如果提交标题都使用同一个兜底文本, 记录会变成:
 *	    db6b387 小勤 合入前把手上的活收一下
 *	    1262b82 接口 合入前把手上的活收一下
 *	代码全对、测试全绿, 而这份记录一个字都没说 —— 而它恰恰是"产物归谁"
 *	的全部依据, 也是交接时接手人第一眼要看的东西.
 */
func TestMergeUp收活时拿这一轮在干什么当标题(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	b, err := Assign(repo, "小勤", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	write(t, b.Dir, "notice.py", "公告模块\n")

	got, err := MergeUpChecked(b.Project, b.Dir, b.Branch, "小勤", TurnNote{
		Doing:    "在 oa 里加「公告」模块：发布/列表两个接口 + 一份测试\n然后合进主干",
		Verified: []verifyRun{{Cmd: "python3 -m unittest", Exit: 0}},
	})
	if err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}
	body, err := runGit(b.Dir, "log", "--pretty=%B", "-3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "公告") {
		t.Errorf("提交标题里没有这一轮在干什么:\n%s", body)
	}
	// **只取头一行**: 用户交代的话可能有好几段, 标题不是正文
	if strings.Contains(body, "然后合进主干") {
		t.Errorf("把整段交代都塞进标题了:\n%s", body)
	}
	// 证据也要跟着落进去 —— 原来这条路上是空的
	if !strings.Contains(body, "Neox-Verify") {
		t.Errorf("验证证据没进提交:\n%s", body)
	}
}

func TestTurnNote没交代过就退回缺省(t *testing.T) {
	if got := (TurnNote{}).title("合入前把手上的活收一下"); got != "合入前把手上的活收一下" {
		t.Fatalf("%q", got)
	}
	if got := (TurnNote{Doing: "  \n "}).title("兜底"); got != "兜底" {
		t.Fatalf("空白当成了标题: %q", got)
	}
}

/**
 * 证据漏在了一个很常见的顺序上: 写完 → 提交(还没验) → 跑测试 → merge_up.
 * 到 merge_up 的时候手上已经没有没提交的东西, 于是这一轮验过什么一个字
 * 都没留下 —— 闸门照样拦得住(它读内存里那份), 但事后翻 git log 看不出
 * 这条改动验没验过. 而"事后翻得出来"正是把证据写进提交的全部理由.
 */
func TestMergeUp先提交后验证证据也要落进去(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, _ := Assign(repo, "小登", t.TempDir(), false)
	write(t, plan.Dir, "login.py", "写完了\n")
	// 先提交 —— 这时候还没验
	if _, err := CommitTurn(plan.Dir, "小登", plan.Branch, "搭登录", 1); err != nil {
		t.Fatal(err)
	}
	// 然后才跑的测试
	got, err := MergeUpChecked(plan.Project, plan.Dir, plan.Branch, "小登",
		TurnNote{Verified: []verifyRun{{Cmd: "python3 -m unittest", Exit: 0}}})
	if err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}
	body, err := runGit(repo, "log", "--pretty=%B", "-3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Neox-Verify") || !strings.Contains(body, "unittest") {
		t.Errorf("证据没落进提交:\n%s", body)
	}
}

// **已经合进主干的那条不许动** —— 改写别人拉过的历史, 代价是所有人
// 下次 sync_down 撞上一堆莫名其妙的冲突
func TestAttachEvidence不动已经合进主干的(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, _ := Assign(repo, "小登", t.TempDir(), false)
	write(t, plan.Dir, "a.py", "x\n")
	if _, err := CommitTurn(plan.Dir, "小登", plan.Branch, "干活", 1); err != nil {
		t.Fatal(err)
	}
	if got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小登"); err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}
	before, _ := runGit(plan.Dir, "rev-parse", plan.Branch)
	attachEvidence(plan.Dir, plan.Branch, mainBranch(repo), []verifyRun{{Cmd: "go test", Exit: 0}})
	after, _ := runGit(plan.Dir, "rev-parse", plan.Branch)
	if before != after {
		t.Fatal("改写了已经在主干上的那条提交")
	}
}

/**
 * 提交标题要**先把信封拆掉**.
 *
 *	例如: `a997c8a 报表 │ [说话的是「阿导」—— 这是界面上
 *	设的名字]…` —— 那是给模型看的记号, 不是这一轮在干什么, 而 git log
 *	是给人看的.
 */
func TestTurnNote标题要拆掉信封(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[说话的是「阿导」—— 这是用户在界面上设的名字]\n把 budget.py 补出来", "把 budget.py 补出来"},
		{"[房间「finance」里你没看到的几句]\n报表: 我做了 cli\n" + roomContextTail + "\n把预算这块做完", "把预算这块做完"},
		{"就是一句普通的话", "就是一句普通的话"},
	}
	for _, one := range cases {
		if got := (TurnNote{Doing: one.in}).title("兜底"); got != one.want {
			t.Errorf("拆错了:\n给的 %q\n拿到 %q\n要 %q", one.in, got, one.want)
		}
	}
	// 拆完什么都不剩就退回缺省, 不留一个空标题
	if got := (TurnNote{Doing: "[只有一个记号]"}).title("兜底"); got != "兜底" {
		t.Errorf("%q", got)
	}
	/**
	 * **系统自己叫的那一声不算"这一轮在干什么"**.
	 *
	 *	如果把系统提醒当成标题, finance 的 git log 会变成三条一模一样的:
	 *	  866781f 老账 │ 你申请的那个权限批下来了。**接着把刚才那件事做完**…
	 *	那是催促, 不是内容.
	 */
	nudge := "[系统] 你申请的那个权限批下来了。**接着把刚才那件事做完**"
	if got := (TurnNote{Doing: nudge}).title("合入前把手上的活收一下"); got != "合入前把手上的活收一下" {
		t.Errorf("系统那一声成了提交标题: %q", got)
	}
}

/**
 * **bot 干活、bot 自助合并这条路上, 不该有"工作目录"这个变量**.
 *
 *	`git merge --ff-only` 要动主干那个工作目录, 于是那里任何没提交的
 *	东西都能挡住它 —— npm install 改一行锁文件、服务写一个数据文件、
 *	谁 build 了一次. 一挡就是**全屋人都合不进去**, 而 bot 一个字都
 *	改不了(主干不在它的写范围里).
 *
 *	而这条路根本不需要那个目录: 快进就是把 master 这个指针往前挪.
 *	所以现在只 update-ref —— 它不碰工作目录, 也就永远不会被它挡住.
 */
func TestMergeUp主干目录脏着照样合得进去(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, _ := Assign(repo, "小前", t.TempDir(), false)
	write(t, repo, "package-lock.json", "{}\n")
	commit(t, repo, "先有个锁文件")
	for i := 0; i < 2; i++ {
		if _, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小前"); err != nil {
			t.Fatal(err)
		}
	}
	// 主干目录里有人动过东西, 而且动的正是这次合并要改的那个文件
	write(t, repo, "package-lock.json", "{\"我正在改\": true}\n")
	write(t, plan.Dir, "src.js", "干活\n")
	write(t, plan.Dir, "package-lock.json", "{\"branch\": true}\n")
	if _, err := CommitTurn(plan.Dir, "小前", plan.Branch, "干活", 1); err != nil {
		t.Fatal(err)
	}

	got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小前")
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("主干目录脏着就合不进去 —— 一个人 npm install 就能卡住全屋:\n%s", got.Message)
	}
	// 主干这条**分支**必须真的前进了 —— 别人接着合的前提
	if out, _ := runGit(repo, "ls-tree", "-r", "--name-only", mainBranch(repo)); !strings.Contains(out, "src.js") {
		t.Fatal("ref 没前进, 那这次合并等于没发生")
	}
	/**
	 * **他手上那份一个字都不能动**: 目录旧一点没关系, 他正在改的东西
	 * 比"目录里的版本号"值钱得多.
	 */
	body, _ := os.ReadFile(filepath.Join(repo, "package-lock.json"))
	if !strings.Contains(string(body), "我正在改") {
		t.Fatalf("把主干目录里没提交的改动动了: %q", body)
	}
	if out, _ := runGit(repo, "status", "--porcelain"); strings.Contains(out, "UU ") {
		t.Fatalf("留下了冲突:\n%s", out)
	}
}

/**
 * **不撞的时候要原样放回去** —— 用户在主干上改了个别的文件, 那跟这次
 * 合并没关系, 合完他手上那份还得在.
 */
func TestMergeUp主干上不相干的改动要放回去(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, _ := Assign(repo, "小前", t.TempDir(), false)
	write(t, repo, "笔记.md", "原来的\n")
	commit(t, repo, "先有个笔记")
	for i := 0; i < 2; i++ {
		if _, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小前"); err != nil {
			t.Fatal(err)
		}
	}
	// 用户在主干上改了个跟这次合并无关的文件, 没提交
	write(t, repo, "笔记.md", "我正在写的东西\n")
	write(t, plan.Dir, "src.js", "干活\n")
	if _, err := CommitTurn(plan.Dir, "小前", plan.Branch, "干活", 1); err != nil {
		t.Fatal(err)
	}

	got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小前")
	if err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}
	body, _ := os.ReadFile(filepath.Join(repo, "笔记.md"))
	if !strings.Contains(string(body), "我正在写的东西") {
		t.Fatalf("把用户手上那份弄没了: %q", body)
	}
	// 跟这次合并不相干的改动: 目录照样跟上, 他那份原样留着
	if out, _ := runGit(repo, "ls-files"); !strings.Contains(out, "src.js") {
		t.Error("目录没跟上")
	}
}

/**
 * **逐个文件对齐, 不整体放弃**.
 *
 *	reset --keep 是"撞一个就全不动": 人只改了 index.css 时,
 *	README.md 也会跟着落后, 即使那份改动与人无关.
 */
func TestMergeUp主干目录只留下人正在动的那几个(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, _ := Assign(repo, "小前", t.TempDir(), false)
	write(t, repo, "样式.css", "旧样式\n")
	write(t, repo, "说明.md", "旧说明\n")
	commit(t, repo, "先有两份")
	for i := 0; i < 2; i++ {
		if _, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小前"); err != nil {
			t.Fatal(err)
		}
	}
	// 人只在动其中一个
	write(t, repo, "样式.css", "我正在改的样式\n")
	// bot 两个都改了
	write(t, plan.Dir, "样式.css", "小前改的样式\n")
	write(t, plan.Dir, "说明.md", "小前改的说明\n")
	if _, err := CommitTurn(plan.Dir, "小前", plan.Branch, "两个都改", 1); err != nil {
		t.Fatal(err)
	}
	if got, err := MergeUp(plan.Project, plan.Dir, plan.Branch, "小前"); err != nil || !got.OK {
		t.Fatalf("%v %+v", err, got)
	}

	// 他没碰的那份**跟上了**
	doc, _ := os.ReadFile(filepath.Join(repo, "说明.md"))
	if !strings.Contains(string(doc), "小前改的说明") {
		t.Errorf("没动过的那份也落后了: %q", doc)
	}
	// 他正在改的那份**一个字没动**
	css, _ := os.ReadFile(filepath.Join(repo, "样式.css"))
	if !strings.Contains(string(css), "我正在改的样式") {
		t.Errorf("把他手上那份动了: %q", css)
	}
	// 目录里只剩他那一个跟主干不一样
	out, _ := runGit(repo, "status", "--porcelain")
	if strings.Contains(out, "说明.md") {
		t.Errorf("说明.md 还挂着:\n%s", out)
	}
}

/**
 * 系统自己叫的那一声, **裹在信封里也得认出来**.
 *
 *	两人做前端时, git log 可能写着"那个权限批了，接着干吧。" ——
 *	记号明明在, 只是前面多裹了一层"[说话的是…]": HasPrefix 认不出来,
 *	而拆信封那一步又把它一起剥掉了.
 */
func Test系统那一声裹在信封里也不当标题(t *testing.T) {
	const fallback = "合入前把手上的活收一下"
	for _, doing := range []string{
		"[系统] 那个权限批了，接着干吧。",
		"[说话的是「阿导」—— 这是用户在界面上设的名字]\n[系统] 那个权限批了，接着干吧。",
		"[房间「finance」里你没看到的几句]\n小乙：好\n" + roomContextTail +
			"\n[系统] 上次干到一半停了，看一眼接着做。",
	} {
		if got := (TurnNote{Doing: doing}).title(fallback); got != fallback {
			t.Errorf("把系统的一声当成了这一轮在干什么:\n  收到 %q\n  标题 %q", doing, got)
		}
	}
	// 而人真说的那句照旧要当标题 —— 别把这条修成"一律退回缺省"
	real := "[说话的是「阿导」]\n" + "把导出改成按月份分表"
	if got := (TurnNote{Doing: real}).title(fallback); got != "把导出改成按月份分表" {
		t.Errorf("人说的那句没当上标题: %q", got)
	}
}

/**
 * 人正在改的那个文件, 没落到磁盘上 —— **要说出来**.
 *
 *	例如人手上 pay.py 有一行没提交的 TODO, bot 也改了
 *	pay.py. 合入之后 master 那份是新的, 而磁盘上还是他那份旧的(护住他
 *	没提交的改动, 这一步是对的). 于是新的 test_pay.py 配上旧的 pay.py,
 *	**测试当场红了** —— 而回执说的是"合进主干（master）了。3 files
 *	changed", 一个字都没提.
 *
 *	他被告知一切正常, 然后对着一个红的测试发懵.
 */
func Test没覆盖你手上那份要说出来(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	root := t.TempDir()

	write(t, repo, "pay.py", "def charge(a):\n    return a\n")
	if _, err := runGit(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "-c", "user.name=我", "-c", "user.email=me@x",
		"commit", "-qm", "先把收款那块搭起来"); err != nil {
		t.Fatal(err)
	}
	plan, err := Assign(repo, "小巳", root, true)
	if err != nil {
		t.Fatal(err)
	}
	// bot 改了 pay.py
	write(t, plan.Dir, "pay.py", "FEE = 0.05\n\n\ndef charge(a, f=FEE):\n    return a + a*f\n")
	if _, err := CommitTurn(plan.Dir, "小巳", plan.Branch, "加了手续费", 1); err != nil {
		t.Fatal(err)
	}
	// 而**我手上也有一行没提交的**
	write(t, repo, "pay.py", "def charge(a):\n    return a\n    # TODO: 手续费还没想好\n")

	got, err := MergeUp(repo, plan.Dir, plan.Branch, "小巳")
	if err != nil {
		t.Fatalf("合不上: %v", err)
	}
	if !got.OK {
		t.Fatalf("没合上: %s", got.Message)
	}
	// 我那份一个字都不许被动
	if body := readAll(t, filepath.Join(repo, "pay.py")); !strings.Contains(body, "TODO") {
		t.Errorf("把我没提交的那行冲掉了:\n%s", body)
	}
	// **而且要告诉我这件事**
	if !strings.Contains(got.Message, "pay.py") {
		t.Errorf("没说哪个文件没落下来:\n%s", got.Message)
	}
	if !strings.Contains(got.Message, "没覆盖") {
		t.Errorf("没说清主干上那份跟磁盘上不一样:\n%s", got.Message)
	}
	/**
	 * **要紧的那半句必须在第一行**.
	 *
 *	给人的通知只取第一行(agent/gitteam.go 的 oneLine). 写在
 *	第二段的话 bot 看得见、人看不见 —— 而这件事 bot 根本解决不了,
 *	它碰不到主干那个工作目录.
	 */
	first := got.Message
	if at := strings.IndexAny(first, "\r\n"); at >= 0 {
		first = first[:at]
	}
	if !strings.Contains(first, "pay.py") {
		t.Errorf("要紧的那半句不在第一行, 人看不到:\n%s", got.Message)
	}
	if len([]rune(first)) > 120 {
		t.Errorf("第一行 %d 字, 通知那儿会被截断: %s", len([]rune(first)), first)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
