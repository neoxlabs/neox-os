package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 常驻主动进程 —— 感知层的判断者.
//
// ── 它跟对话进程是两回事 ──
//
//	对话进程   有人在等它说话. 沉默是失败
//	主动进程   **没有人在等**. 它说的每一句都是打扰, 沉默是常态
//
// 这个区别决定了几乎所有设计:
//
//	摘要不能再插进当前对话. 那是在拿"你正在聊的事"的上下文去装
//	"外面发生的事" —— 两者互相污染, 而且你每聊一句都在为感知付钱
//
//	它必须能"什么都不做", 而且那要是**最省事的一条路**.
//	让沉默需要额外表态的话, 模型会倾向于说点什么 —— 它被训练成有用的
//
// ── 沉默是默认的, 说话必须显式调工具 ──
//
// 这是这一层唯一的结构性设计: 它只有一个工具 notify_user.
// 不调 = 什么都不发生, 用户永远不知道这一轮存在过.
//
// 反过来做(默认把它的话转给用户, 让它主动声明"这次不说")会失败,
// 原因不在模型笨: **一个被训练成有帮助的模型, 在"说点什么"和
// "明确宣布我不说"之间, 一定倾向前者**. 结构上让沉默免费, 比在
// 提示词里求它闭嘴可靠得多.

// NotifyTool 主动进程唯一的工具: 把一件事捅到用户面前.
//
// 它不写文件、不跑命令 —— 主动进程的职责是**判断**, 不是干活.
// 真要干活是下一步的事(而且那时候该起一个干活的进程, 不是让判断者兼职).
func NotifyTool(notify func(text, why string)) Tool {
	return Tool{
		Name: "notify_user",
		Desc: "把一件事主动告诉用户。**这是打扰**，不确定就别调",
		Args: map[string]string{
			"text": "告诉他什么。一两句话，说清是什么事、他要不要做点什么",
			"test": "这条过了哪一条判据。**只能三选一**: " +
				"irreversible(错过了无法挽回) / actionable(他现在能做点什么) / " +
				"informed(你动了他的东西)",
			"why": "为什么这件事值得打扰他，一句话",
		},
		ArgOrder: []string{"text", "test", "why"},
		// 不碰任何受管资源 —— 它只是把话递出去
		Mutates: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			text := strings.TrimSpace(argStr(a, "text"))
			if text == "" {
				return "", fmt.Errorf("text 是空的。真要说就说清楚, 不说就别调这个工具")
			}
			// ── 必填的三选一让通知必须有打扰理由 ──
			//
			// 仅靠提示词"不需要为'这次我不说'做任何解释"仍可能让模型
			// 调 notify_user 发送这样的内容:
			//
			//	（不打扰。刚通知过降压药超时未服, 第 2 次是同一件事的重复…）
			//
			// **它花掉一次打扰额度, 去告诉用户自己不打扰.**
			//
			// 措辞管不住就改结构: 逼它三选一之后, "我不打扰"这句话
			// **在参数上就填不出来** —— 一件不打扰的事没有判据可选.
			// 这跟"沉默必须免费"是同一个手法: 让错的那条路走不通,
			// 而不是在提示词里求它别走.
			test := strings.ToLower(strings.TrimSpace(argStr(a, "test")))
			switch test {
			case "irreversible", "actionable", "informed":
			default:
				return "", fmt.Errorf(
					"test 必须三选一: irreversible / actionable / informed。"+
						"你给的是 %q。**选不出来就说明这件事不该打扰他** —— "+
						"那就什么都不要调, 直接收工", argStr(a, "test"))
			}
			if notify != nil {
				notify(text, test+": "+argStr(a, "why"))
			}
			return okResult("success", "已经告诉用户了。这一轮到此为止, 不要再说别的"), nil
		},
	}
}

// ── 主动进程的系统提示词 ────────────────────────────────────
//
// 刻意很短. 它只做一个判断, 层数多了反而稀释 ——
// 而这个判断本身已经足够难.

const layerProactiveIdentity = `你是 Neox 的**感知判断进程**。你不跟用户对话，也不干活。

你的唯一职责：看一眼刚发生的事，判断**值不值得打扰他**。

**没有人在等你说话。** 这一点跟你平时不一样——你说的每一句都是主动打扰，而不是回答。所以：

- **默认什么都不做。** 绝大多数时候，正确的动作是什么都不调、直接收工。
- 要说就调 notify_user。不调就是不说，用户永远不会知道这一轮存在过——**这是好事，不是失职**。
- 你**不需要**为"这次我不说"做任何解释或声明。沉默不用表态。`

const layerProactiveJudgment = `## 值不值得打扰：按顺序问四条

**⓪ 他已经知道了吗？** 他自己刚做的那件事，不用告诉他。
   他开的门、他关的灯、他自己出的门 —— 说了不是提醒，是**复读**。
   而复读几次之后，他就不再看这个通道了。
   环境事实里写着他在不在家、今天你跟他说过什么 —— 先拿它对一下。

**① 错过了能不能补？** 有截止时间、且过了就无法挽回 → 说。
   火车快开了、药该吃了、门锁半夜开着 —— 这类晚了就没用了。
   而"疫苗过期三天了"晚说几小时没有任何区别 → 攒着。

**② 他现在能做什么动作吗？** 他此刻做不了任何事的信息，现在说就是噪音。
   凌晨三点告诉他快递到了，他能干什么？

**③ 你动过他的东西吗？** 改了他的文件、替他做了决定 → **无条件说**，
   这是知情权，不受前两条限制。

四条都不满足 → **什么都不做**。他要是想知道，会自己问。

## 几种常见的、不该说的

- 例行的状态变化：灯开了关了、位置从公司到家、电量掉了一档
- 你已经说过的事再次发生（除非这次不一样）
- 标着"补传的历史"的摘要：**那些已经发生过了**，他此刻做不了任何事。
  它们是用来回答"我今天去过哪儿"的，不是用来主动汇报的。
- 你不确定的事。不确定就是不够格 —— 打扰错一次的代价，比漏说一次高得多。

## notify_user 不是用来汇报你的判断的

它只有一个用途：**把一件事告诉用户**。调它就要填 test（三条判据三选一）——
**选不出来，就说明这件事不该打扰他，那就什么都不要调，直接收工。**

绝不要用它来说"我这次不打扰""这条我攒着""重复了所以不报"——
那些话没有人在等，而且会白白花掉一次打扰额度。

## 说的时候

一两句话说完：什么事、他要不要做点什么。不要复述摘要，不要解释你的推理过程。`

