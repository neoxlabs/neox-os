package agent

import (
	"strings"
)

// HTML 抽正文 —— 用状态机扫一遍, 不引解析库.
//
// ── 为什么手写而不是拉一个库 ──
//
// 这个仓库只依赖 golang.org/x/sys(confine 要的). 为"把标签去掉"多背一个
// 依赖不值当: 我们要的不是一棵正确的 DOM, 是**给模型看的正文**, 而模型
// 对残缺的标签结构完全不敏感 —— 它敏感的是 script/style 里那几万个字节
// 有没有被扔掉.
//
// ── 判据: 扔掉的比留下的重要 ──
//
// 一个普通网页里真正的正文常常不到 5%. 所以这里的重点不是"抽得准",
// 是**该扔的一定扔掉**:
//
//	script / style / noscript / svg / template   整块内容都扔
//	head 里的东西                                  只留 <title>
//	所有属性                                       全扔 (class 名比正文还长)
//
// 剩下的部分保留块级结构(段落之间空行、li 前面加 -), 因为那是模型判断
// "哪一段是正文哪一段是导航"的唯一线索 —— 全压成一行的话, 一个页面读起来
// 就是一坨没有边界的词.
func htmlToText(src string) string {
	var b strings.Builder
	b.Grow(len(src) / 4)

	i := 0
	for i < len(src) {
		c := src[i]
		if c != '<' {
			// 正文字节. 连续空白压成一个空格 —— HTML 源码里的缩进
			// 会让每一段前面挂十几个空格
			if isSpace(c) {
				writeSpace(&b)
				i++
				continue
			}
			b.WriteByte(c)
			i++
			continue
		}

		// 注释: <!-- … -->. 里面可能藏着整段被注释掉的旧版页面
		if strings.HasPrefix(src[i:], "<!--") {
			if end := strings.Index(src[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
				continue
			}
			break // 没闭合, 后面全是注释, 不要了
		}

		name, closing, next := tagAt(src, i)
		if name == "" {
			// 不是标签, 是正文里的一个裸 '<'
			b.WriteByte('<')
			i++
			continue
		}

		// 整块丢弃的标签 —— 内容也一起丢
		if !closing && dropWhole[name] {
			i = skipBlock(src, next, name)
			continue
		}
		if blockTag[name] {
			writeBreak(&b)
		}
		if !closing && name == "li" {
			b.WriteString("- ")
		}
		i = next
	}

	return cleanup(b.String())
}

// dropWhole 这些标签连内容一起扔.
//
// svg/template 是后来补的: 一个图标库能在 <svg> 里塞几千个路径点,
// 那些数字每一个都是 token, 而它们对理解页面的贡献是零.
//
// **head 不在这张表里**, 尽管它整块都是元数据: 里面躺着 <title>,
// 而标题常常是判断"这个页面是不是我要找的东西"最快的一句话.
// head 里真正费字节的是 script/style, 它们自己就在表里.
var dropWhole = map[string]bool{
	"script": true, "style": true, "noscript": true,
	"svg": true, "template": true, "iframe": true,
}

// blockTag 会造成换行的标签. 不求全 —— 漏一个的后果只是两段粘在一起
var blockTag = map[string]bool{
	"p": true, "div": true, "br": true, "hr": true, "section": true,
	"article": true, "header": true, "footer": true, "nav": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "tr": true, "table": true,
	"blockquote": true, "pre": true, "figure": true, "aside": true, "main": true,
	"title": true, "td": true, "th": true, "dt": true, "dd": true,
}

// tagAt 认一个标签, 返回标签名(小写)、是不是闭合标签、以及标签后面的位置.
// 认不出来时 name 为空 —— 那说明这个 '<' 是正文里的字符
func tagAt(src string, i int) (name string, closing bool, next int) {
	j := i + 1
	if j < len(src) && src[j] == '/' {
		closing = true
		j++
	}
	start := j
	for j < len(src) && isNameByte(src[j]) {
		j++
	}
	if j == start {
		return "", false, i + 1
	}
	name = strings.ToLower(src[start:j])
	// 跳到 '>' —— 中间是属性, 一概不要.
	//
	// **属性里的引号要认**: 一个 title="a > b" 里的 '>' 不是标签结束,
	// 不认引号的话后面整段正文会被当成属性吞掉
	var quote byte
	for j < len(src) {
		ch := src[j]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
		} else if ch == '"' || ch == '\'' {
			quote = ch
		} else if ch == '>' {
			return name, closing, j + 1
		}
		j++
	}
	return name, closing, len(src)
}

// skipBlock 跳过 <name> … </name> 整块.
//
// 找不到闭合标签时**丢掉后面全部** —— 一个没闭合的 <script> 之后的东西
// 本来也是脚本. 反过来(当成正文留着)会把一整个 JS bundle 塞进上下文.
func skipBlock(src string, from int, name string) int {
	closing := "</" + name
	rest := strings.ToLower(src[from:])
	k := strings.Index(rest, closing)
	if k < 0 {
		return len(src)
	}
	// 跳过闭合标签本身
	j := from + k
	for j < len(src) && src[j] != '>' {
		j++
	}
	return j + 1
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func writeSpace(b *strings.Builder) {
	s := b.String()
	if s == "" {
		return
	}
	if last := s[len(s)-1]; last == ' ' || last == '\n' {
		return
	}
	b.WriteByte(' ')
}

func writeBreak(b *strings.Builder) {
	s := b.String()
	if s == "" {
		return
	}
	if strings.HasSuffix(s, "\n") {
		return
	}
	b.WriteByte('\n')
}

// cleanup 收尾: 解实体、把空行压到最多一个、去掉行首尾空白.
//
// 行内空白已经在扫描时压过了, 这里管的是**行与行之间** ——
// 一个导航栏能产出几十个只有空白的行, 它们在上下文里跟正文一样贵.
func cleanup(s string) string {
	s = unescapeEntities(s)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || ln == "-" { // 空的 <li> 只剩一个横杠, 那不是内容
			blank++
			continue
		}
		if blank > 0 && len(out) > 0 {
			out = append(out, "")
		}
		blank = 0
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// unescapeEntities 只解常见的那几个 + 数字实体.
//
// 不做全表: 剩下的实体在正文里出现的概率极低, 而**原样留着比解错好** ——
// 解错会静默改掉正文的意思, 留着最多是一句 &hellip; 看着别扭.
func unescapeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	// &amp; 放最后: 先解它的话 "&amp;lt;" 会被两步解成 "<", 而原文
	// 想说的就是字面的 "&lt;"
	r := strings.NewReplacer(
		"&nbsp;", " ", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&apos;", "'", "&mdash;", "—", "&ndash;", "–",
		"&hellip;", "…", "&#x27;", "'", "&#x2F;", "/", "&amp;", "&",
	)
	return r.Replace(s)
}
