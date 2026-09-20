package agent

import (
	"strings"
	"testing"
	"time"
)

func proactiveTools(notify func(text, why string)) *ToolSet {
	return NewToolSet([]Tool{NotifyTool(notify)})
}

// **主动进程只有一个工具.**
//
// 它的职责是判断, 不是干活. 给它文件工具的话, 一个被外部信号唤醒的
// 常驻进程就成了一条间接执行路径 —— 而感知层收的是位置和来电,
// 它的攻击面本来就是全系统最大的.
func TestProactiveHasOnlyNotify(t *testing.T) {
	ts := proactiveTools(nil)
	if names := ts.Names(); len(names) != 1 || names[0] != "notify_user" {
		t.Fatalf("主动进程的工具表是 %v —— 它不该能读写文件或跑命令", names)
	}
	if caps := ProactiveCaps(); len(caps) != 0 {
		t.Fatalf("主动进程被给了能力 %v —— 它一条都不该有", caps)
	}
}

// **沉默必须是免费的: 不调工具就是不说.**
//
// 反过来做(默认转发它的话, 让它声明"这次不说")会失败, 原因不在模型笨:
// 一个被训练成有帮助的模型, 在"说点什么"和"明确宣布我不说"之间
// 一定倾向前者. 结构上让沉默免费, 比在提示词里求它闭嘴可靠.
func TestSilenceRequiresNoAction(t *testing.T) {
	var said []string
	ts := proactiveTools(func(text, why string) { said = append(said, text) })
	p := BuildProactivePrompt(ts)

	for _, want := range []string{"默认什么都不做", "不用表态", "没有人在等你说话"} {
		if !strings.Contains(p, want) {
			t.Fatalf("提示词没把沉默设成默认(缺 %q)", want)
		}
	}
	// 一轮什么工具都不调 —— 用户侧应该一个字都没有
	if len(said) != 0 {
		t.Fatal("没调工具却有话传出去了")
	}
}

// 三条判据要在提示词里, 而且要按顺序
func TestProactivePromptCarriesTheThreeTests(t *testing.T) {
	p := BuildProactivePrompt(proactiveTools(nil))
	for _, want := range []string{"错过了能不能补", "他现在能做什么动作吗", "你动过他的东西吗"} {
		if !strings.Contains(p, want) {
			t.Fatalf("三条判据缺了 %q", want)
		}
	}
	// 知情权那条不受预算和前两条限制
	if !strings.Contains(p, "无条件说") {
		t.Fatal("知情权那条没写成无条件 —— 动过用户的东西必须说")
	}
}

// **补传的历史不该被主动汇报.**
//
// 它们已经发生过了, 用户此刻做不了任何事 —— 那正是判据②说的噪音.
// 补传是用来回答"我今天去过哪儿"的, 不是用来敲门的.
func TestProactivePromptSaysBackfillIsNotForAnnouncing(t *testing.T) {
	p := BuildProactivePrompt(proactiveTools(nil))
	if !strings.Contains(p, "补传的历史") {
		t.Fatal("没告诉它补传的摘要不该主动汇报 —— 它会把昨天的行程念一遍")
	}
}

