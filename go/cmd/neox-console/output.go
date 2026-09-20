package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

/**
 * output —— 一个 bot **干出来了什么**.
 *
 *	判据是 git, 不是它自己说的话.
 *
 *	原来"它干了什么"只能从对话里读: 它说"改好了", 而到底改了哪几个文件、
 *	改成什么样, 得人去翻。翻不动的时候就只能信它 —— 而信它正是这套东西
 *	最不该要求用户做的事。
 *
 *	现在每个 bot 有自己的分支(见 gitwork.go), 于是"它干了什么"有了一个
 *	不依赖它自述的答案: 分支上的提交, 加上工作目录里还没提交的改动.
 */

// Change 一个文件被怎么动了.
type Change struct {
	Path string `json:"path"`
	// Status git 的状态字母: M 改了 / A 新增 / D 删了 / ?? 还没进版本控制
	Status string `json:"status"`
	Add    int    `json:"add,omitempty"`
	Del    int    `json:"del,omitempty"`
}

// Commit 一次提交.
type Commit struct {
	Hash    string `json:"hash"`
	At      int64  `json:"at"`
	Subject string `json:"subject"`
	Files   int    `json:"files"`
	Add     int    `json:"add,omitempty"`
	Del     int    `json:"del,omitempty"`
}

// Output 这个 bot 的产物.
type Output struct {
	Branch string `json:"branch,omitempty"`
	Dir    string `json:"dir,omitempty"`
	// Base 从哪儿分出来的 —— 提交只数它之后的那些, 否则整个项目史都算它的
	Base    string   `json:"base,omitempty"`
	Commits []Commit `json:"commits"`
	// Dirty 还没提交的改动. **这一格不为空就是"活干了一半"**:
	// 没提交的东西对别人等于不存在, 交接的时候一个字都传不过去
	Dirty []Change `json:"dirty"`
	// Total 它一共留下过几次提交(**合进主干的也算**) —— 见 CommitsBy.
	//	Commits 那一列只有"还没合的", 合完就空了; 光看那一列会以为
	//	干完活的人什么都没干过.
	Total int    `json:"total"`
	Error string `json:"error,omitempty"`
}

// baseOf 这条分支跟主干的分叉点.
//
//	没有它的话 log 会把整个项目的历史都算成这个 bot 的产物 ——
//	而它可能昨天才来.
func baseOf(dir, branch string) string {
	for _, main := range []string{"master", "main"} {
		if out, err := runGit(dir, "merge-base", main, branch); err == nil {
			if hash := strings.TrimSpace(out); hash != "" {
				return hash
			}
		}
	}
	return ""
}

// OutputOf 读一个 bot 的产物. dir 是它的 worktree.
func OutputOf(dir, branch string) Output {
	out := Output{Branch: branch, Dir: dir, Commits: []Commit{}, Dirty: []Change{}}
	if dir == "" || branch == "" {
		return out
	}
	if _, err := gitBin(); err != nil {
		out.Error = err.Error()
		return out
	}
	out.Base = baseOf(dir, branch)

	// ── 提交 ──
	// 一行一条, 字段用 \x1f 隔开: 提交信息里什么字符都可能有, 用常见的
	// 分隔符(|、tab)迟早撞上. \x1f 是 ASCII 的 unit separator, 正是干这个的.
	scope := branch
	if out.Base != "" {
		scope = out.Base + ".." + branch
	}
	if log, err := runGit(dir, "log", "--no-merges", "--date=unix",
		"--pretty=format:%H\x1f%at\x1f%s", "--max-count=200", scope); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			parts := strings.Split(line, "\x1f")
			if len(parts) != 3 {
				continue
			}
			at, _ := strconv.ParseInt(parts[1], 10, 64)
			commit := Commit{Hash: shortHash(parts[0]), At: at * 1000, Subject: parts[2]}
			commit.Files, commit.Add, commit.Del = statOf(dir, parts[0])
			out.Commits = append(out.Commits, commit)
		}
	}

	out.Total = CommitsBy(dir, botOfBranch(branch))

	// ── 还没提交的 ──
	if status, err := runGit(dir, "status", "--porcelain=v1"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
			if len(line) < 4 {
				continue
			}
			out.Dirty = append(out.Dirty, Change{
				Status: strings.TrimSpace(line[:2]),
				Path:   strings.TrimSpace(line[3:]),
			})
		}
	}
	return out
}

