//go:build !darwin

package agent

// 这台机器上没有可用的沙箱.
//
// **不假装有**: 返回 false 之后, 提示词会照实说"这条边界靠你自觉",
// 设置页也会显示"没人强制". 说了做不到的话比没有保护更糟 ——
// 用户会因此放心把一个没验过的 bot 派进真实项目目录.
func CanSandbox() bool { return false }

func sandboxAvailable() bool { return false }

func sandboxWrap(root string, argv []string) []string { return argv }
