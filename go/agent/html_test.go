package agent

import (
	"strings"
	"testing"
)

// 属性里的 '>' 不是标签结束. 不认引号的话, 一个 title="a > b" 之后的
// 整段正文会被当成属性吞掉 —— 而且**页面看起来只是"少了一段"**, 不报任何错
func TestAttrQuoteDoesNotEatBody(t *testing.T) {
	got := htmlToText(`<div data-tip="甲 > 乙这半截还在属性里"><p>这段必须还在</p></div>`)
	if !strings.Contains(got, "这段必须还在") {
		t.Fatalf("属性里的 > 把正文吞了: %q", got)
	}
	// 不认引号的话, 标签会在属性中间那个 '>' 处提前结束,
	// 于是属性值的后半截被当成正文吐出来 —— 而这**不报任何错**,
	// 页面只是看起来多了几个碎片
	if strings.Contains(got, "这半截还在属性里") {
		t.Fatalf("属性值的后半截漏进正文了: %q", got)
	}
}

// 没闭合的 <script> 之后的东西本来也是脚本.
// 当成正文留着的话, 一整个 JS bundle 会被塞进上下文
func TestUnclosedScriptDropsRest(t *testing.T) {
	got := htmlToText(`<p>正文</p><script>var a = "</p>" ; while(1){}`)
	if !strings.Contains(got, "正文") {
		t.Fatalf("正文丢了: %q", got)
	}
	if strings.Contains(got, "while") {
		t.Fatalf("没闭合的脚本被当成正文留下了: %q", got)
	}
}

// 实体解码的顺序: &amp; 必须最后解.
// 先解它的话 "&amp;lt;" 会被两步解成 "<", 而原文想说的就是字面的 "&lt;" ——
// 这类错**静默改掉正文的意思**, 没有任何地方会报
func TestEntityOrder(t *testing.T) {
	if got := unescapeEntities("&amp;lt;"); got != "&lt;" {
		t.Fatalf("&amp;lt; 该解成字面的 &lt;, 得到 %q", got)
	}
	if got := unescapeEntities("a &amp; b &nbsp;c"); got != "a & b  c" {
		t.Fatalf("常见实体没解对: %q", got)
	}
}

// 块级结构要留 —— 全压成一行的话, 一个页面读起来就是一坨没有边界的词,
// 模型分不出哪一段是正文哪一段是导航
func TestBlockStructureKept(t *testing.T) {
	got := htmlToText(`<h1>标题</h1><ul><li>甲</li><li>乙</li></ul><p>尾巴</p>`)
	lines := strings.Split(got, "\n")
	if len(lines) < 4 {
		t.Fatalf("结构被压平了: %q", got)
	}
	if !strings.Contains(got, "- 甲") || !strings.Contains(got, "- 乙") {
		t.Fatalf("列表项没有标记: %q", got)
	}
}

// svg 里能塞几千个路径点, 每一个都是 token, 对理解页面的贡献是零
func TestSVGDropped(t *testing.T) {
	got := htmlToText(`<p>正文</p><svg><path d="M0 0 L1 1 L2 2 Z"/></svg>`)
	if strings.Contains(got, "M0 0") {
		t.Fatalf("svg 的路径数据留下来了: %q", got)
	}
	if !strings.Contains(got, "正文") {
		t.Fatalf("正文丢了: %q", got)
	}
}

// 注释里可能藏着整段被注释掉的旧版页面
func TestCommentDropped(t *testing.T) {
	got := htmlToText(`<p>新的</p><!-- <p>旧的</p> --><p>也是新的</p>`)
	if strings.Contains(got, "旧的") {
		t.Fatalf("注释内容被当成正文: %q", got)
	}
	if !strings.Contains(got, "新的") || !strings.Contains(got, "也是新的") {
		t.Fatalf("注释后面的正文丢了: %q", got)
	}
}

// 抽出来的东西要真的小 —— 这是这个函数存在的全部理由
func TestExtractionActuallyShrinks(t *testing.T) {
	page := `<!doctype html><html><head><title>T</title>` +
		`<style>` + strings.Repeat(".cls-9999{padding:0}", 500) + `</style>` +
		`<script>` + strings.Repeat("function f(){return 1};", 500) + `</script></head>` +
		`<body class="` + strings.Repeat("x", 2000) + `"><p>就这一句是正文。</p></body></html>`
	got := htmlToText(page)
	if len(got) > 200 {
		t.Fatalf("没压下来: 原始 %d 字节 → %d 字节\n%.200q", len(page), len(got), got)
	}
	if !strings.Contains(got, "就这一句是正文。") {
		t.Fatalf("把正文一起压没了: %q", got)
	}
}
