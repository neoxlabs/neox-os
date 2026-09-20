package agent

import (
	"fmt"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// web_search —— 搜一次公开网络.
//
// ── 它跟 fetch 是两件事, 分开是刻意的 ──
//
//	web_search   给它一张地图: 哪几个地址可能有答案
//	fetch        去其中一个地址把正文取回来 —— **这一步过能力集**
//
// 合成一个"搜索并抓取"的工具会省一轮往返, 但那等于让搜索变成绕过出网
// 审批的旁路: 用户批的是"允许连 pypi.org", 而一个自动抓取的搜索工具
// 能把任何搜到的站都读一遍.
//
// ── 为什么它不需要 net 能力 ──
//
// 搜索走 OS (abi.MSearch), key 在引擎手里. 进程的 netns 里依然可以
// 一张网卡都没有 —— 跟推理是同一条.
//
// 代价要说清楚: **搜索词会离开这台机器**, 送到搜索供应商那里.
// 这件事记在事件日志里 (abiserver 的 phase=search), 不做逐次审批 ——
// 逐次审批的搜索没人用得下去, 而一个没人用的功能等于没做.
const searchDefaultLimit = 5

// SearchTool 搜索工具. search 为 nil 时**不要挂它** ——
// 见 DefaultToolsWith: 不承诺做不到的事.
func SearchTool(search func(query string, limit int) ([]abi.SearchHit, error)) Tool {
	return Tool{
		Name: "web_search",
		Desc: "搜公开网络, 拿到标题+链接+摘要。要正文再用 fetch 取",
		Args: map[string]string{
			"query": "搜索词。像用搜索引擎那样写关键词, 不要写成一句问话",
			"limit": "要几条, 不给就是 5",
		},
		ArgOrder: []string{"query", "limit"},
		Optional: map[string]bool{"limit": true},
		// 不声明 Needs: 搜索不出网(走 OS), 也不碰文件系统.
		// **这不是"给它开了个后门"** —— 它拿不到网页正文, 拿正文要走 fetch,
		// 而 fetch 是 net 轴的.
		//
		// 世界会变: 同一个词今天明天搜出来的不一样, 所以 WorldSensitive.
		WorldSensitive: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			q := strings.TrimSpace(argStr(a, "query"))
			if q == "" {
				return "", fmt.Errorf("query 是空的。给一组关键词, 比如 " +
					"golang landlock ABI 版本, 而不是一句完整的问话")
			}
			n := argInt(a, "limit")
			if n <= 0 {
				n = searchDefaultLimit
			}
			hits, err := search(q, n)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				// **空结果不是错误**, 是一个真实的答案: 换个词, 别重试同一条
				return "", fmt.Errorf("%q 什么都没搜到。换一组关键词试试"+
					"(更短、更具体、或者换成英文), 别原样重搜", q)
			}
			return formatWebHits(q, hits), nil
		},
	}
}

// formatWebHits 结果排版.
//
// 编号 + 链接单独一行, 是为了让模型能**原样使用链接调用 fetch** ——
// 混在一段话里的 URL 抄错一个字符就是一次白跑.
//
// 末尾那句提示是必要的: 只给摘要的话, 模型很容易拿摘要当事实直接回答,
// 而摘要是搜索引擎截的, 常常缺上下文甚至过期.
func formatWebHits(query string, hits []abi.SearchHit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "「%s」搜到 %d 条:\n", query, len(hits))
	for i, h := range hits {
		title := strings.TrimSpace(h.Title)
		if title == "" {
			title = "(无标题)"
		}
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, title, h.URL)
		if s := strings.TrimSpace(h.Snippet); s != "" {
			fmt.Fprintf(&b, "   %s\n", s)
		}
	}
	b.WriteString("\n(这些只是摘要。要拿它当依据回答之前, " +
		"用 fetch 把对应链接的正文取回来看 —— 摘要是搜索引擎截的, 可能过期或缺上下文)")
	return b.String()
}
