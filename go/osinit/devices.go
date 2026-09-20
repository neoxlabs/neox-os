package osinit

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 设备契约 —— 屋里有哪些东西, 各自能感知什么、能怎么告诉你.
//
// ── 为什么要有这一层 ──
//
//	采集端原来是匿名的: 第一次见到 source 这个字符串就建一条心跳记录,
//	OS 对它一无所知. 而"怎么告诉用户"那一头被写死成了推安卓通知 ——
//	代码里到处是 Android、KeepAliveService、通知渠道.
//
//	这两件事合在一起的后果是: **换一个硬件就得改 OS**. 而下一个接进来的
//	东西很可能不是手机 —— 一副耳机(只能出声、没有屏)、一块 ESP32
//	(只有一颗灯和一个按钮)、一块表(能测心率, 屏小得只放得下一行字).
//
//	所以把它翻过来: **设备自己声明它是什么**, OS 据此决定怎么说.
//	这跟"OS 只认 Signal 这一种形状, 加一个数据源不用改 OS 一行"是同一条
//	边界, 只是方向反过来 —— 那条管进, 这条管出.
//
// ── 一台设备可以只有一半 ──
//
//	NeoxPilot 可以只出不进(收通知、送语音),
//	另一个 app 只进不出(送位置和来电, 收不到任何东西). 契约要能表达
//	"我只感知"和"我只呈现", 否则这两个东西永远合不起来.

// Sense 一台设备能感知什么 —— 就是它会投哪几种信号的**前缀**.
//
//	用前缀而不是完整 kind: 一台手机报 place.arrived / place.left,
//	声明成 "place" 就够了. 让设备把它可能报的每一种都列出来的话,
//	加一种传感器要改两处(声明和实际投递), 而漏掉的那处是静默的.
type Sense = string

// Present 一台设备能怎么把话送到人跟前.
//
//	**这是一份能力清单, 不是一份偏好清单**: 设备说的是"我做得到什么",
//	该用哪种由 OS 判(它手上有紧急程度和打扰预算). 设备自己挑的话,
//	每台设备的规则都不一样, 而用户看到的是"同一件事在手机上震、
//	在耳机里静悄悄".
const (
	// PresentNotify 摆一条出来, 人回头看得见(通知栏、列表、一行字)
	PresentNotify = "notify"
	// PresentAlert 现在就吵醒他(出声、震动、亮灯)
	PresentAlert = "alert"
	// PresentSpeak 念出来 —— 耳机、音箱、车里
	PresentSpeak = "speak"
	// PresentAsk 能问一句并且**等得到答案**.
	//
	//	跟上面三种是两回事: 那三种是单向的. 有这一条才谈得上
	//	"它卡住了, 只有你能让它继续"(needs_you)真正闭环
	PresentAsk = "ask"
)

// Device 一台设备.
type Device struct {
	// ID 稳定标识. **换了就是另一台设备** —— 所以不要用会变的东西
	// (IP、安卓的 ANDROID_ID 在恢复出厂后会变是可以接受的: 那本来
	// 就该算另一台)
	ID string `json:"id"`
	// Name 给人看的名字("老王的手机"/"客厅音箱")
	Name string `json:"name"`
	// Kind 大类: phone / watch / earbuds / speaker / bridge / mcu …
	// **OS 不解释它**, 只在界面上显示 —— 一旦开始按 kind 分支,
	// 加一种硬件就要改 OS
	Kind string `json:"kind,omitempty"`
	// Senses 能感知什么(信号 kind 的前缀)
	Senses []Sense `json:"senses,omitempty"`
	// Presents 能怎么把话送到人跟前
	Presents []string `json:"presents,omitempty"`
	// Owner 这台设备是**谁的**. 空 = 屋子的, 不属于某个人.
	//
	//	这是"两个人各有各的手机"能成立的全部依据: 一条位置信号是谁的,
	//	取决于它从哪台设备来. 没有它的话, 他到家和她到家是同一条事实,
	//	后到的盖掉先到的.
	//
	//	**家里的传感器故意不填**: 门、天气、客厅有没有人, 这些是屋子的
	//	事实, 屋里每个人都该看得见.
	Owner string `json:"owner,omitempty"`
	// PaceSec 它多久报一次. 0 = 它不主动报(纯呈现设备)
	PaceSec int `json:"paceSec,omitempty"`
	// At 最后一次报到的时刻
	At int64 `json:"at,omitempty"`
}

