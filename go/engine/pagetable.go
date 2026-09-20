// Package engine 是全机唯一的执行引擎.
//
// 执行体是轻的: 上下文页表 + 状态 + 能力集 + 一个 ReAct 游标.
// 引擎是重的、共享的: 页存储 / prompt 组装 / 连接池 / 调度.
//
// 把上下文当虚拟内存来管:
//
//	页        = 一条内容 (输入/工具结果/文档片段), 内容寻址, 不可变
//	地址空间  = 一个执行体"记得"的全部, 由 base 链 + 自己的页组成
//	Fork      = O(1). 子只持有对父的引用, 不复制任何内容
//	换出/换入 = 内容进 swap, 页仍在地址空间里. **无损**
//
// 为什么是 base 链而不是数组拷贝:
//
//	子往自己的 own 里追加, **永远不会动到父的任何字节**.
//	于是父的序列化结果永远是子的字节前缀 —— 提供方的前缀缓存必然命中.
//	用数组拷贝也能做到, 但 Fork 就是 O(n) 且没有结构性保证.
//
// 跟旧做法的区别:
//
//	旧: 压缩 = 丢弃. 丢了就没了.
//	新: 换出 = 内容进 swap, 页号还在. 引用到就换回来.
package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type PageID = string

type PageKind string

const (
	KindInput      PageKind = "input"
	KindOutput     PageKind = "output"
	KindToolResult PageKind = "tool_result"
	KindDoc        PageKind = "doc"
	KindNote       PageKind = "note"
)

type Page struct {
	ID      PageID   `json:"id"`
	Kind    PageKind `json:"kind"`
	Content any      `json:"content"`
	// Bytes 序列化后的字节数 —— 传输成本的真实单位
	Bytes int `json:"bytes"`
}

// PageFault 页不在物理内存里 —— 需要先换入
type PageFault struct{ PageID PageID }

func (e *PageFault) Error() string { return "page fault: " + e.PageID }

// PageStore 内容寻址页存储 · 全引擎唯一.
//
// 相同内容只存一份 —— 这是"多个执行体共享同一份理解"的物理基础.
type PageStore struct {
	mu       sync.RWMutex
	resident map[PageID]Page
	// swap 换出区. 真实现里这是磁盘/事件日志
	swap map[PageID]Page
	// appendCount 每页被追加过多少次. **这不是引用计数**, 不参与回收.
	// 留着是因为它能回答"这份内容被多少处独立产生过", 是去重效果的度量.
	appendCount map[PageID]int

	// refs 真正的引用计数: 有多少个活着的地址空间持有这一页.
	// 只有 refs 归零的页才可能被删 —— 这是"绝不丢活页"那条底线的载体.
	refs map[PageID]int
	// lastUse LRU 时钟. 用单调计数而不是墙钟 —— 墙钟会被系统时间调整搅乱
	lastUse map[PageID]uint64
	clock   uint64

	capacity Capacity
}

// 缺省容量.
//
// ── 为什么不能是 0 ──
//
// Reclaim 的两步都写着 `if cap.XxxBytes > 0`, 所以零值容量 = **永不回收**.
// 而生产路径一直是裸的 NewPageStore(), 从没设过容量 ——
// 于是回收模块写了三百行、十个单测, **真实运行中一次都没生效过**.
//
// 这就是我自己定下的那条: 只支持不接入等于没有, 那是 fail-open.
// (上一次是 EventLog.Restore, 同样写着"落盘用"但没人调.)
//
// 危害不是理论上的: 我们的设计是"一次对话一个长期存活的进程",
// 那个进程可能活几天, 它的页表只增不减.
//
// 数值按实际用量定: 一段对话的历史撑死几 MB (上下文预算本身才 256KB),
// 软限 8MB 已经很宽; 到 32MB 说明有异常, 该开始删死页了.
// 想要真"不回收"必须**显式**说 (SetCapacity 传负数), 不能靠零值默认成那样.
const (
	defaultSoftBytes = 8 << 20
	defaultHardBytes = 32 << 20
)

