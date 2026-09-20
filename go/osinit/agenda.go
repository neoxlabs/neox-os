package osinit

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 待办和日程 —— **同一个东西, 差一个时间**.
//
// ── 为什么合成一份 ──
//
//	"下周三下午三点开会"和"记得买牛奶"在存储上没有任何区别: 一句话、
//	归谁、什么时候、做没做. 差的只是前者有个确定的时刻.
//
//	分成两个登记簿时, 四个"以后提醒我"的工具会让
//	模型每一轮都要在两个里挑, 而挑错不报错 —— 只是那件事再也找不着.
//
// ── 跟提醒的分工 ──
//
//	remind_me   到点**说一句话**. 说完就没了, 不留痕迹.
//	agenda      **一件待办的事**. 它会一直在, 直到做完或者删掉.
//
//	一件事可以两者都要("三点开会"既进日程也设个 2:45 的提醒),
//	那是两条独立的记录 —— 合起来的话, 划掉待办会顺带撤掉闹钟,
//	而那多半不是他要的.
//
// ── 为什么不是 Notes 的一种 ──
//
//	Notes 是"记住一个事实"(老婆生日 3 月 2 号), 没有状态也不会过期.
//	待办有**状态**(做没做)和**时间**(什么时候), 而这两样正是它有用的
//	全部原因: 界面要按时间排、要把做完的收起来、要把过期没做的挑出来.
//	塞进 Notes 的话每个用的地方都要自己解析一遍字符串.
type Task struct {
	ID string `json:"id"`
	// Who 归谁. 空 = 这台机器公有的事
	Who string `json:"who,omitempty"`
	// What 一句话. 用他的原话
	What string `json:"what"`
	// At 什么时候. 0 = 没定时间(纯待办)
	At int64 `json:"at,omitempty"`
	// Done 做完了. **不删** —— 划掉的东西他还要能看见,
	// 而且"我今天干了什么"要靠它答
	Done bool `json:"done,omitempty"`
	// DoneAt 什么时候划掉的
	DoneAt int64 `json:"doneAt,omitempty"`
	// Where 在哪儿(会议室/地名). 空就是没说
	Where string `json:"where,omitempty"`
	// From 哪儿来的. 空 = 他自己跟 bot 说的; "手机日历" = 扫上来的.
	//
	//	**扫上来的不许在这边划掉**: 那是他日历里的事, 划掉这边的不会
	//	改那边, 而他会以为改了
	From string `json:"from,omitempty"`
	At0  int64  `json:"createdAt,omitempty"`
}

// Agenda 待办和日程.
type Agenda struct {
	mu   sync.Mutex
	all  map[string]Task
	next int
	log  *EventLog
	now  func() time.Time
}

// maxTasks 最多攒多少条没做完的.
//
//	满了报错, **不静默丢**: 悄悄丢掉最旧的那条, 用户会在几个月后发现
//	它忘了一件他明确交代的事, 而中间没有任何一处说过. 跟 Notes 同一条
const maxTasks = 500

func NewAgenda(log *EventLog) *Agenda {
	return &Agenda{all: map[string]Task{}, log: log, now: time.Now}
}

// Add 记一件事. at=0 表示没定时间.
func (a *Agenda) Add(who, what string, at int64, where string) (Task, error) {
	what = strings.TrimSpace(what)
	if what == "" {
		return Task{}, fmt.Errorf("没说要记什么事")
	}
	if len([]rune(what)) > 200 {
		return Task{}, fmt.Errorf("太长了, 一条最多 200 字 —— 挑要紧的那句")
	}
	// **一年以后多半是算错了**: 判错的代价不对称 —— 拦下来最多让它
	// 重算一次, 放过去是一件永远排在最后、永远看不见的事
	if at != 0 {
		nowMs := a.now().UnixMilli()
		if at > nowMs+366*24*3600*1000 || at < nowMs-366*24*3600*1000 {
			return Task{}, fmt.Errorf(
				"%s 离现在超过一年, 多半是年份或单位算错了。现在是 %s",
				time.UnixMilli(at).Format("2006-01-02 15:04"),
				a.now().Format("2006-01-02 15:04"))
		}
	}
	a.mu.Lock()
	open := 0
	for _, t := range a.all {
		if !t.Done {
			open++
		}
	}
	if open >= maxTasks {
		a.mu.Unlock()
		return Task{}, fmt.Errorf("排不下了(已经 %d 件没做完)。先划掉几件", maxTasks)
	}
	a.next++
	t := Task{ID: fmt.Sprintf("t%d", a.next), Who: who, What: what, At: at,
		Where: strings.TrimSpace(where), At0: a.now().UnixMilli()}
	a.all[t.ID] = t
	a.mu.Unlock()
	a.append(abi.EvTaskAdded, map[string]any{
		"id": t.ID, "who": who, "what": what, "at": at, "where": t.Where})
	return t, nil
}

// Done 划掉一件. **不删** —— 划掉的他还要能看见
func (a *Agenda) Done(id string) (Task, bool) {
	id = strings.TrimSpace(id)
	a.mu.Lock()
	t, ok := a.all[id]
	if ok && !t.Done {
		t.Done, t.DoneAt = true, a.now().UnixMilli()
		a.all[id] = t
	}
	a.mu.Unlock()
	if !ok {
		return Task{}, false
	}
	a.append(abi.EvTaskDone, map[string]any{"id": id})
	return t, true
}

// Drop 扔掉一件 —— 记错了、不做了
func (a *Agenda) Drop(id string) bool {
	id = strings.TrimSpace(id)
	a.mu.Lock()
	_, ok := a.all[id]
	delete(a.all, id)
	a.mu.Unlock()
	if ok {
		a.append(abi.EvTaskDropped, map[string]any{"id": id})
	}
	return ok
}

