package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 写后自动验证 —— 把"我以为改对了"变成"编译器说改对了".
//
// ── 为什么提示词不够 ──
//
// prompt.go 已经写了"改了代码就跑测试"。但那是**建议**, 模型不一定跑 ——
// 跟当年"提示词里写了同方向连着两次失败就换角度"是完全同一类问题:
// 提示词管不住的东西, 得由 loop 兜住。
//
// 而且这一步的收益不对称: 跑一次几百毫秒的语法检查, 换掉的是
// "写进去了但根本跑不起来"这一整类失败 —— 那类失败的代价是
// 用户拿到一个假的"已完成", 下一轮才发现。
//
// ── 只报跟这次改动有关的错 ──
//
// 这是 6X 那边记下来的关键一条: 项目里本来就有的编译错误会把信号淹掉。
// 模型看到一屏跟自己无关的报错, 要么去修不该它修的东西, 要么直接放弃。
// 所以输出要按被改文件过滤; 过滤完没有了但退出码非零, 就明说
// **"有错, 但不在你改的文件里"** —— 那句话本身就是有用的信息。
//
// ── 做不到就闭嘴 ──
//
// 没有 proc 能力、没装那个工具链、验证器本身跑挂了 —— 一律**静默跳过**。
// 验证是锦上添花, 不该变成新的噪音源, 更不该因为它失败就让这一步显得失败。

const (
	verifyTimeout = 20 * time.Second
	// verifyCooldown 同一个文件多久内不重复验.
	// 一次改动常常拆成好几个 edit, 每个都验一遍纯属浪费。
	verifyCooldown = 8 * time.Second
	verifyMaxBytes = 1500
)

// verifier 一次验证要跑什么
type verifier struct {
	// what 给人看的一句话, 会出现在工具结果里
	what string
	cmd  string
}

