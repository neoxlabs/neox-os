package osinit

import (
	"fmt"
	"strings"
	"sync"

	"github.com/neox-os/neox-os/abi"
)

/*
主动消息的**唯一出口**.

	── 为什么要有这一层 ──

	"该让用户知道的事"现在有三个来源, 各走各的路:

		remind_me 到点     proc.output(贴在 bot 的事件流上)
		主动判断要说       interrupt.verdict
		日报               daily.report

	三种形状, 于是每一端都要认三种 —— 而**加第四种时必然漏掉某一端**.
	只要某个订阅方漏掉其中一种来源, remind_me 到点时就会出现账本里有、
	桌面界面上有, 而手机端完全收不到的情况. 一个收不到的闹钟等于没有闹钟。

	现在三条都汇进 Delivery: 订阅方只认一种形状, 以后加第五种来源,
	手机端一行都不用改。

	── 它不替代账本 ──

	interrupt.verdict 那几条照旧记 —— 它们记的是"发生了什么",
	包括**没送出去的那些**(defer/duplicate)。Delivery 只记
	"这条要送到人跟前"。两者的条数天然不等, 而那正是它们分开的理由。
*/

// Deliver 一条要送到人跟前的东西.
type Deliver struct {
	ID string `json:"id"`

	/*
		From 哪个 bot 说的. **可以是空**.

			闹钟和 bot 自己说的话有主人; 而感知层判出来的那些
			(interrupt.verdict / 日报)是**主动进程**判的, 它不属于
			任何一个 bot。

			界面不能假设每条都有一张脸 —— 但那一列的位置要照留,
			否则有脸的和没脸的行左边缘会参差。
	*/
	From string `json:"from,omitempty"`

	// Kind 哪一类. 界面据此分通知渠道 —— **闹钟和主动必须分开**:
	// 闹钟是用户要的, 主动是它想说的, 他可能想关掉后者而留着前者
	Kind DeliverKind `json:"kind"`

	Text string `json:"text"`

	/*
		Why 凭哪条判据说的. 只有主动那类有.

			**必须带上**: 一次打扰值不值得, 只有看见它凭什么说才判得出来;
			判不出来的话用户唯一能做的就是把整个通道关掉。
	*/
	Why string `json:"why,omitempty"`

	/*
		Urgent 要不要现在就吵醒他.

			true  = 出声/振动. 用户自己设的闹钟、破例的主动消息
			false = 安静地摆在那儿. 日报、普通主动消息

			**这个判断在 OS 这一侧做**: 它手上有打扰预算和判据,
			而手机端只知道"来了一条"。让手机去猜的话, 每一端猜的
			规则都不一样。
	*/
	Urgent bool `json:"urgent"`

	/*
		To 给谁的. 空 = 屋里所有人.

			一台家用的 OS 上不止一个人。她的"到家提醒"在他手机上响一次,
			他就会把整个通道关掉 —— 而那一关, 真正要紧的那次也到不了他。

			**这不是保密**(见 people.go): 谁拿到 token 谁就看得见全部。
			它是"别推给不相干的人"。
	*/
	To string `json:"to,omitempty"`
}

type DeliverKind string

const (
	// DeliverRemind 用户自己设的闹钟到点了. **不占打扰额度, 一定要送到**
	DeliverRemind DeliverKind = "remind"
	// DeliverProactive 它判断出值得说的事(已经过了打扰预算那道闸)
	DeliverProactive DeliverKind = "proactive"
	// DeliverDaily 日报 —— 可预期的打扰不消耗信任, 所以安静地送
	DeliverDaily DeliverKind = "daily"
	// DeliverNeedsYou 它卡住了, 只有你能让它继续
	DeliverNeedsYou DeliverKind = "needs_you"
)

// Deliveries 投递登记处. 一台机器一个.
type Deliveries struct {
	mu  sync.Mutex
	seq int
	log *EventLog
}

func NewDeliveries(log *EventLog) *Deliveries { return &Deliveries{log: log} }

/*
Post 送一条.

	**空文本一律丢掉**: 一条没有内容的通知在手机上就是一个空气泡,
	用户点进来什么都没有 —— 而那比不通知更让人不安。
*/
func (d *Deliveries) Post(n Deliver) {
	if d == nil || d.log == nil || strings.TrimSpace(n.Text) == "" {
		return
	}
	d.mu.Lock()
	d.seq++
	n.ID = fmt.Sprintf("d%d", d.seq)
	d.mu.Unlock()

	p := map[string]any{
		"id": n.ID, "kind": string(n.Kind), "text": n.Text, "urgent": n.Urgent,
	}
	// from 和 why 只在有的时候写 —— 空字符串挤在 payload 里,
	// 订阅方那边要多判一次"这是空的还是没给"
	if n.From != "" {
		p["from"] = n.From
	}
	if n.Why != "" {
		p["why"] = n.Why
	}
	if n.To != "" {
		p["to"] = n.To
	}
	d.log.Append(signalPID, abi.EvDelivery, p)
}
