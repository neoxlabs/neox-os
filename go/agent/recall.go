package agent

import (
	"fmt"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// recall —— 查自己的过去.
//
// 事件日志一直在写, 但 agent 够不着. 用户问"上周那个报表怎么算的",
// 它会说"我没有跨会话的记忆" —— 那是把"我这个进程看不见"
// 说成了"这件事不存在". 清单 (layerRecent) 只有标题, 细节要来这儿查.
//
// 走 OS, 不读卷: 账本在卷外, agent 碰不到 (那条没变).
// 当前这段对话 OS 会排除 —— 它已经在 Window 里.
const recallDefaultLimit = 8

func RecallTool(recall func(query string, limit int) ([]abi.RecallHit, error)) Tool {
	return Tool{
		Name: "recall",
		Desc: "查以前的对话: 他说过什么、你做过什么、他批过什么。当前这段不用查",
		Args: map[string]string{
			"query": "关键词, 像搜文件那样写, 不要写成一句问话",
			"limit": "要几条, 不给就是 8",
		},
		ArgOrder: []string{"query", "limit"},
		Optional: map[string]bool{"limit": true},
		// 不出网、不碰文件系统. 世界会变 (新对话会进来), 所以 WorldSensitive.
		WorldSensitive: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			q := strings.TrimSpace(argStr(a, "query"))
			if q == "" {
				return "", fmt.Errorf("query 是空的。给一组关键词, 比如 " +
					"账本 用分, 而不是一句完整的问话")
			}
			if recall == nil {
				return "", fmt.Errorf("这个环境里查不到过去的对话")
			}
			n := argInt(a, "limit")
			if n <= 0 {
				n = recallDefaultLimit
			}
			hits, err := recall(q, n)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "", fmt.Errorf("%q 在以前的对话里没找到。换一组关键词试试"+
					"(更短、更具体), 别原样再查。当前这段对话你本来就看得见, 不用查", q)
			}
			return formatRecallHits(q, hits), nil
		},
	}
}

func formatRecallHits(query string, hits []abi.RecallHit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "「%s」在以前的对话里找到 %d 条 (当前这段对话不在里面):\n", query, len(hits))
	for i, h := range hits {
		title := strings.TrimSpace(h.Title)
		if title == "" {
			title = "(没有开场白)"
		}
		fmt.Fprintf(&b, "\n%d. [%s] %s · 「%s」\n   %s\n",
			i+1, h.When, h.Kind, title, strings.TrimSpace(h.Text))
	}
	return b.String()
}