func NewPageStore() *PageStore {
	return &PageStore{
		resident: map[PageID]Page{}, swap: map[PageID]Page{},
		appendCount: map[PageID]int{},
		refs:        map[PageID]int{},
		lastUse:     map[PageID]uint64{},
		capacity:    Capacity{SoftBytes: defaultSoftBytes, HardBytes: defaultHardBytes},
	}
}

func (s *PageStore) Put(kind PageKind, content any) Page {
	body := StableStringify(content)
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(body))
	id := hex.EncodeToString(h.Sum(nil))[:32]

	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.resident[id]; ok {
		s.appendCount[id]++
		s.touch(id)
		return p
	}
	if p, ok := s.swap[id]; ok {
		s.appendCount[id]++
		s.touch(id)
		return p
	}
	p := Page{ID: id, Kind: kind, Content: content, Bytes: len(body)}
	s.resident[id] = p
	s.appendCount[id] = 1
	s.touch(id)
	return p
}

// Get 取页. 已换出 → 返回 PageFault, 由调用方决定要不要换回来
func (s *PageStore) Get(id PageID) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.resident[id]; ok {
		s.touch(id) // 记一次访问, LRU 靠它
		return p, nil
	}
	if _, ok := s.swap[id]; ok {
		return Page{}, &PageFault{PageID: id}
	}
	return Page{}, fmt.Errorf("unknown page: %s", id)
}

func (s *PageStore) IsResident(id PageID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.resident[id]
	return ok
}

func (s *PageStore) Has(id PageID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, a := s.resident[id]
	_, b := s.swap[id]
	return a || b
}

// Appends 这一页被追加过几次. 名字刻意不叫 Refs —— 它不参与回收
func (s *PageStore) Appends(id PageID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appendCount[id]
}

// BytesOf 页的字节数, 不管它在不在物理内存里 —— 换出不改变逻辑大小
func (s *PageStore) BytesOf(id PageID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.resident[id]; ok {
		return p.Bytes
	}
	if p, ok := s.swap[id]; ok {
		return p.Bytes
	}
	return 0
}

// Evict 换出 —— **无损**. 内容进 swap, 页号仍然有效, 地址空间不变.
// 这就是"压缩"的正确形态: 不是删除, 是移出物理内存.
func (s *PageStore) Evict(id PageID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.resident[id]
	if !ok {
		return false
	}
	delete(s.resident, id)
	s.swap[id] = p
	return true
}

// Fault 换入 —— 缺页中断的处理. 由 OS 触发, 不需要模型自己想起来
func (s *PageStore) Fault(id PageID) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.swap[id]
	if !ok {
		return Page{}, fmt.Errorf("page not in swap: %s", id)
	}
	delete(s.swap, id)
	s.resident[id] = p
	s.touch(id)
	return p, nil
}

type Stats struct {
	Resident      int
	Swapped       int
	ResidentBytes int
}

func (s *PageStore) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{Resident: len(s.resident), Swapped: len(s.swap)}
	for _, p := range s.resident {
		st.ResidentBytes += p.Bytes
	}
	return st
}

// ContextSpace 上下文地址空间 · 一个执行体一个.
//
// 结构是 base 链: [根的页...] ++ [父的页...] ++ [自己的页...]
// 追加只动自己那截, 所以祖先的字节永远稳定.
type ContextSpace struct {
	Store *PageStore
	base  *ContextSpace
	Label string

	mu sync.RWMutex
	// own 本空间自己追加的页
	own []PageID
	// dropped 已经显式交还给页表的页 —— 幂等靠它,
	// 同一页 Drop 两次不能把别人的引用减没
	dropped map[PageID]bool
	// pendingDrop 有子空间挂着时先攒着, 等它们都走了再交还.
	// 子空间靠 base 的引用活着, 这时候减引用会删掉它们正在用的页.
	pendingDrop []PageID
	// childRefs 有多少个活着的子空间还挂在自己身上
	childRefs int
	// selfReleased 拥有者是否已经放手.
	//
	// 必须跟 childRefs 分开记: 合成一个计数的话, 拥有者重复调 Release
	// 会把子持有的那份也减掉 —— 于是子突然"想不起"继承来的东西.
	// 这是真机测试之前先被单元测试抓到的一个设计缺陷.
	selfReleased bool
	freed        bool
}

