package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// 上下文管理 —— 让前缀缓存真的命中.
//
// 每步发送历史时必须保持已有消息稳定.
// 供应商的前缀缓存要求**这一次的前缀跟上一次逐字节相同**才算命中.
// 只要中间有一条消息的措辞变了、顺序动了、或者截断位置变了,
// 后面全部作废; 每步重拼并改动前缀时, CachedTokens 可以一直为 0.
//
// 做法: 用引擎的地址空间来管历史.
//
//	· 每一轮的产物**只追加**, 从不改写已有的那几条
//	· 于是第 N 轮的 messages 永远是第 N+1 轮的严格前缀
//	· 缓存必然命中, 不需要任何"尽量复用"的启发式
//
// 超长时怎么办 —— 这里要说清一件容易想岔的事.
//
// "只能从尾部截断, 绝不改写中间"无法解决超限: 一旦上下文放不下,
// 无论丢弃还是折叠, 前缀都必然作废,
// 区别只在作废多少、多久一次. 所以真正该优化的不是"改不改",而是:
//
//	折什么     只折工具结果 (大头), 动作和用户输入原样留着
//	折多少     一次折到水位线 (一半), 不是刚好够 —— 刚好够会几乎每轮触发
//
// 没超限时那条铁律仍然成立: **一个字节都不许动**.

// ContextWindow 一个执行体的对话历史.
//
// 它是 engine.ContextSpace 的薄封装 —— 页表负责去重/换出/回收,
// 这里只负责"怎么把页变成 messages".
type Window struct {
	space *engine.ContextSpace
	// lastTrim 最近一次 Messages() 的账.
	//
	// **Messages() 是有副作用的**: 它决定折哪些页、把折掉的页交还给页表.
	// 所以它不能被当成一个可以随便再调一次的取值器 —— 原来 agent 循环里
	// 为了看一眼 Starved 就又调了一次, 那一次会照着第一次之后的状态再折一轮,
	// 而它的返回值(包括折叠通知)转手就被丢掉.
	//
	// 账记在窗口自己身上, 想知道上一轮折了什么问它就行, 不用再算一遍.
	lastTrim Trim
	// task 任务原文. 永远在最前面, 是所有轮次的公共前缀
	task string
	// maxTailBytes 上下文预算. 超了就从最旧的工具结果开始折叠,
	// 折叠这件事本身要留痕 —— 不许静默丢
	maxTailBytes int
	// foldedPages 已经折过的页 —— 记账而不是每轮重算.
	//
	// 两个作用, 缺一不可:
	//	① 页交还给页表之后 Get 就失败了, 而通知还要说清
	//	   "你刚才执行了什么、省了多少字节、这是哪次调用的结果"
	//	② **一次折了就永远折**. 每轮重算的话, 预算一涨已经折过的页
	//	   可能又"不折了" —— 那会改写前缀, 缓存整段作废, 而且是静默的
	foldedPages map[engine.PageID]foldRecord
	// trimmedThought 推理文本已经被收掉的动作页.
	//
	// **跟 foldedPages 一样必须是粘的**: 一次收了就永远收. 每轮重算的话,
	// 预算一涨它可能又"不收了", 那会改写前缀, 缓存整段作废, 而且是静默的.
	trimmedThought map[engine.PageID]bool
	// budgeter 按模型窗口自动定预算. **只在没人显式指定时才作数.**
	//
	// 顺序反了会出静默故障: 轮次 8 加自动预算之后, budgeter 一装上就
	// 盖过了显式值, 于是 NEOX_CTX_BYTES 悄悄失效 —— 设了没反应,
	// 也不报错, 排查时完全看不出来.
	//
	// **人显式下的命令优先于系统的自动推算**, 这条不能反.
	budgeter *Budgeter
	// explicit 预算是人显式指定的 (构造时传了非零值或设了环境变量)
	explicit bool
}

// UseBudgeter 让预算跟着模型窗口走
func (w *Window) UseBudgeter(b *Budgeter) { w.budgeter = b }

// BudgetBytes 当前上下文预算 —— 报折叠时要跟着说, 不然"折了 3 条"
// 没法判断是预算配小了还是活确实大
func (w *Window) BudgetBytes() int { return w.budget() }

// budget 这一轮的预算. 人显式指定的优先于自动推算的.
func (w *Window) budget() int {
	if w.explicit || w.budgeter == nil {
		return w.maxTailBytes
	}
	return w.budgeter.Bytes()
}

// thoughtKeepBytes 短于这个长度的推理不收 —— 省不了什么, 还白断一次缓存
const thoughtKeepBytes = 2000

// thoughtTrimmed 收掉之后留下的标记.
//
// **不能是空串**: 带 tool_calls 的消息必须回传 reasoning_content,
// 空的会被供应商直接拒掉(这条是客户端那边用 400 换回来的经验).
const thoughtTrimmed = "(这一步的推敲过程已回收；做过什么看下面的调用。)"

const defaultTailBytes = 60 * 1024

// NEOX_CTX_BYTES 覆盖上下文预算.
//
// 不同模型的窗口差一个数量级, 写死一个数一定有一头是错的.
// 放在这里而不是两个调用点各读一次 —— 缺省值的来源必须只有一处,
// 否则两个入口的行为会悄悄分叉.
const ctxBytesEnv = "NEOX_CTX_BYTES"

func NewWindow(store *engine.PageStore, task string, maxTailBytes int) *Window {
	explicit := maxTailBytes > 0
	if maxTailBytes <= 0 {
		maxTailBytes = defaultTailBytes
		if v, err := strconv.Atoi(os.Getenv(ctxBytesEnv)); err == nil && v > 0 {
			maxTailBytes, explicit = v, true
		}
	}
	w := &Window{
		space:          engine.NewContextSpace(store, "agent"),
		task:           task,
		maxTailBytes:   maxTailBytes,
		explicit:       explicit,
		foldedPages:    map[engine.PageID]foldRecord{},
		trimmedThought: map[engine.PageID]bool{},
	}
	return w
}

