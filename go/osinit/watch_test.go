package osinit

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func digestWith(kind, place string) Digest {
	return Digest{Count: 1, Items: []DigestItem{{
		Kind: kind, Sample: map[string]any{"地点": place}}}}
}

// **"以后我一到家你就跟我说一声"要真的成立.**
//
// 只回复"这个链路是通的"或"我可以现在接上"并不会创建关注,
// 而系统里根本没有"用户定的关注"这个概念 —— 主动进程只按三条判据判,
// 而"你到家了"几乎一条都过不了, 它会保持沉默.
func TestWatchFiresOnMatchingSignal(t *testing.T) {
	w := NewWatches(nil)
	if _, err := w.Add("place.arrived", "家", "", "以后我一到家你就跟我说一声"); err != nil {
		t.Fatal(err)
	}
	hits := w.Hits(digestWith("place.arrived", "家"))
	if len(hits) != 1 {
		t.Fatalf("到家了却没命中: %v", hits)
	}
	if !strings.Contains(hits[0], "家") {
		t.Fatalf("命中的话里没说是哪儿: %q", hits[0])
	}
	// 到公司不该响
	if h := w.Hits(digestWith("place.arrived", "公司")); len(h) != 0 {
		t.Fatalf("到公司也响了: %v", h)
	}
	// 别的种类不该响
	if h := w.Hits(digestWith("battery.level", "")); len(h) != 0 {
		t.Fatalf("电量变化也响了: %v", h)
	}
}

// **不许"什么都关注"** —— 那等于每条信号都打扰他一次, 比不做还糟.
//
// 而且它是**静默的灾难**: 用户以为设了个贴心的提醒, 拿到的是一天几十条
// 通知, 然后把整个通知关掉.
func TestWatchRejectsCatchAll(t *testing.T) {
	w := NewWatches(nil)
	if _, err := w.Add("", "", "随便说点什么", "帮我盯着点"); err == nil {
		t.Fatal("两个条件都空却收下了 —— 那会把每条信号都推给用户")
	}
}

// 只给种类不给地点也成立: "有人开门就告诉我"
func TestWatchByKindOnly(t *testing.T) {
	w := NewWatches(nil)
	w.Add("door.opened", "", "", "有人开门就告诉我")
	if len(w.Hits(digestWith("door.opened", ""))) != 1 {
		t.Fatal("开门没命中")
	}
	if len(w.Hits(digestWith("door.closed", ""))) != 0 {
		t.Fatal("关门也命中了")
	}
}

// 说两遍不该收到两条 —— 用户重复说一件事是常态
func TestWatchDeduped(t *testing.T) {
	w := NewWatches(nil)
	w.Add("place.arrived", "家", "", "到家告诉我")
	w.Add("place.arrived", "家", "", "记得我到家跟我说")
	if len(w.List()) != 1 {
		t.Fatalf("同一个关注存了 %d 份 —— 用户会收到两条一样的", len(w.List()))
	}
	if len(w.Hits(digestWith("place.arrived", "家"))) != 1 {
		t.Fatal("一次到家响了不止一声")
	}
}

// **落盘是它存在的理由的一半.**
//
// 关注语义是"以后", 而下一段对话是全新进程. 不落盘的话,
// 这个功能跟 agent 嘴上答应一句没有区别 —— 而那正是要修的那个失败.
func TestWatchSurvivesRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return 0 })
	w := NewWatches(log)
	w.Add("place.arrived", "家", "", "到家说一声")
	w.Add("door.opened", "", "", "开门告诉我")
	w.Remove(w.List()[1].ID)

	w2 := NewWatches(nil)
	w2.Restore(log.Replay(signalPID, 0))
	if len(w2.List()) != 1 {
		t.Fatalf("重启后剩 %d 条关注, 该是 1 条(另一条撤了)", len(w2.List()))
	}
	if len(w2.Hits(digestWith("place.arrived", "家"))) != 1 {
		t.Fatal("装回来的关注不响了 —— 那这个功能跟嘴上答应一句没区别")
	}
}

// 用户问"我让你盯着什么"要能原样答 —— 所以原话要留着
func TestWatchKeepsUserWords(t *testing.T) {
	w := NewWatches(nil)
	raw := "以后我一到家你就跟我说一声"
	w.Add("place.arrived", "家", "", raw)
	if got := w.List()[0].Raw; got != raw {
		t.Fatalf("原话没留住: %q", got)
	}
}

