package main

import (
	"sort"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

/**
 * spend —— **这摊活到底花了多少**.
 *
 * ── 为什么这是个必须回答的问题 ──
 *
 *	用户的原话: "成本和进度放在设置页面里, 你单独去做做得好, 做的最重要的指标"。
 *
 *	一个人带着一屋子 bot 干活, 花的是真钱, 而现在这笔钱**只在账本里,
 *	界面上一个数都没有**. 那不只是"少了个页面": 看不见花费的人只有两种
 *	反应 —— 要么不敢用, 要么用到某天收到账单才发现. 两种都会让这套东西
 *	用不下去.
 *
 * ── 判据: 按谁分 ──
 *
 *	**按人, 再按项目**. 因为决策是按人做的: "小登太贵了"这句话能引出
 *	下一步(换个模型、换个人、把活拆细); 而"这个月花了 12 块"引不出任何
 *	东西.
 *
 * ── 数从哪儿来 ──
 *
 *	全部来自事件账本, 不另记一份:
 *	  · usage      每次推理的 prompt/completion/cached —— 花费的原始凭据
 *	  · proc.state 起停 —— 算"活了多久"和"起过几次"
 *	  · 提交       git 那边 —— 算"产出"
 *
 *	不另存一份账是有意的: 两份账迟早对不上, 而对不上的时候没人知道
 *	该信哪个.
 */

// Spend 一个 bot 花了多少、干出了什么.
type Spend struct {
	Bot     string `json:"bot"`
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch,omitempty"`
	// Prompt/Completion 送进去的和吐出来的 token
	Prompt     int64 `json:"prompt"`
	Completion int64 `json:"completion"`
	// Cached 命中前缀缓存的部分 —— **这一格是省下来的钱**, 单列出来
	Cached int64 `json:"cached"`
	// Calls 叫过几次模型. 一轮里会叫很多次(每一步一次)
	Calls int64 `json:"calls"`
	// Turns 说过几轮话 —— 人这边感受到的"用了多少次"
	Turns int64 `json:"turns"`
	// Commits 干出来几次提交 —— 花钱换来了什么
	Commits int `json:"commits"`
	// Starts 起过几次进程
	Starts int `json:"starts"`
	// FirstAt/LastAt 第一次和最后一次动静
	FirstAt int64 `json:"firstAt,omitempty"`
	LastAt  int64 `json:"lastAt,omitempty"`
}

// SpendReport 全机器的账.
type SpendReport struct {
	Bots []Spend `json:"bots"`
	// Total 合计 —— 单独算, 不让界面自己加(加法散在两处迟早对不上)
	Total Spend `json:"total"`
}

// spendFrom 从事件账本里算账.
//
//	events 是全部事件(跨重启), byPid 说明每个 pid 是谁 —— 进程会死,
//	对话不死, 一个 bot 的花费散在它历次进程上, 要合起来算.
func spendFrom(events []abi.Event, nameOf func(abi.ProcessID) (bot, project, branch string)) SpendReport {
	acc := map[string]*Spend{}
	get := func(pid abi.ProcessID) *Spend {
		bot, project, branch := nameOf(pid)
		if bot == "" {
			return nil
		}
		got, ok := acc[bot]
		if !ok {
			got = &Spend{Bot: bot, Project: project, Branch: branch}
			acc[bot] = got
		}
		// 项目和分支以**最近一次**为准: 它可能被换过地方
		if project != "" {
			got.Project = project
		}
		if branch != "" {
			got.Branch = branch
		}
		return got
	}

	for _, event := range events {
		row := get(event.PID)
		if row == nil {
			continue
		}
		if row.FirstAt == 0 || event.At < row.FirstAt {
			row.FirstAt = event.At
		}
		if event.At > row.LastAt {
			row.LastAt = event.At
		}
		body, _ := event.Payload.(map[string]any)
		switch event.Kind {
		case abi.EvProcState:
			if str(body, "state") == "created" {
				row.Starts++
			}
		case abi.EvProcOutput:
			switch str(body, "phase") {
			case "usage":
				row.Calls++
				row.Prompt += num(body, "prompt")
				row.Completion += num(body, "completion")
				row.Cached += num(body, "cached")
			case "start":
				// 一轮 = 一次请求启动处理 —— 对外计为一次使用
				row.Turns++
			}
		}
	}

	report := SpendReport{Bots: make([]Spend, 0, len(acc))}
	for _, row := range acc {
		report.Bots = append(report.Bots, *row)
		report.Total.Prompt += row.Prompt
		report.Total.Completion += row.Completion
		report.Total.Cached += row.Cached
		report.Total.Calls += row.Calls
		report.Total.Turns += row.Turns
		report.Total.Starts += row.Starts
	}
	// 花得最多的排最前 —— 这一页是拿来做决定的, 而决定通常关于最贵的那个
	sort.Slice(report.Bots, func(i, j int) bool {
		a, b := report.Bots[i], report.Bots[j]
		if a.Prompt+a.Completion != b.Prompt+b.Completion {
			return a.Prompt+a.Completion > b.Prompt+b.Completion
		}
		return a.Bot < b.Bot
	})
	return report
}

func str(body map[string]any, key string) string {
	if body == nil {
		return ""
	}
	got, _ := body[key].(string)
	return got
}

// num 数字字段. **两种形态都要认**: 活着的时候是 Go 的 int/int64,
// 从账本装回来之后是 JSON 的 float64 —— 只认一种的症状是
// "重启前有数、重启后全是 0", 而那看起来像功能坏了.
func num(body map[string]any, key string) int64 {
	if body == nil {
		return 0
	}
	switch got := body[key].(type) {
	case float64:
		return int64(got)
	case int64:
		return got
	case int:
		return int64(got)
	}
	return 0
}

// labelsOf 从进程规格里认出这是谁.
func labelsOf(info abi.ProcessInfo) (bot, project, branch string) {
	l := info.Spec.Labels
	return l["name"], l["project"], l["branch"]
}

// spendNow 算一次当下的账.
func spendNow(o *osinit.OS, plans *workPlanner) SpendReport {
	// pid → 是谁: 活着的从进程表认, 死掉的从它的创建事件认
	who := map[abi.ProcessID][3]string{}
	for _, info := range o.List() {
		bot, project, branch := labelsOf(info)
		who[info.PID] = [3]string{bot, project, branch}
	}
	events := flatEvents(o.Log().Snapshot())
	for _, event := range events {
		if event.Kind != abi.EvProcState {
			continue
		}
		if _, ok := who[event.PID]; ok {
			continue
		}
		body, _ := event.Payload.(map[string]any)
		labels, _ := body["labels"].(map[string]any)
		if labels == nil {
			continue
		}
		pick := func(key string) string {
			got, _ := labels[key].(string)
			return got
		}
		if name := pick("name"); name != "" {
			who[event.PID] = [3]string{name, pick("project"), pick("branch")}
		}
	}

	report := spendFrom(events, func(pid abi.ProcessID) (string, string, string) {
		got := who[pid]
		return got[0], got[1], got[2]
	})

	// 产出: 提交数从 git 那边读 —— 花钱换来了什么, 得有个不靠自述的答案
	for i := range report.Bots {
		row := &report.Bots[i]
		if plan, err := plans.planOf(row.Bot); err == nil && plan.Project != "" {
			// **合进主干的也算**: 按作者数, 不按"分支上还没合的" —— 见 CommitsBy
			row.Commits = CommitsBy(plan.Project, row.Bot)
			report.Total.Commits += row.Commits
		}
	}
	report.Total.Bot = "合计"
	return report
}