// Release 进程结束时放掉这个地址空间 —— 页才可能被回收
func (w *Window) Release() { w.space.Release() }

// AppendUserTurn 记新一轮的用户输入.
//
// 它跟工具结果一样只是**追加一页** —— 所以多轮对话的前缀
// 依然是严格递增的, 缓存跨轮有效.
// 这正是"一次对话一个进程"比"每句话起新进程"强的地方.
func (w *Window) AppendUserTurn(text string) {
	w.space.Append(engine.KindInput, map[string]any{"user": text})
}

// AppendInterrupt 用户在**干活的中途**插的一句话.
//
// ── 为什么不能跟普通轮次一样append ──
//
// 一次插话会落在几十条工具结果中间, 渲染出来就是一个孤零零的
// `{"user":"顺便也统计一下 IP"}` —— 跟它前后的一堆数据长得一模一样.
// 模型没有任何理由认为那是**人刚刚下的新指令**.
//
// 未标明插话身份的失败案例是: 内容完整进入请求和事件日志, 模型仍按
// 原任务读 2000 多行, 全程不提 IP. 仅把"这是最新的指令"写进 agent
// 循环的 history 也无效, 因为 LLM.once() 只从 Window 拼消息,
// **不读 history**. 只断言 history 的测试不能证明模型收到框架话.
//
// 框架话必须跟内容在**同一处**, 也就是真正发出去的那份消息里.
func (w *Window) AppendInterrupt(text string) {
	w.space.Append(engine.KindInput, map[string]any{
		"user":      text,
		"interrupt": true,
	})
}

// interruptNote 插话的那句框架话.
//
// ── 它只能挂在**最新那条**上 ──
//
// 原来这句话是跟着内容一起存进页里的, 也就是**永久留在上下文里**.
// 用户连问几句之后, 历史里就有好几条都自称"这是最新的指令, 优先于你
// 正在做的事, 然后照它做" —— 于是它每一轮都把那几个老问题再服务一遍.
//
// 用户看到的样子: 问"你有什么工具", 它答完工具, 又"顺手把你前面几个问题
// 一起答了", 把蚌埠在哪、为啥这么快全部重答一遍.
//
// "最新"是个**位置**, 不是内容的一部分. 存进页里就等于宣布它永远最新.
// 所以框架话在**组装时**才贴, 而且只贴给最后一条插话.
// **说位置, 不说该怎么办**: 它是要你停下、改方向、还是补一个要求,
// 那是它读了这句话之后的判断 —— 而"这句是最新的"是我们才知道的事实,
// 它混在几十条工具结果里, 没有任何线索看得出这是人刚刚说的.
const interruptNote = "（这句是他在你干活的中途插进来的 —— **这是最新的指令**。）"

// AppendStep 记一次模型的动作 (它决定调哪个工具).
//
// **只追加**. 这是前缀稳定的唯一来源.
//
// ── 调用编号必须存供应商给的那一个 ──
//
// 不能按位置推导 call_0/call_1 等编号: id 不只是本地重建对话的配对键,
// 还参与供应商对消息来源的校验.
//
// 供应商靠 tool_call id 认"这一轮是不是我自己生成的". id 不是它给的,
// 它就把这条 assistant 消息当成外部拼出来的, 转而要求把
// reasoning_content 也交出来 —— 而报出来的错就是那句
// "The reasoning_content in the thinking mode must be passed back",
// **跟真实原因(id 不对)完全不是一回事**.
//
// 同一段对话只换 id 时的协议接受条件:
//
//	用供应商给的 id            → 通过
//	换成本地生成的 call_0      → 拒绝
//	本地 id + 带 reasoning    → 通过
//
// 没有 id 的页 (从事件日志恢复的历史里根本没有这个字段) 退回**文本形式**
// 渲染 —— 账本本来就是给人看的展示流、结果还是截断的, 拿它冒充一段
// 协议对话既做不到也没必要.
func (w *Window) AppendStep(tool string, args map[string]any, callID string) {
	w.AppendBatch([]PageCall{{ID: callID, Tool: tool, Args: args}}, "")
}

// PageCall 落进页表的一次调用
type PageCall struct {
	ID   string
	Tool string
	Args map[string]any
}

// AppendBatch 记一次模型的动作. 一次决定 = 一页, 哪怕它同时发了好几个调用.
//
// ── 为什么必须整批落成一页 ──
//
// 每个调用各落一页会在重建时把**一条 assistant 消息拆成 N 条**.
// 即使 id 都来自供应商, 一条带 3 个调用的消息变成三条后形状也不再匹配,
// 供应商会判定"这不是我生成的", 并报 reasoning_content 错误.
//
// **重建出来的形状必须跟它当初发出来的一模一样**, 不只是 id 对得上.
// reasoning 是这一步的推理原文. **必须跟动作一起落页**:
// 下一轮重发时, 带 tool_calls 的 assistant 消息必须把它一并交回去
// (DeepSeek thinking 协议双向 400 规则), 只留在这一轮的内存里就来不及了.
func (w *Window) AppendBatch(calls []PageCall, reasoning string) {
	items := make([]any, 0, len(calls))
	for _, c := range calls {
		item := map[string]any{"tool": c.Tool, "args": orEmptyArgs(c.Args)}
		if c.ID != "" {
			item["id"] = c.ID
		}
		items = append(items, item)
	}
	page := map[string]any{"calls": items}
	if reasoning != "" {
		page["reasoning"] = reasoning
	}
	w.space.Append(engine.KindOutput, page)
}

