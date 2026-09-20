package agent

import (
	"strings"
	"testing"
)

// streamOf 把一段话按每 n 个字节切成增量喂进去, 收回屏幕上拼出来的字.
func streamOf(s string, n int) string {
	var got strings.Builder
	lw := newLiveUnwrap(func(t string) { got.WriteString(t) })
	for i := 0; i < len(s); i += n {
		j := i + n
		if j > len(s) {
			j = len(s)
		}
		lw.Write(s[i:j])
	}
	lw.Close()
	return got.String()
}

// **怎么切都得拼出同一句** —— 供应商切增量的位置不固定, 会切在转义中间、
// 起手式中间, 甚至半个汉字上
func TestLiveUnwrapAnyChunking(t *testing.T) {
	cases := map[string]string{
		// 线上原文(08:08 那条的开头)
		`{"said":"你说得对，是我错了。\n\n坐标是**照着手机记的**"}`: "你说得对，是我错了。\n\n坐标是**照着手机记的**",
		// 起手式里有空白
		"{ \"reply\" : \"好的\"}": "好的",
		// \u 转义和引号转义
		`{"said":"你好，他说\"到了\""}`: `你好，他说"到了"`,
		// 包了两层
		`{"said":"{\"said\":\"蚌埠现在多云\"}"}`: "蚌埠现在多云",
		// 普通回答原样
		"就是一句话。":        "就是一句话。",
		"  前面有空白":       "  前面有空白",
		`{"a":1,"b":2}`: `{"a":1,"b":2}`,
		"{不是 json":      "{不是 json",
		"好":             "好",
		// 输出被截断, 没收尾 —— 拆出来的照样给
		`{"said":"行，不扯了`: "行，不扯了",
	}
	for in, want := range cases {
		for _, n := range []int{1, 2, 3, 5, 7, 64} {
			if got := streamOf(in, n); got != want {
				t.Errorf("每 %d 字节一片: %q\n→ %q\n要 %q", n, in, got, want)
			}
		}
	}
}

// 收尾之后的 "} 以及更后面的东西不许漏到屏幕上
func TestLiveUnwrapStopsAtClosingQuote(t *testing.T) {
	if got := streamOf(`{"said":"到了"}   `, 1); got != "到了" {
		t.Fatalf("%q", got)
	}
}

// 收尾引号之后模型接着说的人话不该被吃掉 —— 原来 lwDone 之后一律丢
func TestLiveUnwrapKeepsTextAfterEnvelope(t *testing.T) {
	var got strings.Builder
	u := newLiveUnwrap(func(s string) { got.WriteString(s) })
	u.Write(`{"said":"到公司了`)
	u.Write(`"`) // 收尾引号
	u.Write(`}`) // 结构字符: 扔
	u.Write("\n\n还有一句他该看到的话")
	u.Close()
	if !strings.Contains(got.String(), "到公司了") {
		t.Fatalf("信封里的话没拆出来: %q", got.String())
	}
	if !strings.Contains(got.String(), "还有一句他该看到的话") {
		t.Fatalf("收尾之后的话被吃了: %q", got.String())
	}
	if strings.Contains(got.String(), "}") {
		t.Fatalf("结构字符漏出去了: %q", got.String())
	}
}
