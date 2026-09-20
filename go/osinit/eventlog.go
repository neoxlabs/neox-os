package osinit

import (
	"sync"

	"github.com/neox-os/neox-os/abi"
)

// EventLog 只追加事件日志.
//
// 设计要点 (这些是跟 agent IDE 的渲染态的本质区别):
//
//  1. **没有特权观察者**. UI / 推送 / IM / 审计都是平等订阅者.
//     日志不知道有没有人在看, 也不因为没人看而改变行为.
//
//  2. **Subscribe 自带补齐**. 订阅者给一个 fromSeq, 先收到历史再接实时流,
//     永远看不到断层 —— 这是"任意时刻接入都能完整重建过程"的实现点.
//
//  3. **每个订阅者一条有序队列**.
//     入队在日志锁内 (保证入队顺序 == seq 顺序), 回调在锁外 (保证不死锁).
//     早先的实现是"锁内拷贝订阅者列表, 锁外直接调回调" —— 单线程下看不出问题,
//     多线程下两个并发 Append 的回调顺序不再等于 seq 顺序.
//     并发 Append 可能让 seq=51 出现在 seq=58 之后.
//     **乱序的事件流是不可重建的**, 所以这条不能将就.
type EventLog struct {
	mu     sync.Mutex
	logs   map[abi.ProcessID][]abi.Event
	perPid map[abi.ProcessID]map[*subscription]struct{}
	global map[*subscription]struct{}
	now    func() int64
	// store 落盘. **同步写, 在锁内** —— 见 WriteThrough
	store eventSink
	// next 每个进程下一条事件的 seq.
	//
	// **不能再用 len(log) 算** —— 内存压紧之后 len 会变小, 于是新事件的
	// seq 跟一条早就存在的撞上. 而 Replay(pid, fromSeq) 是靠 seq 定位的:
	// 订阅方拿着旧 seq 回来续, 会跳过或者重放一批, 而它自己不会知道.
	//
	// 这跟 S25 那个"id 计数器退回去"是同一类错的第二次出现:
	// **凡是从"现存内容"反推出来的计数器, 压紧都会把它打回去.**
	next map[abi.ProcessID]int
	// senseCompacted 上一次压紧之后 sense 桶有多长 —— 再涨
	// senseCompactGrowth 条才值得压下一次. 见 Append 里那段
	senseCompacted int
}

// senseCompactGrowth 压紧之后要再涨这么多条才压下一次.
//
//	压一次要把整桶扫两遍. 过了线就每条都压的话, 写 n 条是 O(n²) ——
//	而装回历史正是"一口气写很多条"的那种场合.
const senseCompactGrowth = 1000

// eventSink 能把一条事件收下去的东西. 只要 EventStore 那一个方法,
// 不把整个类型绑进来 —— 测试里可以塞一个假的
type eventSink interface {
	Append(abi.Event) error
}

// WriteThrough 接上落盘. **同步的, 而且在锁内.**
//
// ── 为什么不能是订阅 ──
//
// 原来的写法是 SubscribeAll + 一个投递协程, 注释写着"落盘要在事件产生的
// 那一刻做, 而不是退出时统一写, 退出时统一写等于崩溃就全丢".
// **但订阅本身就不是那一刻**: Append 只把事件塞进队列就返回了,
// 另一个协程才写文件. 被杀的时候队尾那几条根本没落地 ——
// 而 S23 已经证明被杀是常态(开机扫出 199 个残留 socket).
//
// 丢的恰恰是**用户刚做的那件事**: 刚定的提醒、刚命名的地点、
// 刚取走的攒项. 表现是"我明明刚跟它说过", 而且查不出来 ——
// 账本里没有那条, 看起来就像他从来没说过.
//
// ── 为什么在锁内 ──
//
// 账本的语义就是顺序(重放靠它). 落在锁外的话, 两个协程同时 Append
// 时盘上可能是反的, 而重放出来的现状会跟着反: "设了又撤"变成
// "撤了又设", 那条提醒就复活了.
//
// 代价是每条事件多一次 write(2). 可以接受: 事件是人的动作和工具调用
// 的节奏, 不是高频流(EventStore 自己的注释里就是这么算的).
func (l *EventLog) WriteThrough(s eventSink) {
	l.mu.Lock()
	l.store = s
	l.mu.Unlock()
}

type subscription struct {
	fn func(abi.Event)

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []abi.Event
	closed bool
}

