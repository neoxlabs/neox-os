package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func fpOf(sys string, msgs []abi.InferMessage) cacheFingerprint {
	return fingerprintOf(sys, ToolDefs(NewToolSet(DefaultTools())), msgs)
}

// 只在末尾追加 = 前缀干净, 不该报
func TestAppendOnlyIsNotDrift(t *testing.T) {
	a := fpOf("SYS", []abi.InferMessage{{Role: "user", Content: "一"}})
	b := fpOf("SYS", []abi.InferMessage{
		{Role: "user", Content: "一"}, {Role: "assistant_reply", Content: "二"}})
	if d := a.compare(b); d != nil {
		t.Fatalf("只追加却报了漂移: %+v", d)
	}
}

// 改写已有消息 = 前缀断了, 必须报出**是第几条、什么角色**
func TestRewritingHistoryIsCaught(t *testing.T) {
	a := fpOf("SYS", []abi.InferMessage{
		{Role: "user", Content: "一"}, {Role: "assistant_reply", Content: "二"}})
	b := fpOf("SYS", []abi.InferMessage{
		{Role: "user", Content: "一"}, {Role: "assistant_reply", Content: "改过了"}})
	d := a.compare(b)
	if d == nil {
		t.Fatal("历史被改写却没报 —— 那一轮的钱白花了没人知道")
	}
	if d.FirstDivergentIdx != 1 {
		t.Fatalf("没指出是第几条: %d", d.FirstDivergentIdx)
	}
}

// **tool_call 的 id 变了同样要抓到.**
//
// 只哈希 content 会漏掉这种 —— 而 id 一变缓存就全废,
// 这个场景会让缓存完全失效, 且已经出现过两次.
func TestToolCallIDChangeIsCaught(t *testing.T) {
	mk := func(id string) []abi.InferMessage {
		return []abi.InferMessage{
			{Role: "user", Content: "干活"},
			{Role: "assistant_reply", ToolCalls: []abi.ToolCall{
				{ID: id, Name: "run", Arguments: "{}"}}},
		}
	}
	if d := fpOf("SYS", mk("call_a")).compare(fpOf("SYS", mk("call_b"))); d == nil {
		t.Fatal("tool_call id 变了没抓到 —— 只哈希 content 就会漏这种")
	}
}

// 系统段变了要给到**字符级位置**, 只说"变了"没法查 —— 它有几千字
func TestSystemDriftGivesCharacterPosition(t *testing.T) {
	a := fpOf("你是助手。工作目录 /a。别乱来。", nil)
	b := fpOf("你是助手。工作目录 /b。别乱来。", nil)
	d := a.compare(b)
	if d == nil || !d.SystemChanged {
		t.Fatal("系统段变了没抓到")
	}
	if d.SysDiffAt == 0 {
		t.Fatal("没给出第几个字符不同 —— 系统段几千字, 肉眼比对是不可能的")
	}
	if !strings.Contains(d.SysDiffPrev, "/a") || !strings.Contains(d.SysDiffCur, "/b") {
		t.Fatalf("片段没截到真正变的地方: %q vs %q", d.SysDiffPrev, d.SysDiffCur)
	}
	if !strings.Contains(d.why(), "字符") {
		t.Fatalf("说法没指向去查哪里: %s", d.why())
	}
}

// 工具声明变了也要抓 —— 它跟系统段一样进前缀
func TestToolsDriftIsCaught(t *testing.T) {
	full := ToolDefs(NewToolSet(DefaultTools()))
	a := fingerprintOf("SYS", full, nil)
	b := fingerprintOf("SYS", full[:len(full)-1], nil)
	d := a.compare(b)
	if d == nil || !d.ToolsChanged {
		t.Fatal("工具声明变了没抓到")
	}
}

// 同一份输入反复算, 指纹必须一样 —— 检测器自己不稳就全是噪音
func TestFingerprintIsStable(t *testing.T) {
	msgs := []abi.InferMessage{{Role: "user", Content: "一"},
		{Role: "assistant_reply", ToolCalls: []abi.ToolCall{{ID: "c", Name: "run", Arguments: `{"cmd":"ls"}`}}}}
	first := fpOf("SYS", msgs)
	for i := 0; i < 50; i++ {
		if d := first.compare(fpOf("SYS", msgs)); d != nil {
			t.Fatalf("检测器自己不稳: %+v", d)
		}
	}
}

// **第一轮不许报.**
//
// 没有上一轮可比时一切都"变了", 报出来是纯误报 —— 而误报比不报更糟:
// 狼来了喊多了, 真漂移那次就没人看了.
// 第一轮没有上一轮指纹可比 (sysDiffPrev 为空串, sysDiffAt=0).
func TestFirstRoundNeverReportsDrift(t *testing.T) {
	var zero cacheFingerprint
	cur := fpOf("你是 Neox，跑在 Neox OS 上的助手。", []abi.InferMessage{{Role: "user", Content: "干活"}})
	if d := zero.compare(cur); d != nil {
		t.Fatalf("第一轮报了漂移 —— 纯误报: %+v", d)
	}
}

// 但第二轮开始必须照报不误
func TestSecondRoundStillReports(t *testing.T) {
	a := fpOf("SYS-A", []abi.InferMessage{{Role: "user", Content: "一"}})
	b := fpOf("SYS-B", []abi.InferMessage{{Role: "user", Content: "一"}})
	if d := a.compare(b); d == nil || !d.SystemChanged {
		t.Fatal("第二轮的真漂移被吞了")
	}
}
