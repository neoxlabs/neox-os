package osinit

import (
	"strings"
	"testing"
)

// **每次验收都是我手工拼一遍脚本, 而脚本会拼错.**
//
// S48 到 S62 一共拼了九份一次性脚本, 而其中:
//
//	S50  feed() 里用 $4 想拿外层函数的端口 —— shell 的位置参数是每个
//	     函数自己的, curl 静默失败, 两边都显示"账本 0 条",
//	     看起来像"改前也没问题". **差点得出完全相反的结论.**
//	S54  拿了一组闸会拒的参数去跑, 跑出来的数据证明不了它要证明的事
//
// **验收的可信度取决于我这次脚本拼对没有** —— 而那是最不该靠运气的地方.
// 所以把"跑哪两天、怎么判、什么时候拒绝跑"收进代码.
func TestAcceptNeedsBothDays(t *testing.T) {
	// 只跑了一天 —— 不能算通过. 只验一边的话, 见门就喊的和从不开口的
	// 各能混过去一半(S62)
	r := AcceptResult{Days: []DayVerdict{{Name: "有事发生的一天", OK: true}}}
	if r.OK() {
		t.Fatal("只跑了一天却算通过 —— 只验一边等于没验")
	}
	if !strings.Contains(r.Text(), "只跑了") {
		t.Fatalf("没说清是少跑了一天:\n%s", r.Text())
	}
}

// 一天没过, 整体就不过, 而且要说清是哪一天
func TestAcceptFailsLoudlyOnOneBadDay(t *testing.T) {
	r := AcceptResult{Days: []DayVerdict{
		{Name: "有事发生的一天", OK: true, Said: 1, Want: 1},
		{Name: "全是琐事的一天", OK: false, Said: 3, Want: 0,
			Problems: []string{"开口 3 次, 期望 0 次"}},
	}}
	if r.OK() {
		t.Fatal("有一天没过却算通过")
	}
	if !strings.Contains(r.Text(), "全是琐事的一天") {
		t.Fatalf("没说清是哪一天没过:\n%s", r.Text())
	}
}

// **尺子坏了就别跑** —— 跑完给一份没意义的数据比不跑更糟:
// 它看起来是个结果(S50 那次"开口 0 次"跟成功一模一样)
func TestAcceptRefusesWhenRulerIsBroken(t *testing.T) {
	r := AcceptResult{RulerProblems: []string{
		"lock.front_door 开门 → 关上 只剩 1.7 秒(要 3 秒, 卡在设备)"}}
	if r.OK() {
		t.Fatal("尺子坏了却算通过")
	}
	if !strings.Contains(r.Text(), "尺子") {
		t.Fatalf("没说清是尺子的问题:\n%s", r.Text())
	}
	// 这种时候**不该再报那些数字** —— 它们全是空的
	if strings.Contains(r.Text(), "开口") {
		t.Fatalf("尺子坏了还在报数字:\n%s", r.Text())
	}
}

// 两天都过才算过, 而且要把两天都列出来 —— 只说"通过"的话,
// 人没办法知道它验的是不是自己以为的那两天
func TestAcceptPassListsBothDays(t *testing.T) {
	r := AcceptResult{Days: []DayVerdict{
		{Name: "有事发生的一天", OK: true, Said: 1, Want: 1},
		{Name: "全是琐事的一天", OK: true, Said: 0, Want: 0},
	}}
	if !r.OK() {
		t.Fatalf("两天都过却没算通过: %s", r.Text())
	}
	for _, want := range []string{"有事发生的一天", "全是琐事的一天"} {
		if !strings.Contains(r.Text(), want) {
			t.Fatalf("判决里没列出 %q:\n%s", want, r.Text())
		}
	}
}
