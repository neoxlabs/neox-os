package agent

import (
	"fmt"
	"testing"
)

func obsOK() Observation           { return Observation{Result: "ok"} }
func obsErr(e string) Observation  { return Observation{Err: e} }
func args(p string) map[string]any { return map[string]any{"path": p} }

// 同一个调用: 第 2 次提醒, 第 3 次停
func TestRepeatEscalates(t *testing.T) {
	tr := newStallTracker()
	if v, _ := tr.observe("read_file", args("a.txt"), obsOK(), false, true); v != stallNone {
		t.Fatal("第一次不该干预")
	}
	if v, _ := tr.observe("read_file", args("a.txt"), obsOK(), false, true); v != stallWarn {
		t.Fatalf("第二次该提醒, got %v", v)
	}
	if v, _ := tr.observe("read_file", args("a.txt"), obsOK(), false, true); v != stallStop {
		t.Fatalf("第三次该停, got %v", v)
	}
}

// 同一个提醒只发一次 —— 发两次说明系统自己在刷屏
func TestWarnOnlyOnce(t *testing.T) {
	tr := newStallTracker()
	tr.observe("read_file", args("a"), obsOK(), false, true)
	v1, _ := tr.observe("read_file", args("a"), obsOK(), false, true)
	tr.observe("read_file", args("b"), obsOK(), false, true)
	// 回到 a: 已经提醒过, 这次直接算重复到停的阈值
	v2, _ := tr.observe("read_file", args("a"), obsOK(), false, true)
	if v1 != stallWarn || v2 != stallStop {
		t.Fatalf("升级路径不对: %v → %v", v1, v2)
	}
}

// 内容不同地连着写同一个文件 = 迭代, **不许拦**.
//
// 如果按 "写工具只看 path" 判, 给待办清单加一个按钮时,
// 5 个 edit 全部成功
// (app.js 1653→1723→1798→2000, 每次都真的改了), 却被判成
// "用同样的参数调了 3 次" 硬停 —— **干得越对, 死得越快**.
func TestDifferentWritesToSameFileAreProgress(t *testing.T) {
	tr := newStallTracker()
	for i, body := range []string{"v1", "v2", "v3", "v4"} {
		v, msg := tr.observe("write_file",
			map[string]any{"path": "x.txt", "content": body}, obsOK(), true, false)
		if v != stallNone {
			t.Fatalf("第 %d 次写(内容不同)被当成原地转: %s", i+1, msg)
		}
	}
}

// 一次改动拆成好几个小 edit, 每个动一处 —— 那正是提示词要求的
// "只动需要动的地方", 不能反过来惩罚它
func TestMultipleEditsToSameFileAllowed(t *testing.T) {
	tr := newStallTracker()
	for i, anchor := range []string{"函数一", "函数二", "函数三", "函数四"} {
		v, msg := tr.observe("edit_file", map[string]any{
			"path": "app.js", "old_string": anchor, "new_string": anchor + "改",
		}, obsOK(), true, false)
		if v != stallNone {
			t.Fatalf("第 %d 处 edit(锚点不同)被拦了: %s", i+1, msg)
		}
	}
}

// 一模一样地再写一遍才是真的原地转
func TestIdenticalRewriteStillCaught(t *testing.T) {
	tr := newStallTracker()
	same := map[string]any{"path": "x.txt", "content": "一模一样"}
	tr.observe("write_file", same, obsOK(), true, false)
	if v, _ := tr.observe("write_file", same, obsOK(), true, false); v != stallWarn {
		t.Fatal("原样重写第二次该提醒")
	}
	if v, _ := tr.observe("write_file", same, obsOK(), true, false); v != stallStop {
		t.Fatal("原样重写第三次该停")
	}
}

// 同一个锚点反复 edit 也是原地转 —— 大概率是它没意识到已经改过了
func TestIdenticalEditStillCaught(t *testing.T) {
	tr := newStallTracker()
	same := map[string]any{"path": "app.js", "old_string": "同一处", "new_string": "改"}
	tr.observe("edit_file", same, obsOK(), true, false)
	tr.observe("edit_file", same, obsOK(), true, false)
	if v, _ := tr.observe("edit_file", same, obsOK(), true, false); v != stallStop {
		t.Fatal("同一个锚点改三次该停")
	}
}

