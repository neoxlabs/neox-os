// neox-sense-replay — 手机的替身.
//
// 读一条轨迹, 跑一遍**跟安卓端一模一样的降采样**, 把结果投给采集入口.
//
// ── 为什么值得单独有它 ──
//
// 安卓 APP 要写、要签名、要安装, 如果没有替身, 整条链路
// (降采样 → 投递 → 窗口 → 摘要 → 唤醒)就无法提前验证.
// 有了替身, APP 变成"最后一公里", 而不是"验证的前提".
//
// 它也是排查工具: 线上觉得信号太多/太少时, 把那天的原始轨迹
// 灌进来重放一遍, 就知道是采集端的阈值不对还是别的地方.
//
//	NEOX_SENSE_URL    http://127.0.0.1:8787
//	NEOX_SENSE_TOKEN  采集入口凭据
//	REPLAY_SPEED      加速倍数(缺省 0 = 不等待, 一次性灌完)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/sense"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: neox-sense-replay <轨迹.json>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var v struct {
		Config sense.PhoneConfig `json:"config"`
		Trace  []sense.Fix       `json:"trace"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Fprintf(os.Stderr, "轨迹解不开: %v\n", err)
		os.Exit(1)
	}

	sink := sense.Sink{URL: envOr("NEOX_SENSE_URL", "http://127.0.0.1:8787"),
		Token: os.Getenv("NEOX_SENSE_TOKEN")}
	if sink.Token == "" {
		fmt.Fprintln(os.Stderr, "要 NEOX_SENSE_TOKEN")
		os.Exit(2)
	}

	p := sense.NewPhoneFilter("phone.mk", v.Config)
	var out []abi.Signal
	for _, f := range v.Trace {
		out = append(out, p.Location(f)...)
	}

	// **事件时间要落在现在附近.**
	//
	// 轨迹样本里的时间是从 0 开始的相对毫秒. 原样投出去的话,
	// 全部会被判成迟到(1970 年) —— 而那看起来像"总线坏了",
	// 其实是重放工具没做时间平移. 这个坑值得在工具里一次性解决.
	base := time.Now().UnixMilli() - lastAt(out)
	for i := range out {
		out[i].At += base
	}

	counts, err := sink.Post(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "投递失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%d 个定点 → %d 条信号 (降噪 %.2f%%), 扔掉低精度 %d 个\n投递结果: %v\n",
		len(v.Trace), len(out),
		100-float64(len(out))/float64(max(len(v.Trace), 1))*100,
		p.DroppedFixes(), counts)
}

func lastAt(s []abi.Signal) int64 {
	var m int64
	for _, x := range s {
		if x.At > m {
			m = x.At
		}
	}
	return m
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