// CanPresent 这台设备做不做得到某种呈现
func (d Device) CanPresent(way string) bool {
	for _, p := range d.Presents {
		if p == way {
			return true
		}
	}
	return false
}

// CanSense 这台设备报不报某种信号 —— 按前缀比
func (d Device) CanSense(kind string) bool {
	for _, s := range d.Senses {
		if s == kind || strings.HasPrefix(kind, s+".") {
			return true
		}
	}
	return false
}

// Devices 设备登记处. 一台机器一个.
type Devices struct {
	mu  sync.Mutex
	all map[string]Device
	log *EventLog
}

func NewDevices(log *EventLog) *Devices {
	return &Devices{all: map[string]Device{}, log: log}
}

// Declare 报到.
//
//	**每次都记, 不是只记第一次**: 设备的能力会变 —— 用户关掉了定位权限,
//	那台手机就不再 senses location 了. 而"它声称能感知却从来不报"是
//	最难查的一类: OS 会一直等一个永远不来的信号.
func (d *Devices) Declare(dev Device) (Device, error) {
	if strings.TrimSpace(dev.ID) == "" {
		return Device{}, fmt.Errorf("缺 id: 这台设备叫什么(稳定标识, 换了就算另一台)")
	}
	if strings.TrimSpace(dev.Name) == "" {
		dev.Name = dev.ID
	}
	// **一台什么都不会的设备是没有意义的**, 而且多半是拼错了字段名:
	// 收下的话它会安静地待在列表里, 既不报信号也收不到通知
	if len(dev.Senses) == 0 && len(dev.Presents) == 0 {
		return Device{}, fmt.Errorf(
			"senses 和 presents 至少要有一个: 一台既不感知也不呈现的设备, " +
				"接进来什么也不会发生")
	}
	for _, p := range dev.Presents {
		switch p {
		case PresentNotify, PresentAlert, PresentSpeak, PresentAsk:
		default:
			return Device{}, fmt.Errorf(
				"presents 里的 %q 不认得。只有这四种: %s(摆出来) / %s(现在就吵醒他) / "+
					"%s(念出来) / %s(问一句并等到答案)",
				p, PresentNotify, PresentAlert, PresentSpeak, PresentAsk)
		}
	}
	if dev.At == 0 {
		dev.At = time.Now().UnixMilli()
	}
	d.mu.Lock()
	d.all[dev.ID] = dev
	d.mu.Unlock()

	if d.log != nil {
		d.log.Append(signalPID, abi.EvDeviceDeclared, map[string]any{
			"id": dev.ID, "name": dev.Name, "kind": dev.Kind,
			"senses": dev.Senses, "presents": dev.Presents,
			"owner": dev.Owner, "paceSec": dev.PaceSec, "at": dev.At,
		})
	}
	return dev, nil
}

// Forget 删掉一台设备.
//
//	换手机、卖掉一块表 —— 删不掉的话列表里会永远躺着一台三年前的手机,
//	而 OS 还在等它报到.
//
//	**落一条新事件, 不从账本里抹旧的**: 账本只增不删是这套系统的骨架.
//	Restore 按时间顺序重放, 后面这条会把前面那条盖掉
func (d *Devices) Forget(id string) bool {
	d.mu.Lock()
	_, had := d.all[id]
	delete(d.all, id)
	d.mu.Unlock()
	if had && d.log != nil {
		d.log.Append(signalPID, abi.EvDeviceForgotten, map[string]any{"id": id})
	}
	return had
}

// Seen 这台设备刚有动静 —— 只更新时间, 不落账.
//
//	落账的话一天几千条"我还在", 那正是感知层最该避免的东西
//	(跟心跳不落账同一条理由)
func (d *Devices) Seen(id string, at int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dev, ok := d.all[id]; ok {
		dev.At = at
		d.all[id] = dev
	}
}