// AppendResult 记一次工具结果.
//
// callID 说明它是哪次调用的结果 —— **按 id 配对, 不靠相邻顺序**.
// 靠顺序的话, 折叠、乱序、批次拆分任意一个出岔子都会产生一条
// 没有归属的结果消息, 而供应商会因此拒掉整个请求.
func (w *Window) AppendResult(result, errMsg, callID string) {
	payload := map[string]any{"result": result}
	if errMsg != "" {
		payload = map[string]any{"error": errMsg}
	}
	if callID != "" {
		payload["callId"] = callID
	}
	w.space.Append(engine.KindToolResult, payload)
}

// Messages 拼出这一轮要发的消息.
//
// 顺序恒定: 任务原文 → 每一轮的 (动作, 结果) 对.
// 因为页只增不改, 所以这个序列的前缀永远稳定.
func (w *Window) Messages() ([]abi.InferMessage, Trim) {
	ids := w.space.PageIDs()
	store := w.space.Store

	// 先算总量, 决定要不要从头部省略.
	// 已经折过的页不算 —— 它们的内容已经交还给页表, 不再进请求.
	// **按出现次数算, 不按页数算.**
	//
	// 页表是内容寻址的: 同一段内容(比如同一个文件读了两次)只有一页,
	// 但它在这段对话里出现几次就要发几次 —— 花的是几份钱.
	// 反过来, 折它一次就把那几次全折了.
	occ := map[engine.PageID]int{}
	total := 0
	for _, id := range ids {
		occ[id]++
		if w.foldedPages[id].bytes > 0 {
			continue
		}
		n := store.BytesOf(id)
		// 推理已经收掉的动作页, **按收掉之后的大小算**.
		//
		// 我第一版漏了这一句: 收的时候从 acc 里减了, 下一轮重算 total 时
		// 却又按全长加回来 —— 于是永远超限, 而该收的已经收过了,
		// 每轮都在空转. 症状跟没修一样(每步还是折 1 条), 只是省的字节数变大了.
		if w.trimmedThought[id] {
			if page, err := store.Get(id); err == nil {
				if r := pageReasoning(page); len(r) > len(thoughtTrimmed) {
					n -= len(r) - len(thoughtTrimmed)
				}
			}
		}
		total += n
	}

	// 这一轮**新**折的页要交还给页表.
	//
	// 折叠原来只省请求的字节, 不省内存: 被折掉的结果页仍然被 Retain 着,
	// 页表一页都删不掉 —— 一个跑几天的常驻对话请求是有界的、内存是无界的.
	// 交还之后它们才真的能被回收, 而这是安全的: 折过的结果内容
	// **永远不会再进请求**, 留着只是占内存.
	var toDrop []engine.PageID
	var trim Trim
	limit := w.budget()
	if limit < minWorkableBudget {
		// 预算小到干不了活要**说出来**. 静默接受的后果是:
		// agent 每轮都在折叠边缘挣扎, 而看的人以为是模型不行.
		trim.Starved = true
	}
	if total > limit {
		// ── 折叠什么 ──
		//
		// 上一版是"从最旧的开始, 成对丢弃动作+结果". 那是错的:
		// 动作被丢掉之后模型**完全不知道自己做过那件事**, 于是重做一遍 ——
		// 而重做的结果又是一大坨, 立刻再次超限. 越省越糟.
		//
		// 现在只折叠**结果**, 动作原样留着:
		//
		//	动作   "我调了 read_file site/a.md"  几十字节, 但它是"我做过什么"的全部证据
		//	结果   文件内容                       几万字节, 是真正的大头
		//
		// 留着动作, 模型知道自己读过 a.md; 需要细节时它会自己重读一次,
		// 那是**它的判断**, 而不是被我们逼着重做.
		//
		// ── 用户输入永不折叠 ──
		//
		// KindInput 是多轮对话的骨架. 折掉它, "再改一下"就没有了指代对象,
		// 整个多轮记忆当场失效 —— 省那几百字节完全不值.
		//
		// ── 一次折够 ──
		//
		// 折到水位线 (一半), 不是刚好够. 折叠必然让前缀缓存作废,
		// 所以要让作废尽量少发生: 刚好够会导致几乎每一轮都触发一次.
		// ── 最新的结果永远不折 ──
		//
		// 折叠只能作用于**已经被后续步骤消费过**的结果.
		// 最新那个是模型刚刚要来的东西, 折掉它, 它看到的就是
		// "你做过这一步, 结果没了" → 立刻原样重做一次 → 又被折掉 → 死循环.
		//
		// 预算过小会让 read_file 的结果一到就被折, 模型连读三次同一段
		// 就被停滞检测硬停, 却可能给出"我读了 7 次, 每次都被系统截断,
		// 没看到任何内容"这样的交代.
		// 省下的那点空间换来的是整轮任务失败.
		newest := -1
		for i := len(ids) - 1; i >= 0; i-- {
			if p, err := store.Get(ids[i]); err == nil && p.Kind == engine.KindToolResult {
				newest = i
				break
			}
		}

		// ── 失败结果不折 ──
		//
		// 折叠原来对成功和失败一视同仁. 但两者的价值完全不对称:
		//
		//	成功的结果   文件内容, 几万字节, 需要时重读一次就有
		//	失败的原因   几百字节, 而且**重做一遍只会再失败一次**
		//
		// 折掉失败的后果是模型看不到自己上次为什么没成:
		// 一个 string_not_found 被折掉之后, 它会拿同一个 old_string
		// 再试一遍 —— 而这正是我们刚补的错误递进策略要防的事.
		// 折叠把那份记忆抹掉, 等于把策略架空.
		//
		// 代价可控: 错误信息本来就短, 留着它省不了多少空间.
		// 真要连错误都装不下, 那是预算配错了(Trim.Starved 会说出来),
		// 不是折叠该解决的问题.
		// **低水位放低救不了断缓存.** 试过 limit/3、limit/4、limit/8:
		// 24 步里断的次数**一次都没少**(都是 7 次), 只是每次多折两条.
		// 因为稳态下顶破限额的是每步新产生的内容, 不是剩下的旧内容 ——
		// 降低水位那儿根本无处可折.
		target := limit / 2
		acc := total
		for i := 0; i < len(ids) && acc > target; i++ {
			if i == newest || w.foldedPages[ids[i]].bytes > 0 {
				continue
			}
			page, err := store.Get(ids[i])
			if err != nil || page.Kind != engine.KindToolResult {
				continue
			}
			if isFailurePage(page) {
				continue
			}
			n := store.BytesOf(ids[i])
			// 留账再交还 —— 顺序不能反: 交还之后 Get 就读不到了,
			// 而账里要记的 callId 只能从页里拿
			w.foldedPages[ids[i]] = foldRecord{
				bytes: n, callID: pageCallID(page), action: lastActionBefore(store, ids, i)}
			toDrop = append(toDrop, ids[i])
			// 折一次覆盖它的全部出现 —— 省下的是 n × 出现次数
			saved := n * occ[ids[i]]
			acc -= saved
			trim.Dropped += occ[ids[i]]
			trim.DroppedBytes += saved
		}

		// ── 结果都折完了还是超 → 收模型自己的长推理 ──
		//
		// 如果只折工具结果, 每步附带的推理文本
		// (5000~6800 output tokens ≈ 15~20KB)就会无限累积、吃满预算,
		// 逼得每一步都去折仅剩的那一条结果.
		//
		// 对应的折叠序列是: 起初 7 条、3 条、2 条, 之后**每步 1 条,
		// 一直到底**. 而每折一次就改写一次历史, 打断一次前缀缓存 ——
		// 折叠步骤的未命中 token 约为不折叠步骤的两倍
		// (21938 vs 11473), 16 次折叠会多花十几万 token.
		//
		// 模拟精确复现: 每步不带回复时是健康的"每 4 步折 4 条";
		// 每步带 18KB 推理时, 从第 6 步起退化成每步折 1 条.
		//
		// **留什么、扔什么**: 动作本身(tool + args)是"我做过什么"的证据,
		// 几十字节, 永远留着; 附在上面的长篇推敲不是证据, 是过程.
		// "动作原样留着"指调用本身, 不能把长推理也当成不可回收的动作.
		for i := 0; i < len(ids) && acc > target; i++ {
			if i == newest || w.trimmedThought[ids[i]] {
				continue
			}
			page, err := store.Get(ids[i])
			if err != nil || page.Kind != engine.KindOutput {
				continue
			}
			r := pageReasoning(page)
			if len(r) < thoughtKeepBytes {
				continue // 本来就短, 收它省不了什么, 还白断一次缓存
			}
			w.trimmedThought[ids[i]] = true
			saved := (len(r) - len(thoughtTrimmed)) * occ[ids[i]]
			acc -= saved
			trim.DroppedBytes += saved
		}
	}

	// 最后一条插话是哪个 —— 只有它配得上"这是最新的指令"那句话.
	// 见 interruptNote: "最新"是位置, 不是内容的一部分.
	lastInterrupt := engine.PageID("")
	for _, id := range ids {
		if w.foldedPages[id].bytes > 0 {
			continue
		}
		if p, err := store.Get(id); err == nil && isInterruptPage(p) {
			lastInterrupt = id
		}
	}

	msgs := []abi.InferMessage{{Role: "user", Content: "任务: " + w.task}}

	// lastAction 上一条动作页说的是哪次调用 —— 折叠通知要**指名道姓**
	var lastAction string
	// 这一轮**新**折的页要交还给页表.
	//
	// 折叠原来只省请求的字节, 不省内存: 被折掉的结果页仍然被 Retain 着,
	// 页表一页都删不掉. 一个跑几天的常驻对话请求是有界的、内存是无界的.
	//
	// 交还之后它们才真的能被回收 —— 而这是安全的: 折过的结果内容
	// **永远不会再进请求**, 留着只是占内存.
	for _, id := range ids {
		// **先查账本再取页**: 折过的页已经交还, Get 必然失败,
		// 而通知要说的东西(动作、字节数、是哪次调用)全在账本里.
		if rec, ok := w.foldedPages[id]; ok && rec.bytes > 0 {
			// 折叠了什么必须让模型知道 —— 静默省略会让它以为那一步没有结果, 然后瞎猜.
			//
			// 通知必须避免两种相反的误导:
			//
			//	① "如果需要就重新读一次"会诱导同一段读 5 次,
			//	   offset 在 1/401/1/801/1 之间来回跳. 上下文放不下才折的,
			//	   原样重读还是会被折 —— 那句话是死循环的邀请函.
			//	② "原样重读一遍没有意义"又可能被理解成
			//	   **"别再碰这个文件"**: 第一轮读了 app.log 前 400 行,
			//	   第二轮说"再读 401-800", 它回"我这边没有正在读的文件" ——
			//	   而那一步的参数明明就在上一条消息里.
			//
			// 所以把两件事分开说: **同样的参数**再来一遍没意义;
			// 换个 offset 读别的部分恰恰是该做的. 并且明确指出
			// **动作参数还在旁边**, 免得它以为自己失忆了.
			// **指名道姓地说是哪一次调用.**
			//
			// 仅说"参数就在它旁边"仍可能丢失指代: 第一轮读 bench/app.log,
			// 折叠后第二轮却先 list_dir 摸索, 再读成 site/big.log,
			// **连文件都搞混**. 通知直接带上调用参数才能提供明确的定位信息.
			//
			// 调用参数已在折叠记录里, 直接写进通知即可, 不必让模型另找.
			did := rec.action
			if did == "" {
				did = "上面那一步"
			}
			// **说事实, 不排"可以做/不要做"两栏**: 接下来怎么办是它的判断.
			//
			//	这两条分别说明不同的边界, 不能省略:
			//	  "一模一样的参数"  省略是因为放不下, 原样重来还会被省略 ——
			//	                    会诱导同一段读 5 次的重复循环
			//	  "换 offset"       避免被理解成"别再碰这个文件",
			//	                    而接着读别的部分恰恰是该做的
			notice := fmt.Sprintf(
				"(你刚才执行了: %s —— 这一步确实做过, 只是结果太长, "+
					"内容已省略 %d 字节, 原文不在上下文里了。"+
					"用**一模一样的参数**再来一遍还会被省略; 换 offset 读别的部分拿得到。)",
				did, rec.bytes)
			// **折叠通知必须占住那次调用的结果位**.
			//
			// 原来它是单独一条 user 消息 —— 自造 JSON 协议下没问题.
			// 但原生协议要求每个 tool_calls 都有对应 id 的结果,
			// 少一条整个请求就被拒: 折叠会直接把请求打成 400,
			// 而现象是"一折叠就再也发不出去", 跟折叠逻辑本身毫无关系.
			if rec.callID != "" {
				msgs = append(msgs, abi.InferMessage{
					Role: "tool_result", ToolCallID: rec.callID, Content: notice})
			} else {
				msgs = append(msgs, abi.InferMessage{Role: "user", Content: notice})
			}
			continue
		}
		page, err := store.Get(id)
		if err != nil {
			// 页被换出了. 这里刻意**不自动换入** ——
			// 换入是 OS 的调度决策, agent 只如实报告自己看不到.
			continue
		}
		content := renderPage(page)
		// 插话的框架话在这儿贴, 不进页 —— 只贴给最后一条.
		// 存进页里等于宣布它永远最新, 于是老问题每轮都被再服务一遍.
		if id == lastInterrupt {
			content += "\n" + interruptNote
		}
		role := "user"
		if page.Kind == engine.KindOutput {
			role = "assistant_reply"
			calls := pageCalls(page)
			if len(calls) > 0 {
				// 记下这次动作是什么, 后面那条结果被折叠时要**指名道姓**引用它
				lastAction = describeCalls(calls)
				// 全都有 id 才走原生形式.
				//
				// **形状要跟它当初发出来的一模一样**: 一次决定就是一条
				// assistant 消息, 哪怕里面有好几个调用. 拆成 N 条的话
				// 供应商会判定"这不是我生成的", 即使调用 id 全部正确也会拒绝.
				if native := nativeCalls(calls); native != nil {
					reasoning := pageReasoning(page)
					// 收掉的推理换成一句短标记. **不能换成空串**:
					// 带 tool_calls 的消息必须回传 reasoning_content,
					// 空的会被供应商直接拒掉.
					if w.trimmedThought[id] {
						reasoning = thoughtTrimmed
					}
					msgs = append(msgs, abi.InferMessage{
						Role: "assistant_reply", ToolCalls: native,
						Reasoning: reasoning})
					continue
				}
				// 没有 id (恢复出来的历史): 退回文本. 编一个 id 会被
				// 供应商当成外部拼的消息.
				content = lastAction
			}
		}
		if page.Kind == engine.KindToolResult {
			if cid := pageCallID(page); cid != "" {
				msgs = append(msgs, abi.InferMessage{
					Role: "tool_result", ToolCallID: cid, Content: content})
				continue
			}
		}
		msgs = append(msgs, abi.InferMessage{Role: role, Content: content})
	}
	// **最后一道闸: 绝不放一条没有结果的调用出去.**
	//
	// 上面已经根治了会漏结果的那条路径(批次里提前 return), 但这条闸
	// 仍然要有 —— 它守的是"以后有人新加一条提前退出的路径"这种情况.
	// 代价是一条占位消息, 不加的代价是**整个对话永久废掉**
	// (坏历史每轮重发, 供应商见一次拒一次).
	//
	// 闸必须加在**真正产出消息的这一处**. 客户端吃过亏: 它的
	// sanitizeToolPairs 挂在一个 OpenAI 协议根本不经过的 adapter 上,
	// 从来没生效过 —— 一道走不到的闸比没有闸更糟, 因为它让人以为有保护.
	// **交还必须在消息拼完之后** —— 交还之后 Get 就读不到了,
	// 而这一轮的通知还要从页里取 callId.
	if len(toDrop) > 0 {
		w.space.Drop(toDrop)
	}
	// 账落在窗口上 —— 调用方想知道这一轮折了什么, 问它, 别再算一遍
	w.lastTrim = trim
	return repairPairs(msgs), trim
}