// BuildProactivePrompt 主动进程的系统段.
//
// 跟对话进程共用工具表的生成方式, 但**层不共用** ——
// 对话进程那套(自主执行、动手闭环、完成条件)对它全是噪音:
// 它不动手, 也没有"完成"这回事.
func BuildProactivePrompt(ts *ToolSet) string {
	return strings.Join([]string{
		layerProactiveIdentity,
		layerProactiveJudgment,
		"## 工具\n\n" + ts.Describe(),
		"要说就直接调用 notify_user。不说就直接收工，不用解释。",
	}, "\n\n")
}

// NewProactiveLLM 造一个用主动提示词的模型.
//
// ── 为什么是构造函数, 不是一个公开字段 ──
//
// 头一版我写的是 `type ProactiveModel struct{ *LLM }` 加一个影子方法.
// **那是错的, 而且是静默错的**: Go 没有虚派发, LLM.once() 调的是
// 具体类型上的 systemPrompt, 影子方法永远不会被调到 ——
// 编译通过、测试(如果只测 BuildProactivePrompt)也绿, 而真跑起来
// 主动进程用的是对话进程那套两万字的提示词.
//
// 换成写**私有字段**的构造函数之后, 既生效, 又不存在一个
// "可以在对话进程上拨错"的公开旋钮. 提示词的选择由**这个进程是什么**
// 决定, 那不该是运行时可调的.
func NewProactiveLLM(m *LLM) *LLM {
	m.prompt = BuildProactivePrompt(m.Tools)
	return m
}

// ProactiveCaps 主动进程的能力集.
//
// **一条都不给.** 它不读文件、不写文件、不出网、不起进程 ——
// 它只是看一眼摘要然后决定说不说.
//
// 这不是吝啬: 一个常驻的、被外部信号唤醒的进程, 如果还能读写文件,
// 那么任何能往采集口投信号的东西都获得了一条间接的执行路径.
// 感知层收的是位置和来电, 它的攻击面本来就是全系统最大的.
func ProactiveCaps() []abi.Capability { return nil }

// RemindTool 让 agent 设一个闹钟.
//
// ── 为什么走 Emit 而不是新加一个系统调用 ──
//
// 这个工具跑在被约束的进程里, 够不着 OS 的对象. 两条路:
//
//	加一个 ABI 系统调用   要动 wire/server/client 三处, 而且多一个
//	                     "只有一个调用方"的接口面
//	发一个事件让 OS 接    跟 notify_user 完全一样的形状, 而且
//	                     **事件日志本来就是闹钟的持久化载体**
//
// 选后者. 顺带解决了持久化: 闹钟落进日志的那一刻就已经活得比进程长了.
//
// 代价说清楚: 事件是单向的, 工具拿不到 OS 分配的闹钟号. 所以工具结果
// 只能说"记下了", 不能说"这是第 3 号". 对这件事够用 —— 用户要撤销的话
// 说人话就行, 不用记号码.
func RemindTool(remind func(atMs int64, text string) error,
	daily func(atMs int64, what string) error) Tool {
	return Tool{
		Name: "remind_me",
		Desc: "设提醒。活得比这段对话长——进程被杀、机器重启都还在",
		Args: map[string]string{
			"at":    "毫秒时间戳，必须是将来。每天固定时刻的话用 daily",
			"daily": "每天几点，写 06:40。给了它就不用给 at",
			"text": "到点说的原话。给 daily 时它是**给你自己的题目**" +
				"（到点会把你叫醒判一次，没人等你回话）",
		},
		ArgOrder: []string{"at", "daily", "text"},
		Optional: map[string]bool{"at": true, "daily": true},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			at := int64(argInt(a, "at"))
			text := strings.TrimSpace(argStr(a, "text"))
			nowMs := time.Now().UnixMilli()
			if text == "" {
				return "", fmt.Errorf("text 是空的 —— 到点了说什么?")
			}
			// ── 每天几点 ──
			//
			//	原来这是另一个工具(check_every), 而且是**轮询**: 它为了盯住
			//	7:05–8:00 那一小时设了每 10 分钟醒一次, 一天 144 次空转.
			//	按时刻一天一次, 醒来就是该干活的时候.
			if hhmm := strings.TrimSpace(argStr(a, "daily")); hhmm != "" {
				when, err := nextDaily(hhmm, time.Now())
				if err != nil {
					return "", err
				}
				if daily == nil {
					return "", fmt.Errorf("这个环境里设不了每天的定时")
				}
				if err := daily(when.UnixMilli(), text); err != nil {
					return "", err
				}
				return okResult("success", fmt.Sprintf(
					"记下了：每天 %s 看一眼「%s」。第一次是 %s。到点会把你叫醒，"+
						"那时候没有人在等你回话。",
					when.Format("15:04"), text, when.Format("01-02 15:04"))), nil
			}
			// ── 时间必须在这一侧就验, 不能等 OS 那边拒 ──
			//
			// **本地校验防止把无效提醒报告为成功, 让用户依赖不存在的提醒.**
			//
			// 例如"今天下午三点半要去医院"需要将来的提醒, 但缺少当前时间时,
			// 模型可能算出七个月前的时间戳. 若 OS 拒绝只在终端显示,
			// 而**工具当场返回 success**(单向 Emit 不回传拒绝),
			// agent 就会错误承诺"提醒设好了: 今天下午 2:45".
			//
			// 用户就此以为有人替他记着这件事了.
			//
			// 单向通道这个设计本身没错, 错在**把能在本地验的东西留给了对端**.
			// 进程自己有时钟, 那就在这儿验.
			if at <= 0 {
				return "", remindTimeErr(nowMs, "at 要是毫秒时间戳")
			}
			if at <= nowMs {
				return "", remindTimeErr(nowMs, fmt.Sprintf(
					"你给的 %s 已经过去了", time.UnixMilli(at).Format("01-02 15:04")))
			}
			// 一年以后 = 多半是单位或年份算错了. 判错的代价不对称:
			// 拦下来最多让它重算一次, 放过去是一年后突然冒出一句莫名其妙的提醒
			if at > nowMs+365*24*3600*1000 {
				return "", remindTimeErr(nowMs, fmt.Sprintf(
					"你给的 %s 在一年以后, 多半是单位或年份算错了",
					time.UnixMilli(at).Format("2006-01-02 15:04")))
			}
			if remind == nil {
				return "", fmt.Errorf("这个环境里没有闹钟服务")
			}
			if err := remind(at, text); err != nil {
				return "", err
			}
			// **提前量是算出来的, 不是固定的** —— 这条要在结果里点一下,
			// 因为模型很容易把"提前一小时"当成规则.
			// 12 点的火车提前 1 小时, 是因为"路上 40 分钟 + 缓冲"倒推的.
			// **把解析出来的时间回读给它, 而且带上"多久以后".**
			//
			// 它算错年份时, "01-21 08:21"这种绝对时间自己看不出问题,
			// 但"还有 158 天"一眼就不对.
			// 两条自查分别防止日期错误和出发过晚:
			//
			//	时间对不对   算错年份时绝对时间看不出来, "多久以后"一眼看得出
			//	提前量够不够 "三点半要到医院, 打车四十分钟"不能把闹钟设在
			//	             15:30 并提醒
			//	             "该出发了" —— 到点时人应该已经在医院了.
			//	             提醒的时刻要从"要到的时刻"倒推路上的时间.
			return okResult("success", fmt.Sprintf(
				"记下了: %s(%s后) 提醒「%s」。这个闹钟活得比这段对话长。\n"+
					"**回头自查两件事**: ① 这个时间对不对 —— 如果'%s后'跟你想的"+
					"不一样, 说明算错了; ② **提前量够不够** —— 如果用户说的是"+
					"'几点要到', 而路上要花时间, 那么提醒的时刻应该是"+
					"'要到的时刻减去路上的时间', 不是那个时刻本身。不对就重设一个。",
				time.UnixMilli(at).Format("01-02 15:04"),
				humanGap(at-nowMs), text, humanGap(at-nowMs))), nil
		},
	}
}

