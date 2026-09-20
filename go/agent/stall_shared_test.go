package agent

import (
	"strings"
	"testing"
)

/**
 * **系统按自己的协议要求它再调一次, 然后因为它照做而停掉它.**
 *
 *	merge_up 的正常流程本来就是两步: 第一次说"这次合并从主干带进了
 *	别人改过的文件, 先在这儿验一遍", 验完再调一次. 而停滞检测数的是
 *	"同样的参数调了几次" —— merge_up 没有参数, 于是第二次必然撞上.
 *
 *	五个 bot 并行处理时, 这种判定会把其中两个停掉:
 *	  「这一轮我停下来了 —— 这是你第 2 次用同样的参数调 merge_up」
 */
func Test协议要求的第二次调用不算原地转(t *testing.T) {
	tr := newStallTracker()
	ok := Observation{Result: "先在这儿验一遍"}
	for i := 0; i < 4; i++ {
		tr.nextStep()
		v, msg := tr.observeShared("", "merge_up", map[string]any{}, ok, true, true, true)
		if v == stallStop {
			t.Fatalf("第 %d 次就把它停了: %s", i+1, msg)
		}
	}
}

// 不是 shared 的照旧要拦 —— 这条判据本身是对的, 只是不该管到那两个工具头上
func Test普通工具原地转照样拦(t *testing.T) {
	tr := newStallTracker()
	obs := Observation{Result: "一样的结果"}
	var last stallVerdict
	var msg string
	for i := 0; i < 4; i++ {
		tr.nextStep()
		last, msg = tr.observeShared("", "list_dir", map[string]any{"path": "src"}, obs, false, false, false)
	}
	if last == stallNone {
		t.Fatal("同样的参数调了四次还不拦")
	}
	if !strings.Contains(msg, "list_dir") {
		t.Errorf("没说清是哪个工具: %s", msg)
	}
}

/**
 * **反复失败那条照旧管着 shared 的工具**: 那条判的是"错了还在同一个
 * 地方硬撞", 跟"重复调用"是两回事 —— 一个 merge_up 一直报同一个冲突,
 * 该拦还得拦.
 */
func Test就算是shared一直报同一个错也要拦(t *testing.T) {
	tr := newStallTracker()
	bad := Observation{Err: "这几个文件还冲突着：src/api.ts"}
	var last stallVerdict
	for i := 0; i < 4; i++ {
		tr.nextStep()
		last, _ = tr.observeShared("", "merge_up", map[string]any{}, bad, true, true, true)
	}
	if last == stallNone {
		t.Fatal("同一个错撞了四次还不管")
	}
}
