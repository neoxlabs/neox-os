// Package agent 是跑在 Neox OS 上的**第一个应用**.
//
// 它证明的不是"agent 有多聪明", 而是**这个 OS 能不能承载一个 agent**:
//
//	· 进程通过 ABI 拿能力, 拿不到 OS 实例
//	· 工具调用落到被内核约束的文件系统上
//	· 越界时不是崩, 而是登记待决策 → 问人 → 醒来继续
//	· 全程没有观众也照跑
//
// 模型层是可插拔的: Scripted 用于确定性验证 (不花钱、可重放),
// HTTP 用于接真模型. **OS 侧的正确性不该依赖某个模型的心情**.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// ── 模型接口 ────────────────────────────────────────────────

// Step 模型的一步输出. 三选一:
//
//	Reply != ""  直接回话 —— 闲聊/问答/说不清的需求, **不该动手**
//	Tool  != ""  调一个工具
//	Done  != ""  活干完了
//
// Reply 这条路是必须有的. 如果只有 Tool/Done, 面对一句"嗨",
// 模型也只能硬找活干, 去 list_dir、去 read_file, 而问候根本不需要工具.
// **一个只会干活不会说话的东西不是助手, 是任务执行器.**
type Step struct {
	// Thought 给人看的一句话. 不进决策逻辑
	Thought string `json:"thought"`
	// Reply 直接回给人的话. 有它就不该再调工具
	Reply string `json:"reply,omitempty"`
	// Tool 要调的工具 (单个)
	Tool string         `json:"tool,omitempty"`
	Args map[string]any `json:"args,omitempty"`
	// CallID 单个调用时的编号 —— 结果要按它配对回去
	CallID string `json:"callId,omitempty"`
	// Reasoning 这一步的推理原文.
	//
	// **它不参与任何判定** —— 把推理喂回决策逻辑是让模型自己给自己背书.
	// 留着只有一个用途: 带 tool_calls 的消息重发时必须原样交回去,
	// 否则 DeepSeek thinking 协议直接 400.
	Reasoning string `json:"-"`
	// Tools 一次调多个**互不依赖**的工具. 只读工具会并发跑.
	//
	// 存在的理由: 读三个文件本来要三轮往返, 一轮就够.
	// 模型一次给出数组, runner 并发执行 —— 这是最直接的提速,
	// 而且省的是**往返次数**, 比省 token 更值钱.
	Tools []ToolCall `json:"tools,omitempty"`
	// Done 收工时的交付说明
	Done string `json:"done,omitempty"`
}

// Model 产出下一步. observations 是到目前为止的工具结果.
type Model interface {
	Next(task string, history []Observation) (Step, error)
	Name() string
}

// ToolCall 一次工具调用
type ToolCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
	// ID 供应商给这次调用的编号.
	//
	// 必须一路带着: 原生协议要求**每个 tool_calls 后面跟着对应 id 的
	// 结果消息**, 少一条或者对不上, 供应商直接拒整个请求.
	// 这是自造 JSON 协议没有的约束.
	ID string `json:"id,omitempty"`
}

// UnmarshalJSON 收下参数的两种写法.
//
// 标准形式是 {"tool":"read_file","args":{"path":"x.js"}}, 但模型也常写成
// {"tool":"read_file","path":"x.js"} —— 参数直接摊在项上.
//
// **这不是猜**: 除了 tool 之外的键只有一种合理解释, 就是参数.
// (跟"从自由文本里用正则抠工具调用"完全是两回事, 那个才是猜.)
//
// 不兼容这种写法会静默丢参数: 7 路并发 read_file 都把参数摊在项上时,
// 7 个 args 会全空, 而且**解析时一声不吭**. 7 个调用各失败一次,
// 第 3 个失败就足以触发停滞硬停, 使整轮报废.
func (c *ToolCall) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	c.Tool, _ = raw["tool"].(string)
	if a, ok := raw["args"].(map[string]any); ok {
		c.Args = a
		return nil
	}
	// 没有 args: 把项上其余的键当参数
	rest := map[string]any{}
	for k, v := range raw {
		if k == "tool" || k == "thought" || k == "args" {
			continue
		}
		rest[k] = v
	}
	if len(rest) > 0 {
		c.Args = rest
	}
	return nil
}

// Observation 一次工具调用的结果
type Observation struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Result string         `json:"result"`
	Err    string         `json:"err,omitempty"`
}

// ── Agent 循环 ──────────────────────────────────────────────

type Agent struct {
	// heard 他最近几句原话 —— 见 Toolbox.Heard
	heard []string
	// budgetDone 止损线撞上并且**已经给过交代**了.
	//
	// 跨轮活着, 不能是 Run 里的局部变量 —— 见 Run 开头的说明.
	budgetDone bool
	// AfterTurn 一轮说完之后叫一声 —— **系统兜底提交挂在这儿**.
	//
	//	提示词里已经写着"一件事干完就提交", 但那是要求不是保证:
	//	模型忘了、这一轮被打断了, 改动就留在工作目录里. 而没提交的东西
	//	对别人等于不存在 —— 交接传不过去, 合并合不进来, 界面上也说不出
	//	它干了什么. said 是它这一轮最后说的那句, 拿来当提交标题.
	AfterTurn func(said string)
	/**
	 * BeforeTurn 这一轮开始前, 把**要干什么**说给宿主.
	 *
	 *	宿主替它记账(每轮一次提交), 而提交标题原来只有"合入前把手上的活
	 *	收一下"这种话 —— git log 于是变成一列一模一样的句子, 谁在什么时候
	 *	干了什么, 一个字都看不出来. 那是这套东西最该答得出的问题.
	 */
	BeforeTurn func(task string)
	/**
	 * ToolsNow 每一轮开始前重新问一次工具表. nil = 用固定那份.
	 *
	 *	**这台机器有什么服务是会变的**: 用户在设置里打开"能看图"、
	 *	配上搜索、给这个项目起了 git —— 都不该等到重启才生效.
	 */
	ToolsNow func() []Tool
	// lastSaid 它这一轮最后说的那句 —— 兜底提交拿它当标题
	lastSaid string
	// FirstQuiet 第一句就是机器叫醒的. 调用方拼第一句时才知道这件事,
	// 后面每一轮由 Recv 自己带 —— 见 abi.RecvResult.Quiet
	FirstQuiet bool
	// quiet 这一轮是机器自己叫醒的, 没有人在等回话 ——
	// 回话带上这一位, 界面据此不画. 见 abi.RecvResult.Quiet
	quiet bool
	// starvedSaid 预算小到干不了活这件事说过了没有.
	//
	// 原来是 Run 里的局部变量, 于是"说一次就够"变成了**每轮说一次**.
	// 跟 budgetDone 是同一类错: 一句只该说一次的话, 状态却挂在一轮上.
	starvedSaid bool

	ABI   Syscalls
	Model Model
	Tools *ToolSet
	Box   Toolbox
	// Window 对话历史 (可选). 有它才有前缀缓存
	Window *Window
	// MaxSteps 止损: 模型的开销预估不出来, 但跑飞了要能停
	MaxSteps int
	// ToolTimeout 单次工具调用的上限. 0 = 缺省 30s.
	// **不是整个任务的上限** —— 见 runWithTimeout 的说明
	ToolTimeout time.Duration
	// Speaker 第一句话是谁说的. 之后每轮从 Recv 带过来.
	//
	//	**不是环境事实**: 它随时可能变(用户改了名字, 或者房间里换了人说话),
	//	所以不进系统段 —— 进了就等于每改一次名字作废一次前缀缓存.
	//	它跟着**那一轮的用户输入**走, 只在换人时说一句.
	Speaker string
	// lastSpeaker 上一轮是谁说的 —— 只在换人时报一次
	lastSpeaker string
	// prevPromptTokens 上一轮的 prompt 有多大 —— 前缀复用率靠它算.
	//
	//	必须是"上一轮"而不是"这一轮": 判据是**上一轮的前缀还在不在缓存里**,
	//	跟这一轮追加了多少无关.
	prevPromptTokens int64
	// lastVerified 每个文件上次自动验证的时刻 —— 冷却用.
	// 一次改动常常拆成好几个 edit, 每个都验一遍纯属浪费.
	lastVerified map[string]time.Time
}