func NewContextSpace(store *PageStore, label string) *ContextSpace {
	return &ContextSpace{Store: store, Label: label}
}

// Fork 派生一个子空间 —— O(1), 不复制任何内容.
//
// 子持有父的一份引用: 父被释放了, 只要还有活着的子, 父的页就不能被回收.
// 否则子会突然"想不起"它继承来的东西 —— 那正是最难查的一类丢数据.
func (c *ContextSpace) Fork(label string) *ContextSpace {
	c.mu.Lock()
	c.childRefs++
	c.mu.Unlock()
	return &ContextSpace{Store: c.Store, base: c, Label: label}
}

func (c *ContextSpace) Append(kind PageKind, content any) Page {
	p := c.Store.Put(kind, content)
	c.mu.Lock()
	c.own = append(c.own, p.ID)
	c.mu.Unlock()
	c.Store.Retain([]PageID{p.ID})
	return p
}

// Release 释放这个地址空间 —— 进程进终态时调用.
//
// 引用归零才真的放掉自己的页, 并连带释放对 base 的引用.
// 幂等: 重复调用不会把别人的引用减没.
// Drop 告诉页表: **这一页我不要了**.
//
// ── 为什么必须有这个动作 ──
//
// 引用计数管的是"还有没有人可能用到它", 而回收只删引用为 0 的页.
// 于是只要地址空间还活着, 它 Append 过的每一页都被 Retain 着 ——
// 硬上限对**活着的对话完全不生效**.
//
// 真机量到的样子: 活字节 3013 / 硬上限 2000 / 删除 0 / StillOver=true.
// 一个跑几天的常驻 agent 会一直涨.
//
// 但"静默丢活页"也是错的(那会让模型看到一步凭空消失). 出路是让**应用
// 显式交还**它确定不再需要的页 —— 折叠过的结果就是这样的页:
// 它的内容永远不会再进请求了, 留着只是占内存.
//
// 这就是 madvise(MADV_DONTNEED) 那件事: OS 不猜应用要什么,
// 应用告诉 OS 它不要什么.
//
// 幂等: 同一页 Drop 两次不会把别人的引用减没.
//
// **有子空间挂着时只记账不真的交还.** 子空间(fork 出去的)是靠 base 的
// 引用活着的 —— 这时候减引用会把它们正在用的页删掉, 而对方毫无察觉.
// 等最后一个子空间走了再一起交还.
func (c *ContextSpace) Drop(ids []PageID) {
	if len(ids) == 0 {
		return
	}
	c.mu.Lock()
	if c.dropped == nil {
		c.dropped = map[PageID]bool{}
	}
	fresh := make([]PageID, 0, len(ids))
	for _, id := range ids {
		if c.dropped[id] {
			continue
		}
		c.dropped[id] = true
		fresh = append(fresh, id)
	}
	held := c.childRefs > 0
	if held {
		c.pendingDrop = append(c.pendingDrop, fresh...)
	}
	c.mu.Unlock()
	if !held {
		c.Store.ReleaseIDs(fresh)
	}
}

func (c *ContextSpace) Release() {
	c.mu.Lock()
	if c.selfReleased {
		c.mu.Unlock()
		return // 幂等: 拥有者重复放手不做任何事
	}
	c.selfReleased = true
	c.mu.Unlock()
	c.maybeFree()
}

// releaseChildRef 由子空间在自己释放完之后调用
// releaseChildRef 一个子空间走了.
//
// 最后一个走的时候, 把之前攒下的交还一次性做掉 ——
// 那些页在子空间还活着时不能减引用.
func (c *ContextSpace) releaseChildRef() {
	c.mu.Lock()
	if c.childRefs > 0 {
		c.childRefs--
	}
	var flush []PageID
	if c.childRefs == 0 && len(c.pendingDrop) > 0 {
		flush = c.pendingDrop
		c.pendingDrop = nil
	}
	c.mu.Unlock()
	c.Store.ReleaseIDs(flush)
	c.maybeFree()
}

