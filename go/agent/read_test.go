package agent

import (
	"fmt"
	"strings"
	"testing"
)

func manyLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "第%d行\n", i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// 小文件必须原样返回 —— 一个字节的额外噪音都不能加.
// 编辑按内容精确匹配, 模型必须能直接使用读取到的原文, 额外字节会让匹配失败.
func TestSmallFileVerbatim(t *testing.T) {
	src := "# 标题\n\n正文一\n正文二"
	if got := readSlice("a.md", src, 0); got != src {
		t.Fatalf("小文件被改动了:\n%q", got)
	}
}

// 截断必须说出来, 而且要给出接着读的确切办法.
// 静默截断最糟: 模型以为读完了, 然后基于半截内容下结论.
func TestTruncationIsLoud(t *testing.T) {
	got := readSlice("big.txt", manyLines(1000), 0)
	if !strings.Contains(got, "共 1000 行") {
		t.Fatalf("没告诉它文件总共多长: %.120q", got)
	}
	if !strings.Contains(got, "offset=401") {
		t.Fatalf("没给出接着读的确切办法: %.200q", got)
	}
	if strings.Contains(got, "第500行") {
		t.Fatal("超出上限还在往外吐")
	}
	if !strings.Contains(got, "第400行") || !strings.Contains(got, "第1行") {
		t.Fatal("该给的行没给全")
	}
}

// 按提示接着读要能真接上, 不重不漏
func TestOffsetContinuesExactly(t *testing.T) {
	got := readSlice("big.txt", manyLines(1000), 401)
	if strings.Contains(got, "第400行") {
		t.Fatal("重了 —— 第二片不该包含第一片的内容")
	}
	if !strings.Contains(got, "第401行") {
		t.Fatal("漏了 —— 第二片必须从 401 开始")
	}
}

// 读到末尾要明说"到此为止", 否则模型会一直往后翻
func TestLastSliceSaysEnd(t *testing.T) {
	got := readSlice("big.txt", manyLines(500), 401)
	if !strings.Contains(got, "到文件末尾") {
		t.Fatalf("没说读到头了, 模型会一直翻: %.120q", got)
	}
}

// offset 越界不是错误, 是"你已经读完了" —— 报错会让它重试
func TestOffsetPastEndIsNotAnError(t *testing.T) {
	got := readSlice("a.txt", manyLines(10), 99)
	if !strings.Contains(got, "读完了") {
		t.Fatalf("越界该告诉它已读完: %q", got)
	}
}

// 超长单行也要挡住 —— 按行数算的上限对 minified 文件完全无效
func TestByteCapCatchesLongLines(t *testing.T) {
	huge := strings.Repeat("x", 200*1024)
	got := readSlice("bundle.js", huge+"\nsecond", 0)
	if len(got) > readMaxBytes+2048 {
		t.Fatalf("单行超长没被字节上限挡住: %d 字节", len(got))
	}
}

// 二进制不能进上下文
func TestBinaryDetected(t *testing.T) {
	if !isBinary([]byte{0x7f, 'E', 'L', 'F', 0x00, 0x01}) {
		t.Fatal("ELF 该被认出来")
	}
	if isBinary([]byte("中文文本 UTF-8 完全正常")) {
		t.Fatal("中文文本被误判成二进制了")
	}
}

// 模型经常把数字写成字符串
func TestArgIntAcceptsString(t *testing.T) {
	if argInt(map[string]any{"offset": "401"}, "offset") != 401 {
		t.Fatal("字符串形式的数字要收")
	}
	if argInt(map[string]any{"offset": float64(7)}, "offset") != 7 {
		t.Fatal("JSON 数字要收")
	}
}