// Serve 待命跑多轮对话.
//
// 一句话一轮, 但**进程一直活着**. 这带来三件事:
//
//	多轮记忆   历史在同一个地址空间里累积, "再改一下"仍有明确的指代对象
//	前缀缓存   跨轮保持 —— 每句话起新进程的话缓存每次都要重建
//	待命成本   等下一句时进程是 waiting, 不占计算
//
// 第一句从 first 来 (spawn 时带的), 之后每轮从 Recv 等.
func (a *Agent) Serve(first string) error {
	// 第一句的标记由**调用方**拼好 —— 它必须同时进 Window(模型看的是
	// Window, 不是这里的 turn). 这里只把"上一个说话的人"记上, 免得
	// 同一个人的第二句又报一遍. 见 SpeakerNote.
	a.lastSpeaker = strings.TrimSpace(a.Speaker)
	// 第一句也可能是机器叫醒的(闹钟把 bot 拉起来的那种) —— 见 FirstQuiet
	a.quiet = a.FirstQuiet
	turn := first
	for {
		/**
		 * **每一轮重新问一次"这台机器现在有什么工具"**.
		 *
		 *	工具表原来只在进程启动时定一次. 于是用户在设置里打开"这个
		 *	模型能看图"之后, 已经在跑的 bot **一辈子都拿不到 view_image**
		 *	—— 它收到图只会说"我看不了", 而设置页上明明写着能看.
		 *	那种不一致查起来最费劲: 两边都"对", 只是差了一次重启.
		 *
		 *	一轮重建一次的代价是拼一个切片; 而它换来的是设置改完下一句
		 *	话就生效.
		 */
		if a.ToolsNow != nil {
			now := NewToolSet(a.ToolsNow())
			a.Tools = now
			if llm, ok := a.Model.(*LLM); ok {
				llm.Tools = now
			}
		}
		if turn != "" {
			if a.BeforeTurn != nil {
				a.BeforeTurn(turn)
			}
			if err := a.Run(turn); err != nil {
				// 单轮失败不该让整个对话死掉 —— 说清楚, 然后接着等下一句.
				// 会话级的错误只有"进程被终止"和"对话关闭"两种.
				a.ABI.Emit(map[string]any{"phase": "turn_failed", "err": err.Error()})
			}
			// **失败那一轮也要收**: 它可能已经改了半个文件才失败的,
			// 而那半个文件正是下一次要接着干的东西
			if a.AfterTurn != nil {
				a.AfterTurn(a.lastSaid)
			}
		}
		msg, err := a.ABI.Recv()
		if err != nil || msg.Closed {
			return nil
		}
		// **这一轮是谁开的头**要在跑之前记上: handoff 只认用户开的头,
		// 同屋交办来的这一轮不许再往下转 —— 见 handoff.go
		a.Box.Relayed = msg.Relay
		// 机器自己叫醒的这一轮, 回话是**任务回执**不是对人说的话 ——
		// 界面据此不画. 见 abi.RecvResult.Quiet
		a.quiet = msg.Quiet
		turn = a.noteSpeaker(msg.From) + voiceNote(msg.Voice) + msg.Text + nowNote()
		if a.Window != nil {
			a.Window.AppendUserTurn(turn)
		}
	}
}

// NowNote 给调用方拼第一句用 —— 后面每一轮由 Serve 自己加
func NowNote() string { return nowNote() }

// nowNote 现在几点 —— **挂在这一句的尾巴上, 不进系统段**.
//
// ── 为什么时间属于最新消息, 不属于系统段 ──
//
//	系统段里刻意不给时间, 理由是"拼一个时间戳进去, 每分钟的缓存全废"。
//	那句话是对的 —— 但它只对**系统段**成立。
//
//	前缀缓存是线性的: [system][tools][msg0..k]。挂在**最新那条用户
//	消息的尾巴**上, 前面全部逐字节不变 —— 一个字节的缓存都不破。
//
//	缺少时间的开销样本是: 122 次 run 里有 23 次是 `date`,
//	每次一个完整往返、两秒。它想知道今天星期几、还有多久到点、
//	那条闹钟是不是已经过了 —— 这些是它每一轮都可能要的东西。
//
// ── 为什么带星期和时区 ──
//
//	"工作日才有事"这种判断要星期几; 时区不说清的话它没法把"7:05"
//	和手机上那个钟对起来; 时区相差 8 小时就会把提醒整体错开 8 小时。
func nowNote() string {
	t := time.Now()
	return fmt.Sprintf("\n\n[现在 %s %s %s]",
		t.Format("2006-01-02 15:04"), weekdayCN(t.Weekday()), t.Format("MST"))
}

