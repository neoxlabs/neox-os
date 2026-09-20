package main

// 他那天收到过什么 —— **账本里本来就有, 只是它够不着**.
//
//	当查询"我今天收到什么消息"时, 它手里只有"最新那一条"(世界模型答的是
//	"现在怎么样", 见 osinit/world.go), 于是它去 run grep events.jsonl ——
//	两天 27 次, 每次现写一段 python 解析原始 JSON.
//
//	位置那一路是 where(day=…)(见 geo.trail); 这一路是别的信号:
//	通知和消息、电量、WiFi、车机蓝牙、到没到某个起过名的地方.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

// historyMax 一次最多给几条 —— 再多就是把一条流塞进上下文
const historyMax = 120

// historyKinds what → 信号种类. nil = 除了位置和天气之外的全部.
//
//	位置有 where(day=…), 天气有 where(weather=…) —— 那两样在这儿只会
//	把真正要看的东西冲掉(账本里电量 665 条、位置 268 条)
func historyKinds(what string) map[string]bool {
	switch strings.TrimSpace(what) {
	case "", "1", "true", "全部", "所有":
		return nil
	case "通知", "消息", "notice":
		return map[string]bool{"notice.posted": true}
	case "电量", "电池", "battery":
		return map[string]bool{"battery.level": true}
	case "网络", "wifi", "WiFi":
		return map[string]bool{"network.wifi": true}
	case "蓝牙", "车", "bluetooth":
		return map[string]bool{"bluetooth.audio": true}
	case "到达", "地点", "place":
		return map[string]bool{"place.arrived": true, "place.left": true}
	case "天气", "weather":
		return map[string]bool{"weather.now": true}
	}
	return nil
}

// renderSignal 一条信号说成人话. 采集端多半自己带了一句(body.text),
// 它比 OS 更懂自己报的是什么 —— 跟 osinit/world.go 同一条规矩.
func renderSignal(kind string, body map[string]any) string {
	say := strings.TrimSpace(fmt.Sprint(body["text"]))
	if body["text"] == nil {
		say = ""
	}
	switch kind {
	case "notice.posted":
		app := strings.TrimSpace(fmt.Sprint(body["app"]))
		title := strings.TrimSpace(fmt.Sprint(body["title"]))
		if body["app"] == nil {
			app = "通知"
		}
		who := app
		if body["title"] != nil && title != "" {
			who = app + "「" + title + "」"
		}
		if say == "" {
			return who
		}
		return who + ": " + say
	case "place.arrived":
		return "到了" + strings.TrimSpace(fmt.Sprint(body["place"]))
	case "place.left":
		return "离开" + strings.TrimSpace(fmt.Sprint(body["place"]))
	case "location":
		return "" // 位置走 where(day=…)
	}
	if say != "" {
		return say
	}
	return kind
}

// oneLine 一条记录占一行 —— 群公告能有十几行, 原样铺开就把这一屏冲没了.
// 掐掉的部分他要真想看, 手机上那条通知还在
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// history 他那天收到过什么. what 见 historyKinds; day 见 dayRange.
func (c *components) history(what, day string, limit int) (string, error) {
	if c.os == nil {
		return "", fmt.Errorf("这台机器没有账本")
	}
	from, to, label, err := dayRange(day, time.Now())
	if err != nil {
		return "", err
	}
	want := historyKinds(what)
	if limit <= 0 || limit > historyMax {
		limit = 40
	}
	type line struct {
		at   int64
		text string
		n    int
	}
	var lines []line
	var log *osinit.EventLog
	if c.os != nil {
		log = c.os.Log()
	}
	eachSignal(c.ledger, log, func(e abi.Event) bool {
		if e.Kind != abi.EvSignal {
			return true
		}
		p, ok := e.Payload.(map[string]any)
		if !ok {
			return true
		}
		kind := strings.TrimSpace(fmt.Sprint(p["kind"]))
		if want == nil {
			// 缺省把两条最吵的挡在外面 —— 它们各自有更好的入口
			if kind == "location" || kind == "weather.now" {
				return true
			}
		} else if !want[kind] {
			return true
		}
		at := int64(numOf(p["at"]))
		if at == 0 {
			at = e.At
		}
		if at < from || at >= to {
			return true
		}
		body, _ := p["body"].(map[string]any)
		text := oneLine(renderSignal(kind, body), 110)
		if text == "" {
			return true
		}
		// 一模一样的连着来就并成一行 —— 电量一天 665 条, 全列出来
		// 就是把一条流塞进上下文
		if n := len(lines); n > 0 && lines[n-1].text == text {
			lines[n-1].n++
			lines[n-1].at = at
			return true
		}
		lines = append(lines, line{at: at, text: text, n: 1})
		return true
	})
	if len(lines) == 0 {
		return fmt.Sprintf("%s没有这类记录。", label), nil
	}
	// **总数要说真的那个**: 截断之后再数就成了"共 12 条", 而他那天收了 102 条
	total, more := len(lines), ""
	if total > limit {
		more = fmt.Sprintf("(只列最近 %d 条, 前面还有 %d 条)\n", limit, total-limit)
		lines = lines[total-limit:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s收到的(共 %d 条):\n%s", label, total, more)
	for _, l := range lines {
		fmt.Fprintf(&b, "- %s %s", time.UnixMilli(l.at).Format("15:04"), l.text)
		if l.n > 1 {
			fmt.Fprintf(&b, "（×%d）", l.n)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// homeAct 冲家里那台喊一声 —— 见 abi.EvDeviceAct 和 sense/haact.go.
//
//	**不落账**(EventLog.Append 里那条瞬时分支认它): 这是一句吆喝, 不是
//	一件发生过的事. 灯真亮了会有一条状态信号进来, 那条才是事实.
func (c *components) homeAct(name, do string) (string, error) {
	if c.os == nil {
		return "", fmt.Errorf("这台机器没接家里的东西")
	}
	c.os.Log().Append(abi.ProcessID("sense"), abi.EvDeviceAct, map[string]any{
		"device": "ha", "act": do, "name": name,
	})
	return fmt.Sprintf("已经冲家里那台喊了一声「%s %s」。"+
		"做没做成这一轮看不到 —— 真做成了会有一条状态信号进来", do, name), nil
}

// envOn 这个开关打开了没有 —— 1/true/yes/on 都算开.
//
//	**默认关**: 控制家里的东西跟读传感器不是一回事, 得他明确打开
//	(见 HomeTool: 没接的时候摆一个用不了的工具比没有更糟)
func envOn(k string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
