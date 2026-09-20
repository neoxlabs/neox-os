package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func seenBox(t *testing.T) Toolbox {
	t.Helper()
	return Toolbox{Root: t.TempDir(), Seen: newSeenFiles()}
}

// **不许盖掉你没读过的那一版**.
//
// 拉人进来是同一个工作区(分开目录的话他连你写的代码都改不了), 于是两个
// bot 会同时动同一份文件. write_file 原来是无条件覆盖的: 后写的那个把
// 先写的整份盖掉, **两边都显示成功**, 而其中一个人的活没了.
func TestWriteRefusesToClobberWhatYouNeverRead(t *testing.T) {
	box := seenBox(t)
	// 同屋的谁先建了一个文件
	other := filepath.Join(box.Root, "PLAN.md")
	if err := os.WriteFile(other, []byte("别人写的内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	write, _ := NewToolSet(DefaultTools()).Get("write_file")
	_, err := write.Run(box, map[string]any{"path": "PLAN.md", "content": "我的内容"})
	if err == nil {
		t.Fatal("盖掉了没读过的文件 —— 别人的活就这么没了, 而两边都显示成功")
	}
	// 报错要说清**下一步怎么办**, 不是只说"不行"
	if !contains(err.Error(), "read_file") {
		t.Errorf("没告诉它先读一遍: %v", err)
	}
	if got, _ := os.ReadFile(other); string(got) != "别人写的内容" {
		t.Fatal("拦住了却还是把文件改了")
	}
}

// 读过之后照常写 —— 这道闸拦的是"没看过就盖", 不是拦写.
func TestWriteWorksAfterReading(t *testing.T) {
	box := seenBox(t)
	path := filepath.Join(box.Root, "a.txt")
	if err := os.WriteFile(path, []byte("原来的"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(DefaultTools())
	read, _ := ts.Get("read_file")
	if _, err := read.Run(box, map[string]any{"path": "a.txt"}); err != nil {
		t.Fatal(err)
	}
	write, _ := ts.Get("write_file")
	if _, err := write.Run(box, map[string]any{"path": "a.txt", "content": "新的"}); err != nil {
		t.Fatalf("读过了还不让写: %v", err)
	}
	// 自己刚写的这一版也算读过 —— 否则接着再写一次会被自己拦住
	if _, err := write.Run(box, map[string]any{"path": "a.txt", "content": "再新的"}); err != nil {
		t.Fatalf("被自己刚才的改动拦住了: %v", err)
	}
}

// 读过之后**别人又改了**, 这次要拦 —— 那正是丢活的那一刻.
func TestWriteRefusesWhenSomeoneElseChangedIt(t *testing.T) {
	box := seenBox(t)
	path := filepath.Join(box.Root, "a.txt")
	_ = os.WriteFile(path, []byte("v1"), 0o644)
	ts := NewToolSet(DefaultTools())
	read, _ := ts.Get("read_file")
	_, _ = read.Run(box, map[string]any{"path": "a.txt"})
	// 同屋的谁在这中间改了它
	_ = os.WriteFile(path, []byte("同屋的谁改的 v2"), 0o644)

	write, _ := ts.Get("write_file")
	_, err := write.Run(box, map[string]any{"path": "a.txt", "content": "我的 v2"})
	if err == nil {
		t.Fatal("别人的改动被悄悄盖掉了")
	}
	if got, _ := os.ReadFile(path); string(got) != "同屋的谁改的 v2" {
		t.Fatal("拦住了却还是覆盖了")
	}
}

// 新建文件从来不会覆盖谁 —— 不许拦.
func TestNewFileIsNeverBlocked(t *testing.T) {
	box := seenBox(t)
	write, _ := NewToolSet(DefaultTools()).Get("write_file")
	if _, err := write.Run(box, map[string]any{"path": "new.txt", "content": "x"}); err != nil {
		t.Fatalf("新建也被拦了: %v", err)
	}
}

// 没接这道闸的场景(单人/测试)照旧 —— nil 不许崩.
func TestNoSeenTrackerStillWorks(t *testing.T) {
	box := Toolbox{Root: t.TempDir()}
	write, _ := NewToolSet(DefaultTools()).Get("write_file")
	_ = os.WriteFile(filepath.Join(box.Root, "a.txt"), []byte("x"), 0o644)
	if _, err := write.Run(box, map[string]any{"path": "a.txt", "content": "y"}); err != nil {
		t.Fatalf("没有追踪器时不该拦: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// 被闸拦下 ≠ 干活失败.
//
// 界面把两者显示成同一句"N 个没成", 于是用户会去查一个不存在的故障;
// 看多了还会开始怀疑这些闸本身有毛病 —— 而它们恰恰是这个工作区里
// 最不该被怀疑的东西. 分类得从工具这一层就带出来.
func TestClobberRefusalIsBlockedNotFailed(t *testing.T) {
	box := seenBox(t)
	if err := os.WriteFile(filepath.Join(box.Root, "PLAN.md"), []byte("别人写的"), 0o644); err != nil {
		t.Fatal(err)
	}
	write, _ := NewToolSet(DefaultTools()).Get("write_file")
	_, err := write.Run(box, map[string]any{"path": "PLAN.md", "content": "我的"})
	if !isBlocked(err) {
		t.Fatalf("闸拦下的被当成了故障: %v", err)
	}
	// 而真正的故障不能被误标成"拦下"
	_, err = write.Run(box, map[string]any{"path": "../外面.md", "content": "x"})
	if err != nil && isBlocked(err) {
		t.Fatalf("越界写被标成了'按设计拦下', 会被当成正常: %v", err)
	}
}
