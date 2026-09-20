package engine

import (
	"errors"
	"strings"
	"testing"
)

func bigPage(n int) map[string]any {
	return map[string]any{"pad": strings.Repeat("x", n)}
}

// distinctPage 内容各不相同的大页.
// 用 bigPage 造多页会被内容寻址去重成同一页 —— 那是去重在正确工作,
// 但会让"删最老的那页"这类测试失去意义.
func distinctPage(tag string, n int) map[string]any {
	return map[string]any{"tag": tag, "pad": strings.Repeat("x", n)}
}

// ── 底线: 绝不丢活页 ────────────────────────────────────────

func TestNeverDeletesLivePages(t *testing.T) {
	s := NewPageStore()
	s.SetCapacity(Capacity{HardBytes: 100}) // 故意设得极小
	sp := NewContextSpace(s, "live")
	p := sp.Append(KindDoc, bigPage(5000))

	rep, err := s.Reclaim()
	// 全是活页且超限 → 必须报压力, 而不是把活页删掉
	var pressure Pressure
	if !errors.As(err, &pressure) {
		t.Fatalf("应当报压力, got %v", err)
	}
	if rep.Deleted != 0 {
		t.Fatal("**活页一个都不能删** —— 删了就是静默丢数据")
	}
	if !s.Has(p.ID) {
		t.Fatal("活页必须还在")
	}
	if rep.LiveBytes < 5000 {
		t.Fatalf("活页字节要如实报: %d", rep.LiveBytes)
	}
}

// 释放之后才可回收
func TestDeletesOnlyAfterRelease(t *testing.T) {
	s := NewPageStore()
	s.SetCapacity(Capacity{HardBytes: 100})
	sp := NewContextSpace(s, "x")
	p := sp.Append(KindDoc, bigPage(5000))

	if s.Refs(p.ID) != 1 {
		t.Fatalf("活着时引用数应为 1, got %d", s.Refs(p.ID))
	}
	sp.Release()
	if s.Refs(p.ID) != 0 {
		t.Fatal("释放后引用数应归零")
	}

	rep, err := s.Reclaim()
	if err != nil {
		t.Fatalf("现在应当能回收干净: %v", err)
	}
	if rep.Deleted != 1 {
		t.Fatalf("应当删掉 1 页, got %d", rep.Deleted)
	}
	if s.Has(p.ID) {
		t.Fatal("已释放的页应当被删掉")
	}
}

// 父被释放但子还活着 → 父的页不能回收.
// 否则子会突然"想不起"它继承来的东西 —— 最难查的一类丢数据.
func TestChildKeepsParentPagesAlive(t *testing.T) {
	s := NewPageStore()
	s.SetCapacity(Capacity{HardBytes: 100})
	parent := NewContextSpace(s, "parent")
	pp := parent.Append(KindDoc, bigPage(3000))
	child := parent.Fork("child")

	parent.Release() // 父先走
	if s.Refs(pp.ID) == 0 {
		t.Fatal("子还活着, 父的页不该失去引用")
	}
	rep, _ := s.Reclaim()
	if rep.Deleted != 0 || !s.Has(pp.ID) {
		t.Fatal("子还活着时父的页不能被删")
	}

	child.Release() // 子也走了
	rep, err := s.Reclaim()
	if err != nil {
		t.Fatalf("现在应当能回收: %v", err)
	}
	if rep.Deleted == 0 || s.Has(pp.ID) {
		t.Fatal("父子都释放后才该真删")
	}
}

// Release 幂等 —— 重复调用不能把别人的引用减没
func TestReleaseIsIdempotent(t *testing.T) {
	s := NewPageStore()
	parent := NewContextSpace(s, "p")
	pp := parent.Append(KindDoc, bigPage(100))
	child := parent.Fork("c")

	parent.Release()
	parent.Release()
	parent.Release()
	if s.Refs(pp.ID) == 0 {
		t.Fatal("重复 Release 把子持有的引用也减没了")
	}
	child.Release()
	if s.Refs(pp.ID) != 0 {
		t.Fatal("全部释放后应当归零")
	}
}

// ── 降级是无损的 ────────────────────────────────────────────

func TestSoftLimitDemotesLosslessly(t *testing.T) {
	s := NewPageStore()
	sp := NewContextSpace(s, "x")
	var ids []PageID
	for i := 0; i < 5; i++ {
		ids = append(ids, sp.Append(KindDoc, map[string]any{"i": float64(i),
			"pad": strings.Repeat("y", 1000)}).ID)
	}
	// 软限只够放 2 页左右
	s.SetCapacity(Capacity{SoftBytes: 2500})
	rep, err := s.Reclaim()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Demoted == 0 {
		t.Fatal("超软限应当降级")
	}
	if rep.Deleted != 0 {
		t.Fatal("软限只降级, 不该删任何东西")
	}
	// 无损: 全部页仍然在, 且能换回来
	for _, id := range ids {
		if !s.Has(id) {
			t.Fatalf("降级丢了页 %s —— 降级必须无损", id)
		}
	}
	if s.Stats().Swapped == 0 {
		t.Fatal("应当有页进了 swap")
	}
	// 换回来内容一致
	for _, id := range ids {
		if tier, _ := s.TierOf(id); tier == TierWarm {
			if _, err := s.Fault(id); err != nil {
				t.Fatalf("换回失败: %v", err)
			}
		}
	}
}

