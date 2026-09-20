package main

import "testing"

// **主动进程一起来就烧一次推理, 而它手上什么都没有.**
//
// 真机长跑里看到的: 进程刚起来, 日志里就是
//
//	[用量 prompt=1099 缓存=1024(93%) 输出=90]
//	待命中。当前没有需要判断的摘要或事件——什么都不做，直接收工。
//
// 因为 NEOX_TASK("待命。有摘要进来时判断值不值得打扰用户…")被当成了
// 第一句话, 于是 Serve 立刻跑了一整轮.
//
// **不只是浪费一次调用**(每回收一次还要再烧一次, 每 30 份摘要一回收).
// 更糟的是: 它在**没有任何依据**的情况下被要求判断"要不要打扰用户",
// 而模型在没有依据时的输出是不可预期的 —— 那正是这套设计最怕的
// (判断力一飘, 用户就把通知关掉, 真正重要的那次也到不了他).
func TestProactiveDoesNotThinkBeforeItHasAnything(t *testing.T) {
	if first := proactiveFirstTurn("待命。有摘要进来时判断值不值得打扰用户。"); first != "" {
		t.Fatalf("主动进程开局就跑了一轮, 输入是 %q —— "+
			"它手上一份摘要都没有, 却被要求判断要不要打扰用户", first)
	}
}

// 但那句"你是干什么的"不能丢 —— 它得进上下文, 只是不该触发一轮推理.
// 丢了的话第一份摘要来时它不知道自己该沉默
func TestProactiveKeepsItsStandingOrder(t *testing.T) {
	const task = "待命。有摘要进来时判断值不值得打扰用户。"
	if got := proactiveStandingOrder(task); got != task {
		t.Fatalf("常驻指令没带上: %q", got)
	}
}
