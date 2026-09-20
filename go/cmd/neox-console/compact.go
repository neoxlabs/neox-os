package main

// 上下文压紧 —— **后台写摘要, 轮次之间换上**.
//
// ── 为什么要有这一步 ──
//
//	一段对话是长期存活的进程, 历史还跨重启装回来: 一个 bot
//	从开机到现在的每一轮都装. 一个进程起手 32801 个
//	prompt token, 聊到晚上 73107 —— 而折叠那条路一次都没触发过
//	(窗口 100 万, 预算 175 万字节, 够聊几个月).
//
//	所以今天的样子是**什么都不丢, 全程原样重发**: 窗口撑得住, 但每一轮
//	都在为三天前那段闲聊付钱(缓存命中也要钱), 而它得在几万 token 的流水里
//	找"他刚才说什么".
//
// ── 为什么摘要要在后台写 ──
//
//	写摘要本身是一次推理, 几秒. 摆在他这句话前面就是白等几秒 ——
//	而压紧根本不急在这一轮: 上下文是慢慢涨的.
//
//	所以: 这一轮结束时发现该压了 → 后台去写; 写好了放着;
//	**下一轮结束时**才换上去(换的那一刻不在任何一轮中间, 见 Window.CompactFrom
//	对成对动作/结果的要求).

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
)

const (
	// compactAt 历史涨到这么大就该压了. 约 4 万 token(中文 3 字节上下一个 token)
	compactAt = 120 << 10
	// compactKeep 留最近这么多原文 —— 再往前的换成摘要
	compactKeep = 40 << 10
	// compactHeadMax 喂给摘要器的素材上限. 超过这个数就只摘最近的那部分,
	// 不值得为一次摘要再付一次长上下文
	compactHeadMax = 24 << 10
)

// compactor 一段对话一个. 状态只有两个: 后台在写吗、写好的那份.
type compactor struct {
	busy  atomic.Bool
	ready atomic.Value // string
}

// afterTurn 一轮结束时看一眼 —— **换上 / 起草 / 什么都不做**.
func (c *components) afterTurn(cp *compactor, win *agent.Window, emit func(map[string]any)) {
	if cp == nil || win == nil {
		return
	}
	// ① 上一轮起草的摘要写好了 → 现在换上去
	if s, _ := cp.ready.Load().(string); strings.TrimSpace(s) != "" {
		cp.ready.Store("")
		from := win.KeepFromTailBytes(compactKeep)
		before := win.Bytes()
		if n := win.CompactFrom(s, from); n > 0 && emit != nil {
			// **摘要本身要落进账本**: 不落的话它只活在内存里, 重启之后
			// historyOf 把原文全量装回 —— 压了等于没压(见 RestoreWindow)
			emit(map[string]any{"phase": "compacted", "summary": s,
				"dropped": n, "before": before, "after": win.Bytes()})
		}
		return
	}
	// ② 该压了就后台起草一份. **素材在这一刻取好**: 后台那条路
	// 一个字节都不许碰 win —— 它跟下一轮是并行的
	if cp.busy.Load() || win.Bytes() < compactAt {
		return
	}
	head := win.HeadText(win.KeepFromTailBytes(compactKeep), compactHeadMax)
	if strings.TrimSpace(head) == "" {
		return
	}
	cp.busy.Store(true)
	go func() {
		defer cp.busy.Store(false)
		s, err := c.summarize(head)
		if err != nil {
			if emit != nil {
				emit(map[string]any{"phase": "compact_failed", "err": err.Error()})
			}
			return
		}
		cp.ready.Store(s)
	}()
}

// summarizePrompt 摘要器的系统段.
//
//	**要的是事实, 不是文学**: 这段话接下来要替代原文活在上下文里,
//	它记错一件事, 后面每一轮都照着错的答.
const summarizePrompt = `把下面这段"他和你"的对话收成一份事实清单，给你自己以后接着用。

只留还算数的东西：他交代过什么、你替他记了什么、定下来的时间地点人名数字、
还欠着没做完的事。过程、寒暄、已经作废的说法都不要。

用短句，一行一条，不超过 20 行。不要开场白，不要总结感想。`

// summarize 真去写一次摘要. **走这台机器当前的模型** —— 换模型是设置页
// 的事, 这儿不该自己挑一个.
func (c *components) summarize(head string) (string, error) {
	if c.os == nil || c.os.Provider() == nil {
		return "", fmt.Errorf("这台机器没有推理服务")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := c.os.Provider().Infer(ctx, abi.InferParams{
		System:    summarizePrompt,
		Messages:  []abi.InferMessage{{Role: "user", Content: head}},
		MaxTokens: 900,
	})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(res.Content)
	if out == "" {
		return "", fmt.Errorf("摘要是空的")
	}
	return out, nil
}
