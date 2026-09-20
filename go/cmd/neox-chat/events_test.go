package main

import (
	"os"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// **事件回调里不许直接打印.**
//
// 这三轮连着栽在同一件事上:
//
//	S36  六个 phase 发了事件却一个字不显示
//	S37  闸只扫两个文件, 盲区后面藏着我自己引入的回归
//	S37  notify 直接 Printf, 绕过渲染 —— 既测不到, 闸也看不见
//
// 每次都是"又发现一处绕过去了". 根子在**能绕**: 只要回调里可以直接
// fmt.Print, 就一定会有下一处. 把这条堵死, 这一类才算完.
func TestEventCallbackDoesNotPrintDirectly(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "o.Log().SubscribeAll(func(e abi.Event) {") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("找不到事件回调 —— 这道闸跟源码对不上了, 已经形同虚设")
	}
	var offenders []string
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "\t})" { // 回调收尾
			if i-start < 5 {
				t.Fatal("回调只有几行? 闸的定位错了")
			}
			break
		}
		if strings.Contains(lines[i], "fmt.Print") {
			offenders = append(offenders, strings.TrimSpace(lines[i]))
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("事件回调里直接打印了 %d 处:\n  %s\n"+
			"走 renderEvent —— 直接打印的东西测不到, 渲染那道闸也看不见它",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// **失败的原因不能截在半个词上.**
//
// 把整个 payload 序列化再砍到 120 字会导致输出变成
//
//	● 结束 {"ok":false,"error":{"code":"error","message":"准备 cgroup 失败: 建 cgroup 目录: mkdir /sys/…permi…
//
// 真正有用的那半句("permission denied")正好被砍掉. 而结束这一行
// **是一次失败唯一的现场**.
//
// **两种形状都要喂**: 活着的事件是 abi.ProcessOutcome 结构体,
// 从盘上读回来的过了一趟 JSON、成了 map. 只按 map 处理时,
// 事件行会直接变成 `● 结束(失败): (没给原因)` ——
// **比原来更糟**, 原来至少还打出被截断的 JSON, 里面看得见半句原因.
func TestFailureOutcomeShowsTheWholeReason(t *testing.T) {
	const boom = "准备 cgroup 失败: 建 cgroup 目录: mkdir /sys/fs/cgroup/neox-os/p1: permission denied"
	for name, payload := range map[string]any{
		"活着的事件(结构体)": abi.ProcessOutcome{OK: false,
			Error: &abi.OutcomeError{Code: "error", Message: boom}},
		"读回来的(map)": map[string]any{"ok": false,
			"error": map[string]any{"code": "error", "message": boom}},
	} {
		out := renderOutcome(payload)
		if !strings.Contains(out, "permission denied") {
			t.Errorf("%s: 失败的原因被砍掉了:\n%s", name, out)
		}
		if strings.Contains(out, `{"ok"`) {
			t.Errorf("%s: 给人看的一行还是原始 JSON:\n%s", name, out)
		}
	}
}

// 成功那一行不该甩一堆 JSON —— 它只需要说"完了"
func TestSuccessOutcomeIsShort(t *testing.T) {
	for _, payload := range []any{
		abi.ProcessOutcome{OK: true}, map[string]any{"ok": true},
	} {
		checkShortOutcome(t, renderOutcome(payload))
	}
}

func checkShortOutcome(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "{") || len(out) > 40 {
		t.Fatalf("成功那行太吵: %q", out)
	}
}

// 决策行必须同时包含选项和回答方式, 少一样进程就会一直卡住
func TestDecisionShowsOptionsAndHow(t *testing.T) {
	out := renderDecision("d1", abi.PresentSpec{Title: "要装 openpyxl，放行 pypi？",
		Options: []abi.PresentOption{{ID: "yes", Label: "允许"}, {ID: "no", Label: "不许"}}})
	for _, want := range []string{"d1", "openpyxl", "yes", "允许", "/答 d1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("拍板那条少了 %q:\n%s", want, out)
		}
	}
}

// **一条选项都没有的决策要说清楚** —— 否则用户看到"回答方式: /答 d1 <选项>"
// 却不知道能填什么, 而那个进程就一直卡着
func TestDecisionWithNoOptionsSaysSo(t *testing.T) {
	out := renderDecision("d1", abi.PresentSpec{Title: "问点什么"})
	if !strings.Contains(out, "yes") && !strings.Contains(out, "没给选项") {
		t.Fatalf("没有选项时用户不知道能填什么:\n%s", out)
	}
}

// 今日小结要认得出来 —— 它跟别的输出长得不一样是有理由的:
// **别的东西都要用户先问一句, 只有这一份是系统自己递过来的**
func TestDailyReportIsMarked(t *testing.T) {
	out := renderDaily("今天攒下了 2 件没跟你说的事:\n  · 客厅灯开着")
	if !strings.Contains(out, "今日小结") {
		t.Fatalf("小结没标出来: %q", out)
	}
	if !strings.Contains(out, "客厅灯开着") {
		t.Fatalf("正文丢了: %q", out)
	}
}