// 一份摘要里同一条关注只响一次 —— 一个窗口里到家两次是信号抖动, 不是两件事
func TestWatchFiresOncePerDigest(t *testing.T) {
	w := NewWatches(nil)
	w.Add("place.arrived", "家", "", "到家说一声")
	d := Digest{Count: 2, Items: []DigestItem{
		{Kind: "place.arrived", Sample: map[string]any{"地点": "家"}},
		{Kind: "place.arrived", Sample: map[string]any{"地点": "家"}},
	}}
	if h := w.Hits(d); len(h) != 1 {
		t.Fatalf("一份摘要里响了 %d 次", len(h))
	}
}

// 事件种类要对, 否则重建不出来
func TestWatchEventsRecorded(t *testing.T) {
	log := NewEventLog(func() int64 { return 0 })
	w := NewWatches(log)
	it, _ := w.Add("door.opened", "", "", "开门告诉我")
	w.Remove(it.ID)
	kinds := map[abi.EventKind]int{}
	for _, e := range log.Replay(signalPID, 0) {
		kinds[e.Kind]++
	}
	if kinds[abi.EvWatchSet] != 1 || kinds[abi.EvWatchRemoved] != 1 {
		t.Fatalf("事件没记全: %v", kinds)
	}
}

// **闹钟和关注的 ID 不能撞.**
//
// 两边原来都用 w%d. 没人按 id 查的时候看不出问题, 但一加撤销就是灾难:
// 撤销 w1 时, 两个对象都叫 w1 —— 而**撤错一个是静默的**:
// 他以为取消了提醒, 实际取消的是"到家告诉我".
func TestWatchAndWakeIDsDoNotCollide(t *testing.T) {
	log := NewEventLog(func() int64 { return 0 })
	w := NewWatches(log)
	tm := NewTimers(log, nil, nil)

	it, _ := w.Add("door.opened", "", "", "开门告诉我")
	wk, err := tm.Set("th", 4102444800000, "该吃药了") // 2100 年, 肯定是将来
	if err != nil {
		t.Fatal(err)
	}
	if it.ID == wk.ID {
		t.Fatalf("关注和闹钟的 id 撞了, 都是 %q —— 撤销时会撤错对象", it.ID)
	}
	// 前缀要能自识别
	if it.ID[0] != 'g' || wk.ID[0] != 'w' {
		t.Fatalf("id 前缀不自识别: 关注 %q, 闹钟 %q", it.ID, wk.ID)
	}
	// 重启之后仍然不撞
	w2 := NewWatches(nil)
	w2.Restore(log.Replay(signalPID, 0))
	it2, _ := w2.Add("lock.opened", "", "", "开锁告诉我")
	if it2.ID == it.ID {
		t.Fatalf("重启后新关注的 id 跟旧的撞了: %q", it2.ID)
	}
}

// **模型用 `*` 表达"不限", 而代码把它当成地名 —— 静默失效.**
//
// 工具描述写着"不限就留空"时, 模型可能写成 `*`.
// 关注被登记成 lock.opened @* , 登记成功、state.txt 里看着好好的、
// 用户也收到了"盯上了"的确认, 而它**永远不会命中** ——
// 门开了没人说话, 而没有任何一处会报错.
//
// 提示词改得再清楚也挡不住: 模型有几十种方式表达"不限", 每一种写错
// 都是静默的. **在收口这一侧认掉**才治本.
func TestWatchTreatsWildcardsAsUnlimited(t *testing.T) {
	for _, star := range []string{"*", "any", "ALL", "不限", "任意", "全部", "-"} {
		w := NewWatches(nil)
		it, err := w.Add("lock.opened", star, "", "开锁就告诉我")
		if err != nil {
			t.Fatalf("place=%q 被拒了: %v", star, err)
		}
		if it.Place != "" {
			t.Fatalf("place=%q 没被当成'不限', 存成了 %q —— 它永远不会命中",
				star, it.Place)
		}
		if len(w.Hits(digestWith("lock.opened", "家"))) != 1 {
			t.Fatalf("place=%q 的关注命不中任何地点", star)
		}
	}
	// kind 那一侧同理
	w := NewWatches(nil)
	it, err := w.Add("*", "家", "", "家里有任何动静都告诉我")
	if err != nil {
		t.Fatal(err)
	}
	if it.Kind != "" {
		t.Fatalf("kind=* 没被当成'不限': %q", it.Kind)
	}
	if len(w.Hits(digestWith("door.opened", "家"))) != 1 {
		t.Fatal("kind 不限的关注命不中")
	}
}

