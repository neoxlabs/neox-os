package agent

import (
	"strings"
	"testing"
)

// ── 一队 bot 的前缀缓存 ──
//
// 前缀缓存是**线性**的: [system][tools][msg0..k] 逐字节比对, 某一层一分叉,
// 它后面全部不共享. 所以"每个 bot 有自己的岗位和工作区"这件事, 放在头部
// 和放在尾部, 差的不是风格, 是**同一台机器上派 N 个 bot 时 N 份系统段
// 各付一次首次写入**.
//
// 这一组测试钉的就是那条分界线: 上面全队逐字节相同, 下面各不相同.

func twoBots() (string, string, *ToolSet) {
	ts := NewToolSet(DefaultTools())
	a := BuildSystemPromptAs(ts, "你负责查资料、读代码。", "/Users/x/.neox-os/work/research",
		"- 「上周那个报表」 6 轮 · 2 天前", "他让你别在回答前面加开场白。")
	b := BuildSystemPromptAs(ts, "你负责编译和跑测试。", "/Users/x/.neox-os/work/build",
		"", "")
	return a, b, ts
}

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// 两个 bot 的共享前缀必须**正好是** sharedLayers 那一段 —— 不多不少.
//
// 少了: 说明有 per-bot 的东西漏进了通用层(最容易漏的是 writable),
//
//	于是通用层整段跟着分叉, 后面全部不共享.
//
// 多了: 说明本该分开的东西其实两个 bot 一样, 那是测试写错了.
func TestTeamSharesEverythingUpToTheStation(t *testing.T) {
	a, b, ts := twoBots()
	got := commonPrefix(a, b)
	want := SharedPrefix(ts) + "\n\n"
	// **判据是"至少共享到分界线", 不是"正好等于"**: 两个 bot 的岗位描述
	// 头几个字凑巧一样(都以"你负责"开头)时, 字符级的共同前缀会多出几十字节.
	// 那是巧合, 不是设计, 拿它当判据只会得到一条按 persona 内容抖动的测试.
	if !strings.HasPrefix(got, want) {
		t.Fatalf("两个 bot 没共享到分界线就分叉了。\n共享了 %d 字节, 分界线在 %d 字节。\n"+
			"第一处分叉往后 80 字节: %q", len(got), len(want), tail80(got, a))
	}
	// 分叉必须落在岗位段里 —— 再往后就说明尾段没排在一起
	if len(got) > len(want)+len("## 你的岗位与工作区\n\n")+60 {
		t.Fatalf("共享了 %d 字节, 比分界线多出 %d —— 尾段的排布可能变了",
			len(got), len(got)-len(want))
	}
}

// 共享前缀里不许出现任何 per-bot 的字符串.
//
// 这是上一条的**独立判据**: 上一条比的是两个具体的 bot, 万一两个都
// 漏了同一个东西, 它们仍然"相同"而测试照样绿.
func TestSharedPrefixCarriesNoPerBotFacts(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	shared := SharedPrefix(ts)
	for _, leak := range []string{
		"/Users/x/.neox-os/work/research", // 工作区
		"你负责查资料",                          // 岗位
		"上周那个报表",                          // 最近对话
		"别在回答前面加",                         // 他纠正过的事
	} {
		if strings.Contains(shared, leak) {
			t.Fatalf("per-bot 的东西 %q 漏进了全队共享的那一段 —— "+
				"整段会跟着它分叉, 后面全部不共享", leak)
		}
	}
	// 反过来: 这一段必须真的存在.
	//
	//	原来的下限是 10000 字节 —— 那是九层 11888 字节的年代定的.
	//	**"够大"从来不是要护的东西**: 提示词砍到 1829 之后共享的
	//	绝对量变小了, 而共享这件事本身一点没变(仍然是全队逐字节相同).
	//	真要护的"分界线没往前挪"由上面那几条 leak 判据管着.
	if len(shared) < 800 {
		t.Fatalf("共享段只有 %d 字节 —— 分界线大概挪到前面去了", len(shared))
	}
}

