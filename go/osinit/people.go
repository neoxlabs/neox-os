package osinit

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 人 —— 屋里住着谁.
//
// ── 为什么 OS 必须有这个概念 ──
//
//	一台家用的 OS 上不止一个人. 两个人各有各的手机、各有各的"家"和
//	"公司"、各有各的日程. 没有这一层的话:
//
//	  · "他到家了"和"她到家了"是同一条事实, 后到的盖掉先到的
//	  · 她的提醒会推到他手机上
//	  · "我现在在哪"这个问题**没有唯一答案**, 而系统会给一个
//
//	最后一条最要命: 它不会报错, 只会答错.
//
// ── 这不是鉴权 ──
//
//	**说清楚**: 谁拿到这台 OS 的 token, 谁就有全部权限 —— 现在是这样,
//	加了这一层之后还是这样. 人这个概念解决的是**归属**("这条位置是谁的"、
//	"这条通知该给谁"), 不是**权限**.
//
//	把它当鉴权用的话, 会得到一种最坏的东西: 一道看起来存在、实际上
//	一推就倒的墙. 而人会依据那道墙去放东西.
//
//	真正的鉴权在 token 那一层(见 observe.go 的 guard). 要做到"她看不见
//	他的东西", 得先有每个人各自的 token —— 那是另一件事, 没做.
//
// ── id 从哪儿来 ──
//
//	由宿主提供的用户 id. 用它而不是本地自增, 是因为**同一个人可能
//	有三台设备**(手机、平板、以后的耳机), 而它们要认得出是同一个人.
//	账号 id 是唯一一个跨设备稳定、又是用户自己拿得到的东西.
//
//	OS **不验证**这个 id(见上面"这不是鉴权"). 设备说自己是谁, OS 就
//	信 —— 反正它已经拿着 token 了.

// Person 一个人.
type Person struct {
	// ID 稳定标识. 通常由宿主提供
	ID string `json:"id"`
	// Name 给人看的名字. **界面上只显示这个** —— 账号 id 是给机器认的
	Name string `json:"name"`
	// At 最后一次见到(某台属于他的设备报到)
	At int64 `json:"at,omitempty"`
}

// People 屋里的人.
type People struct {
	mu  sync.Mutex
	all map[string]Person
	log *EventLog
}

func NewPeople(log *EventLog) *People {
	return &People{all: map[string]Person{}, log: log}
}

// Know 记住一个人. 已经认得就更新名字(他改了昵称)
func (p *People) Know(x Person) (Person, error) {
	if strings.TrimSpace(x.ID) == "" {
		return Person{}, fmt.Errorf("缺 id: 这个人的稳定标识(通常是账号 id)")
	}
	if strings.TrimSpace(x.Name) == "" {
		// **名字空着也收下, 但要有个能看的** —— 一个显示成一串 uuid
		// 的人, 在通知里是完全没法认的
		x.Name = "某人"
	}
	if x.At == 0 {
		x.At = time.Now().UnixMilli()
	}
	p.mu.Lock()
	old, existed := p.all[x.ID]
	same := existed && old.Name == x.Name
	p.all[x.ID] = x
	p.mu.Unlock()

	// 名字没变就不落账 —— 每次开 app 落一条的话, 一年下来账本里
	// 全是"张三还叫张三"
	if p.log != nil && !same {
		p.log.Append(signalPID, abi.EvPersonKnown, map[string]any{
			"id": x.ID, "name": x.Name, "at": x.At,
		})
	}
	return x, nil
}

// Forget 忘掉一个人.
//
//	**他说过的话、他的位置记录不会跟着没** —— 那些在账本里, 而账本
//	只增不删. 忘掉的只是"屋里住着谁"这份名单: 一个搬走的人不该继续
//	出现在通知的收件人里.
func (p *People) Forget(id string) bool {
	p.mu.Lock()
	_, had := p.all[id]
	delete(p.all, id)
	p.mu.Unlock()
	if had && p.log != nil {
		p.log.Append(signalPID, abi.EvPersonForgotten, map[string]any{"id": id})
	}
	return had
}

// Seen 某台属于他的设备刚有动静. 只更新时间, 不落账
func (p *People) Seen(id string, at int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if x, ok := p.all[id]; ok {
		x.At = at
		p.all[id] = x
	}
}

func (p *People) Get(id string) (Person, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x, ok := p.all[id]
	return x, ok
}

// Name 给人看的名字. 认不出就回 id ——
// **不能回空**: 一条署名为空的通知看起来像系统故障
func (p *People) Name(id string) string {
	if id == "" {
		return ""
	}
	if x, ok := p.Get(id); ok {
		return x.Name
	}
	return id
}

func (p *People) List() []Person {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Person, 0, len(p.all))
	for _, x := range p.all {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Restore 从账本装回来 —— 人比设备活得久, 换手机不该变成换个人
func (p *People) Restore(evs []abi.Event) {
	for _, e := range evs {
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		if e.Kind == abi.EvPersonForgotten {
			p.mu.Lock()
			delete(p.all, str(m["id"]))
			p.mu.Unlock()
			continue
		}
		if e.Kind != abi.EvPersonKnown {
			continue
		}
		id := str(m["id"])
		if id == "" {
			continue
		}
		p.mu.Lock()
		p.all[id] = Person{ID: id, Name: str(m["name"]), At: int64Of(m["at"])}
		p.mu.Unlock()
	}
}