func weekdayCN(d time.Weekday) string {
	return [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[d]
}

// VoiceNote 给调用方拼第一句用 —— 后面每一轮由 Recv 自己带.
func VoiceNote(voice bool) string { return voiceNote(voice) }

// voiceNote 这一轮他在用耳朵听.
//
// ── 为什么是拼在这一句前面, 不是加一层提示词 ──
//
//	它是**这一轮的一个事实**, 不是这台机器的一个模式: 同一个 bot,
//	他上一句打字、这一句说话、下一句又打字. 做成提示词层的话, 那一层
//	要么每轮都在(而它一半的轮次是错的), 要么每轮重建系统段(前缀缓存
//	全废).
//
//	而且这个仓库刚花了一整轮把两套提示词并成一套 —— 不该在这儿又
//	开一套。
//
// ── 为什么这么短 ──
//
//	多写一个字, 每一句语音都要付一遍。而要传达的就一件事:
//	**念出来的东西不能长**。屏幕上三行扫一眼就过去了, 念出来是二十秒,
//	而他多半在开车、在做饭。
func voiceNote(voice bool) string {
	if !voice {
		return ""
	}
	// **说一件事实, 不下一道命令**: 原来这句是"两三句话说完, 不分点、
	// 不列清单、不念路径和编号" —— 他大半的话是说出来的, 于是几乎每一轮
	// 都顶着一道语气钳. 念出来长什么样, 它自己知道
	return "[他这句是说出来的, 你的回答也会被念出来给他听]\n"
}

// Run 跑一个任务.
//
// 这就是 ReAct: 想 → 做 → 看 → 再想. 它不会消失,
// 变的是每一步的成本和边界.
func (a *Agent) Run(task string) error {
	// ── 交代只给一次 ──
	//
	// 撞上止损线时给用户一次完整交代是对的(做到哪了、验过什么、还差什么).
	// 但那**要花一次真实推理**. 而止损线一旦撞上就一直撞着, 于是用户
	// 之后每说一句话, 都会再买一次同样的交代.
	//
	// 开销样本: 上限 40000, 首次停止时用量 79906, 再来一句就涨到
	// **96979**. 这句不做任何工作却消耗 17073 个 token, 而且可以无限重复.
	// 一道要持续花钱才能维持的止损线, 不叫止损线.
	//
	// **零推理**地回一句就够, 而且要说清怎么走出去 —— 用户此刻唯一
	// 需要知道的是"加预算还是另起一段".
	//
	// 每次都重新问一遍 OS(零消耗探针): 预算可能被调高了, 那就接着干.
	// 拿本地标记当真相源会让"加了预算还是走不了"变成一个查不出的死结.
	if a.budgetDone {
		if err := a.ABI.Spend(abi.BudgetDelta{}); err != nil {
			a.ABI.Emit(map[string]any{"phase": "budget_done", "err": err.Error(),
				"msg": "这段对话的预算已经用完(" + err.Error() + ")，我没法再往下干了。" +
					"要接着干: 把预算调高，或者另起一段对话。"})
			return nil
		}
		a.budgetDone = false // 预算被调高了, 接着干
	}
	a.ABI.Emit(map[string]any{"phase": "start", "task": task, "model": a.Model.Name()})
	a.hear(task)
	a.Box.Heard = a.recentHeard

	var history []Observation
	// nudged/hushed 见回复那一段"说了记下了就得真记下"
	nudged, hushed := false, false
	// ── 步数**默认无限** ──
	//
	// 固定 32/40 步上限不能反映任务成本, 缺省不限制步数,
	// 由花费和进展分别约束运行.
	//
	// **步数不是一个有意义的度量**: 它既不等于花费(一步可能读一个字节
	// 也可能跑一次全量构建), 也不等于进展(原地转十步和扎实干十步
	// 数出来一样多).
	//
	// 真正需要约束的两件事都有更准确的判据:
	//
	//	止损线    按 token 记账, **由 OS 强制**, agent 躲不掉  → 管的是花费
	//	停滞检测   按"有没有进展"判                            → 管的是跑飞
	//
	// 步数是多余的第三道闸, 还是最钝的那道 —— 一个确实需要 60 步的
	// 长任务会被它拦腰砍断, 而砍断的理由跟"这活干得对不对"毫无关系.
	max := a.MaxSteps
	if max <= 0 {
		max = 0 // 无限
	}

	modelErrs := 0
	stall := newStallTracker()
	for step := 0; max <= 0 || step < max; step++ {
		/**
		 * **人按了停就是停** —— 别再往下调一个工具.
		 *
		 *	取消后继续调用没有意义: 连调 5 个工具, 每个都会当场取消.
		 *	若又把取消记成"write_file 跑了 30s 还没回来"或
		 *	"run 跑了 3m0s 还没回来", 连着三条就会触发
		 *	"同一类错误连着出现 3 次"的停滞误判, 掩盖真正的停止原因.
		 *	因此在下一步推理和调用之前检查取消状态.
		 */
		if a.Box.Ctx != nil && a.Box.Ctx.Err() != nil {
			a.ABI.Emit(map[string]any{"phase": "stopped", "n": step,
				"msg": "这一轮被叫停了"})
			return nil
		}
		// ── 干活的时候也要听得见 ──
		//
		// Serve 只在**两轮之间**调 Recv, 因此循环内也必须取收件箱.
		// 不取的失败案例是: 日志读到第 488 行时收到"停, 别读了",
		// 任务却继续读过第 1457 行, 停止指令仍躺在收件箱里.
		// 每读一片都在花钱, 仅在轮间收信无法支持中途打断.
		//
		// **注入点必须在这里**: 上一步的工具结果已经全部补齐, 现在插一条
		// user 消息不会把 tool_call 和它的 result 切开 —— 切开就是
		// 供应商直接 400, 而且报的错跟真正的原因八竿子打不着.
		//
		// 不替用户决定要不要停: 把话原样交给模型, 它自己判断是收手、
		// 改方向还是接着干. 用户可能说的是"顺便也统计一下 IP".
		if msg, has := a.ABI.TryRecv(); has && !msg.Closed && strings.TrimSpace(msg.Text) != "" {
			a.ABI.Emit(map[string]any{"phase": "interrupt", "text": msg.Text,
				"n": step, "msg": "用户中途插话"})
			// **框架话要跟内容在同一处** —— 也就是真正发出去的那份消息里.
			// 原来它只进了 history, 而 LLM.once() 只从 Window 拼消息,
			// history 一眼都不看: 话到了, 但看起来跟旁边几十条工具结果
			// 没有区别, 于是它按原计划接着读了 2000 多行.
			a.hear(msg.Text)
			note := Observation{Tool: "(用户)", Result: "用户中途说: " + msg.Text}
			history = append(history, note)
			if a.Window != nil {
				a.Window.AppendInterrupt(msg.Text)
			}
		}

		s, err := a.Model.Next(task, history)
		if err != nil {
			// 模型出错不直接死 —— 把错误当成一次观察喂回去, 让它自己纠正.
			// 这本来就是 ReAct 该有的行为: 失败也是信息.
			// 但连续错就停 —— 否则会在同一个坑里烧钱.
			modelErrs++
			a.ABI.Emit(map[string]any{"phase": "model_err", "n": step, "err": err.Error()})
			// ── 配置错了就别再撞 ──
			//
			//	供应商下线模型名并返回 400、要求更换模型时, 同一请求
		//	再次发送两遍也不会成功, 只会延迟说明原因.
			//	重试能恢复的是网络抖动和暂时的模型输出错误;
			//	模型名、key、权限这一类, **再问一百次也是同一个 400**
			if permanentModelErr(err) {
				return fmt.Errorf("模型那边拒了这个请求: %w%s", err, whatNow(err))
			}
			if modelErrs >= 3 {
				return fmt.Errorf("模型连续 %d 次出错: %w%s", modelErrs, err, whatNow(err))
			}
			// **不能叫它"重新输出一步合法 json"** —— 那是旧协议时代的话.
			//
			// 现在走的是原生工具调用: 结构由供应商保证, 模型该做的是
			// **发起一次调用**, 不是输出一段 json 文本. 而这句话字面上
			// 就是在教它把调用写成文字: 输出一整块
			// {"said": …, "tool_calls": […]} 的**文本**不会执行 recruit,
			// 动作会静默丢掉, 用户只看到一坨 JSON.
			history = append(history, Observation{
				Tool: "(模型)", Err: err.Error() +
					" —— 重来一次。要动手就**直接发起工具调用**, 不要把调用写成 json 文本(写成文字什么都不会发生)"})
			continue
		}
		modelErrs = 0
		// 问一句"还有没有预算" —— **零消耗**.
		//
		// 每步额外记一笔固定 500 token 等于**用花钱来问自己还有没有钱**:
		// 真实用量已经由 OS 在 Infer 那一处按供应商回报记账了(而且认缓存),
		// 这 500 是凭空加的, 而且正比于步数 —— 等于往"花费"这条线里
		// 掺了一个"步数"的量.
		//
		// 额外用量占比约 2%(一段 90 次推理的会话: 真实计费 ≈ 1,918,537,
		// 凭空的 45,000). 不大, 但它是假的, 而且方向固定 —— 轻量步骤越多
		// 偏得越厉害, 正好偏向"看起来像步数上限"那一边.
		if err := a.ABI.Spend(abi.BudgetDelta{}); err != nil {
			// **止损线才是真正的边界**, 而撞上它同样不该把已经做的扔掉.
			// 跟步数上限一个道理: 止损是"别再往下花钱了",
			// 不是"把做过的都作废".
			why, detail := budgetReason(err)
			return a.finalSummary(task, history, why, detail)
		}

		// 直接回话 —— 不动手就结束. 这是最常见的一条路径, 不是例外
		// 预算小到干不了活: 说一次就够, 但必须说
		if !a.starvedSaid && a.Window != nil {
			// 用**这一轮真正发出去的那次**的账, 不再调一次 Messages():
			// 它有副作用(决定折哪些页、把页交还给页表), 当取值器用会
			// 照着上一次之后的状态再折一轮, 而那一轮的返回值直接被丢掉.
			if tr := a.Window.LastTrim(); tr.Starved {
				a.starvedSaid = true
				a.ABI.Emit(map[string]any{"phase": "budget_starved",
					"msg": "上下文预算比一次工具结果还小, agent 会一直在折叠边缘挣扎 —— 看起来像模型不行, 其实是预算配错了"})
			}
		}

		// 用量播报 + 页回收 —— **推理一发生就记账**, 放在任何分支之前.
		//
		// 放在循环末尾会被"直接回话"的 return 跳过, 导致
		// **最常见的一条路径不记账**: 纯聊天的 token、缓存命中、页回收全空.
		// 漏发的量级是六轮对话只留 1 条 usage,
		// 出现 5 个回复却只有 1 次推理的矛盾记录.
		//
		// 记账的位置该由"发生了什么"决定, 不该由"接下来走哪个分支"决定.
		if m, ok := a.Model.(UsageReporter); ok && m.LastUsage().PromptTokens > 0 {
			u := m.LastUsage()
			// 每轮回收一次 —— 页表不能只增不减.
			//
			// 一次对话是一个**长期存活的进程**(可能活几天), 不回收就是慢性泄漏.
			// 缺省容量下平时什么都不做, 超限了才动手, 而且只碰死页.
			if a.Window != nil {
				if rep, err := a.Window.Reclaim(); err != nil || rep.Deleted > 0 || rep.Demoted > 0 {
					a.ABI.Emit(map[string]any{"phase": "reclaim",
						"demoted": rep.Demoted, "deleted": rep.Deleted,
						"stillOver": rep.StillOver, "liveBytes": rep.LiveBytes})
				}
			}
			if b := a.budgeter(); b != nil {
				// 预算是怎么算出来的必须看得见 —— 否则"为什么这轮折叠了"永远查不清
				a.ABI.Emit(map[string]any{"phase": "budget", "budget": b.Stats()})
			}
			// 命中率答不了"缓存到底正不正常" —— 它把"这轮追加了一大截"
			// 和"前缀断了"混成同一个数. 命中率 50% 也可能完整复用上一轮前缀.
			// 能区分的判据是: **上一轮的 prompt, 这一轮还在不在缓存里**.
			reuse, cold := cacheReuse(a.prevPromptTokens, u.CachedTokens)
			usage := map[string]any{"phase": "usage",
				"prompt": u.PromptTokens, "cached": u.CachedTokens,
				"completion": u.CompletionTokens,
				"hitRate": fmt.Sprintf("%.0f%%",
					float64(u.CachedTokens)/float64(max64(u.PromptTokens, 1))*100)}
			if a.prevPromptTokens > 0 {
				usage["reuse"] = fmt.Sprintf("%d%%", reuse)
			}
			a.ABI.Emit(usage)
			// 前缀断了要**吵**. 它的症状是账单悄悄翻几倍, 而结果照常正确 ——
			// 不吵的话没有任何一处会说这件事发生过.
			if cold {
				a.ABI.Emit(map[string]any{"phase": "cache_cold",
					"prevPrompt": a.prevPromptTokens, "cached": u.CachedTokens,
					"reuse": fmt.Sprintf("%d%%", reuse),
					"msg": "上一轮的前缀这一轮没被复用, 这些 token 在按全价重付。" +
						"先看有没有前缀漂移报告(是我们自己把前缀改了), 没有的话就是供应商那边的缓存掉了"})
			}
			a.prevPromptTokens = u.PromptTokens
		}

		if s.Reply != "" {
			// **它说过的话必须进上下文.**
			//
			// 只发事件、不写回窗口会让下一轮模型以为问题全都还没答,
			// 于是再答一遍.
			//
			// 失败案例: 连问"蚌埠是哪里""你咋这么快""你有什么工具",
			// **同一个问题被三轮各答一遍**. 其中只有 1 次插话, 另外两问
			// 是正常轮次, 所以即使插话标记不永久生效, 缺失回答仍会导致复读.
			//
			// "历史只增不改"必须覆盖所有产物: 用户输入、动作、工具结果
			// 以及**它自己的回答**, 缺少回答就没有完成过对话的证据.
			if a.Window != nil {
				a.Window.AppendAssistantSaid(s.Reply)
			}
			a.lastSaid = s.Reply
			// ── 带上流水号 ──
			//
			//	这一轮如果边生成边吐过(见 LLM.infer), 屏幕上已经有一条
			//	正在长的消息了. 不带流水号的话, 渲染端只能猜"这两条是不是
			//	同一句" —— 而猜错的结果是**同一句话出现两遍**.
			//
			//	这一条本身仍然是**账本里的真相**: 重启后重建对话窗口
			//	(restore.go)、事后召回(recall)读的都是它. 增量不进账本.
			reply := map[string]any{"phase": "reply", "text": s.Reply}
			if a.quiet || hushed {
				reply["quiet"] = true
			}
			if st, ok := a.Model.(interface{ Streamed() (string, bool) }); ok {
				if id, streamed := st.Streamed(); streamed {
					reply["stream"] = id
				}
			}
			a.ABI.Emit(reply)
			// ── 说了"记下了"就得真记下 ──
			//
			//	例如回复"记下了: 8 点到高新是你老婆单位…以后倒推我都按这个来",
			//	却不调用记录工具, 下一天仍可能按"8 点到校"催促.
			//	口头承诺不等于持久化, 必须检查这一轮是否真的执行记录.
			//
			//	话已经说出去了(流式早就到了他屏幕上), 收不回来. 能做的是
			//	**让这句话变成真的**: 再给它一步, 这一步对他静默, 只补那个
			//	漏掉的调用. 只补一次, 只认"记下了"这类过去时的明说 ——
			//	"我会提醒你"那种描述已有安排的话不算, 否则会补出重复的来.
			if !nudged && !a.quiet && claimsRecorded(s.Reply) && !recordedIn(history) &&
				a.hasRecordTool() {
				nudged, hushed = true, true
				a.ABI.Emit(map[string]any{"phase": "claim_check",
					"msg": "回复里说了已记下, 但这一轮没调任何记录工具 —— 静默补一步"})
				if a.Window != nil {
					a.Window.AppendUserTurn(claimNudge)
				}
				history = append(history, Observation{Tool: "(系统)", Result: claimNudge})
				continue
			}
			return nil
		}
		// 并行批次: 一次调多个互不依赖的工具
		if len(s.Tools) > 0 {
			obss := a.callBatch(s)
			// 同一批次内**不互相计入重复**.
			//
			// 停滞是"时间上反复做同一件事", 批次是"空间上一次做几件".
			// 一个批次里的调用是同时发出去的, 不是重试 —— 混为一谈的话,
			// 模型一次发 7 个同样的调用就会在第 3 个上被判死,
			// 而它其实只做了一个决定.
			// 整批落成**一页**: 模型发的是一条带 N 个调用的消息,
			// 重建时也必须是一条. 拆成 N 条的话供应商判定"这不是我生成的",
			// 报的却是 reasoning_content 的错, 因此不能只检查调用 id 是否匹配.
			if a.Window != nil {
				pcs := make([]PageCall, len(s.Tools))
				for i, c := range s.Tools {
					pcs[i] = PageCall{ID: c.ID, Tool: c.Tool, Args: c.Args}
				}
				a.Window.AppendBatch(pcs, s.Reasoning)
			}
			// **先把结果全部落页, 再做任何可能中断的判定.**
			//
			// 原来这两件事在同一个循环里, 而停滞判定会 return ——
			// 于是 3 个调用在第 1 个上被判死时, 后两个的结果永远不会落页.
			// 页表里就留下一条带 3 个 tool_calls 却只有 1 个结果的历史.
			//
			// 那不是"这一轮失败"这么简单: 历史是累积的, **之后每一轮
			// 都会把这段坏历史重发一遍**, 而供应商见一次拒一次 ——
			// 这个对话就永久废掉了, 用户只能新开一个.
			//
			// 跟"用量记账被分支跳过"是同一类错: **记录发生了什么,
			// 不该由接下来走哪个分支决定.**
			for i, obs := range obss {
				history = append(history, obs)
				if a.Window != nil {
					a.Window.AppendResult(obs.Result, obs.Err, s.Tools[i].ID)
				}
			}
			seen := map[string]bool{}
			// 跟单步那条路一样: 折叠过的调用不再算"上一次"
			if a.Window != nil {
				stall.forgetFolded(a.Window.FoldedCallIDs())
			}
			// **整个批次算一步**: 那几个调用是同时发出去的一次尝试 ——
			// 四个文件同时读不到, 不等于"连着撞了四次墙". 见 stall.nextStep
			stall.nextStep()
			for i, obs := range obss {
				key := s.Tools[i].Tool + "|" + fmt.Sprint(s.Tools[i].Args)
				if seen[key] {
					continue
				}
				seen[key] = true
				if v, msg := stall.observeShared(s.Tools[i].ID, s.Tools[i].Tool, s.Tools[i].Args, obs,
					a.mutates(s.Tools[i].Tool), a.worldSensitive(s.Tools[i].Tool), a.shared(s.Tools[i].Tool)); v == stallStop {
					a.ABI.Emit(map[string]any{"phase": "stall", "level": "stop", "msg": msg})
					return fmt.Errorf("检测到停滞: %s", msg)
				}
			}
			continue
		}

		if s.Tool == "" {
			// **空的 done 不是完成, 是什么都没说.**
			//
			// 把 {"done":""} 当成完成会让终端只显示一个成功标记,
			// 用户的问题一个字都没得到回答, 系统却认为这一轮成功.
			// 静默地"什么都不做还报成功"是最糟的一类路径.
			//
			// 处理方式跟空回复一致: 当成一次观察喂回去让它自纠 ——
			// 失败也是信息, 这本来就是 ReAct 该有的行为.
			if strings.TrimSpace(s.Done) == "" {
				obs := Observation{Err: "你给了一个空的 done, 等于什么都没说。" +
					"用户在等一个答案: 要么用 reply 回答他, 要么继续调工具, " +
					"要么在 done 里写清楚你做了什么、结论是什么。"}
				history = append(history, obs)
				if a.Window != nil {
					a.Window.AppendResult("", obs.Err, "")
				}
				a.ABI.Emit(map[string]any{"phase": "empty_done", "n": step})
				continue
			}
			// 收工那句同样要留下 —— 它也是"我说过什么"
			if a.Window != nil {
				a.Window.AppendAssistantSaid(s.Done)
			}
			a.lastSaid = s.Done
			done := map[string]any{"phase": "done", "summary": s.Done, "steps": step}
			if a.quiet {
				done["quiet"] = true
			}
			a.ABI.Emit(done)
			return nil
		}

		a.ABI.Emit(map[string]any{"phase": "step", "n": step,
			"thought": s.Thought, "tool": s.Tool, "args": s.Args})

		if a.Window != nil {
			a.Window.AppendBatch(
				[]PageCall{{ID: s.CallID, Tool: s.Tool, Args: s.Args}}, s.Reasoning)
		}
		obs := a.callTool(s)
		history = append(history, obs)
		if a.Window != nil {
			a.Window.AppendResult(obs.Result, obs.Err, s.CallID)
		}

		// 停滞检测 —— 提示词只是建议, 这里是硬拦.
		// 模型卡住时恰恰是它判断力最差的时候, 指望它自己看提示词不现实.
		// 折叠过的调用不再算"上一次" —— 结果被系统腾掉之后重取是正当的.
		// 放在 observe 之前: 这一步的判定就该按最新的上下文状态来.
		if a.Window != nil {
			stall.forgetFolded(a.Window.FoldedCallIDs())
		}
		stall.nextStep()
		if v, msg := stall.observeShared(s.CallID, s.Tool, s.Args, obs,
			a.mutates(s.Tool), a.worldSensitive(s.Tool), a.shared(s.Tool)); v != stallNone {
			a.ABI.Emit(map[string]any{"phase": "stall",
				"level": map[stallVerdict]string{stallWarn: "warn", stallStop: "stop"}[v],
				"msg":   msg})
			if v == stallStop {
				return fmt.Errorf("检测到停滞: %s", msg)
			}
			// warn: 当成一次观察喂回去, 让它自己纠正
			note := Observation{Tool: "(系统)", Err: msg}
			history = append(history, note)
			if a.Window != nil {
				a.Window.AppendResult("", msg, "")
			}
		}

	}
	// ── 撞上步数上限: 给它最后一次机会交代, 而不是直接死掉 ──
	//
	// 直接 return 错误会丢掉交代: 即使统计已跑完、报告已写进文件,
	// 用户也只看到一句"超过 4 步还没收工" ——
	// **活干完了, 交代没了**.
	//
	// 步数上限是**止损**, 不是判它有罪. 止损的意思是"别再往下花钱了",
	// 不是"把已经做的都扔掉". 而且这时候的总结成本极低:
	// 一次推理, 不许再调工具.
	//
	// 长任务可能在交付前触及显式上限, 因此达到上限仍需报告进展,
	// 让用户有依据决定是否继续.
	//
	// 走到这里只有一种可能: 调用方**显式**设了 MaxSteps.
	// 缺省是无限, 由止损线和停滞检测兜底.
	return a.finalSummary(task, history, fmt.Sprintf("步数上限(%d 步)", max), "")
}

// finalSummary 撞上边界时的收尾. why 说明撞的是哪条边.
//
// **只给一次推理, 而且明确告诉它不许再调工具.** 它这时候要做的不是
// 接着干, 是把"做到哪了、什么验过了、什么没做完"讲清楚 ——
// 那正是用户此刻唯一需要的东西.
//
// 收尾本身也失败的话才真的返回错误: 那时候确实没什么可交代的了.
// detail 撞的那条线的实数(花了多少/上限多少). 可以为空 ——
// 步数上限自己就带着数, 止损线的数要从 OS 那边的错误里来.
func (a *Agent) finalSummary(task string, history []Observation, why, detail string) error {
	// 止损线的交代**只给一次** —— 之后再来的话零推理挡在 Run 开头
	if strings.Contains(why, "止损线") {
		a.budgetDone = true
	}
	msg := "到" + why + "了"
	if detail != "" {
		// **数要摆出来.** 只说"到止损线了"用户没法决定下一步:
		// 花了 190 万还是 1900, 该加预算还是该看看它在瞎跑, 是两件事.
		msg += " —— " + detail
	}
	a.ABI.Emit(map[string]any{"phase": "step_limit", "why": why,
		"detail": detail, "msg": msg + ", 让它做最后一次交代"})

	note := Observation{Tool: "(系统)", Err: fmt.Sprintf(
		"这一轮到%s了, **不能再调任何工具了**。"+
			"现在只做一件事: 把情况讲清楚 —— 做完了什么(哪些是真的验证过的)、"+
			"还差什么、下一步该干什么。用户接下来要靠这段话决定要不要让你继续。", why)}
	if a.Window != nil {
		a.Window.AppendResult("", note.Err, "")
	}

	s, err := a.Model.Next(task, append(history, note))
	if err != nil {
		return fmt.Errorf("到%s了, 而且最后的交代也没做出来: %w", why, err)
	}
	// 它可能用 reply 也可能用 done 来收尾 —— 两种都收下.
	// 这时候还想调工具就不理它: 上限就是上限.
	text := strings.TrimSpace(s.Done)
	if text == "" {
		text = strings.TrimSpace(s.Reply)
	}
	if text == "" {
		return fmt.Errorf("到%s了, 而且一个字的交代都没给出来", why)
	}
	// 撞边界时的交代也要留下 —— 下一轮它得知道自己已经交代过了
	if a.Window != nil {
		a.Window.AppendAssistantSaid(text)
	}
	a.ABI.Emit(map[string]any{"phase": "done", "summary": text, "why": why,
		"hitLimit": true})
	return nil
}

// callBatch 跑一批工具调用.
//
// **只有全是只读工具时才并发**. 混了写操作就退回串行 ——
// 两个写可能落在同一个文件上, 并发跑结果不确定;
// 而且写完之后的读要看到写的结果, 那本来就是有依赖的.
//
// 判断"能不能并发"由 OS 侧的工具属性决定, 不由模型声明 ——
// 模型说"这几个互不依赖"是它的判断, 而并发安全是事实问题.
func (a *Agent) callBatch(s Step) []Observation {
	calls := s.Tools
	allReadOnly := true
	for _, c := range calls {
		if t, ok := a.Tools.Get(c.Tool); !ok || !t.ReadOnly() {
			allReadOnly = false
			break
		}
	}

	// 批次也要记参数. 只记工具名的话, 批次里出的错在账本里查不出来 ——
	// 并行调用报"缺参数"时, 只有工具名无法判断缺的是哪个参数或哪次调用.
	args := make([]map[string]any, len(calls))
	for i, c := range calls {
		args[i] = c.Args
	}
	a.ABI.Emit(map[string]any{"phase": "batch", "n": len(calls),
		"parallel": allReadOnly, "thought": s.Thought,
		"tools": toolNames(calls), "args": args})

	out := make([]Observation, len(calls))
	if !allReadOnly {
		for i, c := range calls {
			out[i] = a.callTool(Step{Tool: c.Tool, Args: c.Args})
		}
		return out
	}

	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c ToolCall) {
			defer wg.Done()
			out[i] = a.callTool(Step{Tool: c.Tool, Args: c.Args})
		}(i, c)
	}
	wg.Wait()
	return out
}

