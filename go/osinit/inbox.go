package osinit

import (
	"sync"

	"github.com/neox-os/neox-os/abi"
)

// 收件箱 —— 让一个进程能"待命等下一句话".
//
// 这是"一次对话 = 一个活着的进程"的实现基础. 跟每句话起一个新进程比:
//
//	多轮记忆   历史就在它自己的地址空间里, 不用来回搬
//	前缀缓存   跨轮保持 —— 新进程每次都要重建缓存, 这是决定性差别
//	资源       等待期间转 waiting, 不占计算
//
// 语义跟决策登记处刻意保持一致 (都是"进程停下来等外面的人"):
//   - 等待期间进程是 waiting, 不是 running
//   - 可以等很久, 没有传输层超时
//   - **关闭是显式的** —— 不是靠超时猜"大概没人说话了"
type inbox struct {
	mu     sync.Mutex
	queue  []abi.RecvResult
	waiter chan abi.RecvResult
	closed bool
}

func newInbox() *inbox { return &inbox{} }

// push 投一句话进去.
//
// 已经有人在等 → 直接交给他; 没人等 → 排队.
// **排队是必须的**: 用户可能在进程忙着干活的时候又说了一句,
// 丢掉的话他会以为自己说了但系统没听见.
func (b *inbox) push(msg abi.RecvResult) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	if b.waiter != nil {
		w := b.waiter
		b.waiter = nil
		b.mu.Unlock()
		w <- msg
		b.mu.Lock()
		return true
	}
	b.queue = append(b.queue, msg)
	return true
}

// take 取一句. 队列空就返回一个等待通道.
//
// 返回通道而不是直接阻塞, 是为了让调用方能同时 select 进程的退出信号 ——
// 否则关机时会被一个永远不来的输入钉住.
func (b *inbox) take() (abi.RecvResult, <-chan abi.RecvResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return abi.RecvResult{Closed: true}, nil, true
	}
	if len(b.queue) > 0 {
		msg := b.queue[0]
		b.queue = b.queue[1:]
		return msg, nil, true
	}
	ch := make(chan abi.RecvResult, 1)
	b.waiter = ch
	return abi.RecvResult{}, ch, false
}

// tryTake 看一眼有没有话, **不留等待通道**.
//
// 不能用 take() 代替: 队列空时它会把自己登记成 waiter, 而调用方转身
// 就走的话, 之后 push 进来的那句话就送进了没人收的通道 —— **消息静默丢掉**.
// 用户中途插的那句话正是最不能丢的.
func (b *inbox) tryTake() (abi.RecvResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return abi.RecvResult{Closed: true}, true
	}
	if len(b.queue) > 0 {
		msg := b.queue[0]
		b.queue = b.queue[1:]
		return msg, true
	}
	return abi.RecvResult{}, false
}

// close 显式关闭. 正在等的人立刻收到 Closed.
func (b *inbox) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	w := b.waiter
	b.waiter = nil
	b.mu.Unlock()
	if w != nil {
		w <- abi.RecvResult{Closed: true}
	}
}

// ── OS 侧 API ───────────────────────────────────────────────

// Delivery 一次投递.
//
// ── 为什么"送进去的"和"他说的"要分开 ──
//
//	群里没点名时, 同一句话要投给屋里每个进程, 而且每份都带一段
//	**给模型看的**上下文(它没听到的那几句)属于信封, 不是用户原话。
//
//	不分开会错两次:
//	  1. 账本里记的是信封 —— 一句话在界面上变成一大坨转述;
//	  2. **判纠正的判据看错了对象** —— 信封里裹着别人说过的"不对…",
//	     于是普通输入也会被记成一次纠正。上下文里的"不对…"会被误算到用户头上。
//
//	ID 让"同一句话投给 N 个人"这件事在账本里认得出来:
//	不给身份就只能靠文本和时间去猜, 而那两样在群聊里恰好都不可靠。
type Delivery struct {
	// Text 真正送进进程的内容(可能带上下文信封)
	Text string
	// Said 用户**原话**. 空表示跟 Text 一样
	Said string
	// From 谁说的
	From string
	// ID 同一句话的多份投递共用一个 —— 界面据此只显示一次
	ID string
	// Relay 这一轮由同屋的 bot 交办, 不是用户输入 —— 见 abi.RecvResult.Relay
	Relay bool
	// Quiet 机器自己叫醒的, 没有人在等回话 —— 见 abi.RecvResult.Quiet
	Quiet bool
	// Voice 他在用耳朵听 —— 见 abi.RecvResult.Voice
	Voice bool
	// Images 跟这句话一起来的图.
	//
	//	**账本里只记 id 和名字, 不记内容**: 图本身是磁盘上的文件,
	//	账本是要整段重放的一行行文字. 见 blobs.go
	Images []blobRef
	// Files 跟这句话一起来的文件(非图). 账本里只记名字 ——
	// 界面拿它画一枚"附件"标签, 字节只有 bot 会去读
	Files []blobRef
}

