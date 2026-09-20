package engine

import "strings"

// prompt 组装器 · 全引擎唯一.
//
// 它保证的性质只有一条, 但这一条决定了所有前缀缓存的成败:
//
//	**父空间的组装结果, 永远是子空间组装结果的字节前缀.**
//
// 有了这条, 提供方的前缀缓存在 Fork 出的每个子执行体上必然命中 ——
// 不需要任何"尽量复用"的启发式, 是结构保证.
//
// 反过来说, 只要有第二个地方也在组装 prompt, 这条就守不住:
// 键序、空白、字段顺序早晚会漂. 这是"一个引擎"而不是
// "每个 agent 一个引擎"的根本理由.

type AssembleOptions struct {
	// SystemSegment 系统段. **必须字节稳定** ——
	// 变一个字节, 所有执行体的缓存全废. 所以它不接受任何随时间变化的东西.
	SystemSegment string
	// BreakAt 在这些页数位置放缓存断点
	BreakAt []int
}

type AssembleResult struct {
	Bytes string
	// Offsets 每一页结束时的字节偏移.
	// **只含页边界, 不含系统段边界** —— 混进去会让 BreakAt 的下标错一位.
	Offsets []int
	// PrefixBytes 系统段占的字节数 (Offsets 的起点)
	PrefixBytes int
	Breakpoints []int
	// Faults 换出的页 —— 组装前必须先换回来
	Faults []PageID
}

func Assemble(space *ContextSpace, opts AssembleOptions) AssembleResult {
	var sb strings.Builder
	var offsets []int
	var faults []PageID
	cursor := 0

	prefixBytes := 0
	if opts.SystemSegment != "" {
		sb.WriteString(opts.SystemSegment)
		prefixBytes = len(opts.SystemSegment)
		cursor += prefixBytes
		// 刻意不 push —— Offsets[i] 必须恒等于"第 i+1 页结束处"
	}

	for _, id := range space.PageIDs() {
		if !space.Store.IsResident(id) {
			// 换出的页 —— 记下来让调用方换回, 不在这里偷偷跳过.
			// 悄悄跳过就是"压缩掉了但没人知道", 那正是旧做法的病根.
			faults = append(faults, id)
			continue
		}
		p, err := space.Store.Get(id)
		if err != nil {
			faults = append(faults, id)
			continue
		}
		chunk := StableStringify(map[string]any{"k": string(p.Kind), "c": p.Content})
		sb.WriteString(chunk)
		cursor += len(chunk)
		offsets = append(offsets, cursor)
	}

	var breakpoints []int
	for _, n := range opts.BreakAt {
		if n-1 >= 0 && n-1 < len(offsets) {
			breakpoints = append(breakpoints, offsets[n-1])
		}
	}

	return AssembleResult{Bytes: sb.String(), Offsets: offsets,
		PrefixBytes: prefixBytes, Breakpoints: breakpoints, Faults: faults}
}

// PlanBreakpoint 给一组同源的执行体算最优缓存断点.
// 断点放在它们的共享前缀末尾 —— 那是所有子都能命中的最长部分.
func PlanBreakpoint(parent *ContextSpace, children []*ContextSpace) int {
	if len(children) == 0 {
		return parent.Length()
	}
	min := -1
	for _, c := range children {
		n := c.SharedPrefixWith(parent)
		if min == -1 || n < min {
			min = n
		}
	}
	if min < 0 {
		return 0
	}
	return min
}
