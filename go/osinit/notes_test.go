package osinit

import (
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 记住的事必须跨重启活着 —— **这一层丢了最让人恼火**:
// 地点丢了他还能到了那儿重说一句, 而"我老婆生日"这种事他不会想到
// 要再说一遍, 他会以为它记着
func TestNotesSurviveRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	n := NewNotes(log)
	if _, err := n.Set("", "老婆生日", "3 月 2 号"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Set("u1", "车牌", "尾号 517"); err != nil {
		t.Fatal(err)
	}

	// 换一台"开机"
	again := NewNotes(log)
	again.Restore(flatten(log))
	if got, ok := again.Get("", "老婆生日"); !ok || got.Text != "3 月 2 号" {
		t.Fatalf("重启后忘了: %+v ok=%v", got, ok)
	}
	if got, ok := again.Get("u1", "车牌"); !ok || got.Text != "尾号 517" {
		t.Fatalf("按人归属的那条丢了: %+v ok=%v", got, ok)
	}
}

// 忘掉要跨重启也算数 —— 不然重启一次, 删掉的偏好自己回来了,
// 而用户没有任何办法知道为什么
func TestNotesForgetSurvivesRestart(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	n := NewNotes(log)
	n.Set("", "咖啡", "不喝")
	if !n.Forget("", "咖啡") {
		t.Fatal("忘不掉")
	}
	again := NewNotes(log)
	again.Restore(flatten(log))
	if _, ok := again.Get("", "咖啡"); ok {
		t.Fatal("删掉的那条重启后自己回来了")
	}
}

// 同一句话说三遍不该让账本长三条 —— 跟 People.Know 同一条规矩
func TestNotesSameTextDoesNotGrowLedger(t *testing.T) {
	log := NewEventLog(func() int64 { return time.Now().UnixMilli() })
	n := NewNotes(log)
	for i := 0; i < 3; i++ {
		n.Set("", "咖啡", "不喝")
	}
	got := 0
	for _, evs := range log.Snapshot() {
		for _, e := range evs {
			if e.Kind == abi.EvNoteSet {
				got++
			}
		}
	}
	if got != 1 {
		t.Fatalf("同样的内容记了 %d 条, 该只有 1 条", got)
	}
}

// 同键是改, 不是多一条
func TestNotesSameKeyOverwrites(t *testing.T) {
	n := NewNotes(nil)
	n.Set("", "咖啡", "不喝")
	n.Set("", "咖啡", "只喝美式")
	list := n.List("")
	if len(list) != 1 || list[0].Text != "只喝美式" {
		t.Fatalf("同键没覆盖: %+v", list)
	}
}

// 别人的事不给 —— 理由跟 World.Text 那条一样: **不是保密, 是别答错**.
// 公有的那些照样给: 一条"家里 wifi 密码"没有归属, 但对谁都有用
func TestNotesListGivesMineAndShared(t *testing.T) {
	n := NewNotes(nil)
	n.Set("", "wifi", "在路由器背面")
	n.Set("u1", "车牌", "517")
	n.Set("u2", "车牌", "918")
	list := n.List("u1")
	var keys []string
	for _, x := range list {
		keys = append(keys, x.Key+"/"+x.Who)
	}
	if len(list) != 2 {
		t.Fatalf("该给 2 条(我的 + 公有的), 给了 %v", keys)
	}
	for _, x := range list {
		if x.Who == "u2" {
			t.Fatalf("把别人的事给出来了: %v", keys)
		}
	}
}

// 太长的当场拒 —— 截断的话用户以为记住了整句, 而它记住的是半句
func TestNotesRejectsTooLong(t *testing.T) {
	n := NewNotes(nil)
	if _, err := n.Set("", "长", strings.Repeat("字", maxNoteText+1)); err == nil {
		t.Fatal("超长的该拒")
	}
}

// 满了报错, 不静默丢最旧的: 悄悄丢掉的话, 用户会在几个月后发现它忘了
// 一件他明确让它记住的事, 而中间没有任何一处说过
func TestNotesFullSaysSo(t *testing.T) {
	n := NewNotes(nil)
	for i := 0; i < maxNotes; i++ {
		if _, err := n.Set("", key(i), "x"); err != nil {
			t.Fatalf("第 %d 条就记不下了: %v", i, err)
		}
	}
	if _, err := n.Set("", "再来一条", "x"); err == nil {
		t.Fatal("满了该报错")
	}
	// 满了之后**改已有的那条照样成**: 拒掉的话用户连纠正都做不了
	if _, err := n.Set("", key(0), "y"); err != nil {
		t.Fatalf("满了之后改不了已有的那条: %v", err)
	}
}

func key(i int) string { return "k" + string(rune('a'+i%26)) + string(rune('a'+i/26)) }
