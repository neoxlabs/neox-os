package agent

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/engine"
)

// 压紧: 前面那一大截换成一段摘要, 后面原样留着
func TestCompactKeepsTailAndSummarizesHead(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "第一句", 0)
	defer w.Release()
	// 头上那一截要够大 —— 省不下的话压紧本来就该拒(见下一个测试)
	w.AppendUserTurn("我老婆明天八点到学校" + strings.Repeat("，路上还要送孩子", 60))
	w.AppendBatch([]PageCall{{ID: "c1", Tool: "remind_me", Args: map[string]any{"daily": "07:10"}}}, "想一下")
	w.AppendResult("记下了", "", "c1")
	w.AppendAssistantSaid("设好了")
	w.AppendUserTurn("今天路况怎么样")
	w.AppendBatch([]PageCall{{ID: "c2", Tool: "where", Args: map[string]any{"traffic": "1"}}}, "看一眼")
	w.AppendResult("东海大道畅通", "", "c2")

	before := w.Bytes()
	head := w.HeadText(3, 4000)
	if !strings.Contains(head, "他: 我老婆明天八点到学校") || !strings.Contains(head, "remind_me") {
		// 动作页不该进摘要素材 —— 结论在人话里
		if strings.Contains(head, "remind_me") {
			t.Fatalf("动作页混进摘要素材了: %q", head)
		}
	}
	dropped := w.CompactFrom("他老婆每天 7:10 到高新实验学校, 闹钟已设", 2)
	if dropped == 0 {
		t.Fatal("一个字节都没切掉")
	}
	if w.Bytes() >= before {
		t.Fatalf("压紧之后反而更大了: %d → %d", before, w.Bytes())
	}
	msgs, _ := w.Messages()
	var all strings.Builder
	for _, m := range msgs {
		all.WriteString(m.Role + ": " + m.Content + "\n")
	}
	got := all.String()
	if !strings.Contains(got, "他老婆每天 7:10") || !strings.Contains(got, "收成一段摘要") {
		t.Fatalf("摘要没进去: %q", got)
	}
	// 后半截原样留着
	if !strings.Contains(got, "今天路况怎么样") || !strings.Contains(got, "东海大道畅通") {
		t.Fatalf("该留的被切了: %q", got)
	}
	// **切在用户那一句上**: 留下来的第一条动作必须还配得上它的结果
	if strings.Count(got, "c2") > 0 && !strings.Contains(got, "东海大道畅通") {
		t.Fatal("动作和结果被切散了")
	}
	if strings.Contains(got, "我老婆明天八点到学校") {
		t.Fatalf("前半截该换成摘要而不是留着: %q", got)
	}
}

// 切不到用户那一句就不切 —— 宁可不压, 不能把一对动作/结果切散
func TestCompactRefusesMidPair(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "任务", 0)
	defer w.Release()
	w.AppendUserTurn("看一下")
	w.AppendBatch([]PageCall{{ID: "c1", Tool: "run", Args: map[string]any{"cmd": "ls"}}}, "")
	w.AppendResult("a.txt", "", "c1")
	if n := w.CompactFrom("摘要", 2); n != 0 {
		t.Fatalf("后面没有用户输入页, 不该切: %d", n)
	}
	if n := w.CompactFrom("", 1); n != 0 {
		t.Fatal("没有摘要就不该切")
	}
}

// 省不下就不压 —— 压一次整段前缀作废一次, 不能为几十字节去换
func TestCompactRefusesWhenItSavesNothing(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "任务", 0)
	defer w.Release()
	w.AppendUserTurn("嗯")
	w.AppendAssistantSaid("好")
	w.AppendUserTurn("那走吧")
	if n := w.CompactFrom(strings.Repeat("很长的摘要", 50), 1); n != 0 {
		t.Fatalf("省不下还压了: %d", n)
	}
}

// 切点按字节挑 —— 一页可能是一句"嗯", 也可能是一个读回来的文件
func TestKeepFromTailBytes(t *testing.T) {
	store := engine.NewPageStore()
	w := NewWindow(store, "任务", 0)
	defer w.Release()
	w.AppendUserTurn(strings.Repeat("旧", 2000))
	w.AppendAssistantSaid(strings.Repeat("旧", 2000))
	w.AppendUserTurn("新的一句")
	w.AppendAssistantSaid("新的回答")
	from := w.KeepFromTailBytes(200)
	if from == 0 {
		t.Fatal("留 200 字节不该从头留")
	}
	if from >= w.PageCount() {
		t.Fatalf("切点越界: %d/%d", from, w.PageCount())
	}
	// 留得下全部时就从头留
	if got := w.KeepFromTailBytes(1 << 20); got != 0 {
		t.Fatalf("预算够大就该一页不切, 得到 %d", got)
	}
}