func toolNames(calls []ToolCall) []string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Tool
	}
	return names
}

func (a *Agent) callTool(s Step) Observation {
	obs := Observation{Tool: s.Tool, Args: s.Args}
	var approvedButKernelMayRefuse string

	tool, ok := a.Tools.Get(s.Tool)
	if !ok {
		obs.Err = fmt.Sprintf("没有这个工具: %s。可用的是: %s",
			s.Tool, strings.Join(a.Tools.Names(), " "))
		a.ABI.Emit(map[string]any{"phase": "tool_err", "tool": s.Tool, "args": s.Args, "err": obs.Err})
		return obs
	}
	// 参数先校验再跑 —— 缺参数要报"缺了什么", 不能变成一句系统错误
	if err := tool.Validate(s.Args); err != nil {
		obs.Err = err.Error()
		a.ABI.Emit(map[string]any{"phase": "tool_err", "tool": s.Tool, "args": s.Args, "err": obs.Err})
		return obs
	}

	// 密钥碰不得 —— 见 secrets.go. 在审批**之前**拦: 这一类不该问人
	// "允许读 provider.json 吗", 他点了允许, key 就进了上下文
	if scope := a.resolveScope(tool, s.Args); scope != "" {
		hit := ""
		switch tool.Needs {
		case abi.AxisRead, abi.AxisWrite:
			if a.Box.secret(scope) {
				hit = scope
			}
		case abi.AxisProc:
			hit = a.Box.secretInCmd(scope)
		}
		if hit != "" {
			obs.Err = secretRefusal(hit).Error()
			a.ABI.Emit(map[string]any{"phase": "tool_err", "tool": s.Tool, "args": s.Args, "err": obs.Err})
			return obs
		}
	}

	// 预检: 让 agent 提前知道, 少吃一个莫名其妙的 EACCES.
	// **这不是安全边界** —— 真强制在内核, 不调它也逃不掉.
	//
	// 要哪条能力**由工具自己声明** (Needs/ScopeArg), 不再按工具名前缀猜.
	// 那个前缀表漏过 edit_file: 它被当成读操作预检, 越界时直接撞在内核上
	// 拿一个 EPERM, **用户从来没有被问过** —— 审批路径对它整个失效.
	// 而"越界不是崩、是问人"正是这个 OS 跟沙盒的根本区别.
	//
	// 判错方向比判得粗危险得多: 把一次写当成读放行, 等于用读权限做了写.
	// 声明式没有这个失败模式 —— 新工具不声明就没有预检, 会立刻暴露,
	// 而不是被悄悄归进"读"那一类.
	if scope := a.resolveScope(tool, s.Args); tool.Needs != "" && scope != "" {
		allowed, err := a.ABI.Can(tool.Needs, scope)
		if err == nil && !allowed {
			// 越界了. **不是崩, 也不是偷偷跳过, 而是问人**.
			// 这正是这个 OS 跟"沙盒里跑 agent"的区别.
			res, derr := a.ABI.Decide(abi.DecisionRequest{
				Urgency: "high",
				Scope:   scope, // 批准之后 OS 要知道授的是什么
				Present: abi.PresentSpec{
					Kind:   "choice",
					Title:  askTitle(tool.Needs, scope),
					Detail: fmt.Sprintf("agent 想调 %s, 这不在它的能力集里", s.Tool),
					Options: []abi.PresentOption{
						{ID: "yes", Label: "允许"},
						{ID: "no", Label: "拒绝", Destructive: true},
					},
				},
			})
			if derr != nil || res.Choice != "yes" {
				obs.Err = "被拒绝: " + scope
				a.ABI.Emit(map[string]any{"phase": "denied", "scope": scope})
				return obs
			}
			a.ABI.Emit(map[string]any{"phase": "approved", "scope": scope, "by": res.By})
			// 批准了, 但**这次调用多半还是会被内核拒**: landlock 的规则
			// 在进程启动时就定死, 只能收紧不能放宽. 授权已经记在这段对话上,
			// 下次说话时新进程会带着它起来.
			//
			// 这件事必须让 agent 知道, 否则它拿到"权限被拒"会以为
			// 用户的批准没生效, 然后要么反复重试, 要么告诉用户"你没给我权限".
			approvedButKernelMayRefuse = scope
		}
	}

	out, err := a.runWithTimeout(tool, s.Args)

	if err != nil {
		obs.Err = err.Error()
		// 刚被批准却仍然失败 —— 说清真相, 否则它会以为用户的批准没生效,
		// 然后要么反复重试, 要么反过来告诉用户"你没给我权限".
		if approvedButKernelMayRefuse != "" {
			obs.Err += fmt.Sprintf("\n注意: 用户**已经批准**了 %s, 授权也记下了。"+
				"但内核的约束规则在进程启动时就定死、只能收紧不能放宽, "+
				"所以这一次仍然做不成。**不要重试, 也不要说用户没给权限** —— "+
				"告诉他授权已生效, 下次说话时就能做了。",
				approvedButKernelMayRefuse)
		}
		/**
		 * **失败也要带上参数**.
		 *
		 *	tool_err 和 tool_ok 一样带 args, 因为失败时更需要
		 *	认清"是哪一次调用". 四行并排的 list_dir 如果都没有参数,
		 *	就看不出哪个路径没找到.
		 *
		 *	(串行调用还能从前面那条 step 里认领参数; 并行批次不发 step,
		 *	于是那几行从头到尾都是空的.)
		 *
		 *	账本这边同理: "它当时试的是哪个路径"对失败才更要答得出来.
		 */
		ev := map[string]any{"phase": "tool_err", "tool": s.Tool, "args": s.Args, "err": obs.Err}
		// 闸按设计拦下的, 不是故障 —— 界面靠这个标记把两者分开显示
		if isBlocked(err) {
			ev["blocked"] = true
		}
		// "还没有这个东西"是探查的**阴性结果**, 不是故障 —— 开工先读
		// CHARTER/PLAN 是规矩, 项目里还没有它们是常态. 界面刷红的话,
		// 用户就会把正常探查误读成"两个失败", 无法区分缺失与执行故障.
		if classify(obs.Err) == errMissing {
			ev["missing"] = true
		}
		a.ABI.Emit(ev)
	} else {
		obs.Result = out
		// 写完顺手验一下 —— **提示词说"改完要跑一次"只是建议**,
		// 模型不一定跑. 跟"提示词里写了连续失败就换角度"一样,
		// 提示词管不住的东西得由 loop 兜住.
		//
		// 只在**真改动了文件**之后验, 而且验的结果跟在工具结果后面 ——
		// 不占一步、不进并发批次、没有验证器就一个字都不说.
		if tool.Mutates && tool.ScopeArg == "path" {
			if note := a.autoVerify(argStr(s.Args, "path")); note != "" {
				obs.Result += note
				a.ABI.Emit(map[string]any{"phase": "verify",
					"path": argStr(s.Args, "path"), "note": ledgerTrunc(note)})
			}
		}
		a.ABI.Emit(map[string]any{"phase": "tool_ok", "tool": s.Tool,
			"args": s.Args, "result": ledgerTrunc(obs.Result)})
	}
	return obs
}