// Trim 这一轮省略了多少 —— 要进事件日志, 不许静默
// FoldedCallIDs 结果已经被折出上下文的那些调用.
//
// 停滞检测要用它: 结果被腾掉之后, 同样的读**确实**带来新信息 ——
// 不该再算"原地转". 见 stallTracker.forgetFolded.
func (w *Window) FoldedCallIDs() map[string]bool {
	if len(w.foldedPages) == 0 {
		return nil
	}
	out := make(map[string]bool, len(w.foldedPages))
	for _, rec := range w.foldedPages {
		if rec.callID != "" {
			out[rec.callID] = true
		}
	}
	return out
}

// LastTrim 最近一次 Messages() 的账.
//
// **不要为了看这个再调一次 Messages()** —— 它会照着上一次之后的状态
// 再折一轮, 而返回值(包括给模型的折叠通知)会被丢掉.
func (w *Window) LastTrim() Trim { return w.lastTrim }

type Trim struct {
	Dropped      int
	DroppedBytes int
	// Starved 预算比一次工具结果还小 —— 这不是"紧", 是**干不了活**
	Starved bool
}

// minWorkableBudget 能干活的最小预算.
//
// 一次 read_file 分片上限就是 24KB. 预算比它还小, 意味着**结果一进来
// 就得折掉**, 模型每轮都在看"你做过这一步, 内容没了" —— 它会开始摸索:
// 4000 字节预算下的失败案例是: 第一轮读 bench/app.log, 第二轮先 list_dir
// 摸索, 然后读成 site/big.log, **连文件都搞混**.
//
// 同一个任务换成按模型窗口自动算的真实预算(256KB), 三次分片路径全对、
// 一次折叠都没触发. 所以那不是折叠逻辑的错, 是**预算配错了**.
//
// 三倍余量: 装得下当前这次结果, 还留得下前两次的动作和结论.
const minWorkableBudget = 3 * readMaxBytes

