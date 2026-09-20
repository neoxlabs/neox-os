package main

import (
	"path/filepath"
	"testing"
)

/**
 * **删掉的 bot 不该自己长回来**.
 *
 *	内置那几个(研究/值守/构建/发版/回归/文案)的身份写在代码里, 删掉只是
 *	杀了进程和账本 —— 下次开机它们照样长回来. 用户的原话:
 *	"让你把所有的 BOT 都清掉…你还是没有清".
 *
 *	他说得对: 从他的角度这就是没清掉. 而且这种"清了又回来"最费解,
 *	因为界面上确实清空过一次.
 */
func Test删过的不再自己起(t *testing.T) {
	r := &botRoster{path: filepath.Join(t.TempDir(), "bots.json")}
	if err := r.remove(map[string]bool{"研究": true, "值守": true}); err != nil {
		t.Fatal(err)
	}
	buried := r.buried()
	if !buried["研究"] || !buried["值守"] {
		t.Fatalf("墓碑没立上: %+v", buried)
	}
	// 没删过的不受影响
	if buried["构建"] {
		t.Error("把没删的也埋了")
	}
}

// 他又建了一个同名的, 那就是想要它回来 —— 墓碑得撤掉
func Test又建一个同名的墓碑要撤(t *testing.T) {
	r := &botRoster{path: filepath.Join(t.TempDir(), "bots.json")}
	if err := r.remove(map[string]bool{"研究": true}); err != nil {
		t.Fatal(err)
	}
	if err := r.add(savedBot{Name: "研究", Work: "/AI/x"}); err != nil {
		t.Fatal(err)
	}
	if r.buried()["研究"] {
		t.Fatal("建回来了墓碑还在 —— 那它还是起不来")
	}
	// 而且名册里只有一条, 不是墓碑 + 新的两条
	if got := r.load(); len(got) != 1 || got[0].Work != "/AI/x" {
		t.Fatalf("名册脏了: %+v", got)
	}
}

/**
 * **墓碑不是 bot**.
 *
 *	删掉的名字在名册里留一行 {name, gone:true}. 装回"界面上建过的 bot"
 *	那个循环如果照单全收 —— 六块墓碑就会被当成六个 bot 起回来,
 *	名字还是那几个, 身份却是空的. 清空之后重启,
 *	六条会话原样长回来, 而名册里明明写着 gone.
 */
func Test墓碑不该被当成bot起回来(t *testing.T) {
	r := &botRoster{path: filepath.Join(t.TempDir(), "bots.json")}
	if err := r.add(savedBot{Name: "小登", Work: "/AI/oa"}); err != nil {
		t.Fatal(err)
	}
	if err := r.remove(map[string]bool{"研究": true}); err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, saved := range r.load() {
		if saved.Builtin || saved.Gone {
			continue
		}
		live++
		if saved.Name != "小登" {
			t.Errorf("起了个不该起的: %+v", saved)
		}
	}
	if live != 1 {
		t.Fatalf("该起 1 个, 起了 %d 个", live)
	}
}
