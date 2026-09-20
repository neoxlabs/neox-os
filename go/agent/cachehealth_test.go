package agent

import "testing"

// 判据要用**真实会话中的数据**验, 不是凭空设定的.
//
// 下面每一行都来自一次真实的两 bot 会话(deepseek-v4-flash).
func TestCacheReuseOnRealNumbers(t *testing.T) {
	for _, c := range []struct {
		name               string
		prevPrompt, cached int64
		wantCold           bool
	}{
		// 第一轮: 没有上一轮可复用, 不许报
		{"第一轮", 0, 0, false},
		// 正常的一串
		{"正常 97%", 5151, 5248, false},
		{"正常 95%", 5415, 5248, false},
		{"正常 98%", 5639, 5888, false},
		// **这一条是关键**: 命中率 50%, 但前缀是全命中的 ——
		// 上一轮输出了 5161 token, 这一轮 prompt 翻倍, 新的那半截
		// 本来就没缓存过. 报它就是误报.
		{"追加了一大截, 命中率 50% 但前缀没断", 5990, 5888, false},
		{"正常 96%", 11682, 11648, false},
		// 真断了: 上一轮 12139, 这一轮一个字都没复用
		{"前缀断了", 12139, 0, true},
		// 断了一半也是断
		{"前缀断了一半", 12139, 6000, true},
	} {
		pct, cold := cacheReuse(c.prevPrompt, c.cached)
		if cold != c.wantCold {
			t.Errorf("%s: prev=%d cached=%d → 复用 %d%%, cold=%v, 想要 cold=%v",
				c.name, c.prevPrompt, c.cached, pct, cold, c.wantCold)
		}
	}
}

// 块边界的零头不许触发报警 —— 误报比不报更糟.
//
// **这一条是被真机数据打红过的**: 第一版按"差一块"设余量, 而真实的
// 一次健康复用里 cached 比上一轮能缓存的部分少了 167 token(两块半).
// 块边界怎么落是供应商的事, 我们数不准.
func TestCacheReuseToleratesBlockBoundary(t *testing.T) {
	if _, cold := cacheReuse(5415, 5248); cold { // 健康复用中的这组数据
		t.Fatal("正常复用被报成断了 —— 每轮都喊, 真断那次就没人看了")
	}
	// 塌方才该说话
	if _, cold := cacheReuse(5990, 3000); !cold {
		t.Fatal("只复用了一半还不报 —— 那这个判据就是个装饰")
	}
}

// 太短的上下文不判 —— 一个块的抖动就能带走 10 个点, 那是噪音.
func TestCacheReuseStaysQuietOnTinyContexts(t *testing.T) {
	if _, cold := cacheReuse(300, 0); cold {
		t.Fatal("几百 token 的时候就开始喊了")
	}
}
