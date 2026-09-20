package agent

import (
	"strings"
	"testing"
)

// 真机 08:49: 他问的是"我设了哪些提醒", 它立了一条他从没说过的来电拦截
func TestWhenRefusesRawHeNeverSaid(t *testing.T) {
	var added bool
	ts := NewToolSet(WithWhen(DefaultTools(),
		func(a, b, c string, d any, e, f, g, h, i string, j bool) (string, error) {
			added = true
			return "ok", nil
		},
		func(kind, place, say, raw string) error { added = true; return nil }))
	tool, _ := ts.Get("notify_when")
	box := Toolbox{Heard: func() []string {
		return []string{"现在几点？我设了哪些提醒？你都记着哪些地方？"}
	}}
	_, err := tool.Run(box, map[string]any{"event": "call.incoming",
		"raw": "我上课呢接不了电话，你先给我拦住", "why": "他正上课"})
	if err == nil || !strings.Contains(err.Error(), "不在他最近说过的话里") {
		t.Fatalf("编出来的原话被收下了: %v", err)
	}
	if added {
		t.Fatal("拒了还是写进去了")
	}
}

// 他真说了, 模型顺手改了几个字、换了标点 —— 照收
func TestWhenAcceptsLightlyEditedQuote(t *testing.T) {
	ts := NewToolSet(WithWhen(DefaultTools(),
		func(a, b, c string, d any, e, f, g, h, i string, j bool) (string, error) { return "ok", nil },
		func(kind, place, say, raw string) error { return nil }))
	tool, _ := ts.Get("notify_when")
	box := Toolbox{Heard: func() []string {
		return []string{"嗯", "明天要是下雨的话提醒我带伞啊"}
	}}
	if _, err := tool.Run(box, map[string]any{"fact": "weather", "op": ">", "value": "60",
		"raw": "要是下雨提醒我带伞", "why": "降水概率", "say": "要下雨了, 带伞"}); err != nil {
		t.Fatalf("他说过的话被拒了: %v", err)
	}
}

// 他没提过的事**照记, 但要说出来** —— 拦下来的多半是它正常的归纳
// (他说"电费该交了", 它记"交电费"); 而真记错了, 一条待办他一眼看得见、
// 一句话删得掉. 会自己触发的那些(notify_when/watch)才拦, 见 notSaid.
func TestAgendaFlagsThingsHeNeverMentioned(t *testing.T) {
	var added []string
	ts := NewToolSet(WithAgenda(DefaultTools(),
		func(what string, at int64, where string) (string, error) {
			added = append(added, what)
			return "ok", nil
		},
		func(bool) string { return "" },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { return "", nil }))
	tool, _ := ts.Get("agenda")
	box := Toolbox{Heard: func() []string {
		return []string{"记一下：明天下午三点开会，还有记得买牛奶", "现在几点？我设了哪些提醒？"}
	}}
	for what, mentioned := range map[string]bool{"开会": true, "买牛奶": true, "上课": false} {
		out, err := tool.Run(box, map[string]any{"do": "add", "what": what})
		if err != nil {
			t.Errorf("%s 被拒了: %v", what, err)
			continue
		}
		if got := strings.Contains(out, "他最近没提过"); got == mentioned {
			t.Errorf("%s: 提示语该有没有(mentioned=%v): %q", what, mentioned, out)
		}
	}
	if len(added) != 3 {
		t.Fatalf("三条都该写进去, 实际 %v", added)
	}
}

// 没有对话可核对的场景(测试、neox-chat)不拦 —— 那里 Heard 是 nil
func TestNoHeardNoCheck(t *testing.T) {
	ts := NewToolSet(WithAgenda(DefaultTools(),
		func(string, int64, string) (string, error) { return "ok", nil },
		func(bool) string { return "" },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { return "", nil }))
	tool, _ := ts.Get("agenda")
	if _, err := tool.Run(Toolbox{}, map[string]any{"do": "add", "what": "上课"}); err != nil {
		t.Fatal(err)
	}
}

// Agent 自己把这一轮和插话记下来, 给工具核对
func TestAgentHearsTurnAndInterrupts(t *testing.T) {
	a := &Agent{}
	for i := 0; i < 10; i++ {
		a.hear(strings.Repeat("话", i+1))
	}
	a.hear("   ")
	got := a.recentHeard()
	if len(got) != heardKeep || got[len(got)-1] != strings.Repeat("话", 10) {
		t.Fatalf("%v", got)
	}
}
