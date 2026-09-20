package main

// 线头记账 —— 下一句话该并进哪一段对话.
//
// ── 为什么要收成一个类型 ──
//
// 原来是三个散落的变量(pendingResume / threadOfCurrent / grants)
// 加上七八处直接赋值. 每加一条命令都得记得"这条要不要清 pendingResume、
// 要不要落膛" —— 而漏掉的症状全是静默的:
//
//	/继续 3 之后打 /新   下一句话还是被并回第 3 段 ——
	//	                     开新话题后仍记着刚才的事,
//	                     没有任何报错, 他只会觉得新话题是假的
//	批准之后打 /新       旧进程收尾时还会自动冒出一句"接着干",
//	                     接着的是**他刚刚放弃的**那件事, 而且冒在新话题里
//
// 两条都是"忘了清状态", 跟 S32 那一族(忘了恢复状态)是镜像的另一半.
type chatThreads struct {
	// pending 下一个进程**将要加入**哪一段. 空 = 全新对话
	pending string
	// current **当前**这段对话的线头 —— 批准之后靠它把对话接续到新进程
	current string
	grants  grantResumer
}

// Continue 用户挑了一段接着聊
func (s *chatThreads) Continue(thread string) { s.pending = thread }

// NewTopic 开新话题.
//
// **这是一次明确的放弃**: 待接续的那一段作废, 批准的那一膛也落掉.
// 少做任何一样, 上一段对话都会从某个缝里渗回来 —— 而用户刚说过不要它.
func (s *chatThreads) NewTopic() {
	s.pending = ""
	s.current = ""
	s.grants.Fire() // 落膛; 返回值不关心, 这里只是清掉
}

// Approve 用户批了, 等这一轮收尾就自己接着干.
//
// 接的是**当前**这段(current), 不是某个还没用掉的接续 ——
// 批准是对着眼前这件事说的.
func (s *chatThreads) Approve() bool { return s.grants.Arm("yes") }

// Deny 拒绝. 不换进程: 被拒的能力本来就没有, 旧进程拿到"不行"
// 可以接着想别的办法, 重开一轮是白白扔掉上下文
func (s *chatThreads) Deny() bool { return s.grants.Arm("no") }

// FireResume 当前进程收尾了, 该不该自动接着干. 只响一次
func (s *chatThreads) FireResume() bool {
	if s.grants.Fire() {
		s.pending = s.current // 接回原来那段
		return true
	}
	return false
}

// SpawnInto 起进程了 —— 返回它该并进哪一段(空 = 全新一段).
//
// **接续只用一次**: 用完就清, 否则后面每一句话都往那段老对话里灌.
func (s *chatThreads) SpawnInto(pid string) string {
	join := s.pending
	s.pending = ""
	if join != "" {
		s.current = join
	} else {
		s.current = pid // 新对话: 它自己就是线头
	}
	return join
}

// Current 当前这段的线头
func (s *chatThreads) Current() string { return s.current }
