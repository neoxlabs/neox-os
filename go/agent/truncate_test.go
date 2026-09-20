package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 截断要说清是哪一种 —— 两种截断的出路完全不同.
//
// 真机日志: **6 次截断, 6 次都是推理吃光了额度**(已出 16000, 其中推理 16000,
// 正文 0), 而每次给的建议都是"把文件拆小分几次写" —— 它根本没在写文件.
// 报错报的不是真实发生的那件事, 于是动作也是错的, 每错一次的代价是
// 整整一轮 16000 输出 token.
func TestTruncationByReasoningSaysSo(t *testing.T) {
	err := truncatedErr(abi.InferResult{
		FinishReason: "length", CompletionTokens: 16000, ReasoningTokens: 16000})
	msg := err.Error()
	// ① 必须说清是推理占满, 不是正文太长
	if !strings.Contains(msg, "推理") || !strings.Contains(msg, "一个字答案都没出来") {
		t.Fatalf("没说清是推理吃光了额度: %s", msg)
	}
	// ② **不许给"拆文件"这种照做不了的动作** —— 它什么都没写
	if strings.Contains(msg, "拆小分几次写") {
		t.Fatalf("它一个字都没写, 却被告知去拆文件: %s", msg)
	}
	// ③ 得给一个能直接照做的动作
	if !strings.Contains(msg, "下一小步") {
		t.Fatalf("没给能直接照做的动作: %s", msg)
	}
}

// 反向: 真的是正文太长时, 还是要给"拆开写"那条 —— 放宽过头等于两边都说不准
func TestTruncationByContentStillSaysSplit(t *testing.T) {
	err := truncatedErr(abi.InferResult{
		FinishReason: "length", CompletionTokens: 16000, ReasoningTokens: 300})
	msg := err.Error()
	if !strings.Contains(msg, "拆小分几次写") {
		t.Fatalf("正文太长时该给拆开写: %s", msg)
	}
	if strings.Contains(msg, "一个字答案都没出来") {
		t.Fatalf("正文有 15700 token, 不该说一个字都没出来: %s", msg)
	}
}

// **小额度下不许误判成"推理占满"**.
//
// 这条是真机抓出来的: 第一版按绝对量判(正文 < 200 就算推理占满), 单元测试
// 全绿 —— 因为那几个数字是我照着阈值挑的. 把额度压到 120 一跑:
// 推理 33、正文 87, 占比才 27%, 却被报成"额度全花在推理上了".
func TestSmallBudgetIsNotMisreadAsReasoningExhaustion(t *testing.T) {
	msg := truncatedErr(abi.InferResult{
		FinishReason: "length", CompletionTokens: 120, ReasoningTokens: 33}).Error()
	if strings.Contains(msg, "一个字答案都没出来") {
		t.Fatalf("推理只占 27%%, 不该说额度全花在推理上: %s", msg)
	}
}

// 模型不报 reasoning 时(很多供应商不报), 退回原来那条 —— 别凭 0 就断言"没推理"
func TestNoReasoningFieldFallsBackToContentAdvice(t *testing.T) {
	msg := truncatedErr(abi.InferResult{
		FinishReason: "length", CompletionTokens: 16000}).Error()
	if strings.Contains(msg, "一个字答案都没出来") {
		t.Fatalf("供应商没报推理量, 不能据此断言是推理占满: %s", msg)
	}
}

// 三个数都得摆出来 —— 只说"截断了"没法判断是哪一种
func TestTruncationShowsTheNumbers(t *testing.T) {
	msg := truncatedErr(abi.InferResult{
		FinishReason: "length", CompletionTokens: 16000, ReasoningTokens: 15900}).Error()
	for _, want := range []string{"16000", "15900", "100"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("缺少数字 %s: %s", want, msg)
		}
	}
}
