package main

import "testing"

/**
 * **人按了停, 就别让"自动接着干"把它又叫起来**.
 *
 *	刹车的做法是杀掉这一轮再把同一个人起回来. 而"掉线自己接着干"那个勾
 *	看到的正是同一副样子: 工作区里有没提交的改动. 于是它立刻发一句
 *	"上次干到一半停了，看一眼接着做" —— 这种误触发会持续**87 秒**:
 *	人按了停, 它自己又干了 87 秒, 而那一轮的账本里写着"这一轮被叫停了".
 *
 *	自动接着干是给断电、退出这类意外准备的. 人明确按了停不是意外.
 */
func Test按了停就不该自动接着干(t *testing.T) {
	yes := func() bool { return true }
	if shouldResume(true, true, "neox/小辛", yes) {
		t.Error("人按了停, 它还是自己接着干了 —— 那不叫停得住")
	}
	// 断电、退出那种情况照旧要接着干 —— 别把整条功能修没了
	if !shouldResume(true, false, "neox/小辛", yes) {
		t.Error("意外断了却不接着干了")
	}
	// 勾没打就一律不动
	if shouldResume(false, false, "neox/小辛", yes) {
		t.Error("用户没打那个勾, 它自己动了")
	}
	// 干净收尾不该无中生有地叫它
	if shouldResume(true, false, "neox/小辛", func() bool { return false }) {
		t.Error("工作区干净的时候也叫了一声")
	}
	// 前面就不成立的时候, 那次 git 调用不该白跑
	asked := false
	shouldResume(true, true, "neox/小辛", func() bool { asked = true; return true })
	if asked {
		t.Error("已经确定不接着干了, 还去问了一次工作区脏不脏")
	}
}
