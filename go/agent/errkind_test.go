package agent

import (
	"strings"
	"testing"
)

// **这条钉住的是一个真 bug.**
//
// 原来判"是不是同一类错"靠 4 个中文子串
// (文件不存在/权限被拒/目录不存在/没有这个工具) 加一个整句相等兜底.
// 而 edit_file 报的是 string_not_found 和 ambiguous_match ——
// **一个都不在表里**; 整句又因为带了路径和出现次数而每次不同.
//
// 结果: edit 反复匹配失败这条**最主要的卡死路径, 一直没有任何保护**.
func TestEditFailuresNowCount(t *testing.T) {
	tr := newStallTracker()
	// 每次换一个 old_string —— 指纹不同, 所以重复检测抓不到它;
	// 只有错误分类才拦得住
	verdicts := make([]stallVerdict, 0, 3)
	for i := 0; i < 3; i++ {
		obs := Observation{Tool: "edit_file", Err: "string_not_found: a.go 里找不到那段原文（第 " +
			string(rune('1'+i)) + " 次尝试）"}
		v, _ := tr.observe("edit_file", map[string]any{
			"path": "a.go", "old_string": strings.Repeat("x", i+1)}, obs, true, false)
		verdicts = append(verdicts, v)
	}
	if verdicts[1] == stallNone {
		t.Fatal("edit 连着匹配失败 2 次没有任何干预 —— 这正是那个 bug")
	}
	if verdicts[2] != stallStop {
		t.Fatalf("第 3 次该停, got %v", verdicts[2])
	}
}

// 每一级给的必须是**不同的、能直接照做的**动作.
//
// 重复上一级等于告诉模型"你没听懂" —— 而它确实没辙.
// "换个思路"这种话不可执行, "改用 write_file 整个重写"可以.
func TestAdviceEscalatesWithDifferentActions(t *testing.T) {
	for _, k := range []errKind{errNoMatch, errAmbiguous, errMissing, errCommand, errBadArgs} {
		a2, a3 := advise(k, 2), advise(k, 3)
		if a2 == "" || a3 == "" {
			t.Errorf("%s 缺少第 2/3 级的具体建议", k)
			continue
		}
		if a2 == a3 {
			t.Errorf("%s 两级说的是同一句话 —— 等于告诉模型'你没听懂'", k)
		}
	}
	// 越权是例外: 换写法永远救不了, 多说一次就是多打扰用户一次
	if advise(errDenied, 3) != advise(errDenied, 2) {
		t.Error("越权不该给递进建议 —— 换写法救不了")
	}
}

// 认不出来的错不许给建议 —— 针对不了的建议比不给更糟
func TestUnknownErrorsGetNoFakeAdvice(t *testing.T) {
	if advise(errOther, 2) != "" || advise(errOther, 3) != "" {
		t.Fatal("给认不出来的错编了一条建议")
	}
	if classify("某个我们没见过的怪错误") != errOther {
		t.Fatal("认不出来的错该归 other, 不该硬塞进某一类")
	}
}

// 分类看的是**哨兵串**不是整句 —— 整句带可变部分(次数/路径/退出码),
// 按整句比会让同一类错误每次都算新错误, 计数永远起不来
func TestClassifyIgnoresVariableParts(t *testing.T) {
	a := classify("ambiguous_match: 那段原文在 a.go 里出现了 3 次，不唯一。")
	b := classify("ambiguous_match: 那段原文在 b.py 里出现了 17 次，不唯一。")
	if a != b || a != errAmbiguous {
		t.Fatalf("同一类错被判成不同类: %s vs %s", a, b)
	}
}

// run 会改动世界, 必须声明成 Mutates.
//
// 原来停滞检测按名字前缀猜 (write/edit/delete), run 一个都不匹配 →
// 被当成只读工具, 后果有两个: 空转计数在它用 run 干活时一路涨;
// worldFor 拿不到 run 造成的变化, "跑完再读同一个文件"被判成原地转.
func TestRunIsDeclaredAsMutating(t *testing.T) {
	if !runTool().Mutates {
		t.Fatal("run 没声明 Mutates —— 停滞检测会把它当只读, 正当的'跑完再读'会被判死")
	}
	for _, tl := range DefaultTools() {
		if tl.Name == "read_file" || tl.Name == "search" || tl.Name == "find_files" ||
			tl.Name == "list_dir" {
			if tl.Mutates {
				t.Errorf("%s 不该声明成会改动世界", tl.Name)
			}
		}
	}
}

// **改完代码再跑一次同一条测试, 不许被判成原地转.**
//
// 真机抓到的误报: agent 跑 `python3 run_tests.py` 看到红的 → 改代码 →
// 再跑同一条命令确认绿了, 第二次被警告"你已经第 2 次用同样的参数调 run"。
// 那正是它该做的事.
//
// Mutates 一个字段表达不了真实结构:
//
//	          改变世界   结果依赖世界
//	write/edit   是         否
//	read/search  否         是
//	run          是         是         ← 唯一两者都占的
//
// 把 run 声明成 Mutates 之后它按写工具的规矩算指纹(不含世界状态),
// 于是"世界变了"这件事进不了指纹, 两次调用看起来一模一样.
func TestRerunAfterFixIsNotAStall(t *testing.T) {
	tr := newStallTracker()
	cmd := map[string]any{"cmd": "python3 run_tests.py"}

	// ① 跑测试, 红的
	tr.observe("run", cmd, Observation{Tool: "run", Result: "[退出码 1] FAIL"}, true, true)
	// ② 改代码
	tr.observe("edit_file", map[string]any{"path": "stats.py", "old_string": "a"},
		obsOK(), true, false)
	// ③ 再跑同一条测试 —— 世界已经变了, 这是正当的验证
	if v, msg := tr.observe("run", cmd,
		Observation{Tool: "run", Result: "[退出码 0] PASS"}, true, true); v != stallNone {
		t.Fatalf("改完代码再跑同一条测试被判成原地转了: %v %s", v, msg)
	}
}

// 反过来: 什么都没改就连着跑同一条命令, 该拦
func TestRerunWithoutChangeStillStalls(t *testing.T) {
	tr := newStallTracker()
	cmd := map[string]any{"cmd": "go test ./..."}
	var last stallVerdict
	for i := 0; i < 3; i++ {
		last, _ = tr.observe("run", cmd,
			Observation{Tool: "run", Result: "[退出码 1] 同样的失败"}, true, true)
	}
	if last != stallStop {
		t.Fatalf("什么都没改却连跑三次同样的命令, 该停: %v", last)
	}
}

// 不同的命令不许撞指纹.
//
// 把 run 声明成 Mutates 之后, 它的指纹一度是"工具+path+改动内容",
// 而 run 既没有 path 也没有 content —— **所有 run 调用指纹全都一样**,
// 两条完全不同的命令会被判成重复.
func TestDifferentCommandsDoNotCollide(t *testing.T) {
	tr := newStallTracker()
	a := tr.fingerprint("run", map[string]any{"cmd": "go build ./..."}, true)
	b := tr.fingerprint("run", map[string]any{"cmd": "git status"}, true)
	if a == b {
		t.Fatalf("两条不同的命令指纹相同: %s", a)
	}
}