// 但两边都写"不限"仍然要拒 —— 那还是"什么都关注"
func TestWatchRejectsWildcardOnBothSides(t *testing.T) {
	w := NewWatches(nil)
	if _, err := w.Add("*", "any", "", "都告诉我"); err == nil {
		t.Fatal("两边都是通配却收下了 —— 那等于每条信号都打扰他一次")
	}
}

// **同一件事不能说两遍, 但也不能闷掉更有信息量的那条.**
//
// 设置"开锁就告诉我"后, 半夜有人开锁可能被告知两遍.
// 而主动那条的信息量明显更大(它发现时间戳跟备注对不上).
//
// 所以不闷掉摘要, 而是把"这些已经说过了"作为**事实**贴给主动进程,
// 让它自己判断还有没有别的要补.
func TestWatchNoteTellsProactiveWhatWasSaid(t *testing.T) {
	if WatchNote(nil) != "" {
		t.Fatal("没命中却贴了话 —— 那是每份摘要都多一段噪音")
	}
	note := WatchNote([]string{"开锁", "到达：家"})
	for _, want := range []string{"已经按用户设的关注直接告诉他了",
		"开锁", "到达：家", "别再把同一件事说一遍", "另有要说的"} {
		if !strings.Contains(note, want) {
			t.Fatalf("贴的话里缺 %q:\n%s", want, note)
		}
	}
	// **不能写成"别说话"** —— 那会闷掉它发现"时间对不上"这类的机会
	if strings.Contains(note, "不要开口") || strings.Contains(note, "保持沉默") {
		t.Fatalf("把主动进程整个闷掉了:\n%s", note)
	}
}

// **关注一个不存在的地点 = 永远不会命中.**
//
// 典型的同类错误是登记了"以后有人开锁就告诉我"之后, agent 登记了
// `lock.opened @家` —— 而那一轮从来没有命名过"家".
//
// 登记成功、state.txt 里看着好好的、用户收到了"盯上了"的确认,
// 而它**永远不可能命中**: 摘要里的地点名只可能来自已命名的地点.
//
// 跟通配符那次(S19)是同一个家族: 看着成了, 其实永远不响.
func TestWatchRejectsUnknownPlace(t *testing.T) {
	p := NewPlaces(nil)
	w := NewWatches(nil)
	w.UsePlaces(p)

	_, err := w.Add("lock.opened", "家", "", "以后有人开锁就告诉我")
	if err == nil {
		t.Fatal("盯了一个没命名过的地点却收下了 —— 它永远不会响, " +
			"而用户以为有人替他盯着")
	}
	// 报错要说清**怎么办**, 不是只说"不行"
	for _, want := range []string{"永远不会响", "name_place", "先不限地点"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("报错没给出路(缺 %q): %v", want, err)
		}
	}

	// 命名之后就能设
	p.Add("家", 31.88, 117.28, 0)
	if _, err := w.Add("lock.opened", "家", "", "开锁告诉我"); err != nil {
		t.Fatalf("地点存在了还是被拒: %v", err)
	}
	// 不限地点的一直都能设
	if _, err := w.Add("door.opened", "", "", "开门告诉我"); err != nil {
		t.Fatalf("不限地点的被拒了: %v", err)
	}
}

// 报错里要列出**现在认得哪些地点** —— 只说"不认识家"的话,
// agent 只能再猜一个名字; 给了清单它才能跟用户对上
func TestUnknownPlaceErrorListsKnownOnes(t *testing.T) {
	p := NewPlaces(nil)
	p.Add("公司", 31.86, 117.28, 0)
	w := NewWatches(nil)
	w.UsePlaces(p)
	_, err := w.Add("place.arrived", "家", "", "到家告诉我")
	if err == nil || !strings.Contains(err.Error(), "公司") {
		t.Fatalf("没列出现在认得的地点: %v", err)
	}
}

// 没装地点表时不拦 —— 测试和没接感知层的场景不该被这条挡住
func TestWatchWithoutPlacesDoesNotBlock(t *testing.T) {
	w := NewWatches(nil)
	if _, err := w.Add("place.arrived", "家", "", "到家告诉我"); err != nil {
		t.Fatalf("没装地点表却拦了: %v", err)
	}
}
