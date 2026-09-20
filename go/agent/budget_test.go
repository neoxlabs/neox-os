package agent

import (
	"testing"

	"github.com/neox-os/neox-os/engine"
)

// 不知道窗口时用保守缺省 —— 编一个大数字比不知道更危险
func TestUnknownWindowFallsBackConservatively(t *testing.T) {
	b := NewBudgeter(0)
	if got := b.Bytes(); got <= 0 || got > 200*1024 {
		t.Fatalf("缺省预算不合理: %d", got)
	}
}

// 窗口大就该给得多 —— 写死一个数一定有一头是错的
func TestBiggerWindowBiggerBudget(t *testing.T) {
	small := NewBudgeter(32000).Bytes()
	big := NewBudgeter(128000).Bytes()
	if big <= small {
		t.Fatalf("128K 窗口的预算 (%d) 没比 32K (%d) 大", big, small)
	}
	if big < small*3 {
		t.Fatalf("预算没跟着窗口线性走: %d vs %d", big, small)
	}
}

// 换算系数随内容类型变化. 中文一个 token 一点几字节, 代码三四字节 ——
// 固定一个系数等于在两种内容上各错一半.
func TestRatioLearnedFromRealUsage(t *testing.T) {
	b := NewBudgeter(100000)
	start := b.Bytes()
	// 全是代码: 4 字节一个 token
	for i := 0; i < 20; i++ {
		b.Observe(40000, 10000)
	}
	grown := b.Bytes()
	if grown <= start {
		t.Fatalf("观测到高比率之后预算没跟着涨: %d → %d", start, grown)
	}

	// 换成中文: 1.5 字节一个 token, 预算该缩回去
	c := NewBudgeter(100000)
	for i := 0; i < 20; i++ {
		c.Observe(15000, 10000)
	}
	if c.Bytes() >= grown {
		t.Fatalf("中文内容的预算不该跟代码一样大: %d vs %d", c.Bytes(), grown)
	}
}

// 单轮异常值不许把预算打飞.
//
// 预算来回抖动会让折叠时而触发时而不触发, 前缀缓存跟着反复作废 ——
// 那比预算定得不准更糟.
func TestOneOutlierDoesNotSwingBudget(t *testing.T) {
	b := NewBudgeter(100000)
	for i := 0; i < 10; i++ {
		b.Observe(18000, 10000) // 稳定在 1.8
	}
	before := b.Bytes()
	b.Observe(400000, 10000) // 一次极端的纯代码大文件
	after := b.Bytes()
	if after > before*2 {
		t.Fatalf("一个异常值把预算打飞了: %d → %d", before, after)
	}
}

// 脏数据不许污染 —— 0 token 的回报是供应商的毛病, 不是内容特征
func TestGarbageObservationsIgnored(t *testing.T) {
	b := NewBudgeter(100000)
	before := b.Bytes()
	b.Observe(0, 100)
	b.Observe(100, 0)
	b.Observe(-5, -5)
	if b.Bytes() != before {
		t.Fatal("脏数据改变了预算")
	}
}

// 预算必须留余量给系统段和输出 —— 顶满窗口会让输出被截断,
// 那是整轮任务失败, 比历史短一点严重得多
func TestBudgetLeavesRoomForOutput(t *testing.T) {
	const window = 100000
	b := NewBudgeter(window)
	for i := 0; i < 10; i++ {
		b.Observe(20000, 10000) // 2 字节/token
	}
	// 预算换算回 token 不能占满窗口
	tokens := float64(b.Bytes()) / 2.0
	if tokens > window*0.8 {
		t.Fatalf("预算占了窗口的 %.0f%%, 输出会被截断", tokens/window*100)
	}
}

// 没人显式指定时, 折叠按 Budgeter 算出来的预算走.
//
// (这条原来同时传了显式值和 Budgeter 却期望 Budgeter 赢 ——
// 那正是把优先级写反了的那一版, 现在按"人说的优先"改过来.)
func TestWindowUsesBudgeter(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "t", 0) // 没人指定
	w.UseBudgeter(NewBudgeter(1000))              // 自动算出来很小
	for i := 0; i < 10; i++ {
		w.AppendStep("read_file", map[string]any{"path": "a"}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	_, trim := w.Messages()
	if trim.Dropped == 0 {
		t.Fatal("装了 Budgeter 却还在按写死的那个大数走")
	}
}

// 没装 Budgeter 就用显式传进来的固定值 —— 测试和排查要能钉死一个数
func TestExplicitBudgetStillWins(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "t", 1<<20)
	for i := 0; i < 10; i++ {
		w.AppendStep("read_file", map[string]any{"path": "a"}, "")
		w.AppendResult(bigResult(2000), "", "")
	}
	if _, trim := w.Messages(); trim.Dropped != 0 {
		t.Fatal("固定预算够大却折叠了")
	}
}

// 人显式指定的预算优先于系统自动推算的.
//
// 顺序反了会出静默故障: 轮次 8 加了 Budgeter 之后, 它一装上就盖过
// 显式值, 于是 NEOX_CTX_BYTES 设了没反应、也不报错 ——
// 排查时完全看不出来, 我是在验回收时才发现它早就失效了.
func TestExplicitBudgetBeatsAutoBudgeter(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "t", 2048) // 人说 2048
	w.UseBudgeter(NewBudgeter(128000))               // 自动算出来是几十万
	if got := w.budget(); got != 2048 {
		t.Fatalf("显式预算被自动推算盖掉了: %d", got)
	}
}

// 没人指定时才用自动推算的
func TestAutoBudgeterUsedWhenNothingSpecified(t *testing.T) {
	w := NewWindow(engine.NewPageStore(), "t", 0)
	b := NewBudgeter(128000)
	w.UseBudgeter(b)
	if got := w.budget(); got != b.Bytes() {
		t.Fatalf("没人指定却没用自动预算: %d vs %d", got, b.Bytes())
	}
}

// 页表容量要跟**实际生效的**预算走, 不是跟自动推算的走.
//
// 第一版让调用方传 budgeter.Bytes()*8 进来, 而实际生效的是显式的 3000,
// 于是页表守着 1.8MB 容量, 回收永远不触发.
// 同一个数在两个地方各算各的, 必然对不上.
func TestStoreCapacityFollowsEffectiveBudget(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "t", 3000)   // 人说 3000
	w.UseBudgeter(NewBudgeter(128000)) // 自动算出来二十几万
	w.Reclaim()                        // 同步容量

	if got := store.Capacity().HardBytes; got != 3000*8 {
		t.Fatalf("页表容量 %d, 没跟着实际生效的预算(3000)走", got)
	}
}

// 预算变了容量要跟着变 —— 比率会收敛, 但每轮都可能不同
func TestCapacityTracksBudgetChanges(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "t", 0)
	b := NewBudgeter(100000)
	w.UseBudgeter(b)
	w.Reclaim()
	first := store.Capacity().HardBytes

	for i := 0; i < 20; i++ {
		b.Observe(40000, 10000) // 比率往上走
	}
	w.Reclaim()
	if store.Capacity().HardBytes <= first {
		t.Fatal("预算涨了容量没跟上")
	}
}