// per-bot 的四段必须**连续**摆在一起, 中间不许夹通用内容.
//
// 夹一段进去的后果不是"顺序难看": 夹在中间的那段通用内容也变成了
// 分叉之后的东西, 白白从共享区掉出来.
func TestPerBotBlockIsContiguous(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	p := BuildSystemPromptAs(ts, "你负责编译。", "/w", "- 「甲」 1 轮 · 1 小时前", "他说过要先跑测试。")
	station := strings.Index(p, "## 你的岗位与工作区")
	recent := strings.Index(p, "## 这台机器上还有哪些对话")
	corr := strings.Index(p, "## 他纠正过你的事")
	out := strings.Index(p, "## 动手的方式")
	if station < 0 || recent < 0 || corr < 0 || out < 0 {
		t.Fatalf("少了某一段: station=%d recent=%d corrections=%d output=%d",
			station, recent, corr, out)
	}
	if !(station < recent && recent < corr && corr < out) {
		t.Fatal("per-bot 的三段不是连续摆在尾部 —— 中间夹了通用内容, " +
			"那段通用内容就白白从共享区掉出来了")
	}
	// 岗位段前面**紧挨着**的必须是共享段的结尾
	if p[:station] != SharedPrefix(ts)+"\n\n" {
		t.Fatal("岗位段不是紧接在共享段后面 —— 中间还有别的东西")
	}
}

// 空 persona / 空 recent / 空 corrections 的 bot, 共享的仍然是同一段.
//
// 这一条防的是"用 if 把某一层整个换掉"这种改法: 那样两个 bot 会在
// 更靠前的地方分叉, 而它不会体现在任何一条现有测试上.
func TestBareBotSharesTheSamePrefix(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	bare := BuildSystemPromptAs(ts, "", "/w", "", "")
	full := BuildSystemPromptAs(ts, "你负责编译。", "/other", "- 「甲」 1 轮 · 1 小时前", "他说过要先跑测试。")
	if !strings.HasPrefix(commonPrefix(bare, full), SharedPrefix(ts)+"\n\n") {
		t.Fatal("光身子的 bot 跟带全套的 bot 没共享到同一段前缀")
	}
	// 没有岗位描述时, 工作区那一句仍然要在 —— 它是硬事实, 不是可选装饰
	if !strings.Contains(bare, "你的工作区是 /w") {
		t.Fatal("没给 persona 就把工作区也吞了 —— 它会以为自己无处可写")
	}
}

// 同一个 bot 反复组装必须逐字节一样 (TestPromptByteStable 的 persona 版).
func TestPersonaPromptIsByteStable(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	first := BuildSystemPromptAs(ts, "你负责编译。", "/w", "- 「甲」 1 轮 · 1 小时前", "他说过要先跑测试。")
	for i := 0; i < 50; i++ {
		if BuildSystemPromptAs(ts, "你负责编译。", "/w", "- 「甲」 1 轮 · 1 小时前", "他说过要先跑测试。") != first {
			t.Fatal("带 persona 的提示词不稳定 —— 这个 bot 每一轮的缓存都会全废")
		}
	}
}

