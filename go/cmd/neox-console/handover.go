package main

import (
	"fmt"
	"strings"
)

/**
 * handover —— **把一摊干到一半的活交给另一个人**.
 *
 * ── 为什么不能靠"拉个新人"了事 ──
 *
 *	现在换人接手只有一条路: 拉个新人, 它从主干开一条自己的分支.
 *	而前一个人**没合进主干的那些活全部拿不到** —— 那往往正是要交接的
 *	东西(干到一半、还没验完、所以才没合).
 *
 *	日常里交接是这么发生的: "我这块给你了, 我的分支在这儿, 我做到哪儿了,
 *	剩下什么没做". 三件事一件都不能少 —— 少了分支, 他从零开始;
 *	少了进度, 他得先读一遍代码去猜; 少了"剩下什么", 他会以为做完了.
 *
 * ── 前一个人的分支不删 ──
 *
 *	交接不是删除. 他做过的东西留在他名下(git 的作者是他), 那既是产物
 *	也是记录. 新人是从那条分支**分出去**接着干, 不是把它抢过来.
 */

// Handover 一次交接的结果 —— 说给接手的人听.
type Handover struct {
	From string
	To   string
	// Branch 接手的人拿到的新分支
	Branch string
	Dir    string
	// Commits 接过来多少次提交
	Commits int
	// Files 这摊活动过哪些文件 —— 接手的人第一眼要看的就是这个
	Files []string
	// Unfinished 还没提交的那些(交接时由系统替他收进分支)
	Unfinished int
	Message    string
}

/**
 * HandOver 把 from 的活交给 to.
 *
 *	做法是**从 from 的分支上分一条给 to**: 那条分支上有 from 干过的
 *	一切(包括没合进主干的), 于是 to 一签出就是"接着往下干"的状态,
 *	而不是"从主干重新开始".
 */
func HandOver(project string, from, to Plan, fromBot, toBot string) (Handover, error) {
	if from.Branch == "" {
		return Handover{}, fmt.Errorf("「%s」没走分支隔离，没有可以交出去的分支", fromBot)
	}
	if from.Project != to.Project {
		return Handover{}, fmt.Errorf("「%s」和「%s」不在同一个项目里，交接不过去", fromBot, toBot)
	}
	lock := lockFor(project)
	lock.Lock()
	defer lock.Unlock()

	out := Handover{From: fromBot, To: toBot, Branch: to.Branch, Dir: to.Dir}

	// ── 1. 他手上没提交的活先收进他自己的分支 ──
	//
	//	**不收就交不过去**: 没提交的东西只存在于他的工作目录里,
	//	而交接走的是 git —— 那正是"干到一半"最要紧的那部分.
	if Dirty(from.Dir) {
		before := CommitsBy(from.Project, fromBot)
		/**
		 * 标题要**说出这次发生了什么**.
		 *
		 *	这是整份记录里唯一一次我们百分之百知道情况的提交: 谁把活
		 *	交给了谁. 而它原来写的是"交接前把手上的活收一下" —— 在
		 *	"活到一半换人"那一轮的 git log 三条里有两条是这种套话,
		 *	谁在什么时候干了什么一个字看不出来.
		 */
		title := fmt.Sprintf("把手上的活交给%s", toBot)
		if _, err := CommitTurn(from.Dir, fromBot, from.Branch, title, 0); err != nil {
			return out, fmt.Errorf("「%s」手上的活收不起来: %w", fromBot, err)
		}
		if CommitsBy(from.Project, fromBot) > before {
			out.Unfinished = 1
		}
	}

	/**
	 * ── 2. 接手的人这边不能有**没合掉的**活 ──
	 *
	 *	他要是分支上还压着没合进主干的东西, 把基点挪走就是把那些弄丢.
	 *
	 *	**判据是"还没合掉的", 不是"干过活的"**: 按后者的话, 一个人只要
	 *	提交过一次就永远不能再接手别人的活了 —— 而合进主干正是"这摊活
	 *	已经交代完了"的意思, 那之后他本来就该能接新的.
	 */
	if Dirty(to.Dir) || len(OutputOf(to.Dir, to.Branch).Commits) > 0 {
		return out, fmt.Errorf("「%s」手上还有没合进主干的活，先让他合掉（merge_up）或者交出去，再接这一摊", toBot)
	}

	// ── 3. 把 to 的分支挪到 from 的分支上 ──
	//
	//	用 reset --hard 而不是新建: to 的 worktree 已经签出着它自己那条
	//	分支了, 换基点比换分支干净(不用退出 worktree 再重挂).
	if _, err := runGit(to.Dir, "reset", "--hard", from.Branch); err != nil {
		return out, fmt.Errorf("接不过来: %w", err)
	}

	// ── 4. 说清楚接到了什么 ──
	base := baseOf(to.Dir, to.Branch)
	if base != "" {
		if files, err := runGit(to.Dir, "diff", "--name-only", base, to.Branch); err == nil {
			out.Files = splitLines(files)
		}
		if log, err := runGit(to.Dir, "log", "--oneline", base+".."+to.Branch); err == nil {
			out.Commits = len(splitLines(log))
		}
	}
	out.Message = handoverNote(out)
	return out, nil
}

// handoverNote 交接说明 —— **给接手的人看的**.
//
//	三件事: 从谁那儿接的、接到了什么、第一步该干什么.
//	最后一句尤其要有: 接手的人最容易做的错事是"从头再来一遍",
//	而那等于把交接的意义全扔了.
func handoverNote(h Handover) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[系统] %s这摊活交给你了，他干的都在你分支上", h.From)
	if h.Commits > 0 {
		fmt.Fprintf(&b, "（%d 次提交", h.Commits)
		if h.Unfinished > 0 {
			b.WriteString("，还有没提交的也收进来了")
		}
		b.WriteString("）")
	}
	b.WriteString("。\n")
	if len(h.Files) > 0 {
		shown := h.Files
		if len(shown) > 8 {
			shown = shown[:8]
		}
		fmt.Fprintf(&b, "动过：%s", strings.Join(shown, "、"))
		if len(h.Files) > len(shown) {
			fmt.Fprintf(&b, " 等 %d 个", len(h.Files))
		}
		b.WriteString("\n")
	}
	b.WriteString("先看一眼他做到哪儿了再动手，别重来。")
	return b.String()
}
