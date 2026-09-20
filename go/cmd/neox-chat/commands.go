package main

import (
	"fmt"
	"sort"
	"strings"
)

// 命令识别 —— 一行话是命令, 还是要说给它听的.
//
// ── 为什么要单独判 ──
//
// 原来是一个 switch, **default 分支是"把这行发给模型"**. 于是
// /退出、/攒着、/静音x 这类手误会起一个进程去问模型, 而模型会
// 煞有介事地回答. 用户看到一段一本正经的胡话, 完全不知道自己
// 只是敲错了一个字 —— 而且这一下是花钱的.
//
// ── 判据: 以 / 开头, 而且第一个词看起来不是路径 ──
//
// 一刀切地把所有 / 开头的都当命令是不行的: "/etc/hosts 里写了什么"
// 是一句正常的话, 堵掉它的话用户根本不知道为什么它不回答.
//
// 所以第一个词里**再出现 / 或 . 就当路径**. 这个判据很粗, 但它错的
// 方向是安全的: 判错了只是把一句话发给模型(本来就是默认行为),
// 而不是把用户真正的问题堵死.

type lineKind int

const (
	// lineTalk 要说给它听的
	lineTalk lineKind = iota
	// lineCommand 认得的命令
	lineCommand
	// lineUnknownCommand 看着像命令, 但不认得 —— **绝不能发给模型**
	lineUnknownCommand
)

// Cmd 一条命令的定义
type Cmd struct {
	Name    string   // 规范名(switch 里按它分支)
	Aliases []string // 认哪些写法, 长的写在前面 —— 见 classify
	Usage   string
}

// commands 全部命令.
//
// **/取消静音 必须排在 /静音 前面**: 两条命令一个是另一个的前缀时,
// 匹配顺序就是正确性的一部分 —— 顺序错了的症状是"我明明打了取消,
// 它却又静了一遍", 而且没有任何报错.
var commands = []Cmd{
	{Name: "退", Aliases: []string{"/退", "/quit"}, Usage: "/退 走人"},
	{Name: "对话", Aliases: []string{"/对话", "/list"}, Usage: "/对话 看历史"},
	{Name: "继续", Aliases: []string{"/继续", "/resume"}, Usage: "/继续 <编号> 接着聊"},
	{Name: "新", Aliases: []string{"/新", "/new"}, Usage: "/新 开一段新的"},
	{Name: "取消静音", Aliases: []string{"/取消静音", "/unmute"}, Usage: "/取消静音 <来源>/<种类>"},
	{Name: "静音", Aliases: []string{"/静音", "/mute"}, Usage: "/静音 [<来源>/<种类>] 让某类信号闭嘴"},
	{Name: "攒", Aliases: []string{"/攒", "/held"}, Usage: "/攒 看攒着没说的"},
	{Name: "答", Aliases: []string{"/答"}, Usage: "/答 <did> <选项> 回决策"},
}

// Line 一行输入解出来的东西
type Line struct {
	Kind lineKind
	Name string // Kind == lineCommand 时有效
	Arg  string
}

func classify(line string) Line {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return Line{Kind: lineTalk}
	}
	// 先按别名长度从长到短试 —— 短的是长的前缀时(/静音 vs /取消静音),
	// 顺序就是正确性
	type alias struct {
		text string
		name string
	}
	var all []alias
	for _, c := range commands {
		for _, a := range c.Aliases {
			all = append(all, alias{a, c.Name})
		}
	}
	sort.Slice(all, func(i, j int) bool { return len(all[i].text) > len(all[j].text) })
	for _, a := range all {
		if line == a.text {
			return Line{Kind: lineCommand, Name: a.name}
		}
		if strings.HasPrefix(line, a.text+" ") {
			return Line{Kind: lineCommand, Name: a.name,
				Arg: strings.TrimSpace(line[len(a.text):])}
		}
	}
	// 不认得. **第一个词里再有 / 或 . 就当路径**, 那是一句正常的话 ——
	// 判错的方向是安全的: 只是把一句话发给模型(本来就是默认行为),
	// 而不是把用户真正的问题堵死
	first := strings.Fields(line)[0]
	if strings.Contains(first[1:], "/") || strings.Contains(first, ".") {
		return Line{Kind: lineTalk}
	}
	return Line{Kind: lineUnknownCommand}
}

// unknownCommandHint 不认识的命令要**把有哪些说出来** ——
// 只说"没有这条命令"等于没说, 用户还得去翻文档
func unknownCommandHint(line string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "没有 %s 这条命令。有这些:\n", strings.Fields(line)[0])
	for _, c := range commands {
		fmt.Fprintf(&b, "   %s\n", c.Usage)
	}
	b.WriteString("(要是想让它做这件事, 别用 / 开头)")
	return b.String()
}
