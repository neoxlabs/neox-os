package osinit

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 地点命名 —— 让"你到了 31.86001, 117.28"变成"你到公司了".
//
// ── 为什么这件事不是锦上添花 ──
//
// 真机演示时看到的摘要长这样:
//
//   - phone.mk · place.arrived × 1  最新: acc=5 lat=31.86001 lon=117.28
//
// **模型对这行字能做的判断为零.** 它不知道那是公司、是家、还是医院,
// 于是三条判据一条都过不了 —— 到达信号在语义上是空的.
//
// 一个坐标和一个名字, 对"值不值得打扰他"这个判断来说, 差的不是精度,
// 是**有没有内容**.
//
// ── 为什么名字由用户给, 不是查地图 ──
//
// 反地理编码只能给出"某某路 128 号", 而那同样不是判断的依据 ——
// 真正有用的是"这是他公司"这个**关系**, 而只有他自己知道.
// 何况查地图要出网, 而把用户的全部轨迹送给一个地图服务, 是这套系统
// 最不该做的事之一.
//
// ── 边界: 总线不解释 body, 宿主可以 ──
//
// senseapi.go 里写死了"OS 不校验 body 的形状" —— 那条不能破,
// 破了就是加一种传感器要改 OS.
//
// 所以命名不在总线里, 在**宿主的投递路径上**: 有 lat/lon 就用,
// 没有就算, 认不出来就原样放过. 这是"有就用"的增强, 不是契约.
type Place struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	// Radius 多近算"在这儿", 米. 缺省见 defaultPlaceRadius
	Radius float64 `json:"radius"`
}

type Places struct {
	mu   sync.Mutex
	list []Place
	log  *EventLog
	// 最近一次见到的位置 —— 给当前位置命名时必须知道"这儿"是哪儿.
	//
	// 由投递路径上的 Observe 喂. 它是**机会性**的: 信号里恰好有
	// lat/lon 就记下来, 没有就算
	lastLat, lastLon float64
	lastAt           int64
}

// defaultPlaceRadius 缺省多近算"在这儿".
//
// ── 150 米太小 ──
//
//	他人在家里, 手机报 32.963,117.352, 而记着的"家"是 32.963,117.350 ——
//	差 190 米, 刚好出圈. 于是它说"在一个还没起过名的地方", 而他人就在
//	自己家客厅里.
//
//	190 米不是定位飘了: 一个小区从东门到西门就有这么远, 而他昨天在
//	楼下、今天在楼上, 报的点本来就不一样. 写字楼、学校也一样.
//
//	**判错的代价不对称**: 圈小了, 他在家而系统说不认识 —— 所有跟
//	"到家"有关的判断全部失灵, 而且一次都不报错; 圈大了, 最多是他
//	刚拐进小区就算到家, 早那么一两分钟.
//
//	所以缺省放到 250, 而且**可以指定** —— 一个大院和一间小店该有
//	不同的圈, 那是他才知道的事.
const defaultPlaceRadius = 250.0

func NewPlaces(log *EventLog) *Places { return &Places{log: log} }

// Observe 顺路看一眼信号里有没有坐标.
//
// **不解释形状, 只是碰运气**: 有 lat/lon 就记, 类型不对就跳过.
// 这样加一种带位置的传感器不用改这里, 而不带位置的传感器也不会出错.
func (p *Places) Observe(s abi.Signal) {
	lat, ok1 := floatOf(s.Body["lat"])
	lon, ok2 := floatOf(s.Body["lon"])
	if !ok1 || !ok2 {
		return
	}
	p.mu.Lock()
	if s.At >= p.lastAt {
		p.lastLat, p.lastLon, p.lastAt = lat, lon, s.At
	}
	p.mu.Unlock()
}

// Here 最后一次知道的坐标. ok=false 表示从来没收到过带位置的信号.
//
//	**给要出网查东西的那几个用**(天气、路况): 它们得知道查哪儿的.
//	把这件事留给采集端的话, 每个采集端都要自己带一份坐标, 而那份
//	迟早跟 OS 手里这份对不上.
func (p *Places) Here() (lat, lon float64, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastLat, p.lastLon, p.lastAt != 0
}

// HereAt 最后一次收到位置是什么时候(毫秒). 0 = 从来没收到过.
//
//	给"要不要现问一次"用 —— 见 Devices.Ask
func (p *Places) HereAt() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAt
}

