package engine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const system = "NEOX-OS/1"

func rootWith(store *PageStore, n int) *ContextSpace {
	root := NewContextSpace(store, "root")
	for i := 0; i < n; i++ {
		root.Append(KindDoc, map[string]any{
			"file": "src/mod.ts", "i": float64(i),
			"summary": strings.Repeat("模块理解", 20),
		})
	}
	return root
}

// ── 性质 1: 父的字节永远是子的前缀 ──────────────────────────

func TestParentBytesArePrefixOfChild(t *testing.T) {
	store := NewPageStore()
	parent := rootWith(store, 5)
	before := Assemble(parent, AssembleOptions{SystemSegment: system}).Bytes

	child := parent.Fork("child")
	child.Append(KindInput, map[string]any{"ask": "把 mod3 重构了"})
	child.Append(KindToolResult, map[string]any{"ok": true})

	after := Assemble(parent, AssembleOptions{SystemSegment: system}).Bytes
	childBytes := Assemble(child, AssembleOptions{SystemSegment: system}).Bytes

	if after != before {
		t.Fatal("子追加不该动到父的任何字节")
	}
	// 提供方的前缀缓存必然命中 —— 这是结构保证, 不是启发式
	if !strings.HasPrefix(childBytes, before) {
		t.Fatal("父必须是子的严格字节前缀")
	}
	if len(childBytes) <= len(before) {
		t.Fatal("子应当更长")
	}
}

func TestSiblingsShareSamePrefix(t *testing.T) {
	store := NewPageStore()
	parent := rootWith(store, 4)
	base := Assemble(parent, AssembleOptions{SystemSegment: system}).Bytes

	var kids []*ContextSpace
	for _, n := range []string{"a", "b", "c"} {
		k := parent.Fork(n)
		k.Append(KindInput, map[string]any{"branch": n})
		kids = append(kids, k)
	}
	for _, k := range kids {
		if !strings.HasPrefix(Assemble(k, AssembleOptions{SystemSegment: system}).Bytes, base) {
			t.Fatalf("[%s] 前缀不匹配", k.Label)
		}
		if k.SharedPrefixWith(parent) != 4 {
			t.Fatalf("[%s] 共享前缀长度不对", k.Label)
		}
	}
	if PlanBreakpoint(parent, kids) != 4 {
		t.Fatal("断点应落在共享前缀末尾")
	}
}

func TestAssembleIsDeterministic(t *testing.T) {
	store := NewPageStore()
	s := NewContextSpace(store, "s")
	s.Append(KindNote, map[string]any{"b": float64(2), "a": float64(1),
		"c": map[string]any{"z": float64(1), "y": float64(2)}})
	once := Assemble(s, AssembleOptions{}).Bytes
	if once != Assemble(s, AssembleOptions{}).Bytes {
		t.Fatal("同样输入两次必须得到同样字节")
	}
	// 键序不同但内容相同 → 必须是同一页
	t2 := NewContextSpace(store, "t")
	t2.Append(KindNote, map[string]any{"c": map[string]any{"y": float64(2), "z": float64(1)},
		"a": float64(1), "b": float64(2)})
	if Assemble(t2, AssembleOptions{}).Bytes != once {
		t.Fatal("键序不同不该产生不同字节")
	}
}

// ── 性质 2: Fork O(1), 内容零复制 ──────────────────────────

func TestForkAllocatesNoPages(t *testing.T) {
	store := NewPageStore()
	parent := rootWith(store, 10)
	before := store.Stats().Resident

	var kids []*ContextSpace
	for i := 0; i < 50; i++ {
		kids = append(kids, parent.Fork("k"))
	}
	if store.Stats().Resident != before {
		t.Fatal("50 个子空间不该产生任何新页")
	}
	for _, k := range kids {
		if k.Length() != 10 || k.OwnLength() != 0 {
			t.Fatal("子应当继承全部且自己不持有")
		}
	}
}

