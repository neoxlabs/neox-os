package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// **撞上步数上限不许把已经做的都扔掉.**
//
// 典型表现是: 统计已经跑完、报告也写进文件了, 而界面显示的
// 只有一句"超过 4 步还没收工" —— 活干完了, 交代没了.
//
// 步数上限是**止损**, 不是判它有罪. 止损的意思是"别再往下花钱了",
// 不是"把已经做的都扔掉". 而且这时候的总结成本极低: 一次推理.
func TestStepLimitStillGetsASummary(t *testing.T) {
	sys := newFakeSys()
	dir := t.TempDir()
	steps := make([]Step, 0, 6)
	// 目录要真的存在 —— 否则先被"连着找不到"的错误递进拦下,
	// 测不到步数上限那条路
	for i := 0; i < 3; i++ {
		name := "d" + string(rune('a'+i))
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, Step{Tool: "list_dir", Args: map[string]any{"path": name}})
	}
	// 第 4 次调用 = 收尾那次(前 3 步用光上限), 这时才给 done
	steps = append(steps, Step{Done: "统计跑完了，报告写在 stats.md，还差图表没做"})

	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: dir}, MaxSteps: 3,
		Model: &Scripted{Steps: steps}}
	if err := ag.Run("统计一下"); err != nil {
		t.Fatalf("撞上限不该返回错误 —— 该给交代: %v", err)
	}
	if !sys.saw("step_limit") {
		t.Fatal("没告诉观察者是撞了上限")
	}
	e := sys.last("done")
	if e == nil {
		t.Fatal("撞上限之后一个字的交代都没有 —— 用户拿不到任何东西")
	}
	if !strings.Contains(str2(e["summary"]), "stats.md") {
		t.Fatalf("交代里没有它真正做过的事: %v", e["summary"])
	}
	if e["hitLimit"] != true {
		t.Fatal("没标出这是撞上限收的尾 —— 用户会以为它自己认为干完了")
	}
}

// 收尾那一次**不许再调工具** —— 上限就是上限
func TestFinalSummaryCannotKeepWorking(t *testing.T) {
	sys := newFakeSys()
	dir0 := t.TempDir()
	os.MkdirAll(filepath.Join(dir0, "a"), 0o755)
	os.MkdirAll(filepath.Join(dir0, "b"), 0o755)
	steps := []Step{
		{Tool: "list_dir", Args: map[string]any{"path": "a"}},
		{Tool: "list_dir", Args: map[string]any{"path": "b"}},
		// 收尾时它还想干活 —— 不理它
		{Tool: "write_file", Args: map[string]any{"path": "x.txt", "content": "y"}},
	}
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: dir0}, MaxSteps: 2, Model: &Scripted{Steps: steps}}
	_ = ag.Run("干活")
	for _, e := range sys.events {
		if e["phase"] == "tool_ok" && e["tool"] == "write_file" {
			t.Fatal("收尾那次还让它写了文件 —— 上限形同虚设")
		}
	}
}

func str2(v any) string { s, _ := v.(string); return s }

// **步数默认无限.**
//
// 原来硬顶 32/40 步 —— 那是错的, 而且比客户端弱:
// 6X 的 DEFAULT_MAX_ITERATIONS = 0, 注释写着"靠 turnStallGuard 按进展兜底".
//
// 步数不是有意义的度量: 既不等于花费(一步可能读一个字节也可能跑一次
// 全量构建), 也不等于进展(原地转十步和扎实干十步数出来一样多).
// 真正该拦的两件事我们都有, 而且都比步数准 —— 止损线管花费(OS 强制),
// 停滞检测管跑飞(按进展).
func TestStepsAreUnlimitedByDefault(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		os.MkdirAll(filepath.Join(dir, "d"+string(rune('A'+i))), 0o755)
	}
	steps := make([]Step, 0, 61)
	for i := 0; i < 60; i++ {
		steps = append(steps, Step{Tool: "list_dir",
			Args: map[string]any{"path": "d" + string(rune('A'+i))}})
	}
	steps = append(steps, Step{Done: "六十步都走完了"})

	sys := newFakeSys()
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box:   Toolbox{Root: dir}, // MaxSteps 不设 = 无限
		Model: &Scripted{Steps: steps}}
	if err := ag.Run("走六十步"); err != nil {
		t.Fatalf("默认不该有步数上限: %v", err)
	}
	if sys.saw("step_limit") {
		t.Fatal("没设上限却被步数拦了")
	}
	if e := sys.last("done"); e == nil || e["hitLimit"] == true {
		t.Fatal("六十步正常收工才对")
	}
}