// toolScope 这次调用要的 scope —— 参数值经工具自己的归一化.
//
// 归一化失败(翻出空串)时**返回空**, 于是预检整个跳过, 由内核/代理去拦.
// 反过来做(拿翻不出来的原值去问人)更糟: 用户会看到一句
// "允许连 我不知道这是什么 吗", 批了也不会生效.
// resolveScope 预检要看的那个 scope.
//
//	**必须跟工具真正会去动的东西是同一个**. 文件类工具的路径参数
//	是相对工作目录的, 而能力集里存的是绝对路径 —— 直接拿相对路径去比,
//	**一比一个不中**: 能力明明覆盖了工作目录, 预检照样判越界,
//	于是每写一个文件都要问一次人; 而批准挂在对话上、只对下一个进程生效,
//	这一个进程再问一次还是被拒 —— 死循环.
//
//	例如已有整个工作目录的写权限, 相对路径 hello.py 未归一化时
//	仍会被预检拦下, 再批准也不能消除路径表示不一致.
func (a *Agent) resolveScope(t Tool, args map[string]any) string {
	scope := toolScope(t, args)
	if scope == "" {
		return ""
	}
	// 只有路径轴需要相对→绝对; 命令行和主机名照原样
	if t.Needs != abi.AxisRead && t.Needs != abi.AxisWrite {
		return scope
	}
	return a.Box.resolve(scope)
}

