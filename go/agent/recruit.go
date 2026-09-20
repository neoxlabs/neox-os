package agent

import (
	"fmt"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// recruit —— agent 自己拉人.
//
// ── 为什么这件事该交给它 ──
//
// 用户的原话: "我要你一个人够不够? 你要加速的话, 能不能多拉几个人?
// 这样的能力都是要有的, 你不能让我自己去拉吧。"
//
// 没有这个工具时, 系统只能明确返回:
//
//	"我没有拉起其他 agent / 开新对话实例的能力 —— 工具清单里根本没有
//	 这个东西。'拉人'这个动作只能你来"
//
// 谁该拆活、拆成几块、每块谁干 —— 这是**正在干这摊活的那个人**最清楚的事,
// 让用户去猜是把判断推给了信息最少的一方.
//
// ── 为什么必须走审批 ──
//
// **每个 bot 都是钱**. 一个能自己拉人的 bot, 如果不需要人工批准, 就能在
// 一轮里把账单拉爆 —— 而且拉出来的人还会接着拉人.
//
// 这跟出网走审批是同一条规矩(缺省不安全). 而审批这条链已经全在了:
// 决策可以活得比进程长、支持跨设备批准, 且请求方不可用时会判过期.
//
// ── 为什么不给"解雇" ──
//
// 能加就得能减是对的, 但**减人不该由它自己做**: 它可以判断"这活干完了",
// 却判断不了"这个人以后还用不用得上". 删对话的入口本来就在界面上,
// 那是用户的决定.

// RecruitTool 拉人. hire 为 nil 时不挂这个工具 —— 见 DefaultToolsWith:
// 不承诺做不到的事.
func RecruitTool(hire func(name, role string) (string, error)) Tool {
	return Tool{
		Name: "recruit",
		// 声明只说"是什么", **判断和细节留给工具的错误文本** ——
		// 那份在出错那一刻才付, 而声明是每一轮都付
		Desc: "拉个人一起干",
		Args: map[string]string{
			"name": "叫他什么。两三个字",
			"role": "他负责什么。这句会变成他的岗位说明",
			"why":  "为什么要多一个人",
		},
		ArgOrder: []string{"name", "role", "why"},
		Mutates:  true,
		// 等待人工批准时, **一律不设时限** —— 时间闸限制的是"机器不响应", 不是"人还没回答"
		WaitsForHuman: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			return runRecruit(t, a, hire)
		},
	}
}

func runRecruit(t Toolbox, a map[string]any, hire func(name, role string) (string, error)) (string, error) {
	if t.Sys == nil {
		return "", fmt.Errorf("这个环境里没有 OS, 拉不了人")
	}
	name := strings.TrimSpace(argStr(a, "name"))
	role := strings.TrimSpace(argStr(a, "role"))
	why := strings.TrimSpace(argStr(a, "why"))
	if name == "" {
		return "", fmt.Errorf("得给他起个名字 —— 用户在侧栏里就是按名字认人的")
	}
	if len([]rune(name)) > 12 {
		return "", fmt.Errorf("名字太长了(%q)。侧栏一行就那么宽, 起个两三个字的", name)
	}
	// **说不清他干什么就别拉**: 一个没有岗位说明的 bot 上线之后只会问
	// "要我干什么" —— 那等于把活又推回给用户, 而这个工具存在的理由
	// 恰恰是不推回去
	if role == "" {
		return "", fmt.Errorf("没说清他负责什么。拉一个不知道自己干什么的人来, 他上线只会问你要我干什么")
	}
	if why == "" {
		return "", fmt.Errorf("没说为什么要多一个人 —— 用户就靠这句话决定给不给")
	}

	/**
	 * 用户开了"拉人不用问"就直接拉 —— 那是他的钱包他做主.
	 *
	 *	但**免批要说在结果里**: 用户翻记录时要看得出这个人是按哪档
	 *	进来的, 不是谁偷偷放进来的.
	 */
	if t.AutoHire != nil && t.AutoHire() {
		if hire == nil {
			return "", fmt.Errorf("这台机器起不了新 bot")
		}
		detail, herr := hire(name, role)
		if herr != nil {
			return "", fmt.Errorf("人没起来: %v", herr)
		}
		return okResult("success", "(按你在权限页开的档, 拉人免批)\n"+detail), nil
	}

	res, err := t.Sys.Decide(abi.DecisionRequest{
		Urgency: "high",
		// 拉人就是起进程, 所以记在 proc 轴上.
		//
		// **这条授权是留痕, 不是闸**: 真正起进程的是宿主, 它不查这条能力.
		// 记它的理由跟别的审批一样 —— 事后翻账本要能答出"这个人是谁批的".
		Axis:  abi.AxisProc,
		Scope: "bot:" + name,
		Present: abi.PresentSpec{
			Kind:   "choice",
			Title:  fmt.Sprintf("拉「%s」进来一起干吗?", name),
			Detail: role + "\n\n为什么要多一个人: " + why,
			Options: []abi.PresentOption{
				{ID: "yes", Label: "拉进来"},
				{ID: "no", Label: "不用", Destructive: true},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("申请没送到用户那儿: %v", err)
	}
	if res.Choice != "yes" {
		// **别换个说法再申请一次**: 同一条规矩在 request_access 那边也写着
		return okResult("denied", fmt.Sprintf(
			"用户**不同意**拉「%s」。别换个名字再申请一次 —— 自己把这摊活干完, "+
				"或者告诉用户人手不够、哪一块因此会慢", name)), nil
	}
	if hire == nil {
		return "", fmt.Errorf("这台机器起不了新 bot")
	}
	detail, herr := hire(name, role)
	if herr != nil {
		return "", fmt.Errorf("用户批了, 但人没起来: %v", herr)
	}
	return okResult("success", detail), nil
}