// 止损线撞上了同样要给交代 —— 那才是真正的边界.
//
// 跟步数上限一个道理: 止损是"别再往下花钱了", 不是"把做过的都作废".
func TestBudgetExhaustionAlsoGetsASummary(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a"), 0o755)
	sys := newFakeSys()
	n := 0
	sys.spendFn = func(abi.BudgetDelta) error {
		n++
		if n >= 3 {
			return errors.New("[budget_exceeded] budget exceeded")
		}
		return nil
	}
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: dir},
		Model: &Scripted{Steps: []Step{
			{Tool: "list_dir", Args: map[string]any{"path": "a"}},
			{Tool: "list_dir", Args: map[string]any{"path": "a"}},
			// 第 3 次 Next 之后 Spend 才拒 —— 这一步不会被执行
			{Tool: "list_dir", Args: map[string]any{"path": "a"}},
			// 收尾那次拿到的是这个
			{Done: "钱花完了，我做了这些"},
		}}}
	if err := ag.Run("干活"); err != nil {
		t.Fatalf("止损线撞上不该直接失败, 该给交代: %v", err)
	}
	e := sys.last("done")
	if e == nil || e["hitLimit"] != true {
		t.Fatal("止损线撞上之后没给交代 —— 用户拿不到任何东西")
	}
	if !strings.Contains(str2(e["why"]), "止损线") {
		t.Fatalf("没说清撞的是哪条边: %v", e["why"])
	}
}

// 停下来的时候, 报的必须是**真实耗尽的那个量**.
//
// 真机日志里打出来的是 `⏹ 到止损线了 (上限 <nil> 步)` —— 用户看到的是
// "步数"用完了, 而步数默认无限, 真正用完的是钱(那段会话计费合计
// ≈ 1,918,537, 正好撞上 2,000,000 的上限). 这两件事的下一步动作完全不同:
// 一个该加预算, 一个该放开步数.
//
// 信息是在**源头**丢的: OS 三条线(token/网络/时长)撞哪条都返回同一个
// 裸哨兵, 于是 agent 只能说"止损线", shell 只能猜成"步数".
func TestStopReasonCarriesTheRealNumbers(t *testing.T) {
	sys := newFakeSys()
	sys.spendErr = fmt.Errorf("止损线: tokens 花了 1918537, 上限 2000000")
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Model: &Scripted{Steps: []Step{
			{Tool: "list_dir", Args: map[string]any{"path": "a"}},
			{Done: "钱花完了，我做了这些"},
		}}}
	if err := ag.Run("干活"); err != nil {
		t.Fatalf("止损线撞上不该直接失败: %v", err)
	}
	e := sys.last("step_limit")
	if e == nil {
		t.Fatal("停下来了却没说为什么")
	}
	// ① 得认得出是哪条边
	if !strings.Contains(str2(e["why"]), "止损线") {
		t.Fatalf("没说清撞的是哪条边: %v", e["why"])
	}
	// ② 得带上实数 —— 只说"到止损线了"用户没法决定下一步
	msg := str2(e["msg"])
	for _, want := range []string{"1918537", "2000000"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("停止消息里没有实数 %s: %q", want, msg)
		}
	}
	// ③ 绝不能把钱的事说成步数的事
	if strings.Contains(msg, "步") {
		t.Fatalf("钱花完了却报成步数: %q", msg)
	}
}