func newSubscription(fn func(abi.Event), initial []abi.Event) *subscription {
	s := &subscription{fn: fn, queue: initial}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// enqueue 必须在日志锁内调用 —— 入队顺序就是最终的投递顺序
func (s *subscription) enqueue(ev abi.Event) {
	s.mu.Lock()
	if !s.closed {
		s.queue = append(s.queue, ev)
		s.cond.Signal()
	}
	s.mu.Unlock()
}

// drain 单一投递协程. 回调**不持任何锁**, 所以订阅者回调里可以再调日志.
func (s *subscription) drain() {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if len(s.queue) == 0 && s.closed {
			s.mu.Unlock()
			return
		}
		batch := s.queue
		s.queue = nil
		s.mu.Unlock()

		for _, ev := range batch {
			s.fn(ev)
		}
	}
}

func (s *subscription) close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func NewEventLog(now func() int64) *EventLog {
	return &EventLog{
		logs:   map[abi.ProcessID][]abi.Event{},
		perPid: map[abi.ProcessID]map[*subscription]struct{}{},
		global: map[*subscription]struct{}{},
		next:   map[abi.ProcessID]int{},
		now:    now,
	}
}

func (l *EventLog) Append(pid abi.ProcessID, kind abi.EventKind, payload any) abi.Event {
	// ── 瞬时事件: 只发给此刻正看着的人 ──
	//
	//	不进内存日志、不占 seq、不落盘. 见 abi.EvProcDelta.
	//
	//	**不占 seq 是要紧的那一半**: 游标是按 seq 走的, 而断线重连的人
	//	是拿着游标回来补的. 让上百条瞬时的东西去推高 seq, 那些游标就会
	//	指向一堆永远补不出来的空号 —— 而客户端只会看到"补齐好像少了点
	//	什么", 查不出为什么.
	if kind == abi.EvProcDelta || kind == abi.EvDeviceAsk || kind == abi.EvDeviceAct {
		l.mu.Lock()
		ev := abi.Event{Seq: l.next[pid], PID: pid, At: l.now(), Kind: kind, Payload: payload}
		for s := range l.perPid[pid] {
			s.enqueue(ev)
		}
		for s := range l.global {
			s.enqueue(ev)
		}
		l.mu.Unlock()
		return ev
	}
	l.mu.Lock()
	log := l.logs[pid]
	ev := abi.Event{Seq: l.next[pid], PID: pid, At: l.now(), Kind: kind, Payload: payload}
	l.next[pid]++
	l.logs[pid] = append(log, ev)
	// **压紧接在产生的那一条路上** —— 长跑里没有别人会来调它.
	// 放在开机时做等于没做: 内存是在**跑的过程中**涨起来的,
	// 而这台机器可以几个月不重启(那正是它该有的样子).
	// **压紧要节流**: 原来是"过了线就每写一条压一遍" —— 压一次是 O(n),
	// 于是写 n 条就是 O(n²). 三万条历史在开机装回时会直接卡死.
	// 涨够一截才压一次, 摊下来是常数.
	if pid == signalPID && len(l.logs[pid]) > compactAfterSenseEvents &&
		len(l.logs[pid]) >= l.senseCompacted+senseCompactGrowth {
		if keep, drop := compactSense(l.logs[pid]); len(drop) > 0 {
			l.logs[pid] = keep
		}
		l.senseCompacted = len(l.logs[pid])
	}
	// **落盘也在锁内** —— 出了这个函数, 这条就已经在文件里了.
	// 错在这里不上报: 盘写不进去不该让内存里的日志也停摆
	// (那会让一块坏盘变成整个系统不响应). 归档/校验那边看得出来.
	//
	// 瞬时事件不落盘 —— 见 abi.EvProcDelta: 一次回复上百段,
	// 而它们加起来的信息量跟最后那条 phase:reply 一模一样
	if l.store != nil {
		_ = l.store.Append(ev)
	}
	// 入队在锁内 —— 这是顺序保证的唯一来源. enqueue 本身不会阻塞.
	for s := range l.perPid[pid] {
		s.enqueue(ev)
	}
	for s := range l.global {
		s.enqueue(ev)
	}
	l.mu.Unlock()
	return ev
}

func (l *EventLog) Replay(pid abi.ProcessID, fromSeq int) []abi.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.replayLocked(pid, fromSeq)
}

func (l *EventLog) replayLocked(pid abi.ProcessID, fromSeq int) []abi.Event {
	log := l.logs[pid]
	if fromSeq <= 0 {
		return append([]abi.Event(nil), log...)
	}
	// **按 seq 找, 不按下标.** 原来是 log[fromSeq:] —— 那等价于假设
	// "seq 就是下标". 压紧之后前面少了一截, 同一个 fromSeq 会切到
	// 完全无关的位置: 订阅方续上来的是一段错位的历史, 而它看不出来
	for i, e := range log {
		if e.Seq >= fromSeq {
			return append([]abi.Event(nil), log[i:]...)
		}
	}
	return nil
}

func (l *EventLog) Length(pid abi.ProcessID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.logs[pid])
}

