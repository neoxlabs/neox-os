package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsVerification只认真的在检验代码的那些(t *testing.T) {
	yes := []string{
		"go test ./...",
		"cd app && pytest -q",
		"npm test",
		"npx vitest run",
		"npm run build",
		"cargo test",
		"make check",
		"go vet ./cmd/...",
		// **验证命令使用 python3**: macOS 上没有裸 python.
		// 少这一个 3, 整轮验证的证据全丢, 而闸门也就白设了
		"python3 -m unittest discover -s tests",
		"python3 -m pytest -q",
	}
	for _, cmd := range yes {
		if !isVerification(cmd) {
			t.Errorf("这条该算验证: %q", cmd)
		}
	}
	// **宁可少认一条, 不能多认一条**: 多认了就是在提交里写了一句假话
	no := []string{
		"ls -la",
		"cat README.md",
		"git log --oneline",
		"git status",
		"echo hello",
		"pwd",
		"mkdir -p data",
		"python app/server.py",
		// **装依赖不是验证**: 这条里有 pytest 这个词, 按关键词匹配会被
		// 当成"跑了测试" —— 关键词匹配会把它混进来. 而装依赖成功
		// 跟代码没坏是两件毫不相干的事.
		"cd .tmp && python3 -m pip install --target=.pylibs pytest -q",
		"npm install",
		"go get ./...",
	}
	for _, cmd := range no {
		if isVerification(cmd) {
			t.Errorf("这条不该算验证: %q", cmd)
		}
	}
}

func TestVerifyLog取完就清下一轮从头记(t *testing.T) {
	log := newVerifyLog()
	log.note("小登", "go test ./...", 0)
	log.note("小登", "ls", 0) // 不是验证, 不记
	log.note("报表", "pytest", 1)

	got := log.takeFor("小登")
	if len(got) != 1 || got[0].Cmd != "go test ./..." || got[0].Exit != 0 {
		t.Fatalf("记岔了: %+v", got)
	}
	if again := log.takeFor("小登"); len(again) != 0 {
		t.Fatalf("取完没清 —— 下一轮会把上一轮的证据又写一遍: %+v", again)
	}
	// 别人的不受影响
	if other := log.takeFor("报表"); len(other) != 1 || other[0].Exit != 1 {
		t.Fatalf("串台了: %+v", other)
	}
}

func TestVerifyLog只留最近几条(t *testing.T) {
	log := newVerifyLog()
	// 一轮里改一次跑一次测试是常事 —— 进提交的是结论, 不是过程
	for i := 0; i < 20; i++ {
		log.note("小登", "go test ./...", i%2)
	}
	got := log.takeFor("小登")
	if len(got) > 4 {
		t.Fatalf("把整个过程都记进去了: %d 条", len(got))
	}
}

func TestVerifyTrailer失败的也要写(t *testing.T) {
	// 一次没通过的测试是这次提交最重要的事实之一: 它说明这条改动是在
	// 已知不通过的情况下留下的. 藏起来只会让下一个人以为它是绿的.
	text := verifyTrailer([]verifyRun{{Cmd: "go test ./...", Exit: 1}})
	if !strings.Contains(text, "没通过") || !strings.Contains(text, "1") {
		t.Fatalf("失败没写清楚: %s", text)
	}
	if !strings.HasPrefix(text, "Neox-Verify: ") {
		t.Fatalf("不是 git trailer 格式: %s", text)
	}
	if verifyTrailer(nil) != "" {
		t.Fatal("什么都没验却写了尾注")
	}
}

func TestCommitTurnWith证据进提交(t *testing.T) {
	needGit(t)
	atHome(t)
	repo := newRepo(t)
	plan, err := Assign(repo, "小登", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Dir, "a.py"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitTurnWith(plan.Dir, "小登", plan.Branch, "改完了", 3,
		[]verifyRun{{Cmd: "pytest -q", Exit: 0}}); err != nil {
		t.Fatal(err)
	}
	body, err := runGit(plan.Dir, "log", "-1", "--pretty=%B")
	if err != nil {
		t.Fatal(err)
	}
	// 它挑的命令 + 系统记的结果 —— 两样都要在
	for _, want := range []string{"pytest -q", "通过", "Neox-Bot: 小登"} {
		if !strings.Contains(body, want) {
			t.Errorf("提交里少了 %q:\n%s", want, body)
		}
	}
}