// 步数上限那条路径仍然要说清是步数, 并且带着那个数
func TestStepLimitStillSaysSteps(t *testing.T) {
	sys := newFakeSys()
	dir := t.TempDir()
	// 每步换一个目录 —— 重复调同一个会先撞上停滞检测, 那就测不到步数上限了
	steps := make([]Step, 0, 8)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("d%d", i)
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, Step{Tool: "list_dir", Args: map[string]any{"path": name}})
	}
	steps = append(steps, Step{Done: "到上限了"})
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: dir}, MaxSteps: 3,
		Model: &Scripted{Steps: steps}}
	_ = ag.Run("干活")
	e := sys.last("step_limit")
	if e == nil || !strings.Contains(str2(e["why"]), "步数上限") {
		t.Fatalf("步数上限没说清: %v", e)
	}
	if !strings.Contains(str2(e["msg"]), "3") {
		t.Fatalf("步数上限没带上那个数: %v", e["msg"])
	}
}

// 撞了止损线之后, 再来的话**不许再花钱**.
//
// 交代给一次是对的(做到哪了、验过什么、还差什么), 但那要花一次真实推理.
// 而止损线一旦撞上就一直撞着 —— 于是用户之后每说一句, 都会再买一次
// 同样的交代. 真机: 上限 40000, 第一次停在 79906, 接着说一句话 → 96979,
// 那一句什么活都没干, 净烧 17073. 一道要持续花钱才能维持的止损线,
// 不叫止损线.
func TestAccountingIsGivenOnceNotEveryTurn(t *testing.T) {
	sys := newFakeSys()
	sys.spendErr = fmt.Errorf("tokens 花了 79906, 上限 40000")
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Model: &Scripted{Steps: []Step{
			{Tool: "list_dir", Args: map[string]any{"path": "a"}},
			{Done: "钱花完了，我做了这些"},
		}}}
	if err := ag.Run("干活"); err != nil {
		t.Fatalf("第一轮该给交代: %v", err)
	}
	if !sys.saw("step_limit") {
		t.Fatal("第一轮没给交代")
	}
	// 数**模型调用次数** —— 那才是真实开销的来源.
	// 交代那一次本来就该花一次推理.
	m := ag.Model.(*Scripted)
	firstCalls := m.i
	if firstCalls == 0 {
		t.Fatal("交代那次本来就该花一次推理")
	}

	// 第二轮: 一次推理都不许有
	if err := ag.Run("那你继续"); err != nil {
		t.Fatalf("第二轮不该报错, 该直接说清: %v", err)
	}
	if got := m.i; got != firstCalls {
		t.Fatalf("撞线之后又买了 %d 次推理 —— 止损线在持续花钱", got-firstCalls)
	}
	e := sys.last("budget_done")
	if e == nil {
		t.Fatal("第二轮既没干活也没说话 —— 用户面对的是沉默")
	}
	// 必须说清怎么走出去, 否则用户只知道死了不知道怎么活
	msg := str2(e["msg"])
	for _, want := range []string{"预算", "79906"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("没说清现状/出路: %q", msg)
		}
	}
}

// 预算被调高之后要能接着干 —— 本地标记不能当真相源.
//
// 拿本地 bool 当真相会让"我加了预算怎么还是走不了"变成一个查不出的死结.
func TestRaisingTheBudgetUnsticksIt(t *testing.T) {
	sys := newFakeSys()
	sys.spendErr = fmt.Errorf("tokens 花了 79906, 上限 40000")
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()),
		Model: &Scripted{Steps: []Step{{Done: "第一轮"}, {Done: "第二轮真干了"}, {Done: "备用"}}}}
	_ = ag.Run("干活")
	sys.spendErr = nil // 用户把预算调高了
	if err := ag.Run("继续"); err != nil {
		t.Fatalf("预算调高了还走不了: %v", err)
	}
	if sys.last("budget_done") != nil {
		t.Fatal("预算已经调高, 还在说预算用完了")
	}
}
