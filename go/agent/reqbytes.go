package agent

import (
	"encoding/json"

	"github.com/neox-os/neox-os/abi"
)

// 一次请求到底有多少字节.
//
// ── 为什么必须数全 ──
//
// 这个数只有一个用途: 除以供应商报回来的 promptTokens, 得到"字节/token",
// 再乘模型窗口算出上下文预算. **两边口径必须一致** —— 分母是整个请求的
// token, 分子就得是整个请求的字节.
//
// 原来只数了 system + 每条消息的 Content 和 Role. 漏掉的是:
//
//	ToolCalls.Arguments  **最大的一块** —— write_file 的整段文件内容、
//	                     edit_file 的新旧串都在这儿, 而那条消息的
//	                     Content 是**空的**
//	工具声明             每次请求都带, 2760 字节
//	ToolCallID/Reasoning 单条不大, 但每轮都有
//
// 后果是个会自我加强的滑坡: 漏数 → 比率被低估 → 预算变小 → 更早折叠 →
// 折叠改写了历史里那一条 → **前缀缓存被打穿**. 一段 71 步的会话里
// 预算从 175131 一路缩到 65997(比率被压到接近下限 0.8), 折叠 50 次,
// 缓存命中在 96% 和 **9%** 之间反复跳, 同时伴着
// `⚑ 前缀缓存断了: 第 144 条消息(tool_result)被改写了`.
//
// ── 跟指纹算同一张表 ──
//
// cachefp.go 的 fingerprintOf 早就把"会进 wire format 的东西"列全了
// (role/content/toolCalls/toolCallID/reasoning). 计量漏项而指纹不漏,
// 说明这张表被抄了两遍且抄漏一次 —— 所以这里照着同一张表数.
//
// 不追求跟供应商的字节数完全相等: 要的是**不随会话内容结构漂移**.
// 少数几个 JSON 括号的误差会被滑动平均吸收, 漏掉一整类字段不会.
func requestBytes(sys string, tools []abi.ToolDef, msgs []abi.InferMessage) int {
	n := len(sys)
	if len(tools) > 0 {
		if tb, err := json.Marshal(tools); err == nil {
			n += len(tb)
		}
	}
	for _, m := range msgs {
		n += len(m.Role) + len(m.Content) + len(m.ToolCallID) + len(m.Reasoning)
		for _, c := range m.ToolCalls {
			n += len(c.ID) + len(c.Name) + len(c.Arguments)
		}
	}
	return n
}
