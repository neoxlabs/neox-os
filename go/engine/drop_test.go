package engine

import "testing"

// **活着的对话必须能被硬上限约束住.**
//
// 引用计数管的是"还有没有人可能用到它", 而回收只删引用为 0 的页.
// 于是只要地址空间还活着, 它 Append 过的每一页都被 Retain 着 ——
// 硬上限对活着的对话完全不生效.
//
// 真机量到: 活字节 3013 / 硬上限 2000 / 删除 0 / StillOver=true.
// 一个跑几天的常驻 agent 会一直涨到 OOM.
func TestLiveSpaceCanBeBoundedByDropping(t *testing.T) {
	s := NewPageStore()
	s.SetCapacity(Capacity{SoftBytes: 1000, HardBytes: 2000})
	sp := NewContextSpace(s, "t")
	defer sp.Release()

	// **内容必须各不相同**: 页表是内容寻址的, 一样的内容会被去重成一页
	// 引用 N 次 —— 那样测的是去重不是回收. (第一版探针就栽在这儿:
	// 50 个页去重成 1 个, refs=50, 怎么 Drop 都删不掉.)
	var ids []PageID
	for i := 0; i < 50; i++ {
		body := make([]byte, 500)
		for j := range body {
			body[j] = byte('a' + i%26)
		}
		ids = append(ids, sp.Append(KindToolResult,
			map[string]any{"result": string(body), "n": i}).ID)
	}
	// 不交还 → 一页都删不掉
	rep, _ := s.Reclaim()
	if rep.Deleted != 0 || !rep.StillOver {
		t.Fatalf("前提变了: 不交还时本该删不动且仍超限, got 删除=%d StillOver=%v",
			rep.Deleted, rep.StillOver)
	}
	// 应用说"前面这些我不要了" → 页表才能真的回收
	before := rep.LiveBytes
	sp.Drop(ids[:40])
	rep2, _ := s.Reclaim()
	if rep2.Deleted == 0 {
		t.Fatal("交还之后仍然一页都删不掉 —— 常驻对话还是会一直涨")
	}
	if rep2.LiveBytes >= before {
		t.Fatalf("交还之后活字节没降: %d → %d", before, rep2.LiveBytes)
	}
	// **剩下没交还的页超限时仍然报压力, 这是对的** ——
	// 不许静默丢活页, 那会让模型看到一步凭空消失.
	// 应用交还多少, 页表才回收多少; 交还得不够就该听见压力.
	t.Logf("活字节 %d → %d, 删除 %d 页", before, rep2.LiveBytes, rep2.Deleted)
}

// Drop 幂等 —— 同一页还两次不能把别人的引用减没
func TestDropIsIdempotent(t *testing.T) {
	s := NewPageStore()
	sp := NewContextSpace(s, "a")
	p := sp.Append(KindToolResult, "x")
	sp.Drop([]PageID{p.ID})
	sp.Drop([]PageID{p.ID})
	sp.Drop([]PageID{p.ID})
	if got := s.Refs(p.ID); got < 0 {
		t.Fatalf("引用被减成负数: %d", got)
	}
}

// 已经 Drop 过的页, Release 时不能再还一次 ——
// 那会把 fork 出去的空间的引用减没, 页被删掉而对方还在用
func TestReleaseDoesNotDoubleFreeDroppedPages(t *testing.T) {
	s := NewPageStore()
	a := NewContextSpace(s, "a")
	p := a.Append(KindToolResult, "共享的内容")
	b := a.Fork("b") // b 也引用了 p
	a.Drop([]PageID{p.ID})
	a.Release()
	if s.Refs(p.ID) <= 0 {
		t.Fatal("fork 出去的空间还在用, 页却已经没有引用了")
	}
	if _, err := s.Get(p.ID); err != nil {
		t.Fatalf("对方还在用却读不到了: %v", err)
	}
	b.Release()
}
