package osinit

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 定时唤醒 —— "11 点提醒我 12 点的火车".
//
// ── 为什么这是内核原语, 不是 agent 自己 sleep ──
//
// 一个 agent 进程可以起个协程睡到 11 点. 但那个闹钟跟进程同生共死:
// 进程被杀、机器重启、对话结束, 闹钟就没了 —— 而**用户不会知道它没了**.
// 他会在 12 点错过火车, 然后才发现"你不是说好提醒我吗".
//
// 静默地不响, 是闹钟最糟的失败方式.
//
// 所以闹钟必须活得比进程长. 这跟 decisions.go 那条是同一条:
// "决策活得比进程的任何一次运行都长". 载体也一样 —— 事件日志.
//
// ── 到点之后走哪条路 ──
//
// **到点 = 产出一条信号**, 然后走感知层已经建好的那一整套
// (穿透 → 常驻进程 → 打扰预算 → 呈现). 不另开一条投递路径, 理由:
//
//	① 一条新的投递路径就是一份新的"它什么时候会打扰我"的规则,
//	   而用户脑子里只该有一套
//	② 那一套已经真机验过了
//
// 但有一处例外, 见 fire(): **用户自己要的提醒不占打扰额度**.
// 预算防的是"它没事找事", 而这件事是他自己让做的.

// Wake 一个闹钟.
type Wake struct {
	ID string `json:"id"`
	// Thread 这个闹钟属于哪一段对话. 到点之后话说回那一段
	Thread string `json:"thread"`
	// At 什么时候响, 毫秒
	At int64 `json:"at"`
	// Text 到点时说什么. **是给用户看的原话**, 不是给模型的指令 ——
	// 中间过一次模型就多一次跑偏的机会, 而这件事没有任何需要判断的地方
	Text string `json:"text"`
	// SetAt 什么时候设的 —— 排查"这个闹钟哪来的"要用
	SetAt int64 `json:"setAt"`

	/**
	 * Every 隔多久再响一次(毫秒). 0 = 只响一次.
	 *
	 *	── 为什么周期是闹钟的一个字段, 不是另一种东西 ──
	 *
	 *	"每天早上七点看一眼今天什么情况" 跟 "11 点提醒我 12 点的火车"
	 *	差别只有一个: 响完之后要不要再排一次. 别的全一样 ——
	 *	同一份持久化、同一条补响规则、同一个 Tick.
	 *
	 *	做成两种东西的话, 补响、撤销、开机装回这三件事各要写两遍,
	 *	而它们迟早会漂(这个仓库已经在设置行上栽过一次).
	 */
	Every int64 `json:"every,omitempty"`

	/**
	 * Think 到点之后起一次**判断**, 而不是直接说 Text.
	 *
	 *	── 这是"闹钟"和"管家"的分界线 ──
	 *
	 *	Text 那条路是对的, 而且必须留着: "12 点的火车"这件事没有
	 *	任何需要判断的地方, 中间过一次模型只多一次跑偏的机会.
	 *
	 *	但管家要的那类不一样 —— "今天几点出门" 取决于今天的日程、
	 *	此刻的路况、你在不在家. 到点时**没有一句现成的话可说**,
	 *	只有一个要现算的判断.
	 *
	 *	所以 Think=true 时 Text 不是给用户看的原话, 而是**给判断者的
	 *	题目**("看一眼今天的通勤"). 说什么由它现场定, 而且照样要过
	 *	打扰预算 —— 到点不等于值得打扰.
	 */
	Think bool `json:"think,omitempty"`
}

// Timers 闹钟登记处. 一台机器一个.
type Timers struct {
	mu      sync.Mutex
	now     func() time.Time
	pending map[string]Wake
	seq     int
	log     *EventLog
	fire    func(w Wake, lateMs int64)
}

func NewTimers(log *EventLog, now func() time.Time, fire func(w Wake, lateMs int64)) *Timers {
	if now == nil {
		now = time.Now
	}
	return &Timers{now: now, pending: map[string]Wake{}, log: log, fire: fire}
}

// Set 设一个闹钟.
// Set 设一个一次性闹钟 —— 老接口, 一个字没改.
func (t *Timers) Set(thread string, at int64, text string) (Wake, error) {
	return t.set(Wake{Thread: thread, At: at, Text: text})
}

/*
SetEvery 设一个周期闹钟.

	every 是毫秒. 到点响完自己排下一次 —— 所以它**活得比进程长**
	这条对周期同样成立: 机器重启之后从事件日志装回来, 接着按周期走.

	think=true 的话到点起一次判断(见 Wake.Think).
*/
func (t *Timers) SetEvery(thread string, at, every int64, text string, think bool) (Wake, error) {
	if every > 0 && every < int64(time.Minute/time.Millisecond) {
		// **一分钟以下的周期一律拒**.
		//
		// 不是嫌它快, 是它必然出事: Tick 是一秒一跳, 而 think 那条
		// 每响一次要起一轮推理. 一个 10 秒的周期 = 一天 8640 轮,
		// 而这台机器上每轮都是真钱.
		//
		// 想要更密的东西, 要的其实不是闹钟而是信号订阅(见 watch.go)
		return Wake{}, fmt.Errorf("周期不能短于一分钟(给的是 %d 毫秒) —— "+
			"更密的东西该用信号订阅, 不该用闹钟", every)
	}
	return t.set(Wake{Thread: thread, At: at, Text: text, Every: every, Think: think})
}

