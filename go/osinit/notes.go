package osinit

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 它记住的事 —— **一个不限种类的记忆**.
//
// ── 为什么非有这一层不可 ──
//
//	在它之前, 每一种"记住的东西"都是一个手写的登记簿:
//
//	  地点 place.named/forgotten     人 person.known/forgotten
//	  设备 device.declared/forgotten 规则 rule.set/removed
//	  关注 watch.added/removed
//
//	每个都是一份 Add/Forget/Restore/List 加一条 HTTP 路由, 五份长得
//	几乎一样. 后果不是"代码重复"这种审美问题, 是**"记一下我老婆生日"
//	这种最普通的要求一件都办不到** —— 而办不到的原因跟这件事本身
//	没有关系, 只是因为没人为它写过第六个登记簿.
//
//	用户问的就是这个: "以后还有可能记其他东西呢".
//
// ── 为什么不把那五个也合进来 ──
//
//	它们各自带着**索引和语义**: 地点要按距离反查(在不在这儿)、规则要
//	按条件触发、设备要按能力挑投递通道. 那些不是"存一条字符串"能
//	替代的 —— 硬合的结果是每个用的地方都要自己解析一遍 JSON,
//	而语义就散到各处去了.
//
//	所以分工是: **有索引的留在自己的登记簿, 剩下的全部走这儿**.
//	这一层刻意什么索引都没有 —— 一个键、一句话、归谁.
//
// ── 为什么它不参与任何判断 ──
//
//	它只在两个地方露面: 新进程启动时进提示词尾段(所以下次开机它还
//	知道), 和 what_now 里. **不进规则、不触发通知** —— 一条自由文本
//	没有可判定的结构, 拿它去判断只能靠模型猜, 而猜错的通知比不通知糟.
type Note struct {
	// Who 归谁. 空 = 这台机器公有的事
	Who string `json:"who,omitempty"`
	// Key 这条叫什么 —— 用户自己的说法("老婆生日"/"车牌"/"不喝咖啡").
	//
	//	**用它当唯一标识**: 同一个键再记一次就是改, 不是多一条.
	//	不然"我不喝咖啡"说三遍就攒出三条一样的话
	Key string `json:"key"`
	// Text 记住的内容. 一句话
	Text string `json:"text"`
	At   int64  `json:"at,omitempty"`
}

// Notes 记忆本.
type Notes struct {
	mu  sync.Mutex
	all map[noteKey]Note
	log *EventLog
	now func() int64
}

type noteKey struct{ who, key string }

const (
	// maxNoteText 一条最长多少字. 超了当场拒 ——
	// 截断的话用户以为记住了整句, 而它记住的是半句
	maxNoteText = 200
	// maxNotes 最多记多少条.
	//
	//	**满了报错, 不静默丢**: 悄悄丢掉最旧的那条, 用户会在几个月后
	//	发现它忘了一件他明确让它记住的事, 而中间没有任何一处说过.
	maxNotes = 300
)

func NewNotes(log *EventLog) *Notes {
	return &Notes{all: map[noteKey]Note{}, log: log}
}

// Set 记一条. 同键覆盖.
func (n *Notes) Set(who, key, text string) (Note, error) {
	key = strings.TrimSpace(key)
	text = strings.TrimSpace(text)
	if key == "" {
		return Note{}, fmt.Errorf("没说这条叫什么")
	}
	if text == "" {
		return Note{}, fmt.Errorf("没说要记什么")
	}
	if len([]rune(text)) > maxNoteText {
		return Note{}, fmt.Errorf("太长了(%d 字), 一条记忆最多 %d 字 —— 挑要紧的那句记",
			len([]rune(text)), maxNoteText)
	}
	k := noteKey{who: who, key: key}
	note := Note{Who: who, Key: key, Text: text, At: n.stamp()}
	n.mu.Lock()
	if _, had := n.all[k]; !had && len(n.all) >= maxNotes {
		n.mu.Unlock()
		return Note{}, fmt.Errorf("记不下了(已经 %d 条)。先忘掉几条用不上的", maxNotes)
	}
	same := false
	if old, had := n.all[k]; had && old.Text == text {
		same = true
	}
	n.all[k] = note
	n.mu.Unlock()
	// 一模一样的内容不再落一条 —— 同一句话说三遍不该让账本长三条.
	// 跟 People.Know 同一条规矩
	if !same && n.log != nil {
		n.log.Append(signalPID, abi.EvNoteSet, map[string]any{
			"who": who, "key": key, "text": text})
	}
	return note, nil
}

// Get 查一条.
func (n *Notes) Get(who, key string) (Note, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	note, ok := n.all[noteKey{who: who, key: strings.TrimSpace(key)}]
	return note, ok
}

// Forget 忘掉一条. 落一条新事件, 不抹旧的 —— 账本只增不删
func (n *Notes) Forget(who, key string) bool {
	key = strings.TrimSpace(key)
	k := noteKey{who: who, key: key}
	n.mu.Lock()
	_, found := n.all[k]
	delete(n.all, k)
	n.mu.Unlock()
	if found && n.log != nil {
		n.log.Append(signalPID, abi.EvNoteForgot, map[string]any{
			"who": who, "key": key})
	}
	return found
}

// List 某个人的, 加上公有的.
//
//	**公有的也给**: 一条"家里的 wifi 密码"没有归属, 但对谁都有用.
//	按人过滤到一个不剩的话, 用户会觉得它把话忘了
func (n *Notes) List(who string) []Note {
	n.mu.Lock()
	out := make([]Note, 0, len(n.all))
	for k, note := range n.all {
		if k.who != "" && k.who != who {
			continue
		}
		out = append(out, note)
	}
	n.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Who < out[j].Who
	})
	return out
}

// All 全部, 不按人过滤 —— 给界面上"它记着什么"那一页用
func (n *Notes) All() []Note {
	n.mu.Lock()
	out := make([]Note, 0, len(n.all))
	for _, note := range n.all {
		out = append(out, note)
	}
	n.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Text 摊平成给模型看的几行. 空 = 什么都没记过, 那就别提这件事
func (n *Notes) Text(who string) string {
	list := n.List(who)
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	for _, note := range list {
		b.WriteString("- ")
		b.WriteString(note.Key)
		b.WriteString(": ")
		b.WriteString(note.Text)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Restore 开机装回来.
//
//	**这一层丢了最让人恼火**: 地点丢了他还能到了那儿重说一句,
//	而"我老婆生日"这种事他不会想到要再说一遍 —— 他会以为它记着,
//	直到某天发现它不记着
func (n *Notes) Restore(events []abi.Event) {
	for _, e := range events {
		if e.Kind != abi.EvNoteSet && e.Kind != abi.EvNoteForgot {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		who, _ := m["who"].(string)
		key, _ := m["key"].(string)
		if key == "" {
			continue
		}
		if e.Kind == abi.EvNoteForgot {
			// 顺序重放: 忘掉那条在后面, 它盖掉前面的记忆
			n.mu.Lock()
			delete(n.all, noteKey{who: who, key: key})
			n.mu.Unlock()
			continue
		}
		text, _ := m["text"].(string)
		if text == "" {
			continue
		}
		n.mu.Lock()
		n.all[noteKey{who: who, key: key}] = Note{
			Who: who, Key: key, Text: text, At: e.At}
		n.mu.Unlock()
	}
}

func (n *Notes) stamp() int64 {
	if n.now != nil {
		return n.now()
	}
	return time.Now().UnixMilli()
}
