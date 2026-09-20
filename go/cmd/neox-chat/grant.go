package main

import "sync"

// 批准之后自己把话接上.
//
// ── 为什么需要它 ──
//
// 内核的能力集在 exec 时定死, 只能收紧不能放宽 —— 这是对的, 也必须是这样,
// 否则"批准"就成了一条能对着正在跑的进程放权的后门. 代价是**扩权在当前
// 进程里永远生效不了**, 得换个进程重新开始.
//
// agent 申请连接 pypi 的三个主机来安装 openpyxl, 获得批准后只能收尾说
// "授权已生效, 但这一轮还装不成……下一句话随便说句'继续'我就装".
// 如果批准不触发后续动作, 还要额外输入一次"继续" —— **这等于让人替 OS 干活**.
// "允许"本身就表示可以继续, 不应再要求第二个指令.
//
// ── 为什么要等收尾 ──
//
// 批准的那一刻旧进程还在跑 (它正卡在决策上, 拿到答复会继续走完这一轮).
// 这时候起新进程, 同一段对话上就同时挂着两个 —— 两边都在写同一个卷、
// 都在往同一条 thread 上追加事件. 所以必须等它收尾.
//
// ── 为什么只认 yes ──
//
// 拒绝不需要换进程: 被拒的能力本来就没有, 旧进程拿到"不行"这个答复
// 可以接着想别的办法. 对拒绝也重开进程是白白扔掉一轮的上下文.
type grantResumer struct {
	mu    sync.Mutex
	armed bool
}

// Arm 记下"批过了, 收尾之后要接着干". 只有批准才算数.
// 返回是否真的上了膛 —— 调用方据此决定是否发送后续提示.
func (g *grantResumer) Arm(answer string) bool {
	if answer != "yes" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = true
	return true
}

// Fire 当前进程收尾了, 该不该自动接着干.
//
// **只响一次**: 响过就落膛. 不然接下来每一轮结束都会再自动说一句话,
// 那是个永动机 —— 用户没说话, 它自己跟自己聊下去.
func (g *grantResumer) Fire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.armed {
		return false
	}
	g.armed = false
	return true
}