// Stats 给事件日志看的
func (w *Window) Stats() map[string]any {
	ids := w.space.PageIDs()
	bytes := 0
	for _, id := range ids {
		bytes += w.space.Store.BytesOf(id)
	}
	return map[string]any{"pages": len(ids), "bytes": bytes}
}

// AppendAssistantSaid 记 agent 自己说过的话.
//
// 恢复对话时要用: 它的回复和结论是**唯一保真留下来的思考产物**
// (工具结果原文没了), 丢掉它等于把对话恢复成一串没有结论的动作.
func (w *Window) AppendAssistantSaid(text string) {
	w.space.Append(engine.KindOutput, map[string]any{"said": text})
}

// Reclaim 回收一轮页 —— 页表不能只增不减.
//
// 一次对话是一个长期存活的进程, 不回收就是慢性泄漏.
// **绝不动活页**: 当前空间引用着的页都算活的, 回收只碰没人要的.
// foldRecord 一页被折掉时留下的账.
//
// **必须留账**: 页交还给页表之后 Get 就失败了, 而折叠通知还得说清
// "你刚才执行了什么、省了多少字节、这是哪次调用的结果".
//
// 留账还顺带修了一个静默的毛病: 折叠原来每轮重算, 预算一涨
// 已经折过的页可能又"不折了" —— 那会改写前缀, 缓存整段作废.
// 记在账上之后**一次折了就永远折**, 前缀只增不改.
type foldRecord struct {
	bytes  int
	callID string
	action string
}

