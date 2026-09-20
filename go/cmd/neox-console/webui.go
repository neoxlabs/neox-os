package main

import (
	"embed"
	"io/fs"
)

/**
 * 内嵌的 Web 界面 —— 自托管形态的入口.
 *
 *	docker pull 装完之后用户拿什么打开? 不能再让他装一个桌面客户端.
 *	所以界面直接打进二进制: 浏览器打开观察口就是完整的 UI,
 *	Electron 降级成可选的桌面壳(它自己带 dist, 不走这条路).
 *
 *	webui/ 里的内容由 packages/apps/console 的 os:pack 构建时拷进来,
 *	不进 git(见 .gitignore). 没打进来的时候 uiFS 返回 nil,
 *	观察口照常只开 API —— 开发期 go build 不依赖前端构建.
 */

//go:embed all:webui
var webuiRaw embed.FS

// uiFS 打进来的界面. nil = 这次构建没带界面.
func uiFS() fs.FS {
	sub, err := fs.Sub(webuiRaw, "webui")
	if err != nil {
		return nil
	}
	// 只认真正的界面: 占位文件(.gitkeep)不算 —— 半个界面比没有更糟,
	// 用户打开是一页 404 还以为服务坏了
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
