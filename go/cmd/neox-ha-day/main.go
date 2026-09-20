// neox-ha-day — 在真 Home Assistant 上重放一天的住宅活动.
//
// **它只调 HA 的服务, 一个字节都不碰采集入口.**
//
// 这条边界是整件事的意义所在: 灌合成信号会绕过采集器的全部判据
// (钟、上线风暴、数值阈值、语义 kind), 那样量出来的是"我编的信号有多吵",
// 不是"这套过滤有多好" —— 而后者才是要验收的.
//
// 于是这条路上的每一步都是真的: HA 真的改了实体状态, 采集器真的拉回来,
// 真的过一遍所有判据, 真的进窗口、真的叫醒主动进程.
//
//	HA_BASE_URL   http://127.0.0.1:8123
//	HA_TOKEN      长期访问令牌
//	DAY_SECONDS   一天压成多少秒(缺省 1200 = 20 分钟, 72:1)
package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/neox-os/neox-os/sense"
)

func main() {
	base := envOr("HA_BASE_URL", "http://127.0.0.1:8123")
	token := os.Getenv("HA_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "要 HA_TOKEN")
		os.Exit(2)
	}
	daySec, _ := strconv.Atoi(envOr("DAY_SECONDS", "1200"))
	if daySec <= 0 {
		daySec = 1200
	}
	// **两天都要能跑**: 光证明"该说的说了"不够, 一个见门就喊的系统
	// 同样能通过那一半, 而它会在第一周就被关掉通知. 见 QuietDay
	d := sense.Household()
	if envOr("DAY", "household") == "quiet" {
		d = sense.Quiet()
	}
	day := d.Actions
	fmt.Printf("跑的是: %s\n", d.Which)

	// **手机的位置信号也按剧本的时刻投** —— 它跟灯和门一样是这一天的
	// 一部分. 原来由脚本在开头投一条常量, 于是"故事里人回家了"而
	// "前提里人一直不在家", 之后每件事都被当成"没人在家却有动静".
	senseURL, senseTok := os.Getenv("NEOX_SENSE_URL"), os.Getenv("NEOX_SENSE_TOKEN")
	postPhone := func(kind string) {
		if senseURL == "" || senseTok == "" {
			return
		}
		now := time.Now().UnixMilli()
		body := fmt.Sprintf(`[{"id":"ph%d","source":"phone.mk","kind":%q,`+
			`"at":%d,"knownAt":%d,"body":{"lat":31.88001,"lon":117.28}}]`,
			now, kind, now, now)
		req, _ := http.NewRequest("POST", senseURL+"/signals",
			bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+senseTok)
		req.Header.Set("Content-Type", "application/json")
		if resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
		} else {
			fmt.Fprintf(os.Stderr, "  手机信号投不出去: %v\n", err)
		}
	}
	scale := float64(daySec) / (24 * 60 * 60) // 压缩比

	fmt.Printf("重放一天: %d 个动作 · 压成 %d 秒(%.0f:1)\n",
		len(day), daySec, 1/scale)
	// **这个压缩比会把什么弄失真, 一条一条说出来** ——
	// 一个不说自己哪儿失真的尺子, 比不准更危险(见 CompressionWarnings)
	for _, w := range sense.CompressionWarnings(daySec) {
		fmt.Println("**注意** " + w)
	}

	// **尺子坏了要当场喊, 不能跑完给一份没意义的数据.**
	//
	// 例如对照日里"开门拿快递"是两分钟, 压缩 72:1 之后只剩
	// 1.7 秒, 而采集器 5 秒才拉一次 —— 那扇门从来没被看见过开着,
	// 而它正是那一天存在的全部意义. 更糟的是它**时好时坏**:
	// 轮询正好落在那 1.7 秒里就看得见, 落在外面就看不见,
	// 同一份剧本跑两次结果不一样, 人会去找一个不存在的 bug.
	poll, _ := strconv.Atoi(envOr("POLL_SEC", "5"))
	if bad := sense.TooFastForPolling(day, daySec, poll); len(bad) > 0 {
		// 抬头别把原因说死成"轮询" —— 有的是卡在设备自己的状态机上,
		// 而那两句写在一起会自相矛盾(下面明明写着"卡在设备")
		fmt.Fprintln(os.Stderr,
			"\n⚠ 这把尺子在这几处是坏的 —— 压缩之后那件事短得看不见:")
		for _, b := range bad {
			fmt.Fprintf(os.Stderr, "   %-28s %s → %s  只剩 %.1f 秒(要 %.0f 秒, 卡在%s)\n",
				b.Entity, b.From, b.To, b.GapSec, b.NeedSec, b.Because)
		}
		// **两条的办法不一样**: 锁转一圈的那两秒是物理的, 调采集器没有用
		fmt.Fprintln(os.Stderr,
			"   卡在轮询: 把 POLL_SEC 调小  ·  卡在设备: 只能把 DAY_SECONDS 调大(压得轻一点)")
		if os.Getenv("FORCE") == "" {
			fmt.Fprintln(os.Stderr, "   不跑了 —— 跑出来的数据证明不了它要证明的事。要硬跑加 FORCE=1")
			os.Exit(3)
		}
	}

	cli := &http.Client{Timeout: 15 * time.Second}
	start := time.Now()
	phoneIdx := 0
	for _, a := range day {
		// 到点了就先把手机那条投出去 —— 它跟这一刻的动作是同一件事的
		// 两面(人出门锁门, 手机随后报"离开家了")
		for phoneIdx < len(d.Phone) && d.Phone[phoneIdx].AtMin <= a.AtMin {
			p := d.Phone[phoneIdx]
			target := start.Add(time.Duration(float64(p.AtMin) * 60 * scale * float64(time.Second)))
			if w := time.Until(target); w > 0 {
				time.Sleep(w)
			}
			fmt.Printf("  %02d:%02d  %-28s 手机: %s\n", p.AtMin/60, p.AtMin%60, "phone.mk", p.Kind)
			postPhone(p.Kind)
			phoneIdx++
		}
		// 按剧本里的时刻等 —— 保的是**相对间隔**
		target := start.Add(time.Duration(float64(a.AtMin) * 60 * scale * float64(time.Second)))
		if d := time.Until(target); d > 0 {
			time.Sleep(d)
		}
		mark := " "
		if a.Worthy {
			mark = "★" // 这几件是"该说的" —— 量结果时对着看
		}
		fmt.Printf("%s %02d:%02d  %-28s %s\n", mark, a.AtMin/60, a.AtMin%60, a.Entity, a.Note)
		body := fmt.Sprintf(`{"entity_id":%q}`, a.Entity)
		req, _ := http.NewRequest("POST", base+"/api/services/"+a.Service,
			bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := cli.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  调用失败: %v\n", err)
			continue
		}
		if resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "  HA 返回 %d (%s)\n", resp.StatusCode, a.Service)
		}
		resp.Body.Close()
	}
	fmt.Println("一天放完了")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