// 降级按 LRU —— 最久没碰的先走
func TestDemoteFollowsLRU(t *testing.T) {
	s := NewPageStore()
	sp := NewContextSpace(s, "x")
	old := sp.Append(KindDoc, distinctPage("old", 1000))
	sp.Append(KindDoc, distinctPage("mid", 1000))
	fresh := sp.Append(KindDoc, distinctPage("fresh", 1000))

	// 碰一下 fresh, 让它成为最近使用
	_, _ = s.Get(fresh.ID)

	s.SetCapacity(Capacity{SoftBytes: 2200}) // 只能留两页
	if _, err := s.Reclaim(); err != nil {
		t.Fatal(err)
	}
	if tier, _ := s.TierOf(old.ID); tier != TierWarm {
		t.Fatal("最久没碰的那页应当先被降级")
	}
	if tier, _ := s.TierOf(fresh.ID); tier != TierHot {
		t.Fatal("刚碰过的页不该被降级")
	}
}

// ── 删除按年龄, 且只碰死页 ──────────────────────────────────

func TestDeleteOldestDeadFirst(t *testing.T) {
	s := NewPageStore()
	dead1 := NewContextSpace(s, "d1")
	p1 := dead1.Append(KindDoc, distinctPage("d1", 2000))
	dead2 := NewContextSpace(s, "d2")
	p2 := dead2.Append(KindDoc, distinctPage("d2", 2000))
	live := NewContextSpace(s, "live")
	p3 := live.Append(KindDoc, distinctPage("live", 2000))

	dead1.Release()
	dead2.Release()

	// 硬限只够留一页多一点 → 应当先删最老的死页
	s.SetCapacity(Capacity{HardBytes: 4500})
	rep, err := s.Reclaim()
	if err != nil {
		t.Fatalf("死页够删, 不该报压力: %v", err)
	}
	if rep.Deleted != 1 {
		t.Fatalf("应当只删一页, got %d", rep.Deleted)
	}
	if s.Has(p1.ID) {
		t.Fatal("最老的死页应当先删")
	}
	if !s.Has(p2.ID) || !s.Has(p3.ID) {
		t.Fatal("够用了就该停手, 不多删")
	}
	if s.Refs(p3.ID) == 0 {
		t.Fatal("活页引用不该被动")
	}
}

// 去重的页要被两个空间同时持有, 一个释放不能把它删了
func TestDedupedPageNeedsBothReleases(t *testing.T) {
	s := NewPageStore()
	a := NewContextSpace(s, "a")
	b := NewContextSpace(s, "b")
	doc := map[string]any{"same": "同一份内容"}
	p := a.Append(KindDoc, doc)
	if b.Append(KindDoc, doc).ID != p.ID {
		t.Fatal("相同内容应当是同一页")
	}
	if s.Refs(p.ID) != 2 {
		t.Fatalf("两个空间持有, 引用数应为 2, got %d", s.Refs(p.ID))
	}

	a.Release()
	s.SetCapacity(Capacity{HardBytes: 1})
	rep, _ := s.Reclaim()
	if rep.Deleted != 0 || !s.Has(p.ID) {
		t.Fatal("还有一个空间持有, 不能删")
	}
	b.Release()
	rep, _ = s.Reclaim()
	if rep.Deleted != 1 {
		t.Fatal("全部释放后才该删")
	}
}

// 不设容量 = 不回收 (显式选择, 不是忘了)
func TestNoCapacityMeansNoReclaim(t *testing.T) {
	s := NewPageStore()
	sp := NewContextSpace(s, "x")
	sp.Append(KindDoc, bigPage(10000))
	sp.Release()
	rep, err := s.Reclaim()
	if err != nil || rep.Deleted != 0 || rep.Demoted != 0 {
		t.Fatal("没设容量就不该动任何东西")
	}
}

// 回收之后, 前缀共享那些性质不能被破坏
func TestReclaimPreservesAssembly(t *testing.T) {
	s := NewPageStore()
	root := NewContextSpace(s, "root")
	for i := 0; i < 3; i++ {
		root.Append(KindDoc, map[string]any{"i": float64(i)})
	}
	child := root.Fork("c")
	child.Append(KindInput, map[string]any{"ask": "继续"})

	before := Assemble(child, AssembleOptions{SystemSegment: "SYS"}).Bytes

	// 触发降级 (无损)
	s.SetCapacity(Capacity{SoftBytes: 1})
	if _, err := s.Reclaim(); err != nil {
		t.Fatal(err)
	}
	// 降级后组装会报缺页, 换回来之后必须跟原来逐字节一致
	r := Assemble(child, AssembleOptions{SystemSegment: "SYS"})
	if len(r.Faults) == 0 {
		t.Fatal("降级后应当报缺页")
	}
	for _, id := range r.Faults {
		if _, err := s.Fault(id); err != nil {
			t.Fatal(err)
		}
	}
	if got := Assemble(child, AssembleOptions{SystemSegment: "SYS"}).Bytes; got != before {
		t.Fatal("换回之后组装结果必须跟从没降级过一样")
	}
}
