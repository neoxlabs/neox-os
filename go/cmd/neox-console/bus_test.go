package main

import (
	"strings"
	"sync"
	"testing"
)

func TestOverlap只挑真的碰在一起的那几个(t *testing.T) {
	merged := []string{"app/db.py", "app/routes/leave.py", "README.md"}
	mine := []string{"app/db.py", "static/index.html"}
	got := overlap(merged, mine)
	if len(got) != 1 || got[0] != "app/db.py" {
		t.Fatalf("交集算错了: %+v", got)
	}
	// 没交集就是没交集 —— 这条决定了"不打扰谁"
	if rest := overlap(merged, []string{"static/index.html"}); len(rest) != 0 {
		t.Fatalf("八竿子打不着的也算上了: %+v", rest)
	}
}

func TestTouchedBy认得出它在自己分支上碰过什么(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	write(t, repo, "db.py", "原来的\n")
	commit(t, repo, "起个头")

	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	// 提交过的
	write(t, plan.Dir, "login.py", "新写的\n")
	commit(t, plan.Dir, "写登录")
	// 还没提交的 —— **它正在改的东西最要紧**, 也得算
	write(t, plan.Dir, "db.py", "我正在改这个\n")

	got := touchedBy(plan.Dir, plan.Branch)
	has := func(name string) bool {
		for _, x := range got {
			if x == name {
				return true
			}
		}
		return false
	}
	if !has("login.py") {
		t.Errorf("提交过的没算上: %+v", got)
	}
	if !has("db.py") {
		t.Errorf("正在改的没算上 —— 那是最要紧的那一类: %+v", got)
	}
	// 项目本来就有的那些**不算它碰过的** —— 从分叉点算起
	if len(got) > 2 {
		t.Errorf("把不是它改的也算进来了: %+v", got)
	}
}

func TestMergedNotice说清三件事(t *testing.T) {
	// 谁动了、动了你在改的哪几个、下一步干什么 —— 少一件收到的人就得猜,
	// 而猜出来的下一步通常是"再问一遍"
	text := mergedNotice("小登", []string{"app/db.py"}, 3)
	for _, want := range []string{"小登", "app/db.py", "sync_down"} {
		if !strings.Contains(text, want) {
			t.Errorf("通知里少了 %q:\n%s", want, text)
		}
	}
	/**
	 * **短**. 这段是塞进对方一轮对话里的, 每一个字都要付钱, 而它需要的
	 * 信息只有两条: 谁动了、动了你也在改的哪几个. "这次一共动了 3 个,
	 * 其余跟你没关系"是废话 —— 跟他没关系的本来就不用说.
	 */
	if len([]rune(text)) > 60 {
		t.Errorf("话太长了(%d 字), 这段每轮都要付钱:\n%s", len([]rune(text)), text)
	}
}

func TestBus发出去不拖住合入的那个人(t *testing.T) {
	bus := &Bus{}
	blocked := make(chan struct{})
	done := make(chan struct{})
	bus.OnMerged(func(Merged) {
		<-blocked // 订阅者故意卡住
		close(done)
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// 合入的人正卡在工具调用里等返回 —— 发通知不该让它多等
		bus.PublishMerged(Merged{Project: "p", Bot: "小登"})
	}()
	wg.Wait() // 卡住的话这里就过不去

	close(blocked)
	<-done
}

func TestBus没人订阅也不炸(t *testing.T) {
	var nilBus *Bus
	nilBus.PublishMerged(Merged{}) // 不该 panic
	(&Bus{}).PublishMerged(Merged{})
}
