package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func searchDir(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, body := range files {
		full := filepath.Join(d, name)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func mustSearch(t *testing.T, dir, pat string) string {
	t.Helper()
	re := regexp.MustCompile(pat)
	hits, scanned, trunc, err := runSearch(Toolbox{}, dir, "site", re)
	if err != nil {
		t.Fatal(err)
	}
	return formatHits(pat, "site", hits, scanned, trunc)
}

// 行号必须能直接喂给 read_file 的 offset —— 搜索定位 + 分片读细节,
// 两个工具接在一起才闭环. 少了行号就又得把整个文件读进来.
func TestHitsCarryLineNumbers(t *testing.T) {
	body := strings.Repeat("ok\n", 776) + "FATAL 磁盘满了\n" + strings.Repeat("ok\n", 200)
	out := mustSearch(t, searchDir(t, map[string]string{"big.log": body}), "FATAL")
	if !strings.Contains(out, "site/big.log:777:") {
		t.Fatalf("行号不对, 模型没法接着 read_file:\n%s", out)
	}
}

// 搜出来的只能是匹配行, 不能把整个文件带出来 —— 那等于没做
func TestOnlyMatchingLinesReturned(t *testing.T) {
	body := "第一行无关\n命中 TARGET 了\n第三行无关\n"
	out := mustSearch(t, searchDir(t, map[string]string{"a.txt": body}), "TARGET")
	if strings.Contains(out, "第一行无关") || strings.Contains(out, "第三行无关") {
		t.Fatalf("把不匹配的行也带出来了:\n%s", out)
	}
}

// 截断必须说出来, 否则模型会把"只有这些"当成结论
func TestSearchTruncationIsLoud(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("HIT\n")
	}
	out := mustSearch(t, searchDir(t, map[string]string{"a.txt": b.String()}), "HIT")
	if !strings.Contains(out, "还有匹配没列出来") {
		t.Fatalf("截断没说出来:\n%.300s", out)
	}
	if strings.Count(out, "\n") > searchMaxTotal+4 {
		t.Fatal("超过上限还在往外吐")
	}
}

// 一个文件不能淹掉其它文件的匹配
func TestPerFileCapLeavesRoomForOthers(t *testing.T) {
	var noisy strings.Builder
	for i := 0; i < 200; i++ {
		noisy.WriteString("HIT 噪音\n")
	}
	out := mustSearch(t, searchDir(t, map[string]string{
		"a_noisy.log": noisy.String(),
		"b_quiet.log": "HIT 这条不能被淹掉\n",
	}), "HIT")
	if !strings.Contains(out, "这条不能被淹掉") {
		t.Fatalf("一个吵闹的文件把别的文件淹了:\n%.400s", out)
	}
}

// 没找到也要给下一步 —— 只说"没找到"模型会原样再搜一次
func TestNoMatchGivesNextStep(t *testing.T) {
	out := mustSearch(t, searchDir(t, map[string]string{"a.txt": "什么都没有\n"}), "ZZZ")
	if !strings.Contains(out, "别原样重搜") {
		t.Fatalf("没告诉它下一步该怎么办:\n%s", out)
	}
}

// 坏正则要说清怎么改 —— 模型最常犯的是把字面量当正则写
func TestBadRegexIsActionable(t *testing.T) {
	tool := searchTool()
	_, err := tool.Run(Toolbox{Root: t.TempDir()}, map[string]any{"pattern": "a(b"})
	if err == nil {
		t.Fatal("坏正则该报错")
	}
	if !strings.Contains(err.Error(), "转义") {
		t.Fatalf("没告诉它怎么改: %v", err)
	}
}

// 二进制不搜 —— 匹配上了对模型也没意义, 还会吐一堆乱码
func TestBinarySkipped(t *testing.T) {
	d := searchDir(t, map[string]string{"a.txt": "HIT 文本\n"})
	os.WriteFile(filepath.Join(d, "b.bin"), append([]byte{0, 1, 2}, []byte("HIT")...), 0o644)
	out := mustSearch(t, d, "HIT")
	if strings.Contains(out, "b.bin") {
		t.Fatalf("二进制被搜进来了:\n%s", out)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatal("文本文件反而漏了")
	}
}

// 遍历顺序必须稳定 —— 不稳定会让截断位置每次不同, 前缀缓存跟着废
func TestSearchOrderStable(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"z.txt", "a.txt", "m.txt", "b.txt", "y.txt"} {
		files[n] = "HIT\n"
	}
	d := searchDir(t, files)
	first := mustSearch(t, d, "HIT")
	for i := 0; i < 20; i++ {
		if mustSearch(t, d, "HIT") != first {
			t.Fatal("同一次搜索结果顺序不稳定")
		}
	}
}

// search 是只读的 —— 否则并行编排会把它当写操作退回串行
func TestSearchIsReadOnly(t *testing.T) {
	if !searchTool().ReadOnly() {
		t.Fatal("search 必须是只读的, 否则并行会被退回串行")
	}
	// 停滞检测按**声明**判会不会改动世界, 不按名字猜 —— 名字前缀那套
	// 已经漏过一次 (run 被判成只读)
	if searchTool().Mutates {
		t.Fatal("search 被声明成会改动世界, 停滞检测会判错")
	}
}

// 目录太大没搜完时, **绝不能说成"没找到"**.
//
// 典型失败是: 在 423MB 的目录树里搜一个不存在的词, 处理只完成一部分却答
// "site 里没有这个名字" —— 可它只看了 2000 个文件就停了.
// "搜了 N 个没找到"会被模型读成"不存在", 而那个结论完全不成立.
func TestUnfinishedSearchNeverSaysNotFound(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < searchMaxFiles+200; i++ {
		files[fmt.Sprintf("d%d/f%d.txt", i/50, i)] = "无关内容\n"
	}
	out := mustSearch(t, searchDir(t, files), "ZZZ_不存在")
	if !strings.Contains(out, "没搜完") {
		t.Fatalf("没说清没搜完, 模型会当成'不存在':\n%s", out)
	}
	if !strings.Contains(out, "别据此下") {
		t.Fatalf("没拦住它下错误结论:\n%s", out)
	}
}

// 真搜完了就要明说"全部" —— 否则模型对每个"没找到"都不敢下结论
func TestFinishedSearchSaysSoExplicitly(t *testing.T) {
	out := mustSearch(t, searchDir(t, map[string]string{"a.txt": "什么都没有\n"}), "ZZZ")
	if !strings.Contains(out, "全部") {
		t.Fatalf("搜完了却没说清, 模型会以为还有没看的:\n%s", out)
	}
}
