package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/engine"
)

func windowText(t *testing.T, w *Window) string {
	t.Helper()
	msgs, _ := w.Messages()
	all := ""
	for _, m := range msgs {
		all += m.Content
	}
	return all
}

// **它说过的话必须留在上下文里.**
//
// 原来 reply 那条路只发事件、不写回窗口 —— 下一轮模型看过去, 用户的问题
// 全都还没答, 于是再答一遍.
//
// 连问"蚌埠是哪里""你咋这么快""你有什么工具"时,
// **同一个问题被三轮各答了一遍**. 插话框架话可能永久生效
// (那确实是另一个 bug, 也修了), 但复现时只有 1 次插话, 另外两问是正常轮次
// —— 根子在这儿: 回答从来没留下过.
func TestAgentRepliesGoIntoTheWindow(t *testing.T) {
	sys := newFakeSys()
	w := NewWindow(engine.NewPageStore(), "闲聊", 1<<20)
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Window: w,
		Model: &Scripted{Steps: []Step{{Reply: "蚌埠在安徽北部"}}}}
	if err := ag.Run("安徽蚌埠是哪里"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(windowText(t, w), "蚌埠在安徽北部") {
		t.Fatal("它说过的话没进上下文 —— 下一轮会把同一个问题再答一遍")
	}
}

// 收工那句(done)同样是"我说过什么"
func TestAgentDoneSummaryGoesIntoTheWindow(t *testing.T) {
	sys := newFakeSys()
	dir := t.TempDir()
	w := NewWindow(engine.NewPageStore(), "干活", 1<<20)
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Box: Toolbox{Root: dir},
		Window: w, Model: &Scripted{Steps: []Step{
			{Tool: "list_dir", Args: map[string]any{"path": "."}, CallID: "c1"},
			{Done: "看完了，目录是空的"},
		}}}
	if err := ag.Run("看看目录"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(windowText(t, w), "目录是空的") {
		t.Fatal("收工的交代没进上下文")
	}
}

// 多轮: 第二轮必须看得见第一轮的答案.
// 这条钉的是用户实际撞到的形态 —— 连问几个问题, 老问题被反复重答.
func TestSecondTurnSeesTheFirstAnswer(t *testing.T) {
	sys := newFakeSys()
	w := NewWindow(engine.NewPageStore(), "第一问", 1<<20)
	m := &Scripted{Steps: []Step{{Reply: "答案一"}, {Reply: "答案二"}}}
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Window: w, Model: m}
	if err := ag.Run("第一问"); err != nil {
		t.Fatal(err)
	}
	w.AppendUserTurn("第二问")
	if err := ag.Run("第二问"); err != nil {
		t.Fatal(err)
	}
	all := windowText(t, w)
	for _, want := range []string{"答案一", "答案二", "第二问"} {
		if !strings.Contains(all, want) {
			t.Fatalf("上下文里缺 %q:\n%s", want, all)
		}
	}
	// 顺序不能乱: 答案一在第二问之前
	if strings.Index(all, "答案一") > strings.Index(all, "第二问") {
		t.Fatalf("时间线是错的:\n%s", all)
	}
}

// 用户设的名字必须**到得了模型眼前**.
//
// 在设置里填了名字后, 客户端一直把它放在 /say 的 from 里送,
// OS 也收到了 —— 但 abi 那边写着"From 谁说的, 审计用", 于是它只进事件日志.
// 用户问"我是谁", bot 答"我没有你身份的直接资料, 你没自我介绍过".
// 界面上一切正常, 名字也存住了, 只是那条线断在最后一步.
func TestSpeakerNameReachesTheTurn(t *testing.T) {
	a := &Agent{}
	got := a.noteSpeaker("刘明康")
	if !strings.Contains(got, "刘明康") {
		t.Fatalf("名字没进这一轮: %q", got)
	}
	// 同一个人接着说, 不再重复报 —— 每轮都带一句是纯噪音
	if again := a.noteSpeaker("刘明康"); again != "" {
		t.Fatalf("同一个人重复报了: %q", again)
	}
	// 换了人必须再报一次: 那正是它会答错的时候
	if other := a.noteSpeaker("老张"); !strings.Contains(other, "老张") {
		t.Fatalf("换人了却没报: %q", other)
	}
}

// "我" "你" 是占位词, 不是名字 —— 报上去模型会以为用户真叫这个.
func TestPlaceholdersAreNotNames(t *testing.T) {
	for _, placeholder := range []string{"", " ", "我", "你", "用户"} {
		if got := (&Agent{}).noteSpeaker(placeholder); got != "" {
			t.Errorf("占位词 %q 被当成名字报了: %q", placeholder, got)
		}
		if got := SpeakerNote(placeholder); got != "" {
			t.Errorf("占位词 %q 被 SpeakerNote 当成名字: %q", placeholder, got)
		}
	}
}

// **那一句必须自己说清楚它是什么**.
//
// 只写"[说话的是 明康]"时, 标记确实送到了 —— 模型照样答
// "我不知道你是谁". 一个光秃秃的标记读不出"这是用户的名字、可以用".
func TestSpeakerNoteExplainsItself(t *testing.T) {
	note := SpeakerNote("明康")
	if !strings.Contains(note, "明康") {
		t.Fatal("名字都不在")
	}
	if !strings.Contains(note, "名字") {
		t.Fatalf("没说清这是个名字, 模型读不出来该拿它干什么: %q", note)
	}
	if !strings.HasSuffix(note, "\n") {
		t.Fatalf("标记要自成一行, 不然会跟用户原话粘在一起: %q", note)
	}
}

// **别再教它写信封.**
//
//	它自己说过的话原来以 {"said":"…"} 的形状回到它眼前, 于是它学会了
//	照着吐 —— 一天有 11 次把 {"said":""} 原样发给用户.
func TestOwnRepliesComeBackAsPlainText(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "他问了一句", 0)
	defer w.Release()
	w.AppendUserTurn("我在哪")
	w.AppendAssistantSaid("在固镇县庙岗路，汇金国际碧桂苑里面。")
	msgs, _ := w.Messages()
	for _, m := range msgs {
		if strings.Contains(m.Content, `{"said"`) || strings.Contains(m.Content, `{"user"`) {
			t.Fatalf("它在自己的历史里看见了信封: %q", m.Content)
		}
		if m.Role == "assistant_reply" && m.Content != "在固镇县庙岗路，汇金国际碧桂苑里面。" {
			t.Fatalf("回复没有原样回来: %q", m.Content)
		}
	}
}
