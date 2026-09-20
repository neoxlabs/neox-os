package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 路径不存在的时候, 宿主那句"在谁手上"要接到报错里 —— 见 Toolbox.Missing
func Test读不到的文件把在谁手上一并说出来(t *testing.T) {
	box := Toolbox{Root: t.TempDir(), Missing: func(rel string) string {
		return "小己 手上有 " + rel + "，还没合上来"
	}}
	read, _ := NewToolSet(DefaultTools()).Get("read_file")
	_, err := read.Run(box, map[string]any{"path": "app/server.js"})
	if err == nil {
		t.Fatal("读一个不存在的文件竟然成了")
	}
	if !strings.Contains(err.Error(), "小己") {
		t.Errorf("宿主知道它在谁手上, 报错里却一个字没说: %v", err)
	}
	// 原来那句也还在 —— 路径真写错的时候它仍然是对的
	if !strings.Contains(err.Error(), "list_dir") {
		t.Errorf("把原来的提示吞了: %v", err)
	}
	// 宿主答不上来的时候一个字都别多说
	quiet := Toolbox{Root: box.Root, Missing: func(string) string { return "" }}
	if _, err := read.Run(quiet, map[string]any{"path": "x.js"}); err == nil ||
		strings.Contains(err.Error(), "手上") {
		t.Errorf("答不上来却编了一句: %v", err)
	}
}

/**
 * **写一个空文件是正当的**.
 *
 *	缺省把空串当成"没给" —— 对 path、pattern 是对的, 对 content 不是.
 *	创建 tests/__init__.py 时, 错误信息可能是
 *
 *	    调 write_file 缺参数: content。…你这次给的是: content=, path=tests/__init__.py
 *
 *	一句自相矛盾的话(说缺, 又把它列出来了), 而它什么都没做错.
 */
func Test空文件写得出来(t *testing.T) {
	root := t.TempDir()
	write, _ := NewToolSet(DefaultTools()).Get("write_file")
	if err := write.Validate(map[string]any{"path": "tests/__init__.py", "content": ""}); err != nil {
		t.Fatalf("建一个空文件被拒了: %v", err)
	}
	if _, err := write.Run(Toolbox{Root: root, Seen: NewSeenFiles()},
		map[string]any{"path": "tests/__init__.py", "content": ""}); err != nil {
		t.Fatalf("空文件写不出来: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tests", "__init__.py")); err != nil {
		t.Errorf("说写成了, 文件却不在: %v", err)
	}

	// 真的没给 content 照旧要拒 —— 别把这条修成"什么都放行"
	if err := write.Validate(map[string]any{"path": "a.py"}); err == nil {
		t.Error("一个字都没给 content 也放行了")
	}
	// 空路径照旧要拒: 那跟空内容不是一回事
	if err := write.Validate(map[string]any{"path": "", "content": "x"}); err == nil {
		t.Error("空路径也放行了")
	}
}

/**
 * **"名字写错了"和"还没有这个东西"是两件事**.
 *
 *	账本里最高频的那几条报错是这个:
 *
 *	    PLAN.md: 文件不存在。别重试同一路径——先 list_dir 看看真实的文件名  ×5
 *	    CHARTER.md: 文件不存在。…                                    ×2
 *
 *	它按规矩开工先读章程, 而那个项目还没人建过章程 —— **路径一个字都
 *	没错**. 被那么一说, 它去 list_dir、去猜别的名字, 白花一步.
 */
func Test还没建跟名字写错要分开说(t *testing.T) {
	root := t.TempDir()
	read, _ := NewToolSet(DefaultTools()).Get("read_file")
	box := Toolbox{Root: root}

	// 上一级好好的, 只是这份东西还没人建
	_, err := read.Run(box, map[string]any{"path": "PLAN.md"})
	if err == nil {
		t.Fatal("读一个不存在的文件竟然成了")
	}
	if strings.Contains(err.Error(), "真实的文件名") {
		t.Errorf("路径一个字没错, 却说名字写错了: %v", err)
	}
	if !strings.Contains(err.Error(), "还没有") {
		t.Errorf("没说清是还没建: %v", err)
	}

	// 上一级都不在 —— 那多半真是路径不对, 原来那句话才对
	_, err = read.Run(box, map[string]any{"path": "谁知道在哪/x.md"})
	if err == nil || !strings.Contains(err.Error(), "list_dir") {
		t.Errorf("上一级都没有的时候该让它去看看: %v", err)
	}
}
