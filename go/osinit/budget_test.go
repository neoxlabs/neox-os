package osinit

import (
	"context"
	"errors"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 缓存命中不能按全价记账.
//
// 工程对话的缓存命中率可稳定在 97~100%, 而记账把它们按新
// token 全价收 —— 数字比真实开销大近十倍, 止损线也就提前十倍触发.
//
// 现象极具迷惑性: 进程停在"止损线到了", 而对话才十几轮、上下文才 11K.
// 看着像预算配小了, 其实是**记账口径错了**: 同一段前缀每轮重新收费.
func TestCachedTokensAreNotChargedAtFullPrice(t *testing.T) {
	// 典型的一轮: 11000 prompt, 其中 10800 命中缓存, 输出 200
	full := billableTokens(abi.InferResult{
		PromptTokens: 11000, CachedTokens: 10800, CompletionTokens: 200})
	if full >= 11200 {
		t.Fatalf("缓存命中按全价算了: %d", full)
	}
	// 200 未命中 + 10800/10 + 200 输出 = 1480
	if full != 1480 {
		t.Fatalf("记账口径变了: %d", full)
	}
}

// 没有缓存时不该打折 —— 那是另一个方向的错
func TestUncachedIsChargedInFull(t *testing.T) {
	if got := billableTokens(abi.InferResult{
		PromptTokens: 1000, CompletionTokens: 300}); got != 1300 {
		t.Fatalf("没有缓存时应当全价: %d", got)
	}
}

// 供应商偶尔报出 cached > prompt.
//
// 直接相减会变成负数, 于是这一轮不但不花钱还**倒退** —— 止损线永远到不了,
// 一个跑飞的进程能一直烧下去. 缺省必须是安全的那个方向.
func TestBogusCacheCountCannotRewindTheMeter(t *testing.T) {
	got := billableTokens(abi.InferResult{
		PromptTokens: 100, CachedTokens: 999999, CompletionTokens: 50})
	if got < 50 {
		t.Fatalf("记账倒退了 (%d) —— 止损线会永远到不了", got)
	}
}

// 撞的是哪条线, 在**源头**就得说清楚.
//
// 三条线(token / 网络调用数 / 活跃时长)原来撞哪条都返回同一个裸哨兵,
// 而这里是唯一知道真相的地方. 丢在这儿, 后面每一层都只能猜:
// agent 只能说"止损线", shell 猜成"步数上限", 真机日志里打出来的是
// **`⏹ 到止损线了 (上限 <nil> 步)`** —— 而步数默认无限, 真正用完的是钱.
func TestBudgetErrorSaysWhichLineAndHowMuch(t *testing.T) {
	cases := []struct {
		name  string
		b     abi.Budget
		delta abi.BudgetDelta
		kind  string
		limit int64
	}{
		{"token", abi.Budget{Tokens: p64(100)}, abi.BudgetDelta{Tokens: p64(150)}, "tokens", 100},
		{"网络调用", abi.Budget{NetCalls: p64(2)}, abi.BudgetDelta{NetCalls: p64(5)}, "netCalls", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := devOS()
			defer o.Shutdown("t")
			done := make(chan error, 1)
			_, err := o.Spawn(abi.ProcessSpec{App: "t", Budget: &tc.b},
				InprocBody{Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
					done <- pc.Spend(tc.delta)
					return nil, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got == nil {
				t.Fatal("超了却没报错")
			}
			// ① 老的判断不许因为错误变详细而失效
			if !errors.Is(got, ErrBudgetExceeded) {
				t.Fatalf("errors.Is 不成立了: %v", got)
			}
			// ② 得说清是哪条线
			var be *BudgetExceeded
			if !errors.As(got, &be) {
				t.Fatalf("还是个裸哨兵, 撞了哪条线查不出来: %v", got)
			}
			if be.Kind != tc.kind {
				t.Fatalf("线报错了: %s, 要 %s", be.Kind, tc.kind)
			}
			// ③ 得带上实数 —— 只说"超了"用户没法决定是加预算还是看它瞎跑
			if be.Limit != tc.limit || be.Spent <= be.Limit {
				t.Fatalf("实数不对: 花了 %d 上限 %d", be.Spent, be.Limit)
			}
		})
	}
}

func p64(v int64) *int64 { return &v }
