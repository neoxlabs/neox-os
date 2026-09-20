package agent

import (
	"fmt"
	"strings"
	"unicode"
)

// 他说过没有 —— **写他的数据之前核对一下**.
//
// ── 真机 08:49 那一轮 ──
//
//	他问: "现在几点？我设了哪些提醒？你都记着哪些地方？"
//	它做了: 加待办"上课"、加待办"买奶"、立了一条来电拦截, raw 填的是
//	"我上课呢接不了电话，你先给我拦住". 回了一句"行。来电我盯着"。
//
//	那句话他从来没说过, 仓库和提示词里也找不到 —— 纯粹是模型编的, 而且
//	**写进了他的数据**: 待办里多了两条, 来电从此会被"拦下". 他只好把问题
//	再问一遍.
//
//	提示词里已经写着"只记他明确交代的". 那是要求, 不是保证. 保证要落在
//	写数据的那一刻: raw 字段本来就要求"他的原话一字不改", 那就真的去比.

const heardKeep = 6

// hear 记下他说的一句.
func (a *Agent) hear(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.heard = append(a.heard, text)
	if len(a.heard) > heardKeep {
		a.heard = a.heard[len(a.heard)-heardKeep:]
	}
}

func (a *Agent) recentHeard() []string {
	out := make([]string, len(a.heard))
	copy(out, a.heard)
	return out
}

// normalize 只留字和数字 —— 标点、空格、全半角不算"改了原话".
func normalize(s string) []rune {
	var out []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out = append(out, r)
		}
	}
	return out
}

// quotedFrom raw 是不是出自他说过的某一句.
//
//	模型会顺手改几个字("明天要是下雨的话提醒我带伞" → "要是下雨提醒我
//	带伞"), 所以不要求逐字: raw 里**连着的两个字**, 有六成能在他某一句
//	里找到就算. 编出来的话几乎一个都对不上 —— 例如那句"我上课呢接不了
//	电话"跟他那句"我设了哪些提醒"一对都没有.
func quotedFrom(raw string, heard []string) bool {
	r := normalize(raw)
	if len(r) == 0 {
		return false
	}
	for _, h := range heard {
		hn := string(normalize(h))
		if len(r) == 1 {
			if strings.ContainsRune(hn, r[0]) {
				return true
			}
			continue
		}
		hit := 0
		for i := 0; i+1 < len(r); i++ {
			if strings.Contains(hn, string(r[i:i+2])) {
				hit++
			}
		}
		if hit*10 >= (len(r)-1)*6 {
			return true
		}
	}
	return false
}

// mentions what 里有没有**任何两个连着的字**出现在他说过的话里.
//
//	比 quotedFrom 松得多: 待办的 what 是它归纳过的("交电费"), 不是原话.
//	但一件他提都没提过的事(上课), 两个字都对不上.
func mentions(what string, heard []string) bool {
	w := normalize(what)
	if len(w) < 2 {
		return true // 一个字的待办没法核对, 放过
	}
	for _, h := range heard {
		hn := string(normalize(h))
		for i := 0; i+1 < len(w); i++ {
			if strings.Contains(hn, string(w[i:i+2])) {
				return true
			}
		}
	}
	return false
}

// notSaid 拒绝的话 —— 要让它知道**下一步该做什么**, 不只是"不行".
func notSaid(field, text string) error {
	return fmt.Errorf("%s「%s」不在他最近说过的话里。这一条会在以后自己触发, "+
		"而那时候他不在场、也看不出是谁定的 —— 所以它得出自他的原话", field, text)
}