// maybeFree 拥有者放手了、且没有活着的子 → 才真的放掉自己的页
// maybeFree 归零时把自己那些页还回去.
//
// **已经 Drop 过的不能再还一次** —— 那会把别人(比如另一个 fork)
// 的引用减没, 页被删掉而对方还在用.
func (c *ContextSpace) maybeFree() {
	c.mu.Lock()
	if c.freed || !c.selfReleased || c.childRefs > 0 {
		c.mu.Unlock()
		return
	}
	c.freed = true
	// 已经 Drop 过的不能再还一次
	own := make([]PageID, 0, len(c.own))
	for _, id := range c.own {
		if !c.dropped[id] {
			own = append(own, id)
		}
	}
	base := c.base
	c.mu.Unlock()

	c.Store.ReleaseIDs(own)
	if base != nil {
		base.releaseChildRef()
	}
}

// OwnLength 本空间独有的页数 (不含继承来的)
func (c *ContextSpace) OwnLength() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.own)
}

func (c *ContextSpace) Length() int {
	n := 0
	if c.base != nil {
		n = c.base.Length()
	}
	c.mu.RLock()
	n += len(c.own)
	c.mu.RUnlock()
	return n
}

// PageIDs 展开成有序页号列表
func (c *ContextSpace) PageIDs() []PageID {
	var out []PageID
	if c.base != nil {
		out = c.base.PageIDs()
	}
	c.mu.RLock()
	out = append(out, c.own...)
	c.mu.RUnlock()
	return out
}

// SharedPrefixWith 与另一个空间的共享前缀长度 ——
// 这就是能命中提供方前缀缓存的部分. 靠 base 链求最近公共祖先, 不逐页比对.
func (c *ContextSpace) SharedPrefixWith(other *ContextSpace) int {
	mine := map[*ContextSpace]int{}
	for s := c; s != nil; s = s.base {
		mine[s] = s.Length()
	}
	for s := other; s != nil; s = s.base {
		if n, ok := mine[s]; ok {
			return n
		}
	}
	return 0
}

type SharingReport struct {
	Spaces      int     `json:"spaces"`
	NaiveBytes  int     `json:"naiveBytes"`
	SharedBytes int     `json:"sharedBytes"`
	SavedRatio  float64 `json:"savedRatio"`
}

// MeasureSharing 量一组空间的传输节省 —— 这是这套设计要证明的那个数.
//
//	NaiveBytes:  每个执行体各传各的完整上下文 (现在的做法)
//	SharedBytes: 唯一页只算一次 (前缀命中缓存后实际要传的)
func MeasureSharing(spaces []*ContextSpace) SharingReport {
	naive := 0
	unique := map[PageID]int{}
	for _, s := range spaces {
		for _, id := range s.PageIDs() {
			// 换出的页也要算 —— 换出只改变它在不在内存, 不改变逻辑大小
			b := s.Store.BytesOf(id)
			naive += b
			unique[id] = b
		}
	}
	shared := 0
	for _, b := range unique {
		shared += b
	}
	r := SharingReport{Spaces: len(spaces), NaiveBytes: naive, SharedBytes: shared}
	if naive > 0 {
		r.SavedRatio = 1 - float64(shared)/float64(naive)
	}
	return r
}

// StableStringify 确定性序列化 —— 键序稳定.
//
// 字节不稳定 = 缓存全废. 这是引擎必须统一组装的根本原因:
// 多处各自组装, 键序早晚会漂.
//
// 必须跟 TypeScript 版**逐字节一致**, 否则两边算出的页号不同,
// 共享就无从谈起. engine_test.go 用 TS 生成的样本锁死这一点.
func StableStringify(v any) string {
	var sb strings.Builder
	writeStable(&sb, v)
	return sb.String()
}

func writeStable(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		sb.WriteString("null")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			sb.Write(kb)
			sb.WriteByte(':')
			writeStable(sb, t[k])
		}
		sb.WriteByte('}')
	case []any:
		sb.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeStable(sb, e)
		}
		sb.WriteByte(']')
	default:
		// 标量走标准 JSON. 注意 Go 的 json 默认会转义 <>& ——
		// 那会跟 JS 的 JSON.stringify 不一致, 所以关掉.
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(t)
		sb.WriteString(strings.TrimRight(b.String(), "\n"))
	}
}