// Send 给某个进程投一句用户输入.
//
// 返回 false 表示进程不在或收件箱已关 —— 调用方要如实告诉用户,
// 不能静默吞掉 (那会让他以为说过的话被听见了).
func (o *OS) Send(pid abi.ProcessID, text, from string) bool {
	return o.Deliver(pid, Delivery{Text: text, From: from})
}

// Deliver 投一次, 并如实记下用户到底说了什么.
func (o *OS) Deliver(pid abi.ProcessID, in Delivery) bool {
	text, from := in.Text, in.From
	said := in.Said
	if said == "" {
		said = text
	}
	o.mu.Lock()
	rec, ok := o.procs[pid]
	if !ok || rec.info.State.IsTerminal() {
		o.mu.Unlock()
		return false
	}
	if rec.inbox == nil {
		rec.inbox = newInbox()
	}
	box := rec.inbox
	o.mu.Unlock()

	if !box.push(abi.RecvResult{Text: text, From: from, Relay: in.Relay,
		Quiet: in.Quiet, Voice: in.Voice}) {
		return false
	}
	// 账本里记**原话**, 不记信封
	recv := map[string]any{"text": said, "from": from}
	if in.ID != "" {
		recv["utterance"] = in.ID
	}
	// 交办要标出来: 不标的话界面只能按"收到的话"画 —— 于是同屋 bot
	// 不标记的话, 同屋 bot 交代的活会被界面误画成**用户发出的**,
	// 界面就会出现一条来源错误的消息.
	if in.Relay {
		recv["relay"] = true
	}
	// **界面据此不画这一行**: 定时任务的题目长得跟用户打的字一模一样,
	// 而它不是用户打的
	if in.Quiet {
		recv["quiet"] = true
	}
	// 界面按这几个 id 去 /blob 取图 —— 一句话配几张图, 那是同一条消息
	if len(in.Images) > 0 {
		shots := make([]map[string]any, 0, len(in.Images))
		for _, ref := range in.Images {
			shots = append(shots, map[string]any{"id": ref.ID, "name": ref.Name, "mime": ref.Mime})
		}
		recv["images"] = shots
	}
	// 文件只记名字: 界面不取字节(读出口只放媒体), 一枚标签就够了
	if len(in.Files) > 0 {
		files := make([]map[string]any, 0, len(in.Files))
		for _, ref := range in.Files {
			files = append(files, map[string]any{"name": ref.Name})
		}
		recv["files"] = files
	}
	o.events.Append(pid, abi.EvInputRecv, recv)
	// 判纠正也看原话 —— 看信封会把别人说的"不对"算到用户头上
	if IsCorrection(said) {
		// 单独记. 流水里这句也在 (input.recv), 但流水不会说"这是纠正".
		o.events.Append(pid, abi.EvCorrection, map[string]any{"text": said, "from": from})
	}
	// **投递不改状态.**
	//
	// 谁阻塞谁负责状态: 进程可能停在 Recv 上等输入, 也可能停在 Decide 上
	// 等决策 —— 从外面看不出来. 投递一句话就把状态改成 running,
	// 是在猜它停在哪儿, 而且猜错了会**骗人**.
	//
	// 典型失败情况是: agent 请求审批后转 waiting, 随后的五句话每句都把
	// 状态改回 running, 而它其实还卡在等决策 ——
	// 那五句全进了收件箱没人取, 界面上却显示进程在跑.
	// 一次没人回答的审批, 让整个对话永久卡死且看不出来.
	//
	// Recv 自己会在拿到输入时改回 running, 那才是唯一知道真相的地方.
	return true
}

// CloseInbox 告诉进程别再等了 —— 对话结束
// **收件箱还没建出来也要关.**
//
// 这里原来是 `if rec.inbox != nil` 就关, 否则什么都不做. 而收件箱是
// **懒创建**的: 第一句话走的是 spawn 时带的 NEOX_TASK, 根本不过收件箱,
// 所以进程收到第二句话之前, 它压根不存在 —— 那段时间里"关掉这段对话"
// 是句空话, 而且**一声不吭**. 之后进程调 Recv, Recv 自己又建了一个
// 新的、开着的箱子, 于是它接着等下去.
//
// 典型情况是: 批准出网之后关掉当前进程、准备用扩权后的能力重开,
// 关没关上; 进程停在 waiting 永远不结束, 收尾事件也就永远不来,
// "批准之后自己接着干"整条链子断在这儿. 顺着往下看还有三处同病 ——
// `/退` 说"走了"、`/新` 说"之前的上下文不再带着"、`/继续` 说切走了,
// 在这个窗口里全是假话, 老进程原地泄漏.
//
// 关闭是**进程的一个属性**, 不是某个对象的字段. 箱子还没有就先建一个
// 再关, 让后来的 Recv 拿到的是同一个已关的箱子.
func (o *OS) CloseInbox(pid abi.ProcessID) {
	o.mu.Lock()
	rec, ok := o.procs[pid]
	if !ok {
		o.mu.Unlock()
		return
	}
	if rec.inbox == nil {
		rec.inbox = newInbox()
	}
	box := rec.inbox
	o.mu.Unlock()
	box.close()
}