// Subscribe 订阅某个进程的事件流. 返回退订函数.
//
// 取历史快照和注册订阅在**同一把锁内**完成 —— 于是:
//
//	注册之前的事件 → 在快照里
//	注册之后的事件 → 进队列
//
// 没有中间地带, 所以既不重也不漏, 而且顺序天然正确.
func (l *EventLog) Subscribe(pid abi.ProcessID, fromSeq int, fn func(abi.Event)) func() {
	l.mu.Lock()
	history := l.replayLocked(pid, fromSeq)
	s := newSubscription(fn, history)
	if l.perPid[pid] == nil {
		l.perPid[pid] = map[*subscription]struct{}{}
	}
	l.perPid[pid][s] = struct{}{}
	l.mu.Unlock()

	go s.drain()
	return func() {
		l.mu.Lock()
		if set := l.perPid[pid]; set != nil {
			delete(set, s)
			if len(set) == 0 {
				delete(l.perPid, pid)
			}
		}
		l.mu.Unlock()
		s.close()
	}
}

// SubscribeAll 跨进程订阅 —— 触达层用
func (l *EventLog) SubscribeAll(fn func(abi.Event)) func() {
	l.mu.Lock()
	s := newSubscription(fn, nil)
	l.global[s] = struct{}{}
	l.mu.Unlock()

	go s.drain()
	return func() {
		l.mu.Lock()
		delete(l.global, s)
		l.mu.Unlock()
		s.close()
	}
}

// Forget 把一个进程的历史从**内存里**抹掉.
//
//	注意它只管内存. **单独调它就是假删** —— 账本还在盘上,
//	下次开机 LoadEvents 一装, 那段历史原封不动回来了.
//	这类"clearXxx 只清内存"的假删会导致用户删了、界面上没了、
//	第二天又出现了.
//
//	真删要连账本一起: 见 EventStore.Forget.
func (l *EventLog) Forget(pid abi.ProcessID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.logs, pid)
	delete(l.next, pid)
	for sub := range l.perPid[pid] {
		sub.close()
	}
	delete(l.perPid, pid)
}

// PIDs 日志里出现过的所有进程.
//
//	注意跟 OS.List() 的区别: List 返回**进程表**里还在的进程,
//	这里返回**日志里有过记录**的进程 —— 已经退出、进程表里清掉了,
//	但历史还要能补给刚连上来的观察者.
func (l *EventLog) PIDs() []abi.ProcessID {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]abi.ProcessID, 0, len(l.logs))
	for pid := range l.logs {
		out = append(out, pid)
	}
	return out
}

// Snapshot 整个日志的可序列化快照 —— 落盘用
func (l *EventLog) Snapshot() map[abi.ProcessID][]abi.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[abi.ProcessID][]abi.Event, len(l.logs))
	for pid, log := range l.logs {
		out[pid] = append([]abi.Event(nil), log...)
	}
	return out
}

// Restore 从快照恢复 —— 唤醒被换出的进程时用
func (l *EventLog) Restore(snap map[abi.ProcessID][]abi.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for pid, log := range snap {
		l.logs[pid] = append([]abi.Event(nil), log...)
		// 计数器要跟着装回来 —— 不然重建之后新事件的 seq 从 0 开始,
		// 跟装回来的那些全撞上
		for _, e := range log {
			if e.Seq >= l.next[pid] {
				l.next[pid] = e.Seq + 1
			}
		}
	}
}

// CompactSense 把感知层那一桶按语义压紧 —— **内存这一侧**.
//
// ── 为什么磁盘压了还不够 ──
//
// S25 压的是 EventStore 那个文件, 而 logs 这张 map 从没被压过.
// 2500 条合法信号进来, 宿主 RSS 就会永久涨 6MB.
// 一台跑一年的机器, 内存里躺着的是**全部历史** —— 而其中绝大多数对
// "现在还剩什么"已经没有贡献(响过的闹钟、上上周的摘要).
//
// 判据跟磁盘那边是同一条: **压紧前后重建出来的现状必须一模一样**,
// 允许少读事件, 不允许改变结论. 所以复用同一个 compactSense.
//
// ── 只动 sense, 对话一条不碰 ──
//
// /继续 要的是那段对话的**完整**历史. 从中间砍掉几条的话, 恢复出来
// "看着对但其实不对" —— 那比没有历史更糟(磁盘轮转的红线① 同一条).
// 增长主要来自 sense: 对话是人的节奏, 感知是机器的节奏.
func (l *EventLog) CompactSense() (dropped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	keep, drop := compactSense(l.logs[signalPID])
	if len(drop) == 0 {
		return 0
	}
	l.logs[signalPID] = keep
	return len(drop)
}
