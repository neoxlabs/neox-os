package agent

import (
	"strings"
	"testing"
)

// "这台机器上还有哪些对话"是**环境事实**, 跟"写只能写进卷"同一类.
//
// 典型失败是输入"我昨天让你做的那个东西做完了吗"时回答
// **"我没有跨会话的记忆, 看不到昨天的对话"** —— 而 OS 明明记着(事件日志是
// append-only 的, R12 那个开机横幅就是从同一份数据算出来的). 它把
// "我这个进程看不见"说成了"这件事不存在".
func TestPromptCarriesRecentConversations(t *testing.T) {
	digest := "- 「给 todo 加 done 命令」 12 轮 · 2 小时前\n" +
		"- 「做个记账小工具」 5 轮 · 昨天  ⚠ 没干完(停在第 5 步)"
	p := BuildSystemPrompt(NewToolSet(DefaultTools()), "/work", digest, "")
	if !strings.Contains(p, "给 todo 加 done 命令") {
		t.Fatal("对话清单没进提示词 —— 它会说自己没有跨会话记忆")
	}
	if !strings.Contains(p, "没干完(停在第 5 步)") {
		t.Fatal("没干完那条丢了 —— 那是清单里最值钱的一条")
	}
	// 得明确拦住那句错话
	if !strings.Contains(p, "我没有跨会话的记忆") {
		t.Fatal("没告诉它别说'我没有跨会话的记忆'")
	}
}

// 没有别的对话时**一个字都不加** —— 空段落是纯噪音, 还白占前缀
func TestNoRecentSectionWhenNothingToShow(t *testing.T) {
	p := BuildSystemPrompt(NewToolSet(DefaultTools()), "/work", "", "")
	if strings.Contains(p, "这台机器上还有哪些对话") {
		t.Fatal("没有别的对话却加了一整段")
	}
	// 空层不能留下多余的空行 —— 层与层之间恰好一个空行
	if strings.Contains(p, "\n\n\n\n") {
		t.Fatal("空层留下了连续空行")
	}
}

// 同样的输入必须逐字节一样 —— 系统段进前缀, 抖一下缓存整段作废
func TestRecentSectionIsByteStable(t *testing.T) {
	digest := "- 「甲」 1 轮 · 刚刚\n- 「乙」 2 轮 · 1 小时前"
	first := BuildSystemPrompt(NewToolSet(DefaultTools()), "/work", digest, "")
	for i := 0; i < 5; i++ {
		if BuildSystemPrompt(NewToolSet(DefaultTools()), "/work", digest, "") != first {
			t.Fatal("同样输入组装出了不同的系统段")
		}
	}
}

func TestPromptCarriesCorrections(t *testing.T) {
	digest := "- 纠正：「账本用分不要用元」"
	p := BuildSystemPrompt(NewToolSet(DefaultTools()), "/work", "", digest)
	if !strings.Contains(p, "账本用分不要用元") {
		t.Fatal("纠正没进提示词 —— 下一轮它会再犯同一件事")
	}
	if !strings.Contains(p, "别再问一遍") {
		t.Fatal("没告诉它按纠正做, 它会当成参考意见再确认一次")
	}
}

func TestNoCorrectionsSectionWhenNothingToShow(t *testing.T) {
	p := BuildSystemPrompt(NewToolSet(DefaultTools()), "/work", "", "")
	if strings.Contains(p, "他纠正过你的事") {
		t.Fatal("没有纠正却加了一整段")
	}
}
