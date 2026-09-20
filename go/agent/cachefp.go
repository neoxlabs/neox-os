package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/neox-os/neox-os/abi"
)

// 前缀漂移检测 —— 把一笔看不见的钱变成一条看得见的事件.
//
// ── 为什么必须有 ──
//
// 前缀缓存要求 [system][tools][msg0..k] **逐字节稳定**, 任何一段变了,
// 那之后全部 miss. 而这种失效是**完全静默**的: 请求照常成功, 结果照常
// 正确, 只是账单悄悄翻几倍.
//
// 长会话里命中率可以稳定在 96~99%, 但某一轮会突然掉到 15%
// (只命中 4864, 差不多正好是系统段的大小), 下一轮又回到 96%.
// 那一轮会多付 28K 未命中 token —— 而**没有任何一处会说这件事发生了**.
//
// 仅凭结果无法判断是折叠、页被换出还是 tools 声明不稳定.
// **查不出来的根本原因是缺少能指出分叉位置的仪表.**
//
// 所以这一步不是修复, 是先装上仪表: 逐条消息哈希, 报出第一处分叉的
// 下标和角色; 系统段变了还要给出字符级的位置和前后片段.
// 有了它, 下一次漂移会自己说出自己是谁.
//
// 6X 那边同样的位置有同一套东西 (runner.ts 的 firstDivergentMsgIdx) ——
// 代价已经付过一次的不该再付.

// cacheFingerprint 一次请求的前缀指纹
type cacheFingerprint struct {
	system string
	tools  string
	msgs   []string
	sysTxt string
}

// cacheDrift 跟上一轮比, 前缀在哪儿断的. nil 表示没断.
type cacheDrift struct {
	// FirstDivergentIdx 第一条对不上的消息下标. -1 = 消息部分没变
	FirstDivergentIdx int
	DivergentRole     string
	PrevMsgCount      int
	MsgCount          int
	SystemChanged     bool
	ToolsChanged      bool
	// SysDiffAt 系统段第一个不同的字符位置 + 前后片段.
	// **必须给到字符级**: 只说"system 变了"没法查, 系统段几千字,
	// 肉眼比对是不可能的.
	SysDiffAt   int
	SysDiffPrev string
	SysDiffCur  string
}

func fingerprintOf(system string, tools []abi.ToolDef, msgs []abi.InferMessage) cacheFingerprint {
	fp := cacheFingerprint{
		system: shortHash(system),
		sysTxt: system,
		msgs:   make([]string, 0, len(msgs)),
	}
	tb, _ := json.Marshal(tools)
	fp.tools = shortHash(string(tb))
	for _, m := range msgs {
		// 把**所有会进 wire format 的东西**都算进去 —— 只哈希 content
		// 会漏掉 tool_calls 的 id 变了这种情况, 而 id 一变缓存就全废.
		var calls string
		for _, c := range m.ToolCalls {
			calls += c.ID + ":" + c.Name + ":" + c.Arguments + ";"
		}
		fp.msgs = append(fp.msgs,
			shortHash(m.Role+"\x00"+m.Content+"\x00"+calls+"\x00"+m.ToolCallID+"\x00"+m.Reasoning))
	}
	return fp
}

// compare 跟上一轮比. 返回 nil 表示前缀是干净的 (只在末尾追加了).
//
// **第一轮不报.** 没有上一轮可比时, 一切都"变了" —— 报出来是纯误报,
// 而误报比不报更糟: 狼来了喊多了, 真漂移那次就没人看了.
// 第一轮的 sysDiffPrev 是空串, sysDiffAt=0, 因此不能当作漂移报告.
func (prev cacheFingerprint) compare(cur cacheFingerprint) *cacheDrift {
	if prev.system == "" && len(prev.msgs) == 0 {
		return nil // 第一轮
	}
	d := &cacheDrift{
		FirstDivergentIdx: -1,
		PrevMsgCount:      len(prev.msgs),
		MsgCount:          len(cur.msgs),
		SystemChanged:     prev.system != cur.system,
		ToolsChanged:      prev.tools != cur.tools,
	}
	n := len(prev.msgs)
	if len(cur.msgs) < n {
		n = len(cur.msgs)
	}
	for i := 0; i < n; i++ {
		if prev.msgs[i] != cur.msgs[i] {
			d.FirstDivergentIdx = i
			break
		}
	}
	if d.SystemChanged {
		// 字符级定位 —— 系统段几千字, 只说"变了"等于没说
		a, b := prev.sysTxt, cur.sysTxt
		i, m := 0, len(a)
		if len(b) < m {
			m = len(b)
		}
		for i < m && a[i] == b[i] {
			i++
		}
		d.SysDiffAt = i
		d.SysDiffPrev = snippet(a, i)
		d.SysDiffCur = snippet(b, i)
	}
	if !d.SystemChanged && !d.ToolsChanged && d.FirstDivergentIdx < 0 {
		return nil // 干净: 只在末尾追加
	}
	return d
}

func (d *cacheDrift) payload() map[string]any {
	m := map[string]any{
		"phase": "prefix_broken", "firstDivergentIdx": d.FirstDivergentIdx,
		"prevMsgCount": d.PrevMsgCount, "msgCount": d.MsgCount,
		"systemChanged": d.SystemChanged, "toolsChanged": d.ToolsChanged,
	}
	if d.DivergentRole != "" {
		m["divergentRole"] = d.DivergentRole
	}
	if d.SystemChanged {
		m["sysDiffAt"] = d.SysDiffAt
		m["sysDiffPrev"] = d.SysDiffPrev
		m["sysDiffCur"] = d.SysDiffCur
	}
	return m
}

// why 一句人话, 说清这次是哪一段断的.
//
// **要能直接指向去查哪里**: "system 段第 1234 个字符变了"跟
// "前缀断了"是完全不同的信息量.
func (d *cacheDrift) why() string {
	switch {
	case d.SystemChanged:
		return fmt.Sprintf("系统段变了(第 %d 个字符起) —— 它必须字节稳定, "+
			"检查是不是拼进了时间/随机 id/机器名", d.SysDiffAt)
	case d.ToolsChanged:
		return "工具声明变了 —— 它跟系统段一样进前缀, 检查工具表和 schema 生成是不是不稳定"
	default:
		return fmt.Sprintf("第 %d 条消息(%s)被改写了 —— 历史本该只增不改",
			d.FirstDivergentIdx, d.DivergentRole)
	}
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func snippet(s string, at int) string {
	lo := at - 20
	if lo < 0 {
		lo = 0
	}
	hi := at + 60
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}
