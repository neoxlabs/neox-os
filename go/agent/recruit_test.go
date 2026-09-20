package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func recruitRun(t *testing.T, sys *fakeSys, hired *[]string, args map[string]any) (string, error) {
	t.Helper()
	hire := func(name, role string) (string, error) {
		*hired = append(*hired, name+"/"+role)
		return "「" + name + "」上线了, 岗位是: " + role, nil
	}
	tool := RecruitTool(hire)
	return tool.Run(Toolbox{Root: t.TempDir(), Sys: sys}, args)
}

// 说不清他干什么就**别拉** —— 而且这一条要在弹卡之前拦住.
//
// 拉一个没有岗位说明的 bot 上线只会问"要我干什么", 那等于把活又推回给
// 用户 —— 而这个工具存在的理由恰恰是不推回去.
func TestRecruitRefusesVagueHiresBeforeAskingAnyone(t *testing.T) {
	for _, missing := range []map[string]any{
		{"role": "写前端", "why": "要加速"},                          // 没名字
		{"name": "前端", "why": "要加速"},                           // 没岗位
		{"name": "前端", "role": "写前端"},                          // 没理由
		{"name": "这个名字长得侧栏一行放不下真的很长", "role": "x", "why": "y"}, // 名字太长
	} {
		sys := &fakeSys{}
		asked := 0
		sys.decideFn = func(abi.DecisionRequest) (abi.DecisionResolution, error) {
			asked++
			return abi.DecisionResolution{Choice: "yes"}, nil
		}
		var hired []string
		if _, err := recruitRun(t, sys, &hired, missing); err == nil {
			t.Errorf("%v 这种也放行了", missing)
		}
		if asked != 0 {
			t.Errorf("%v: 参数不全就弹了卡 —— 那是白打扰用户一次", missing)
		}
		if len(hired) != 0 {
			t.Errorf("%v: 居然真起了人", missing)
		}
	}
}

// 每个 bot 都是钱: **必须先问人**, 而且卡上要写清他是谁、为什么要他.
func TestRecruitAsksBeforeHiring(t *testing.T) {
	sys := &fakeSys{}
	var req abi.DecisionRequest
	sys.decideFn = func(r abi.DecisionRequest) (abi.DecisionResolution, error) {
		req = r
		return abi.DecisionResolution{Choice: "yes"}, nil
	}
	var hired []string
	out, err := recruitRun(t, sys, &hired, map[string]any{
		"name": "前端", "role": "负责页面和交互", "why": "接口和页面能并行, 一个人干太慢"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hired) != 1 || hired[0] != "前端/负责页面和交互" {
		t.Fatalf("批了却没起人: %v", hired)
	}
	if !strings.Contains(req.Present.Title, "前端") {
		t.Errorf("卡上没写要拉谁: %q", req.Present.Title)
	}
	if !strings.Contains(req.Present.Detail, "负责页面和交互") || !strings.Contains(req.Present.Detail, "一个人干太慢") {
		t.Errorf("卡上没写他干什么/为什么要他, 用户没法判断: %q", req.Present.Detail)
	}
	if !strings.Contains(out, "前端") {
		t.Errorf("起完了没告诉模型结果: %q", out)
	}
}

// 拒绝后就**别换个名字再申请一次**.
//
// 换个说法再问一遍就是绕过已经作出的决定.
func TestRecruitTakesNoForAnAnswer(t *testing.T) {
	sys := &fakeSys{}
	sys.decideFn = func(abi.DecisionRequest) (abi.DecisionResolution, error) {
		return abi.DecisionResolution{Choice: "no"}, nil
	}
	var hired []string
	out, err := recruitRun(t, sys, &hired, map[string]any{"name": "前端", "role": "写页面", "why": "快点"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hired) != 0 {
		t.Fatal("用户拒了还是把人起了")
	}
	if !strings.Contains(out, "别换个名字再申请") {
		t.Errorf("没挡住「换个说法再问一遍」: %q", out)
	}
}

// 起不了新 bot 的机器上, 工具表里**一个字都不该提** recruit.
func TestRecruitAbsentWhenTheMachineCannotHire(t *testing.T) {
	off := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil, nil, nil, nil))
	if _, ok := off.Get("recruit"); ok {
		t.Fatal("起不了人却挂着 recruit —— 它会照着调, 然后对用户许下做不到的承诺")
	}
	on := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil, nil,
		func(name, role string) (string, error) { return "", nil }, nil))
	if _, ok := on.Get("recruit"); !ok {
		t.Fatal("能起人却没挂 recruit")
	}
}

// 等待人工决定的工具**一律不设时限** —— 时间闸只处理"机器不响应", 不能把
// "人还没回答"当成超时.
func TestRecruitWaitsForHuman(t *testing.T) {
	tool := RecruitTool(func(string, string) (string, error) { return "", nil })
	if !tool.WaitsForHuman {
		t.Fatal("recruit 没标 WaitsForHuman —— 用户还没看到卡, 30 秒的通用时限就把它砍了")
	}
}

// 用户开了"拉人不用问"就直接拉 —— 但结果里要标明这是免批档
func TestRecruit免批档不弹卡(t *testing.T) {
	hired := ""
	tool := RecruitTool(func(name, role string) (string, error) {
		hired = name + "/" + role
		return "「" + name + "」上线了", nil
	})
	// Decide 一被碰就报错 —— 免批路径根本不该走到审批
	sys := &fakeSys{decideFn: func(abi.DecisionRequest) (abi.DecisionResolution, error) {
		t.Fatal("免批档不该弹卡")
		return abi.DecisionResolution{}, nil
	}}
	box := Toolbox{Sys: sys, AutoHire: func() bool { return true }}
	out, err := tool.Run(box, map[string]any{"name": "小试", "role": "打杂", "why": "活多"})
	if err != nil {
		t.Fatal(err)
	}
	if hired != "小试/打杂" {
		t.Fatalf("没拉成: %q", hired)
	}
	if !strings.Contains(out, "免批") {
		t.Fatalf("免批要说在结果里, 拿到: %q", out)
	}
}
