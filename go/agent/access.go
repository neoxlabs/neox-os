package agent

import (
	"fmt"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// request_access —— agent 主动申请一条它现在没有的能力.
//
// ── 为什么必须有这个口子 ──
//
// 在这之前, 审批只在**它撞墙的那一刻**被动触发: 工具预检发现越界 → 问人.
// 那条路只覆盖得到"有 path 参数的文件操作", 而且是事后的.
//
// 结果是它遇到别的墙就只能干瞪眼. 最典型的就是出网:
// 一条 `npm install` 被代理挡回 403, 它看到的是一句失败,
// 然后大概率去换镜像源、加 --registry、重试 —— **全是白费**,
// 因为它缺的是授权, 而它没有任何办法说出"我需要授权"这件事.
//
// 有了这个工具, "我做不到"就能变成"我需要什么才能做到" ——
// 后者是可以被用户一句话解决的, 前者只能卡死.
//
// ── 为什么是工具而不是自动申请 ──
//
// 自动申请等于每撞一次墙就弹一次窗, 而模型撞墙常常是它自己路走错了
// (路径写错、命令拼错). 让它显式调用, 意味着它得先判断
// "这是我不该做的事, 还是我确实缺权限" —— 这个判断必须由它来做,
// 不能由一个"失败就弹窗"的规则代劳.

func accessTool() Tool {
	return Tool{
		Name: "request_access",
		Desc: "申请你现在没有的能力(出网/写某个路径)",
		Args: map[string]string{
			"axis": "net 出网 / write 写 / read 读 / proc 起进程",
			"scope": "针对什么。**这次要用到的全部目标一次写全**, 逗号分隔 —— " +
				"装 Python 包要 pypi.org,files.pythonhosted.org",
			"why": "为什么要它, 一句话。他就靠这句决定给不给",
		},
		ArgOrder: []string{"axis", "scope", "why"},
		// 申请能力本身不需要任何能力 —— 它就是"我没有能力"时唯一能做的事.
		// 给它设 Needs 会变成"要先有能力才能申请能力"的死循环.
		//
		// 这类请求等待人工决定, 因此**不设时限**: 用户可能稍后才看到.
		// 通用时限会在决定仍未返回时提前终止请求, 留下未完成的授权状态,
		// 而 agent 已经当它失败, 接着去瞎试别的.
		WaitsForHuman: true,
		Run:           requestAccess,
	}
}

var knownAxes = map[string]abi.CapAxis{
	"net":    abi.AxisNet,
	"write":  abi.AxisWrite,
	"read":   abi.AxisRead,
	"proc":   abi.AxisProc,
	"secret": abi.AxisSecret,
}

func requestAccess(t Toolbox, a map[string]any) (string, error) {
	if t.Sys == nil {
		return "", fmt.Errorf("这个环境里没有 OS, 申请不了能力")
	}
	axisStr := strings.ToLower(strings.TrimSpace(argStr(a, "axis")))
	axis, ok := knownAxes[axisStr]
	if !ok {
		names := make([]string, 0, len(knownAxes))
		for k := range knownAxes {
			names = append(names, k)
		}
		return "", fmt.Errorf("不认识的能力轴 %q。只有这几条: %s",
			axisStr, strings.Join(names, " "))
	}
	scope := strings.TrimSpace(argStr(a, "scope"))
	why := strings.TrimSpace(argStr(a, "why"))

	// 一次申请可以带多个目标 —— **一次操作只该打扰用户一次**.
	//
	// 安装一个 Python 包要两次审批(pypi.org 查包 + files.pythonhosted.org
	// 下载), 而这两次问的其实是同一件事: "允许它装 tabulate 吗".
	targets := splitTargets(scope)
	if len(targets) == 0 {
		return "", fmt.Errorf("scope 是空的。说清楚要连哪个主机/动哪个路径")
	}
	// 先问 OS 是不是已经有了 —— 白问一次就是白打扰用户一次.
	//
	// 已有权限的路径不应重复申请: 一次别的原因的
	// 失败(路径拼错)误判成权限问题了.
	var missing []string
	for _, tg := range targets {
		if allowed, err := t.Sys.Can(axis, tg); err == nil && allowed {
			continue
		}
		missing = append(missing, tg)
	}
	if len(missing) == 0 {
		return okResult("already_done", fmt.Sprintf(
			"你**已经有** %s:%s 这条能力了, 不用申请。刚才那次失败是别的原因 ——"+
				"回去看报错原文, 别再申请一遍", axisStr, scope)), nil
	}
	scope = strings.Join(missing, ",")

	res, err := t.Sys.Decide(abi.DecisionRequest{
		Urgency: "high",
		Axis:    axis,
		Scope:   scope,
		Present: abi.PresentSpec{
			Kind:   "choice",
			Title:  askTitle(axis, scope),
			Detail: why,
			Options: []abi.PresentOption{
				{ID: "yes", Label: "允许"},
				{ID: "no", Label: "拒绝", Destructive: true},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("申请没送到用户那儿: %v", err)
	}
	if res.Choice != "yes" {
		return okResult("denied", fmt.Sprintf(
			"用户**拒绝**了 %s:%s。别换个说法再申请一次 ——"+
				"想办法绕过这个需求, 或者告诉用户这件事因此做不成",
			axisStr, scope)), nil
	}
	// 批准了, 但这个进程用不上.
	//
	// 内核的约束在进程启动时就定死: landlock 只能收紧不能放宽,
	// netns 里的网卡也不会凭空长出来. 授权记在**对话**上,
	// 下一句话起的新进程会带着它.
	//
	// 这件事必须说清楚, 否则它会立刻重试, 失败, 然后告诉用户"你没给我权限".
	return okResult("granted", fmt.Sprintf(
		"用户批准了 %s:%s，授权已经记在这段对话上。\n"+
			"**但这一次还是做不成**：内核的约束在进程启动时就定死了，只能收紧不能放宽。\n"+
			"**不要重试，也不要说用户没给权限。** 现在就用 done 收尾，"+
			"告诉他授权已生效、下一句话就能接着做。",
		axisStr, scope)), nil
}

// splitTargets 一次申请里的多个目标.
//
// 跟 OS 侧的 splitScopes 必须**同一套切法** —— 两边不一致的话,
// 用户看到批了 3 个而实际只授了 1 个, 而且没有任何一处会说这件事.
func splitTargets(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';'
	}) {
		if f = strings.TrimSpace(f); f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
