package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// 真正的语法错误要说成语法错误
func TestBrokenJSONReportedAsSyntax(t *testing.T) {
	_, err := parseStep(`{"tool":"read_file", "args":`)
	if err == nil {
		t.Fatal("半截 json 该报错")
	}
	if !strings.Contains(err.Error(), "不是合法 json") {
		t.Fatalf("语法错误没说成语法错误: %v", err)
	}
}

// 语法没问题但字段形状不对, **绝不能报成"不是合法 json"**.
//
// 模型可能发出 {"args":["a.js","b.md"]} —— args 给成了数组。
// JSON 完全合法，报成语法错会让它去修语法，而真正要改的是把 args 写成对象。
// 说错原因比不说更糟：它会照着错的方向改，然后再错一次。
func TestWrongShapeIsNotCalledSyntaxError(t *testing.T) {
	_, err := parseStep(`{"tool":"read_file","args":["a.js","b.md"]}`)
	if err == nil {
		t.Fatal("args 给成数组该被拒")
	}
	msg := err.Error()
	if strings.Contains(msg, "不是合法 json") {
		t.Fatalf("把结构错误报成了语法错误: %s", msg)
	}
	if !strings.Contains(msg, "args") {
		t.Fatalf("没说清是哪个字段: %s", msg)
	}
	if !strings.Contains(msg, "对象") {
		t.Fatalf("没说清该是什么形状: %s", msg)
	}
}

// 要一次调多个工具时该往哪走, 也要顺手指出来
func TestArgsShapeHintPointsToToolsArray(t *testing.T) {
	_, err := parseStep(`{"tool":"read_file","args":["a","b"]}`)
	if !strings.Contains(err.Error(), "tools 数组") {
		t.Fatalf("没告诉它多工具该用 tools: %v", err)
	}
}

// tools 形状错了要说 tools 的正确形状, 不是 args 的
func TestToolsShapeHint(t *testing.T) {
	_, err := parseStep(`{"tools":"read_file"}`)
	if err == nil {
		t.Fatal("tools 给成字符串该被拒")
	}
	if !strings.Contains(err.Error(), "tools 是一个数组") {
		t.Fatalf("提示不对: %v", err)
	}
}

// 正常的几种形态都要能解
func TestValidShapesParse(t *testing.T) {
	cases := []string{
		`{"reply":"你好"}`,
		`{"thought":"看看","tool":"list_dir","args":{"path":"."}}`,
		`{"thought":"读三个","tools":[{"tool":"read_file","args":{"path":"a"}}]}`,
		`{"done":"做完了"}`,
	}
	for _, c := range cases {
		if _, err := parseStep(c); err != nil {
			t.Fatalf("%s 解不开: %v", c, err)
		}
	}
}

// 报错里要带原文 —— 不带的话人和模型都不知道它到底发了什么
func TestSyntaxErrorCarriesTheOriginal(t *testing.T) {
	_, err := parseStep(`不是 json 一个字都不是`)
	if !strings.Contains(err.Error(), "不是 json") {
		t.Fatalf("报错没带原文: %v", err)
	}
}

