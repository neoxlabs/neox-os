package osinit

import (
	"os"
	"time"
)

// 等账本安静下来.
//
// ── 为什么不能猜一个 sleep ──
//
// 验收脚本要等"最后一个窗口收完"才能判. 猜多少才够?
// 窗口 + 容忍度 + 补传的静默期 + 主动进程判断的时间 —— 每一项都可配,
// 而最后一项取决于模型这次快不快. **猜不出来.**
//
// 猜早了的后果不是报错, 是**证据少了一块而结论看起来没变**:
// 最后几条没进账本, 而判决照样说"通过". 这跟 S50 那次(时间压缩把
// 两次开门弄没了)是同一类, 只是换了个地方.
//
// 所以不猜: 账本连着 quiet 这么久没长, 就是收完了.
//
// ── 超时也要返回 ──
//
// 一个永远等下去的验收比等早了更糟: 它会挂在 CI 里, 而挂住的东西
// 没有人会去看第二次.
func WaitQuiet(path string, quiet, max time.Duration) (grew bool) {
	const poll = 50 * time.Millisecond
	deadline := time.Now().Add(max)
	var lastSize int64 = -1
	lastChange := time.Now()

	for time.Now().Before(deadline) {
		st, err := os.Stat(path)
		switch {
		case err != nil:
			// 账本还没出现 —— **不算安静**: 那说明进程还没起来,
			// 这时候返回等于跳过整场
			lastChange = time.Now()
		case st.Size() != lastSize:
			if lastSize >= 0 {
				grew = true
			}
			lastSize, lastChange = st.Size(), time.Now()
		default:
			if time.Since(lastChange) >= quiet {
				return grew
			}
		}
		time.Sleep(poll)
	}
	return grew
}

// HasContent 这份账本里到底有没有东西.
//
// **跟"这几十秒里长没长"是两件事**: 例如第一天正在正常跑,
// 8 条信号摊在半小时里(平均四分钟一条), 拿 45 秒的观察窗口去看,
// 它确实一条都没长. 拿那个当"采集器没接上"是误报, 而脚本会照着它
// 掐掉一场好好的验收.
//
// 一个稀疏但有内容的账本, 安静就是收完了.
func HasContent(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}
