package osinit

import (
	"github.com/neox-os/neox-os/abi"
)

// 摘要投递的接线 —— 从"外面发生了什么"到"谁看得见".
//
// ── 为什么值得单独拎出来 ──
//
// 这段逻辑原来活在 main 里的一个闭包中. 它有五条各自独立的性质
// (换地名、关注直说、破例资格、回收、兜底摊在终端), **一条测试都没有** ——
// 只靠我手工跑二进制去看. 组件全都测得好好的, 而把它们连起来的那截线没人管.
//
// 顺带治的是另一件事: main 里那个函数太长, "声明位置"的错在同一个文件里
// **栽了三次**(watches / digestsSinceSpawn / bus). 那是结构在报警.
//
// ── 五条性质, 各对一个失败 ──
//
//	换地名   模型对 "lat=31.86001" 能做的判断为零 —— 它不知道那是公司、
//	         是家、还是医院, 于是三条判据一条都过不了(S9)
//	关注直说 用户明确要盯的事不占额度也不过模型: 预算防的是"它没事找事",
//	         而这件事是他自己让做的; 中间过一次模型只多一次跑偏的机会
//	破例资格 刚投过一条紧急摘要这件事**只有 OS 知道**, 不能问 agent ——
//	         每个 agent 都觉得自己的事最急
//	回收     主动进程的上下文线性无界增长(S22), 每 N 份摘要换一个;
//	         **只有真收下了才算一份**, 否则进程没起来时会把计数空转掉
//	兜底     主动进程收不下时摊在终端上. 静默丢的话, "今天很安静"和
//	         "主动进程崩了"长得一模一样
type DigestRouter struct {
	Places  *Places
	Watches *Watches
	Budget  *InterruptBudget
	// Presence 家里现在有没有人 —— **判据要的上下文**.
	//
	// S48 在真 HA 上重放一天, 主动进程判了 15 次一次没开口, 其中
	// "白天没人在家时门开了"那件我认为该说 —— 而它不可能知道:
	// 它看到的只是 lock.opened, 而门开了一天好几次.
	// 三条判据它一条也判不出来, 因为判据要的上下文根本不存在.
	Presence *Presence

	// ToProcess 投给主动进程. 返回它收没收下 —— **收没收下决定后面两件事**:
	// 算不算一份(回收计数)、要不要兜底摊在终端上
	ToProcess func(text string) bool
	// ToTerminal 兜底. **要拿到摘要本身**, 不只是文本 ——
	// 兜底那一行得说清是哪种摘要、几条信号: 这条路走到的时候
	// 主动进程正好是坏的, 而那正是最需要线索的时刻
	ToTerminal func(d Digest, text string)
	// OnWatchHit 用户明确要盯的事命中了 —— 直接说
	OnWatchHit func(line string)

	// Respawn 投不进去时试着把主动进程重新拉起来. 返回拉没拉起来.
	//
	// **它会死**: 撞止损线(每进程 200 万 token)、崩溃、被 OOM 杀.
	// 死了之后投递永远失败, 摘要**从此只会摊在终端上** ——
	// 而人不在终端边的时候等于全丢了.
	//
	// 更糟的是它是**永久的**: 回收只在"投递成功够 N 次"之后触发,
	// 而投递已经永远失败, 那条路也走不到. 感知层降级成一个只会往
	// 空气里打印的东西, 而且没有任何一处会说一声.
	//
	// **每份摘要只试一次**: 每次都反复重启等于把一个坏掉的东西
	// 拿去撞墙, 而日志会被刷满 —— 拉不起来就老老实实摊在终端上.
	Respawn func() bool

	// RecycleEvery 每几份摘要换一个主动进程. 0 = 不换
	RecycleEvery int
	Recycle      func()

	accepted int
}

// Route 一份摘要从产出到有人看见, 中间的全部处理.
func (r *DigestRouter) Route(d Digest) {
	// **投递前把坐标换成地名** —— 换成"公司"之后三条判据才有得判
	if r.Places != nil {
		r.Places.Annotate(&d)
	}

	var hits []string
	if r.Watches != nil {
		hits = r.Watches.Hits(d)
	}
	for _, hit := range hits {
		if r.OnWatchHit != nil {
			r.OnWatchHit(hit)
		}
	}

	// **不因为关注说过就闷掉这份摘要**: 主动那条的信息量
	// 比关注那条大(它发现时间戳跟备注对不上). 闷掉是在用一条规矩
	// 换掉一次真正有用的判断. 把事实贴给它, 让它自己判断.
	text := d.Text() + WatchNote(hits)
	if r.Presence != nil {
		// 跟"今天说过什么"一样贴成**环境事实** —— OS 手里有的东西
		// 贴给它就行, 不需要新工具、也不需要它自己去查
		// 能成为环境事实的内容直接贴给它, 不要把已有事实变成记忆并占用上下文
		text += r.Presence.Note()
	}
	if r.Budget != nil {
		// 它今天说过什么由 OS 告诉它 —— 这样它不需要靠自己的上下文
		// 记住, 于是那个上下文可以被丢掉(S22 的回收靠的就是这条)
		text += r.Budget.SaidNote()
		// 破例资格来自这里, 不是来自 agent 的自述
		if d.Reason == DigestUrgent {
			r.Budget.NoteUrgent(d.To)
		}
	}

	sent := r.ToProcess != nil && r.ToProcess(text)
	if !sent && r.Respawn != nil {
		// 投不进去 —— 先试着把它拉起来, **然后把这一份补投进去**:
		// 不补的话那一份就白丢了, 而它可能正是要紧的那一条
		if r.Respawn() && r.ToProcess != nil {
			sent = r.ToProcess(text)
		}
	}
	if sent {
		r.accepted++
		if r.RecycleEvery > 0 && r.accepted >= r.RecycleEvery {
			r.accepted = 0
			if r.Recycle != nil {
				r.Recycle()
			}
		}
		return // 静悄悄地转过去. 说不说由它判断
	}
	if r.ToTerminal != nil {
		r.ToTerminal(d, text)
	}
}

// ObserveChain 每条信号都要走的那几步.
//
// 拎出来是因为它们**必须一起发生**: places 要看一眼(好让用户能说
// "这儿是公司"), 可读视图要写一行(好让用户能问"我今天去过哪儿").
// 漏掉后者的症状是"它明明看见了却答不出来" —— 而那查起来很费劲,
// 因为账本里什么都在.
//
// 总线自己不解释 body —— 这些都是宿主在投递路径上的增强.
func ObserveChain(p *Places, more ...func(abi.Signal)) func(abi.Signal) {
	return func(s abi.Signal) {
		if p != nil {
			p.Observe(s)
		}
		for _, f := range more {
			f(s)
		}
	}
}
