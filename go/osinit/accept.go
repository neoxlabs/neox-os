package osinit

import (
	"fmt"
	"strings"
)

// 一次完整的真机验收 —— 两天一起.
//
// ── 为什么要收进代码 ──
//
// S48 到 S62 一共拼了九份一次性 shell 脚本, 而其中两份是错的:
//
//	S50  feed() 里用 $4 想拿外层函数的端口 —— shell 的位置参数是每个
//	     函数自己的, curl 静默失败, 两边都显示"账本 0 条", 看起来像
//	     "改前也没问题". **差点得出完全相反的结论.**
//	S54  拿了一组闸会拒的参数去跑, 跑出来的数据证明不了它要证明的事.
//
// **验收的可信度取决于我这次脚本拼对没有** —— 而那是最不该靠运气的
// 地方. 所以"跑哪两天、怎么判、什么时候拒绝跑"收进代码, 脚本只负责
// 起进程.
//
// ── 三条判据 ──
//
//	① 两天都要跑        只验一边的话, 见门就喊的和从不开口的各能混过去
//	                    一半(S62)
//	② 尺子坏了就别跑    跑完给一份没意义的数据比不跑更糟: 它看起来是个
//	                    结果(S50 那次"开口 0 次"跟成功一模一样)
//	③ 结论要点名        只说"通过"的话, 人没办法知道它验的是不是自己
//	                    以为的那两天

// AcceptResult 一次验收的结果
type AcceptResult struct {
	// RulerProblems 尺子本身的问题(压缩之后事件短得看不见).
	// **非空就是没跑** —— 那些数字全是空的
	RulerProblems []string
	Days          []DayVerdict
}

// needDays 一次验收要跑几天 —— 两天, 缺一不可
const needDays = 2

func (r AcceptResult) OK() bool {
	if len(r.RulerProblems) > 0 || len(r.Days) != needDays {
		return false
	}
	for _, d := range r.Days {
		if !d.OK {
			return false
		}
	}
	return true
}

func (r AcceptResult) Text() string {
	var b strings.Builder
	b.WriteString("══ 感知层验收 ═══════════════════════\n\n")
	// **尺子坏了就到此为止, 别再报那些数字** —— 它们全是空的,
	// 而报出来会让人以为只是缺了点数据
	if len(r.RulerProblems) > 0 {
		b.WriteString("✗ 没跑: **这把尺子是坏的**\n")
		for _, p := range r.RulerProblems {
			fmt.Fprintf(&b, "  · %s\n", p)
		}
		b.WriteString("\n跑完给一份没意义的数据比不跑更糟 —— 它看起来是个结果。\n")
		return b.String()
	}
	for _, d := range r.Days {
		mark := "✓"
		if !d.OK {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s %s: 开口 %d 次(期望 %d)\n", mark, d.Name, d.Said, d.Want)
		for _, p := range d.Problems {
			fmt.Fprintf(&b, "    · %s\n", p)
		}
	}
	if len(r.Days) != needDays {
		fmt.Fprintf(&b, "\n✗ 只跑了 %d 天 —— **两边都要验**: 只验有事那天的话,\n"+
			"   一个见门就喊的系统也能通过; 只验对照日的话,\n"+
			"   一个从不开口的系统也能通过。\n", len(r.Days))
		return b.String()
	}
	if r.OK() {
		b.WriteString("\n✓ 两边都过了。\n")
	} else {
		b.WriteString("\n✗ 没通过。\n")
	}
	return b.String()
}