func toolScope(t Tool, args map[string]any) string {
	raw := argStr(args, t.ScopeArg)
	if raw == "" || t.ScopeNorm == nil {
		return raw
	}
	return t.ScopeNorm(raw)
}

// askTitle 问人的那句话. 按轴分开写 ——
// 一律写成"允许写 X 吗"的话, 一条 `rm -rf build` 会被问成"允许写 rm -rf build 吗",
// 用户看不懂自己在批什么.
func askTitle(axis abi.CapAxis, scope string) string {
	switch axis {
	case abi.AxisProc:
		return fmt.Sprintf("允许跑这条命令吗?\n  %s", scope)
	case abi.AxisNet:
		// 多个目标要**逐个列出来** —— 批量审批不等于含糊,
		// 用户必须看得见他到底放行了哪几个主机
		if parts := splitTargets(scope); len(parts) > 1 {
			return "允许连这几个吗?\n  " + strings.Join(parts, "\n  ")
		}
		return fmt.Sprintf("允许连 %s 吗?", scope)
	case abi.AxisWrite:
		return fmt.Sprintf("允许写 %s 吗?", scope)
	default:
		return fmt.Sprintf("允许%s %s 吗?", axis, scope)
	}
}

// mutates 这个工具会不会改动世界 —— **问工具表, 不猜名字**.
//
// 工具表里查不到的一律当成会改动: 判错方向的代价不对称.
// 当成只读的后果是停滞检测拿不到世界变化, 把正当的"改完再读"判成原地转;
// 当成会改的后果只是少一次停滞告警.
func (a *Agent) mutates(name string) bool {
	if t, ok := a.Tools.Get(name); ok {
		return t.Mutates
	}
	return true
}