// 同一类错误连着出现 → 换角度, 而不是换参数
func TestSameErrorKindEscalates(t *testing.T) {
	tr := newStallTracker()
	tr.observe("read_file", args("a"), obsErr("a: 文件不存在。别重试同一路径"), false, true)
	v, msg := tr.observe("read_file", args("b"), obsErr("b: 文件不存在。别重试同一路径"), false, true)
	if v != stallWarn {
		t.Fatalf("同类错误第二次该提醒, got %v", v)
	}
	if msg == "" {
		t.Fatal("提醒要说清楚该怎么办")
	}
	if v, _ := tr.observe("read_file", args("c"), obsErr("c: 文件不存在"), false, true); v != stallStop {
		t.Fatalf("第三次该停, got %v", v)
	}
}

// 成功一次就把错误连击清零 —— 否则偶发失败会累积成误报
func TestSuccessResetsErrorStreak(t *testing.T) {
	tr := newStallTracker()
	tr.observe("read_file", args("a"), obsErr("文件不存在"), false, true)
	tr.observe("read_file", args("b"), obsOK(), false, true)
	if v, _ := tr.observe("read_file", args("c"), obsErr("文件不存在"), false, true); v != stallNone {
		t.Fatalf("成功之后应当重新计数, got %v", v)
	}
}

// 动过手之后又连着只读 → 提醒该动手
func TestNoWriteWarns(t *testing.T) {
	tr := newStallTracker()
	// 先真动一次手 —— 空转告警只在"这一轮已经开始改东西"之后才有意义
	tr.observe("write_file", map[string]any{"path": "seed.txt", "content": "x"}, obsOK(), true, false)
	var last stallVerdict
	for i := 0; i < noWriteWarn; i++ {
		last, _ = tr.observe("read_file", args(string(rune('a'+i))), obsOK(), false, true)
	}
	if last != stallWarn {
		t.Fatalf("连着 %d 步只读该提醒, got %v", noWriteWarn, last)
	}
}

// **纯问答不许被"该动手了"打扰.**
//
// 用户问"这个项目是干嘛的", agent 读六个文件是完全正当的.
// 原来的实现从第一步就开始数空转, 于是这类任务必然吃一条误报 ——
// 而误报的代价是: 模型会因为这句话去做一次它本来不该做的修改.
func TestPureQuestionNeverGetsNoWriteWarning(t *testing.T) {
	tr := newStallTracker()
	for i := 0; i < noWriteWarn*2; i++ {
		if v, msg := tr.observe("read_file", args(string(rune('a'+i))), obsOK(), false, true); v != stallNone {
			t.Fatalf("纯问答第 %d 步被打扰了: %v %s", i+1, v, msg)
		}
	}
}

// 写一次就把空转计数清零
func TestWriteResetsNoWrite(t *testing.T) {
	tr := newStallTracker()
	for i := 0; i < noWriteWarn-1; i++ {
		tr.observe("read_file", args(string(rune('a'+i))), obsOK(), false, true)
	}
	tr.observe("write_file", args("out.txt"), obsOK(), true, false)
	if v, _ := tr.observe("read_file", args("z"), obsOK(), false, true); v != stallNone {
		t.Fatalf("写过之后不该立刻报空转, got %v", v)
	}
}

// 写完再读**是提示词要求的验证动作**, 不能被当成原地转.
// 检测器不该惩罚我们明确要求它做的事 —— 真机跑 edit_file 时撞到过.
func TestWriteClearsRepeatHistoryForThatPath(t *testing.T) {
	tr := newStallTracker()
	tr.observe("read_file", args("a.txt"), obsOK(), false, true) // 改之前先读
	tr.observe("edit_file", args("a.txt"), obsOK(), true, false) // 改
	if v, _ := tr.observe("read_file", args("a.txt"), obsOK(), false, true); v != stallNone {
		t.Fatalf("写完回读是验证, 不该报警, got %v", v)
	}
}

// 但写别的文件不该把这个路径的历史清掉
func TestWriteOnlyClearsItsOwnPath(t *testing.T) {
	tr := newStallTracker()
	tr.observe("read_file", args("a.txt"), obsOK(), false, true)
	tr.observe("write_file", args("b.txt"), obsOK(), true, false)
	if v, _ := tr.observe("read_file", args("a.txt"), obsOK(), false, true); v != stallWarn {
		t.Fatalf("改的是 b, 重复读 a 仍该报警, got %v", v)
	}
}

// edit_file 也算写 —— 两处判断必须一致
func TestEditCountsAsWrite(t *testing.T) {
	tr := newStallTracker()
	for i := 0; i < noWriteWarn-1; i++ {
		tr.observe("read_file", args(string(rune('a'+i))), obsOK(), false, true)
	}
	tr.observe("edit_file", args("x.txt"), obsOK(), true, false)
	if v, _ := tr.observe("read_file", args("z"), obsOK(), false, true); v != stallNone {
		t.Fatalf("edit 应当也算动过手, got %v", v)
	}
}

