package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

/**
 * workplan —— "这个 bot 在哪儿干活"只算一次, 谁问都是同一个答案.
 *
 * ── 为什么要有这一层 ──
 *
 *	这条路径同时是三件事的真相源: 内核给它的 write 能力、提示词里
 *	"你的工作区是哪儿"那一句、界面上显示的那一条. 三处各算一次的话,
 *	迟早分叉 —— 而分叉之后的样子是: 界面显示 A, 提示词告诉它 B,
 *	内核只许它写 C. 它照着 B 去写, 被 C 拦下, 而用户在 A 那儿找原因.
 *
 *	(这不是假想: 能力和提示词分叉这件事, 代码里已经为它留过一条注释 ——
 *	"必须是同一个, 见 layerStation".)
 *
 * ── 独占模式下的互斥 ──
 *
 *	没有 git 就开不了 worktree, 于是隔离不了. 这时**不能装作没事**:
 *	一个项目目录只许一个 bot 有写能力, 第二个来的当场挡住并说清为什么.
 *	偷偷让两个人共用一个目录, 出的事是"改了的东西过一会儿自己变回去"——
 *	那种问题查起来要几个小时, 而且没人会想到是这儿.
 */

type workPlanner struct {
	mu    sync.Mutex
	byBot map[string]Plan
	/**
	 * forWork 这份计划是**照哪个工作目录**算出来的.
	 *
	 *	缓存原来只按名字取, 一个字都不问"这次派的是哪儿". 于是把一个人
	 *	从财务挪到 OA, 拿回来的还是财务那份计划 —— **它继续在财务那边
	 *	改文件, 而人以为它在 OA**. 两边都不会报错.
	 *
	 *	不能拿 Plan.Project 直接比: 人填的可能是仓库里的一个子目录,
	 *	而 Project 是仓库根. 记下当初问的那个才对得上.
	 */
	forWork map[string]string
	// root worktree 放在哪. 默认 ~/.neox-os/worktrees
	root string
	/**
	 * allowInit 这个项目目录**许不许**收进 git.
	 *
	 *	**默认是许的**, 这一格是用来说"不"的. 原来反过来: 要用户
	 *	先点头才走 git —— 而界面上根本没有那个勾, 于是所有项目都在
	 *	独占模式里, 一个项目只能一个 bot, 交接/合入/产物全是空的.
	 *	nil = 全都收.
	 */
	allowInit func(project string) bool
}

func newWorkPlanner(allowInit func(string) bool) *workPlanner {
	return &workPlanner{
		byBot:     map[string]Plan{},
		forWork:   map[string]string{},
		root:      filepath.Join(neoxHome(), "worktrees"),
		allowInit: allowInit,
	}
}

// plan 给这个 bot 算一块地方. 同一个 bot 反复问, 答案是同一个.
func (w *workPlanner) plan(p persona) (Plan, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	want := ""
	if p.work != "" {
		want = realPath(p.work)
	}
	if got, ok := w.byBot[p.name]; ok && stillGood(got) && w.forWork[p.name] == want {
		return got, nil
	}

	// 没派项目: 系统给一个匿名目录. 那儿天然只有它一个人, 不用隔离
	if p.work == "" {
		dir := filepath.Join(neoxHome(), "work", p.app)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Plan{}, err
		}
		got := Plan{Dir: realPath(dir), Project: realPath(dir), Mode: ModeExclusive,
			Why: "系统分的目录，只有它一个人"}
		w.byBot[p.name] = got
		w.forWork[p.name] = want
		return got, nil
	}

	if err := os.MkdirAll(p.work, 0o755); err != nil {
		return Plan{}, err
	}
	// **默认收**: 没给判据就是全都收. 见 adopt.go 开头那段
	allow := w.allowInit == nil || w.allowInit(p.work)
	got, err := Assign(p.work, p.name, w.root, allow)
	if err != nil {
		return Plan{}, err
	}
	// **独占模式下一个项目只许一个人**: 隔离不了的时候, 说清楚比
	// 让两个人在同一个目录里互相覆盖强得多
	if got.Mode == ModeExclusive {
		if other := w.holderOf(got.Project, p.name); other != "" {
			return Plan{}, fmt.Errorf(
				"「%s」已经在这个项目里干活了。%s —— "+
					"要多人一起干，先让这个目录变成 git 仓库", other, got.Why)
		}
	}
	w.byBot[p.name] = got
	w.forWork[p.name] = want
	return got, nil
}

