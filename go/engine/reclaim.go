package engine

import (
	"fmt"
	"sort"
)

// 页回收 —— 让页存储不再只增不减.
//
// 这是上感知层之前的**硬前置**: 一天几千条信号, 只增不减,
// 几周就把盘吃满.
//
// 但回收有一条不能破的底线:
//
//	**绝不能丢掉还被活着的地址空间引用的页.**
//
// 丢了就是"压缩把用户原话删了"那个坑重演 —— 而且这次是静默的,
// 只表现为某个 agent 突然想不起一件它明明记过的事.
//
// 所以分两步, 严格区分:
//
//	降级 (Demote): 热 → 温. **无损**, 内容进 swap, 页号仍有效, 随时换回
//	删除 (Delete): 只对**引用计数为 0** 的页. 有损, 但那些页已经没人要了
//
// 如果全是活页还超限 —— **不许静默丢**, 要把压力报出来,
// 让 OS 去挂起/终止进程. 沉默地丢数据比 OOM 危险得多.

type Tier string

const (
	// TierHot 常驻物理内存, 每次推理都带着
	TierHot Tier = "hot"
	// TierWarm 在 swap 里, 引用到才换入
	TierWarm Tier = "warm"
)

// Capacity 页存储的容量闸.
//
// 两级是刻意的: 软限触发无损降级, 硬限才触发有损删除.
// 只有一级的话, 要么太早开始丢东西, 要么丢的时候已经晚了.
type Capacity struct {
	// SoftBytes 常驻字节软限. 超了就把最久没碰的页降级到 swap (无损)
	SoftBytes int
	// HardBytes 总字节 (常驻+swap) 硬限. 超了才删无人引用的页 (有损)
	HardBytes int
}

// ReclaimReport 一次回收的结果. 每个数字都要能对得上账.
type ReclaimReport struct {
	// Demoted 被降级到 swap 的页数与字节 (无损)
	Demoted      int `json:"demoted"`
	DemotedBytes int `json:"demotedBytes"`
	// Deleted 被真正删掉的页数与字节 (只删引用计数为 0 的)
	Deleted      int `json:"deleted"`
	DeletedBytes int `json:"deletedBytes"`
	// StillOver 回收之后仍然超硬限 —— 说明全是活页, 光靠回收解决不了
	StillOver bool `json:"stillOver"`
	// LiveBytes 活页占的字节 (回收不掉的下界)
	LiveBytes int `json:"liveBytes"`
}

// Pressure 回收也压不下去时报出来的压力信号.
//
// 这是给 OS 看的: 该挂起谁、该终止谁, 由调度层决定, 不由页存储决定.
// 页存储的职责到"如实报告"为止 —— 它不该替调度层做决定.
type Pressure struct {
	TotalBytes int
	LiveBytes  int
	HardBytes  int
}

func (p Pressure) Error() string {
	return fmt.Sprintf("页存储超限且无法回收: 总 %d / 活页 %d / 上限 %d 字节",
		p.TotalBytes, p.LiveBytes, p.HardBytes)
}

// SetCapacity 设定容量闸. 零值表示不限.
// Capacity 当前容量闸 —— 自检要能看见它, 否则'回收是死的'这种事查不出来
func (s *PageStore) Capacity() Capacity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.capacity
}

func (s *PageStore) SetCapacity(c Capacity) {
	s.mu.Lock()
	s.capacity = c
	s.mu.Unlock()
}

// Retain 给一批页加引用 —— ContextSpace 持有页时调用
func (s *PageStore) Retain(ids []PageID) {
	s.mu.Lock()
	for _, id := range ids {
		s.refs[id]++
	}
	s.mu.Unlock()
}

// ReleaseIDs 给一批页减引用. 减到 0 的页**不立即删**,
// 只是变成"可回收" —— 万一马上又被引用, 省一次重建.
func (s *PageStore) ReleaseIDs(ids []PageID) {
	s.mu.Lock()
	for _, id := range ids {
		if s.refs[id] > 0 {
			s.refs[id]--
			if s.refs[id] == 0 {
				delete(s.refs, id)
			}
		}
	}
	s.mu.Unlock()
}

