package osinit

import (
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func muteBus(t *testing.T) (*SignalBus, *EventLog, *[]Digest) {
	t.Helper()
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	var got []Digest
	bus := NewSignalBus(log, func(d Digest) { got = append(got, d) }, SignalOptions{
		Window: 50 * time.Millisecond, Lateness: 10 * time.Millisecond,
	})
	return bus, log, &got
}

func muteSig(src, kind, id string) abi.Signal {
	return abi.Signal{ID: id, Source: src, Kind: kind,
		At: time.Now().UnixMilli(), KnownAt: time.Now().UnixMilli()}
}

// **报告认出了噪音, 却没有任何办法让它闭嘴.**
//
// 报告里那句建议是"考虑在采集端就砍掉" —— 而采集端是装在别人手机里的
// APK、或者别人跑着的 HA 桥. **那是个改不动的地方.**
// 于是这条建议永远只能看着, 校准闭环的最后一段是断的.
func TestMutedKindDoesNotReachDigest(t *testing.T) {
	bus, _, got := muteBus(t)
	bus.Mute("phone.mk", "phone.moved", "从没导致过通知")

	bus.Ingest(muteSig("phone.mk", "phone.moved", "a1"))
	bus.Ingest(muteSig("phone.mk", "phone.arrived", "a2"))
	time.Sleep(150 * time.Millisecond)
	bus.Tick()

	if len(*got) == 0 {
		t.Fatal("一份摘要都没出 —— 静音把不该静的也挡了")
	}
	for _, d := range *got {
		for _, it := range d.Items {
			if it.Kind == "phone.moved" {
				t.Fatal("静音的种类还是进了摘要")
			}
		}
	}
}

// **静音不是丢弃 —— 它照样落账.**
//
// 两条理由, 缺一不可:
//
//	① 用户问"我今天去过哪儿"要能答得出来(signals.txt 就是账本渲染的)
//	② **报告本身就是靠账本算的**. 静音的种类要是从账本里消失,
//	   "这条静音当初静得对不对"就永远无法复核 —— 静音会变成一个
//	   没人记得为什么的永久盲区
func TestMutedSignalStillLandsInLedger(t *testing.T) {
	bus, log, _ := muteBus(t)
	bus.Mute("phone.mk", "phone.moved", "太吵")
	bus.Ingest(muteSig("phone.mk", "phone.moved", "a1"))

	n := 0
	for _, e := range log.Replay(signalPID, 0) {
		if e.Kind == abi.EvSignal {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("账本里有 %d 条 —— 静音把它真丢了, 报告从此看不见它", n)
	}
}

// **按 source+kind, 不是只按 kind** ——
// 一个坏掉的传感器不该让所有同类的信号跟着闭嘴
func TestMuteIsPerSource(t *testing.T) {
	bus, _, got := muteBus(t)
	bus.Mute("ha.badsensor", "state.changed", "这个传感器坏了")

	bus.Ingest(muteSig("ha.badsensor", "state.changed", "b1"))
	bus.Ingest(muteSig("ha.livingroom", "state.changed", "b2"))
	time.Sleep(150 * time.Millisecond)
	bus.Tick()

	var kinds []string
	for _, d := range *got {
		for _, it := range d.Items {
			kinds = append(kinds, it.Source)
		}
	}
	if len(kinds) != 1 || kinds[0] != "ha.livingroom" {
		t.Fatalf("摘要里的来源是 %v —— 静一个传感器把同类的都静了", kinds)
	}
}

// 静音要活得过重启 —— 不然一次崩溃之后那个噪音种类又开始吵,
// 而用户以为自己已经处理过了
func TestMutesSurviveRestart(t *testing.T) {
	bus, log, _ := muteBus(t)
	bus.Mute("phone.mk", "phone.moved", "太吵")

	bus2, _, got2 := muteBus(t)
	bus2.RestoreMutes(log.Replay(signalPID, 0))
	bus2.Ingest(muteSig("phone.mk", "phone.moved", "c1"))
	bus2.Ingest(muteSig("phone.mk", "phone.arrived", "c2"))
	time.Sleep(150 * time.Millisecond)
	bus2.Tick()

	for _, d := range *got2 {
		for _, it := range d.Items {
			if it.Kind == "phone.moved" {
				t.Fatal("重启之后静音没了 —— 那个噪音种类又开始吵, " +
					"而用户以为自己已经处理过了")
			}
		}
	}
}

// 撤得掉, 而且撤了要能看见 —— 静音是个会被忘掉的决定
func TestUnmuteWorksAndIsVisible(t *testing.T) {
	bus, _, _ := muteBus(t)
	bus.Mute("phone.mk", "phone.moved", "太吵")
	if len(bus.Mutes()) != 1 {
		t.Fatalf("静音列表里有 %d 条", len(bus.Mutes()))
	}
	if !bus.Unmute("phone.mk", "phone.moved") {
		t.Fatal("撤不掉")
	}
	if len(bus.Mutes()) != 0 {
		t.Fatal("撤了还在列表里")
	}
}

// **静音期间来了多少条要能看见.**
//
// 看不见的话, 静音就是个没人记得为什么的永久盲区 ——
// 一个传感器修好了、或者一个种类突然变得重要了, 没有任何信号提醒人回来看
func TestMuteCountsWhatItSwallowed(t *testing.T) {
	bus, _, _ := muteBus(t)
	bus.Mute("phone.mk", "phone.moved", "太吵")
	for i := 0; i < 3; i++ {
		bus.Ingest(muteSig("phone.mk", "phone.moved", string(rune('x'+i))))
	}
	m := bus.Mutes()
	if len(m) != 1 || m[0].N != 3 {
		t.Fatalf("静音吞掉的条数记成了 %+v, 该是 3", m)
	}
}

// **静掉那个钟, 同域的温度传感器还得能过.**
//
// 这条是真数据逼出来的(S39): sensor.time 每分钟一条纯噪音, 而域粒度下
// 它跟真温度传感器共用一个来源 —— 静音表**下不了刀**.
// 采集端改成按实体报之后, 这里要能证明刀落得下去.
func TestMutingOneEntityLeavesItsSiblingsAlone(t *testing.T) {
	bus, _, got := muteBus(t)
	bus.Mute("ha.sensor.time", "device.state", "每分钟跳一次, 纯噪音")

	bus.Ingest(muteSig("ha.sensor.time", "device.state", "t1"))
	bus.Ingest(muteSig("ha.sensor.living_room_temp", "device.state", "t2"))
	time.Sleep(150 * time.Millisecond)
	bus.Tick()

	var srcs []string
	for _, d := range *got {
		for _, it := range d.Items {
			srcs = append(srcs, it.Source)
		}
	}
	if len(srcs) != 1 || srcs[0] != "ha.sensor.living_room_temp" {
		t.Fatalf("摘要里的来源是 %v —— 静一个钟把温度也静掉了", srcs)
	}
}