// 翻页不是卡住 —— 第 3 页被硬停时, 模型只好退回第 1 页.
// 只读工具的指纹必须含全部参数: 参数不同 = 要的东西不同 = 有进展.
func TestPagingIsNotStall(t *testing.T) {
	tr := newStallTracker()
	for i, off := range []any{1, 401, 801} {
		v, msg := tr.observe("read_file",
			map[string]any{"path": "big.log", "offset": off}, Observation{}, false, true)
		if v != stallNone {
			t.Fatalf("翻第 %d 页被当成卡住了: %s", i+1, msg)
		}
	}
}

// 真的反复读同一段还是要拦住
func TestSameSliceStillCaught(t *testing.T) {
	tr := newStallTracker()
	args := map[string]any{"path": "a.txt", "offset": 1}
	tr.observe("read_file", args, Observation{}, false, true)
	if v, _ := tr.observe("read_file", args, Observation{}, false, true); v != stallWarn {
		t.Fatal("同一段读两次该提醒")
	}
	if v, _ := tr.observe("read_file", args, Observation{}, false, true); v != stallStop {
		t.Fatal("同一段读三次该停")
	}
}

// 跑飞了仍然要有兜底 —— 只是兜底的是 MaxSteps 和预算, 不是停滞检测.
//
// 停滞检测的职责是"发现原地转", 不是"限制总工作量".
// 让它去管工作量, 代价是把正确的迭代也杀掉, 因此不划算.
func TestRunawayWritesAreNotStallDetectorsJob(t *testing.T) {
	tr := newStallTracker()
	for i := 0; i < 20; i++ {
		v, _ := tr.observe("write_file",
			map[string]any{"path": "a.txt", "content": fmt.Sprintf("第%d版", i)},
			Observation{}, true, false)
		if v == stallStop {
			t.Fatal("内容一直在变却被停滞检测拦下 —— 那是 MaxSteps 的活")
		}
	}
}

// 指纹要稳定 —— map 遍历顺序随机, 不排序会让同一次调用算出不同指纹,
// 重复永远检测不到
func TestFingerprintStable(t *testing.T) {
	args := map[string]any{"path": "a", "offset": 1, "z": "x", "b": 2}
	tr := newStallTracker()
	first := tr.fingerprint("read_file", args, false)
	for i := 0; i < 50; i++ {
		if tr.fingerprint("read_file", args, false) != first {
			t.Fatal("指纹不稳定")
		}
	}
}

// 改完之后重新搜索确认有没有遗漏 —— **那是我们要求的验证动作**.
//
// 改完 site/proj/README.md 再 search site/proj 确认,
// 被判成"第 2 次用同样的参数调 search". 检测器又一次
// 精确地惩罚了提示词明确要求它做的那件事(第三次变形了).
func TestSearchAfterWriteInThatDirIsVerificationNotStall(t *testing.T) {
	tr := newStallTracker()
	sea := map[string]any{"pattern": "todos", "path": "site/proj"}
	tr.observe("search", sea, obsOK(), false, true)
	tr.observe("edit_file", map[string]any{
		"path": "site/proj/README.md", "old_string": "a", "new_string": "b"}, obsOK(), true, false)
	if v, msg := tr.observe("search", sea, obsOK(), false, true); v != stallNone {
		t.Fatalf("改完再搜是验证, 不该报警: %s", msg)
	}
}

// 改的是别的目录就不该清掉这次搜索的历史
func TestWriteElsewhereDoesNotClearSearch(t *testing.T) {
	tr := newStallTracker()
	sea := map[string]any{"pattern": "x", "path": "site/proj"}
	tr.observe("search", sea, obsOK(), false, true)
	tr.observe("write_file", map[string]any{
		"path": "other/a.txt", "content": "x"}, obsOK(), true, false)
	if v, _ := tr.observe("search", sea, obsOK(), false, true); v != stallWarn {
		t.Fatal("改的是别处, 重复搜同一目录仍该报警")
	}
}

// 通则: 世界变过之后同样的读不算重复.
//
// 这一条替掉了三个补丁(清读历史/指纹补参数/范围覆盖判断).
// 判据不是"长得一样吗", 而是"还会不会带来新信息".
func TestWorldChangeMakesRepeatedReadFresh(t *testing.T) {
	tr := newStallTracker()
	a := args("a.txt")
	tr.observe("read_file", a, obsOK(), false, true)
	if v, _ := tr.observe("read_file", a, obsOK(), false, true); v != stallWarn {
		t.Fatal("世界没变, 读两次该提醒")
	}
	// 改的就是它读的那个文件 —— 世界变了
	tr.observe("write_file", map[string]any{"path": "a.txt", "content": "x"}, obsOK(), true, false)
	if v, msg := tr.observe("read_file", a, obsOK(), false, true); v != stallNone {
		t.Fatalf("世界变过之后同样的读不该报警: %s", msg)
	}
}