// Refs 某页当前的引用数 —— 这才是真的引用计数.
//
// 注意跟 Appends() 的区别: 后者是"被追加过几次", 只增不减, 不参与回收.
func (s *PageStore) Refs(id PageID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.refs[id]
}

// touch 记一次访问 —— LRU 用. 必须在持锁时调用.
func (s *PageStore) touch(id PageID) {
	s.clock++
	s.lastUse[id] = s.clock
}

// Reclaim 回收一轮.
//
// 顺序: 先无损降级, 不够再有损删除, 还不够就报压力.
// **永远不动活页.**
func (s *PageStore) Reclaim() (ReclaimReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var rep ReclaimReport
	cap := s.capacity

	// ── 第一步: 常驻超软限 → 降级最久没碰的页 (无损) ──
	if cap.SoftBytes > 0 {
		for s.residentBytesLocked() > cap.SoftBytes {
			id, ok := s.lruResidentLocked()
			if !ok {
				break // 没得降了
			}
			p := s.resident[id]
			delete(s.resident, id)
			s.swap[id] = p
			rep.Demoted++
			rep.DemotedBytes += p.Bytes
		}
	}

	// ── 第二步: 总量超硬限 → 删无人引用的页 (有损, 但那些页没人要了) ──
	if cap.HardBytes > 0 && s.totalBytesLocked() > cap.HardBytes {
		for _, id := range s.deletableByAgeLocked() {
			if s.totalBytesLocked() <= cap.HardBytes {
				break
			}
			b := s.bytesOfLocked(id)
			delete(s.resident, id)
			delete(s.swap, id)
			delete(s.lastUse, id)
			delete(s.appendCount, id)
			rep.Deleted++
			rep.DeletedBytes += b
		}
	}

	rep.LiveBytes = s.liveBytesLocked()

	// ── 第三步: 还超限 → 报压力, **不许静默丢活页** ──
	if cap.HardBytes > 0 && s.totalBytesLocked() > cap.HardBytes {
		rep.StillOver = true
		return rep, Pressure{
			TotalBytes: s.totalBytesLocked(),
			LiveBytes:  rep.LiveBytes,
			HardBytes:  cap.HardBytes,
		}
	}
	return rep, nil
}

func (s *PageStore) residentBytesLocked() int {
	n := 0
	for _, p := range s.resident {
		n += p.Bytes
	}
	return n
}

func (s *PageStore) totalBytesLocked() int {
	n := s.residentBytesLocked()
	for _, p := range s.swap {
		n += p.Bytes
	}
	return n
}

func (s *PageStore) liveBytesLocked() int {
	n := 0
	for id := range s.refs {
		n += s.bytesOfLocked(id)
	}
	return n
}

func (s *PageStore) bytesOfLocked(id PageID) int {
	if p, ok := s.resident[id]; ok {
		return p.Bytes
	}
	if p, ok := s.swap[id]; ok {
		return p.Bytes
	}
	return 0
}

// lruResidentLocked 常驻页里最久没被碰过的那个
func (s *PageStore) lruResidentLocked() (PageID, bool) {
	var best PageID
	var bestUse uint64
	found := false
	for id := range s.resident {
		u := s.lastUse[id]
		if !found || u < bestUse {
			best, bestUse, found = id, u, true
		}
	}
	return best, found
}

// deletableByAgeLocked 可删的页 (引用计数为 0), 最久没碰的排前面.
//
// **活页一个都不进这个列表** —— 这是那条底线的实现点.
func (s *PageStore) deletableByAgeLocked() []PageID {
	var ids []PageID
	for id := range s.resident {
		if s.refs[id] == 0 {
			ids = append(ids, id)
		}
	}
	for id := range s.swap {
		if s.refs[id] == 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return s.lastUse[ids[i]] < s.lastUse[ids[j]] })
	return ids
}

// TierOf 某页现在在哪一级
func (s *PageStore) TierOf(id PageID) (Tier, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.resident[id]; ok {
		return TierHot, true
	}
	if _, ok := s.swap[id]; ok {
		return TierWarm, true
	}
	return "", false
}
