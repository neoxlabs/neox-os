package main

import (
	"fmt"
	"sync"

	"github.com/neox-os/neox-os/osinit"
)

/**
 * cap —— **一轮最多烧多少**.
 *
 * ── 为什么是"每轮"不是"每个 bot 一共" ──
 *
 *	跑飞的样子是**一轮停不下来**: 它在两个错误之间来回改, 每一步都
 *	花钱, 而每一步看起来都像在干活. 一个干了三天的 bot 累计花得多是
 *	正常的 —— 按累计设上限, 到点之后那个 bot 就永久残废了, 而那不是
 *	用户想要的.
 *
 * ── 为什么缺省不限 ──
 *
 *	一道用户自己没设过的上限, 突然把一件正经活拦腰砍断, 比没有上限
 *	更糟: 他不知道发生了什么, 只看到"它干到一半不干了".
 *
 * ── 撞线之后 ──
 *
 *	Spend 返回错误, agent 会**收尾**(说清做到哪儿了)而不是把做过的
 *	扔掉 —— 见 agent.Agent.Run 里那段. 手上的改动照样由宿主收进提交.
 */
type turnCap struct {
	mu   sync.Mutex
	used map[string]int64
	// rounds 这一轮跟模型来回了几次 —— 用来认出"上限比起步还低"
	rounds map[string]int
	// limit 现在的上限, 每次现问 —— 用户可能刚在设置里改过
	limit func() int64
}

func newTurnCap(limit func() int64) *turnCap {
	return &turnCap{used: map[string]int64{}, rounds: map[string]int{}, limit: limit}
}

// reset 新的一轮开始 —— 计数归零
func (c *turnCap) reset(bot string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.used, bot)
	delete(c.rounds, bot)
}

// note 记一笔 —— 数的是 usage 那条事件里的数(送进去 + 吐出来),
// 跟"花了多少"那一页同一个来源, 用户填的那个数才对得上
func (c *turnCap) note(bot string, tokens int64) {
	if c == nil || tokens <= 0 {
		return
	}
	c.mu.Lock()
	c.used[bot] += tokens
	c.rounds[bot]++
	c.mu.Unlock()
}

/**
 * over 这一轮超了没有.
 *
 *	agent 每一步之前会零消耗地问一次 —— 问的就是这个. 超了它会**收尾**
 *	(说清做到哪儿了)而不是把做过的扔掉.
 */
func (c *turnCap) over(bot string) error {
	if c == nil {
		return nil
	}
	cap := c.limit()
	if cap <= 0 {
		return nil // 没设上限
	}
	c.mu.Lock()
	used, rounds := c.used[bot], c.rounds[bot]
	c.mu.Unlock()
	if used < cap {
		return nil
	}
	/**
	 * **上限比一轮的起步还低, 得说出来**.
	 *
	 *	把上限设成 6k, 派了一摊正经活: 它列了一次目录、读了两个
	 *	文件, 就到 11k 撞线了 —— 光把活和工具说给模型听就不止 6k.
	 *	那个数字**注定什么都做不成**, 而设置页收下它的时候一个字没说,
	 *	撞线那句话也只说"超过上限", 于是用户看到的是每一轮都立刻停,
	 *	而他不知道是自己填的数有问题.
	 *
	 *	判据是**第一次来回就超了**: 那说明起步本身就比上限贵.
	 */
	extra := ""
	if rounds <= 1 {
		extra = fmt.Sprintf("。（这个上限比一轮的起步还低 —— 光把活和工具说给我听就要 %s，"+
			"填这么小基本干不了活）", countly(used))
	}
	/**
	 * **话是给人看的, 种类是给代码看的** —— 两件事别混在一句里.
	 *
	 *	用 fmt.Errorf("%w: …") 的话, 用户在界面上看到的是
	 *	"budget exceeded: 这一轮已经花掉…" —— 一句中文前面挂着一截
	 *	英文内部错误名. 自己包一层: 话干净, errors.Is 照样认得出.
	 */
	return capHit{fmt.Sprintf("这一轮已经花掉 %s token，超过你在设置里定的每轮上限 %s。"+
		"先收尾说清做到哪儿了；要接着干，把上限调高或者另起一句话%s",
		countly(used), countly(cap), extra)}
}

// countly 给人看的数字 —— 80 万比 800000 好读.
// (adopt.go 那个 human 说的是字节, 两回事)
func countly(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// capHit 撞线了 —— 话是干净的中文, 而 errors.Is 照样认得出它是哪一类
type capHit struct{ msg string }

func (e capHit) Error() string { return e.msg }
func (e capHit) Unwrap() error { return osinit.ErrBudgetExceeded }