// List 某个人的加公有的. done=false 只给没做完的
func (a *Agenda) List(who string, withDone bool) []Task {
	a.mu.Lock()
	out := make([]Task, 0, len(a.all))
	for _, t := range a.all {
		if t.Who != "" && t.Who != who {
			continue
		}
		if t.Done && !withDone {
			continue
		}
		out = append(out, t)
	}
	a.mu.Unlock()
	// 定了时间的按时间排在前面, 没定时间的按记的顺序排在后面 ——
	// **他要的是"接下来干什么"**, 而没有时间的那些回答不了这个问题
	sort.Slice(out, func(i, j int) bool {
		if (out[i].At == 0) != (out[j].At == 0) {
			return out[i].At != 0
		}
		if out[i].At != out[j].At {
			return out[i].At < out[j].At
		}
		return out[i].At0 < out[j].At0
	})
	return out
}

// Text 摊平成给模型看的几行. 空 = 一件都没有, 那就别提这件事
func (a *Agenda) Text(who string) string {
	list := a.List(who, false)
	if len(list) == 0 {
		return ""
	}
	now := a.now()
	var b strings.Builder
	for _, t := range list {
		b.WriteString("- [")
		b.WriteString(t.ID)
		b.WriteString("] ")
		if t.At != 0 {
			at := time.UnixMilli(t.At)
			b.WriteString(at.Format("01-02 15:04"))
			// **过期的要标出来**: 一件昨天该做的事混在列表里,
			// 模型会当成还没到点的
			if at.Before(now) {
				b.WriteString("(已过)")
			}
			b.WriteString(" ")
		}
		b.WriteString(t.What)
		if t.Where != "" {
			b.WriteString(" @" + t.Where)
		}
		// **来源要标出来**: 手机日历那些他划不掉(那是他日历里的事),
		// 而模型不知道的话会回一句"划掉了"
		if t.From != "" {
			b.WriteString("（" + t.From + "）")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Observe 顺路收一条手机日历上的日程.
//
// ── 为什么日历事件不当普通信号 ──
//
//	当普通信号的话它只会进世界模型的"最新一条", 而日历的价值恰恰在
//	**一批**: "这周还有什么""明天几点出门要避开哪个会". 一条盖一条,
//	那批东西一件都留不下.
//
//	所以它进的是同一份待办表 —— 见文件头: 待办和日程本来就是一个东西。
//
// ── 手机上的和他自己说的, 分开记 ──
//
//	From="手机日历" 的那些**不许他在这边划掉**: 那是他日历里的事,
//	划掉这边的不会改那边, 而他会以为改了。它们随着下一次扫描更新,
//	那边删了这边跟着没。
//
//	他自己跟 bot 说的那些照旧, 两边互不干扰。
func (a *Agenda) Observe(s abi.Signal) {
	if s.Kind != "calendar.event" {
		return
	}
	id, _ := s.Body["id"].(float64)
	what, _ := s.Body["what"].(string)
	what = strings.TrimSpace(what)
	if what == "" {
		return
	}
	at := int64Of(s.Body["at"])
	where, _ := s.Body["where"].(string)
	// **id 带上来源前缀**: 手机日历的 id 是它自己的自增数,
	// 跟这边的 t1/t2 撞上的话, 划掉一件会划掉另一件
	key := fmt.Sprintf("cal%d", int64(id))
	a.mu.Lock()
	old, had := a.all[key]
	// 已经划掉的不复活 —— 他划掉是有意的, 下一次扫描不该把它推回来
	if had && old.Done {
		a.mu.Unlock()
		return
	}
	same := had && old.What == what && old.At == at && old.Where == where
	a.all[key] = Task{ID: key, What: what, At: at, Where: strings.TrimSpace(where),
		From: "手机日历", At0: a.now().UnixMilli()}
	a.mu.Unlock()
	// **没变就不落账**: 每 8 分钟扫一遍, 全落的话一天几百条
	if !same {
		a.append(abi.EvTaskAdded, map[string]any{
			"id": key, "what": what, "at": at, "where": where, "from": "手机日历"})
	}
}

// Restore 开机装回来.
//
//	**丢了最让人恼火**: 他交代过的事没了, 而他不会想到要再说一遍 ——
//	他会以为它记着, 直到某天发现它不记着. 跟 Notes 同一条
func (a *Agenda) Restore(events []abi.Event) {
	for _, e := range events {
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		switch e.Kind {
		case abi.EvTaskAdded:
			who, _ := m["who"].(string)
			what, _ := m["what"].(string)
			where, _ := m["where"].(string)
			from, _ := m["from"].(string)
			at := int64Of(m["at"])
			if what == "" {
				continue
			}
			a.mu.Lock()
			a.all[id] = Task{ID: id, Who: who, What: what, At: at,
				Where: where, From: from, At0: e.At}
			// 编号接着往下走 —— 不接的话下一件会顶掉一件旧的
			var n int
			if _, err := fmt.Sscanf(id, "t%d", &n); err == nil && n > a.next {
				a.next = n
			}
			a.mu.Unlock()
		case abi.EvTaskDone:
			a.mu.Lock()
			if t, had := a.all[id]; had {
				t.Done, t.DoneAt = true, e.At
				a.all[id] = t
			}
			a.mu.Unlock()
		case abi.EvTaskDropped:
			a.mu.Lock()
			delete(a.all, id)
			a.mu.Unlock()
		}
	}
}

func (a *Agenda) append(kind abi.EventKind, payload map[string]any) {
	if a.log != nil {
		a.log.Append(signalPID, kind, payload)
	}
}