// verifierFor 决定这次改动该怎么验.
//
// **判定必须确定性**: 同样的仓库同样的文件永远给出同一条命令。
// 不许按目录遍历顺序、不许按 map 顺序 —— 结果会进上下文,
// 抖动会让同一次改动每次看起来都不一样。
//
// 只认**语法/类型**这一层, 不跑测试:
//
//	语法错   一定是这次改动造成的, 而且一定要修 → 值得自动跑
//	测试红   可能是环境、可能是本来就红的、可能要几分钟 → 交给模型自己判断
func verifierFor(root, rel string) *verifier {
	abs := filepath.Join(root, rel)
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		// 整包编译才看得出跨文件的错 (改了签名、少了 import)。
		// 单文件 gofmt -e 只看得到语法, 看不到类型。
		if exists(filepath.Join(root, "go.mod")) {
			return &verifier{"go build", "go build ./... 2>&1"}
		}
	case ".py":
		// py_compile 只看语法, 不 import, 所以不会有副作用也不会慢
		return &verifier{"python 语法检查",
			fmt.Sprintf("python3 -m py_compile %s 2>&1", shArg(abs))}
	case ".ts", ".tsx":
		if exists(filepath.Join(root, "tsconfig.json")) {
			// --noEmit 只做类型检查不产出文件。装没装 tsc 不确定,
			// 没装就是一条 command not found, 会被当成"没有验证器"跳过
			return &verifier{"tsc 类型检查",
				"npx --no-install tsc --noEmit 2>&1"}
		}
	// **.jsx 不在这儿**: node --check 根本不认这个后缀, 更不认 JSX 语法.
	// 它报的是 ERR_UNKNOWN_FILE_EXTENSION —— 而那句话会被包成
	// "**你改的这个文件有问题**". 每个 .jsx 都会收到一次这句, 多个文件
	// 就会重复产生假的错误提示.
	//
	// 验不了就别验. 一条假警报比没有警报坏得多: 没有警报它接着干,
	// 假警报它要停下来找一个不存在的毛病.
	case ".js", ".mjs", ".cjs":
		return &verifier{"node 语法检查",
			fmt.Sprintf("node --check %s 2>&1", shArg(abs))}
	case ".json":
		return &verifier{"JSON 格式检查",
			fmt.Sprintf("python3 -c 'import json,sys;json.load(open(sys.argv[1]))' %s 2>&1", shArg(abs))}
	case ".sh":
		return &verifier{"shell 语法检查", fmt.Sprintf("sh -n %s 2>&1", shArg(abs))}
	}
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// shArg 单引号包裹. 路径来自模型, **必须当成不可信输入** ——
// 它会被拼进 sh -c 的命令行里。
func shArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// autoVerify 一次写操作之后顺手验一下.
//
// 返回要追加到工具结果后面的话; 空串表示这次没什么可说的
// (没有验证器 / 没有 proc 能力 / 冷却期内 / 验过没问题)。
//
// **验过没问题也不说话**: 每次都说一句"语法没问题"是纯噪音,
// 而且它会挤掉真正有用的上下文。只有发现问题才开口。
func (a *Agent) autoVerify(rel string) string {
	if a.Box.Root == "" || rel == "" {
		return ""
	}
	// 没有起进程的能力就别验 —— 而且**不要因此报错**,
	// 这只是没有这项增强, 不是这一步失败了
	if ok, err := a.ABI.Can(abi.AxisProc, "verify"); err != nil || !ok {
		return ""
	}
	v := verifierFor(a.Box.Root, rel)
	if v == nil {
		return ""
	}
	if a.verifiedRecently(rel) {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	out, err := execCommand(Toolbox{Root: a.Box.Root, Ctx: ctx},
		map[string]any{"cmd": v.cmd})
	if err != nil {
		// 验证器自己跑挂了 (超时/起不来) —— 闭嘴。
		// 把它报出去只会让模型去修一个跟它无关的问题。
		return ""
	}
	if strings.HasPrefix(out, "[退出码 0") {
		return ""
	}
	/**
	 * **检查器验不了这个文件, 不等于这个文件有毛病**.
	 *
	 *	这两件事的区别就是这个功能的全部价值. 说反了的后果是不对称的:
	 *	漏报它接着干, 误报它停下来找一个不存在的毛病 —— 多个 .jsx 文件会
	 *	重复收到"你改的这个文件有问题"的错误提示.
	 *
	 *	后缀那张表已经修了, 这里是**兜底**: 工具自己不认的东西还会有
	 *	别的形态(缺插件、版本太老), 它们一律不是作者的错.
	 */
	if cannotCheck(out) {
		return ""
	}

	body := relevantLines(out, rel)
	if body == "" {
		// ── 有错, 但不在它刚改的那个文件里 ──
		//
		// **不许断言"那是本来就有的".** 我们不知道这件事, 而说错的代价
		// 是不对称的:
		//
		//	第一种情况: 全新的空项目, 刚写下第一个文件,
		//	go build 当然不过(main 还没写). 这时候说"本来就有的"是
		//	纯粹的假话 —— 前一秒那儿什么都没有。
		//
		//	更糟的第二种: 它改了 notes.go 的函数签名, main.go 因此编译不过。
		//	报错行里没有 notes.go, 于是这条会说"不是你改出来的, 别去动它"
		//	—— 项目就这么坏着交付了, 而它以为自己被明确告知过不用管。
		//
		// 能知道的只有一件事: **这次会话里它动过哪些文件**(a.lastVerified
		// 的键就是). 所以按这个分两种说法, 一句都不多说。
		return elsewhereNote(v.what, rel, a.touchedIn(out, rel))
	}
	return fmt.Sprintf(
		"\n\n⚠ 顺带跑了 %s，**你改的这个文件有问题**：\n%s\n"+
			"先把它修掉再往下走。", v.what, trunc(body, verifyMaxBytes))
}

// cannotCheck 这条输出说的是"我验不了", 不是"你写错了"
func cannotCheck(out string) bool {
	for _, sign := range []string{
		"ERR_UNKNOWN_FILE_EXTENSION",
		"Unknown file extension",
		"command not found",
		"No such file or directory",
		"cannot find module",
		"Cannot find module",
	} {
		if strings.Contains(out, sign) {
			return true
		}
	}
	return false
}

// verifiedRecently 冷却. 一次改动常常拆成好几个 edit, 每个都验纯属浪费
func (a *Agent) verifiedRecently(rel string) bool {
	if a.lastVerified == nil {
		a.lastVerified = map[string]time.Time{}
	}
	now := time.Now()
	if t, ok := a.lastVerified[rel]; ok && now.Sub(t) < verifyCooldown {
		return true
	}
	a.lastVerified[rel] = now
	return false
}

// elsewhereNote 报错落在别处时那句话.
//
// 单拎出来是因为**它的措辞就是这个功能本身**: 同一份事实说成
// "不是你改出来的"还是"落在你也动过的文件上", 决定了模型接下来
// 是去修还是不管。拆出来才测得到。
func elsewhereNote(what, rel, other string) string {
	if other != "" {
		return fmt.Sprintf(
			"\n\n⚠ 顺带跑了 %s：报错不在 %s 里，但落在**你这次也动过的** %s 上"+
				"——多半是这次改动的跨文件影响，去那儿看。", what, rel, other)
	}
	return fmt.Sprintf(
		"\n\n(顺带跑了 %s：项目里有编译/语法错误，但不在 %s 里，"+
			"也不在你这次动过的别的文件里。可能是项目本来就有的，"+
			"也可能是还没写完的部分——**先别管它**，除非你接下来要动的正是那儿。)",
		what, rel)
}

// touchedIn 报错行里有没有落在**这次会话动过的别的文件**上.
//
// 这是"不是你改出来的"和"多半就是你改出来的"之间唯一能查实的判据:
// 我们手里没有项目的历史, 但我们知道自己动过谁。
//
// 返回一个逗号分隔的文件名, 空 = 报错跟它动过的文件都无关。
func (a *Agent) touchedIn(out, rel string) string {
	var hit []string
	seen := map[string]bool{}
	for path := range a.lastVerified {
		if path == rel {
			continue
		}
		base := filepath.Base(path)
		if base == "" || seen[base] {
			continue
		}
		if strings.Contains(out, base) || strings.Contains(out, path) {
			seen[base] = true
			hit = append(hit, base)
		}
	}
	// 顺序要稳 —— 不稳的话同一次报错每次说法都不一样
	sort.Strings(hit)
	return strings.Join(hit, "、")
}

// relevantLines 只留跟这次改动有关的行.
//
// 项目里本来就有的错会把信号淹掉 —— 模型看到一屏跟自己无关的报错,
// 要么去修不该它修的东西, 要么直接放弃。
//
// 按**文件名**匹配而不是完整路径: 编译器报的路径形态各不相同
// (相对/绝对/带包名前缀), 按完整路径比会一条都留不下。
func relevantLines(out, rel string) string {
	base := filepath.Base(rel)
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, base) || strings.Contains(line, rel) {
			keep = append(keep, strings.TrimRight(line, "\r"))
		}
	}
	return strings.Join(keep, "\n")
}