// List 屋里有哪些设备 —— 按名字排, 界面照这个顺序画
func (d *Devices) List() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Device, 0, len(d.all))
	for _, dev := range d.all {
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get 按 id 找一台
func (d *Devices) Get(id string) (Device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	dev, ok := d.all[id]
	return dev, ok
}

// OwnerOf 这个来源属于谁. 认不出或者没主人就回空 —— **空是合法的**:
// 天气和家里的传感器本来就不属于任何人
func (d *Devices) OwnerOf(source string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dev, ok := d.all[source]; ok {
		return dev.Owner
	}
	return ""
}

// Presenting 能用这种方式说话的设备有哪些.
//
//	**投递不挑设备, 但要知道有没有人接得住**: 一条要念出来的话,
//	如果屋里一台能出声的设备都没有, 那它就该退回成一条摆出来的通知 ——
//	而不是发出去然后石沉大海.
//
//	who 非空则只算那个人的设备(和屋子公用的那些). 一条给她的通知
//	不该在他手机上响
func (d *Devices) Presenting(way string, who string) []Device {
	var out []Device
	for _, dev := range d.List() {
		if !dev.CanPresent(way) {
			continue
		}
		if who != "" && dev.Owner != "" && dev.Owner != who {
			continue
		}
		out = append(out, dev)
	}
	return out
}

// Ask 反过来问设备要一样东西 —— **一句吆喝, 不进账本**.
//
// ── 为什么需要反向这一条 ──
//
//	采集端按变化触发: 人没挪 120 米就一条都不报. 那对省电是对的,
//	对"我现在在哪条路"却是致命的 —— 他问这句话时, OS 手里最新
//	的一条是三分钟前的, 而市区里三分钟是两个路口. 答一个过期的路名
//	比说"不知道"糟, 因为他会照着走.
//
//	所以他真问起来的时候, 让手机立刻取一次. 平时照旧不问 ——
//	这条路一天走不了几次, 而按变化触发那套省下的是一整天的电.
//
// ── 只问该问的那几台 ──
//
//	senses 里有这一样的才问. 一台体重秤收到"给我个位置"只会困惑,
//	而那条吆喝还占着它的一次唤醒.
//
//	who 非空则只问那个人的设备(和屋子公用的那些) —— 理由同 Presenting:
//	为了答"他在哪"去唤醒她的手机, 是一次不该发生的打扰.
//
// 返回问了几台. 0 = 没有能答这件事的设备, 调用方**要照实说**,
// 别装作问过了.
func (d *Devices) Ask(what string, who string) int {
	if d == nil || d.log == nil {
		return 0
	}
	n := 0
	for _, dev := range d.List() {
		if !dev.CanSense(what) {
			continue
		}
		if who != "" && dev.Owner != "" && dev.Owner != who {
			continue
		}
		d.log.Append(signalPID, abi.EvDeviceAsk, map[string]any{
			"device": dev.ID, "what": what})
		n++
	}
	return n
}

// Restore 从账本装回来.
//
//	**重启之后 OS 得知道屋里有哪些设备**, 而不是等它们各自下一次报到:
//	一台一天只报一次的设备(体重秤、电表), 重启后能失踪一整天 ——
//	而那段时间里 OS 会以为它从来不存在.
//
//	同一台设备的多条只留最后一条: 声明是**现状**不是流水
func (d *Devices) Restore(evs []abi.Event) {
	for _, e := range evs {
		p, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		// 删掉的那条在后面, 于是它盖掉前面的声明 —— 顺序重放本身
		// 就是这里的全部逻辑
		if e.Kind == abi.EvDeviceForgotten {
			d.mu.Lock()
			delete(d.all, str(p["id"]))
			d.mu.Unlock()
			continue
		}
		if e.Kind != abi.EvDeviceDeclared {
			continue
		}
		dev := Device{
			ID:       str(p["id"]),
			Name:     str(p["name"]),
			Kind:     str(p["kind"]),
			Senses:   strList(p["senses"]),
			Presents: strList(p["presents"]),
			Owner:    str(p["owner"]),
			PaceSec:  intOf(p["paceSec"]),
			At:       int64Of(p["at"]),
		}
		if dev.ID == "" {
			continue
		}
		d.mu.Lock()
		d.all[dev.ID] = dev
		d.mu.Unlock()
	}
}

// strList 账本回来的是 []any(JSON 解出来的), 内存里直接放的是 []string ——
// **两种都要认**: 只认一种的话, 重启之后能力清单会静默地变成空的
func strList(v any) []string {
	switch xs := v.(type) {
	case []string:
		return xs
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func intOf(v any) int {
	if n, ok := v.(float64); ok {
		return int(n)
	}
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}

func int64Of(v any) int64 {
	if n, ok := v.(float64); ok {
		return int64(n)
	}
	if n, ok := v.(int64); ok {
		return n
	}
	return 0
}
