package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/osinit"
)

/**
 * **缺省不限**: 一道用户自己没设过的上限, 突然把一件正经活拦腰砍断,
 * 比没有上限更糟 —— 他不知道发生了什么, 只看到"它干到一半不干了".
 */
func TestCap没设上限就一直放行(t *testing.T) {
	cap := newTurnCap(func() int64 { return 0 })
	for i := 0; i < 50; i++ {
		cap.note("小登", 1_000_000)
		if err := cap.over("小登"); err != nil {
			t.Fatalf("没设上限却拦了: %v", err)
		}
	}
}

func TestCap超了要说清超在哪儿(t *testing.T) {
	cap := newTurnCap(func() int64 { return 100_000 })
	cap.note("小登", 60_000)
	if err := cap.over("小登"); err != nil {
		t.Fatal(err)
	}
	cap.note("小登", 60_000)
	err := cap.over("小登")
	if err == nil {
		t.Fatal("超了还放行")
	}
	// agent 认这个错才会去收尾, 而不是把做过的扔掉
	if !errors.Is(err, osinit.ErrBudgetExceeded) {
		t.Fatalf("错的种类不对, agent 认不出: %v", err)
	}
	// **话是给人看的**: 不许把 "budget exceeded" 这截英文挂在中文前面
	if strings.Contains(err.Error(), "budget exceeded") {
		t.Errorf("内部错误名漏到人眼前了: %v", err)
	}
	// 说清: 花了多少、上限多少、怎么接着干
	for _, want := range []string{"120k", "100k", "调高"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("没说清 %q: %v", want, err)
		}
	}
}

/**
 * **上限管的是"一轮"**: 跑飞的样子是一轮停不下来, 而一个干了三天的
 * bot 累计花得多是正常的. 按累计设上限, 到点之后那个 bot 就永久残废了.
 */
func TestCap新的一轮重新数(t *testing.T) {
	cap := newTurnCap(func() int64 { return 100_000 })
	cap.note("小登", 99_000)
	if err := cap.over("小登"); err != nil {
		t.Fatal(err)
	}
	cap.reset("小登")
	cap.note("小登", 99_000)
	if err := cap.over("小登"); err != nil {
		t.Fatalf("新的一轮还在算上一轮的账: %v", err)
	}
}

func TestCap各人各算(t *testing.T) {
	cap := newTurnCap(func() int64 { return 100_000 })
	cap.note("小登", 99_000)
	cap.note("小勤", 99_000)
	if err := cap.over("小勤"); err != nil {
		t.Fatalf("把别人花的算到他头上了: %v", err)
	}
}

/**
 * **记账口不能用 Spend 那条**.
 *
 *	内核在推理之后自己记账(osinit/context.go), 而 agent 调 Spend 只是
 *	零消耗地问一句"我超没超" —— 拿它当记账口, 数到的永远是 0, 上限
 *	就成了摆设. 设了 3000 的上限时, 一轮花掉 14153 也会照样跑完.
 *
 *	真正的数在 usage 那条事件里(送进去 + 吐出来), 跟"花了多少"那一页
 *	同一个来源 —— 用户在设置里填的那个数, 才对得上他看到的那个数.
 */
func TestCap数的是usage那条事件(t *testing.T) {
	cap := newTurnCap(func() int64 { return 10_000 })
	// 模拟 procSyscalls.Emit 那一路: prompt + completion
	cap.note("小登", asInt(float64(6000))+asInt(float64(1000)))
	if err := cap.over("小登"); err != nil {
		t.Fatal("还没到线就拦了")
	}
	cap.note("小登", asInt(2500)+asInt(int64(600)))
	if err := cap.over("小登"); err == nil {
		t.Fatal("到线了还放行")
	}
}

/**
 * **上限比一轮的起步还低, 得说出来**.
 *
 *	把每轮上限设成 6k 后, 列一次目录、读两个文件就可能到 11k 撞线 ——
 *	光把活和工具说给模型听就不止 6k. 那个数字
 *	注定什么都做不成, 而撞线那句话只说"超过上限", 于是用户看到的是
 *	每一轮都立刻停, 却不知道是自己填的数有问题.
 */
func Test上限比起步还低要说出来(t *testing.T) {
	c := newTurnCap(func() int64 { return 6000 })
	c.note("小丑", 11000) // 第一次来回就超了
	err := c.over("小丑")
	if err == nil {
		t.Fatal("超了却没拦")
	}
	if !strings.Contains(err.Error(), "起步") {
		t.Errorf("没告诉他这个数填小了: %v", err)
	}

	// 干了半天才超的, 不该说这句 —— 那时候上限本身没毛病
	c2 := newTurnCap(func() int64 { return 6000 })
	for i := 0; i < 5; i++ {
		c2.note("小丑", 1500)
	}
	err2 := c2.over("小丑")
	if err2 == nil {
		t.Fatal("超了却没拦")
	}
	if strings.Contains(err2.Error(), "起步") {
		t.Errorf("干了五个来回才超, 却怪用户上限填小了: %v", err2)
	}

	// 新的一轮要连"来回了几次"一起归零, 否则第二轮永远不算"第一次"
	c.reset("小丑")
	c.note("小丑", 11000)
	if err := c.over("小丑"); err == nil || !strings.Contains(err.Error(), "起步") {
		t.Errorf("新的一轮没归零: %v", err)
	}
}
