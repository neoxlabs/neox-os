package osinit

import (
	"strings"
	"testing"
)

/**
 * **协议属于 OS, 不属于某一个客户端**.
 *
 *	分工提示若只写在一个客户端里, 场景测试台走 /say 直接投时,
 *	收到的那句话一个字的分工提示都没有 —— 于是两个人各自搭了一套
 *	Vite 脚手架, 其中一套需要随后清掉.
 *
 *	换个客户端协议就没了, 而 bot 那边完全看不出少了什么.
 */
func TestFanout名单第一个负责拆活(t *testing.T) {
	names := []string{"小甲", "小乙", "小丙"}
	lead := fanoutNote(names, 0)
	if !strings.Contains(lead, "你来分") || !strings.Contains(lead, "pass") {
		t.Errorf("没告诉第一个人他来拆: %q", lead)
	}
	// 名单里其余的人要报上名, 但**不报自己**
	if !strings.Contains(lead, "小乙、小丙") || strings.Contains(lead, "小甲、") {
		t.Errorf("发给了谁没说对: %q", lead)
	}
	other := fanoutNote(names, 2)
	if !strings.Contains(other, "等小甲分工") || !strings.Contains(other, "先别改文件") {
		t.Errorf("没告诉其他人等谁: %q", other)
	}
	// 留一个口子: 只是答句话的不用等
	if !strings.Contains(other, "不用等") {
		t.Errorf("把只答句话的也拦住了: %q", other)
	}
}

// 只发给一个人就一个字都不多说 —— 那段每轮都要付钱
func TestFanout一个人不多说(t *testing.T) {
	if got := fanoutNote([]string{"小甲"}, 0); got != "" {
		t.Errorf("%q", got)
	}
	if got := fanoutNote(nil, 0); got != "" {
		t.Errorf("%q", got)
	}
	if got := fanoutNote([]string{"小甲", "小乙"}, 5); got != "" {
		t.Errorf("名单里没有的人也给了话: %q", got)
	}
}

// **话要短**: 这段每一轮都跟着用户那句话送进模型
func TestFanout话不许长(t *testing.T) {
	got := fanoutNote([]string{"小甲", "小乙", "小丙"}, 1)
	if n := len([]rune(got)); n > 70 {
		t.Errorf("%d 字, 太长了:\n%s", n, got)
	}
}

/**
 * **两种叫法, 同一套协议**.
 *
 *	pid 空 → OS 投给 pids 里每一个(话都一样).
 *	pid 给了 → 只投给他, 而 pids 说明这话是一屋子人一起收到的 ——
 *	界面走这条, 因为每个人要捎的上下文不一样.
 *
 *	不管哪条, 那段"还发给了谁、谁拆活"都由 OS 加.
 */
func TestFanout按名单里的位置给话(t *testing.T) {
	pids := []string{"p1", "p2", "p3"}
	if got := indexOf(pids, "p2"); got != 1 {
		t.Fatalf("位置算错了: %d", got)
	}
	if got := indexOf(pids, "谁啊"); got != -1 {
		t.Fatalf("名单里没有的人不该有位置: %d", got)
	}
	// 名单里没有他 → 一个字都不加(而不是把他当第一个)
	if got := fanoutNote([]string{"小甲", "小乙"}, -1); got != "" {
		t.Fatalf("给了个不在名单里的人一段话: %q", got)
	}
}
