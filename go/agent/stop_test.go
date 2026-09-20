package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

/**
 * **被叫停不是超时**.
 *
 *	取消一次正在执行的任务时, 错误账本可能留下:
 *
 *	    write_file 跑了 30s 还没回来 …缩小范围
 *	    list_dir 跑了 30s 还没回来 …
 *	    run 跑了 3m0s 还没回来 …
 *	    停滞: 同一类错误连着出现 3 次了
 *
 *	一句都不真. 那几个工具是当场回来的 —— 上下文一取消 select 立刻
 *	就走超时那条路, 而那句话里的时长是写死的上限, 不是它真等了多久.
 *	于是: 一串假超时, 一次假停滞, 而人喊了停这件事一个字都没有.
 */
func Test被叫停不许说成超时(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{Box: Toolbox{Ctx: ctx}}
	slow := Tool{Name: "write_file", Run: func(Toolbox, map[string]any) (string, error) {
		time.Sleep(2 * time.Second) // 还没回来, 而这时候人喊了停
		return "写完了", nil
	}}
	cancel()
	started := time.Now()
	_, err := a.runWithTimeout(slow, map[string]any{})
	if err == nil {
		t.Fatal("被叫停了却当成功返回")
	}
	if !Stopped(err) {
		t.Errorf("没认出这是被叫停: %v", err)
	}
	if strings.Contains(err.Error(), "还没回来") || strings.Contains(err.Error(), "缩小范围") {
		t.Errorf("把被叫停说成了超时: %v", err)
	}
	// 而且要当场回来, 不能真等满那个上限
	if took := time.Since(started); took > time.Second {
		t.Errorf("被叫停之后还等了 %v", took)
	}
}
