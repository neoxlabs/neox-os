//go:build darwin

package agent

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// 把 run 关进沙箱 —— 让"写只能写进你的工作区"这句话在没有内核约束的
// 宿主上也成真.
//
// ── 为什么非做不可 ──
//
// 提示词和设置页都说过"边界在内核, 你起的命令继承同一套规则".
// 开发模式的 console 走 dev(in-proc), bot 一条
// 「echo x 重定向到 /tmp/y」就把文件写到了工作区外面 —— 没有审批,
// 没有拦截.
//
// TRAPS A4: **边界只有被强制时才存在**. 而说了做不到的话, 后果不是
// "少一层保护", 是用户以为有保护而其实没有 —— 他会因此放心把一个
// 没验过的 bot 派进真实项目目录.
//
// ── 为什么是 sandbox-exec ──
//
// macOS 自带, 不用装东西、不用提权, 而且它是**内核层**的(Seatbelt),
// 不是包一层壳: 子孙进程一起受约束, nohup / & / setsid 都绕不过.
//
// 它被苹果标了 deprecated, 但仍然可用, 而且这条路的替代品(完整容器)
// 对一个桌面应用来说太重. **有约束比没约束强**, 而且这里的失败模式是
// 安全的: 沙箱起不来就退回"靠自觉", 提示词也跟着改口, 不会假装有保护.

// CanSandbox 这台机器能不能把命令关进沙箱.
//
// 宿主据此决定两件事: 要不要开 Toolbox.Sandbox, 以及**提示词里那句话
// 怎么说** —— 关得住才敢说"由内核挡着".
func CanSandbox() bool { return sandboxAvailable() }

// sandboxAvailable 这台机器能不能把命令关进沙箱.
func sandboxAvailable() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// sandboxWrap 把 argv 包进沙箱. root 是**唯一**允许写的目录.
//
// 用 -p 传内联规则而不是写一个临时规则文件: 规则文件本身要写到磁盘上,
// 而"能写哪儿"正是这里要限制的事 —— 那是个先有鸡还是先有蛋的问题.
func sandboxWrap(root string, argv []string) []string {
	return append([]string{"sandbox-exec", "-p", sandboxProfile(root)}, argv...)
}

// sandboxProfile 规则本身.
//
//	**只拦写, 不拦读**: 读整台机器是它本来就有的能力(read 轴给的是 "/"),
//	而且拦读会让编译器、解释器一大片工具直接不能用 —— 那不是收紧边界,
//	那是让这个环境没法干活.
//
//	/dev 那几个必须放行: 不放行的话 shell 连重定向都做不了,
//	报出来的错(Operation not permitted)跟真正的原因八竿子打不着.
func sandboxProfile(root string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n(allow file-write*\n")
	// **两个路径都要放行**: macOS 把符号链接解析之后再判规则,
	// 而 /tmp 是 /private/tmp、/var 是 /private/var —— 只写没解析的那个,
	// 沙箱会把**工作区自己的写**也拦掉.
	//
	// 这一条是测试当场抓出来的: 临时目录在 /var/folders 下, 规则里写的是
	// /var/... 而内核看到的是 /private/var/..., 于是 echo 到工作区里
	// 都报 Operation not permitted —— 而报错跟真正的原因八竿子打不着.
	for _, p := range sandboxPaths(root) {
		b.WriteString("  (subpath " + sandboxQuote(p) + ")\n")
	}
	// 共用那份下载缓存也得能写 —— 见 agent/cache.go.
	// 不放行的话 npm 每条命令都会先撞一次权限错, 然后退回冷下载.
	for _, p := range sandboxPaths(sharedCache()) {
		if p != "" {
			b.WriteString("  (subpath " + sandboxQuote(p) + ")\n")
		}
	}
	/**
	 * xcrun 的缓存**必须放行**.
	 *
	 *	macOS 上 /usr/bin/git(还有 clang、python3 那一批)是个 shim,
	 *	它每次都要调 xcrun 去找真正的可执行文件, 而 xcrun 会往系统临时
	 *	目录写一个 xcrun_db 缓存. 那个目录是 confstr 拿的, **环境变量
	 *	改不了**(设 TMPDIR 没用).
	 *
	 *	拦掉的后果不是"少写一个缓存": 每条 git 命令都会先吐一行
	 *	"couldn't create cache file … Operation not permitted".
	 *	这行字会让 bot 误以为是自身权限不够, 去试各种绕法,
	 *	最后甚至会写 grep -v xcrun 来把它过滤掉.
	 *
	 *	放行的只是那一个缓存文件名, 不是整个临时目录.
	 */
	b.WriteString(`  (regex #"^/private/var/folders/.+/xcrun_db")` + "\n")
	b.WriteString(`  (literal "/dev/null") (literal "/dev/stdout") (literal "/dev/stderr")` + "\n")
	b.WriteString(`  (literal "/dev/dtracehelper")` + "\n")
	b.WriteString(`  (regex #"^/dev/tty") (regex #"^/dev/fd/"))` + "\n")
	return b.String()
}

// sandboxQuote 路径进规则文件要转义.
//
// 不转义的话, 一个名字里带引号的目录就能把规则**改写成别的意思** ——
// 那是注入, 而这里注入的后果是沙箱形同虚设.
func sandboxQuote(path string) string {
	escaped := strings.ReplaceAll(path, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// sandboxPaths 一个目录在规则里要写几种形态.
//
// macOS 判规则用的是**解析过符号链接**的真身. 两个都放行最省事,
// 而且多放一个自己的别名不会扩大边界 —— 它们指的是同一个地方.
func sandboxPaths(root string) []string {
	out := []string{root}
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		out = append(out, real)
	}
	return out
}