// notify_user 调了就要真的传出去, 而且空话要被拒
func TestNotifyToolDeliversAndRejectsEmpty(t *testing.T) {
	var got []string
	ts := proactiveTools(func(text, why string) { got = append(got, text+"|"+why) })
	tool, _ := ts.Get("notify_user")

	if _, err := tool.Run(Toolbox{}, map[string]any{"text": "  "}); err == nil {
		t.Fatal("空的 text 该被拒 —— 真要说就说清楚, 不说就别调")
	}
	out, err := tool.Run(Toolbox{}, map[string]any{
		"text": "11 点了，12 点的火车该出门了",
		"test": "irreversible", "why": "有截止且过了无法挽回"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "12 点的火车") {
		t.Fatalf("话没传出去: %v", got)
	}
	// 结果要告诉它"到此为止", 否则它会接着再说一遍
	if !strings.Contains(out, "不要再说别的") {
		t.Fatalf("notify 的结果没收口, 它可能接着再说一遍: %s", out)
	}
}

// 主动那份提示词要**小**.
//
// ── 判据从"比对话那份小得多"改成一个绝对数 ──
//
//	原来查的是比例. 那在对话提示词 11888 字节的年代成立, 而现在
//	它自己也砍到了 1829 —— 两份都小的时候, 比例说明不了任何事.
//
//	要护的本来就是"它只做一个判断, 别背一大摊规矩". 那是个绝对量.
func TestProactivePromptIsSmall(t *testing.T) {
	p := BuildProactivePrompt(NewToolSet(DefaultTools()))
	// 5500: 它自己那几层(三条判据 + 打扰预算)就这么大, 而对话那份
	// 现在是 3491 —— 两份都小的时候比例说明不了任何事, 看绝对量
	if len(p) > 5500 {
		t.Fatalf("主动提示词 %d 字节 —— 它只做一个判断, 不该这么大", len(p))
	}
}

// 提示词里提到的工具必须真的存在 —— 跟对话那套同一条规矩
func TestProactivePromptMentionsOnlyRealTools(t *testing.T) {
	p := BuildProactivePrompt(proactiveTools(nil))
	if strings.Contains(p, "read_file") || strings.Contains(p, "run ") {
		t.Fatal("提示词提到了主动进程没有的工具, 它会照着调然后撞墙")
	}
	if !strings.Contains(p, "notify_user") {
		t.Fatal("没提 notify_user, 它不知道怎么说话")
	}
}

// 字节稳定 —— 常驻进程的前缀每一轮都要发
func TestProactivePromptIsByteStable(t *testing.T) {
	first := BuildProactivePrompt(proactiveTools(nil))
	for i := 0; i < 50; i++ {
		if BuildProactivePrompt(proactiveTools(nil)) != first {
			t.Fatal("主动提示词不稳定 —— 常驻进程的缓存会一直断")
		}
	}
}

// **构造函数必须真的换掉提示词.**
//
// 头一版我用的是内嵌 + 影子方法, 那在 Go 里静默失效(没有虚派发):
// 编译通过、只测 BuildProactivePrompt 的话测试也绿, 而真跑起来
// 主动进程用的是对话进程那套两万字的提示词.
func TestProactiveLLMActuallyUsesProactivePrompt(t *testing.T) {
	ts := proactiveTools(nil)
	m := NewProactiveLLM(&LLM{Tools: ts, Writable: "工作目录"})
	got := m.systemPrompt()
	if !strings.Contains(got, "感知判断进程") {
		t.Fatal("主动模型用的还是对话提示词 —— 影子方法在 Go 里不会被调到")
	}
	if strings.Contains(got, "完成条件") {
		t.Fatal("主动提示词里混进了对话进程的层")
	}
}

// **"我不打扰"这句话必须在参数上就填不出来.**
//
// 如果允许这种参数, notify_user 会被调用来发送「（不打扰。刚通知过降压药…第 2 次是重复）」——
// 花掉一次打扰额度, 去告诉用户自己不打扰. 而提示词里明写了
// "不需要为'这次我不说'做任何解释".
//
// 措辞管不住就改结构: 逼它在三条判据里选一条, 一件不打扰的事
// 没有判据可选 —— 这跟"沉默必须免费"是同一个手法.
func TestNotifyRequiresOneOfTheThreeTests(t *testing.T) {
	var got []string
	ts := proactiveTools(func(text, why string) { got = append(got, text) })
	tool, _ := ts.Get("notify_user")

	for _, bad := range []string{"", "不打扰", "duplicate", "重复了所以不报", "urgent"} {
		_, err := tool.Run(Toolbox{}, map[string]any{
			"text": "（不打扰。这条是重复的）", "test": bad})
		if err == nil {
			t.Fatalf("test=%q 竟然放行了 —— 它会用这个工具来宣布自己不说话", bad)
		}
		if !strings.Contains(err.Error(), "什么都不要调") {
			t.Fatalf("报错没告诉它正确的做法是什么都不调: %v", err)
		}
	}
	if len(got) != 0 {
		t.Fatal("参数不合法却把话传出去了")
	}

	for _, ok := range []string{"irreversible", "actionable", "informed", "INFORMED"} {
		if _, err := tool.Run(Toolbox{}, map[string]any{
			"text": "该出门了", "test": ok, "why": "火车 12 点"}); err != nil {
			t.Fatalf("合法的 test=%q 被拒: %v", ok, err)
		}
	}
	if len(got) != 4 {
		t.Fatalf("合法调用没传出去: %d", len(got))
	}
}

// 判据要跟着话一起传出去 —— 事后要能回答"它当时凭什么打扰我"
func TestNotifyCarriesTheClaimedTest(t *testing.T) {
	var whys []string
	ts := proactiveTools(func(text, why string) { whys = append(whys, why) })
	tool, _ := ts.Get("notify_user")
	tool.Run(Toolbox{}, map[string]any{
		"text": "火车快开了", "test": "irreversible", "why": "过了就赶不上"})
	if len(whys) != 1 || !strings.HasPrefix(whys[0], "irreversible:") {
		t.Fatalf("判据没跟着传出去: %v", whys)
	}
}

// ── 闹钟的时间必须在本地验 ────────────────────────────────

// **它对用户许了一个做不到的诺 —— 这是最糟的一类失败.**
//
// 输入"今天下午三点半要去医院"时, 主动设提醒本身没有问题,
// 但算出来的时间戳是**七个月前**(模型不知道现在几点, 只能猜).
// OS 那边正确地拒了, 而**工具当场返回了 success**(设闹钟走单向的 Emit,
// OS 的拒绝回不到 agent), 于是对外会错误地返回"提醒设好了: 今天下午 2:45".
//
// 用户就此以为有人替他记着这件事了.
func TestRemindRejectsPastTimeLocally(t *testing.T) {
	var set []int64
	ts := NewToolSet([]Tool{RemindTool(func(at int64, text string) error {
		set = append(set, at)
		return nil
	}, nil)})
	tool, _ := ts.Get("remind_me")
	past := time.Now().Add(-30 * 24 * time.Hour).UnixMilli()

	_, err := tool.Run(Toolbox{}, map[string]any{"at": past, "text": "去医院"})
	if err == nil {
		t.Fatal("过去的时刻在工具这一侧就该被拒 —— " +
			"等 OS 拒的话, agent 拿到的是 success, 它会告诉用户'设好了'")
	}
	if len(set) != 0 {
		t.Fatal("被拒了还把闹钟发出去了")
	}
	// **报错必须带上"现在是几点"的毫秒数** —— 模型不知道现在几点
	// (提示词里刻意没有当前时间), 只说"你错了"它只能再猜一次
	if !strings.Contains(err.Error(), "毫秒时间戳") {
		t.Fatalf("报错没给现在的毫秒数, 它只能再猜一次: %v", err)
	}
}

// 一年以后多半是年份算错了. 判错的代价不对称:
// 拦下来最多重算一次, 放过去是一年后突然冒出一句莫名其妙的提醒
func TestRemindRejectsAbsurdlyFarFuture(t *testing.T) {
	ts := NewToolSet([]Tool{RemindTool(func(int64, string) error { return nil }, nil)})
	tool, _ := ts.Get("remind_me")
	far := time.Now().Add(3 * 365 * 24 * time.Hour).UnixMilli()
	if _, err := tool.Run(Toolbox{},
		map[string]any{"at": far, "text": "三年后"}); err == nil {
		t.Fatal("三年后的提醒被收下了 —— 多半是年份算错")
	}
}

// **成功时要回读"多久以后"** —— 绝对时间它看不出错, 相对时间一眼看得出.
//
// "01-21 08:21" 这种它自己看不出问题, 而"还有 158 天"一眼就不对.
func TestRemindEchoesRelativeTime(t *testing.T) {
	ts := NewToolSet([]Tool{RemindTool(func(int64, string) error { return nil }, nil)})
	tool, _ := ts.Get("remind_me")
	out, err := tool.Run(Toolbox{}, map[string]any{
		"at": time.Now().Add(2 * time.Hour).UnixMilli(), "text": "出发"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "小时后") {
		t.Fatalf("没回读'多久以后', 它没法自查时间算错没有: %s", out)
	}
	// 措辞从"回头看一眼这个时间对不对"改成了"回头自查两件事"
	// (加了提前量那条). 断言改成钉**意图**而不是钉那一句话 ——
	// 钉死措辞的测试会在每次改文案时红一次, 而它其实什么也没保护
	if !strings.Contains(out, "自查") {
		t.Fatalf("没让它自查: %s", out)
	}
}

// 正常的将来时刻要能设成
func TestRemindAcceptsNormalFutureTime(t *testing.T) {
	var got int64
	ts := NewToolSet([]Tool{RemindTool(func(at int64, _ string) error {
		got = at
		return nil
	}, nil)})
	tool, _ := ts.Get("remind_me")
	want := time.Now().Add(90 * time.Minute).UnixMilli()
	if _, err := tool.Run(Toolbox{},
		map[string]any{"at": want, "text": "该出发了"}); err != nil {
		t.Fatalf("正常的提醒被拒了: %v", err)
	}
	if got != want {
		t.Fatalf("发出去的时刻不对: %d vs %d", got, want)
	}
}

// **提前量: "几点要到"和"几点提醒"是两回事.**
//
// 当需求是"下午三点半要去医院, 打车四十分钟"时,
// 它把闹钟设在 15:30, 内容却是"该出发了" —— 到点时人应该已经在医院了.
//
// 工具结果必须保留这条检查, 因为删除它会让提前量约束失效.
func TestRemindResultAsksAboutLeadTime(t *testing.T) {
	ts := NewToolSet([]Tool{RemindTool(func(int64, string) error { return nil }, nil)})
	tool, _ := ts.Get("remind_me")
	out, _ := tool.Run(Toolbox{}, map[string]any{
		"at": time.Now().Add(3 * time.Hour).UnixMilli(), "text": "该出发了"})
	for _, want := range []string{"提前量", "减去路上的时间"} {
		if !strings.Contains(out, want) {
			t.Fatalf("工具结果没提醒它检查提前量(缺 %q):\n%s", want, out)
		}
	}
}

// **判据里缺了最要紧的一条: 他知不知道.**
//
// 真机对照日抓到的: 人在家、拿快递开了下门, 而它警告
// "前门刚刚被打开解锁了。如果不是你或家人开的，建议现在确认一下"
// —— 理由里**根本没提在不在家**, 尽管摘要里明明白白贴着"家里现在有人".
//
// 三条判据(错过能不能补 / 他能做什么 / 你动过他的东西)全是站在
// **事情**那一侧的, 没有一条问"**他已经知道了吗**". 而"他自己刚做的
// 那件事"恰恰是最常见的一类: 他开的门、他关的灯、他自己出的门.
//
// 告诉他一件他自己刚做的事, 不是打扰错了对象, 是**复读** ——
// 而复读几次之后, 他就不再看这个通道了.
//
// 这一条是**思路**不是脚本: 不列"门锁在家时不报"这种场景规则,
// 而是加一句判据, 让它自己去套(S55 给验收剧本定 Worthy 时,
// 收紧成的也正是这一句).
func TestProactivePromptAsksWhetherHeAlreadyKnows(t *testing.T) {
	p := BuildProactivePrompt(NewToolSet(nil))
	for _, want := range []string{"他已经知道", "自己"} {
		if !strings.Contains(p, want) {
			t.Fatalf("提示词里没有'他知不知道'这条判据(缺 %q):\n%s", want, p)
		}
	}
}

// **第四条判据直接影响误报率, 必须由测试固定.**
//
// 同一个对照日(人在家, 两次开门), 同一个模型, 只差这一条判据:
//
//	改前  22 次判断 · **开口 1 次**  "前门刚刚被打开解锁了…"
//	改后  22 次判断 · **开口 0 次**
//
// 提示词的回归**只有跑一整天才看得出来**(短探针可能在新旧两版都得到 0 次),
// 所以判据本身要有测试守着: 删掉这一条, 当场变红.
func TestFourCriteriaStayInOrder(t *testing.T) {
	p := BuildProactivePrompt(NewToolSet(nil))
	// 四条判据的**顺序**也是判据的一部分: "他已经知道了吗"要排在最前,
	// 因为后面三条问的都是"这件事要不要紧", 而它问的是"该不该由你说"
	idx := func(sub string) int { return strings.Index(p, sub) }
	known := idx("他已经知道")
	if known < 0 {
		t.Fatal("'他已经知道了吗'这条判据没了 —— 真机上它把对照日的" +
			"误报从 1 次降到 0 次")
	}
	for _, later := range []string{"错过了能不能补", "他现在能做什么", "你动过他的东西"} {
		if i := idx(later); i >= 0 && i < known {
			t.Fatalf("%q 排到了'他已经知道了吗'前面 —— 顺序反了: "+
				"后三条问'这件事要不要紧', 而它问'该不该由你说'", later)
		}
	}
	if !strings.Contains(p, "复读") {
		t.Fatal("没说清后果 —— 告诉他一件他自己刚做的事不是打扰错了对象, " +
			"是复读; 而复读几次之后他就不再看这个通道了")
	}
}
