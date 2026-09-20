package main

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/agent"
)

func convs() []agent.Conversation {
	return []agent.Conversation{
		{Thread: "t1", Title: "给 todo 加 done 命令", Turns: 12, LastAt: 300},
		{Thread: "t2", Title: "做个记账小工具", Turns: 5, LastAt: 200,
			Unfinished: true, PendingSteps: 5},
		{Thread: "t3", Title: "整理下载目录", Turns: 3, LastAt: 100},
	}
}

// 这份摘要进系统段, 所以**必须确定性** —— 抖一下前缀缓存整段作废
func TestRecentDigestIsDeterministic(t *testing.T) {
	first := recentDigest(convs(), "")
	for i := 0; i < 20; i++ {
		if recentDigest(convs(), "") != first {
			t.Fatal("同样的对话列表算出了不同的摘要")
		}
	}
}

// 当前这一段不进清单 —— 它的历史 agent 本来就全看得见, 再列一遍是噪音
func TestRecentDigestExcludesCurrent(t *testing.T) {
	got := recentDigest(convs(), "t1")
	if strings.Contains(got, "给 todo 加 done") {
		t.Fatalf("把当前这段也列进去了:\n%s", got)
	}
	if !strings.Contains(got, "做个记账小工具") {
		t.Fatalf("别的对话丢了:\n%s", got)
	}
}

// 「没干完」是这份清单里最值钱的一条 —— 它是真的还欠着活
func TestRecentDigestMarksUnfinished(t *testing.T) {
	got := recentDigest(convs(), "")
	if !strings.Contains(got, "没干完(停在第 5 步)") {
		t.Fatalf("没标出哪段还欠着活:\n%s", got)
	}
	// 干完的那些不该被标
	line := ""
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "整理下载目录") {
			line = l
		}
	}
	if strings.Contains(line, "没干完") {
		t.Fatalf("干完的也标成没干完: %s", line)
	}
}

// 条数有上限 —— 系统段是每轮都发的, 不能让它随对话数无限长
func TestRecentDigestIsBounded(t *testing.T) {
	var many []agent.Conversation
	for i := 0; i < 50; i++ {
		many = append(many, agent.Conversation{
			Thread: string(rune('a' + i%26)), Title: "第 N 段", Turns: 1, LastAt: int64(i)})
	}
	if n := strings.Count(recentDigest(many, ""), "\n") + 1; n > 8 {
		t.Fatalf("摘要 %d 行, 没有上限 —— 系统段会随对话数无限长", n)
	}
}

// 排除的必须是"新进程将要属于的那一段", 不是"上一段".
//
// **注意这条钉不住调用点**: 它验的是 recentDigest 这个函数, 而"传哪个变量进去"
// 在 runShell 里是内联的, 单元测试够不着. 如果调用点改回 threadOfCurrent,
// 这条**不会红** —— 真实链路才会发现清单里少了一段. 这类"算对了但接错线"的错
// 只能在真实链路中暴露.
//
// /新 之后 threadOfCurrent 还留着上一段的线头, 于是刚聊完的
// 那段被当成"当前段"剔掉了 —— 用户问"昨天那个做完了吗", 清单里恰好少了
// 他最可能问的那一段.
func TestRecentDigestExcludesTheThreadWeAreJoining(t *testing.T) {
	// 全新对话: 谁都不排除
	all := recentDigest(convs(), "")
	for _, want := range []string{"给 todo 加 done", "做个记账小工具", "整理下载目录"} {
		if !strings.Contains(all, want) {
			t.Fatalf("全新对话该列出全部, 缺 %q:\n%s", want, all)
		}
	}
	// 接续 t2: 只排除 t2
	got := recentDigest(convs(), "t2")
	if strings.Contains(got, "做个记账小工具") {
		t.Fatalf("接续的那段不该再列一遍:\n%s", got)
	}
	if !strings.Contains(got, "给 todo 加 done") {
		t.Fatalf("别的段被误删了:\n%s", got)
	}
}