// holderOf 这个项目目录现在被谁占着(独占模式下才有意义).
func (w *workPlanner) holderOf(project, except string) string {
	for bot, got := range w.byBot {
		if bot == except {
			continue
		}
		if got.Mode == ModeExclusive && got.Project == project {
			return bot
		}
	}
	return ""
}

// forget 这个 bot 不干了 —— 下次再问要重新算.
//
//	**不动 worktree**: 换工作区、重启都会走到这儿, 而那两件事都不该
//	把它没提交的活删掉. 真要收回目录是 Release, 那是"删掉这个 bot"
//	才做的事.
func (w *workPlanner) forget(bot string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.byBot, bot)
	delete(w.forWork, bot)
}

/**
 * stillGood 这份算好的计划**现在还成立吗**.
 *
 * ── 为什么非查不可 ──
 *
 *	计划是按 bot 名字缓存的, 而世界会在两次之间变: 用户把项目目录删了、
 *	搬走了、重新 git init 了 —— 缓存里那条 worktree 就指向一个不存在的
 *	仓库了.
 *
 *	最危险的一类失败是项目目录被删掉重建, 同名的 bot 起回来,
 *	拿到旧计划 —— 它的工作区里 .git 指着
 *	`<已删掉的项目>/.git/worktrees/小丙`, 一跑 git 就是
 *	"fatal: not a git repository". 而它照样读文件、照样写代码、照样说
 *	"已经写好并验证通过" —— **什么都到不了主干, 而没有一处会说这件事**.
 *
 *	所以每次都便宜地查一眼: 目录还在吗, 那儿还是个仓库吗. 不成立就
 *	重新分一块 —— 重分是安全的(Assign 会接着用还在的那块, 见它自己的
 *	注释), 而拿着一份坏计划干活不是.
 */
func stillGood(plan Plan) bool {
	if plan.Dir == "" {
		return false
	}
	if info, err := os.Stat(plan.Dir); err != nil || !info.IsDir() {
		return false
	}
	// 独占模式没有 worktree 可坏 —— 目录还在就行
	if plan.Mode != ModeWorktree {
		return true
	}
	return IsRepo(plan.Dir)
}

// planOf 按名字查一份**已经算好的**.
//
//	不在这儿新算: 这条路是界面来问的(它的产物在哪儿), 而算一次会真的
//	去建 worktree —— 一个"看一眼"的动作不该有副作用.
/**
 * inProject 这个项目里都有谁, 各自分在哪块地方.
 *
 *	**按项目根认, 不按 worktree 认**: 走分支隔离时每个人的 worktree
 *	都不一样, 而他们干的是同一摊活.
 */
func (w *workPlanner) inProject(project string) map[string]Plan {
	out := map[string]Plan{}
	if w == nil {
		return out
	}
	want := realPath(project)
	w.mu.Lock()
	defer w.mu.Unlock()
	for bot, got := range w.byBot {
		if got.Project == want {
			out[bot] = got
		}
	}
	return out
}

func (w *workPlanner) planOf(bot string) (Plan, error) {
	// **nil 也要答得出**: 测试和裁剪过的宿主会不接这个组件, 而"没有分配"
	// 是一个正常答案, 不是崩溃的理由
	if w == nil {
		return Plan{}, fmt.Errorf("这台机器没有工作区分配")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	got, ok := w.byBot[bot]
	if !ok {
		return Plan{}, fmt.Errorf("「%s」现在没在干活", bot)
	}
	return got, nil
}

// branchOf 界面要显示"它的产物在哪条分支上". 没有就是空.
func (w *workPlanner) branchOf(bot string) string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.byBot[bot].Branch
}