// remindTimeErr 时间不对时的报错.
//
// **必须把"现在是几点"一起给出去**, 而且要给毫秒数 —— 模型不知道现在几点
// (提示词里刻意没有当前时间, 那会让前缀每分钟作废), 只告诉它"你算错了"
// 它只能再猜一次.
func remindTimeErr(nowMs int64, why string) error {
	return fmt.Errorf("%s。现在是 %s, 毫秒时间戳 %d —— 照这个重算一个将来的时刻",
		why, time.UnixMilli(nowMs).Format("2006-01-02 15:04:05"), nowMs)
}

// humanGap "多久以后" —— 绝对时间它看不出错, 相对时间一眼就看得出
func humanGap(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}

// WatchTool 让用户能说"以后我一到家你就跟我说一声".
//
// ── 为什么必须有它 ──
//
// 仅回复"这个链路是通的""我可以现在接上"不能建立长期关注.
// 主动进程只按三条打扰判据判断, "你到家了"几乎一条都过不了;
// 下一段对话又是全新进程, 因此必须把关注登记为活得比对话长的状态.
//
// 否则会像未登记的闹钟一样, 对用户许下没有执行依据的长期承诺.
//
// **两个条件至少给一个**: 都不给等于"什么都关注", 那会把每条信号
// 都推给用户 —— 而且是静默的灾难: 他以为设了个贴心的提醒,
// 拿到的是一天几十条通知, 然后把整个通知关掉.
func WatchTool(watch func(kind, place, say, raw string) error) Tool {
	return Tool{
		Name: "watch_for",
		Desc: "记下用户长期关注的事(到家/开门/来电…)，以后发生就直接告诉他。**活得比这段对话长**",
		Args: map[string]string{
			"kind": "关注哪种事: place.arrived 到达 / place.left 离开 / " +
				"door.opened 开门 / lock.opened 开锁 / call.incoming 来电 / " +
				"presence.motion 有人活动。不限就留空",
			"place": "关注哪个地点(家/公司…，要是已经命名过的)。不限就留空",
			"raw":   "用户的原话，一字不改。他以后问'我让你盯什么了'要照这个答",
		},
		ArgOrder: []string{"kind", "place", "raw"},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			kind := strings.TrimSpace(argStr(a, "kind"))
			place := strings.TrimSpace(argStr(a, "place"))
			raw := strings.TrimSpace(argStr(a, "raw"))
			if t.Heard != nil && raw != "" && !quotedFrom(raw, t.Heard()) {
				return "", notSaid("raw", raw)
			}
			if kind == "" && place == "" {
				return "", fmt.Errorf(
					"kind 和 place 至少要给一个。两个都不给等于'什么都关注', " +
						"用户会被每条信号打扰一次 —— 先问清楚他到底想盯什么")
			}
			if watch == nil {
				return "", fmt.Errorf("这个环境里没有感知层, 盯不了")
			}
			if err := watch(kind, place, "", raw); err != nil {
				return "", err
			}
			what := kind
			if place != "" {
				what = strings.TrimSpace(kind + " @" + place)
			}
			return okResult("success", fmt.Sprintf(
				"记下了, 以后 %s 发生就直接告诉他。这条活得比这段对话长。\n"+
					"**跟他说清楚这依赖采集端**: 手机/家居没接上或者没电的时候, "+
					"这件事发生了也不会有人知道 —— 别让他以为这是万无一失的。", what)), nil
		},
	}
}

// CancelTool 撤掉一条提醒或一条关注.
//
// ── 为什么它非有不可 ──
//
// "别盯着我到家了, 那个吃药的提醒也取消吧"需要真正撤销持久状态.
// 没有撤销工具时, agent 只能答"没有删除的工具", 不能用口头确认代替删除.
//
// **如实说明能力边界仍不够**: 一个只能加不能减的系统, 用起来一次就够
// 让人不敢再用: 说错一句话就永久多一条通知.
//
// ── 为什么参数是 id 而不是描述 ──
//
// 让它按"那个到家的提醒"去模糊匹配, 猜错一次是**静默的**:
// 用户以为取消了吃药提醒, 实际取消的是"到家告诉我", 而他要到
// 下次该吃药时才发现.
//
// 所以要 id, 而 id 从 .neox/state.txt 念 —— 那个文件里每条前面都有.
func CancelTool(cancel func(id string) (string, error), list func() string) Tool {
	return Tool{
		// ── 不给 id 就是"先看看有什么" ──
		//
		//	只提供撤销、让模型"先读 .neox/state.txt", 会把查编号变成
		//	`run grep`。查编号的开销样本是 8 次 run, 每次一个完整往返,
		//	输出还带着原始 JSON, 因此直接提供列表入口。
		//
		//	"能立就要能撤"还依赖中间这一步:
		//	**能撤的前提是看得见**。
		Name: "cancel",
		Desc: "看有哪些提醒/关注/规则，或者撤掉一条。**不给 id 就是列出来**",
		Args: map[string]string{
			"id": "要撤的那条，形如 w1 / g2 / r1。照列出来的那个念，别猜",
		},
		ArgOrder: []string{"id"},
		Optional: map[string]bool{"id": true},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			id := strings.TrimSpace(argStr(a, "id"))
			if id == "" && list != nil {
				return list(), nil
			}
			if id == "" {
				return "", fmt.Errorf("id 是空的")
			}
			if cancel == nil {
				return "", fmt.Errorf("这个环境里没有感知层")
			}
			detail, err := cancel(id)
			if err != nil {
				return "", err
			}
			return okResult("success", detail), nil
		},
	}
}

