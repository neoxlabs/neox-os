// neox-sense-ha — Home Assistant 采集器.
//
// 它**不是 OS 的一部分**: 拿不到事件日志、拿不到进程表, 只会做一件事 ——
// 把 HA 的世界翻译成信号, 投给 OS 的采集入口.
//
// 所以它可以死、可以重启、可以跑在另一台机器上, OS 不受影响.
// 这正是"采集端在外面, 总线在里面"那条边界的意义.
//
//	HA_BASE_URL       http://127.0.0.1:8123
//	HA_TOKEN          HA 的长期访问令牌
//	NEOX_SENSE_URL    http://127.0.0.1:8787
//	NEOX_SENSE_TOKEN  采集入口的凭据
//	HA_POLL_SEC       轮询间隔(缺省 10). **它就是最坏延迟**
//	HA_EPSILON        数值传感器变化多少算变了(缺省 0.05 = 5%)
//	HA_CONTROL        =1 时**同时听 OS 的吆喝**: 它说"把客厅灯关掉",
//	                  这边在本地调 HA(见 sense/haact.go).
//	                  **默认关** —— 采集是读, 控制是写, 不该一起默认打开.
//	                  家里只有出站连接: HA 一个端口都不用往公网开
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/neox-os/neox-os/sense"
)

func main() {
	cfg := sense.HAConfig{
		BaseURL:        envOr("HA_BASE_URL", "http://127.0.0.1:8123"),
		Token:          os.Getenv("HA_TOKEN"),
		Interval:       time.Duration(envInt("HA_POLL_SEC", 10)) * time.Second,
		NumericEpsilon: envFloat("HA_EPSILON", 0.05),
	}
	sink := sense.Sink{
		URL:   envOr("NEOX_SENSE_URL", "http://127.0.0.1:8787"),
		Token: os.Getenv("NEOX_SENSE_TOKEN"),
	}
	if cfg.Token == "" || sink.Token == "" {
		fmt.Fprintln(os.Stderr, "要 HA_TOKEN 和 NEOX_SENSE_TOKEN 才能跑")
		os.Exit(2)
	}

	bridge := sense.NewHABridge(cfg)
	// **最近一次实体表**: 名字到实体的匹配必须拿当下这份做 —— OS 那边
	// 存一份过期的清单, 只会指向一个已经改了名的灯(见 sense.resolveAct)
	var lastStates atomic.Value
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backlog := sense.NewBacklog(envInt("HA_BACKLOG", 500))

	fmt.Printf("HA 采集器 · %s → %s · 每 %s 一次 · 数值阈值 %.0f%%\n",
		cfg.BaseURL, sink.URL, cfg.Interval, cfg.NumericEpsilon*100)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// ── 反过来的那半边 ── 见 sense.Listen.
	//
	//	**默认不开**: 采集是读, 控制是写. 他明确打开(HA_CONTROL=1)才听,
	//	而且听到的东西还要再过一遍白名单(锁和安防不在里面).
	if envOn("HA_CONTROL") {
		fmt.Println("控制已打开: 同时听 OS 的吆喝(锁和安防不开放)")
		go sense.Listen(ctx, sink,
			func() []sense.HAState {
				v, _ := lastStates.Load().([]sense.HAState)
				return v
			},
			func(a sense.ActService) error {
				c, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				return cfg.Do(c, nil, a)
			},
			func(f string, args ...any) { fmt.Printf(f+"\n", args...) })
	}

	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	// **失败要退避, 但不能退出.**
	//
	// HA 重启、网络抖动都是常态. 一个"连不上就退出"的采集器,
	// 会在 HA 每次升级之后永久消失, 而且没人会发现 ——
	// 表现为"它最近好像什么都不知道了".
	fails := 0
	for {
		select {
		case <-stop:
			cancel()
			fmt.Println("采集器退出")
			return
		case <-t.C:
			// **先报到, 再干活.**
			//
			// 报到必须在拉取**之前**: 放在后面的话, 拉不动的时候
			// (令牌过期、HA 停机)这一句就永远走不到 —— 而那恰恰是
			// 最需要让 OS 知道"我还在, 只是投不出东西"的时候.
			//
			// 长跑里真撞过: HA 令牌过期, 采集器每次都报错重试,
			// 而 OS 那边一个字都没有, 用户看到的是"今天很安静".
			sink.Beat("ha", cfg.Interval)
			states, err := sense.FetchStates(cfg, nil)
			if err != nil {
				fails++
				// **如实报"我拿不到数据"** —— 光报到的话 OS 看到的是
				// "它很健康", 而账本里一条信号都没有. 那天就是这样:
				// 令牌过期, 采集器每次都报错重试, 用户看到"今天很安静"
				sink.BeatFailing("ha", cfg.Interval, trunc(err.Error(), 120))
				// 退避到最多一分钟. 不打印每一次 —— HA 停机一小时的话,
				// 每 10 秒一行日志会把真正的问题淹掉
				if fails <= 3 || fails%30 == 0 {
					fmt.Fprintf(os.Stderr, "拉 HA 失败(第 %d 次): %v\n", fails, err)
				}
				time.Sleep(backoff(fails))
				continue
			}
			fails = 0
			lastStates.Store(states)
			// **真拿到数据了才算"我还好"** —— 光报到只证明进程还在
			sink.BeatOK("ha", cfg.Interval)

			sigs := bridge.Step(states)
			if d := backlog.Add(sigs); d > 0 {
				// 丢弃必须说出来 —— 静默丢会让"为什么那段时间它什么都不知道"
				// 永远查不出来
				fmt.Fprintf(os.Stderr, "缓冲满, 丢了 %d 条最老的信号(累计 %d)\n",
					d, backlog.TotalDropped())
			}
			pending := backlog.Take()
			counts, err := sink.Post(pending)
			if err != nil {
				// 投不出去就放回缓冲 —— HA 的 last_changed 只留最新一次,
				// 这里丢了就永久没有了
				backlog.Add(pending)
				fmt.Fprintf(os.Stderr, "投递失败(缓冲 %d 条): %v\n", backlog.Len(), err)
				continue
			}
			if len(counts) > 0 {
				fmt.Printf("投了 %d 条: %v\n", len(pending), counts)
			}
		}
	}
}

func backoff(fails int) time.Duration {
	d := time.Duration(fails) * 2 * time.Second
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return d
}

func envFloat(k string, d float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil && f > 0 {
		return f
	}
	return d
}

// trunc 报给 OS 的原因要短 —— 它会被打到终端上, 一屏的堆栈没人看
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// envOn 这个开关打开了没有 —— 1/true/yes/on 都算开. 默认关
func envOn(k string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
