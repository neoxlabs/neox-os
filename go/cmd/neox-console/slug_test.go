package main

import "testing"

// TestSlugNeverCollides 一名一目录.
//
//	两个 bot 共用一个工作目录 = 领地互斥被破坏, 症状是随机的文件损坏:
//	一个的 edit 改掉另一个正在读的文件, 而它们各自都以为自己是
//	那儿唯一的人. 这个闸就是防它.
func TestSlugNeverCollides(t *testing.T) {
	names := []string{
		"测试员", "写文档", "研究", "值守", "构建", "盯 CI",
		"release", "QA", "qa", "", "  ", "同一个名字",
	}
	seen := map[string]string{}
	for _, name := range names {
		got := slug(name)
		if before, dup := seen[got]; dup && before != name {
			t.Fatalf("撞了: %q 和 %q 都是 %q", before, name, got)
		}
		seen[got] = name
	}
}

// TestSlugIsStable 同一个名字每次都是同一个目录 —— 否则重启一次
// 它就找不到自己上次写的东西了
func TestSlugIsStable(t *testing.T) {
	for _, name := range []string{"测试员", "release", "盯 CI"} {
		if slug(name) != slug(name) {
			t.Fatalf("%q 的目录名不稳定", name)
		}
	}
}

// TestSlugKeepsReadableHead ASCII 名字要看得出是谁 —— 全哈希的话
// 在 Finder 里没人认得出哪个目录是哪个 bot
func TestSlugKeepsReadableHead(t *testing.T) {
	if got := slug("release"); got[:8] != "release-" {
		t.Fatalf("可读部分丢了: %q", got)
	}
}

// TestBuiltinRosterEntriesAreNotSpawned 名册里 builtin 的条目只覆盖房间归属.
//
//	拿它去起进程会起出一个 bot 标签不同的同名分身 —— 界面上同一个人
//	出现两次, 两个都活着各说各的. 这种重复进程会让状态互相干扰.
func TestBuiltinRosterEntriesAreNotSpawned(t *testing.T) {
	entries := []savedBot{
		{Name: "值守", Builtin: true},
		{Name: "小记"},
	}
	spawned := []string{}
	for _, saved := range entries {
		if saved.Builtin {
			continue
		}
		spawned = append(spawned, saved.Name)
	}
	if len(spawned) != 1 || spawned[0] != "小记" {
		t.Fatalf("内置条目被拿去起进程了: %v", spawned)
	}
	// 但归属还得认
	rooms := map[string]string{}
	for _, saved := range entries {
		rooms[saved.Name] = saved.Thread
	}
	if _, ok := rooms["值守"]; !ok {
		t.Fatal("内置条目的房间归属丢了")
	}
}
