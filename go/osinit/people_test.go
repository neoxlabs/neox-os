package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// twoPhones 两个人各一台手机 —— 家用 OS 的常态
func twoPhones(t *testing.T, now time.Time) (*World, *Devices, *People) {
	t.Helper()
	log := NewEventLog(func() int64 { return now.UnixMilli() })
	people := NewPeople(log)
	devices := NewDevices(log)
	if _, err := people.Know(Person{ID: "u1", Name: "老刘"}); err != nil {
		t.Fatal(err)
	}
	if _, err := people.Know(Person{ID: "u2", Name: "老婆"}); err != nil {
		t.Fatal(err)
	}
	mustDeclare(t, devices, Device{ID: "p1", Name: "他的手机", Owner: "u1",
		Senses: []string{"place"}, Presents: []string{PresentNotify}})
	mustDeclare(t, devices, Device{ID: "p2", Name: "她的手机", Owner: "u2",
		Senses: []string{"place"}, Presents: []string{PresentNotify}})

	w := NewWorld(atClock(now))
	w.UseOwner(devices.OwnerOf)
	return w, devices, people
}

// **两个人的位置不能互相盖掉** —— 症状不是报错, 是答错:
// 他问"我在哪", 系统拿她十分钟前的位置回答, 而且答得很有把握
func TestTwoPeopleKeepSeparateLocations(t *testing.T) {
	now := time.Now()
	w, _, _ := twoPhones(t, now)

	w.Observe(abi.Signal{Source: "p1", Kind: "place.arrived", At: now.UnixMilli(),
		Body: map[string]any{"place": "公司"}})
	w.Observe(abi.Signal{Source: "p2", Kind: "place.arrived", At: now.UnixMilli(),
		Body: map[string]any{"place": "家"}})

	his := w.Text("u1")
	if !strings.Contains(his, "公司") {
		t.Fatalf("他的位置不见了: %q", his)
	}
	if strings.Contains(his, "家") {
		t.Fatalf("问他在哪, 答案里混进了她的位置: %q —— 这种错不报错, 只答错", his)
	}
	hers := w.Text("u2")
	if !strings.Contains(hers, "家") || strings.Contains(hers, "公司") {
		t.Fatalf("她那一份也串了: %q", hers)
	}
}

// 屋子的事实**每个人都看得见** —— 天气不属于任何人,
// 而"要不要带伞"是两个人都要问的
func TestHouseFactsAreSharedByEveryone(t *testing.T) {
	now := time.Now()
	w, _, _ := twoPhones(t, now)
	// 天气没有主人(接入器不是任何人的设备)
	w.Observe(abi.Signal{Source: "weather", Kind: "weather.now", At: now.UnixMilli(),
		Body: map[string]any{"text": "有雨 18°C"}})

	for _, who := range []string{"u1", "u2", ""} {
		if !strings.Contains(w.Text(who), "有雨") {
			t.Fatalf("%q 看不到天气 —— 而天气不属于任何人", who)
		}
	}
}

// 按人分组的那一份: bot 不知道正在跟它说话的是谁, 混成一列的话
// 它会说"你在公司", 而那是她
func TestTextAllGroupsByPerson(t *testing.T) {
	now := time.Now()
	w, _, people := twoPhones(t, now)
	w.Observe(abi.Signal{Source: "p1", Kind: "place.arrived", At: now.UnixMilli(),
		Body: map[string]any{"place": "公司"}})
	w.Observe(abi.Signal{Source: "p2", Kind: "place.arrived", At: now.UnixMilli(),
		Body: map[string]any{"place": "家"}})
	w.Observe(abi.Signal{Source: "weather", Kind: "weather.now", At: now.UnixMilli(),
		Body: map[string]any{"text": "晴"}})

	got := w.TextAll(people.Name)
	for _, want := range []string{"屋里", "老刘", "老婆", "公司", "家", "晴"} {
		if !strings.Contains(got, want) {
			t.Fatalf("分组那一份少了 %q:\n%s", want, got)
		}
	}
	// **名字, 不是 id** —— 一个显示成 uuid 的人在回答里没法认
	if strings.Contains(got, "u1") {
		t.Fatalf("把账号 id 报给了模型:\n%s", got)
	}
}

// 通知不该推给不相干的人 —— 她的规矩在他手机上响一次,
// 他就会把整个通道关掉
func TestPresentingPicksTheRightPersonsDevices(t *testing.T) {
	now := time.Now()
	_, devices, _ := twoPhones(t, now)
	// 客厅音箱是屋子的, 没有主人
	mustDeclare(t, devices, Device{ID: "spk", Name: "客厅音箱",
		Presents: []string{PresentNotify, PresentSpeak}})

	got := devices.Presenting(PresentNotify, "u1")
	names := map[string]bool{}
	for _, d := range got {
		names[d.Name] = true
	}
	if names["她的手机"] {
		t.Fatal("给他的通知会推到她手机上")
	}
	if !names["他的手机"] {
		t.Fatal("他自己的手机反而不在里面")
	}
	// 屋子公用的设备照样算 —— 一条给他的话在客厅音箱上念出来是合理的
	if !names["客厅音箱"] {
		t.Fatal("屋子公用的设备被排除了 —— 那客厅音箱就永远用不上")
	}
}

// 人比设备活得久 —— 换手机不该变成换个人
func TestPeopleSurviveRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	p := NewPeople(log)
	if _, err := p.Know(Person{ID: "u1", Name: "老刘"}); err != nil {
		t.Fatal(err)
	}
	var evs []abi.Event
	for _, b := range log.Snapshot() {
		evs = append(evs, b...)
	}
	next := NewPeople(nil)
	next.Restore(evs)
	if next.Name("u1") != "老刘" {
		t.Fatalf("重启之后不认得这个人了: %q", next.Name("u1"))
	}
}

// 名字没变就不落账 —— 每次开 app 落一条的话, 一年下来账本里
// 全是"张三还叫张三"
func TestPeopleDoNotSpamTheLedger(t *testing.T) {
	log := NewEventLog(func() int64 { return 1 })
	p := NewPeople(log)
	for i := 0; i < 10; i++ {
		if _, err := p.Know(Person{ID: "u1", Name: "老刘"}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, b := range log.Snapshot() {
		for _, e := range b {
			if e.Kind == abi.EvPersonKnown {
				n++
			}
		}
	}
	if n != 1 {
		t.Fatalf("同一个人报到 10 次落了 %d 条账 —— 一年下来全是这个", n)
	}
}