// 失败的写没有改变世界, 不该让读历史作废 ——
// 否则一次失败的写就能把原地转洗白
func TestFailedWriteDoesNotAdvanceWorld(t *testing.T) {
	tr := newStallTracker()
	a := args("a.txt")
	tr.observe("read_file", a, obsOK(), false, true)
	tr.observe("write_file", map[string]any{"path": "a.txt", "content": "y"},
		obsErr("权限被拒"), true, false)
	if v, _ := tr.observe("read_file", a, obsOK(), false, true); v != stallWarn {
		t.Fatal("失败的写不该让世界前进")
	}
}

// 世界没再变的话, 连着读还是要拦住
func TestRepeatAfterWorldChangeStillCaught(t *testing.T) {
	tr := newStallTracker()
	tr.observe("write_file", map[string]any{"path": "a.txt", "content": "y"}, obsOK(), true, false)
	a := args("a.txt")
	tr.observe("read_file", a, obsOK(), false, true)
	tr.observe("read_file", a, obsOK(), false, true)
	if v, _ := tr.observe("read_file", a, obsOK(), false, true); v != stallStop {
		t.Fatal("世界不变时连读三次仍该停")
	}
}

// 写别的文件不该把这次读洗白 —— 全局一个计数就会犯这个错
func TestUnrelatedWriteDoesNotFreshenARead(t *testing.T) {
	tr := newStallTracker()
	a := args("a.txt")
	tr.observe("read_file", a, obsOK(), false, true)
	tr.observe("write_file", map[string]any{"path": "毫不相干.txt", "content": "x"}, obsOK(), true, false)
	if v, _ := tr.observe("read_file", a, obsOK(), false, true); v != stallWarn {
		t.Fatal("写的是别的文件, 重复读 a 仍该报警")
	}
}

// 范围判断
func TestCoversSemantics(t *testing.T) {
	cases := []struct {
		read, written string
		want          bool
	}{
		{"site/proj", "site/proj/README.md", true},
		{"site/proj/", "site/proj/a/b.js", true},
		{".", "任意/路径.txt", true},
		{"", "任意.txt", true},
		{"site/proj", "site/other/x.md", false},
		{"site/projx", "site/proj/x.md", false}, // 前缀像但不是同一个目录
		{"a.txt", "a.txt", true},
	}
	for _, c := range cases {
		if got := covers(c.read, c.written); got != c.want {
			t.Fatalf("covers(%q,%q)=%v 期望 %v", c.read, c.written, got, c.want)
		}
	}
}

// **一个并行批次只算一步**.
//
// "连着出现"说的是: 撞了墙, 没改做法, 又撞一次. 而并行批次里那几个调用是
// **同时发出去的一次尝试** —— 它还来不及从第一个失败里学到任何东西.
//
// 并行读四个不存在的文件时, 一个批次直接把计数顶到 4,
// 这一轮会被停滞检测停掉. 而"一次查一批文件
// 在不在"恰恰是最常见的并行用法.
func TestOneBatchCountsAsOneStep(t *testing.T) {
	tr := newStallTracker()
	tr.nextStep() // 这一批开始
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		v, msg := tr.observeCall("", "read_file", map[string]any{"path": name},
			Observation{Err: name + ": 文件不存在"}, false, true)
		if v == stallStop {
			t.Fatalf("一个批次里读 %s 就被判成停滞了: %s", name, msg)
		}
	}
}

// 批次之间照旧累加 —— 一批全挂、换一批还全挂, 那才是真的在原地转
func TestBatchAfterBatchStillStalls(t *testing.T) {
	tr := newStallTracker()
	stopped := false
	for round := 0; round < 4 && !stopped; round++ {
		tr.nextStep()
		for i, name := range []string{"a", "b"} {
			path := fmt.Sprintf("r%d-%s.md", round, name)
			_ = i
			if v, _ := tr.observeCall("", "read_file", map[string]any{"path": path},
				Observation{Err: path + ": 文件不存在"}, false, true); v == stallStop {
				stopped = true
				break
			}
		}
	}
	if !stopped {
		t.Fatal("一批接一批地全挂, 该停下来了 —— 那才是真的在原地转")
	}
}
