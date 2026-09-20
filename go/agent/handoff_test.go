package agent

import (
	"strings"
	"testing"
)

// **转包到此为止**.
//
// roomContext.ts 里写着这套东西最怕的事: 把每个人的发言都投进别人的
// 收件箱, 等于每句话触发 N-1 次新回合, 而那些回合又各自触发下一轮 ——
// 一屋子 bot 会自己聊到预算烧光. handoff 正是在开这个口子, 所以只开一层.
func TestHandoffCannotBeChained(t *testing.T) {
	var passed []string
	tool := HandoffTool(func(who, task string) (string, error) {
		passed = append(passed, who)
		return "交给他了", nil
	})
	// 用户开的头 —— 派得出去
	if _, err := tool.Run(Toolbox{}, map[string]any{
		"who": "小登", "task": "把登录接口补上"}); err != nil {
		t.Fatalf("用户开的头也派不出去: %v", err)
	}
	// 同屋交办来的这一轮 —— 不许再往下转
	_, err := tool.Run(Toolbox{Relayed: true}, map[string]any{
		"who": "小记", "task": "把登录接口补上"})
	if err == nil {
		t.Fatal("被交办的人又转出去了 —— A 交 B、B 交 C、C 交回 A, 链子没有尽头")
	}
	if !strings.Contains(err.Error(), "你自己做完") {
		t.Errorf("没说清该怎么办, 它只会换个人再试一次: %v", err)
	}
	if len(passed) != 1 {
		t.Errorf("拦住了却还是投出去了: %v", passed)
	}
}

// 交代不清就别交 —— 他看不见你们的对话.
//
// 收到一句"帮我看看那个"只能反问, 而反问是问到用户脸上的:
// 等于你把活推给了他, 他把活推回给了用户.
func TestHandoffRefusesVagueTask(t *testing.T) {
	tool := HandoffTool(func(who, task string) (string, error) { return "ok", nil })
	for _, task := range []string{"", "看看", "改一下"} {
		if _, err := tool.Run(Toolbox{}, map[string]any{"who": "小登", "task": task}); err == nil {
			t.Errorf("交代 %q 也放过去了 —— 他没有上下文, 干不出你要的东西", task)
		}
	}
}

// **别许你做不到的诺**: 这条路上没有任何东西会把结果送回来.
func TestHandoffSaysYouWontHearBack(t *testing.T) {
	tool := HandoffTool(func(who, task string) (string, error) { return "已经交给他了", nil })
	out, err := tool.Run(Toolbox{}, map[string]any{"who": "小登", "task": "把登录接口补上"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "收不到他的回复") {
		t.Error("没说清收不到回复 —— 它会对用户说'我盯着他, 做完告诉你', 那是句空话")
	}
}

// 没有房间就不挂这个工具 —— 摆一个用不了的, 它会照着调、许下做不到的承诺.
func TestHandoffNotOfferedOutsideARoom(t *testing.T) {
	alone := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil, nil, nil, nil))
	if _, ok := alone.Get("handoff"); ok {
		t.Error("一个人待着也摆着交办工具, 它会调然后拿到一句'这儿没有别人'")
	}
	inRoom := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil, nil, nil,
		func(who, task string) (string, error) { return "", nil }))
	if _, ok := inRoom.Get("handoff"); !ok {
		t.Error("在房间里却没有交办工具")
	}
}

/**
 * **交代活不是写规格书**.
 *
 *	一次后端交代写了 902 个字: 端口读哪个环境变量、静态文件
 *	怎么托管、POST /api/login 解析失败返回什么状态码、成功那个 JSON 里
 *	有哪几个字段、演示账号叫什么…… 用户看了一眼就说"你这发的太详细了吧".
 *
 *	而那些细节是它猜的 —— 它自己一行后端都没写过, 却把状态码和 JSON
 *	形状全定死了. 接手的人照着做, 做出来的是"小丁想象中的后端".
 *
 *	况且项目里就有一份 CHARTER.md/API.md: 两个人都看得见、改了还能同步.
 *	抄进一句话里的那份, 从抄完那一刻就开始过期.
 */
