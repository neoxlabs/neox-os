package main

import (
	"os"

	"github.com/neox-os/neox-os/osinit"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// **一个 phase 有人发、没人渲染 = 用户什么都看不见.**
//
// 这道闸跟 S32(忘了恢复状态)、S34(忘了接命令)是同一个形状.
// 它一写出来就抓到六个: approved / empty_done / netns_degraded /
// reclaim / start / verify —— 全都发了事件, 而终端一个字不打.
//
// 最刺眼的是 netns_degraded: os.go 那儿写着"静默降级是不行的 ——
// 之后'服务起来了连不上'会查不到根", 事件也老老实实发了,
// **而渲染这一步把它吃掉了**. 意图在最后一米被抵消.
// silentPhases 故意不显示的. **沉默可以, 但必须是写下来的决定** ——
// 空着的话这道闸挡不住"忘了写", 而那正是它要挡的东西
var silentPhases = map[string]string{
	"start": "宿主自己已经打过'起了进程 pXX'了, 再来一行是重复; " +
		"真机账本里 start 有 174 次, 每次多一行就是 174 行噪音",
	// 下面这四个由 osRequests 处理, 而且它自己打了更好的一行
	// 提醒、地点、关注和取消事件已经由 osRequests 输出更完整的一行；
	// 再渲染会在具体内容下面重复一个没有信息量的 phase 名称。
	"remind":     "osRequests 处理并自己打了 ⏰ 那一行",
	"name_place": "osRequests 处理并自己打了 📍 那一行",
	"watch":      "osRequests 处理并自己打了 👁 那一行",
	"cancel":     "osRequests 处理并自己打了 ✂ 那一行",
}

func TestEveryEmittedPhaseIsRendered(t *testing.T) {
	emitted := map[string]bool{}
	// **agentside.go 原来不在扫描范围里** —— 而 remind/name_place/
	// watch/cancel 全从那儿发出来. 盲区后面正好藏着一个回归:
	// S36 给渲染加了兜底之后, 这四个开始被多打一行, 而闸没抓到.
	// 加一个文件进来只要一行, 漏掉一个文件是一整类看不见的东西
	for _, f := range []string{"../../agent/agent.go", "../../osinit/os.go",
		"agentside.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`"phase": *"([a-z_]+)"`).
			FindAllStringSubmatch(string(raw), -1) {
			emitted[m[1]] = true
		}
	}
	if len(emitted) < 15 {
		t.Fatalf("只扫出 %d 个 phase —— 正则跟源码对不上了, 这道闸形同虚设",
			len(emitted))
	}
	var blind []string
	for p := range emitted {
		if silentPhases[p] != "" {
			continue // 故意不显示的, 理由写在表里
		}
		if renderStep(map[string]any{"phase": p}) == "" {
			blind = append(blind, p)
		}
	}
	sort.Strings(blind)
	if len(blind) > 0 {
		t.Fatalf("这些 phase 发了事件却什么都不显示: %s\n"+
			"用户看到的是彻底的沉默 —— 而这个文件反复吃亏的就是这一类",
			strings.Join(blind, " "))
	}
}

// **不认得的 phase 也要打一行.**
//
// 沉默比丑陋糟得多: 一行看不懂的字至少说明"有事发生了",
// 而什么都没有会被读成"它卡住了".
func TestUnknownPhaseStillSaysSomething(t *testing.T) {
	if renderStep(map[string]any{"phase": "什么新东西", "x": 1}) == "" {
		t.Fatal("不认得的 phase 什么都不打 —— 用户会以为它卡住了")
	}
}

// **数字要按整数打.**
//
// 过 JSON 之后数字回来全是 float64, %v 对它用 %g —— 值一大就是科学计数法.
// 窗口为 1048576 时, 输出会变成
// `窗口=1.048576e+06 tok … → 2.014118e+06 字节`.
func TestNumbersAreNotScientific(t *testing.T) {
	out := renderStep(map[string]any{"phase": "budget",
		"budget": map[string]any{"contextTokens": 1048576.0,
			"bytesPerToken": 1.92, "samples": 8.0, "budgetBytes": 2014118.0}})
	if strings.Contains(out, "e+") {
		t.Fatalf("给人看的数字成了科学计数法: %s", out)
	}
	if !strings.Contains(out, "1048576") {
		t.Fatalf("窗口大小没打出来: %s", out)
	}
}

// **"钱花完了"不能报成"步数用完了".**
//
// 这条路径不止步数上限一种, 止损线走的也是这儿. 原来一律渲染成
// "上限 %v 步", 而 steps 根本没在 payload 里 —— 真机日志里打出来的是
// `⏹ 到止损线了 (上限 <nil> 步)`. 步数默认无限, 报错种类错了
// 会让用户做错决定.
func TestStepLimitUsesTheRealMessage(t *testing.T) {
	out := renderStep(map[string]any{"phase": "step_limit", "msg": "到止损线了"})
	if !strings.Contains(out, "到止损线了") {
		t.Fatalf("没用真正的消息: %s", out)
	}
	if strings.Contains(out, "<nil>") || strings.Contains(out, "步") {
		t.Fatalf("又把止损线报成了步数: %s", out)
	}
}

// **一轮没做完必须说出来.**
//
// 进程把预算烧完停下时, 终端不能一个字都没有, 否则看着像卡住。
// 「停了但不说」比报错难查得多。
func TestFailedTurnIsNeverSilent(t *testing.T) {
	out := renderStep(map[string]any{"phase": "turn_failed", "err": "budget exhausted"})
	if out == "" {
		t.Fatal("一轮没做完却一个字都不说 —— 看着像卡住")
	}
	if !strings.Contains(out, "NEOX_BUDGET_TOKENS") {
		t.Fatalf("预算烧完了却没给出路: %s", out)
	}
}

// 网络隔离降级了必须显眼 —— 它是**约束没生效**, 而且事后查不到根
func TestNetnsDegradedIsLoud(t *testing.T) {
	out := renderStep(map[string]any{"phase": "netns_degraded",
		"err": "boom", "msg": "退回匿名断网 ns —— 127.0.0.1 在里面连不上"})
	if !strings.Contains(out, "127.0.0.1") {
		t.Fatalf("降级的后果没说出来: %q", out)
	}
}

// 故意不显示的必须真的不显示 —— 表和代码对不上时, 表就成了谎话
func TestSilentPhasesReallyAreSilent(t *testing.T) {
	for p, why := range silentPhases {
		if out := renderStep(map[string]any{"phase": p}); out != "" {
			t.Errorf("%q 登记成了故意不显示(%s), 实际打了 %q", p, why, out)
		}
	}
}

// 自查的原文是多行的(前面还带两个换行) —— 直接塞进去会把一条旁注
// 摊成一屏, 而它只是一步里的一个附注
func TestVerifyNoteIsCollapsedToOneLine(t *testing.T) {
	out := renderStep(map[string]any{"phase": "verify", "path": "broken.py",
		"note": "\n\n⚠ 顺带跑了 python 语法检查，**你改的这个文件有问题**：\n  File \"x\", line 1\n先把它修掉再往下走。"})
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("一条旁注摊成了 %d 行:\n%s", strings.Count(out, "\n"), out)
	}
	if !strings.Contains(out, "有问题") {
		t.Fatalf("折完之后把关键的话折没了: %s", out)
	}
}

// **攒下的那条不能打到终端** —— 打了就等于还是打扰了他一次.
//
// 这是唯一一条"OS 决定要不要让用户看见"的路径, 而它原来直接 Printf,
// 既测不到、渲染那道闸也看不见它.
func TestDeferredNoticeStaysQuiet(t *testing.T) {
	old := noticeGate
	defer func() { noticeGate = old }()
	noticeGate = func(string, string) (osinit.NoticeVerdict, int, int) {
		return osinit.NoticeDeferred, 3, 3
	}
	if out := renderNotify(map[string]any{"text": "客厅灯开着", "why": "x"}); out != "" {
		t.Fatalf("攒下的那条还是打到了终端上: %q", out)
	}
}

// 破例要标出来 —— 否则用户会以为额度形同虚设
func TestBreakthroughIsMarked(t *testing.T) {
	old := noticeGate
	defer func() { noticeGate = old }()
	noticeGate = func(string, string) (osinit.NoticeVerdict, int, int) {
		return osinit.NoticeBreakthrough, 3, 3
	}
	out := renderNotify(map[string]any{"text": "门锁开了", "why": "不可逆"})
	if !strings.Contains(out, "破例") {
		t.Fatalf("破例进来的没标出来: %q", out)
	}
}
