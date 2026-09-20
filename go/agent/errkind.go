package agent

import "strings"

// 错误分类 + 递进策略.
//
// ── 为什么"数第几次"不够 ──
//
// 原来只有 sameErrKind: 判断这次的错跟上次是不是同一类, 然后数次数,
// 到 2 次 warn、到 3 次停. 两级, 而且两级说的话几乎一样 ——
// 都是"你的假设错了, 换参数救不了".
//
// 问题在于: **"换个方向"这句话本身不可执行**. 模型卡住的时候恰恰是它
// 判断力最差的时候, 你告诉它"换个思路"跟没说一样. 它需要的是
// "现在具体该干哪件事".
//
// 6X 那边的做法是分类之后按次数给**不同的具体动作**:
// 第 2 次让它重读, 第 3 次让它换工具, 第 4 次让它求助 ——
// 每一级都是一条能直接照做的指令, 而且不重复上一级说过的话.
//
// ── 而且原来的分类漏了最主要的一类 ──
//
// 那 4 个中文子串是 `文件不存在/权限被拒/目录不存在/没有这个工具`,
// 而 edit_file 报的是 `string_not_found` 和 `ambiguous_match` ——
// **一个都不在表里**. 靠 a == b 兜底: 只有两次错误文本一字不差才算同类,
// 而 ambiguous_match 的文本里带出现次数 (出现了 3 次 / 出现了 5 次),
// 换个文件、换个 old_string 文本就不同了.
//
// 结果就是: **edit 反复匹配失败这条最主要的卡死路径, 一直没有保护.**

// errKind 错误的类别. 分类的粒度按"下一步该干什么"定 ——
// 两个错如果补救办法一样, 就该是同一类.
type errKind string

const (
	errNone errKind = ""
	// errNoMatch 内容寻址没找到 —— 文件跟它以为的不一样
	errNoMatch errKind = "no_match"
	// errAmbiguous 锚点不唯一 —— 上下文给少了
	errAmbiguous errKind = "ambiguous"
	// errMissing 路径不存在
	errMissing errKind = "missing"
	// errDenied 越权 —— 换写法救不了, 只能申请或者告诉用户
	errDenied errKind = "denied"
	// errBadArgs 参数不对 (缺参数/不认识的工具)
	errBadArgs errKind = "bad_args"
	// errCommand 命令跑失败 (非零退出/起不来/超时)
	errCommand errKind = "command"
	// errOther 认不出来的. 仍然计数, 但不给具体策略 ——
	// **给一条针对不了的建议比不给更糟**
	errOther errKind = "other"
)

// classify 把错误文本归类.
//
// 按**哨兵串**匹配而不是整句比对: 错误文本里常带可变部分
// (出现了几次、哪个路径、退出码多少), 整句比对会让同一类错误
// 每次都算"新错误", 计数永远起不来 —— 那正是原来漏掉 edit 的原因.
func classify(msg string) errKind {
	if strings.TrimSpace(msg) == "" {
		return errNone
	}
	switch {
	case strings.Contains(msg, "string_not_found"):
		return errNoMatch
	case strings.Contains(msg, "ambiguous_match"):
		return errAmbiguous
	case strings.Contains(msg, "权限被拒"), strings.Contains(msg, "不在你的出网授权"),
		strings.Contains(msg, "被拒绝:"):
		return errDenied
	case strings.Contains(msg, "文件不存在"), strings.Contains(msg, "目录不存在"),
		strings.Contains(msg, "不是一个存在的目录"), strings.Contains(msg, "还没有这个东西"):
		return errMissing
	case strings.Contains(msg, "缺参数"), strings.Contains(msg, "没有这个工具"),
		strings.Contains(msg, "不认识的能力轴"):
		return errBadArgs
	case strings.Contains(msg, "退出码"), strings.Contains(msg, "起不来"),
		strings.Contains(msg, "超时, 已经杀掉"):
		return errCommand
	}
	return errOther
}

// advise 同一类错误连着第 n 次时, 给一条**能直接照做**的指令.
//
// 三条规矩:
//
//	① 每一级给不同的动作. 重复上一级等于告诉模型"你没听懂", 而它确实没辙
//	② 动作要具体到工具名和参数. "换个思路"不可执行, "改用 write_file" 可以
//	③ 最后一级一律是"停下来告诉用户" —— 试到第 4 次还不行, 继续试是在烧钱
//
// 返回空串表示这一级没有比通用提示更好的话可说.
func advise(kind errKind, n int) string {
	switch kind {
	case errNoMatch:
		switch n {
		case 2:
			return "文件跟你以为的不一样 —— 现在磁盘上的原文和你手里这份对不上。"
		case 3:
			return "已经匹配失败 3 次了。"
		}
	case errAmbiguous:
		switch n {
		case 2:
			return "那段原文在文件里不止一处。"
		case 3:
			return "还是不唯一 —— 这段内容在文件里重复得很厉害。"
		}
	case errMissing:
		switch n {
		case 2:
			return "这个路径下没有这个东西。"
		case 3:
			return "连着找不到 3 次 —— 目录结构跟你以为的不一样。"
		}
	case errDenied:
		// 越权只说一次就够 —— 换写法永远救不了, 多试一次就是多打扰用户一次
		if n >= 2 {
			return "这是权限, 不是写法 —— 同一条路换个写法还是同一个结果。"
		}
	case errBadArgs:
		switch n {
		case 2:
			return "参数跟工具要的对不上 —— 报错里写了它要什么。"
		case 3:
			return "同一个参数错误 3 次了。"
		}
	case errCommand:
		switch n {
		case 2:
			return "命令失败了 —— 报错通常在输出的头尾两处。"
		case 3:
			return "同一条命令失败 3 次。"
		}
	}
	return ""
}