// ── "到点说一句话" 和 "到点起一次判断" 是两件事 ──
//
//	remind_me(at)     到点**说一句现成的话**. "12 点的火车" —— 这件事
//	                  没有任何需要判断的地方, 中间过一次模型只多一次
//	                  跑偏的机会.
//
//	remind_me(daily)  到点**起一次判断**. "今天几点出门"取决于今天的
//	                  日程、此刻的路况、他在不在家 —— 到点时根本没有
//	                  一句现成的话可说, 只有一个要现算的东西.
//
//	**同一个工具的两个参数, 不是两个工具**: 这个差别对用户不可见
//	(他说的都是"以后提醒我"), 而摆两个工具的后果是模型每一轮都要挑
//	一次. 四个相近工具可能让同一件事被建成三条重复巡检.
//
//	周期落在闹钟里, 于是它跟一次性闹钟共用同一份持久化、同一条补响
//	规则、同一个 Tick —— 机器重启之后照样按点走.

// nextDaily "06:40" → 下一个 06:40 是什么时候(本地时间).
//
//	**只收 HH:MM**: "6" 是六点还是六分钟后, 模型和人会各理解一半.
//	而这条设定活得比对话长, 理解错了要等到第二天才发现.
func nextDaily(hhmm string, now time.Time) (time.Time, error) {
	hhmm = strings.TrimSpace(hhmm)
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil ||
		h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf(
			"看不懂时刻 %q —— 写 06:40 这种 24 小时制", hhmm)
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at, nil
}

// WhenTool 记一条条件触发 —— "当…的时候告诉我".
//
// ── 跟 remind_me / watch_for 的分界 ──
//
//	remind_me   到点说. 唯一的条件是时间.
//	watch_for   某类事发生就说. 条件是"发生了什么".
//	when        某个**状态成立**就说. 条件是"现在是什么样".
//
//	第三种原来没有. 于是"明天下雨就提醒我带伞"只能被硬塞成前两种之一,
//	而两种都表达不了它 —— 模型的实际做法是**许一个做不到的诺**:
//	它会回一句"好的, 下雨我就提醒你", 然后什么都不做.
//
// ── 为什么参数长这样 ──
//
//	fact/field/op/value 四件套看着笨, 但它换来一件要紧的事:
//	**条件是可求值的数据, 不是一句话**. 让模型写自然语言条件的话,
//	求值就得再过一次模型 —— 一天几十次, 而且每次的判断都可能不一样.
func WhenTool(add func(fact, field, op string, value any,
	from, to, say, why, raw string, urgent bool) (string, error),
	watch func(kind, place, say, raw string) error) Tool {
	return Tool{
		Name: "notify_when",
		Desc: "某件事发生/某个状态成立就告诉他(下雨带伞、到家开灯、来电)。" +
			"只在刚成立那一刻说，一天最多一次，活得比这段对话长。" +
			"**二选一**：event(+place) 或 fact/op/value",
		Args: map[string]string{
			"event": "place.arrived / place.left / door.opened / lock.opened / " +
				"call.incoming / presence.motion",
			"place": "哪个地点(已起过名的)。跟 event 至少给一个",
			"fact":  "哪条事实(weather/place/battery…)，先 where 看有哪些",
			"field": "哪个字段(rainProb…)，比整句就留空",
			"op":    "> < >= <= == != contains",
			"value": "跟什么比",
			"from":  "几点之后才判, HH:MM",
			"to":    "几点之前才判, HH:MM",
			"say":   "成立时对他说的原话",
			"why":   "凭哪条判据——他看不见理由就会把整个通道关掉",
			"raw":   "他的原话, 一字不改",
		},
		ArgOrder: []string{"event", "place", "fact", "field", "op", "value",
			"from", "to", "say", "why", "raw"},
		Optional: map[string]bool{"event": true, "place": true, "fact": true,
			"field": true, "op": true, "value": true, "from": true, "to": true,
			"say": true},
		Mutates: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			fact := strings.TrimSpace(argStr(a, "fact"))
			op := strings.TrimSpace(argStr(a, "op"))
			say := strings.TrimSpace(argStr(a, "say"))
			why := strings.TrimSpace(argStr(a, "why"))
			raw := strings.TrimSpace(argStr(a, "raw"))
			if t.Heard != nil && !quotedFrom(raw, t.Heard()) {
				return "", notSaid("raw", raw)
			}
			// ── 盯事件那一半 ──
			//
			//	原来这是另一个工具(watch_for). 两个工具答的是同一句话
			//	"以后…就告诉我", 而模型每次都要在两个里挑 —— 挑错不报错,
			//	只是那条关注永远不触发
			event := strings.TrimSpace(argStr(a, "event"))
			place := strings.TrimSpace(argStr(a, "place"))
			if event != "" || place != "" {
				if watch == nil {
					return "", fmt.Errorf("这个环境里没有感知层, 盯不了")
				}
				if err := watch(event, place, say, raw); err != nil {
					return "", err
				}
				what := event
				if place != "" {
					what = strings.TrimSpace(event + " @" + place)
				}
				return okResult("success", "盯上了: "+what+
					"。发生了会直接告诉他, 这条活得比这段对话长。"), nil
			}
			if fact == "" || op == "" {
				return "", fmt.Errorf("要么给 event/place 盯一件事, " +
					"要么给 fact/op/value 盯一个状态 —— 先用 where 看看现在有哪些事实")
			}
			if say == "" {
				return "", fmt.Errorf("say 是空的 —— 成立的时候对用户说什么?")
			}
			// **判据在这一侧就要, 不能等 OS 拒**: 单向通道不回传拒绝时
			// (见 RemindTool), 工具的成功结果会让 agent 承诺"设好了",
			// 而规则根本不存在. 本地可验证的必填项应先验证.
			if why == "" {
				return "", fmt.Errorf(
					"why 是空的。一次打扰值不值得, 用户只有看见它凭什么说才判得出来; " +
						"判不出来的话他唯一能做的就是把整个通道关掉")
			}
			return add(fact, strings.TrimSpace(argStr(a, "field")), op, a["value"],
				strings.TrimSpace(argStr(a, "from")), strings.TrimSpace(argStr(a, "to")),
				say, why, raw, false)
		},
	}
}

