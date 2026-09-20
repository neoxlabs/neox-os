package main

import (
	"fmt"
	"path"
	"strings"
)

/**
 * whoHas —— "这个文件不存在" 的时候, **它在谁手上**.
 *
 * ── 路径不存在时的误导 ──
 *
 *	多人做同一个登录页时, 后端 app/server.js 可能已经分配给另一个分支.
 *	当前分支按契约去读 app/server.js, 如果只收到
 *
 *	    app/server.js: 文件不存在。别重试同一路径——先 list_dir 看看真实的文件名
 *
 *	**这句话会把判断引向错误方向**: 路径一个字都没错, 只是对应分支还没合上来.
 *	于是它去 list_dir、去 mkdir app、去写一个假 server 来自测 —— 全是
 *	因为报错说的是"你路径写错了".
 *
 * ── 为什么归宿主答 ──
 *
 *	agent 看得见的只有自己那份工作区. "别人合没合"这件事只有知道分支
 *	布局的人答得出来 —— 那就是宿主. 见 agent.Toolbox.Missing.
 *
 *	答的是**事实**, 不是建议: 谁的分支上有这个文件. 接下来是等他合、
 *	自己先写、还是去问一句, 是它自己的判断.
 */
func whoHas(project, main, me string, plans map[string]Plan) func(string) string {
	return func(rel string) string {
		rel = strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "./")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
			return ""
		}
		// 主干上有的话, 它自己拉一下就有了 —— 这是最省事的一条路
		if onBranch(project, main, rel) {
			return fmt.Sprintf("主干上有 %s，sync_down 拉一下就有了", rel)
		}
		var holders []string
		for name, plan := range plans {
			if name == me || plan.Branch == "" {
				continue
			}
			if onBranch(project, plan.Branch, rel) {
				holders = append(holders, name)
			}
		}
		if len(holders) == 0 {
			return ""
		}
		sortStrings(holders)
		return fmt.Sprintf("%s 手上有 %s，还没合上来 —— 等他合，或者先干你自己那份",
			strings.Join(holders, "、"), rel)
	}
}

// onBranch 这条分支上有没有这个文件
func onBranch(project, branch, rel string) bool {
	out, err := runGit(project, "ls-tree", "-r", "--name-only", branch, "--", rel)
	return err == nil && strings.TrimSpace(out) != ""
}

// sortStrings 名字要按固定顺序 —— 报错会进上下文, 抖动等于每次都是新东西
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
