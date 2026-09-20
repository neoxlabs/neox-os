package osinit

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/neox-os/neox-os/abi"
)

// 用户定的关注 —— "以后我一到家你就跟我说一声".
//
// ── 为什么必须有这个东西 ──
//
// 只有把长期关注作为系统对象保存, 才能兑现"以后一到家就通知"这类承诺;
// 而**系统里根本没有"用户定的关注"这个概念**:
//
//	主动进程只按三条判据判(不可逆/可行动/知情权),
//	而"你到家了"几乎必然一条都过不了 → 它会保持沉默
//	下一段对话更是全新进程, 那条"接上的待命"一个字都不剩
//
// 跟 S16 那次是同一类失败: **它对用户许了做不到的诺**, 只是这次
// 许的是一条长期的.
//
// ── 这算不算在做 IFTTT ──
//
// 不算, 而且这个区分是整个感知层的地基:
//
//	**采集端**报判断 → 是 IFTTT. 智能被写进规则, 换个传感器就要重配
//	**用户**说他关心什么 → 不是规则引擎, 是他的意思本身
//
// PERCEPTION_ARCH 里"不做规则引擎"那条针对的是前者(Sensor 只报事实).
// 而用户自己的偏好本来就该被记住 —— 那正是打扰预算里
// "critical 由策略层判"的同一侧: **用户是策略层.**
//
// ── 为什么不占打扰额度 ──
//
// 跟闹钟一个道理: 预算防的是"它没事找事", 而这件事是他自己让做的.
// 一个他明确要求的通知被额度挡下来, 他会觉得系统坏了.
type Watch struct {
	ID string `json:"id"`
	// Kind 关注哪一类信号. 空 = 不限
	Kind string `json:"kind"`
	// Place 关注哪个地点(到达/离开时的地名). 空 = 不限
	Place string `json:"place"`
	// Say 命中后投递的句子. 空值使用默认句子，避免命中后没有可投递的通知
	Say string `json:"say"`
	// Raw 用户原话 —— 事后他问"我让你盯什么了", 要能原样答
	Raw string `json:"raw"`
}

// Match 这条信号命不命中.
//
// **两个条件都空的关注不许存在**(见 Add): 那等于"什么都关注",
// 于是每条信号都打扰他一次 —— 比不做还糟.
func (w Watch) Match(kind, place string) bool {
	if w.Kind != "" && w.Kind != kind {
		return false
	}
	if w.Place != "" && !strings.EqualFold(w.Place, place) {
		return false
	}
	return true
}

type Watches struct {
	mu   sync.Mutex
	list []Watch
	seq  int
	log  *EventLog
	// places 用来验"这个地点存在吗". 可以是 nil(测试/没接感知层)
	places *Places
}

func NewWatches(log *EventLog) *Watches { return &Watches{log: log} }

// UseP1aces 装上地点表 —— 关注一个不存在的地点是**静默失效**, 见 Add
func (w *Watches) UsePlaces(p *Places) { w.places = p }

// wildcardWords 模型用来表达"不限"的各种写法.
//
// ── 为什么必须认这些 ──
//
// 工具描述写着"不限就留空"时, 模型仍可能写成 `*`.
// 于是关注被登记成 `lock.opened @*` —— 代码把 `*` 当成一个**地名**
// 去精确匹配, 而世界上没有一个地方叫 `*`.
//
// **结果是彻头彻尾的静默失效**: 关注登记成功、state.txt 里看着好好的、
// 用户也收到了"盯上了"的确认, 而它**永远不会命中**. 门开了没人说话,
// 而没有任何一处会报错.
//
// 提示词/工具描述改得再清楚也挡不住这类: 模型有几十种方式表达"不限",
// 而每一种写错都是静默的. **在收口这一侧认掉**才治本.
var wildcardWords = map[string]bool{
	"*": true, "any": true, "all": true, "-": true, "none": true,
	"不限": true, "任意": true, "全部": true, "所有": true, "无": true,
}

func normalizeWildcard(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	if wildcardWords[t] {
		return ""
	}
	return strings.TrimSpace(s)
}