// RememberTool 让他说一句"记住…", 它下次开机还知道.
//
// ── 为什么这个工具跟 name_place 不是一类 ──
//
//	name_place / when / watch_for 记下来的东西都**参与判断**: 地点要
//	按距离反查, 规则要按条件触发. 它们各有各的结构, 各有各的索引.
//
//	而"我老婆生日 3 月 2 号""我不喝咖啡""车牌尾号 517"这类事一个结构
//	都没有, 也不触发任何东西 —— 它们唯一的用处是**下次别再问一遍**.
//
//	在这个工具之前, 这类话它一件都记不住, 而且**不会说自己记不住**:
//	当轮的对话里它当然"记得", 于是用户以为记住了, 直到进程重启.
//	那是最坏的一种忘 —— 没有任何一处报错.
//
// ── 键是用户自己的说法 ──
//
//	"老婆生日", 不是 "spouse.birthday". 同一个键再记一次是改, 不是
//	多一条 —— 不然"我不喝咖啡"说三遍会攒出三条一样的话.
func RememberTool(remember func(key, text string) (string, error)) Tool {
	return Tool{
		Name: "remember",
		Desc: "长期记住一件事(生日/口味/车牌/家里的规矩)，换对话、重启都还在。" +
			"**只记他明确交代的**，别把你的推测记进去",
		Args: map[string]string{
			"key":  "叫什么。用他的说法('老婆生日')，别翻译或改写",
			"text": "记住的内容。一句话",
		},
		ArgOrder: []string{"key", "text"},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			key := strings.TrimSpace(argStr(a, "key"))
			text := strings.TrimSpace(argStr(a, "text"))
			if key == "" || text == "" {
				return "", fmt.Errorf("key 和 text 都得给")
			}
			if remember == nil {
				return "", fmt.Errorf("这个环境里没有长期记忆")
			}
			detail, err := remember(key, text)
			if err != nil {
				return "", err
			}
			return okResult("success", detail), nil
		},
	}
}

// ForgetThatTool 忘掉一条.
//
//	**能记就得能忘**: 一条记错的偏好会一直影响它的回话, 而用户唯一的
//	处置办法本来是让它把整件事再说一遍 —— 而那只会多一条, 不会少一条.
func ForgetThatTool(forget func(key string) (string, error)) Tool {
	return Tool{
		Name: "forget_that",
		Desc: "忘掉一条长期记忆。按它的键忘",
		Args: map[string]string{
			"key": "哪一条。照记忆里那个键念, 别猜",
		},
		ArgOrder: []string{"key"},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			key := strings.TrimSpace(argStr(a, "key"))
			if key == "" {
				return "", fmt.Errorf("没说忘哪一条")
			}
			if forget == nil {
				return "", fmt.Errorf("这个环境里没有长期记忆")
			}
			detail, err := forget(key)
			if err != nil {
				return "", err
			}
			return okResult("success", detail), nil
		},
	}
}

// SetTimezoneTool 换这台机器的时区.
//
// ── 为什么给它这个权利 ──
//
//	时区错了的症状是**全错但都不报错**: 日报在凌晨发、"晚上七点提醒我"
//	落在下午、"明天早上八点"差几个小时. 用户看到的是"它时间乱了",
//	而他能说的话就是"我在中国" —— 那句话必须当场能办成一件事,
//	否则他只能去翻设置页, 而他手里多半只有一个手机.
//
//	绝大多数时候它用不上: 手机每次报到都带着自己的时区, OS 自己就采纳了
//	(见 osinit/zone.go 的 Adopt). 这个工具是给"手机还没连上来"和
//	"我要按纽约时间过日子"这两种场合留的.
//
// ── 为什么要写清后果 ──
//
//	它是**机器级**的设定, 不是这一段对话的: 改一次, 屋里另一个人的
//	闹钟也跟着挪. 所以只在用户明说的时候动它 —— 从"他好像在国外"
//	这种推测出发去改, 错了没人会发现.
func SetTimezoneTool(set func(zone string) (string, error)) Tool {
	return Tool{
		Name: "set_timezone",
		Desc: "换这台机器的时区。**只在他明说的时候用** —— 机器级设定, " +
			"屋里其他人也跟着变，别从推测出发改",
		Args: map[string]string{
			"zone": "IANA 时区名, 比如 Asia/Shanghai / America/New_York。别写 CST/UTC+8 这种",
		},
		ArgOrder: []string{"zone"},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			z := strings.TrimSpace(argStr(a, "zone"))
			if z == "" {
				return "", fmt.Errorf("没说换哪个时区")
			}
			if set == nil {
				return "", fmt.Errorf("这个环境里改不了时区")
			}
			detail, err := set(z)
			if err != nil {
				return "", err
			}
			return okResult("success", detail), nil
		},
	}
}

