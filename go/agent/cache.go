package agent

import (
	"os"
	"path/filepath"
	"strings"
)

/**
 * 下载缓存 —— **一台机器上只装一遍**.
 *
 * ── 每人一份缓存的代价 ──
 *
 *	两个人做同一个 React 应用, 小甲搭好脚手架合进主干, 小乙拉下来只改
 *	一个 index.css. 小乙的第一条命令是 npm install, 跑了 **3 分钟没回来**
 *	被掐掉, 换 --prefer-offline 重来 —— 整轮 600 秒就耗在这儿, 报出来是
 *	"600秒还没干完", 而它一行样式都还没提交.
 *
 *	隔离把 HOME 指进各自的工作区(见 commandEnv), 于是
 *	~/.npm/_cacache 也跟着一人一份. 每多一个人, 同样那几百个包就重下
 *	一遍 —— 而且是冷的, 连上一轮自己下过的都不算.
 *
 * ── 为什么可以共用 ──
 *
 *	缓存里放的是从公网下回来的包, 不是谁的代码. 隔离要挡的是
 *	"改到别人的活", 而不是"少下一遍 react". 共用一份既不越界, 也正是
 *	缓存这个东西存在的理由.
 *
 *	工作区里那份 HOME 照旧 —— 工具往家里写的别的东西(配置、日志)
 *	仍然落在自己地盘, 只有这几个明确的下载缓存指出去.
 */
func sharedCache() string {
	home := os.Getenv("NEOX_HOME")
	if strings.TrimSpace(home) == "" {
		base, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(base, ".neox-os")
	}
	dir := filepath.Join(home, "下载缓存")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "" // 建不出来就退回一人一份 —— 慢, 但不会因此跑不起来
	}
	return dir
}

// cacheEnv 把各家包管理器的下载缓存指到共用那份.
//
//	只点名**下载缓存**这一类. 别的(构建产物、虚拟环境、装好的包本身)
//	留在各自工作区里 —— 那些是这个人的活, 混在一起就是串台了.
func cacheEnv(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{
		"npm_config_cache=" + filepath.Join(dir, "npm"),
		"YARN_CACHE_FOLDER=" + filepath.Join(dir, "yarn"),
		"PIP_CACHE_DIR=" + filepath.Join(dir, "pip"),
		"UV_CACHE_DIR=" + filepath.Join(dir, "uv"),
		"GOMODCACHE=" + filepath.Join(dir, "gomod"),
	}
}
