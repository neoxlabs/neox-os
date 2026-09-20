package osinit

import (
	"fmt"
	"sort"

	"github.com/neox-os/neox-os/abi"
)

// 信号静音 —— 校准闭环的最后一段.
//
// ── 为什么必须在 OS 侧 ──
//
// 校准报告认得出噪音种类, 判据也对("它从没让任何人做过任何事"),
// 但它给出的建议是**"考虑在采集端就砍掉"** —— 而采集端是装在别人
// 手机里的 APK、或者别人跑着的 HA 桥. **那是个改不动的地方.**
// 于是那条建议永远只能看着, 报告到动作这一段是断的.
//
// ── 静音不是丢弃 ──
//
// 静音的信号**照样落账**. 两条理由, 缺一不可:
//
//	① 用户问"我今天去过哪儿"要能答得出来(signals.txt 就是账本渲染的)
//	② **报告本身就是靠账本算的**. 静音的种类要是从账本里消失,
//	   "这条静音当初静得对不对"就永远无法复核 —— 它会变成一个
//	   没人记得为什么的永久盲区
//
// 所以静音管的是"要不要为它开口", 不是"要不要记下来" ——
// 跟打扰预算那条"超额不是拒绝, 是延后"是同一个形状.
//
// ── 按 source+kind, 不是只按 kind ──
//
// 一个坏掉的传感器不该让所有同类的信号跟着闭嘴: ha.badsensor 一直
// 抖 state.changed, 不代表 ha.livingroom 的 state.changed 不重要.

// Mute 一条静音
type Mute struct {
	Source string
	Kind   string
	Why    string // 当初为什么静的 —— 半年后回来看, 没有这句就只能猜
	N      int    // 静音期间吞掉了多少条
}

func (m Mute) String() string {
	return fmt.Sprintf("%s/%s (%s, 已吞掉 %d 条)", m.Source, m.Kind, m.Why, m.N)
}

func muteKey(source, kind string) string { return source + "/" + kind }

// Mute 把某个来源的某个种类静音.
func (b *SignalBus) Mute(source, kind, why string) {
	b.mu.Lock()
	if b.mutes == nil {
		b.mutes = map[string]*Mute{}
	}
	k := muteKey(source, kind)
	if _, ok := b.mutes[k]; !ok {
		b.mutes[k] = &Mute{Source: source, Kind: kind, Why: why}
	}
	b.mu.Unlock()
	b.record(abi.EvMuteSet, map[string]any{
		"source": source, "kind": kind, "why": why,
	})
}

// Unmute 解除. 返回原来在不在 —— 撤一个不存在的要能报错,
// 静默成功会让用户以为撤过了
func (b *SignalBus) Unmute(source, kind string) bool {
	b.mu.Lock()
	k := muteKey(source, kind)
	_, ok := b.mutes[k]
	delete(b.mutes, k)
	b.mu.Unlock()
	if ok {
		b.record(abi.EvMuteRemoved, map[string]any{"source": source, "kind": kind})
	}
	return ok
}

// Mutes 现在静着哪些, 各吞掉了多少.
//
// **吞掉的条数要能看见**: 看不见的话静音就是个没人记得为什么的永久盲区 ——
// 传感器修好了、或者某个种类突然变得重要了, 没有任何东西提醒人回来看
func (b *SignalBus) Mutes() []Mute {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Mute, 0, len(b.mutes))
	for _, m := range b.mutes {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N > out[j].N })
	return out
}

// RestoreMutes 开机时读回来.
//
// 不读的话, 一次崩溃之后那个噪音种类又开始吵, 而用户以为自己
// 已经处理过了 —— 跟打扰额度那条(S26)是同一个失败.
func (b *SignalBus) RestoreMutes(events []abi.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mutes == nil {
		b.mutes = map[string]*Mute{}
	}
	for _, e := range events {
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		source, kind := str(m["source"]), str(m["kind"])
		if source == "" || kind == "" {
			continue
		}
		switch e.Kind {
		case abi.EvMuteSet:
			b.mutes[muteKey(source, kind)] = &Mute{
				Source: source, Kind: kind, Why: str(m["why"])}
		case abi.EvMuteRemoved:
			delete(b.mutes, muteKey(source, kind))
		}
	}
}

// mutedLocked 这条要不要闭嘴. 调用方必须持锁.
func (b *SignalBus) mutedLocked(s abi.Signal) bool {
	m, ok := b.mutes[muteKey(s.Source, s.Kind)]
	if !ok {
		return false
	}
	m.N++
	return true
}
