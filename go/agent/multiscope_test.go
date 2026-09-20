package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 一次申请带多个目标 —— **一次操作只该打扰用户一次**.
//
// 装一个 Python 包要两次审批(pypi.org 查包 + files.pythonhosted.org
// 下载), 而这两次问的其实是同一件事: "允许它装 tabulate 吗".
// 分层错了: 用户关心的是这次操作, 我们端到他面前的却是技术细节.
func TestOneRequestCanCoverEveryHostAnOperationNeeds(t *testing.T) {
	sys := newFakeSys()
	var asked abi.DecisionRequest
	sys.canFn = func(abi.CapAxis, string) (bool, error) { return false, nil }
	sys.decideFn = func(r abi.DecisionRequest) (abi.DecisionResolution, error) {
		asked = r
		return abi.DecisionResolution{Choice: "yes", By: "test"}, nil
	}
	out, err := requestAccess(Toolbox{Sys: sys}, map[string]any{
		"axis": "net", "scope": "pypi.org,files.pythonhosted.org", "why": "装 tabulate"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "granted") {
		t.Fatalf("批准了却没说成功: %s", out)
	}
	// **两个主机都要出现在标题里** —— 批量不等于含糊,
	// 用户必须看得见他到底放行了哪几个
	for _, h := range []string{"pypi.org", "files.pythonhosted.org"} {
		if !strings.Contains(asked.Present.Title, h) {
			t.Errorf("审批标题里没列出 %s —— 用户不知道自己批了什么", h)
		}
	}
	if !strings.Contains(asked.Scope, "files.pythonhosted.org") {
		t.Fatal("scope 里丢了第二个主机, 批准之后它还是连不上")
	}
}

// 已经有的目标不重复申请, 但缺的那个还要问 ——
// 只要有一个缺就不能当成"已经有了"
func TestOnlyMissingTargetsAreAsked(t *testing.T) {
	sys := newFakeSys()
	var asked abi.DecisionRequest
	sys.canFn = func(_ abi.CapAxis, scope string) (bool, error) {
		return scope == "pypi.org", nil // 只有第一个有
	}
	sys.decideFn = func(r abi.DecisionRequest) (abi.DecisionResolution, error) {
		asked = r
		return abi.DecisionResolution{Choice: "yes"}, nil
	}
	if _, err := requestAccess(Toolbox{Sys: sys}, map[string]any{
		"axis": "net", "scope": "pypi.org,files.pythonhosted.org", "why": "装包"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(asked.Scope, "pypi.org,") {
		t.Fatalf("把已经有的那个也拿去问了 —— 白打扰: %s", asked.Scope)
	}
	if !strings.Contains(asked.Scope, "files.pythonhosted.org") {
		t.Fatalf("缺的那个没问: %s", asked.Scope)
	}
}

// 全都已经有了就别问
func TestNothingMissingMeansNoPrompt(t *testing.T) {
	sys := newFakeSys()
	sys.canFn = func(abi.CapAxis, string) (bool, error) { return true, nil }
	sys.decideFn = func(abi.DecisionRequest) (abi.DecisionResolution, error) {
		t.Fatal("已经全有了还去问用户")
		return abi.DecisionResolution{}, nil
	}
	out, _ := requestAccess(Toolbox{Sys: sys}, map[string]any{
		"axis": "net", "scope": "a.com, b.com", "why": "x"})
	if !strings.Contains(out, "already_done") {
		t.Fatalf("该说已经有了: %s", out)
	}
}

// **两边的切法必须一模一样.**
//
// 不一致的话用户看到批了 3 个而实际只授了 1 个, 而且没有任何一处
// 会说这件事 —— 那正是最难查的一类。
func TestAgentAndOSSplitIdentically(t *testing.T) {
	for _, raw := range []string{
		"a.com,b.com", "a.com, b.com", "a.com b.com", "a.com;b.com",
		" a.com , , b.com ", "a.com\nb.com", "a.com,a.com,b.com", "",
	} {
		got := splitTargets(raw)
		want := osSplitForTest(raw)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("切法不一致 %q: agent=%v os=%v", raw, got, want)
		}
	}
}

// osSplitForTest 跟 osinit.splitScopes 同一套规则的副本.
// (两个包不互相依赖, 所以只能在这儿把契约写死并比对.)
func osSplitForTest(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';'
	}) {
		if f = strings.TrimSpace(f); f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