func Test交代活不是写规格书(t *testing.T) {
	var got string
	tool := HandoffTool(func(who, task string) (string, error) {
		got = task
		return "交给「" + who + "」了", nil
	})

	// 这一份只长, 不带工具名 —— 长度那条要单独测得到
	spec := "做个最小登录页的后端。上面已有 CHARTER.md/PLAN.md。" +
		strings.Repeat("要求：用 Node 内置 http 模块，端口读 PORT 环境变量默认 3000，"+
			"POST /api/login 解析失败返回 400，演示账号 demo123/secret。", 6)
	_, err := tool.Run(Toolbox{}, map[string]any{"who": "小戊", "task": spec})
	if err == nil {
		t.Fatal("一份 900 字的规格书被当成交代活放过去了")
	}
	// 报错要说清**换个做法**, 不是只说"太长"
	for _, want := range []string{"归他", "要什么", "别碰哪儿"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("没说清该怎么办(缺「%s」): %v", want, err)
		}
	}
	if got != "" {
		t.Error("退回去了却还是发出去了")
	}

	// 人真会写的那句照旧要过 —— 别把这条修成"一律不许交办"
	fine := "后端你来，登录接口按 CHARTER.md 那份契约，别动 public/。"
	if _, err := tool.Run(Toolbox{}, map[string]any{"who": "小戊", "task": fine}); err != nil {
		t.Errorf("一句正常的交代被拦了: %v", err)
	}
	if got != fine {
		t.Errorf("没原样交出去: %q", got)
	}
	// 太短那条不许被冲掉 —— 两头都要有
	if _, err := tool.Run(Toolbox{}, map[string]any{"who": "小戊", "task": "看看"}); err == nil {
		t.Error("太短那条被冲掉了")
	}
}

/**
 * **别教同行怎么干活**.
 *
 *	用户看着两条交办说的话: "竟然还需要说契约 agent 是干啥的…还说这么
 *	详细…那还是团队协作吗, 我来指挥得了呗".
 *
 *	他说的是这些:
 *	  先 sync_down 拉到           —— 教同行用工具
 *	  完成后自己跑一遍再 merge_up   —— 叮嘱同行要自测、要交活
 *
 *	每个 bot 受的是同一套规矩, 这些它自己都会. 而这么写的代价不只是
 *	啰嗦: 接手的人被降成一双手, 他那份判断力白买了 —— 那时候确实
 *	"人自己指挥得了".
 */
func Test别教同行怎么干活(t *testing.T) {
	tool := HandoffTool(func(who, task string) (string, error) { return "交了", nil })

	for _, bossy := range []string{
		"做后端接口。先 sync_down 拉主干，做完 merge_up。",
		"前端你来，完成后 merge_up。",
	} {
		_, err := tool.Run(Toolbox{}, map[string]any{"who": "小戊", "task": bossy})
		if err == nil {
			t.Errorf("在教同行用工具, 却放过去了: %q", bossy)
			continue
		}
		// 报错要说清**为什么不用说**, 不是只说"不许说"
		if !strings.Contains(err.Error(), "同一套规矩") {
			t.Errorf("没说清他自己就会: %v", err)
		}
	}

	// 一句正经的交代照旧要过
	fine := "后端你来，登录接口。"
	if _, err := tool.Run(Toolbox{}, map[string]any{"who": "小戊", "task": fine}); err != nil {
		t.Errorf("一句正常的交代被拦了: %v", err)
	}
	// 指一份文件不算教他干活 —— 那是告诉他去哪儿看
	ok2 := "后端你来，登录接口按 CHARTER.md 那份契约。"
	if _, err := tool.Run(Toolbox{}, map[string]any{"who": "小戊", "task": ok2}); err != nil {
		t.Errorf("指一份文件被当成教他干活: %v", err)
	}
}