// WhereTool 现在什么情况 —— **把六个位置工具收成一个**.
//
// ── 为什么收 ──
//
//	同类能力若拆成六个工具: what_now / how_long_to / my_routine /
//	show_map / name_place / remember_place, 加起来 1245 字节的工具声明,
//	而它们答的是同一类问题("我在哪、那儿多远、我平时都在哪").
//
//	工具多了不是"能力强", 是**每一轮都要在六个里挑一个**. 账本里
//	"我现在在哪"的冗余调用案例是先调 what_now、再 run 两次、再 fetch
//	两次, 而答案第一次就已返回.
//
//	一个工具三个用法, 靠一个可选参数分：
//
//	  where()          现在什么情况: 位置、家里有没有人、天气、下一个提醒
//	  where(to=公司)   到那儿开车要多久(带当下路况)
//	  where(habits=1)  他平时都在哪几个地方
//
// ── 为什么不连 show_map 一起收 ──
//
//	那个是**给他看**不是**说给他听**, 输出形状完全不同(一张卡片).
//	硬塞进来的话返回值要么是文字要么是卡片, 而调用方分不出该画哪个.
//
// WhereKit where 背后的能力. **哪个是 nil 就说明这台机器没有**, 那个用法
// 不出现在工具说明里 —— 摆一个用不了的用法, 它会照着调.
type WhereKit struct {
	// Now 现在什么情况. 必须有
	Now func() string
	// Route 到某处怎么走、要多久、堵不堵. to 可以是任何地名/店名/地址
	Route func(to, from, mode string) (string, error)
	// Traffic 一条路或他周边此刻堵不堵
	Traffic func(road string) (string, error)
	// Weather 今天明天的预报
	Weather func(where string) (string, error)
	Habits  func() string
	// Trail 某一天他在哪儿待过 —— "我今天去过哪儿"
	Trail  func(day string) (string, error)
	Card   func(place string) (map[string]any, error)
	Fresh  func() string
	Places func() string
}

func WhereTool(k WhereKit) Tool {
	desc := "现在什么情况：他在哪、家里有没有人、天气、提醒、待办。" +
		"**答当下的事前先调它，别猜**"
	// 用法说明只写在各参数上, desc 里只点个名 —— 两处各写一遍是双倍字节
	var can []string
	args := map[string]string{
		// ── 主动索取位置的能力必须在参数里说明 ──
		//
		//	缺少说明时, 面对"为什么不能实时拿 GPS", 模型只能按已知能力答
		//	"手机只在它自己愿意上报时才推位置过来, 我不能主动去问".
		//	能力存在但模型不知道, 就无法被调用。
		"fresh": "1=现问手机要新位置",
	}
	order := []string{"fresh"}
	if k.Route != nil {
		// ── to 不再要求"起过名" ──
		//
		//	原来 to 只认起过名的地方, 于是"到凤凰国际广场多久"答不了,
		//	模型就把"这儿"起名成凤凰国际广场(他人在家)再问一次 ——
		//	"0 分钟". 现在认不出的名字工具自己去地图上搜
		can = append(can, "问路(按实时路况)")
		args["to"] = "目的地：地名/店名/地址，认不出会自己搜"
		args["from"] = "起点，不填=他此刻"
		args["mode"] = "开车/公交/骑车/走路"
		order = append(order, "to", "from", "mode")
	}
	if k.Traffic != nil {
		can = append(can, "路况")
		args["traffic"] = "路名，或 1=他周边"
		order = append(order, "traffic")
	}
	if k.Weather != nil {
		// 当前天气只覆盖"此刻"; "今天天气"需要预报, 不能只答"现在多云".
		can = append(can, "预报")
		args["weather"] = "1=他那儿，或城市"
		order = append(order, "weather")
	}
	if k.Habits != nil {
		can = append(can, "他常去哪")
		args["habits"] = "1=他平时在哪"
		order = append(order, "habits")
	}
	// 账本里躺着每一条位置信号, 而它原来够不着 —— 只能 run grep
	if k.Trail != nil {
		can = append(can, "他哪天去过哪儿")
		args["day"] = "今天/昨天/09-11=他那天待过哪些地方"
		order = append(order, "day")
	}
	if k.Card != nil {
		can = append(can, "发地图")
		args["map"] = "1，或地名"
		order = append(order, "map")
	}
	if k.Places != nil {
		args["places"] = "1=认得哪些地方"
		order = append(order, "places")
	}
	if len(can) > 0 {
		desc += "。也能" + strings.Join(can, "、")
	}
	opt := map[string]bool{}
	for _, a := range order {
		opt[a] = true
	}
	opt["places"] = true
	return Tool{
		Name: "where", Desc: desc, Args: args, ArgOrder: order, Optional: opt,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			if yes(argStr(a, "places")) && k.Places != nil {
				return k.Places(), nil
			}
			// ── 发一张地图 ──
			//
			//	"我在哪"这个问题的答案既是一句话也是一张图, 拆成两个工具的
			//	结果是它答完话就走了 —— 而人问这句话的时候想要的多半是后者
			if m := strings.TrimSpace(argStr(a, "map")); yes(m) {
				if k.Card == nil {
					return "", fmt.Errorf("这台机器没配地图")
				}
				place := m
				if place == "1" || place == "true" {
					place = ""
				}
				spec, err := k.Card(place)
				if err != nil {
					return "", err
				}
				if t.Sys == nil {
					return "", fmt.Errorf("这台机器上没有界面, 展示不了。把地名直接说出来")
				}
				raw, err := json.Marshal(spec)
				if err != nil {
					return "", err
				}
				// 走 ui 通道那条现成的路 —— 跟 show 一模一样
				t.Sys.Emit(map[string]any{"channel": "ui", "text": string(raw)})
				// **卡片上写了什么要告诉模型**: 卡片标着"固镇县庙岗路"时,
				// 若工具只返回"发过去了", 模型仍可能误答"系统认不出是哪儿".
				title, _ := spec["title"].(string)
				sub, _ := spec["sub"].(string)
				if title == "我现在在这儿" {
					title = ""
				}
				if on := strings.TrimSpace(strings.Trim(title+"，"+sub, "，")); on != "" {
					return "地图已经发过去了，卡片上写着：" + on + "。", nil
				}
				return "地图已经发过去了。", nil
			}
			// 问路 / 路况 / 天气 **排在 fresh 前面**: 它们自己会在位置旧的
			// 时候现问手机. 原来 fresh 排第一, fresh=1&to=家 的 to 被吞了
			if to := strings.TrimSpace(argStr(a, "to")); to != "" {
				if k.Route == nil {
					return "", fmt.Errorf("这台机器没配地图, 算不了路程。**照实说**, 别估一个时间")
				}
				return k.Route(to, strings.TrimSpace(argStr(a, "from")),
					strings.TrimSpace(argStr(a, "mode")))
			}
			if tr := strings.TrimSpace(argStr(a, "traffic")); yes(tr) {
				if k.Traffic == nil {
					return "", fmt.Errorf("这台机器没配地图, 看不了路况。**照实说**")
				}
				return k.Traffic(tr)
			}
			if w := strings.TrimSpace(argStr(a, "weather")); yes(w) {
				if k.Weather == nil {
					return "", fmt.Errorf("这台机器查不了预报 —— 只有下面「屋里」那条此刻的天气")
				}
				return k.Weather(w)
			}
			if yes(argStr(a, "fresh")) && k.Fresh != nil {
				return k.Fresh(), nil
			}
			if d := strings.TrimSpace(argStr(a, "day")); d != "" {
				if k.Trail == nil {
					return "", fmt.Errorf("这台机器没有位置记录")
				}
				return k.Trail(d)
			}
			if yes(argStr(a, "habits")) {
				if k.Habits == nil {
					return "", fmt.Errorf("这台机器看不出规律")
				}
				return k.Habits(), nil
			}
			out := k.Now()
			// ── 说清楚这台机器**答不了**什么 ──
			//
			//	**编一个"我查过了"比说"我不知道"糟得多**: 他会照着
			//	那个时间出门.
			var missing []string
			if k.Route == nil {
				missing = append(missing, "路程和实时路况")
			}
			if k.Weather == nil {
				missing = append(missing, "天气预报")
			}
			if k.Card == nil {
				missing = append(missing, "地图")
			}
			if len(missing) > 0 {
				out += "\n\n(这台机器没配地图服务, 查不了" +
					strings.Join(missing, "、") + ")"
			}
			return out, nil
		},
	}
}

