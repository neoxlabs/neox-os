package main

import (
	"context"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

func has(caps []abi.Capability, axis abi.CapAxis) bool {
	for _, c := range caps {
		if c.Axis == axis {
			return true
		}
	}
	return false
}

// 三档给的能力必须真的不同 —— 否则那个开关就是个装饰
func TestAskModeChangesWhatItCanDo(t *testing.T) {
	p := persona{app: "build", name: "构建", tools: true}
	// 能力发在哪块地方由 planner 说了算(见 workplan.go); 这条测试问的是
	// **三档给的东西不同**, 所以给一块固定的地方就行
	plan := Plan{Dir: t.TempDir(), Mode: ModeExclusive}

	strict := capsFor(p, osinit.AskAlways, plan)
	if len(strict) != 0 {
		t.Errorf("严格档还给了能力, 那就不会每次都问了: %+v", strict)
	}

	bounds := capsFor(p, osinit.AskBounds, plan)
	if !has(bounds, abi.AxisWrite) || !has(bounds, abi.AxisProc) {
		t.Errorf("缺省档连自己工作目录都动不了: %+v", bounds)
	}
	if has(bounds, abi.AxisNet) {
		t.Error("缺省档不该白给出网 —— 越界才问, 这就是那条界")
	}

	never := capsFor(p, osinit.AskNever, plan)
	if !has(never, abi.AxisNet) {
		t.Error("不问那一档还得为出网问一次 —— 用户说的是别问我")
	}
}

// 不干活的 bot 不给能力 —— 它根本不动手
func TestTalkOnlyBotGetsNothing(t *testing.T) {
	if caps := capsFor(persona{app: "writer", tools: false}, osinit.AskNever, Plan{Dir: t.TempDir()}); len(caps) != 0 {
		t.Errorf("只说话的 bot 拿到了能力: %+v", caps)
	}
}

// 内核给它的写范围, 必须**正好是**它真正干活的那个目录.
//
// 这是"算对了但接错线"最容易发生的一处: 干活目录加了 worktree 那一层之后,
// capsFor 还照着项目根算的话, bot 会在自己的 worktree 里动手、而能力发在
// 项目根上 —— 每写一个文件都撞内核问一次人, 用户看到的是
// "它一直在要权限", 而不是"接线错了".
//
// 现在两处读的是**同一个** Plan(见 workplan.go), 这条测试守的就是那件事.
func TestWriteCapMatchesWhereItActuallyWorks(t *testing.T) {
	atHome(t)
	planner := newWorkPlanner(func(string) bool { return false })
	for _, p := range []persona{
		{app: "notes", name: "记事本", tools: true, work: t.TempDir()},
		{app: "notes2", name: "记事本二", tools: true}, // 不指定 = 匿名目录
	} {
		plan, err := planner.plan(p)
		if err != nil {
			t.Fatal(err)
		}
		root := plan.Dir
		caps := capsFor(p, osinit.AskBounds, plan)
		found := ""
		for _, c := range caps {
			if c.Axis == abi.AxisWrite {
				found = c.Scope
			}
		}
		if found != root {
			t.Fatalf("写能力发在 %q, 它实际在 %q 干活 —— 每写一个文件都要问一次人",
				found, root)
		}
	}
}

// **拉人永远问, 哪怕档位是"都不问"**.
//
// "都不问"说的是"别为它够不够权限干活来烦我" —— 那类决策代价有界.
// 拉人不一样: 它是唯一一个会让花费翻倍的动作, 而且拉进来的人还会接着拉人.
// 一档"都不问"就能让一句话变成一屋子 bot, 等用户发现时账单已经出去了.
func TestHiringIsNeverAutoApproved(t *testing.T) {
	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试结束")
	pid, err := o.Spawn(abi.ProcessSpec{App: "x", Name: "x"},
		osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
			<-ctx.Done()
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	// 拉人那条: proc + bot:<名字>
	ch := o.Decisions().Request(pid, abi.DecisionRequest{
		Axis: abi.AxisProc, Scope: "bot:前端",
		Present: abi.PresentSpec{Kind: "choice", Title: "拉「前端」进来一起干吗?"}})
	var did string
	for _, p := range o.Decisions().Pending() {
		did = string(p.DID)
	}
	if !isRecruit(o, did) {
		t.Fatal("拉人那条没被认出来 —— 它会被自动批掉")
	}
	// 普通的够不够权限那条: 代价有界, 该自动批
	o.Decisions().Request(pid, abi.DecisionRequest{
		Axis: abi.AxisNet, Scope: "pypi.org",
		Present: abi.PresentSpec{Kind: "choice", Title: "允许连 pypi.org 吗?"}})
	for _, p := range o.Decisions().Pending() {
		if p.Request.Axis == abi.AxisNet && isRecruit(o, string(p.DID)) {
			t.Fatal("出网那条被当成拉人了 —— 那用户会被为难两次")
		}
	}
	_ = ch
}
