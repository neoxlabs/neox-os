package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

/**
 * project —— **这个项目现在什么状态**.
 *
 * ── 为什么非要有这一页 ──
 *
 *	三个 bot 并行工作一晚之后, 想知道"oa 现在怎么样了", 只能
 *	一个一个点头像看: 他在哪条分支、有没有没合的、主干上一次是谁改的.
 *	而这几件事**本来就是一个整体** —— 项目才是干活的单位, 人是围着它转的.
 *
 *	更要紧的是"待合"这一格: 一条干完了却没合上去的分支, 在界面上跟
 *	"什么都没干"长得一模一样. 那是最容易丢活的地方.
 *
 * ── 撤一次合入 ──
 *
 *	合错了原来只能自己去命令行 git revert. 而"合上去"是这套东西唯一
 *	不可逆的动作 —— 它改的是所有人的主干. 一个动作有多不可逆, 它就
 *	越该有一颗退回去的按钮.
 *
 *	用 revert 不用 reset: **历史不改写**. 别人的 worktree 已经从主干
 *	拉过东西了, 抹掉一段历史等于让他们下一次 sync_down 撞上一堆
 *	莫名其妙的冲突.
 */

// ProjectReport 一个项目的全景 —— 界面上那一页就照它画
type ProjectReport struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Main 主干叫什么(master / main), 空 = 这个项目没走 git
	Main string `json:"main,omitempty"`
	// Head 主干最后一条改动: 谁、什么时候、干了什么
	Head *CommitLine `json:"head,omitempty"`
	// Files 主干上有多少个文件 —— "这摊活有多大"最省事的一个数
	Files int `json:"files"`
	// Dirty 项目根上还没提交的改动. **那是你自己手上的东西**, 不是 bot 的:
	// bot 都在各自的 worktree 里, 碰不到这儿
	Dirty int `json:"dirty"`
	// Members 屋里每个人各自的状态
	Members []ProjectMember `json:"members"`
	// Merges 最近合进主干的那几次 —— 每一条都能撤
	Merges []CommitLine `json:"merges"`
	/**
	 * Handoffs 这个项目里发生过的交接.
	 *
	 *	交接改变的是"这个项目里谁负责什么" —— 那是项目层面的事实,
	 *	不该只留在两个人的聊天记录里.
	 */
	Handoffs []HandoffLine `json:"handoffs,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// HandoffLine 一次交接 —— 给人看的那几样
type HandoffLine struct {
	From  string `json:"from"`
	To    string `json:"to"`
	At    int64  `json:"at"`
	Files int    `json:"files"`
}

// ProjectMember 一个人在这个项目上的状态
type ProjectMember struct {
	Bot    string `json:"bot"`
	Branch string `json:"branch,omitempty"`
	// Unmerged 干完了还没合上去的提交数. **这一格不为零最要紧**:
	// 一条没合的分支, 在别的地方看跟"什么都没干"一模一样
	Unmerged int `json:"unmerged"`
	// Dirty 手上还没提交的文件数 —— 活干了一半
	Dirty int `json:"dirty"`
	// LastAt 他最后一次动手是什么时候
	LastAt int64 `json:"lastAt,omitempty"`
}

// CommitLine 一条提交, 给人看的那几样
type CommitLine struct {
	Hash    string `json:"hash"`
	Who     string `json:"who"`
	Subject string `json:"subject"`
	At      int64  `json:"at"`
	// Files/Add/Del 这一条动了多大
	Files int `json:"files,omitempty"`
	// Merge 这是不是一次合并 —— 撤的时候要用 -m 1
	Merge bool `json:"merge,omitempty"`
}

// projectOf 攒出一个项目的全景.
//
//	members 是"这个项目里有哪些 bot、各自在哪条分支上" —— 由上层给,
//	因为那是进程表的事, 不是 git 的事.
func projectOf(path string, members map[string]Plan) ProjectReport {
	out := ProjectReport{Path: path, Name: lastSeg(path), Members: []ProjectMember{}, Merges: []CommitLine{}}
	if path == "" {
		out.Error = "没说是哪个项目"
		return out
	}
	if _, err := gitBin(); err != nil {
		out.Error = err.Error()
		return out
	}
	if !IsRepo(path) {
		out.Error = "这个目录没走 git，看不出状态"
		return out
	}
	out.Main = mainBranch(path)
	if files, err := runGit(path, "ls-files"); err == nil {
		out.Files = len(splitLines(files))
	}
	if dirty, err := runGit(path, "status", "--porcelain"); err == nil {
		out.Dirty = len(splitLines(dirty))
	}
	// **主干最近那一条要是"人干的事", 不是一条合并记录**.
	//	合并提交("Merge branch 'master' into neox/小查")是管道动作,
	//	它出现在这一格里, 等于把最要紧的一行让给了噪音.
	if head := lineOf(path, out.Main); head != nil {
		out.Head = head
	}

	// ── 每个人各自什么状态 ──
	names := make([]string, 0, len(members))
	for bot := range members {
		names = append(names, bot)
	}
	sort.Strings(names)
	for _, bot := range names {
		plan := members[bot]
		one := ProjectMember{Bot: bot, Branch: plan.Branch}
		if plan.Branch != "" && out.Main != "" {
			if n, err := runGit(path, "rev-list", "--count", out.Main+".."+plan.Branch); err == nil {
				one.Unmerged, _ = strconv.Atoi(strings.TrimSpace(n))
			}
			if at, err := runGit(path, "log", "-1", "--date=unix", "--pretty=format:%at", plan.Branch); err == nil {
				one.LastAt, _ = strconv.ParseInt(strings.TrimSpace(at), 10, 64)
			}
		}
		if plan.Dir != "" && Dirty(plan.Dir) {
			if dirty, err := runGit(plan.Dir, "status", "--porcelain"); err == nil {
				one.Dirty = len(splitLines(dirty))
			}
		}
		out.Members = append(out.Members, one)
	}

	// ── 最近合进主干的那几次 ──
	//
	//	**只列 bot 留下的那些**: 撤一条你自己手敲的提交, 这里不该越俎代庖.
	if out.Main != "" {
		if log, err := runGit(path, "log", "--no-merges", "--date=unix", "--max-count=12",
			"--pretty=format:%H\x1f%an\x1f%at\x1f%p\x1f%s", out.Main); err == nil {
			for _, line := range splitLines(log) {
				parts := strings.SplitN(line, "\x1f", 5)
				if len(parts) < 5 {
					continue
				}
				at, _ := strconv.ParseInt(parts[2], 10, 64)
				out.Merges = append(out.Merges, CommitLine{
					Hash: parts[0][:min(len(parts[0]), 8)], Who: parts[1], At: at,
					Merge: len(strings.Fields(parts[3])) > 1, Subject: parts[4],
				})
			}
		}
	}
	// 交接: 它改变的是"这个项目里谁负责什么" —— 项目层面的事实,
	// 不该只留在两个人的聊天记录里. 见 handoff_log.go
	for _, one := range handoffsIn(path) {
		out.Handoffs = append(out.Handoffs, HandoffLine{
			From: one.From, To: one.To, At: one.At, Files: len(one.Files),
		})
	}
	return out
}

/**
 * lineOf 某条分支最后那一条**人干的事**.
 *
 *	跳过合并提交: "Merge branch 'master' into neox/小查" 是管道动作,
 *	它出现在"主干最近"那一格里, 等于把最要紧的一行让给了噪音.
 */
func lineOf(dir, branch string) *CommitLine {
	return commitAt(dir, branch, true)
}

/**
 * commitAt 精确到某一条.
 *
 *	**撤销那条路必须用它**: 带 --no-merges 去查一个合并提交, git 会
 *	一路往前走到第一条非合并提交 —— 于是你按的是"撤这条", 撤掉的是
 *	另一条. 那种错没人能在事后看出来.
 */
func commitAt(dir, rev string, skipMerges bool) *CommitLine {
	if rev == "" {
		return nil
	}
	args := []string{"log", "-1", "--date=unix", "--pretty=format:%H\x1f%an\x1f%at\x1f%p\x1f%s"}
	if skipMerges {
		args = append(args, "--no-merges")
	}
	out, err := runGit(dir, append(args, rev)...)
	if err != nil {
		return nil
	}
	parts := strings.SplitN(strings.TrimSpace(out), "\x1f", 5)
	if len(parts) < 5 {
		return nil
	}
	at, _ := strconv.ParseInt(parts[2], 10, 64)
	return &CommitLine{Hash: parts[0][:min(len(parts[0]), 8)], Who: parts[1], At: at,
		Merge: len(strings.Fields(parts[3])) > 1, Subject: parts[4]}
}

/**
 * RevertOn 撤掉主干上的一条改动.
 *
 *	**用 revert 不用 reset**: 历史不改写. 别人的 worktree 已经从主干
 *	拉过东西了, 抹掉一段历史等于让他们下一次 sync_down 撞上一堆莫名
 *	其妙的冲突 —— 而那时候没人会想到是这儿.
 *
 *	主干脏着的时候拒绝: 那上面有你自己没提交的改动, revert 会跟它搅在
 *	一起, 出了事分不清是谁弄的.
 */
func RevertOn(project, hash string) (string, error) {
	if project == "" || strings.TrimSpace(hash) == "" {
		return "", fmt.Errorf("没说撤哪一条")
	}
	if _, err := gitBin(); err != nil {
		return "", err
	}
	lock := lockFor(project)
	lock.Lock()
	defer lock.Unlock()

	if Dirty(project) {
		return "", fmt.Errorf("主干上还有没提交的改动（那是你自己手上的东西），先收拾一下再撤")
	}
	// **精确到这一条** —— 见 commitAt: 撤销不许走到别的提交上
	line := commitAt(project, hash, false)
	if line == nil {
		return "", fmt.Errorf("找不到 %s 这条提交", hash)
	}
	/**
	 * 标题自己写, 别用 git 的缺省.
	 *
	 *	--no-edit 生成的是 `Revert "原标题"` —— 整套东西的记录都在说
	 *	中文, 而这一条冒出来一个英文动词. git log 是给人看的, 而"我
	 *	什么时候撤过什么"恰恰是事后最要紧的那几条之一.
	 */
	args := []string{"-c", "user.name=NeoxOS", "-c", "user.email=os@neox.local",
		"revert", "--no-edit"}
	// 合并提交要说清"撤回到哪一边" —— -m 1 是主干那一边
	if line.Merge {
		args = append(args, "-m", "1")
	}
	args = append(args, hash)
	if out, err := runGit(project, args...); err != nil {
		// 撤到一半撞上冲突: 先退干净, 不留一个"正在 revert"的仓库给用户
		_, _ = runGit(project, "revert", "--abort")
		return "", fmt.Errorf("撤不掉 %s：%s。这条改动后面又被人改过，得手工来", hash, firstLine(out, 120))
	}
	// 刚建出来的这条是我们自己的, 改它的标题不动任何别人的历史
	_, _ = runGit(project, "-c", "user.name=NeoxOS", "-c", "user.email=os@neox.local",
		"commit", "--amend", "-q", "-m", "撤掉「"+firstLine(line.Subject, 60)+"」")
	return fmt.Sprintf("撤掉了「%s」（%s 的那条）。主干上多了一条反向提交，历史没动 —— "+
		"别人下次 sync_down 会正常拿到这个状态。", line.Subject, line.Who), nil
}

func lastSeg(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return parts[len(parts)-1]
}