func tail80(prefix, full string) string {
	s := full[len(prefix):]
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// 项目的目标和进度**放在工作区里, 不放进提示词**.
//
// 注入一份摘要看着更省事, 但那份一动手就过期: 它刚把某项改成做完了,
// 系统段里还写着"未开始" —— 而系统段是进程启动时定死的. 而且那是
// 每一轮都要付的钱.
func TestProjectStateIsReadNotInjected(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	p := BuildSystemPromptAs(ts, "你负责 OA", "/Users/x/AI/oa", "", "")
	if !strings.Contains(p, "PLAN.md") {
		t.Fatal("没告诉它工作区里的 PLAN.md 是进度 —— 新起的进程根本不知道有这个文件")
	}
	if !strings.Contains(p, "干完一项就把状态改掉") {
		t.Fatal("只让它读不让它写, 进度就永远停在第一次那份")
	}
	// **约定归约定, 不许把内容塞进来**: 系统段里不该出现具体的功能条目
	for _, leak := range []string{"P0", "已完成", "未开始", "✅"} {
		if strings.Contains(p, leak) {
			t.Errorf("提示词里出现了进度内容 %q —— 它会过期, 而且每轮都付钱", leak)
		}
	}
	// 这一条属于**岗位那一段**(per-bot 尾巴), 不该跑进全队共享的前缀里
	if strings.Contains(SharedPrefix(ts), "PLAN.md") {
		t.Error("这句话进了共享段 —— 没有工作区的 bot 也会被告知去读一个不存在的文件")
	}
}

// 接活时**没有 CHARTER.md 就先写一份**.
//
// PLAN.md 会被两个 bot 自发在写(进度好写, 每天都在动), 而 CHARTER.md
// 一个都没有 —— 被拉进 OA 项目的新人第一句就是"目录里没有 CHARTER.md,
// 验收标准没单独成文". 它知道缺, 但没人让它补.
//
// 缺的代价是**没人知道什么算干完**: "你去干这个 OA 项目, 要做成…" 这句
// 只说一次, 滚上去就没了, 换个进程看不见, 后面拉进来的人从头就不知道.
func TestCharterGetsWrittenNotJustRead(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	p := BuildSystemPromptAs(ts, "你负责 OA", "/Users/x/AI/oa", "", "")
	if !strings.Contains(p, "CHARTER.md") {
		t.Fatal("连这个文件名都没提")
	}
	// 光让它读不够 —— 一个从来没被写出来的文件, 读多少遍都是空的
	if !strings.Contains(p, "先写一份") {
		t.Fatal("只让读不让写: CHARTER.md 就永远不会存在, 而它恰恰是新人唯一的依据")
	}
	// 补的时机是**接活那一刻** —— 那时候依据(用户刚说的话)就在眼前;
	// 事后再补就只能靠回忆, 而回忆正是这套东西一直在防的东西
	if !strings.Contains(p, "接了一摊活") {
		t.Error("没说清什么时候写 —— 不定时机的规矩等于没有")
	}
	// 同样属于岗位段: 没有工作区的 bot 不该被要求写这个文件
	if strings.Contains(SharedPrefix(ts), "CHARTER.md") {
		t.Error("这句话进了共享段 —— 没派活的 bot 也会去建一份空章程")
	}
}

// **边界由谁挡, 要按这台机器的实情说**.
//
// 开发模式的 console 走 dev 模式(in-proc), 让 bot 用 run 执行
// 「echo x 重定向到 /tmp/xxx」—— 文件当场写出去了, 工作区外面, 没有审批、
// 没有拦截. 而提示词一直写着"卷外一个字节都改不了, 你起的命令继承同一套规则".
//
// 这是这套东西自己最看重的一条(TRAPS A4: 边界只有被强制时才存在).
// 说了做不到的话, 后果不是"少一层保护", 是**用户以为有一层保护而其实没有**.
func TestBoundaryClaimMatchesReality(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	loose := BuildSystemPromptIn(ts, "", "/w", "", "", false)
	strict := BuildSystemPromptIn(ts, "", "/w", "", "", true)

	// 挡不住的那台: 必须说清"靠你自觉", 而且必须点名 run 那条路 ——
	// 不点名的话它只会以为自己被全面保护着
	if !strings.Contains(loose, "这条边界靠你自觉") {
		t.Error("挡不住却没说 —— 它会以为越界会被拦")
	}
	// **判据要够窄**: "run" 这个词在工具清单里本来就有, 拿它当判据的话
	// 把那句话整个删掉测试也照样绿(变异验证时就是这么发现的)
	if !strings.Contains(loose, "你用 run 起的命令不受约束") {
		t.Error("没点名 run 那条路: 文件工具拦得住, run 拦不住, 只说一半等于没说")
	}
	// 挡不住的那台**不许**说"由内核挡着" —— 判据要够窄:
	// 环境层里有"有的机器上是内核挡着"这种中性说法, 那是在解释两种情况,
	// 拿它当判据会永远红
	if strings.Contains(loose, "这条边界**由内核挡着**") {
		t.Error("挡不住却说内核挡着")
	}

	// 挡得住的那台: 说挡得住, 而且不许说"靠你自觉"(那会让它多余地束手束脚)
	if !strings.Contains(strict, "这条边界**由内核挡着**") {
		t.Error("真挡得住却没说")
	}
	if strings.Contains(strict, "这条边界靠你自觉") {
		t.Error("内核挡着还说靠自觉")
	}

	// 老签名**默认按最保守的说**: 判错的代价不对称
	if !strings.Contains(BuildSystemPromptAs(ts, "", "/w", "", ""), "这条边界靠你自觉") {
		t.Error("老签名默认成了'挡得住' —— 那是把没有的保护说成有")
	}
}
