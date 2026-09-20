package agent

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// liveUnwrap 边收增量边拆信封 —— **流式那一路的 unwrapEnvelope**.
//
// ── 为什么最终那条拆了还不够 ──
//
//	unwrapEnvelope 只作用在最后那条 phase:reply 上. 而流式的时候, 屏幕上
//	那条消息是增量一个个拼起来的 —— 于是在最后那条替换上来之前, 他看到的
//	是 `{"said":"你说得对，是我错了。\n\n凤凰…` 一个字一个字往外长,
//	换行还是字面的 \n. 语音模式更糟: 播报是跟着增量走的.
//
// ── 怎么拆 ──
//
//	开头先攒着不发, 直到看得出是不是信封的起手式({"said":" 这种, 允许
//	中间有空白). 不是就原样放行, 从此不再管; 是就进入"字符串里"的状态,
//	边收边还原 JSON 转义, 碰到收尾的那个引号就停(后面的 "} 丢掉).
//	拆出来的字交给**下一层同样的东西** —— 包了两层的线上见过.
//
//	开头最多攒三十来个字节, 人看不出这点延迟.
type liveUnwrap struct {
	emit  func(string)
	mode  int // 0 还在判断 / 1 放行 / 2 字符串里 / 3 收尾之后
	head  strings.Builder
	esc   bool
	uhex  []byte // \uXXXX 攒到一半
	carry []byte // 切在一个 UTF-8 字符中间的那半截
	inner *liveUnwrap
	depth int
}

const (
	lwDeciding = iota
	lwPass
	lwString
	lwDone
)

func newLiveUnwrap(emit func(string)) *liveUnwrap {
	return &liveUnwrap{emit: emit}
}

// Write 喂一段增量.
func (u *liveUnwrap) Write(s string) {
	switch u.mode {
	case lwPass:
		u.emit(s)
	case lwString:
		u.feed(s)
	case lwDeciding:
		u.head.WriteString(s)
		u.decide(false)
	case lwDone:
		// ── 收尾引号之后**不是一律扔** ──
		//
		//	原来 lwDone 之后的每一块增量都被静默丢弃. 信封收尾那几个字
		//	(`"}`、`,"note":…`)确实该扔, 但模型接着说的**人话**不该 ——
		//	那是他再也看不到的一段字, 而且没有任何一处说得出为什么.
		//
		//	**结构字符先扔, 再谈转交**: 反过来的话, 里层多半正处在
		//	"原样放行"状态, 那个 `}` 就从它嘴里出去了(拆两层信封时
		//	屏幕上是"多云}}").
		if onlyEnvelopeTail(s) {
			return
		}
		if u.inner != nil {
			u.inner.Write(s)
			return
		}
		u.emit(s)
	}
}

// onlyEnvelopeTail 这一块是不是只有信封收尾那几个字符
func onlyEnvelopeTail(s string) bool {
	return strings.TrimLeft(s, " \t\r\n\"}],:") == ""
}

// Close 流结束. 还在判断中的(整段回答短得不到起手式那么长)原样放出去.
func (u *liveUnwrap) Close() {
	if u.mode == lwDeciding {
		u.decide(true)
	}
	if u.inner != nil {
		u.inner.Close()
	}
}

// decide 看攒下的开头是不是信封. final = 不会再有更多了.
func (u *liveUnwrap) decide(final bool) {
	buf := u.head.String()
	t := strings.TrimLeft(buf, " \t\r\n")
	if t == "" {
		if final && buf != "" {
			u.emit(buf)
		}
		return
	}
	if t[0] != '{' {
		u.pass(buf)
		return
	}
	if rest, ok := envelopeHead(t); ok {
		u.mode = lwString
		if u.depth < 2 {
			u.inner = &liveUnwrap{emit: u.emit, depth: u.depth + 1}
		}
		u.feed(rest)
		return
	}
	if final || !couldBeHead(t) {
		u.pass(buf)
	}
}

func (u *liveUnwrap) pass(buf string) {
	u.mode = lwPass
	if buf != "" {
		u.emit(buf)
	}
}

// envelopeHead t 以信封起手式开头的话, 返回起手式后面剩下的.
func envelopeHead(t string) (string, bool) {
	c := compactHead(t)
	for _, key := range envelopeKeys {
		head := `{"` + key + `":"`
		if strings.HasPrefix(c.s, head) {
			return t[c.cut(len(head)):], true
		}
	}
	return "", false
}

// couldBeHead t 还可能长成起手式吗(它是某个起手式的前缀).
func couldBeHead(t string) bool {
	c := compactHead(t).s
	for _, key := range envelopeKeys {
		head := `{"` + key + `":"`
		if strings.HasPrefix(head, c) {
			return true
		}
	}
	return false
}

// compacted 去掉 JSON 允许的空白之后的开头, 以及它跟原文的下标对照.
type compacted struct {
	s   string
	idx []int // 去空白后第 i 个字节在原文里的下标
}

func compactHead(t string) compacted {
	var c compacted
	var b strings.Builder
	for i := 0; i < len(t) && b.Len() < 24; i++ {
		switch t[i] {
		case ' ', '\t', '\r', '\n':
			continue
		}
		b.WriteByte(t[i])
		c.idx = append(c.idx, i)
	}
	c.s = b.String()
	return c
}

// cut 去空白后前 n 个字节, 在原文里到哪儿为止.
func (c compacted) cut(n int) int { return c.idx[n-1] + 1 }

// feed 字符串里的字: 还原转义, 碰到收尾的引号就停.
func (u *liveUnwrap) feed(s string) {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case u.uhex != nil:
			u.uhex = append(u.uhex, ch)
			if len(u.uhex) == 4 {
				if r, err := strconv.ParseUint(string(u.uhex), 16, 32); err == nil {
					out.WriteRune(rune(r))
				} else {
					out.WriteString(`\u` + string(u.uhex))
				}
				u.uhex = nil
			}
		case u.esc:
			u.esc = false
			switch ch {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'r':
				out.WriteByte('\r')
			case '"', '\\', '/':
				out.WriteByte(ch)
			case 'u':
				u.uhex = []byte{}
			default:
				// 认不得就原样留着 —— 同 unescapeLoose
				out.WriteByte('\\')
				out.WriteByte(ch)
			}
		case ch == '\\':
			u.esc = true
		case ch == '"':
			u.mode = lwDone
			u.out(out.String())
			return
		default:
			out.WriteByte(ch)
		}
	}
	u.out(out.String())
}

// out 拆出来的字交出去 —— 有下一层就给下一层(包了两层的情况).
// 切在半个汉字上的留到下一次: 半个字发出去, 屏幕上就是一个问号.
func (u *liveUnwrap) out(s string) {
	if len(u.carry) > 0 {
		s = string(u.carry) + s
		u.carry = nil
	}
	if n := incompleteTail(s); n > 0 {
		u.carry = []byte(s[len(s)-n:])
		s = s[:len(s)-n]
	}
	if s == "" {
		return
	}
	if u.inner != nil {
		u.inner.Write(s)
		return
	}
	u.emit(s)
}

// incompleteTail 结尾有几个字节是一个还没收全的 UTF-8 字符.
func incompleteTail(s string) int {
	for n := 1; n <= 3 && n <= len(s); n++ {
		if utf8.RuneStart(s[len(s)-n]) {
			if !utf8.FullRuneInString(s[len(s)-n:]) {
				return n
			}
			return 0
		}
	}
	return 0
}
