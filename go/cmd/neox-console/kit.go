package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

/**
 * kit —— bot 搭前端的样式基底, 开机装到 ~/.neox-os/kit/.
 *
 *	默认 UI 需要有可复用的视觉基线和 skin 能力. 如果每个 demo 都现编一套
 *	设计系统, 结果容易失去一致性; 提供现成的令牌和组件, 拷进项目就有下限.
 *
 *	**每次开机覆盖**: kit 是系统的一部分, 跟着版本走 —— bot 拷进
 *	项目里的那份才是项目自己的, 想改在项目里改.
 *
 *	将来"动态下载的皮肤"就在这个目录上生长: 多套 kit 并存, 按名字选.
 */

//go:embed kit
var kitFS embed.FS

func installKit() {
	dir := filepath.Join(neoxHome(), "kit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "kit 目录建不出来:", err)
		return
	}
	entries, err := kitFS.ReadDir("kit")
	if err != nil {
		return
	}
	for _, entry := range entries {
		raw, err := kitFS.ReadFile("kit/" + entry.Name())
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), raw, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "kit 装不上(%s): %v\n", entry.Name(), err)
		}
	}
}
