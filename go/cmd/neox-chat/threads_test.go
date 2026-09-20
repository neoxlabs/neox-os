package main

import "testing"

// **"开新话题"是一次明确的放弃 —— 之前所有"待接续"都得作废.**
//
// /新 原来只关掉当前进程, 而 pendingResume 还留着(它可能是 /继续 3
// 刚设上的, 也可能是一次批准设上的). 开启新话题后,
// 下一句话却被并回上一段对话 —— **而且没有任何报错**,
// 他只会觉得"这系统的新话题是假的"。
func TestNewTopicDropsPendingResume(t *testing.T) {
	var s chatThreads
	s.Continue("t-3") // /继续 3
	s.NewTopic()      // 用户改主意: 开新的
	if got := s.SpawnInto("p9"); got != "" {
		t.Fatalf("下一句话被并回了 %q —— 用户说了开新话题", got)
	}
	if s.Current() != "p9" {
		t.Fatalf("新对话的线头该是它自己, 实际 %q", s.Current())
	}
}

// **/新 也要把批准的那一膛落掉.**
//
// 上一轮批准过、还没收尾时用户打了 /新: 旧进程收尾时 Fire() 还会
// 返回 true, 于是自动冒出一句"接着干" —— 接着的是**用户刚刚放弃的**
// 那件事, 而且冒在新话题里。
func TestNewTopicDisarmsApproval(t *testing.T) {
	var s chatThreads
	s.Approve() // 用户批了, 等收尾
	s.NewTopic()
	if s.FireResume() {
		t.Fatal("开了新话题之后旧进程收尾, 它还自己冒出一句去干上一件事")
	}
}

// 正常那条路要照走: 批准 → 收尾 → 自动接着干, 并且并回**原来那段**
func TestApprovalResumesIntoSameThread(t *testing.T) {
	var s chatThreads
	s.SpawnInto("p1") // 一段新对话, 线头是 p1
	s.Approve()
	if !s.FireResume() {
		t.Fatal("批准之后收尾, 没有自动接着干 —— 用户点了允许却什么都没发生")
	}
	if got := s.SpawnInto("p2"); got != "p1" {
		t.Fatalf("接着干的那一句并进了 %q, 该是原来那段 p1", got)
	}
}

// 接续只用一次 —— 用完就清, 否则后面每一句话都往那段老对话里灌
func TestResumeIsConsumedOnce(t *testing.T) {
	var s chatThreads
	s.Continue("t-3")
	if got := s.SpawnInto("p1"); got != "t-3" {
		t.Fatalf("第一句没并进 t-3, 而是 %q", got)
	}
	if got := s.SpawnInto("p2"); got != "" {
		t.Fatalf("第二句又往 t-3 里灌了(%q) —— 接续该只用一次", got)
	}
}

// 批准记的是**当前**线头, 不是一个还没用掉的接续
func TestApproveUsesCurrentThread(t *testing.T) {
	var s chatThreads
	s.SpawnInto("p1")
	s.Approve()
	s.FireResume()
	if got := s.SpawnInto("p2"); got != "p1" {
		t.Fatalf("批准之后接进了 %q, 该是当前这段 p1", got)
	}
}

// 拒绝不换进程 —— 被拒的能力本来就没有, 旧进程可以接着想别的办法
func TestDenyDoesNotArm(t *testing.T) {
	var s chatThreads
	s.SpawnInto("p1")
	s.Deny()
	if s.FireResume() {
		t.Fatal("拒绝之后也自动接着干了 —— 白白扔掉一轮上下文")
	}
}