// worldSensitive 同样的调用在世界变了之后会不会给出不同结果 ——
// 同样问工具表. 查不到的当成敏感: 判错方向的代价不对称, 当成不敏感
// 会把正当的"改完再跑"判成原地转.
/**
 * shared 这个工具的结果是不是**由别人决定**的.
 *
 *	查不到当成不是: 判错方向的代价不对称 —— 多放过一次重复调用只是
 *	晚一点被别的闸拦住, 而错误地放过整类工具会让原地转彻底没人管.
 */
func (a *Agent) shared(name string) bool {
	if t, ok := a.Tools.Get(name); ok {
		return t.Shared
	}
	return false
}

func (a *Agent) worldSensitive(name string) bool {
	if t, ok := a.Tools.Get(name); ok {
		return t.WorldSensitive
	}
	return true
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func ptr[T any](v T) *T { return &v }

// MarshalStep 便于把 Step 落进事件日志
func MarshalStep(s Step) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// UsageReporter 会报告上一次推理用量的模型.
//
// 按接口而不是按具体类型判 —— 原来写的是 a.Model.(*LLM),
// 于是任何别的模型实现都悄悄不记账, 连测试都注入不进来.
// (跟 Agent.ABI 曾经绑死 *osinit.AbiClient 是同一类问题.)
type UsageReporter interface {
	LastUsage() abi.InferResult
}

// budgeter 当前模型的预算器 —— 只有真模型才有
func (a *Agent) budgeter() *Budgeter {
	if m, ok := a.Model.(*LLM); ok {
		return m.Budgeter
	}
	return nil
}

// ToolTimeout 单次工具调用的上限. 0 = 用缺省.
//
// ── 只砍单次调用, 不砍整个任务 ──
//
// 单次调用超时和任务总时长必须分开:
// 用**墙钟判死整个运行**, 结果正常的长任务被当成卡死砍掉 ——
// 一个 agent 干半小时活是正常的, 判它死是判错了.
//
//	一次 read_file 跑了 30 秒   → 卡死. 文件系统挂了、路径指向一个坏挂载
//	整个任务跑了 30 分钟        → 可能完全正常, **不许因此判死**
//
// 所以时间闸只加在**单次工具调用**上. "整个任务是不是卡住了"
// 由停滞检测回答 —— 它看的是**有没有进展**, 不是过了多久.
const defaultToolTimeout = 30 * time.Second

// runWithTimeout 跑一次工具, 超时就放弃等待.
//
// **诚实说明它的上限**: Go 杀不掉一个 goroutine.
// 超时之后我们不再等结果, 但那个 goroutine 可能还在跑.
// 对文件 I/O 来说它会自己结束; 对我们自己写的循环 (search/glob 遍历)
// 会通过 Toolbox.Ctx 收到取消, 那才是真的停下来.
// 单次 os.ReadFile 打断不了 —— 那是内核的事, 假装能打断才是骗人.
func (a *Agent) runWithTimeout(tool Tool, args map[string]any) (string, error) {
	// 等待人工决策的工具**不设时限**.
	//
	// 时间闸砍的是"机器不响应", 不是"人还没回答" —— 混了就会砍掉
	// 这个 OS 最核心的那条性质: 进程可以等人几小时而不占计算.
	// (这里的 goroutine 确实一直挂着, 但它挂在一个 socket 读上,
	// 进程本身在 OS 眼里是 waiting, 不占计算.)
	if tool.WaitsForHuman {
		box := a.Box
		box.Ctx = context.Background()
		return tool.Run(box, args)
	}

	// 工具自己声明的优先 —— 用同一个数字砍所有工具, 要么放过真卡死,
	// 要么把正常的活砍掉. `run` 声明 3 分钟, 文件工具还是 30 秒.
	timeout := tool.Timeout
	if timeout <= 0 {
		timeout = a.ToolTimeout
	}
	if timeout <= 0 {
		timeout = defaultToolTimeout
	}
	/**
	 * **从进程自己的 ctx 上派生, 不是从 Background**.
	 *
	 *	若从 context.Background() 派生, 进程被杀掉(用户按停、
	 *	换房间、关客户端)时, 取消**传不到正在跑的工具**里 —— 一条
	 *	`sleep 90` 照样跑完, 一条 `go test` 照样占着机器.
	 *
	 *	这会让按停后新旧进程同时运行: 新进程启动, 旧进程仍是
	 *	running, sleep 90 仍在系统进程表里, 成为无人管理的任务.
	 *
	 *	派生之后两条路都能停: 时限到了停, 进程被杀也停.
	 */
	parent := a.Box.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	box := a.Box
	box.Ctx = ctx

	type result struct {
		out string
		err error
	}
	// 缓冲 1: 超时之后没人收了, 无缓冲会让那个 goroutine 永远卡在发送上
	done := make(chan result, 1)
	go func() {
		out, err := tool.Run(box, args)
		done <- result{out, err}
	}()

	select {
	case r := <-done:
		return r.out, r.err
	case <-ctx.Done():
		/**
		 * **被叫停不是超时**.
		 *
		 *	若把父上下文取消当成超时, 一次停止会留下这样的假记录:
		 *
		 *	    write_file 跑了 30s 还没回来 …缩小范围
		 *	    write_file 跑了 30s 还没回来 …
		 *	    list_dir 跑了 30s 还没回来 …
		 *	    run 跑了 3m0s 还没回来 …
		 *	    停滞: 同一类错误连着出现 3 次了
		 *
		 *	**这些时长都不是真实等待时间**. 工具当场返回: 上下文一取消
		 *	select 立刻就走这条路, 而这句话里的时长是写死的那个上限,
		 *	不是它真等了多久. 于是: 一串假超时, 一次假停滞, 而真正
		 *	发生的事(人喊了停)一个字都没有.
		 *
		 *	被叫停之后也不该接着往下调工具 —— 那一轮已经结束了.
		 */
		if parent.Err() != nil {
			return "", errStopped
		}
		// **不要替它猜原因.**
		//
		// 这句话原来一律说"可能路径指向一个很大的目录树" —— 那是文件
		// 遍历类工具的成因, 套到别的工具上就是纯误导:
		// request_access 超时时它照样这么说, 而真实原因是"人还没回答".
		// 错误信息在出错那一刻被读到, 说错方向比不说更糟.
		/**
		 * **"别原样重来"这条建议对下载类的活是反的**.
		 *
		 *	三次超时均为 npm install 的案例说明: "缩小范围"会误导模型
		 *	去查 registry、查 npm cache、试各种参数, 耗尽一轮时间.
		 *	装依赖没有范围可缩, 应按下载任务的恢复方式给建议.
		 *
		 *	下载缓存是**增量**的(见 agent/cache.go): 上次下了一半的包
		 *	还在, 再跑一次是从断的地方接着下, 而不是从头来. 这时候
		 *	原样重来恰恰是对的那条路.
		 */
		return "", fmt.Errorf("%s 跑了 %s 还没回来, 已经放弃等它。%s",
			tool.Name, timeout, timeoutAdvice(args))
	}
}

// errStopped 这一轮是被人叫停的, 不是它自己出的错.
//
//	停滞检测不该算它(见 stall.go): 那不是"它原地转", 是人按了停.
var errStopped = errors.New("这一轮被叫停了")

// Stopped 这个错是不是"被人叫停"
func Stopped(err error) bool { return errors.Is(err, errStopped) }

/**
 * whatNow —— 连着几次调不通模型了, **人下一步该干什么**.
 *
 * ── 为什么非说不可 ──
 *
 *	这一轮就此结束, 而用户读到的是供应商的原话: "context deadline
 *	exceeded"、"429 Too Many Requests"、"invalid api key". 他既不知道
 *	是自己的网、是额度、还是配置错了, 也不知道要不要再说一句.
 *
 *	长会话里这是最可能撞上的一件事 —— 几个小时下来网抖一次几乎必然.
 *	而这时候他最需要知道的只有两件: **出了什么事**, 和**手上的活丢没丢**.
 *	(没丢: 失败那一轮的改动照样收进提交, 见 Serve 里的 AfterTurn.)
 *
 * ── 认不出来就别编 ──
 *
 *	给一条针对不了的建议比不给更糟 —— 同 errkind.go 那条.
 */
func whatNow(err error) string {
	const safe = "手上的活已经收好了，"
	low := strings.ToLower(err.Error())
	switch {
	case modelGone(low):
		return "。\n模型名失效了(供应商把这个名字下线了) —— 去设置里换成它现在的名字再说一句。"
	case strings.Contains(low, "401"), strings.Contains(low, "403"),
		strings.Contains(low, "api key"), strings.Contains(low, "unauthorized"):
		return "。\n看着像 key 不对或者过期了 —— 去设置里换一个再说一句。"
	case strings.Contains(low, "429"), strings.Contains(low, "rate limit"),
		strings.Contains(low, "too many requests"):
		return "。\n供应商在限流。" + safe + "过一会儿再说一句就接着干。"
	case strings.Contains(low, "402"), strings.Contains(low, "insufficient"),
		strings.Contains(low, "quota"), strings.Contains(low, "balance"):
		return "。\n看着像那边余额或者额度不够了 —— 充上再说一句。"
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline"),
		strings.Contains(low, "connection"), strings.Contains(low, "eof"),
		strings.Contains(low, "no such host"), strings.Contains(low, "refused"):
		return "。\n网这会儿不通。" + safe + "网好了再说一句就接着干。"
	}
	return ""
}

var httpStatus = regexp.MustCompile(`供应商返回 (\d{3})`)

// permanentModelErr 这个错**重来也不会好** —— 模型名、key、权限、请求形状.
//
//	看的是字符串不是类型: 错误跨了一次进程边界(模型调用走 ABI), 类型
//	早就丢了, 剩下的只有 engine 拼的那句"供应商返回 %d: …".
//	429/408/5xx 在 engine 那一层已经带退避重试过了(见 engine/retry.go),
//	到这儿还是那几个的话, 由外面"连续三次"那道兜.
func permanentModelErr(err error) bool {
	m := httpStatus.FindStringSubmatch(err.Error())
	if m == nil {
		return false
	}
	switch m[1] {
	case "401", "403", "404":
		return true
	case "400", "422":
		// 400 不全是配置错: 上下文太长、一时的形状问题, 喂回去它可能自己
		// 缩一下就好了. 只认那几种说得清"是配置"的
		low := strings.ToLower(err.Error())
		return modelGone(low) || strings.Contains(low, "api key") ||
			strings.Contains(low, "authentication") || strings.Contains(low, "unauthorized")
	}
	return false
}

// modelGone 供应商说"没有这个模型 / 这个名字不再支持".
func modelGone(low string) bool {
	if !strings.Contains(low, "model") {
		return false
	}
	for _, sign := range []string{"no longer supported", "not supported", "does not exist",
		"not exist", "not found", "invalid model", "unknown model", "model_not_found"} {
		if strings.Contains(low, sign) {
			return true
		}
	}
	return false
}

// timeoutAdvice 超时之后该往哪儿走 —— 看它跑的是什么活.
func timeoutAdvice(args map[string]any) string {
	cmd, _ := args["cmd"].(string)
	if fetching(cmd) {
		return "这是在下东西, 缓存是接着用的 —— **再跑一次就从断的地方接着下**, " +
			"不用换参数、不用查网络。"
	}
	return "**不要原样重来** —— 缩小范围(更具体的路径、更小的测试集), 或者换一条路"
}

// fetching 这条命令主要是在从网上下东西
func fetching(cmd string) bool {
	for _, sign := range []string{
		"npm install", "npm i ", "npm ci", "yarn install", "pnpm install",
		"pip install", "pip3 install", "uv sync", "uv pip install",
		"go mod download", "cargo fetch", "bundle install",
	} {
		if strings.Contains(cmd, sign) {
			return true
		}
	}
	return false
}

// ledgerTrunc 进账本的工具结果留多少.
//
// ── 截断属于呈现层, 不属于记录层 ──
//
// 之前这里截到 200 字, 而终端渲染那边**本来就又截了一次**(70 字).
// 也就是说记录层那次截断是纯损失: 显示不需要它, 而账本永久地少了证据.
//
// 代价很实: 最近四轮排查 bug, 有三轮卡在"账本里只有 200 字, 看不出
// 工具到底返回了什么". 一次我据此推错了两个方向 —— 因为账本里
// 那条 tool_ok 被截得看不出是成功还是半截.
//
// ── 为什么不干脆全存 ──
//
// 账本是要落盘的. 一次 read_file 分片上限 24KB, 跑 40 次就是 1MB,
// 而账本每次启动都要整个读一遍. 所以留一个**够排查**的量并明确标注,
// 而不是"不限"或者"200".
//
// 4KB 是照着实际用途定的: 一次搜索结果、一个中等文件的头部、
// 一条完整的错误信息, 都在这个量级之内.
const ledgerResultBytes = 4096

func ledgerTrunc(s string) string {
	if len(s) <= ledgerResultBytes {
		return s
	}
	// 截了要说出来 —— 排查的人要知道自己看到的不是全部
	return s[:ledgerResultBytes] + fmt.Sprintf(
		"\n…(账本里只留了前 %d 字节, 原文共 %d 字节)", ledgerResultBytes, len(s))
}

// budgeetReason 止损线撞上时给人看的说法.
//
// 不把供应商/OS 的原话直接抛出来: 用户看到 "[budget_exceeded]"
// 只会觉得是报错, 而这其实是**按他自己设的上限正常停下**.
// budgetReason 止损线撞了哪一条, 花了多少.
//
// 保留 err 中的哪条线、花了多少、上限多少, 避免统一标签丢掉决策依据.
// OS 侧提供这些明细(见 osinit/budgeterr.go), 消息原样穿过 ABI 边界,
// 这里直接传递即可.
//
// **报错的量必须是真实耗尽的那个量**: 步数默认无限, 把"钱花完了"报成
// "步数上限"会让用户做错决定 —— 一个该加预算, 一个该放开步数.
func budgetReason(err error) (why, detail string) {
	if err == nil {
		return "止损线", ""
	}
	return "止损线", err.Error()
}

// noteSpeaker 这一轮是谁说的. 只在**换人**时给一句, 其余时候是空.
//
// ── 为什么非要有 ──
//
// 用户在界面上设了自己的名字, 客户端也一直把它放在 /say 的 from 里送过来,
// OS 也收到名字, 但仅用于审计、只进事件日志并不能让模型知道是谁在说话.
//
// 缺少这一步时, 即使设置里已有名字, 面对"我是谁", bot 仍会答
// "我没有你身份的直接资料, 以前的对话里没有你自我介绍过".
// 界面和存储都正常不等于模型收到事实, 必须把名字送进当前输入.
//
// ── 为什么只在换人时说 ──
//
// 每轮都带一句是纯噪音: 一对一的场景里说话的永远是同一个人.
// 而房间里换了人说话时它必须知道 —— 那正是它会答错的时候.
//
// ── 为什么"我"和"你"不算名字 ──
//
// 客户端的缺省是"我", 观察口的缺省是"你". 它们是**占位词不是名字**,
// 报上去只会让模型以为用户真叫这个.
func (a *Agent) noteSpeaker(from string) string {
	name := strings.TrimSpace(from)
	if name == a.lastSpeaker {
		return ""
	}
	note := SpeakerNote(name)
	if note == "" {
		return ""
	}
	a.lastSpeaker = name
	return note
}

// SpeakerNote 谁在说话那一句. 无状态 —— 第一轮的标记要由调用方拼,
// 因为它必须**同时进 Window**: 模型看的是 Window 里的消息, 不是
// Serve 里那个 turn 变量.
//
// 这一步漏掉的表现极难看出来: 事件日志里的 start.task 带着标记(看着是对的),
// 而真正发给模型的第一条消息没有. 标记仅在日志里时,
// 模型仍会答"我不知道你是谁".
func SpeakerNote(from string) string {
	name := strings.TrimSpace(from)
	switch name {
	case "", "我", "你", "用户", "sense", "闹钟":
		// 客户端的缺省是"我", 观察口的缺省是"你" —— **占位词不是名字**,
		// 报上去只会让模型以为用户真叫这个. sense / 闹钟 是系统自己的
		// 来源, 不能变成"说话的是「sense」, 可以这么称呼他"这样的用户身份.
		return ""
	}
	// **话要说全**. 光一句"[说话的是 明康]"模型读不出这是什么意思 ——
	// 标记仅给名字、不说明它是用户身份时, 模型仍可能答"我不知道你是谁".
	//
	// 说明放在**这一句里**而不是提示词里: 提示词是每轮都付的前缀,
	// 而这句话只在换人时付一次.
	return "[说话的是「" + name + "」—— 这是用户在界面上设的名字。" +
		"他问“我是谁”就照这个答, 平时也可以这么称呼他]\n"
}
