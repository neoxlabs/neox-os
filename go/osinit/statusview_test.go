package osinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStatus(t *testing.T) (*StatusView, *Places, *Watches, *Timers, string) {
	t.Helper()
	dir := t.TempDir()
	p, w := NewPlaces(nil), NewWatches(nil)
	tm := NewTimers(nil, nil, nil)
	return NewStatusView(dir, p, w, tm), p, w, tm, dir
}

// **agent 要看得见自己设过什么, 否则无从撤起.**
//
// 撤销关注和提醒时，agent 若只能答"我这边没有删除的工具"，结果不是编造，
// 但缺口比"没有撤销工具"更深: **它看不见已经设过什么**，加十个撤销工具也没用.
func TestStatusShowsWhatIsActiveWithIDs(t *testing.T) {
	v, p, w, tm, dir := newStatus(t)
	p.Add("家", 31.88, 117.28, 0)
	w.Add("place.arrived", "家", "", "以后我一到家你就跟我说一声")
	tm.Set("th", time.Now().Add(3*time.Hour).UnixMilli(), "该吃药了")
	if err := v.Flush(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, statusViewName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	t.Logf("现状:\n%s", got)

	for _, want := range []string{"家", "[g1]", "以后我一到家", "[w1]", "该吃药了", "小时后"} {
		if !strings.Contains(got, want) {
			t.Fatalf("现状里缺 %q:\n%s", want, got)
		}
	}
}

// **撤掉的不能留在文件里** —— 留着的话 agent 会照着念一个不存在的东西.
//
// 这正是 state.txt 跟 signals.txt 分开的理由: 一个是现状(每次重写),
// 一个是流水(只增不改).
func TestStatusRewritesInsteadOfAppending(t *testing.T) {
	v, _, w, _, dir := newStatus(t)
	it, _ := w.Add("door.opened", "", "", "开门告诉我")
	v.Flush()
	w.Remove(it.ID)
	v.Flush()

	raw, _ := os.ReadFile(filepath.Join(dir, statusViewName))
	if strings.Contains(string(raw), "door.opened") {
		t.Fatalf("撤掉的关注还留在现状里:\n%s", raw)
	}
}

// **前缀自识别, 撤错对象是静默的.**
//
// 用户以为取消了提醒, 实际取消的是"到家告诉我" —— 而他不会立刻发现.
func TestCancelRoutesByIDPrefix(t *testing.T) {
	v, _, w, tm, _ := newStatus(t)
	watch, _ := w.Add("place.arrived", "家", "", "到家说一声")
	wake, _ := tm.Set("th", time.Now().Add(time.Hour).UnixMilli(), "吃药")

	if _, err := v.Cancel(wake.ID); err != nil {
		t.Fatalf("撤提醒失败: %v", err)
	}
	if len(tm.Pending()) != 0 {
		t.Fatal("提醒没撤掉")
	}
	if len(w.List()) != 1 {
		t.Fatal("撤提醒把关注也撤了")
	}

	if _, err := v.Cancel(watch.ID); err != nil {
		t.Fatalf("撤关注失败: %v", err)
	}
	if len(w.List()) != 0 {
		t.Fatal("关注没撤掉")
	}
}

// 认不出来的 id **明说**, 不做模糊匹配也不猜"最近那条" ——
// 猜错是静默的, 而报错至少给了方向
func TestCancelRefusesToGuess(t *testing.T) {
	v, _, _, _, _ := newStatus(t)
	for _, bad := range []string{"那个到家的", "x1", "1", ""} {
		out, err := v.Cancel(bad)
		if err == nil {
			t.Fatalf("认不出的 id %q 却返回了 %q", bad, out)
		}
		if !strings.Contains(err.Error(), statusViewName) {
			t.Fatalf("报错没告诉它去哪儿看真正的 id: %v", err)
		}
	}
	// 格式对但不存在的, 也要明说
	if _, err := v.Cancel("w99"); err == nil {
		t.Fatal("撤一个不存在的提醒却说成功了")
	}
}

// 内容没变就不重写 —— 这个文件频繁重算, 每次都写会让 mtime 一直跳
func TestStatusSkipsUnchangedWrites(t *testing.T) {
	v, _, w, _, dir := newStatus(t)
	w.Add("door.opened", "", "", "开门告诉我")
	v.Flush()
	fi1, _ := os.Stat(filepath.Join(dir, statusViewName))
	time.Sleep(20 * time.Millisecond)
	v.Flush()
	fi2, _ := os.Stat(filepath.Join(dir, statusViewName))
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("内容没变却重写了 —— mtime 一直跳")
	}
}

// 空的时候也要写, 而且要说清"没有" ——
// 一个不存在的文件跟"什么都没设"是两回事, agent 会以为功能不存在
func TestStatusWritesEvenWhenEmpty(t *testing.T) {
	v, _, _, _, dir := newStatus(t)
	v.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, statusViewName))
	if err != nil {
		t.Fatal("空状态就不写文件了 —— agent 会以为这个功能不存在")
	}
	if !strings.Contains(string(raw), "(没有)") {
		t.Fatalf("没说清是'没有'还是'坏了':\n%s", raw)
	}
}
