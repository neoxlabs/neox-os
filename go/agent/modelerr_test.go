package agent

import (
	"errors"
	"strings"
	"testing"
)

// 线上原话 —— 模型名下线, 回 400. 这一类重来一百次也是同一个 400
const goneModel = `供应商返回 400: {"error":{"message":"The model name ` + "`deepseek-v4.1-flash-expires-on-0910`" +
	` is no longer supported since 2026-09-10. You can change the model name to ` + "`deepseek-flash`" +
	` to access DeepSeek V4.1 Flash model.","type":"invalid_request_error"}}`

func TestPermanentModelErrors(t *testing.T) {
	for msg, want := range map[string]bool{
		goneModel: true,
		`供应商返回 401: {"error":"invalid api key"}`: true,
		`供应商返回 404: not found`:                   true,
		// 上下文太长 —— 喂回去它可能自己缩, 不算配置错
		`供应商返回 400: {"error":{"message":"maximum context length exceeded"}}`: false,
		`供应商返回 503: overloaded`:                                              false,
		`context deadline exceeded`:                                          false,
	} {
		if got := permanentModelErr(errors.New(msg)); got != want {
			t.Errorf("%v ← %s", got, msg)
		}
	}
}

func TestWhatNowSaysModelNameIsGone(t *testing.T) {
	if h := whatNow(errors.New(goneModel)); !strings.Contains(h, "模型名失效") {
		t.Fatalf("%q", h)
	}
}

// errModel 每次都回同一个错, 顺便数被问了几次.
type errModel struct {
	err   error
	calls int
}

func (m *errModel) Next(string, []Observation) (Step, error) { m.calls++; return Step{}, m.err }
func (m *errModel) Name() string                             { return "err" }

// 模型名下线: **问一次就停**, 不是撞三次 —— 每撞一次都是他在等
func TestPermanentModelErrorStopsAtOnce(t *testing.T) {
	m := &errModel{err: errors.New(goneModel)}
	ag := &Agent{ABI: newFakeSys(), Model: m, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: t.TempDir()}, MaxSteps: 10}
	err := ag.Run("凤凰国际广场在哪")
	if err == nil || !strings.Contains(err.Error(), "模型名失效") {
		t.Fatalf("该停下并说清楚: %v", err)
	}
	if m.calls != 1 {
		t.Fatalf("同一个 400 撞了 %d 次", m.calls)
	}
}

// 网抖还是要给机会 —— 这道闸只拦重来也好不了的
func TestTransientModelErrorStillRetries(t *testing.T) {
	m := &errModel{err: errors.New("供应商返回 400: maximum context length exceeded")}
	ag := &Agent{ABI: newFakeSys(), Model: m, Tools: NewToolSet(DefaultTools()),
		Box: Toolbox{Root: t.TempDir()}, MaxSteps: 10}
	_ = ag.Run("x")
	if m.calls != 3 {
		t.Fatalf("可能自愈的错只试了 %d 次", m.calls)
	}
}
