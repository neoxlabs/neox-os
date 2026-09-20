package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

/**
 * smoke —— **合进去之后, 主干还起得来吗**.
 *
 * ── 这个检查针对合并后最容易漏掉的断裂 ──
 *
 *	群里说了句"加一块预算". 阿导拆活: 自己写 budget.py, 让报表写 cli 的
 *	命令壳. 报表照做, 跑测试之前**自己造了一个 budget 的 stub 放进
 *	PYTHONPATH** —— 它很诚实, 文件头写着"临时 stub，验证完即弃",
 *	而且在汇报里说了. 测试全绿, 合入闸门放行, 合进主干.
 *
 *	然后阿导那一轮停了(撞上原地打转的止损), budget.py **从来没被写出来**.
 *
 *	于是主干上的 cli.py 写着 `import budget`, 而 budget 不存在 ——
 *	`python3 -c "import cli"` 当场炸. 而项目卡上显示"都合上了",
 *	13 个测试也全过(没有一个测试 import cli).
 *
 *	**每个人都对, 合起来不对** 的一个新变种: 这一块是对着一个假的验的.
 *
 * ── 为什么是"报"不是"拦" ──
 *
 *	判据是启发式的(顶层模块 import 得动吗), 它不可能永远对. 拿一个会错的
 *	判据去拦合入, 换来的是"明明是好的却合不进去" —— 那比漏掉更糟, 因为
 *	人会开始想办法绕过它, 而绕过的路一旦踩熟, 真拦住的那次也会被绕过.
 *
 *	所以: 合完照跑一遍, 结果**当场说给刚合完的那个人听**. 它下一步
 *	自己就会去补. 需要的是让那句"import budget 但没写
 *	budget.py"直接回到负责合并的人手里, 而不是只停留在报表里.
 */

// smokeTimeout 单个模块的上限. import 一个模块本该是毫秒级;
// 卡住的多半是它在 import 时干了不该干的事(起服务、连数据库)
const smokeTimeout = 6 * time.Second

// smokeMax 最多试几个 —— 一个大项目顶层几十个模块, 全试一遍太慢
const smokeMax = 12

/**
 * smokeOf 合完之后在主干上跑一遍最起码的检查.
 *
 *	返回**说给人听的那几句**; 没问题就是空.
 *
 *	只做一件事: 顶层的模块还 import 得动吗. 这一条覆盖的正是最常见的
 *	那类断裂 —— 谁删了/没写一个文件, 而别人在 import 它.
 */
func smokeOf(project string) []string {
	if project == "" {
		return nil
	}
	var bad []string
	for _, mod := range topPyModules(project) {
		out, err := runIn(project, "python3", "-c", "import "+mod)
		if err == nil {
			continue
		}
		// 只报**这一类**: 模块缺了 / 语法坏了. 别的(缺第三方依赖、
		// 环境变量没配)不是这次合并造成的, 报了只会变成天天响的噪音
		if line, ok := brokenImport(out); ok {
			bad = append(bad, fmt.Sprintf("%s: %s", mod+".py", line))
		}
	}
	return bad
}

// topPyModules 项目根上的那几个模块 —— 只看顶层, 不递归.
//
//	包(有 __init__.py 的目录)也算一个模块: 它正是"整个应用的入口"
//	最常待的地方.
func topPyModules(project string) []string {
	entries, err := os.ReadDir(project)
	if err != nil {
		return nil
	}
	var mods []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		switch {
		case entry.IsDir():
			if _, err := os.Stat(filepath.Join(project, name, "__init__.py")); err == nil {
				mods = append(mods, name)
			}
		case strings.HasSuffix(name, ".py"):
			base := strings.TrimSuffix(name, ".py")
			// 测试自己有测试的跑法, 这里不代劳 —— 而且 import 一个
			// 测试文件常常需要 pytest 那套东西在场
			if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test") {
				continue
			}
			mods = append(mods, base)
		}
	}
	sort.Strings(mods)
	if len(mods) > smokeMax {
		mods = mods[:smokeMax]
	}
	return mods
}

/**
 * brokenImport 这条报错是不是"东西缺了/坏了".
 *
 *	**判得窄一点**: 缺第三方依赖(没装 requests)、环境变量没配、
 *	连不上数据库 —— 那些不是这次合并造成的, 报了只会天天响,
 *	而天天响的警报等于没有警报.
 */
func brokenImport(out string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	switch {
	case strings.HasPrefix(last, "ModuleNotFoundError: No module named"):
		// 缺的是**项目自己的**模块才算 —— 第三方装不装是环境的事
		name := strings.Trim(strings.TrimPrefix(last, "ModuleNotFoundError: No module named"), " '\"")
		if isThirdParty(name) {
			return "", false
		}
		return last, true
	case strings.HasPrefix(last, "SyntaxError"), strings.HasPrefix(last, "IndentationError"),
		strings.HasPrefix(last, "ImportError"), strings.HasPrefix(last, "NameError"):
		return last, true
	}
	return "", false
}

// 常见的第三方名字 —— 装没装是这台机器的事, 不是这次合并的事
var thirdParty = map[string]bool{
	"pytest": true, "requests": true, "numpy": true, "pandas": true, "flask": true,
	"django": true, "fastapi": true, "sqlalchemy": true, "yaml": true, "dotenv": true,
	"pydantic": true, "aiohttp": true, "httpx": true, "click": true, "rich": true,
}

func isThirdParty(name string) bool {
	return thirdParty[strings.Split(name, ".")[0]]
}

// runIn 在某个目录里跑一条命令, 带超时.
//
//	**不继承用户 shell 的那堆环境**(PYTHONPATH 尤其): 有人在自己的
//	worktree 里为了验证塞过一个 stub 进 PYTHONPATH —— 那正是这次要
//	查的那种假绿.
func runIn(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), smokeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONPATH=", "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// smokeNote 把检查结果写成说给刚合完那个人听的一段话
func smokeNote(bad []string) string {
	if len(bad) == 0 {
		return ""
	}
	return "\n\n**合进去之后主干起不来了**：\n  " + strings.Join(bad, "\n  ") +
		"\n这是在主干上跑的（没有你那份 PYTHONPATH／stub）。" +
		"你那一轮验证可能是对着一个假的验的 —— 现在别人拉下去就是坏的，先把它补上。"
}