// 参数摊在项上也要收 —— 那不是猜, 除了 tool 之外的键只有一种解释.
//
// 模型发出 7 路并发 read_file 且全是这种写法时, args 会全部为空,
// 而且解析时一声不吭, 直到 7 个调用各失败一次、第 3 个触发硬停.
func TestToolCallAcceptsFlatArgs(t *testing.T) {
	s, err := parseStep(`{"tools":[{"tool":"read_file","path":"a.js"},
	                              {"tool":"read_file","path":"b.js"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tools) != 2 {
		t.Fatalf("拿到 %d 项", len(s.Tools))
	}
	for i, c := range s.Tools {
		if c.Args == nil || c.Args["path"] == nil {
			t.Fatalf("第 %d 项的参数丢了: %+v", i, c.Args)
		}
	}
}

// 标准写法当然要正常
func TestToolCallStandardArgsStillWork(t *testing.T) {
	s, err := parseStep(`{"tools":[{"tool":"read_file","args":{"path":"a.js","offset":10}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tools[0].Args["path"] != "a.js" {
		t.Fatalf("标准写法解错了: %+v", s.Tools[0].Args)
	}
}

// thought 是给人看的, 不能被当成参数塞进去
func TestToolCallThoughtIsNotAnArg(t *testing.T) {
	s, _ := parseStep(`{"tools":[{"tool":"list_dir","thought":"看看","path":"."}]}`)
	if _, bad := s.Tools[0].Args["thought"]; bad {
		t.Fatal("thought 被当成参数了")
	}
	if s.Tools[0].Args["path"] != "." {
		t.Fatal("真正的参数丢了")
	}
}

// TestUnwrapEnvelope 模型把一句话包在结构化信封里, 用户不该看到 JSON.
func TestUnwrapEnvelope(t *testing.T) {
	if got := unwrapEnvelope(`{"said":"版本号还没定"}`); got != "版本号还没定" {
		t.Fatalf("没拆开: %q", got)
	}
	if got := unwrapEnvelope(`{"reply":"好的"}`); got != "好的" {
		t.Fatalf("没拆开: %q", got)
	}
	// 普通文本原样
	for _, plain := range []string{"就是一句话", "{不是 json", `{"a":1,"b":2}`, "{}"} {
		if unwrapEnvelope(plain) != plain {
			t.Fatalf("不该动: %q", plain)
		}
	}
	// 多字段 = 它真想表达结构, 原样保留(拆错会吞内容)
	multi := `{"said":"甲","note":"乙"}`
	if unwrapEnvelope(multi) != multi {
		t.Fatal("多字段被拆了 —— 会吞掉内容")
	}
}

// 模型退回旧的 JSON 协议、**而且吐的 JSON 是坏的**时, 不许把它原样摆给用户.
//
// ── 观测数据 ──
//
// 一台跑着的机器, 57 条回话里 3 条长这样(5.3%), 分布在三个不同的 bot 上.
// 正文里带着没转义的引号或一个坏转义, json.Unmarshal 当场失败,
// 于是用户在界面上看到的是一串 {"said":"…"} 字面量.
func TestSalvagesBrokenEnvelope(t *testing.T) {
	// 下面两条的形状取自真机日志: 一条正文里有没转义的引号, 一条有坏转义
	badQuote := `{"said":"重读了定稿 release.sh，改动只动了一行——"同一版本号只能出现一次"。\n\n路径在 writer 下。"}`
	badEscape := `{"said":"最终核对完成。\n\n读文件时 mtime=1787646\d 已记下。"}`

	got := unwrapEnvelope(badQuote)
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("坏 JSON 原样漏给用户了: %q", got[:60])
	}
	if !strings.Contains(got, "同一版本号只能出现一次") {
		t.Fatalf("正文被救坏了: %q", got)
	}
	if !strings.Contains(got, "\n\n") {
		t.Fatal("换行没还原 —— 用户看到的是一整坨带 \\n 的文本")
	}

	got = unwrapEnvelope(badEscape)
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("坏转义那条没救回来: %q", got[:60])
	}
	if !strings.Contains(got, `\d`) {
		t.Fatal("认不得的转义应该原样留着, 不许猜")
	}
}

// 形状对不上就**原样返回** —— 宁可漏一条, 不能把正常回答切坏.
func TestSalvageRefusesWhenShapeIsUnclear(t *testing.T) {
	for _, keep := range []string{
		`这是一句正常的话，里面提到了 {"said":"x"} 这种写法。`,  // 不是以 { 开头
		`{"tool":"run","args":{"cmd":"ls"}}`, // 不是回话信封
	} {
		if got := unwrapEnvelope(keep); got != keep {
			t.Errorf("形状不明的被动了: %q → %q", keep, got)
		}
	}
}

// 合法的单键信封仍然走严格解析那条路
func TestValidEnvelopeStillUnwrapsStrictly(t *testing.T) {
	if got := unwrapEnvelope(`{"said":"一切正常"}`); got != "一切正常" {
		t.Fatalf("合法信封没拆开: %q", got)
	}
}

// 坏 JSON 里看着还有别的字段就**不救** —— 救了会把后面几个字段一起吞进正文.
//
// 坏 JSON 解不开, 我们只能靠形状判断, 那就宁可保守: 漏一条比切坏一条强.
func TestSalvageRefusesMultiFieldEvenWhenBroken(t *testing.T) {
	broken := `{"said":"他说"这样"不行","tool":"run"}`
	if got := unwrapEnvelope(broken); got != broken {
		t.Fatalf("多字段的坏 JSON 被救了, 后面的字段被吞进正文: %q", got)
	}
}

// 把工具调用**写成文字**要当场吵 —— 静默收下就是动作丢了.
//
// ── 失败形态与处理原因 ──
//
// 模型出错时, 喂回去的那句提示是"请重新输出一步合法 json" —— **旧协议
// 时代的话**. 现在走原生工具调用, 这句话字面上就是在教它把调用写成文字.
// 它照做了: 吐出一整块 {"said": …, "tool_calls": […]} 的文本,
// 于是那次 recruit 根本没发生, 用户只看到一坨 JSON, 而它以为自己动过手了.
func TestTextWrittenToolCallIsLoud(t *testing.T) {
	for _, fake := range []string{
		`{"said":"我拉个人","tool_calls":[{"invoke":"recruit"}]}`,
		`{"tool":"write_file","args":{"path":"a.md"}}`,
		`{"tools":[{"tool":"run","args":{}}]}`,
	} {
		why := textToolCall(fake)
		if why == "" {
			t.Errorf("这段被当成正常回复收下了, 动作就此丢掉: %s", fake[:40])
			continue
		}
		if !strings.Contains(why, "什么都没发生") {
			t.Errorf("没说清后果, 它不会重来: %q", why)
		}
	}
}

// **不按关键词乱猜**: 一句正常的话里提到 tool_calls 是完全可能的.
func TestNormalReplyIsNotMistakenForAToolCall(t *testing.T) {
	for _, ok := range []string{
		"我刚才那一步用的是 tool_calls 这个字段，供应商那边保证结构。",
		// **这一条才真正钉住"必须以 { 开头"那道闸**: 它带着引号的键名,
		// 只按关键词判的话会被误伤 —— 而它只是在跟你解释报错
		`报错里写着 "tool_calls" 这个字段不对，我去核一下。`,
		"结果是 {\"ok\":true}，没别的。",
		"",
		"好了。",
	} {
		if why := textToolCall(ok); why != "" {
			t.Errorf("正常回复被当成写成文字的调用: %q → %q", ok, why)
		}
	}
}

// 出错重来时**不许再教它输出 json** —— 那正是上面那个坑的成因.
func TestRetryHintDoesNotAskForJSON(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Skip(err)
	}
	if strings.Contains(string(src), "请重新输出一步合法 json") {
		t.Fatal("重试提示还在教模型输出 json 文本 —— 它会照做, 然后那一步的动作就丢了")
	}
}

// **接线也要钉**: 检测函数写对了、调用点没接上, 症状跟没写一样.
//
// 如果 once() 里的调用永不触发, 上面几条测试**一条都不会失败** ——
// 它们只测了函数本身, 而实际故障可能出在接线环节.
func TestTextToolCallIsCaughtOnTheRealPath(t *testing.T) {
	sys := &fakeSys{}
	sys.inferFn = func(abi.InferParams) (abi.InferResult, error) {
			// 模型可能返回这种形状
		return abi.InferResult{Content: `{"said":"我拉个人来","tool_calls":[{"invoke":"recruit"}]}`}, nil
	}
	w := NewWindow(engine.NewPageStore(), "拉个人", 1<<20)
	defer w.Release()
	m := &LLM{ABI: sys, Tools: NewToolSet(DefaultTools()), Window: w}

	_, err := m.Next("拉个人", nil)
	if err == nil {
		t.Fatal("写成文字的调用被当成正常回复收下了 —— 那次动作就此丢掉, 而它以为自己动过手")
	}
	if !strings.Contains(err.Error(), "什么都没发生") {
		t.Fatalf("报错没说清后果: %v", err)
	}
}

// 正常回复走原路 —— 别把好人也拦下来.
func TestNormalReplyStillGetsThrough(t *testing.T) {
	sys := &fakeSys{}
	sys.inferFn = func(abi.InferParams) (abi.InferResult, error) {
		return abi.InferResult{Content: "好了，报表那块我来做。"}, nil
	}
	w := NewWindow(engine.NewPageStore(), "干活", 1<<20)
	defer w.Release()
	m := &LLM{ABI: sys, Tools: NewToolSet(DefaultTools()), Window: w}
	s, err := m.Next("干活", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Reply != "好了，报表那块我来做。" {
		t.Fatalf("正常回复被动了: %q", s.Reply)
	}
}

// ── 2026-09-11 线上账本里还漏着的三种 ──
//
//	原来这三种都被"保守闸"原样放过去, 用户在手机上看到的就是 JSON.
//	以 {"said":" 开头的不可能是一句正常人话, 所以都要拆.
func TestUnwrapsWhatLeakedInProduction(t *testing.T) {
	cases := map[string]string{
		// 巡检醒来决定不说话 —— 账本里 11 次
		`{"said":""}`: "",
		// 输出被截断, 没有收尾 —— 原文
		`{"said":"行，不扯了。你要问什么直接说，凤凰国际广场的位置我随时能接。`:   "行，不扯了。你要问什么直接说，凤凰国际广场的位置我随时能接。",
		`{"said":"你说得对，是我错了。\n\n坐标是**照着手机当时的位置记的**`: "你说得对，是我错了。\n\n坐标是**照着手机当时的位置记的**",
		// 包了两层
		`{"said":"{\"said\":\"蚌埠现在多云，二十度出头。\"}"}`: "蚌埠现在多云，二十度出头。",
		// 空信封后面跟着正文 —— 正文才是它要说的
		"{\"said\":\"\"}\n\n到了。": "到了。",
	}
	for in, want := range cases {
		if got := unwrapEnvelope(in); got != want {
			t.Errorf("%q\n→ %q\n要 %q", in, got, want)
		}
	}
}