// PlaceTool 记一个地方 —— **把 name_place 和 remember_place 收成一个**.
//
//	不给 address = "这儿叫公司"(记他此刻站的地方, 要人真在那儿)
//	给了 address = "我上班在合肥xx大厦"(他可以在家里说这句)
//
//	分成两个工具会造成错误选择: 人在公司并命名"这儿是公司"时,
//	模型可能挑 remember_place 再索要地址, 尽管已有当前坐标.
func PlaceTool(here func(name string, radius float64, sure bool) (string, error),
	byAddress func(name, address string, radius float64) (string, error),
	forget func(name string) (string, error)) Tool {
	desc := "给地方起名（家/公司）。**不给 address=记他此刻站的地方，他得真在那儿**"
	args := map[string]string{
		"name": "照他的说法，别改",
		// ── 圈多大他才知道 ──
		//
		//	缺省 250 米对小区和写字楼够用, 但一个大院、一所学校
		//	能有半公里. 圈小了的后果是**他人在里面而系统说不认识**,
		//	而且不会报错; 相差 190 米就可能造成这种静默误判.
		"radius": "米。小区/学校 400-600，店 100，默认 250",
		// ── 工具会先核对他是不是真在那儿 ──
		//
		//	(他在别的起过名的地方里 / 地图上这个名字在一公里外 → 拒).
		//	他亲口说"就是这儿"而工具拒了的时候, 这个是出口
		"sure": "他亲口说就是这儿、工具却拒了时填 1",
	}
	order := []string{"name", "radius", "sure"}
	if byAddress != nil {
		args["address"] = "他不在那儿时填地址或店名"
		order = append(order, "address")
	}
	// ── 删一个地方 ──
	//
	//	原来它只能加不能删: 地点表里"公司"和"凤凰国际广场"是同一栋楼的
	//	两个名字, 它知道(它自己说"在公司，凤凰国际广场"), 却收拾不了 ——
	//	最后这件事被推回给了他. 那本来就是它该办的
	if forget != nil {
		args["forget"] = "1=删掉这个名字（重名、记错了的；随时能再记回来）"
		order = append(order, "forget")
	}
	return Tool{
		Name: "place", Desc: desc, Args: args, ArgOrder: order,
		Optional: map[string]bool{"address": true, "radius": true, "sure": true, "forget": true},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			n := strings.TrimSpace(argStr(a, "name"))
			if n == "" {
				return "", fmt.Errorf("名字是空的")
			}
			if yes(argStr(a, "forget")) {
				if forget == nil {
					return "", fmt.Errorf("这个环境里删不了地方")
				}
				detail, err := forget(n)
				if err != nil {
					return "", err
				}
				return okResult("success", detail), nil
			}
			if addr := strings.TrimSpace(argStr(a, "address")); addr != "" {
				if byAddress == nil {
					return "", fmt.Errorf("这台机器没配地图, 查不了地址。" +
						"他人就在那儿的话, 别填 address")
				}
				detail, err := byAddress(n, addr, argFloat(a, "radius"))
				if err != nil {
					return "", err
				}
				return okResult("success", detail), nil
			}
			if here == nil {
				return "", fmt.Errorf("这个环境里没有位置服务")
			}
			detail, err := here(n, argFloat(a, "radius"), yes(argStr(a, "sure")))
			if err != nil {
				return "", err
			}
			return okResult("success", detail), nil
		},
	}
}

// AgendaTool 待办和日程 —— **一件事, 不是一句话**.
//
// ── 跟 remind_me 的分工 ──
//
//	remind_me   到点说一句话. 说完就没了.
//	agenda      一件**要做的事**. 它会一直在, 直到做完或者扔掉.
//
//	他说"下周三三点开会", 要的是两样: 日程里有这一条(他能翻到),
//	和到点前提醒他一次. 那是两次调用, 不是一次 —— 合起来的话,
//	划掉待办会顺带撤掉闹钟, 而那多半不是他要的.
//
// ── 为什么四个动作挤在一个工具里 ──
//
//	记一件、看有哪些、划掉、扔掉 —— 四个工具的话, 声明字节翻两番,
//	而模型每一轮都要在四个里挑. 这个仓库为同一件事栽过一次
//	(四个"以后提醒我"). 一个 do 参数就够了.
func AgendaTool(add func(what string, atMs int64, where string) (string, error),
	list func(withDone bool) string,
	done func(id string) (string, error),
	drop func(id string) (string, error)) Tool {
	return Tool{
		Name: "agenda",
		Desc: "他要做的事和日程，活得比这段对话长。跟 remind_me 两回事：" +
			"那个到点说一句，这个是一件事",
		Args: map[string]string{
			"do":    "add / list / done 划掉 / drop 扔掉。不给就是 list",
			"what":  "做什么。用他的原话",
			"at":    "毫秒时间戳。没定时间就别给",
			"where": "在哪儿。没说就别给",
			"id":    "哪一条。照 list 里方括号里那个念，**别猜**",
		},
		ArgOrder: []string{"do", "what", "at", "where", "id"},
		Optional: map[string]bool{"do": true, "what": true, "at": true,
			"where": true, "id": true},
		Mutates: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			switch strings.TrimSpace(argStr(a, "do")) {
			case "add":
				what := strings.TrimSpace(argStr(a, "what"))
				if what == "" {
					return "", fmt.Errorf("what 是空的 —— 要记什么事?")
				}
				// **他没提过就说出来, 不拦着**: 拦下来的多半是它正常的
				// 归纳(他说"电费该交了", 它记"交电费"); 而真记错了,
				// 一条待办他一眼看得见、一句话删得掉
				unheard := t.Heard != nil && !mentions(what, t.Heard())
				detail, err := add(what, int64(argInt(a, "at")),
					strings.TrimSpace(argStr(a, "where")))
				if err != nil {
					return "", err
				}
				if unheard {
					detail += "（他最近没提过这件事 —— 是你自己推出来的就跟他说一声）"
				}
				return okResult("success", detail), nil
			case "done", "drop":
				id := strings.TrimSpace(argStr(a, "id"))
				if id == "" {
					return "", fmt.Errorf("没说是哪一条 —— 先 list 看编号, 别猜")
				}
				fn := done
				if argStr(a, "do") == "drop" {
					fn = drop
				}
				detail, err := fn(id)
				if err != nil {
					return "", err
				}
				return okResult("success", detail), nil
			default:
				return list(false), nil
			}
		},
	}
}

