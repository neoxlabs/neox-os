package osinit

import (
	"runtime"
	"testing"
)

// 自检至少要能认出 git —— 这个仓库本身就是 git 管的, 跑测试的机器上
// 必然有. 认不出说明查的路子坏了, 而不是"这台机器真没有".
func TestCheckToolchainFindsGit(t *testing.T) {
	got := CheckToolchain()
	if got.Platform != runtime.GOOS {
		t.Fatalf("平台报的是 %q, 该是 %q", got.Platform, runtime.GOOS)
	}
	for _, tool := range got.Tools {
		if tool.Name != "git" {
			continue
		}
		if !tool.Have {
			t.Fatal("查不到 git —— 跑测试的机器不可能没有它")
		}
		if tool.Path == "" {
			t.Fatal("说有 git 却给不出路径")
		}
		if !tool.Core {
			t.Fatal("git 必须是 core —— 缺了它工件就没有归处")
		}
		return
	}
	t.Fatal("自检清单里根本没查 git")
}

// 每一条都得有说辞和名字, 缺的还得给得出装法 ——
// 界面上只写"python3 缺失"而不说为什么要它、怎么装, 等于没提示.
func TestCheckToolchainEveryToolExplainsItself(t *testing.T) {
	for _, tool := range CheckToolchain().Tools {
		if tool.Name == "" || tool.What == "" {
			t.Fatalf("%+v: 名字和用途都不能空", tool)
		}
		// curl 在 mac 上是系统自带的, 给不出 brew 之外的通用解法 ——
		// 那一条空着是有意的, 别的都得有
		if !tool.Have && tool.Fix == "" && !(tool.Name == "curl" && runtime.GOOS == "darwin") {
			t.Fatalf("%s 缺着却不说怎么装", tool.Name)
		}
	}
}
