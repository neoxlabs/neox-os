package agent

// 前缀复用率 —— 把"这一轮的缓存到底正不正常"变成一个能报警的判据.
//
// ── 为什么命中率本身不够 ──
//
// 这类序列是**正常**的: 97% 95% 98% **50%** 96% 94%.
// 那个 50% 不是缓存坏了 —— 上一轮模型一口气写了 5161 token 的输出,
// 于是这一轮 prompt 从 5990 涨到 11682, 新追加的那 5692 token
// 本来就没被缓存过. 前缀其实是全命中的.
//
// 反过来, 一次真正的前缀漂移可能表现成 85% —— 看着比 50% 好得多,
// 实际上是每一轮都在多付钱. **命中率把"追加了很多"和"缓存塌了"
// 混成了同一个数**, 而这两件事一个不用管、一个必须立刻查.
//
// 能区分它们的判据只有一个: **上一轮的 prompt, 这一轮还在不在缓存里**.
// 在 = 健康(不管这轮追加了多少); 不在 = 前缀断了.
//
// (这跟 cachefp.go 那套是两层: 那一层查的是"我们自己有没有把前缀改掉",
// 这一层查的是"供应商那边到底认没认". 两边都可能出问题, 而且症状一样 ——
// 账单悄悄翻几倍, 结果照常正确.)

// cacheBlock 供应商按块缓存, 不足一块的尾巴不进缓存. DeepSeek 是 64.
const cacheBlock = 64

// reuseFloor 低于这个比例才算前缀断了.
//
// ── 为什么是比例, 不是"差几块" ──
//
// 第一版按块设余量(差一块不算断), 结果**真机数据当场把它打红**:
// 一次健康的复用里, 供应商报回来的 cached 比上一轮能缓存的部分
// 少 167 token —— 两块半. 块边界怎么落、尾巴算不算, 是供应商的事,
// 我们数不准, 而按绝对块数设余量就等于在猜它的实现.
//
// 比例不用猜: 前缀真断了是**塌方**(0%、50%), 不是差几十个 token.
// 90% 这条线离两边都远得很.
const reuseFloor = 90

// minJudged 太短的上下文不判 —— 几百 token 的时候, 一个块的抖动
// 就能把比例带下 10 个点, 那是噪音不是信号.
const minJudged = 512

// cacheReuse 上一轮的前缀, 这一轮复用了多少.
//
//	prevPrompt <= 0 表示没有上一轮(第一轮), 一律不判 —— 第一轮
//	什么都没得复用, 报出来是纯误报.
//
//	pct 是给人看的百分比; cold 才是判据.
func cacheReuse(prevPrompt, cached int64) (pct int, cold bool) {
	if prevPrompt <= 0 {
		return 0, false
	}
	// 上一轮能进缓存的, 最多是它向下取整到整块的那部分
	cacheable := prevPrompt / cacheBlock * cacheBlock
	if cacheable <= 0 {
		return 0, false
	}
	pct = int(float64(cached) / float64(cacheable) * 100)
	if pct > 100 {
		pct = 100
	}
	if cacheable < minJudged {
		return pct, false
	}
	return pct, pct < reuseFloor
}