func (w *Window) Reclaim() (engine.ReclaimReport, error) {
	w.syncCapacity()
	return w.space.Store.Reclaim()
}

// Reclaim 回收一轮页, 顺便把页表容量跟当前预算对齐.
//
// ── 容量必须由 Window 自己按预算算 ──
//
// 调用方独立计算会产生偏差: agentside 若传 budgeter.Bytes()*8,
// 而生效预算是显式指定的 3000, 页表却可能守着 1.8MB 的容量,
// 回收永远不触发. **同一个数在两个地方各算各的, 必然对不上.**
//
// 放在 Reclaim 里同步, 是因为预算随采样得到的字节/token 比率收敛而变化,
// 每轮对齐一次才跟得上.
//
// 8 倍余量: 历史之外还有 fork 出去的子空间、被折叠但仍被引用的页.
// 倍数太小会把还要用的页降级, 换入换出白费劲.
// 软限取硬限的四分之一: 先无损降级, 顶到硬限才删死页.
func (w *Window) syncCapacity() {
	hard := w.budget() * 8
	if hard <= 0 {
		return
	}
	w.space.Store.SetCapacity(engine.Capacity{SoftBytes: hard / 4, HardBytes: hard})
}

// orEmptyArgs 参数为空时给一个空对象.
//
// 不能给 "null": 供应商侧按 JSON 对象解析 arguments, null 会让整条
// 调用被判成格式错误. 而"这个工具不需要参数"是完全正常的情况
// (list_dir 不给 path 就是当前目录).
func orEmptyArgs(a map[string]any) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return a
}

// ── 页 → 原生协议 ──
//
// 配对**按存下来的 id**, 不靠相邻顺序. 靠顺序的话, 折叠、批次拆分、
// 页被换出任意一个出岔子, 都会产生一条没有归属的结果消息,
// 而供应商会因此拒掉整个请求.

func pageCalls(p engine.Page) []map[string]any {
	m, ok := p.Content.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := m["calls"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if c, ok := it.(map[string]any); ok {
			out = append(out, c)
		}
	}
	return out
}

// nativeCalls 全都有 id 才给原生形式; 少一个就整批退回文本.
//
// **不许只把有 id 的那几个发出去**: 那会让重建出来的形状跟模型当初
// 发的不一样, 而形状不对跟 id 不对是同一类拒绝.
func nativeCalls(calls []map[string]any) []abi.ToolCall {
	out := make([]abi.ToolCall, 0, len(calls))
	for _, c := range calls {
		id, _ := c["id"].(string)
		name, _ := c["tool"].(string)
		if id == "" || name == "" {
			return nil
		}
		args, _ := c["args"].(map[string]any)
		out = append(out, abi.ToolCall{
			ID: id, Name: name, Arguments: engine.StableStringify(orEmptyArgs(args)),
		})
	}
	return out
}

func describeCalls(calls []map[string]any) string {
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		name, _ := c["tool"].(string)
		if args, _ := c["args"].(map[string]any); len(args) > 0 {
			name += " " + engine.StableStringify(args)
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, " ; ")
}

func pageCallID(p engine.Page) string {
	if m, ok := p.Content.(map[string]any); ok {
		id, _ := m["callId"].(string)
		return id
	}
	return ""
}

// pageReasoning 动作页存下来的推理原文.
//
// 只有一个用途: 带 tool_calls 的消息重发时必须原样交回去
// (DeepSeek thinking 协议双向 400 规则). **不参与任何判定**.
// isInterruptPage 这一页是不是用户中途插的话
func isInterruptPage(p engine.Page) bool {
	m, ok := p.Content.(map[string]any)
	if !ok {
		return false
	}
	b, _ := m["interrupt"].(bool)
	return b
}

func pageReasoning(p engine.Page) string {
	if m, ok := p.Content.(map[string]any); ok {
		r, _ := m["reasoning"].(string)
		return r
	}
	return ""
}

