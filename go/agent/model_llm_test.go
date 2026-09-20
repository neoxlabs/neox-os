package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

/**
 * 被截断的时候要说清**是在哪一步上**被截断的.
 *
 *	"正文太长, 把它拆小分几次写"是对的, 但它不知道哪一份太长. 例如
 *	"一直干下去"跑到第十轮撞上: 十轮下来文件越堆越大, 它在一次
 *	write_file 里被截断, 而报错一个字都没提是哪个文件 —— 只能猜.
 *
 *	被截断时供应商仍然把工具名给回来(参数是半截的). 那份现成的信息
 *	说出来, 出路就从"拆小"变成可以直接照做的一步.
 */
func Test截断要说清是在哪一步上(t *testing.T) {
	half := abi.InferResult{
		CompletionTokens: 16000, ReasoningTokens: 0, FinishReason: "length",
		ToolCalls: []abi.ToolCall{{Name: "write_file",
			Arguments: `{"path": "README.md", "content": "# 通讯录\n\n这是一`}},
	}
	got := truncatedErr(half).Error()
	if !strings.Contains(got, "README.md") {
		t.Errorf("没说清是哪个文件: %s", got)
	}
	if !strings.Contains(got, "edit_file") {
		t.Errorf("没给出能直接照做的下一步: %s", got)
	}

	// 参数半截到连路径都没写全 —— **只说工具名, 不许猜**
	worse := abi.InferResult{CompletionTokens: 16000, FinishReason: "length",
		ToolCalls: []abi.ToolCall{{Name: "write_file", Arguments: `{"pa`}}}
	if got := truncatedErr(worse).Error(); !strings.Contains(got, "write_file") {
		t.Errorf("连工具名都没说: %s", got)
	}

	// 推理吃光额度那条不能被冲掉 —— 两种截断两条完全不同的出路
	thinking := abi.InferResult{CompletionTokens: 16000, ReasoningTokens: 16000,
		FinishReason: "length"}
	if got := truncatedErr(thinking).Error(); !strings.Contains(got, "推理上") {
		t.Errorf("推理吃光额度那条被冲掉了: %s", got)
	}
}
