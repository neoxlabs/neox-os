package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 内容搜索.
//
// 这是模型**自己提出来的**缺口: 上一轮让它在 1001 行的日志里找一行 FATAL,
// 它只能分三片把整个文件读进上下文, 最后说
// "我没有 grep 之类的搜索工具, 无法确认里面有没有 FATAL".
//
// 它跟上下文管理是同一个问题的两面: 上下文折叠做得再好,
// 也不如**一开始就别把整个文件读进来**.
//
// ── 为什么自己实现而不是 run 一条 grep ──
//
// 现在 agent 有 run 了, 起子进程这条路是通的. 但搜索仍然自己实现:
//
//	不依赖机器      rg 不一定装, grep 的参数在 BSD/GNU 之间还不一样
//	输出形态固定    `路径:行号: 内容`, 行号能直接喂给 read_file 的 offset
//	默认就跳二进制  否则一个 .png 能吐出几万个垃圾 token
//
// 判断标准是**结果要不要进上下文**: 进上下文的东西, 格式必须由我们控;
// 只看退出码/给人读一眼的 (测试、编译、git), 交给 run 更好.
//
// ── 输出形态是关键 ──
//
// 输出 `路径:行号: 行内容`, 行号能**直接喂给 read_file 的 offset** ——
// 搜索定位、分片读取细节, 两个工具接在一起才闭环.
// 返回整段上下文就又变成"把文件读进来", 那等于没做.

const (
	searchMaxTotal   = 60 // 全部匹配上限
	searchMaxPerFile = 20 // 单文件上限 —— 防止一个文件淹掉其它文件的匹配
	searchMaxLineLen = 300
	searchMaxFiles   = 2000
)

type searchHit struct {
	path string
	line int
	text string
}

func searchTool() Tool {
	return Tool{
		Name: "search",
		// "别整文件读"这句原来在提示词里. 它是**这个工具的用法**,
		// 跟着工具表走 —— 模型仍会习惯性整文件读,
		// 需要在用到工具的地方明确这条约束
		Desc: "按内容搜索(正则), 返回 路径:行号: 匹配行。" +
			"**找内容用它, 不要把整个文件读进来**；行号可直接给 read_file 的 offset, " +
			"先定位再看那一段前后文",
		Args: map[string]string{
			"pattern": "正则表达式。要找字面量就把 . * ( ) 等转义",
			"path":    "在哪找, 文件或目录。不给就是当前目录",
		},
		ArgOrder: []string{"pattern", "path"},
		Optional: map[string]bool{"path": true},
		Needs:    abi.AxisRead, ScopeArg: "path", WorldSensitive: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			pat := argStr(a, "pattern")
			re, err := regexp.Compile(pat)
			if err != nil {
				// 坏正则要说清怎么改 —— 模型最常犯的就是把字面量当正则写.
				// 只报 "invalid syntax" 它会瞎改一通.
				return "", fmt.Errorf(
					"正则 %q 编译不了: %v。如果你要找的是字面文本, "+
						"把 . * + ? ( ) [ ] 这些字符前面加反斜杠转义", pat, err)
			}
			root := argStr(a, "path")
			if root == "" {
				root = "."
			}
			hits, scanned, truncated, err := runSearch(t, t.resolve(root), root, re)
			if err != nil {
				return "", actionable(err, root, "路径不存在。用 list_dir 看看有什么")
			}
			return formatHits(pat, root, hits, scanned, truncated), nil
		},
	}
}