// WaitFresher 等一条比 since 更新的位置, 最多等 d.
//
// ── 为什么要等 ──
//
//	采集端按变化触发: 人没挪 120 米就一条都不报. 那对省电是对的,
//	对"我现在在哪条路"却是致命的 —— OS 手里
//	最新的一条是三分钟前的, 而市区里三分钟是两个路口.
//
//	所以问一次(Devices.Ask)之后**等一小会儿**. 等不到就用手上这条,
//	并且照实说它多旧 —— 编一个新的比说"三分钟前"糟得多.
//
// ── 为什么是轮询不是通道 ──
//
//	一个等待通道要处理"没人等的时候往哪儿丢""同时两个人在等"这些事,
//	而这里等的最长时间是几秒、频率是一天几次. 十行轮询看得懂,
//	而看得懂比省那几次 sleep 值钱.
func (p *Places) WaitFresher(since int64, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.HereAt() > since {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return p.HereAt() > since
}

// NameHere 把**刚才所在的位置**命名.
//
// 地点以当前所在位置命名，不要求输入"31.86,117.28"这类坐标 —— 坐标是
// 系统的内部表示，而且通常无法准确提供.
func (p *Places) NameHere(name string, radius float64) (Place, error) {
	p.mu.Lock()
	lat, lon, at := p.lastLat, p.lastLon, p.lastAt
	p.mu.Unlock()
	if at == 0 {
		return Place{}, fmt.Errorf(
			"还没收到过任何带位置的信号, 不知道'这儿'是哪儿。" +
				"等手机传一次位置上来再说")
	}
	return p.Add(name, lat, lon, radius)
}

// Add 记一个地点. 同名的覆盖表示地点迁移后的新位置.
func (p *Places) Add(name string, lat, lon, radius float64) (Place, error) {
	if name == "" {
		return Place{}, fmt.Errorf("地点名是空的")
	}
	if radius <= 0 {
		radius = defaultPlaceRadius
	}
	pl := Place{Name: name, Lat: lat, Lon: lon, Radius: radius}
	p.mu.Lock()
	replaced := false
	for i, old := range p.list {
		if old.Name == name {
			p.list[i], replaced = pl, true
			break
		}
	}
	if !replaced {
		p.list = append(p.list, pl)
	}
	p.mu.Unlock()
	if p.log != nil {
		p.log.Append(signalPID, abi.EvPlaceNamed, map[string]any{
			"name": name, "lat": lat, "lon": lon, "radius": radius})
	}
	return pl, nil
}

// Lookup 这个坐标在哪个已知地点里. 认不出来返回空串.
//
// 多个地点都覆盖到时取**最近的那个** —— 家和小区门口可能重叠,
// 而用户想听到的是更具体的那个.
func (p *Places) Lookup(lat, lon float64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	best, bestD := "", math.MaxFloat64
	for _, pl := range p.list {
		d := haversine(lat, lon, pl.Lat, pl.Lon)
		if d <= pl.Radius && d < bestD {
			best, bestD = pl.Name, d
		}
	}
	return best
}

// Annotate 把摘要里的坐标换成地名.
//
// **换掉而不是并列**: 留着 lat/lon 的话, 模型会在回话里把坐标念出来
// ("你到公司了(31.86001,117.28)"), 那是纯噪音 —— 用户比谁都清楚
// 公司在哪儿, 他要的是"到了"这件事.
//
// 认不出来的原样留着: 一个没命名过的地方, 坐标至少还能让它说
// "你到了一个我不认识的地方待了半小时".
func (p *Places) Annotate(d *Digest) {
	for i := range d.Items {
		s := d.Items[i].Sample
		if s == nil {
			continue
		}
		lat, ok1 := floatOf(s["lat"])
		lon, ok2 := floatOf(s["lon"])
		if !ok1 || !ok2 {
			continue
		}
		name := p.Lookup(lat, lon)
		if name == "" {
			continue
		}
		clone := make(map[string]any, len(s))
		for k, v := range s {
			if k == "lat" || k == "lon" || k == "acc" {
				continue
			}
			clone[k] = v
		}
		clone["地点"] = name
		d.Items[i].Sample = clone
	}
}

// Forget 忘掉一个地方.
//
//	**教错了要能改**: 一个记错的"家"会让所有跟到家有关的判断都错 ——
//	到家提醒在他还在路上时响、"我在哪"答错、通勤时间从错的起点算.
//	而这些一个都不会报错.
//
//	落一条新事件, 不从账本里抹旧的 —— 账本只增不删是这套系统的骨架
func (p *Places) Forget(name string) bool {
	p.mu.Lock()
	found := false
	out := p.list[:0]
	for _, pl := range p.list {
		if pl.Name == name {
			found = true
			continue
		}
		out = append(out, pl)
	}
	p.list = out
	p.mu.Unlock()
	if found && p.log != nil {
		p.log.Append(signalPID, abi.EvPlaceForgotten, map[string]any{"name": name})
	}
	return found
}

// Has 认不认得这个名字的地方.
//
//	给"从地点推出来的结论"复查前提用 —— 见 Presence.Note().
//	删掉一个地方之后, 那些结论必须跟着不算数
func (p *Places) Has(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pl := range p.list {
		if pl.Name == name {
			return true
		}
	}
	return false
}

// Known 已知的地点, 按名字排 —— 用户要能问"你记着哪些地方"
func (p *Places) Known() []Place {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]Place(nil), p.list...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Restore 开机装回来. 地点是用户教的, 丢了要重教一遍 ——
// 那比丢一条提醒更让人恼火
func (p *Places) Restore(events []abi.Event) {
	// ── 装回来的一律不记账 ──
	//
	//	**这一条栽过一次, 而且栽得很难看**: EvPlaceNamed 那条特意摘了
	//	log(见下面), 而 EvPlaceForgotten 那条忘了 —— 于是每次开机,
	//	"忘掉家"这条事件在账本里翻一倍, 而且新写的那几条排在**末尾**.
	//
	//	顺序重放时末尾的 forgotten 就盖掉了它前面的 named ——
	//	**他重新教过的"家"在下一次重启后消失**, 而账本里越积越多.
	//	一条 place.forgotten 会变成四条，地点会消失。
	//
	//	所以整段禁掉记账, 不再逐条摘: 逐条摘就是"记得改两处"那种陷阱,
	//	而这个文件已经证明了没人记得住.
	log := p.log
	p.log = nil
	defer func() { p.log = log }()
	for _, e := range events {
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		// ── "刚才在哪"也要装回来 ──
		//
		//	NameHere 靠的是最后一次收到的坐标, 而那个只活在内存里.
		//	不装回来的话, **每次重启之后"这儿是家"都会失败** ——
		//	它报的是"还没收到过任何带位置的信号", 而账本里明明有,
		//	世界模型也答得出来. 两处各存一份"最后位置", 而只有一处
		//	会恢复.
		//
		//	而且它不会自己好起来: 采集端按移动触发, 人不动就不报 ——
		//	用户可能坐了一下午, 而这句话一直失败.
		if e.Kind == abi.EvSignal {
			if body, ok := m["body"].(map[string]any); ok {
				p.Observe(abi.Signal{At: int64Of(m["at"]), Body: body})
			}
			continue
		}
		// 删掉的那条在后面, 顺序重放时它盖掉前面的命名
		if e.Kind == abi.EvPlaceForgotten {
			if n, _ := m["name"].(string); n != "" {
				p.Forget(n)
			}
			continue
		}
		if e.Kind != abi.EvPlaceNamed {
			continue
		}
		name, _ := m["name"].(string)
		lat, _ := floatOf(m["lat"])
		lon, _ := floatOf(m["lon"])
		r, _ := floatOf(m["radius"])
		if name == "" {
			continue
		}
		// 走 Add 而不是直接 append —— 同名覆盖的语义只该有一处.
		// 不记账由整段的 defer 管着(见函数开头)
		_, _ = p.Add(name, lat, lon, r)
	}
}

func floatOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// MetersBetween 两点之间多少米 —— 给外面用的那个名字
func MetersBetween(lat1, lon1, lat2, lon2 float64) float64 {
	return haversine(lat1, lon1, lat2, lon2)
}

// haversine 球面距离, 米. 跟采集端用的是同一个公式 ——
// 两边算出来的距离必须一致, 否则"在不在这个地点里"两边会有不同答案
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * R * math.Asin(math.Min(1, math.Sqrt(a)))
}

// SeedPlace 直接往账本里落一条"这儿叫什么" —— 与当前位置命名的效果相同.
//
// ── 为什么验收需要它 ──
//
// 整条链第一次自己跑完时抓到的: 有事那天 23 条信号、主动进程判了 20 次、
// 那件★也确确实实发生了(4 条 lock.opened), 而**"家里现在没人"一次都
// 没出现** —— 于是它一次都没开口, 验收判"开口 0 次, 期望 1 次".
//
// 问题不在判断，在**编排**: 脚本喂了手机的坐标，却从来没告诉 OS
// "这儿是家". 而 S56 那条判据是对的 —— 查不到就是查不到, 不能拿一个
// 随便的坐标当家(用户还没认过家就误报, 他会当场关掉通知).
//
// 正常流程由当前位置命名落一条 place.named.
// 验收要重放一天真实的生活, 就得把这一句也重放.
func SeedPlace(log *EventLog, name string, lat, lon float64) {
	if log == nil || name == "" {
		return
	}
	NewPlaces(log).Add(name, lat, lon, 0)
}