func TestSharingSaves(t *testing.T) {
	store := NewPageStore()
	parent := rootWith(store, 20)
	var kids []*ContextSpace
	for i := 0; i < 50; i++ {
		k := parent.Fork("k")
		k.Append(KindInput, map[string]any{"task": float64(i)})
		kids = append(kids, k)
	}
	m := MeasureSharing(kids)
	if m.SavedRatio < 0.9 {
		t.Fatalf("节省应在 90%% 以上, got %.1f%%", m.SavedRatio*100)
	}
}

func TestContentDedup(t *testing.T) {
	store := NewPageStore()
	a := NewContextSpace(store, "a")
	b := NewContextSpace(store, "b")
	doc := map[string]any{"file": "README.md", "text": "同一份文档"}
	pa := a.Append(KindDoc, doc)
	pb := b.Append(KindDoc, doc)
	if pa.ID != pb.ID || store.Stats().Resident != 1 || store.Appends(pa.ID) != 2 {
		t.Fatal("相同内容必须只存一份")
	}
}

// ── 性质 3: 换出无损 ────────────────────────────────────────

func TestEvictIsLossless(t *testing.T) {
	store := NewPageStore()
	s := NewContextSpace(store, "s")
	p := s.Append(KindToolResult, map[string]any{"big": strings.Repeat("x", 5000)})
	n := s.Length()

	if !store.Evict(p.ID) {
		t.Fatal("换出失败")
	}
	// 关键: 地址空间没变短 —— 页还在, 只是不在物理内存里
	if s.Length() != n || !store.Has(p.ID) {
		t.Fatal("换出不该改变地址空间")
	}
	if _, err := store.Get(p.ID); err == nil {
		t.Fatal("换出后直接取应当缺页")
	}
	back, err := store.Fault(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Content.(map[string]any)["big"] != strings.Repeat("x", 5000) {
		t.Fatal("换入后内容必须完全一致")
	}
}

func TestAssembleReportsFaultsNotSilently(t *testing.T) {
	store := NewPageStore()
	s := NewContextSpace(store, "s")
	s.Append(KindNote, map[string]any{"a": float64(1)})
	p := s.Append(KindNote, map[string]any{"b": float64(2)})
	store.Evict(p.ID)

	r := Assemble(s, AssembleOptions{})
	// 悄悄跳过就是"压缩掉了但没人知道" —— 必须显式暴露
	if len(r.Faults) != 1 || r.Faults[0] != p.ID {
		t.Fatalf("换出的页必须报出来: %+v", r.Faults)
	}
}

func TestEvictedPagesStillCounted(t *testing.T) {
	store := NewPageStore()
	root := NewContextSpace(store, "r")
	p := root.Append(KindDoc, map[string]any{"big": strings.Repeat("y", 4000)})
	kids := []*ContextSpace{root.Fork("a"), root.Fork("b")}

	before := MeasureSharing(kids)
	store.Evict(p.ID)
	after := MeasureSharing(kids)
	// 换出只改变它在不在内存, 不改变逻辑大小 —— 否则"省了多少"会虚高
	if before.NaiveBytes != after.NaiveBytes || before.SharedBytes != after.SharedBytes {
		t.Fatal("换出不该改变传输量统计")
	}
}

// 有系统段时缓存断点不能错一位
func TestBreakpointNotOffByOne(t *testing.T) {
	store := NewPageStore()
	s := NewContextSpace(store, "s")
	for i := 1; i <= 3; i++ {
		s.Append(KindNote, map[string]any{"n": float64(i)})
	}
	withSys := Assemble(s, AssembleOptions{SystemSegment: system, BreakAt: []int{2}})
	noSys := Assemble(s, AssembleOptions{BreakAt: []int{2}})
	if withSys.Breakpoints[0]-noSys.Breakpoints[0] != len(system) {
		t.Fatal("两种情况应只差一个系统段长度")
	}
	head := withSys.Bytes[:withSys.Breakpoints[0]]
	if !strings.HasPrefix(head, system) {
		t.Fatal("断点前必须以系统段开头")
	}
	want := StableStringify(map[string]any{"k": "note", "c": map[string]any{"n": float64(2)}})
	if !strings.HasSuffix(head, want) {
		t.Fatalf("第 2 页必须正好收在断点上\n结尾: %q\n期望: %q", head, want)
	}
}

// ── 跨语言对拍: 页号与组装字节必须跟 TS 完全一致 ─────────────

type tsEngine struct {
	System        string   `json:"system"`
	Contents      []any    `json:"contents"`
	PageIDs       []string `json:"pageIds"`
	Stable        []string `json:"stable"`
	RootBytes     string   `json:"rootBytes"`
	RootNoSys     string   `json:"rootNoSys"`
	BreakpointAt2 []int    `json:"breakpointAt2"`
	Offsets       []int    `json:"offsets"`
	Kids          []struct {
		Label        string `json:"label"`
		Bytes        string `json:"bytes"`
		SharedPrefix int    `json:"sharedPrefix"`
	} `json:"kids"`
}

func loadTS(t *testing.T) tsEngine {
	t.Helper()
	raw, err := os.ReadFile("testdata/ts_engine.json")
	if err != nil {
		t.Fatalf("读样本失败: %v", err)
	}
	var f tsEngine
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// 确定性序列化逐字节一致 —— 这是页号一致的前提
func TestParityStableStringify(t *testing.T) {
	f := loadTS(t)
	for i, c := range f.Contents {
		if got := StableStringify(c); got != f.Stable[i] {
			t.Fatalf("第 %d 项序列化不一致\nGo: %s\nTS: %s", i, got, f.Stable[i])
		}
	}
}

// 页号一致 —— 不一致就说明内容寻址算出的哈希不同, 共享无从谈起
func TestParityPageIDs(t *testing.T) {
	f := loadTS(t)
	store := NewPageStore()
	root := NewContextSpace(store, "root")
	for _, c := range f.Contents {
		root.Append(KindDoc, c)
	}
	got := root.PageIDs()
	if len(got) != len(f.PageIDs) {
		t.Fatalf("页数不一致: %d vs %d", len(got), len(f.PageIDs))
	}
	for i := range got {
		if got[i] != f.PageIDs[i] {
			t.Fatalf("第 %d 页 id 不一致\nGo: %s\nTS: %s", i, got[i], f.PageIDs[i])
		}
	}
}

// 组装出的字节逐字节一致 —— 这是"两边共享同一份缓存前缀"的硬前提
func TestParityAssembledBytes(t *testing.T) {
	f := loadTS(t)
	store := NewPageStore()
	root := NewContextSpace(store, "root")
	for _, c := range f.Contents {
		root.Append(KindDoc, c)
	}

	if got := Assemble(root, AssembleOptions{SystemSegment: f.System}).Bytes; got != f.RootBytes {
		t.Fatalf("带系统段的组装不一致\nGo: %s\nTS: %s", got, f.RootBytes)
	}
	if got := Assemble(root, AssembleOptions{}).Bytes; got != f.RootNoSys {
		t.Fatal("不带系统段的组装不一致")
	}

	r := Assemble(root, AssembleOptions{SystemSegment: f.System, BreakAt: []int{2}})
	if len(r.Breakpoints) != len(f.BreakpointAt2) || r.Breakpoints[0] != f.BreakpointAt2[0] {
		t.Fatalf("断点不一致: %v vs %v", r.Breakpoints, f.BreakpointAt2)
	}
	full := Assemble(root, AssembleOptions{SystemSegment: f.System})
	for i := range f.Offsets {
		if full.Offsets[i] != f.Offsets[i] {
			t.Fatalf("第 %d 个页边界不一致: %d vs %d", i, full.Offsets[i], f.Offsets[i])
		}
	}

	// 子空间也要一致
	for _, k := range f.Kids {
		kid := root.Fork(k.Label)
		kid.Append(KindInput, map[string]any{"branch": k.Label, "说明": "分支 " + k.Label})
		if got := Assemble(kid, AssembleOptions{SystemSegment: f.System}).Bytes; got != k.Bytes {
			t.Fatalf("[%s] 子空间组装不一致\nGo: %s\nTS: %s", k.Label, got, k.Bytes)
		}
		if kid.SharedPrefixWith(root) != k.SharedPrefix {
			t.Fatalf("[%s] 共享前缀长度不一致", k.Label)
		}
	}
}