func (w *Watches) Add(kind, place, say, raw string) (Watch, error) {
	kind, place = normalizeWildcard(kind), normalizeWildcard(place)
	if kind == "" && place == "" {
		// **不许"什么都关注"** —— 那等于每条信号都打扰他一次, 比不做还糟.
		// 而且它是**静默的灾难**: 用户以为自己设了个贴心的提醒,
		// 拿到的是一天几十条通知, 然后把整个通知关掉.
		return Watch{}, fmt.Errorf(
			"kind 和 place 至少要给一个 —— 两个都不给等于'什么都关注', " +
				"那会把每条信号都推给用户。他到底想盯什么?")
	}
	// ── 关注一个不存在的地点 = 永远不会命中 ──
	//
// 例如关注"以后有人开锁就告诉我"时, 可能登记了
	// `lock.opened @家` —— 而那一轮**从来没有命名过"家"**.
	//
	// 登记成功、state.txt 里看着好好的、用户收到了"盯上了"的确认,
	// 而它**永远不可能命中**: 摘要里的地点名只可能来自已命名的地点,
	// 没命名过的地方那一栏是坐标.
	//
	// 跟通配符那次(S19)是同一个家族: **看着成了, 其实永远不响**.
	// 这一类只能在收口这一侧拦 —— 而拦下来的代价只是让用户先命名一次,
	// 放过去的代价是他以为有人替他盯着.
	if place != "" && w.places != nil {
		known := false
		for _, p := range w.places.Known() {
			if strings.EqualFold(p.Name, place) {
				known = true
				break
			}
		}
		if !known {
			names := make([]string, 0, 4)
			for _, p := range w.places.Known() {
				names = append(names, p.Name)
			}
			have := "一个都还没有"
			if len(names) > 0 {
				have = "现在认得的是: " + strings.Join(names, " ")
			}
			return Watch{}, fmt.Errorf(
				"还不知道「%s」在哪儿, 这条关注**永远不会响**。"+
					"%s。先等用户到那个地方的时候说一声'这儿是%s'(用 name_place 记下), "+
					"再来设这条关注 —— 或者这条先不限地点",
				place, have, place)
		}
	}

	w.mu.Lock()
	w.seq++
	// ── ID 前缀要自识别 ──
	//
	// 闹钟和关注原来都用 w%d. 没人按 id 查的时候看不出问题, 但一加撤销
	// 就是灾难: 撤销请求可能指向两个同名对象 —— 而**撤错一个是
	// 静默的**: 他以为取消了提醒, 实际取消的是"到家告诉我".
	//
	// 前缀自识别之后, id 本身就说明了它是什么, 撤销那一侧不用再猜.
	it := Watch{ID: fmt.Sprintf("g%d", w.seq), Kind: kind, Place: place,
		Say: strings.TrimSpace(say), Raw: strings.TrimSpace(raw)}
	// 同样的关注不重复加 —— 相同条件不应产生两条通知
	for _, old := range w.list {
		if old.Kind == it.Kind && old.Place == it.Place {
			w.mu.Unlock()
			return old, nil
		}
	}
	w.list = append(w.list, it)
	w.mu.Unlock()
	if w.log != nil {
		w.log.Append(signalPID, abi.EvWatchSet, map[string]any{
			"id": it.ID, "kind": it.Kind, "place": it.Place,
			"say": it.Say, "raw": it.Raw})
	}
	return it, nil
}

func (w *Watches) Remove(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, it := range w.list {
		if it.ID == id {
			w.list = append(w.list[:i], w.list[i+1:]...)
			if w.log != nil {
				w.log.Append(signalPID, abi.EvWatchRemoved, map[string]any{"id": id})
			}
			return true
		}
	}
	return false
}

// Hits 这份摘要里有没有命中的关注.
//
// 返回**已经渲染好的话** —— 命中之后不需要再过一次模型:
// 命中后直接使用已渲染的话, 中间再经过一次模型只会增加跑偏的机会.
func (w *Watches) Hits(d Digest) []string {
	w.mu.Lock()
	list := append([]Watch(nil), w.list...)
	w.mu.Unlock()
	if len(list) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range d.Items {
		place, _ := item.Sample["地点"].(string)
		for _, it := range list {
			if !it.Match(item.Kind, place) || seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			out = append(out, it.render(item, place))
		}
	}
	return out
}

func (it Watch) render(item DigestItem, place string) string {
	if it.Say != "" {
		return it.Say
	}
	what := signalLabel(item.Kind)
	if place != "" {
		return fmt.Sprintf("%s：%s", what, place)
	}
	return what
}

// List 用户问"我让你盯着什么"时要能原样答
func (w *Watches) List() []Watch {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]Watch(nil), w.list...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Restore 开机装回来.
//
// **这条是它存在的理由的一半**: 关注要跨越进程生命周期, 而下一段对话是
// 一个全新进程. 不落盘的话, 这个功能跟 agent 嘴上答应一句没有区别.
func (w *Watches) Restore(events []abi.Event) {
	for _, e := range events {
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		switch e.Kind {
		case abi.EvWatchSet:
			kind, _ := m["kind"].(string)
			place, _ := m["place"].(string)
			say, _ := m["say"].(string)
			raw, _ := m["raw"].(string)
			w.mu.Lock()
			w.list = append(w.list, Watch{ID: id, Kind: kind, Place: place,
				Say: say, Raw: raw})
			if n := watchSeqOf(id); n > w.seq {
				w.seq = n
			}
			w.mu.Unlock()
		case abi.EvWatchRemoved:
			w.mu.Lock()
			for i, it := range w.list {
				if it.ID == id {
					w.list = append(w.list[:i], w.list[i+1:]...)
					break
				}
			}
			w.mu.Unlock()
		}
	}
}

// watchSeqOf 关注的 id 是 g%d —— 跟闹钟的 w%d 分开, 见 Add 的说明
func watchSeqOf(id string) int {
	var n int
	if _, err := fmt.Sscanf(id, "g%d", &n); err != nil {
		return 0
	}
	return n
}

// HitNote 命中之后要贴给主动进程的一句话.
//
// ── 为什么不是直接闷掉那份摘要 ──
//
// 关注设为"开锁就告诉我"时, 半夜有人开锁 ——
// 他被告知了**两遍**:
//
//	开锁
//	◆ 主动(紧急破例): 大门锁在 01:10 被打开, 记录备注写的是"凌晨4点",
//	                 时间对不上。确认一下是不是你或家人开的…
//
// "报两次比晚报更糟"是这套东西第一天就写死的规矩. 但**主动那条的
// 信息量明显更大** —— 它发现了时间戳跟备注对不上. 闷掉它是在
// 用一条规矩换掉一次真正有用的判断.
//
// 所以不闷: 把"这些已经说过了"作为事实贴给它, 让它自己判断
// 还有没有别的要补. 跟"Sensor 只报事实不报判断"是同一条 ——
// 这里 OS 报的也是事实.
func WatchNote(hits []string) string {
	if len(hits) == 0 {
		return ""
	}
	return "\n\n（这几条**已经按用户设的关注直接告诉他了**：" +
		strings.Join(hits, "；") +
		"。别再把同一件事说一遍——只有你另有要说的（时间对不上、" +
		"这次跟平时不一样、需要他做点什么）才开口。）"
}