// yes 这个参数算不算"填了".
//
//	模型会写 1 / true / "是" / 地名, 也会写 0 / false / 空 —— 而
//	不能仅以非空判断真值, 否则 "0" 这个显式的"不要"会被当成"要".
func yes(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "0" && v != "false" && v != "否"
}

// FindPlaceTool 查一个地方在哪 —— **它原来根本答不了这种问题**.
//
// ── 为什么这一条是"全能助手"的底线 ──
//
//	他问"凤凰国际广场在哪条路上", 而这台机器上:
//	  web_search 是 nil(没配搜索服务)
//	  geocoding 要的是**地址**不是**名字**, 拿名字去问只能匹配到区
//	  fetch 得先知道 URL
//
//	三条路都不通, 于是它做了最糟的那件事: **拿手机当时的位置顶上**,
//	然后一本正经地用那个坐标算路程。用户的原话: "它经常用上下文上
//	已经存在的老的错误的数据偷偷直接给我回"。
//
//	POI 检索能答这个问题, 而且这台机器早就有 key 了 —— 缺的只是
//	一个工具。
//
// ── 为什么返回一列而不是一个 ──
//
//	"万达"在一个市里有三个。挑哪个是**他**才知道的事 —— 替他挑一个
//	等于替他决定, 而挑错了他不会知道。列出来让他认。
func FindPlaceTool(find func(query, city string) (string, error)) Tool {
	return Tool{
		Name: "find_place",
		Desc: "按名字查地方在哪（商场/学校/店），列出几个让他认。" +
			"**认不出的地名就查它**，别拿手边坐标顶",
		Args: map[string]string{
			"query": "地方的名字",
			"city":  "哪个城市。不给就按他此刻的位置找",
		},
		ArgOrder: []string{"query", "city"},
		Optional: map[string]bool{"city": true},
		Run: func(t Toolbox, a map[string]any) (string, error) {
			q := strings.TrimSpace(argStr(a, "query"))
			if q == "" {
				return "", fmt.Errorf("要找什么?")
			}
			if find == nil {
				return "", fmt.Errorf(
					"这台机器没配地图服务, 查不了地方。**照实告诉他查不了**, " +
						"别拿手边的坐标顶")
			}
			return find(q, strings.TrimSpace(argStr(a, "city")))
		},
	}
}

// HistoryTool 他那天收到过什么 —— 通知和消息、电量、WiFi、车机蓝牙、到没到某处.
//
//	**位置那一路在 where(day=…)**: 那个答的是"待过哪些地方", 这个答的是
//	"收到过什么". 分开是因为两者的形状不一样, 合在一起那一屏全是电量.
func HistoryTool(look func(what, day string, limit int) (string, error)) Tool {
	return Tool{
		Name: "history",
		Desc: "他那天收到过什么：通知和消息、电量、WiFi、车机蓝牙、到没到某个地方。" +
			"问“我今天收到什么消息”“几点到的”先调它",
		Args: map[string]string{
			"what":  "通知/电量/网络/蓝牙/到达/天气。不给就是除位置和天气之外的全部",
			"day":   "今天/昨天/09-11。不给就是今天",
			"limit": "要几条，不给就是 40",
		},
		ArgOrder: []string{"what", "day", "limit"},
		Optional: map[string]bool{"what": true, "day": true, "limit": true},
		Run: func(t Toolbox, a map[string]any) (string, error) {
			if look == nil {
				return "", fmt.Errorf("这台机器没有账本")
			}
			return look(strings.TrimSpace(argStr(a, "what")),
				strings.TrimSpace(argStr(a, "day")), argInt(a, "limit"))
		},
	}
}


// HomeTool 家里的灯、开关、场景 —— **只吆喝, 不自己动手**.
//
//	OS 跑在机房, Home Assistant 在他家路由器后面: 这一句吆喝由家里那台
//	采集端接住, 在本地调 HA(见 sense/haact.go). 所以这里既不知道有哪些
//	实体, 也不该知道 —— 名字原样传过去, 那边拿当下的实体表匹配.
//
//	**做没做成这一轮答不了**: 灯真亮了, 下一轮会有一条状态信号进来,
//	那条才是事实. 工具结果只说"吆喝出去了" —— 说成"开好了"就是替
//	一件还没发生的事作保.
func HomeTool(act func(name, do string) (string, error)) Tool {
	return Tool{
		Name: "home",
		Desc: "家里的灯、开关、插座、场景、窗帘、空调：开、关、切换。" +
			"锁和安防不归它管",
		Args: map[string]string{
			"what": "哪个东西，用他的叫法（客厅灯、床头灯）",
			"do":   "on 开 / off 关 / toggle 切换",
		},
		ArgOrder: []string{"what", "do"},
		Mutates:  true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			what := strings.TrimSpace(argStr(a, "what"))
			do := strings.TrimSpace(argStr(a, "do"))
			if what == "" {
				return "", fmt.Errorf("没说是哪个东西")
			}
			if do == "" {
				return "", fmt.Errorf("没说开还是关")
			}
			if act == nil {
				return "", fmt.Errorf("这台机器没接家里的东西")
			}
			detail, err := act(what, do)
			if err != nil {
				return "", err
			}
			return okResult("success", detail), nil
		},
	}
}
