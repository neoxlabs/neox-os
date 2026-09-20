package osinit

import (
	"fmt"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 一天跑完之后, 它到底证明了什么.
//
// ── 为什么要能对着真账本跑 ──
//
// 判断既用于 Go 测试, 也用于另一台机器上的 jsonl 账本.
// **逐条肉眼翻日志不能保证验收可重复**: 测试和真实运行必须使用
// 同一套检查, 才能避免漏看信号或误数开口次数.
//
// ── 顺序不能反 ──
//
// 先问"这一天真的发生过吗", 再问"它说了几次".
// 如果时间压缩使对照日的两次开门丢失(账本里一条 lock 信号都没有),
// 那一天的"开口 0 次"**看起来跟成功一模一样**, 却没有验证开门时的判断.

// DayExpect 一天该长什么样
type DayExpect struct {
	Name string
	// MustHave 这几种信号必须真的进过账本.
	// 没有的话这一天不作数 —— 它要证明的事压根没发生
	MustHave []string
	// Notices 它该开口几次
	Notices int
}

// DayVerdict 判决
type DayVerdict struct {
	Name     string
	OK       bool
	Problems []string
	// Said 实际开口几次; Want 期望几次
	Said, Want int
}

// JudgeDay 对着一份账本判一天.
func JudgeDay(events []abi.Event, want DayExpect) DayVerdict {
	r := AnalyzeLedger(events)
	v := DayVerdict{Name: want.Name, Want: want.Notices, Said: sumOf(r.Notices)}

	// ① 这一天真的发生过吗
	for _, need := range want.MustHave {
		found := false
		for _, k := range r.Kinds {
			if strings.Contains(k.Kind, need) {
				found = true
			}
		}
		if !found {
			v.Problems = append(v.Problems, fmt.Sprintf(
				"账本里根本没有 %s —— 这一天要证明的事**压根没发生**, "+
					"下面的数字全是空的(尺子坏了, 不是系统安静)", need))
		}
	}
	if len(v.Problems) > 0 {
		// **这一天不作数, 别再拿它的数字说话** —— 顺带夸一句"开口 0 次
		// 符合预期"会让人以为只是缺了点数据, 而实际上整份结论都是空的
		return v
	}

	// ② 才轮到它说了几次
	if v.Said != want.Notices {
		v.Problems = append(v.Problems, fmt.Sprintf(
			"开口 %d 次, 期望 %d 次", v.Said, want.Notices))
	}
	v.OK = len(v.Problems) == 0
	return v
}

func (v DayVerdict) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "── %s ─────────────────────\n\n", v.Name)
	if v.OK {
		fmt.Fprintf(&b, "✓ 通过: 开口 %d 次, 跟期望一致\n", v.Said)
		return b.String()
	}
	b.WriteString("✗ 没通过:\n")
	for _, p := range v.Problems {
		fmt.Fprintf(&b, "  · %s\n", p)
	}
	return b.String()
}
