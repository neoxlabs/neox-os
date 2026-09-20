package main

import (
	"fmt"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 事件渲染 —— **这个文件里不许出现 fmt.Print**.
//
// ── 为什么要立这条规矩 ──
//
// 连着三轮栽在同一件事上: S36 六个 phase 发了事件却一个字不显示;
// S37 闸只扫两个文件, 盲区后面藏着我自己引入的回归; 同一轮里又发现
// notify 直接 Printf 绕过渲染, 既测不到、闸也看不见.
//
// 每次都是"又发现一处绕过去了". 根子在**能绕**: 只要事件回调里可以
// 直接打印, 就一定会有下一处. 所以渲染一律返回字符串, 打印由调用方
// 一处完成 —— 于是每一行给用户看的东西都能被测, 也都躲不过那道闸.

// renderDecision 渲染需要人工批准的请求.
//
// 三样缺一不可: 是哪件事、有哪些选项、怎么回答. 少一样用户就卡住了,
// 而那个进程会一直等着 —— 它不会自己超时, 也不该自己超时.
func renderDecision(did string, pres abi.PresentSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  ⚠  要你拍板 [%s] %s\n", did, pres.Title)
	for _, opt := range pres.Options {
		fmt.Fprintf(&b, "       %s = %s\n", opt.ID, opt.Label)
	}
	if len(pres.Options) == 0 {
		// **一条选项都没有的时候要说清楚**: 否则用户看到
		// "回答方式: /答 d1 <选项>" 却不知道能填什么, 而那个进程
		// 就一直卡着 —— 这种卡住查起来毫无线索
		b.WriteString("       (它没给选项, 一般填 yes 或 no)\n")
	}
	fmt.Fprintf(&b, "     回答方式: /答 %s <选项>\n> ", did)
	return b.String()
}

// renderOutcome 一个进程收尾了.
//
// ── 失败的原因不能截在半个词上 ──
//
// 把整个 payload 序列化再砍到 120 字时, 输出会变成:
//
//	● 结束 {"ok":false,"error":{"code":"error","message":"准备 cgroup 失败: 建 cgroup 目录: mkdir /sys/…permi…
//
// 真正有用的那半句(permission denied)正好被砍掉 ——
// **而结束这一行是一次失败唯一的现场**. 前面那些花括号和字段名
// 占掉的位置, 每一个字符都是从原因里抢来的.
func renderOutcome(payload any) string {
	ok, msg := outcomeOf(payload)
	if ok {
		// 成功只需要说一声"完了" —— 甩一堆 JSON 是噪音
		return "  ● 结束\n> "
	}
	if msg == "" {
		msg = "(没给原因)"
	}
	// 砍得比原来宽一截, 而且砍的是**原因本身**, 不是包着它的那层壳
	return fmt.Sprintf("  ● 结束(失败): %s\n> ", trunc(oneLine(msg), 300))
}

// outcomeOf 从事件负载里取出"成没成 / 为什么没成".
//
// **两种形状都得认**, 而且都是真实存在的:
//
//	abi.ProcessOutcome   活着的事件 —— 内存日志里保的是原来的 Go 类型
//	map[string]any       从盘上读回来的 —— 过了一趟 JSON, 什么都成了 map
//
// 这里必须同时处理两种负载: 只按 map 编写渲染和测试时, 单测会全绿,
// 运行时却会直接变成 `● 结束(失败): (没给原因)` —— **比原来更糟**,
// 原来至少还打出了被截断的 JSON, 里面看得见半句原因.
func outcomeOf(payload any) (ok bool, msg string) {
	switch v := payload.(type) {
	case abi.ProcessOutcome:
		if v.Error != nil {
			msg = v.Error.Message
			if msg == "" {
				msg = v.Error.Code
			}
		}
		return v.OK, msg
	case *abi.ProcessOutcome:
		if v == nil {
			return false, ""
		}
		return outcomeOf(*v)
	case map[string]any:
		ok, _ = v["ok"].(bool)
		if e, isMap := v["error"].(map[string]any); isMap {
			msg = str(e["message"])
			if msg == "" {
				msg = str(e["code"])
			}
		}
		return ok, msg
	}
	return false, ""
}

// renderDaily 今日小结.
//
// **它是唯一不需要用户主动的出口** —— 别的东西都要他先问一句,
// 只有这一份是系统自己在一天结束时递过来的. 所以它:
//
//	· 正文进账本(见 osinit 那侧的 record) —— 事后查得到
//	· 渲染跟别的输出走同一个口子 —— 而不是散在某处的 Printf
func renderDaily(text string) string {
	return fmt.Sprintf("\n  📋 今日小结\n%s\n> ", text)
}