// repairPairs 保证发出去的消息在协议上是自洽的. **两个方向都要守**:
//
//	调用没有结果  → 补一条占位结果
//	结果没有调用  → 丢掉这条结果
//
// ── 为什么两个方向都会真的发生 ──
//
// 页表会**换出**页 (Reclaim 把冷页降级进 swap), 而换出是按页选的,
// 不认识"动作和结果是一对"这回事. 结果页被换出 → 调用没了结果;
// 动作页被换出 → 结果成了孤儿. 两种都会让供应商拒掉整个请求.
//
// ── 为什么补而不是删 ──
//
// 删掉 tool_calls 会让模型看不见自己做过那一步, 于是重做一遍;
// 占位至少如实说明"这一步的结果没能留下来". 反过来孤儿结果只能删 ——
// 凭空造一条 assistant 调用等于伪造它的历史.
//
// ── 顺序 ──
//
// 占位必须插在那条 assistant **紧后面**, 不能堆到末尾: 有的供应商
// (Moonshot 那类) 要求 assistant(tool_calls) 与它的结果之间不许夹别的消息.
//
// ── 闸必须在真正产出消息的这一处 ──
//
// 客户端吃过亏: 它的 sanitizeToolPairs 挂在一个 OpenAI 协议根本不经过的
// adapter 上, **从来没生效过**. 一道走不到的闸比没有闸更糟 ——
// 它让人以为有保护.
func repairPairs(msgs []abi.InferMessage) []abi.InferMessage {
	answered := map[string]bool{}
	called := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			called[c.ID] = true
		}
		if m.Role == "tool_result" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	out := make([]abi.InferMessage, 0, len(msgs))
	for _, m := range msgs {
		// 孤儿结果: 对应的调用不在这批消息里 (动作页被换出了) —— 丢掉.
		// 留着它整个请求会被拒, 而它本身也没有上下文可言.
		if m.Role == "tool_result" && !called[m.ToolCallID] {
			continue
		}
		// 既没内容也没调用的消息一律不发 —— 供应商会拒
		if m.Content == "" && len(m.ToolCalls) == 0 && m.Role != "tool_result" {
			continue
		}
		out = append(out, m)
		for _, c := range m.ToolCalls {
			if answered[c.ID] {
				continue
			}
			answered[c.ID] = true
			out = append(out, abi.InferMessage{
				Role: "tool_result", ToolCallID: c.ID,
				Content: "(这一步的结果没能留下来。你确实做过它 —— " +
					"需要那个结果就重新拿一次。)"})
		}
	}
	return out
}

// isFailurePage 这一页记的是一次失败.
//
// 失败结果不参与折叠 —— 折掉它, 模型就看不到自己上次为什么没成,
// 于是拿同一个 old_string 再试一遍, 把错误递进策略整个架空.
func isFailurePage(p engine.Page) bool {
	m, ok := p.Content.(map[string]any)
	if !ok {
		return false
	}
	e, _ := m["error"].(string)
	return strings.TrimSpace(e) != ""
}

// lastActionBefore 这一页之前最近的那次动作是什么.
//
// 折叠通知要**指名道姓**说"你刚才执行了 X", 不能让模型自己去旁边找参数.
// 缺少明确指代会让第一轮读 bench/app.log 的任务在折叠后先 list_dir
// 摸索, 再读成 site/big.log, **连文件都搞混**.
//
// 折的时候就把这句话算好记进账本 —— 那时候页还在, 之后就读不到了.
func lastActionBefore(store *engine.PageStore, ids []engine.PageID, i int) string {
	for j := i - 1; j >= 0; j-- {
		p, err := store.Get(ids[j])
		if err != nil || p.Kind != engine.KindOutput {
			continue
		}
		if calls := pageCalls(p); len(calls) > 0 {
			return describeCalls(calls)
		}
	}
	return ""
}

// renderPage 一页在发给模型时长什么样.
//
// ── 它自己说过的话, 要以人话的形状回到它眼前 ──
//
//	一律 StableStringify 会让每轮历史都带 {"said":"…"} 外壳,
//	**诱导模型模仿存储格式**. 这种格式污染的样本是一天 11 次向用户
//	输出 {"said":""}, 还包括双层包装和缺少收尾引号的截断形式.
//	依赖 model_llm + livewrap 两百行拆包也不能解决来源问题,
//	反而可能丢掉收尾引号之后的文字.
//
//	动作页(calls)照旧 —— 那本来就是结构, 不是话.
func renderPage(p engine.Page) string {
	m, ok := p.Content.(map[string]any)
	if !ok {
		return engine.StableStringify(p.Content)
	}
	switch p.Kind {
	case engine.KindOutput:
		if said, ok := m["said"].(string); ok && len(m) == 1 {
			return said
		}
	case engine.KindInput:
		// **他说的话也一样**: 原来每一轮的用户输入回到它眼前都是
		// {"user":"…"} —— 跟 {"said":…} 是同一件事的另一半.
		// 插话页多一个 interrupt 标记, 那句框架话由 Messages 另外贴
		if text, ok := m["user"].(string); ok {
			if len(m) == 1 || (len(m) == 2 && m["interrupt"] == true) {
				return text
			}
		}
	}
	return engine.StableStringify(p.Content)
}

// ── 压紧: 把聊过的那一大截换成一段摘要 ────────────────────────
//
// ── 为什么非做不可 ──
//
//	一段对话是一个**长期存活的进程**, 而历史是跨重启装回来的(见
//	cmd/neox-console.historyOf: 那个 bot 从开机到现在的每一轮都装).
//	长会话的用量样本是: 启动即有 32801 个 prompt token, 一天内增至
//	73107, 仍不触发折叠(窗口 100 万, 预算 175 万字节,
//	够它聊上几个月).
//
//	只靠超限折叠意味着**什么都不丢, 全程原样重发**. 窗口撑得住, 但
//	  钱   每一轮都为三天前的那段闲聊付一次(缓存命中也要钱)
//	  质量 它要在几万 token 的流水里找"他刚才说什么", 而那正是
//	       "把一条流塞进上下文, 模型看到的是噪音的海"
//
// ── 为什么是"换成摘要"而不是"丢掉" ──
//
//	丢掉的后果模型看得见: 他说"上次那个再改一下", 而"上次那个"没了.
//	摘要保留结论, 丢掉过程 —— 这跟折叠只折工具结果是同一条道理,
//	只是粒度从"一次调用"抬到了"一整段对话".
//
// ── 边界: 只在用户那一句上切 ──
//
//	原生协议要求每个 tool_calls 都有对应 id 的结果. 从一对动作/结果
//	中间切开的话, 请求直接 400(这个仓库为同一件事栽过一次, 见
//	repairPairs). 用户的输入页是天然的分界: 它前面那一轮已经收尾了.

