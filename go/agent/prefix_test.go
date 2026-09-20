package agent

import (
	"regexp"
	"strings"
	"testing"
)

// 前缀缓存 —— **这一组守的是钱**.
//
// ── 为什么值得一组测试 ──
//
//	系统段是每次请求的前缀. 前缀缓存是**线性逐字节**比对的:
//	某一处一分叉, 它后面全部不共享.
//
//	所以往系统段里拼任何一个"会变的东西", 代价都不是那几个字 ——
//	是整个系统段(约 19K)加工具表, 每变一次全付一遍:
//
//	  拼当前时间   → 每分钟作废一次
//	  拼当前位置   → 人一动就作废
//	  拼天气       → 每次拉完就作废
//
//	而这三样恰恰是这套系统新长出来的能力, **最容易顺手塞进去**.
//	prompt.go 里那段"刻意不给当前时间"的注释拦不住下一个人 ——
//	注释不会失败, 测试会.
//
// ── 会变的东西该走哪条路 ──
//
//	工具结果. 它落在消息列表的尾巴上, 前面的前缀原样共享 ——
//	what_now / how_long_to 都是这么做的.

// **系统段不许读时钟**.
//
//	读源码判 —— 跟 restore_test 里那道闸同一个办法: 维护一份"不许出现
//	的东西"的清单, 等于把同一个"记得改两处"的问题换个地方放
func TestPromptNeverReadsTheClock(t *testing.T) {
	src := promptSource(t)
	for _, bad := range []string{"time.Now(", "time.Since(", "Format(\"2006"} {
		if strings.Contains(src, bad) {
			t.Fatalf("系统段里出现了 %s —— 前缀每变一次, 整个系统段加工具表"+
				"(约 19K)全部重付. 会变的东西走工具结果那条路, 它在尾巴上", bad)
		}
	}
}

// 同样的输入必须出同样的字节 —— 一个字都不许飘.
//
//	map 遍历顺序、拼接顺序里的任何一点不确定, 表现出来都是"缓存偶尔
//	不命中", 而那是最难查的一类账单问题
func TestPromptIsByteStable(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	first := BuildSystemPromptAs(ts, "你负责盯线上", "/work/ops", "", "")
	for i := 0; i < 20; i++ {
		if got := BuildSystemPromptAs(ts, "你负责盯线上", "/work/ops", "", ""); got != first {
			t.Fatal("同样的输入出了不同的字节 —— 缓存会偶尔不命中, 而那最难查")
		}
	}
}

// 派 N 个 bot 时, **分界线之前必须逐字节相同**.
//
//	prompt.go 那段注释说的就是这件事: 所有 per-bot 的内容收拢到一段
//	连续的尾巴里, 前面九层加工具表全队共享. 谁往前面塞一行岗位名,
//	后面就全不共享了 —— 而账单上看到的是"多派几个 bot 就贵得多"
func TestPromptSharesPrefixAcrossBots(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	a := BuildSystemPromptAs(ts, "你负责盯线上", "/work/ops", "", "")
	b := BuildSystemPromptAs(ts, "你负责写文案", "/work/writer", "", "")

	shared := 0
	for shared < len(a) && shared < len(b) && a[shared] == b[shared] {
		shared++
	}
	// ── 判据是"整段共享层逐字节相同", 不是一个百分比 ──
	//
	//	原来查的是"共享前缀 ≥80%". 那个数在共享层 11888 字节的年代
	//	成立, 而共享层砍到 1829 之后**同样的结构只剩 61%** —— 分子
	//	变小了, 分母(per-bot 那段岗位/工作区)没变.
	//
	//	百分比从来不是要护的东西. 要护的是: **谁都别往分界线前面
	//	塞 per-bot 的内容**. 那件事直接查得到 —— 共享层原样出现在
	//	两份提示词的开头.
	head := SharedPrefix(ts)
	if !strings.HasPrefix(a, head) || !strings.HasPrefix(b, head) {
		t.Fatal("共享层不在两份提示词的开头 —— 有 per-bot 的内容跑到" +
			"分界线前面去了, 派 N 个 bot 就要付 N 份系统段")
	}
	if shared < len(head) {
		t.Fatalf("两个 bot 只共享了 %d 字节, 而共享层有 %d —— "+
			"分界线前面混进了 per-bot 的东西", shared, len(head))
	}
}

// 系统段里不许出现坐标、日期这类**长得就像会变的东西**.
//
//	这一条是给将来的: 世界模型现在只走工具, 而"顺手把当前位置写进
//	提示词"是一个太自然的下一步 —— 自然到不会有人觉得它需要讨论
func TestPromptCarriesNoVolatileFacts(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	got := BuildSystemPromptAs(ts, "你负责盯线上", "/work/ops", "", "")
	for _, pat := range []struct{ name, re string }{
		{"日期", `\d{4}-\d{2}-\d{2}`},
		{"时刻", `\d{2}:\d{2}:\d{2}`},
		{"经纬度", `\d{2}\.\d{4,},\s*\d{2,3}\.\d{4,}`},
	} {
		if regexp.MustCompile(pat.re).MatchString(got) {
			t.Fatalf("系统段里出现了%s —— 它一变, 整个前缀就废了。"+
				"会变的事实走 what_now 那条路", pat.name)
		}
	}
}