func (t *Timers) set(w Wake) (Wake, error) {
	at, text := w.At, w.Text
	if text == "" {
		return Wake{}, fmt.Errorf("提醒内容是空的 —— 到点了说什么?")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	nowMs := t.now().UnixMilli()
	// **过去的时间点要当场拒**, 不能默默不响.
	//
	// 模型算错时间是常有的事(时区、24 小时制、"明天"). 静默收下一个
	// 过去的时刻, 表现出来就是"闹钟没响", 而那是最糟的失败方式.
	if at <= nowMs {
		return Wake{}, fmt.Errorf(
			"这个时间点已经过去了(%s, 现在是 %s)。要么给一个将来的时刻, "+
				"要么现在就直接说",
			time.UnixMilli(at).Format("01-02 15:04"),
			time.UnixMilli(nowMs).Format("01-02 15:04"))
	}
	t.seq++
	w.ID = fmt.Sprintf("w%d", t.seq)
	w.SetAt = nowMs
	t.pending[w.ID] = w
	t.record(abi.EvWakeSet, w, nil)
	return w, nil
}

// Cancel 撤一个闹钟
func (t *Timers) Cancel(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.pending[id]
	if !ok {
		return false
	}
	delete(t.pending, id)
	t.record(abi.EvWakeCancelled, w, nil)
	return true
}

// Tick 到点的都响掉.
func (t *Timers) Tick() {
	t.mu.Lock()
	nowMs := t.now().UnixMilli()
	var due []Wake
	for id, w := range t.pending {
		if w.At <= nowMs {
			due = append(due, w)
			delete(t.pending, id)
		}
	}
	// 按设定的时刻排 —— 同时到期的几个要按它们本该响的顺序说
	sort.Slice(due, func(i, j int) bool { return due[i].At < due[j].At })
	fire := t.fire
	for _, w := range due {
		t.record(abi.EvWakeFired, w, map[string]any{"lateMs": nowMs - w.At})
		if w.Every <= 0 {
			continue
		}
		/*
			周期的响完自己排下一次.

			**从"本该响的那一刻"往后推, 不是从"现在"往后推**:
			机器睡了三天再开机, 从现在推的话每天七点会漂成
			每天开机时间点; 从 At 推则永远钉在七点.

			而中间错过的那几次只补最近一次(下面的 for 会把
			过期的都推过去) —— 睡三天开机时连响三遍"该出门了"
			是噪音, 而且那三天的判断早就没意义了.
		*/
		next := w
		next.At = w.At
		for next.At <= nowMs {
			next.At += w.Every
		}
		t.pending[next.ID] = next
		t.record(abi.EvWakeSet, next, map[string]any{"repeat": true})
	}
	t.mu.Unlock()

	for _, w := range due {
		if fire != nil {
			fire(w, nowMs-w.At)
		}
	}
}

// Start 让闹钟自己走. 返回停止函数.
//
// 一秒一跳: 提醒的精度要求是"到分钟", 一秒够了, 而且它极便宜
// (一个 map 遍历), 不值得为省这点开销去做最小堆.
func (t *Timers) Start() (stop func()) {
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				t.Tick()
			}
		}
	}()
	return func() { close(done) }
}

// Restore 开机时把没响的闹钟装回来.
//
// ── 错过的闹钟要补响, 不能吞掉 ──
//
// 机器可能关着、可能在升级. 一个 11 点该响的闹钟, 如果 11:30 开机时
// 因为"已经过期"被丢掉, 用户就再也不会知道它存在过 —— 而他当初设它,
// 正是因为怕自己忘.
//
// **晚响比不响强**, 但必须**说清晚了多久** —— 用户要据此判断还来不来得及.
func (t *Timers) Restore(events []abi.Event) {
	t.mu.Lock()
	live := map[string]Wake{}
	for _, e := range events {
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		w := wakeFromPayload(m)
		if w.ID == "" {
			continue
		}
		switch e.Kind {
		case abi.EvWakeSet:
			live[w.ID] = w
			if n := seqOf(w.ID); n > t.seq {
				t.seq = n
			}
		case abi.EvWakeFired, abi.EvWakeCancelled:
			delete(live, w.ID)
		}
	}
	for id, w := range live {
		t.pending[id] = w
	}
	t.mu.Unlock()
	// 过期的立刻走一遍 Tick —— 补响的路径跟正常响完全一样,
	// 只是 lateMs 大. 分成两条路的话, 补响这条永远没人测
	t.Tick()
}

// Pending 还没响的闹钟, 按时间排 —— 用户要能问"你还记着什么"
func (t *Timers) Pending() []Wake {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Wake, 0, len(t.pending))
	for _, w := range t.pending {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func (t *Timers) record(kind abi.EventKind, w Wake, extra map[string]any) {
	if t.log == nil {
		return
	}
	p := map[string]any{
		"id": w.ID, "thread": w.Thread, "at": w.At,
		"text": w.Text, "setAt": w.SetAt,
	}
	// 这两个只在用到时才写进 payload —— 老账本里没有它们,
	// 读回来是零值, 正好等于"一次性 + 说原话"
	if w.Every > 0 {
		p["every"] = w.Every
	}
	if w.Think {
		p["think"] = true
	}
	for k, v := range extra {
		p[k] = v
	}
	t.log.Append(signalPID, kind, p)
}

func wakeFromPayload(m map[string]any) Wake {
	id, _ := m["id"].(string)
	th, _ := m["thread"].(string)
	txt, _ := m["text"].(string)
	think, _ := m["think"].(bool)
	return Wake{ID: id, Thread: th, At: asMillis(m["at"]), Text: txt,
		SetAt: asMillis(m["setAt"]), Every: asMillis(m["every"]), Think: think}
}

// asMillis 过了一趟 JSON 的时间戳是 float64.
//
// 终端渲染和摘要文本也会遇到同样的类型变化 ——
// 凡是从事件日志读回来的数字都要过一道.
func asMillis(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func seqOf(id string) int {
	var n int
	if _, err := fmt.Sscanf(id, "w%d", &n); err != nil {
		return 0
	}
	return n
}
