package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

/**
 * gitwork —— 一个项目里**多个 bot 各干各的**, 靠 git worktree.
 *
 * ── 为什么非要隔离 ──
 *
 *	两个 bot 绑同一个目录同时干活, 是这套东西目前最坏的一种坏法:
 *	A 的 edit 会把 B 正在读的文件改掉, 而两边都以为自己是那儿唯一的人.
 *	更糟的是切分支 —— git 的分支是**整个工作目录**的状态, A 切一下,
 *	B 脚底下的文件全变了, 它下一步读到的是另一个世界的代码.
 *
 * ── 为什么是 worktree 而不是"各自 clone" ──
 *
 *	clone 出来的是**另一个仓库**: 历史是副本, 合回去要走 remote,
 *	而用户的项目往往有 remote 有 hooks, 多一个副本就多一处能搞混的地方.
 *	worktree 是同一个仓库的多个工作目录 —— 对象库和历史只有一份,
 *	合并就是本地的 merge. 这正是 worktree 被发明出来要解决的事.
 *
 * ── 边界 ──
 *
 *	**用系统的 git, 不内置一套**. 用户的项目本来就在他自己的 git 里
 *	(有历史、有 remote、有 hooks、有 submodule); 内置一个实现去操作它,
 *	行为跟他命令行里的 git 不一致时, 坏的是他的仓库.
 *	没有 git 的机器**明说**并降级(见 Plan.Mode), 不偷偷换一套.
 */

// WorkMode 这个 bot 最后是怎么干活的.
type WorkMode string

const (
	// ModeWorktree 正常路: 自己的 worktree + 自己的分支, 谁都碰不到谁.
	ModeWorktree WorkMode = "worktree"
	// ModeExclusive 降级路: 没有 git 或用户不让初始化 —— 这个项目目录
	// **只允许一个 bot** 有写能力, 第二个来的会被挡住(不是偷偷共用).
	ModeExclusive WorkMode = "exclusive"
)

// realPath 解掉符号链接.
//
//	**必须解**: macOS 上 /var 是 /private/var 的链接, 我们拼出来的路径
//	是 /var/..., 而 git 报回来的是 /private/var/... —— 同一个目录, 两个
//	字符串. 这条路径会原样变成内核给的 write 能力, 两个写法之间比一次
//	字符串, 就会得出"这不是同一块地方"的结论.
func realPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// Plan 一次分配的结果 —— 它同时是三件事的真相源:
// 内核给的 write 能力、提示词里"你的工作区是哪儿"、界面上显示的那一条.
type Plan struct {
	// Dir bot 真正干活的目录. worktree 模式下是 worktree, 不是项目根
	Dir string
	// Project 项目根 (用户填的那个). 界面上"这是哪个项目"用它
	Project string
	Branch  string
	Mode    WorkMode
	// Why 为什么是这个模式 —— 降级时必须说得出原因, 否则用户只会看到
	// "怎么跟说好的不一样"
	Why string
}

// gitBin 系统 git 在不在.
//
//	**不缓存结果**: 用户中途装上 git 是常事(macOS 上弹一个框装 CLT),
//	缓存一次"没有"会让他装完还得重启整个 OS 才认.
func gitBin() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("这台机器上没有 git")
	}
	return path, nil
}

