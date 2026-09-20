package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// **打错一个字, 命令就变成了给模型的提问.**
//
// 原来 switch 的 default 分支是"把这行发给模型" —— 于是 /退出、/攒着、
// /静音x 这类手误会**起一个进程**去问模型, 而模型会煞有介事地回答.
// 用户看到一段一本正经的胡话, 完全不知道自己只是敲错了一个字;
// 而且这一下是花钱的.
func TestTypoIsNotSentToTheModel(t *testing.T) {
	for _, line := range []string{"/退出", "/攒着", "/静音x", "/quitt", "/继续x"} {
		if got := classify(line); got.Kind != lineUnknownCommand {
			t.Errorf("%q 判成了 %v —— 它会被当成一句话发给模型, "+
				"模型会一本正经地回答一个手误", line, got.Kind)
		}
	}
}

// **以 / 开头的路径不是命令.**
//
// "/etc/hosts 里写了什么" 是一句正常的话. 一刀切地把所有 / 开头的
// 都当命令, 会把这类问题堵死, 而用户根本不知道为什么它不回答.
func TestPathsAreStillNormalTalk(t *testing.T) {
	for _, line := range []string{
		"/etc/hosts 里写了什么", "/var/log/syslog 看一下", "/home/x/a.txt 是什么",
		"3/4 是多少", "看看 /tmp",
	} {
		if got := classify(line); got.Kind == lineUnknownCommand {
			t.Errorf("%q 被当成了打错的命令 —— 这是一句正常的话", line)
		}
	}
}

// 每条命令的两种写法(中文/英文)都要认得, 参数要剥干净
func TestKnownCommandsParse(t *testing.T) {
	cases := []struct {
		line, name, arg string
	}{
		{"/退", "退", ""},
		{"/quit", "退", ""},
		{"/对话", "对话", ""},
		{"/继续 3", "继续", "3"},
		{"/resume 3", "继续", "3"},
		{"/静音 phone.mk/phone.moved", "静音", "phone.mk/phone.moved"},
		{"/mute phone.mk/phone.moved", "静音", "phone.mk/phone.moved"},
		{"/取消静音 phone.mk/phone.moved", "取消静音", "phone.mk/phone.moved"},
		{"/答 d1 yes", "答", "d1 yes"},
		{"/静音", "静音", ""},
	}
	for _, c := range cases {
		got := classify(c.line)
		if got.Kind != lineCommand || got.Name != c.name || got.Arg != c.arg {
			t.Errorf("%q 解成了 %+v, 期望 name=%q arg=%q", c.line, got, c.name, c.arg)
		}
	}
}

// **/取消静音 不能被 /静音 抢走.**
//
// 两条命令一个是另一个的前缀时, 匹配顺序就成了正确性的一部分 ——
// 而顺序错了的症状是"我明明打了取消, 它却又静了一遍", 没有任何报错.
func TestLongerPrefixWins(t *testing.T) {
	got := classify("/取消静音 a/b")
	if got.Name != "取消静音" {
		t.Fatalf("/取消静音 被 %q 抢走了 —— 用户打了取消, 结果又静了一遍", got.Name)
	}
}

// **表里登记的命令必须真的有人处理.**
//
// 这道闸跟 S32 那个是同一个形状: 加一条命令、忘了在 switch 里接上,
// 症状是"打了没反应" —— 而它会掉进 default, 于是变成给模型的提问.
// 反过来, switch 里有而表里没有的, 会被当成打错的命令挡在门外.
func TestEveryCommandIsHandled(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	// 从源码里现读 switch 认的名字 —— 手写一份清单等于把同一个
	// "记得改两处"的陷阱再搭一遍
	re := regexp.MustCompile(`case c\.Name == "([^"]+)"`)
	handled := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		handled[m[1]] = true
	}
	if len(handled) == 0 {
		t.Fatal("从 main.go 里一条命令都没读出来 —— 正则跟源码对不上了, " +
			"这道闸已经形同虚设")
	}
	var missing []string
	for _, c := range commands {
		if !handled[c.Name] {
			missing = append(missing, c.Name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("这些命令登记了却没人处理: %s —— 打了没反应, "+
			"而且会掉进 default 变成给模型的提问", strings.Join(missing, " "))
	}
	for name := range handled {
		found := false
		for _, c := range commands {
			if c.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("switch 里处理了 %q, 但 commands 表里没有 —— "+
				"用户打它会被当成打错的命令挡在门外", name)
		}
	}
}

// 不认识的命令要把有哪些命令说出来 —— 只说"没有这条命令"等于没说
func TestUnknownCommandTellsWhatExists(t *testing.T) {
	msg := unknownCommandHint("/退出")
	if !strings.Contains(msg, "/退") || !strings.Contains(msg, "/静音") {
		t.Fatalf("报错里没列出有哪些命令: %s", msg)
	}
}

// **一个顶层路径("/agentwork 下面有什么")跟一条打错的命令长得一模一样.**
//
// 分不开. 于是要选一边错, 而两边的代价不对称:
//
//	当成命令   用户多敲一次(而且报错里就写着怎么办)
//	当成话     起一个进程去问模型, 模型煞有介事地回答一个手误,
//	           用户不知道自己敲错了 —— 还花了钱
//
// 所以选前者, 但**报错必须告诉他下一步怎么做**, 否则这个选择就变成了
// "它不理我".
func TestBareTopLevelPathIsCaughtButExplained(t *testing.T) {
	if classify("/agentwork 下面有什么").Kind != lineUnknownCommand {
		t.Fatal("顶层路径没被拦下 —— 那它跟 /退出 一样会被发给模型")
	}
	hint := unknownCommandHint("/agentwork 下面有什么")
	if !strings.Contains(hint, "别用 / 开头") {
		t.Fatalf("没告诉用户下一步怎么做, 这就成了'它不理我': %s", hint)
	}
}
