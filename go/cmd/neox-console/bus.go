package main

import (
	"fmt"
	"strings"
	"sync"
)

/**
 * bus —— **一件事发生了, 该知道的人要知道**.
 *
 * ── 为什么需要它 ──
 *
 *	用户的原话: "比如他 push 了一个代码合入主干, 有可能影响到我的功能。
 *	这时候我们其实需要有功能能力去触发这个事件给他的。"
 *
 *	分头干活之后, 每个人在自己的分支上, 看不见别人 —— 这正是隔离要的.
 *	但**有些事必须穿过隔离**: 我改的那个函数, 你也在改; 我合进主干的
 *	那份 db.py, 你正基于旧版往下写. 不告诉你, 你会一直在一个已经过时的
 *	地基上盖房子, 直到某一次合入的时候才发现 —— 那时候要改的东西多得多.
 *
 *	日常里这件事是人来做的: 有人在群里喊一声"我把 db 那块改了". 这里
 *	该由系统喊, 因为**系统知道得比人准**: 谁碰过哪些文件, git 说了算.
 *
 * ── 判据: 谁该收到 ──
 *
 *	**不是同项目的所有人**. 一个项目六个人, 每次合入都群发一遍, 三天
 *	之后没人会读它 —— 而通知一旦被无视, 它就等于不存在.
 *
 *	判据是**文件有没有交集**: 这次合进主干的文件里, 有没有你也改过的.
 *	有 = 你脚底下的东西真的动了, 必须知道; 没有 = 跟你无关, 不打扰.
 *	(git 两边都答得出: 合入的文件从这次合并读, 你改过的从你分支跟主干的
 *	分叉点读.)
 */

// Events 这台机器上的总线. 一个进程一份.
var Events = &Bus{}

// Merged 一次合入 —— 谁, 哪条分支, 动了哪些文件.
type Merged struct {
	Project string
	Bot     string
	Branch  string
	Files   []string
}

// Bus 一个进程内的事件总线.
//
//	**故意做得很小**: 现在只有一个真实的触发器(合入). 先把这一条做对,
//	再谈第二条 —— 一个没有真实用例的通用总线, 每一处都会为想象中的
//	第二个用例让步, 而那个用例往往长得跟想象的不一样.
type Bus struct {
	mu       sync.RWMutex
	onMerged []func(Merged)
}

// OnMerged 有人合进主干了.
func (b *Bus) OnMerged(fn func(Merged)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onMerged = append(b.onMerged, fn)
}

// PublishMerged 发出去.
//
//	**不阻塞合入那条路**: 通知投递要跑 git、要 Deliver, 而合入的人正
//	卡在工具调用里等返回. 让它先拿到"合进去了"这句话.
func (b *Bus) PublishMerged(event Merged) {
	if b == nil {
		return
	}
	b.mu.RLock()
	subs := append([]func(Merged){}, b.onMerged...)
	b.mu.RUnlock()
	for _, fn := range subs {
		go fn(event)
	}
}

// mergedFiles 这次合入动了哪些文件.
//
//	比的是**主干合入前后**: HEAD~1..HEAD 就是这一次带进来的全部改动.
func mergedFiles(project string) []string {
	out, err := runGit(project, "diff", "--name-only", "HEAD~1", "HEAD")
	if err != nil {
		return nil
	}
	return splitLines(out)
}

// touchedBy 这个 bot 在自己分支上碰过哪些文件.
//
//	从**它跟主干的分叉点**算起 —— 不然整个项目史都算它碰过的.
func touchedBy(dir, branch string) []string {
	base := baseOf(dir, branch)
	if base == "" {
		return nil
	}
	out, err := runGit(dir, "diff", "--name-only", base, branch)
	if err != nil {
		return nil
	}
	// 手上还没提交的也算 —— 它正在改的东西最要紧
	if status, err := runGit(dir, "diff", "--name-only", "HEAD"); err == nil {
		out += "\n" + status
	}
	return splitLines(out)
}

func splitLines(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

// overlap 两份文件名单的交集. 顺序按第一份.
func overlap(a, b []string) []string {
	in := map[string]bool{}
	for _, name := range b {
		in[name] = true
	}
	var out []string
	for _, name := range a {
		if in[name] {
			out = append(out, name)
		}
	}
	return out
}

// mergedNotice 给受影响的人的那句话.
//
//	**说清三件事**: 谁动了、动了你在改的哪几个、下一步该干什么.
//	少一件都会让收到的人去猜, 而猜出来的下一步通常是"再问一遍".
func mergedNotice(who string, shared []string, all int) string {
	// **话要短**: 它要的信息只有两条 —— 谁动了、动了你也在改的哪几个
	return fmt.Sprintf("[系统] %s合了主干，%s 你也在改。sync_down 拉一下再接着干。",
		who, strings.Join(shared, "、"))
}