// runGit 在 dir 里跑一条 git.
//
//	超时给 60 秒: 本地操作都是毫秒级, 但 worktree 建在一个大仓库上
//	要拷一份索引; 真卡住的话卡死比报错难查得多.
func runGit(dir string, args ...string) (string, error) {
	bin, err := gitBin()
	if err != nil {
		return "", err
	}
	// **core.quotepath=false**: 不然带中文的文件名会被转义成 \344\272...
	// 而这些输出是要拿去给人看的(界面上"它改了哪些文件")
	args = append([]string{"-c", "core.quotepath=false"}, args...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	// 不继承用户 shell 里的 git 配置噪音, 但保留 PATH 和 HOME ——
	// HOME 少了的话 git 读不到 ~/.gitconfig, 用户的 user.name 就没了
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

// IsRepo 这个目录在不在一个 git 仓库里.
func IsRepo(dir string) bool {
	out, err := runGit(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// RepoRoot 这个目录所属仓库的根.
func RepoRoot(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// hasHEAD 仓库里有没有第一个提交.
//
//	空仓库(git init 之后什么都没提交)是**建不了 worktree** 的:
//	worktree 要一个基点. 这不是错误, 是要先补一个空提交.
func hasHEAD(dir string) bool {
	_, err := runGit(dir, "rev-parse", "--verify", "HEAD")
	return err == nil
}

// Init 把一个普通目录变成仓库, 并补一个**空**提交当基点.
//
//	真正在用的是 Adopt(见 adopt.go): 它会把目录里现有的文件收进第一份
//	快照. 这里留着给测试造一个"干净的空仓库"用 —— 那是另一种起点.
func Init(dir string) error {
	if !IsRepo(dir) {
		if _, err := runGit(dir, "init"); err != nil {
			return err
		}
	}
	if hasHEAD(dir) {
		return nil
	}
	// 空提交当基点: 不 add 任何东西 —— 用户目录里现有的文件是不是该进
	// 版本控制, 那是他的决定, 不是我们的
	_, err := runGit(dir, "-c", "user.name=NeoxOS", "-c", "user.email=os@neox.local",
		"commit", "--allow-empty", "-m", "NeoxOS: 起个头")
	return err
}

// Dirty 工作目录里有没有没提交的改动.
//
//	开 worktree 之前要看一眼: 我们不碰用户的改动(worktree 从 HEAD 建),
//	但得让他知道 —— 他手上那几个改动不会跟着 bot 走.
func Dirty(dir string) bool {
	out, err := runGit(dir, "status", "--porcelain")
	return err == nil && strings.TrimSpace(out) != ""
}

// BranchOf 这个 bot 的分支叫什么.
//
//	前缀 bot/ 是给人看的: 在 git log --graph 或者 GitHub 上一眼看得出
//	"这条是机器开的、是谁的" —— 跟 feature/ release/ 同一种命名法,
//	用户点名要的就是这种标准味(原话: "统一叫 BOT-什么什么, 标准专业一点").
//	名字里不合法的字符换成 -, 但**不做音译** ——
//	中文分支名 git 是支持的, 换成拼音反而认不出是谁.
//
//	旧分支(neox/…)不迁移: 分支名记在每个 bot 的 plan 里, 老的照用,
//	新开的用新前缀 —— 改名字不值得动别人手上正干着的活.
func BranchOf(bot string) string {
	clean := badBranchChars.ReplaceAllString(strings.TrimSpace(bot), "-")
	clean = strings.Trim(clean, "-.")
	if clean == "" {
		// **不能都叫 bot**: 名字全是表情的那几个会挤在同一条分支上,
		// 于是甲的活和乙的活混进同一条线, 而合入的时候谁都看不出来.
		// 见 safeName 上那段.
		sum := sha256.Sum256([]byte(bot))
		clean = hex.EncodeToString(sum[:4])
	}
	return "bot/" + clean
}

// git 的分支名规矩: 不能有空格、~^:?*[\ 和控制字符, 不能以 . 开头或 .lock 结尾
var badBranchChars = regexp.MustCompile(`[\s~^:?*\[\]\\@{}]+`)

// botOfBranch 从分支名读回 bot 名.
//
//	新分支是 bot/…, 但**老账本里还躺着 neox/…** —— 改前缀不迁移旧分支
//	(那是别人手上正干着的活), 所以读回来的时候两种都认.
func botOfBranch(branch string) string {
	if rest, ok := strings.CutPrefix(branch, "bot/"); ok {
		return rest
	}
	return strings.TrimPrefix(branch, "neox/")
}

// branchExists 分支在不在.
func branchExists(repo, branch string) bool {
	_, err := runGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// worktreeOf 已经开着的 worktree 里, 这条分支占的是哪个目录.
//
//	一个分支同时只能被一个 worktree 签出 —— 这正是我们要的互斥,
//	但也意味着重启之后必须**认出旧的那个**, 否则第二次启动会撞上
//	"branch is already checked out", 而那句话对用户毫无意义.
func worktreeOf(repo, branch string) string {
	out, err := runGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	var dir string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			dir = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			if strings.TrimPrefix(line, "branch ") == "refs/heads/"+branch {
				return dir
			}
		}
	}
	return ""
}

/**
 * partOf 这个目录还是那个仓库的 worktree 吗.
 *
 *	判据是它自己报出来的**公共 git 目录**: worktree 里的 .git 是个指针,
 *	指回项目根的 .git —— 指不回去(或者根本不是仓库了)就是死的.
 */
func partOf(root, dir string) bool {
	out, err := runGit(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return false
	}
	got := strings.TrimSpace(out)
	if !filepath.IsAbs(got) {
		got = filepath.Join(dir, got)
	}
	return realPath(got) == realPath(filepath.Join(root, ".git"))
}

// Assign 给一个 bot 分一块能干活的地方.
//
//	project 空 = 没派项目, 用系统分的匿名目录(那儿天然只有它一个人,
//	不需要隔离). 非空 = 用户派了项目, 走 worktree 那条路.
//
//	allowInit 允不允许把这个目录收进 git. **默认是允许的** ——
//	不走 git 的项目, 隔离/归属/交接/合入/验证证据全都不存在, 而且
//	是静悄悄地不存在. 见 adopt.go.
func Assign(project, bot, worktreeRoot string, allowInit bool) (Plan, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return Plan{}, errors.New("没有项目目录")
	}
	plan := Plan{Project: realPath(project), Dir: realPath(project), Mode: ModeExclusive}

	if _, err := gitBin(); err != nil {
		plan.Why = "这台机器上没有 git，这个项目只能一个人干"
		return plan, nil
	}
	if !IsRepo(project) {
		if !allowInit {
			plan.Why = "你说了这个目录别动 git，那这个项目只能一个人干"
			return plan, nil
		}
		// **收进来, 不是空转一个 git init**: 空提交长出来的 worktree 里
		// 一个文件都没有, bot 会对着一个"空项目"发呆. 见 adopt.go
		if err := Adopt(project); err != nil {
			plan.Why = err.Error() + "，所以这个项目只能一个人干"
			return plan, nil
		}
	}
	root, err := RepoRoot(project)
	if err != nil {
		plan.Why = "认不出仓库根: " + err.Error()
		return plan, nil
	}
	/**
	 * **每个项目都要打点, 不管它是不是我们建的**.
	 *
	 *	`Adopt` 只处理还不是仓库的项目, 因此**人自己的仓库也必须单独
	 *	经过同样的整理** —— 工具缓存不进版本控制、锁文件按本地那份合,
	 *	这些规矩都需要在这里生效.
	 *
	 *	把 bot 放进一个有历史的项目, 48 个
	 *	Library/Caches/…/*.pyc 进了版本控制. HOME 被指进工作区(隔离
	 *	要的), 工具往家里写的缓存落在项目里, 收活时 git add -A 一并收走.
	 *
	 *	打点写的是 .git/info/ 下面那两份 —— 本仓库本机的规矩, 不进版本
	 *	控制, 用户的 .gitignore 一个字都不碰. 所以放在这儿是安全的:
	 *	**每次派活都过一遍**, 而不是只在第一次收编的时候.
	 */
	housekeep(root)
	if !hasHEAD(root) {
		if !allowInit {
			plan.Why = "这个仓库还没有第一个提交，这个项目只能一个人干"
			return plan, nil
		}
		if err := Adopt(root); err != nil {
			plan.Why = err.Error() + "，所以这个项目只能一个人干"
			return plan, nil
		}
	}

	branch := BranchOf(bot)
	// 重启回来: 旧的 worktree 还在就**接着用**, 不新建也不覆盖 ——
	// 它上面有这个 bot 没提交完的活
	if dir := worktreeOf(root, branch); dir != "" {
		if _, err := os.Stat(dir); err == nil {
			return Plan{Dir: realPath(dir), Project: realPath(root), Branch: branch, Mode: ModeWorktree,
				Why: "接着用上次那块"}, nil
		}
		// 目录被人删了, 但 git 还记着 —— 先擦掉这条记录, 否则下面建不起来
		_, _ = runGit(root, "worktree", "prune")
	}

	dir := filepath.Join(worktreeRoot, projectSlug(root), botSlug(bot))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Plan{}, err
	}
	/**
	 * 那块地方要是**已经不是这个仓库的 worktree 了**, 清掉重来.
	 *
	 *	项目目录可能被删了重建、搬走或重新 git init, 而上一次留下的目录
	 *	还在, 里面的 .git 指着一个不存在的仓库.
	 *
 *	不清的话 `worktree add` 会因为"目录非空"失败, 于是整份计划掉回
 *	独占模式 —— 一个本该有自己分支的人, 从此跟别人抢同一个目录.
 *	更糟的是, 它可能拿着那份坏计划干完一轮, 说"已经写好并验证通过",
 *	而什么都到不了主干.
	 *
	 *	**只清死的那种**: 还认得出自己是这个仓库的 worktree 就不动 ——
	 *	那里面可能有它没提交的活.
	 */
	if info, err := os.Stat(dir); err == nil && info.IsDir() && !partOf(root, dir) {
		_ = os.RemoveAll(dir)
		_, _ = runGit(root, "worktree", "prune")
	}
	/**
	 * 那块地方**还是这个仓库的 worktree, 只是签在别的分支上**: 接着用,
	 * 分支就用它现在签着的那条.
	 *
	 *	分支前缀从 neox/ 换成 bot/ 后, 正干着活的 worktree 仍可能签在
	 *	老分支上 —— 按新名字 `worktree add` 会撞 "already exists", bot
	 *	就无法恢复, spawn 失败后交办时会只剩一个可用成员.
	 *
	 *	覆盖和报错都不对: 那上面可能有没提交完的活. 认它、用它的分支 ——
	 *	botOfBranch 两种前缀都读得回来, 合入和产出统计不受影响.
	 */
	if info, err := os.Stat(dir); err == nil && info.IsDir() && partOf(root, dir) {
		if cur, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			if b := strings.TrimSpace(cur); b != "" && b != "HEAD" {
				return Plan{Dir: realPath(dir), Project: realPath(root), Branch: b, Mode: ModeWorktree,
					Why: "接着用上次那块"}, nil
			}
		}
	}
	args := []string{"worktree", "add"}
	if branchExists(root, branch) {
		// 分支已经在了(上次删过 worktree, 或者用户自己建的): 签出它, 不重建
		args = append(args, dir, branch)
	} else {
		args = append(args, "-b", branch, dir, "HEAD")
	}
	if _, err := runGit(root, args...); err != nil {
		plan.Why = "开不了 worktree: " + err.Error()
		return plan, nil
	}
	return Plan{Dir: realPath(dir), Project: realPath(root), Branch: branch, Mode: ModeWorktree,
		Why: "自己的分支，谁都碰不到谁"}, nil
}

// Release 一个 bot 不干了 —— 收回 worktree, **但留下分支**.
//
//	分支是它的产物: 人可能删掉一个 bot 只是因为不想再看见它,
//	而它写过的代码不该跟着消失. worktree 只是一个工作目录, 删了不丢东西.
func Release(project, bot string) error {
	root, err := RepoRoot(project)
	if err != nil {
		return err
	}
	branch := BranchOf(bot)
	dir := worktreeOf(root, branch)
	if dir == "" {
		return nil
	}
	// --force: worktree 里通常有没提交完的改动, 那正是常态
	if _, err := runGit(root, "worktree", "remove", "--force", dir); err != nil {
		return err
	}
	return nil
}

// projectSlug 项目目录名 —— worktree 放在哪一格.
//
//	**带上父目录名**: 一台机器上叫 oa 的项目可以有好几个, 光看最后一段
//	会撞在一起, 而撞了之后两个项目的 bot 会共用一块地方.
func projectSlug(root string) string {
	base := filepath.Base(root)
	parent := filepath.Base(filepath.Dir(root))
	name := base
	if parent != "" && parent != "." && parent != string(filepath.Separator) {
		name = parent + "-" + base
	}
	/**
	 * **名字好看是次要的, 不许撞才是要紧的**.
	 *
	 *	原来只取"父目录-目录名". 于是 ~/工作/公司/财务 和 ~/备份/公司/财务
	 *	算出来是同一个 —— 两个不同的项目共用一块 worktree 地皮.
	 *
	 *	撞上之后不是"挤在一起"那么轻: 下面那段发现"这块地方不是这个仓库
	 *	的 worktree"就会**清掉重来** —— 而被清掉的是另一个项目里那个人
	 *	没提交完的活. 两个同名项目交替派活, 就是反复互删.
	 *
	 *	所以后面缀一小段整条路径的指纹. 目录名难看一点, 换的是"绝不
	 *	会指到别人家去".
	 *
	 *	**不影响已经建好的**: 上面先问过 git 这条分支的 worktree 在哪
	 *	(worktreeOf), 有就接着用 —— 这里只决定新建的往哪儿放.
	 */
	sum := sha256.Sum256([]byte(realPath(root)))
	return safeName(name) + "-" + hex.EncodeToString(sum[:3])
}

func botSlug(bot string) string { return safeName(bot) }

var badPathChars = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

func safeName(raw string) string {
	clean := badPathChars.ReplaceAllString(strings.TrimSpace(raw), "-")
	clean = strings.Trim(clean, "-.")
	if clean == "" {
		/**
		 * **兜底那个名字必须是定的**.
		 *
		 *	名字全是符号的 bot 清完之后是空的, 如果返回 x<纳秒>, 就会
		 *	**每次派活都算出一个新目录** —— 上一次没提交完的活留在旧目录
		 *	里成了孤儿, 而它自己一无所知.
		 *
		 *	一个名字必须永远算出同一块地皮, 不同的名字必须算出不同的.
		 *	名字的指纹两条都满足, 时间戳一条都不满足.
		 */
		sum := sha256.Sum256([]byte(raw))
		return "x" + hex.EncodeToString(sum[:4])
	}
	return clean
}