func runSearch(box Toolbox, abs, display string, re *regexp.Regexp) (hits []searchHit, scanned int, truncated bool, err error) {
	var capped bool
	info, err := os.Stat(abs)
	if err != nil {
		return nil, 0, false, err
	}

	var files []string
	if !info.IsDir() {
		files = []string{abs}
	} else {
		// 排序遍历 —— 文件系统的顺序不保证稳定, 不排序会让同一次搜索
		// 每次返回的截断位置不同, 前缀缓存跟着废
		err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				// 走不进去的目录跳过就好. 被约束的进程本来就看不见
				// 能力范围外的东西, 那不是错误, 是设计.
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if n := d.Name(); n == ".git" || n == "node_modules" {
					return fs.SkipDir
				}
				return nil
			}
			// 密钥文件不搜 —— 搜"apiKey"就会把 provider.json 那一行原样带回来
			if box.secret(p) {
				return nil
			}
			if len(files) >= searchMaxFiles {
				// 文件数封顶了. **这件事必须往上传** ——
				// 不传的话下面报的是"搜了 2000 个文件, 没找到",
				// 模型会把它读成"这东西不存在", 然后据此下结论.
				// 423MB 的目录树里搜一个不存在的词时, 模型会答
				// "site 里没有这个名字" —— 可它只看了一部分.
				capped = true
				return fs.SkipAll
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			return nil, 0, false, err
		}
		sort.Strings(files)
	}

	for _, f := range files {
		if box.canceled() {
			// 走到一半被取消: **把已经找到的交出去**, 并说清没走完.
			// 直接丢掉等于白跑, 而部分结果对模型往往已经够用了.
			return hits, scanned, true, nil
		}
		b, e := os.ReadFile(f)
		if e != nil || isBinary(b) {
			continue // 二进制不搜 —— 匹配上了对模型也没有意义
		}
		scanned++
		rel := f
		if info.IsDir() {
			if r, e := filepath.Rel(abs, f); e == nil {
				rel = filepath.Join(display, r)
			}
		} else {
			rel = display
		}

		perFile := 0
		for i, line := range strings.Split(string(b), "\n") {
			if !re.MatchString(line) {
				continue
			}
			if perFile >= searchMaxPerFile || len(hits) >= searchMaxTotal {
				truncated = true
				break
			}
			if len(line) > searchMaxLineLen {
				line = line[:searchMaxLineLen] + " …(这行太长, 已截断)"
			}
			hits = append(hits, searchHit{path: rel, line: i + 1, text: line})
			perFile++
		}
		if len(hits) >= searchMaxTotal {
			truncated = true
			break
		}
	}
	return hits, scanned, truncated || capped, nil
}

func formatHits(pat, root string, hits []searchHit, scanned int, truncated bool) string {
	if len(hits) == 0 {
		// 没找到也要给下一步 —— 只说"没找到"模型会原样再搜一次
		if truncated {
			// **没看全就说没看全.** "搜了 N 个没找到"会被读成"不存在",
			// 而结论完全不成立 —— 我们只看了一部分.
			return fmt.Sprintf(
				"在 %s 下搜了 %d 个文件, 这些里面没有匹配 %q 的行。"+
					"**但这个目录太大, 没搜完** —— 别据此下'不存在'的结论。"+
					"缩小 path 到具体的子目录再搜一次。", root, scanned, pat)
		}
		return fmt.Sprintf(
			"在 %s 下搜了 %d 个文件(全部), 没有一行匹配 %q。"+
				"要么这东西真的不在这里, 要么你的正则太严 —— "+
				"换个更短的关键词再试, 别原样重搜。", root, scanned, pat)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d 处匹配 (搜了 %d 个文件):\n", len(hits), scanned)
	for _, h := range hits {
		fmt.Fprintf(&b, "%s:%d: %s\n", h.path, h.line, h.text)
	}
	if truncated {
		// 截断必须说出来, 而且要说清"还有没列出来的" ——
		// 静默截断会让模型把"只有这些"当成结论.
		fmt.Fprintf(&b, "(达到了 %d 条上限, **还有匹配没列出来**。"+
			"结论别下成'只有这些', 要么把正则写具体点, 要么缩小 path 范围。)",
			searchMaxTotal)
	}
	return strings.TrimRight(b.String(), "\n")
}