/**
 * CommitsBy 这个 bot 一共留下过几次提交 —— **合进主干的也算**.
 *
 *	不能只数它分支上还没合的那些: 合入之后 merge-base 会前进, 于是
 *	"分支上独有的提交"变成 0 —— 干完活合上去, 功劳反而没了. 那正好是
 *	最不该发生的一件事.
 *
 *	判据是**作者**: 提交是以它的名字和邮箱记的(见 CommitTurn), 那条
 *	记录合到哪儿都跟着走.
 */
func CommitsBy(project, bot string) int {
	if project == "" || bot == "" {
		return 0
	}
	out, err := runGit(project, "log", "--all", "--author="+botSlug(bot)+"@neox.local",
		"--pretty=format:%H")
	if err != nil {
		return 0
	}
	return len(splitLines(out))
}

// statOf 这次提交动了几个文件、加了删了多少行.
func statOf(dir, hash string) (files, add, del int) {
	out, err := runGit(dir, "show", "--numstat", "--format=", hash)
	if err != nil {
		return 0, 0, 0
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		cols := strings.Fields(line)
		if len(cols) < 3 {
			continue
		}
		files++
		// 二进制文件那两格是 "-", 不参与行数
		if a, err := strconv.Atoi(cols[0]); err == nil {
			add += a
		}
		if d, err := strconv.Atoi(cols[1]); err == nil {
			del += d
		}
	}
	return files, add, del
}

func shortHash(full string) string {
	if len(full) > 8 {
		return full[:8]
	}
	return full
}

/**
 * Commit 把这一轮干出来的东西提交进它自己的分支.
 *
 * ── 为什么系统要替它提交 ──
 *
 *	提示词里已经写着"一件事干完就提交". 但那是**要求**, 不是保证 ——
 *	模型忘了、或者这一轮被打断了, 改动就留在工作目录里. 而没提交的东西
 *	对别人等于不存在: 交接传不过去, 合并合不进来, 界面上也说不出它干了什么.
 *
 *	所以系统兜一次底: 一轮说完话之后, 工作目录里还有改动就替它提交.
 *	这**不是替它做决定** —— 改动已经是它做出来的了, 提交只是把这件事
 *	记进账.
 *
 *	作者写成这个 bot: 谁改的就是谁改的, git log 里一眼看得出.
 */
func CommitTurn(dir, bot, branch, summary string, turn int64) (string, error) {
	return CommitTurnWith(dir, bot, branch, summary, turn, nil)
}

// CommitTurnWith 带上这一轮的验证证据 —— 见 verify.go.
func CommitTurnWith(dir, bot, branch, summary string, turn int64, verified []verifyRun) (string, error) {
	if dir == "" || branch == "" {
		return "", nil
	}
	if !Dirty(dir) {
		return "", nil // 没动过东西, 不留空提交
	}
	if _, err := runGit(dir, "add", "-A"); err != nil {
		return "", err
	}
	subject := strings.TrimSpace(summary)
	if subject == "" {
		subject = "干了一轮"
	}
	subject = firstLine(subject, 72)
	/**
	 * 尾注是**给机器读的**: 交接、统计、"这条提交是谁在第几轮做的"都靠它.
	 * 而 git 的 trailer 格式(Key: value)是公开约定, 不是我们发明的 ——
	 * git interpret-trailers、GitHub 都认。
	 */
	body := fmt.Sprintf("Neox-Bot: %s\nNeox-Turn: %d\nNeox-At: %s",
		bot, turn, time.Now().Format(time.RFC3339))
	// **验没验过由退出码说了算** —— 它挑的命令, 系统记的结果. 见 verify.go
	if trailer := verifyTrailer(verified); trailer != "" {
		body += "\n" + trailer
	}
	author := fmt.Sprintf("%s <%s@neox.local>", bot, botSlug(bot))
	if _, err := runGit(dir,
		"-c", "user.name="+bot, "-c", "user.email="+botSlug(bot)+"@neox.local",
		"commit", "--author", author, "-m", subject, "-m", body); err != nil {
		return "", err
	}
	hash, err := runGit(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}

// firstLine 提交标题只要第一行, 且不许太长 —— git 的惯例是 50~72 字.
func firstLine(text string, limit int) string {
	if idx := strings.IndexAny(text, "\n\r"); idx >= 0 {
		text = text[:idx]
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