// CompactFrom 从第 n 页开始留, 前面的换成一段摘要.
//
//	返回真正切掉了多少字节. 0 = 没切(没找到能切的地方).
//	**摘要是一条用户消息**: 它不是任何一次调用的结果, 挂在 assistant
//	名下会让配对逻辑找不到对应的调用.
func (w *Window) CompactFrom(summary string, keepFrom int) int {
	store := w.space.Store
	ids := w.space.PageIDs()
	if keepFrom <= 0 || keepFrom >= len(ids) || strings.TrimSpace(summary) == "" {
		return 0
	}
	// **切在用户那一句上** —— 往后找第一条用户输入页
	cut := -1
	for i := keepFrom; i < len(ids); i++ {
		p, err := store.Get(ids[i])
		if err != nil || p.Kind != engine.KindInput {
			continue
		}
		cut = i
		break
	}
	if cut <= 0 {
		return 0
	}
	dropped := 0
	for _, id := range ids[:cut] {
		if w.foldedPages[id].bytes > 0 {
			continue // 已经折过的, 内容早就不在请求里了
		}
		dropped += store.BytesOf(id)
	}
	// 留下来的那些**按内容重新追加**到一个新空间 —— 页表是内容寻址的,
	// 同样的内容还是同一页, 所以这一步不复制任何字节
	keep := make([]engine.Page, 0, len(ids)-cut)
	for _, id := range ids[cut:] {
		p, err := store.Get(id)
		if err != nil {
			continue // 折过/换出的页: 它的内容本来就不进请求
		}
		keep = append(keep, p)
	}
	// **省不下就别压**: 摘要本身也占字节, 而压一次 = 整段前缀作废一次.
	// 小打小闹地压, 花的比省的多
	if dropped <= len(summary)+len(compactNote) {
		return 0
	}
	old := w.space
	next := engine.NewContextSpace(store, old.Label)
	next.Append(engine.KindInput, map[string]any{"user": compactNote + summary})
	for _, p := range keep {
		next.Append(p.Kind, p.Content)
	}
	w.space = next
	old.Release()
	// 账本跟着页走: 留下来的页才留账, 其余的连同旧空间一起作废
	live := map[engine.PageID]bool{}
	for _, id := range next.PageIDs() {
		live[id] = true
	}
	for id := range w.foldedPages {
		if !live[id] {
			delete(w.foldedPages, id)
		}
	}
	for id := range w.trimmedThought {
		if !live[id] {
			delete(w.trimmedThought, id)
		}
	}
	return dropped
}

// compactNote 摘要那一页的抬头 —— **说清楚它是什么**.
//
//	不标明来源就可能被当成用户的新指令, 而不是历史事实.
//	这与折叠通知需要说明内容来源和省略边界是同一要求(见 foldRecord).
const compactNote = "[这是你和他前面聊过的那些, 已经收成一段摘要 —— " +
	"原文不在上下文里了, 但这些事实照样算数]\n"

// Bytes 这段历史现在有多大 —— 压紧的触发判据, 见 CompactFrom
func (w *Window) Bytes() int {
	store := w.space.Store
	n := 0
	for _, id := range w.space.PageIDs() {
		if w.foldedPages[id].bytes > 0 {
			continue
		}
		n += store.BytesOf(id)
	}
	return n
}

// HeadText 前 keepFrom 页说了什么 —— 拿去让模型写摘要.
//
//	**只给人话那几页**: 动作页和工具结果页里是参数和原文, 让摘要器读它们
//	等于把要省的东西再读一遍. 结论在用户输入和助手回复里.
func (w *Window) HeadText(keepFrom int, maxBytes int) string {
	store := w.space.Store
	ids := w.space.PageIDs()
	if keepFrom > len(ids) {
		keepFrom = len(ids)
	}
	var b strings.Builder
	for _, id := range ids[:keepFrom] {
		p, err := store.Get(id)
		if err != nil {
			continue
		}
		switch p.Kind {
		case engine.KindInput:
			if m, ok := p.Content.(map[string]any); ok {
				if s, _ := m["user"].(string); s != "" {
					b.WriteString("他: " + s + "\n")
				}
			}
		case engine.KindOutput:
			if m, ok := p.Content.(map[string]any); ok && len(m) == 1 {
				if s, _ := m["said"].(string); s != "" {
					b.WriteString("你: " + s + "\n")
				}
			}
		}
		if b.Len() > maxBytes {
			break
		}
	}
	return b.String()
}

// PageCount 现在有多少页 —— 压紧时用来算"留最后几页"
func (w *Window) PageCount() int { return len(w.space.PageIDs()) }

// KeepFromTailBytes 留最后 tailBytes 字节的话, 该从第几页开始留.
//
//	按字节不按页数: 一页可能是一句"嗯", 也可能是一个读回来的文件.
func (w *Window) KeepFromTailBytes(tailBytes int) int {
	ids := w.space.PageIDs()
	store := w.space.Store
	acc := 0
	for i := len(ids) - 1; i >= 0; i-- {
		if w.foldedPages[ids[i]].bytes == 0 {
			acc += store.BytesOf(ids[i])
		}
		if acc >= tailBytes {
			return i
		}
	}
	return 0
}
