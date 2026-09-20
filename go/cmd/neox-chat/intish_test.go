package main

import "testing"

// 给人看的整数不许打成科学计数法.
//
// 过了一趟 JSON 之后数字回来全是 float64, 而 %v 对 float64 用的是 %g ——
// 值一大就变成指数形式. 窗口从 128000 变成 1048576 之后, 用量那一行
// 当场变成 `窗口=1.048576e+06 tok … → 2.014118e+06 字节`.
func TestBigNumbersPrintAsIntegers(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{float64(1048576), "1048576"}, // 真实窗口
		{float64(2014118), "2014118"}, // 真实预算字节数
		{float64(128000), "128000"},   // 小数也别退化
		{float64(0), "0"},
		{int64(1048576), "1048576"},
		{int(42), "42"},
	}
	for _, c := range cases {
		if got := intish(c.in); got != c.want {
			t.Fatalf("intish(%v) = %q, 要 %q", c.in, got, c.want)
		}
	}
	// 不是数字就原样打, 别吞掉
	if got := intish("abc"); got != "abc" {
		t.Fatalf("非数字被吞了: %q", got)
	}
	if got := intish(nil); got == "" {
		t.Fatal("nil 打成了空串 —— 那是另一种'看不见'")
	}
}
