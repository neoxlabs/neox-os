package osinit

import (
	"errors"
	"fmt"
)

// 止损线撞了哪一条 —— 信息不能在源头就丢掉.
//
// ── 为什么必须带上 ──
//
// 原来三条线(token / 网络调用数 / 活跃时长)撞哪条都返回同一个裸哨兵
// `ErrBudgetExceeded`. 那之后每一层都只能猜:
//
//	osinit   知道是 token 超了、花了多少、上限多少 → 全扔掉
//	agent    只知道"止损线", 于是 budgetReason 两个分支返回同一个字符串
//	shell    按"步数上限"渲染, 打出 **"⏹ 到止损线了 (上限 <nil> 步)"**
//
// 真机日志里出现过两次. 用户看到的是"步数"用完了 —— 而**步数默认无限**,
// 真正用完的是钱(一段会话的计费合计 ≈ 1,918,537, 正好撞上 2,000,000).
// 这两件事的下一步动作完全不同: 一个是"让它多跑几步", 一个是
// "这活花了这么多, 要不要加预算". 报错的量不对, 用户就做不了决定.
//
// 错误消息会**原样穿过 ABI 边界**(wire 上带 code + message), 所以把话说清楚
// 就够了, 不用另起一套结构.
type BudgetExceeded struct {
	// Kind 撞的是哪条线: tokens / netCalls / activeMs
	Kind  string
	Spent int64
	Limit int64
}

func (e *BudgetExceeded) Error() string {
	// 不再自称"止损线" —— 调用方外层已经说过了, 重复一遍读起来是
	// "到止损线了 —— 止损线: ...". 这里只报**事实**: 哪条线, 花了多少, 上限多少.
	return fmt.Sprintf("%s 花了 %d, 上限 %d", e.Kind, e.Spent, e.Limit)
}

// Is 让 errors.Is(err, ErrBudgetExceeded) 继续成立 ——
// 已有的判断不该因为错误变详细而失效
func (e *BudgetExceeded) Is(target error) bool { return errors.Is(ErrBudgetExceeded, target) }
