package osinit

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// **它在哪儿干活也要留痕**.
//
// 工作区就是写轴那条能力的 scope —— 进程还活着时从能力集读得到, 进程一死
// 就没有任何地方能回答"这个 bot 当时在哪儿动的手". 而账本本来就是为了
// 回答这类问题存在的.
//
// 界面也吃这一口: 工具行把工作区内的路径写成相对的, 靠的就是"这条事件
// 属于哪个工作区". 历史里没有它, 一整段旧对话就只能摊着一堆逐字相同的
// 绝对路径.
func TestCreatedEventRecordsTheWorkspace(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试完了")
	pid, err := o.Spawn(abi.ProcessSpec{
		App: "demo", Name: "小勤",
		Caps: []abi.Capability{
			{Axis: abi.AxisRead, Scope: "/"},
			{Axis: abi.AxisWrite, Scope: "/Users/x/AI/oa"},
		},
	}, InprocBody{Entry: func(context.Context, ProcessContext) (any, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	var work any
	for _, ev := range o.Log().Replay(pid, 0) {
		m, ok := ev.Payload.(map[string]any)
		if !ok || ev.Kind != abi.EvProcState || m["state"] != abi.StateCreated {
			continue
		}
		work = m["work"]
		break
	}
	if work != "/Users/x/AI/oa" {
		t.Fatalf("创建事件里没留工作区(拿到 %v) —— 进程一死就再也查不出来了", work)
	}
}

// 没有写能力的进程不该凭空多一个字段 —— 空字符串会被界面当成"工作区是根目录"
func TestReadOnlyProcessHasNoWorkspace(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试完了")
	pid, _ := o.Spawn(abi.ProcessSpec{
		App:  "demo",
		Caps: []abi.Capability{{Axis: abi.AxisRead, Scope: "/"}},
	}, InprocBody{Entry: func(context.Context, ProcessContext) (any, error) { return nil, nil }})
	for _, ev := range o.Log().Replay(pid, 0) {
		m, ok := ev.Payload.(map[string]any)
		if !ok || ev.Kind != abi.EvProcState || m["state"] != abi.StateCreated {
			continue
		}
		if _, has := m["work"]; has {
			t.Fatal("只读进程也记了工作区 —— 界面会拿它去截路径")
		}
	}
}

// 删一段对话之后, **进程表里那条记录也得没**.
//
// Kill 只是让它停下来 —— 记录还在, /processes 照样返回它. 症状是界面上
// 那条会话删了之后**几秒又自己长回来**, 显示成"不在": 轮询把进程表拉回来,
// 而那条记录还在里面.
func TestDropRemovesTheProcessRecord(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试完了")
	pid, err := o.Spawn(abi.ProcessSpec{App: "demo", Name: "临时"},
		InprocBody{Entry: func(context.Context, ProcessContext) (any, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	o.Kill(pid, "删掉这段对话")
	if len(o.List()) == 0 {
		t.Fatal("杀掉就从进程表里消失了? 那这条测试本身就没意义了")
	}
	o.Drop(pid)
	for _, info := range o.List() {
		if info.PID == pid {
			t.Fatal("抹过之后进程表里还有它 —— 界面上那条会话会自己长回来")
		}
	}
	// token 也要一起失效: 留着它等于给一个已经被删掉的进程留了把钥匙
	if _, ok := o.TokenFor(pid); ok {
		t.Fatal("记录抹了, token 还在")
	}
}

// 一条被删掉的对话**不该有能力弄死这台机器**.
//
// 删对话是先 Kill 再 Drop, 而 Kill 是异步的 —— 记录被抹掉时 body 那个
// goroutine 往往还在跑. 它原来是硬取 o.procs[pid].ctx: 取到 nil 当场
// panic, 而且是在另一个 goroutine 里, 整个 OS 跟着倒.
func TestDropWhileRunningDoesNotCrash(t *testing.T) {
	o := New(Options{Mode: abi.ModeDev})
	defer o.Shutdown("测试完了")
	started := make(chan struct{})
	release := make(chan struct{})
	pid, err := o.Spawn(abi.ProcessSpec{App: "demo", Name: "还在跑"},
		InprocBody{Entry: func(context.Context, ProcessContext) (any, error) {
			close(started)
			<-release
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	o.Kill(pid, "用户删掉了这段对话")
	o.Drop(pid) // ← 这一下原来会让 run 那个 goroutine panic
	close(release)
	// 走到这儿没崩就是过了; 再动一下 OS 确认它还活着
	if _, err := o.Spawn(abi.ProcessSpec{App: "demo"},
		InprocBody{Entry: func(context.Context, ProcessContext) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("抹掉一个还在跑的进程之后, OS 起不了新进程了: %v", err)
	}
}

// **取记录只有一处**, 别再散着写裸下标.
//
// Drop 出现之前, "记录只增不删"一直成立, 于是 o.procs[pid].xxx 到处都是.
// Drop 让它随时可能不在 —— 每加一个删除路径就要把那些裸下标重新数一遍,
// 数漏一个就是一次 panic, 而且是在 body 那个 goroutine 里, 整个 OS 跟着倒.
//
// 这条盯的是**源码本身**: 判据不在运行时, 在"还有没有人这么写".
func TestNoBareProcessLookups(t *testing.T) {
	src, err := os.ReadFile("os.go")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		code := line
		// 注释里提到它是在讲这段历史, 不算 —— 行注释和块注释都要剥
		if at := strings.Index(code, "//"); at >= 0 {
			code = code[:at]
		}
		if strings.HasPrefix(strings.TrimSpace(code), "*") {
			continue
		}
		if !strings.Contains(code, "o.procs[") {
			continue
		}
		// 合法的三种: 带 ok 的取、删、写
		if strings.Contains(code, ", ok :=") || strings.Contains(code, ", alive :=") ||
			strings.Contains(code, "delete(") || strings.Contains(code, "] = ") ||
			strings.Contains(code, "rec := o.procs[") {
			continue
		}
		t.Errorf("os.go:%d 又出现了裸下标, 记录一旦被 Drop 抹掉这里就会 panic:\n  %s", i+1, strings.TrimSpace(line))
	}
}
