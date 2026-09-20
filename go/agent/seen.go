package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
)

// 谁读过什么, 读到的是哪一版 —— 用来挡住"两个人写同一个文件, 一个人的活
// 悄悄没了".
//
// ── 为什么需要 ──
//
// 拉人进来是**同一个工作区**(见 hireFor: 分开目录的话他连你写的代码都
// 改不了). 于是两个 bot 会同时动同一份文件, 而 write_file 原来是
// **无条件覆盖**的: 后写的那个把先写的那个整份盖掉, 两边都显示成功.
//
// 这种冲突的前兆是: 读取发版的 release.sh 时, 读到的是一个
// 写到一半的版本, 于是当着用户的面指控"你说改了, 真文件里没有" ——
// 那次只是读脏, 还没到丢活.
//
// ── 为什么是"读过没有"而不是加锁 ──
//
// 锁要有人放, 而 bot 会中途被杀、被换房间、被重起 —— 一把没人放的锁
// 比没有锁更糟. 而"你读到的还是不是当初那一版"这件事**每次写之前
// 现算一遍就知道**, 不需要任何人维护状态.
//
// 这也正是提示词里那条("不改你没读过的东西")的强制版: 原来只是劝,
// 现在真的拦得住.

// seenFiles 这个 bot 读到过的每个文件的内容指纹.
//
//	一个 bot 一份: 它只该为**自己**读到的那一版负责.
type seenFiles struct {
	mu   sync.Mutex
	hash map[string]string
}

func newSeenFiles() *seenFiles { return &seenFiles{hash: map[string]string{}} }

// NewSeenFiles 给宿主用 —— 一个 bot 一份.
func NewSeenFiles() *seenFiles { return newSeenFiles() }

func (s *seenFiles) note(path string, content []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hash[path] = digestOf(content)
}

// stale 这个文件在它读过之后是不是被别人改了.
//
//	返回 (变了没有, 说明). 文件不存在 = 新建, 不算.
func (s *seenFiles) stale(path string) (bool, string) {
	if s == nil {
		return false, ""
	}
	current, err := os.ReadFile(path)
	if err != nil {
		// 读不到多半是不存在(新建) —— 新建从来不会覆盖谁
		return false, ""
	}
	s.mu.Lock()
	known, seen := s.hash[path]
	s.mu.Unlock()
	if !seen {
		return true, "你没读过这个文件, 而它已经存在了 —— 可能是同屋的谁刚建的"
	}
	if known != digestOf(current) {
		return true, "这个文件在你读过之后被改了 —— 同屋的谁动了它"
	}
	return false, ""
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
