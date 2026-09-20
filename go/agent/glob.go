package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 按名字找文件.
//
// search 解决的是"内容在哪", 这个解决的是"文件在哪".
// 两者缺一不可: 想知道"哪些文件叫 *_test.go", search 帮不上忙 ——
// 只能 list_dir 一层层翻, 深目录里翻十几轮.
//
// ── 为什么要自己写匹配 ──
//
// Go 标准库的 filepath.Match **不支持 `**`**: 它的 `*` 不跨目录分隔符,
// 所以 `**/*.go` 在标准库里永远匹配不到 `a/b/c.go`.
// 而 `**` 恰恰是这个工具唯一有价值的形态 —— 不能跨层就等于 list_dir.
//
// 语义跟生产版 6X 的 search_files 对齐 (fast-glob):
//
//	*     匹配一段路径里的任意字符, **不跨 /**
//	**    匹配任意多段路径 (包括零段)
//	?     匹配一个字符
//	[abc] 字符集
//
// "包括零段"这一条容易漏: `src/**/*.go` 必须能匹配 `src/main.go`,
// 否则用户写出最自然的那个模式却找不到最顶层的文件.

const globMaxResults = 200

func globTool() Tool {
	return Tool{
		Name: "find_files",
		Desc: "按文件名找文件(glob)。** 跨目录, * 不跨。要搜内容用 search",
		Args: map[string]string{
			"pattern": "如 **/*.go 、 src/**/*.test.ts 、 **/README*",
			"path":    "从哪个目录开始找。不给就是当前目录",
		},
		ArgOrder: []string{"pattern", "path"},
		Optional: map[string]bool{"path": true},
		Needs:    abi.AxisRead, ScopeArg: "path", WorldSensitive: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			pat := argStr(a, "pattern")
			root := argStr(a, "path")
			if root == "" {
				root = "."
			}
			hits, truncated, err := runGlob(t, t.resolve(root), pat)
			if err != nil {
				return "", actionable(err, root, "目录不存在。用 list_dir 看看有什么")
			}
			return formatGlob(pat, root, hits, truncated), nil
		},
	}
}

func runGlob(box Toolbox, abs, pattern string) (hits []string, truncated bool, err error) {
	if _, err := os.Stat(abs); err != nil {
		return nil, false, err
	}
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			// 走不进去就跳过. 被约束的进程本来就看不见能力范围外的东西,
			// 那不是错误, 是设计.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if box.canceled() {
			truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		rel, e2 := filepath.Rel(abs, p)
		if e2 != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !globMatch(pattern, rel) {
			return nil
		}
		if len(hits) >= globMaxResults {
			truncated = true
			return fs.SkipAll
		}
		hits = append(hits, rel)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	// 排序 —— 文件系统顺序不保证稳定, 不排会让同一次查询每次结果不同,
	// 前缀缓存跟着废
	sort.Strings(hits)
	return hits, truncated, nil
}

// globMatch 支持 ** 的匹配.
//
// 按 / 切成段之后是个经典的两段式匹配问题:
// `**` 能吃掉零到多段, 其余每段用标准库逐段匹配.
func globMatch(pattern, name string) bool {
	return matchSegments(
		strings.Split(path.Clean(pattern), "/"),
		strings.Split(path.Clean(name), "/"))
}

func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// `**` 吃零段也算 —— `src/**/*.go` 必须能匹配 `src/main.go`.
			// 漏了这一条, 用户写出最自然的模式却找不到最顶层的文件.
			rest := pat[1:]
			if len(rest) == 0 {
				return true // 末尾的 ** 吃掉剩下全部
			}
			for i := 0; i <= len(seg); i++ {
				if matchSegments(rest, seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

func formatGlob(pat, root string, hits []string, truncated bool) string {
	if len(hits) == 0 {
		// 没找到要给下一步 —— 只说"没找到"模型会原样再找一次.
		// 最常见的错法是忘了 **, 于是只在最顶层找.
		hint := ""
		if !strings.Contains(pat, "**") {
			hint = " 你的模式里没有 ** —— 那只会在最顶层找。" +
				"想连子目录一起找就写成 **/" + path.Base(pat) + "。"
		}
		return fmt.Sprintf("在 %s 下没有文件匹配 %q。%s别原样重找, "+
			"换个更宽的模式, 或者先 list_dir 看看目录长什么样。", root, pat, hint)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d 个文件匹配 %s:\n", len(hits), pat)
	for _, h := range hits {
		fmt.Fprintf(&b, "%s\n", path.Join(root, h))
	}
	if truncated {
		fmt.Fprintf(&b, "(到了 %d 个上限, **还有没列出来的**。"+
			"结论别下成'只有这些', 把模式写具体点或者缩小 path。)", globMaxResults)
	}
	return strings.TrimRight(b.String(), "\n")
}
