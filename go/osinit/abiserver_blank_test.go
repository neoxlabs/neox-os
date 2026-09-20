package osinit

import (
	"errors"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// fakeCtx 只为这两条测试而存在 —— 它记录"审批有没有真的走到人面前".
type fakeCtx struct {
	onDecide func(abi.DecisionRequest)
}

func (f *fakeCtx) PID() abi.ProcessID      { return "p1" }
func (f *fakeCtx) ABIVersion() string      { return abi.Version }
func (f *fakeCtx) Emit(any)                {}
func (f *fakeCtx) EmitLive(any)            {}
func (f *fakeCtx) Can(string, string) bool { return false }
func (f *fakeCtx) Infer(abi.InferParams) (abi.InferResult, error) {
	return abi.InferResult{}, errors.New("这台机器没有配置推理服务")
}

func (f *fakeCtx) InferStream(abi.InferParams, func(engine.StreamDelta)) (abi.InferResult, error) {
	return abi.InferResult{}, errors.New("这台机器没有配置推理服务")
}

func (f *fakeCtx) Spend(abi.BudgetDelta) error     { return nil }
func (f *fakeCtx) Recv() (abi.RecvResult, error)   { return abi.RecvResult{Closed: true}, nil }
func (f *fakeCtx) TryRecv() (abi.RecvResult, bool) { return abi.RecvResult{}, false }
func (f *fakeCtx) Done() <-chan struct{}           { return nil }
func (f *fakeCtx) Decide(r abi.DecisionRequest) (abi.DecisionResolution, error) {
	if f.onDecide != nil {
		f.onDecide(r)
	}
	return abi.DecisionResolution{Choice: "no"}, nil
}

// 空白审批框必须当场拒掉, 不能端到人面前.
//
// 外来进程 (Node) 可能把 decide 的参数摊平, 少一层
// request —— Go 侧解出零值 DecisionRequest, 弹给用户的是一个标题为空、
// 什么都没有的框. 他要么乱点一个, 要么以为系统坏了; 而且批准之后 scope
// 也是空的, 授权同样落不下去, 从头到尾没有一处会说"你刚才批的是空的".
//
// 参数不对是**调用方的错**, 该让调用方看见, 不该转嫁给用户.
func TestBlankDecisionIsRejectedNotShownToHuman(t *testing.T) {
	asked := false
	ctx := &fakeCtx{onDecide: func(abi.DecisionRequest) { asked = true }}
	srv := &AbiServer{}

	var got abi.Response
	srv.dispatch(abi.Request{ID: 1, Method: abi.MDecide,
		Params: []byte(`{"request":{"present":{"kind":"choice"}}}`)},
		ctx, func(r abi.Response) { got = r })

	if asked {
		t.Fatal("把一个空白审批框端给用户了")
	}
	if got.Error == nil || got.Error.Code != abi.ErrBadRequest {
		t.Fatalf("应该报参数错误, got %+v", got)
	}
	// 报错要指向真正的原因 —— 参数形状, 而不是让人去猜
	if !strings.Contains(got.Error.Message, "params.request") {
		t.Fatalf("没告诉调用方参数该怎么放: %s", got.Error.Message)
	}
}

// 有标题的正常审批照旧走到人面前
func TestNormalDecisionStillReachesHuman(t *testing.T) {
	asked := false
	ctx := &fakeCtx{onDecide: func(abi.DecisionRequest) { asked = true }}
	srv := &AbiServer{}
	srv.dispatch(abi.Request{ID: 1, Method: abi.MDecide,
		Params: []byte(`{"request":{"present":{"kind":"choice","title":"允许写 /etc 吗?"}}}`)},
		ctx, func(abi.Response) {})
	if !asked {
		t.Fatal("正常的审批没走到人面前")
	}
}
