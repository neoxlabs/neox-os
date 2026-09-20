package osinit

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 日报 —— 攒下来的东西的唯一自动出口.
//
// ── 为什么没有它, "延后"就等于"丢掉" ──
//
// 打扰预算说的是"额度用完就攒着, 等你问、或者进日报". 前半句已经做了
// (`/攒` 能看), 但**要用户自己想起来去问**的东西, 跟丢掉的区别很小 ——
// 他不知道有东西攒着, 也就永远不会问.
//
// 所以攒下的必须有一条不需要用户主动的出口. 那条出口就是日报.
//
// ── 日报本身不能变成第二个打扰通道 ──
//
// 一天一次、时间固定, 于是它是**可预期的**. 可预期的打扰不消耗信任 ——
// 这跟"它突然冒出来说一句"是完全不同的两件事, 也是它可以不占
// 打扰额度的理由.
//
// 三条规矩, 每条对应一种把日报做坏的方式:
//
//	空的不发     "今天没什么要说的"这句话本身就是打扰
//	一天只发一次  否则它就退化成了一个慢一点的通知流
//	发过就清空    说过一次不该再说第二次

// DailyReport 每天把攒下的东西发一次.
type DailyReport struct {
	// deliver 投递总线 —— 日报也要能到手机上(见 notice.go).
	// 可以是 nil
	deliver *Deliveries

	mu sync.Mutex
	// Hour 几点发. 傍晚而不是清晨: 攒下的都是"今天"的事,
	// 清晨发的话它们已经隔夜了, 而用户对隔夜的事能做的更少
	hour   int
	now    func() time.Time
	budget *InterruptBudget
	send   func(text string)
	log    *EventLog
	// lastDay 上一次发日报是哪一天. 跨天才重新武装
	lastDay string
}

// UseDeliveries 接上投递总线 —— 理由同 InterruptBudget.UseDeliveries
func (d *DailyReport) UseDeliveries(x *Deliveries) { d.deliver = x }

func NewDailyReport(log *EventLog, b *InterruptBudget, hour int,
	now func() time.Time, send func(string)) *DailyReport {
	if now == nil {
		now = time.Now
	}
	if hour < 0 || hour > 23 {
		hour = 21
	}
	return &DailyReport{hour: hour, now: now, budget: b, send: send, log: log}
}

// Tick 到点了就发.
func (d *DailyReport) Tick() {
	d.mu.Lock()
	t := d.now()
	today := t.Format("2006-01-02")
	if today == d.lastDay || t.Hour() < d.hour {
		d.mu.Unlock()
		return
	}
	// 先占住今天, 再取内容 —— 反过来的话, 取完到写标记之间
	// 又跑一次 Tick 就会发两遍
	d.lastDay = today
	d.mu.Unlock()

	held := d.budget.Deferred()
	used, quota, _ := d.budget.Stats()

	if len(held) == 0 {
		// **空的不发, 但要记账.**
		//
		// "今天没什么要说的"这句话本身就是打扰. 而不记账的话,
		// "今天真的很安静"和"日报坏了"两种情况长得一模一样 ——
		// 跟打扰预算那边的 Stats 是同一个道理.
		d.record(0, used, quota, "今天没有攒下的, 不发", "")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "今天攒下了 %d 件没跟你说的事(当时额度已经用完, 或者不够格打断你):\n",
		len(held))
	for _, n := range held {
		fmt.Fprintf(&b, "  · %s\n", n.Text)
	}
	fmt.Fprintf(&b, "(今天主动打扰过你 %d/%d 次)", used, quota)

	d.record(len(held), used, quota, "", b.String())
	if d.deliver != nil {
		// 日报**不 urgent**: 一天一次、时间固定, 所以它是可预期的,
		// 而可预期的打扰不消耗信任 —— 那正是它可以不占额度的理由,
		// 也是它不该出声的理由
		d.deliver.Post(Deliver{Kind: DeliverDaily, Text: b.String()})
	}
	if d.send != nil {
		d.send(b.String())
	}
}

// Start 让它自己走. 一分钟一跳 —— 日报的精度要求是"到小时"
func (d *DailyReport) Start() (stop func()) {
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(time.Minute)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				d.Tick()
			}
		}
	}()
	return func() { close(done) }
}

// Restore 开机时读回"今天发过没有".
//
// ── 为什么这个状态必须落盘 ──
//
// 不落的话, 每次重启都会重新武装 —— 一天重启五次就发五份日报,
// 而日报的全部价值在于"一天一次、可预期". 发五次的日报比不发更糟:
// 它从"可预期的摘要"变成了"随机的打扰".
//
// 反过来, 机器整天没开、到晚上才开机的话, 这里读到的是"今天没发过",
// 于是开机后第一次 Tick 就补发 —— 跟错过的闹钟同一条原则:
// **晚发比不发强**.
func (d *DailyReport) Restore(events []abi.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range events {
		if e.Kind != abi.EvDailyReport {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		if day, _ := m["day"].(string); day != "" {
			d.lastDay = day
		}
	}
}

// record 记一次日报. **正文也要记.**
//
// 原来只记了 items/used/quota —— "发过了"记着, **说了什么没记**.
// 后果:
//
//	· "昨天那份小结讲了啥"事后查不到, 而它是唯一不需要用户主动的出口
//	· 人不在电脑前的时候那份小结等于没发, 而账本里写着"发过了",
//	  于是第二天也不会重发 —— 看起来一切正常
//
// 记进账本之后, 它就跟别的事件一样能被订阅、能被翻出来 ——
// 那条"UI 只是订阅者之一, 没有特权客户端"在这儿才成立.
func (d *DailyReport) record(n, used, quota int, note, text string) {
	if d.log == nil {
		return
	}
	d.log.Append(signalPID, abi.EvDailyReport, map[string]any{
		"day": d.now().Format("2006-01-02"), "items": n,
		"used": used, "quota": quota, "note": note,
		// 空的那次不记正文 —— 它本来就没说话
		"text": text,
	})
}
