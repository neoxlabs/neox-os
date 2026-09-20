package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// 碰不得的东西 —— 模型的 API key、观察口 token.
//
// ── 需要防护的工作目录 ──
//
//	线上 bot 的工作目录就在 ~/.neox-os/work/ 下面, 它习惯 `cd /root/.neox-os`
//	去 grep 账本; 同一个目录里躺着 provider.json(模型 key)和 token(整台
//	机器的钥匙). 11:52 那一轮它还把 token 抄进一条 curl 去猜接口. 读权限
//	是 "/" —— 没有任何一处拦.
//
// ── 这一层是什么、不是什么 ──
//
//	**是**: 文件工具碰到这几个路径当场拒; 搜索走目录时跳过它们;
//	run 的命令里明着点名它们(或者 /proc/*/environ)也拒. 挡住的是
//	无意访问和出于好奇的访问.
//
//	**不是**: 安全边界. 一条故意绕的 shell(通配符、拼字符串)照样读得到,
//	因为 Linux 上 run 没有沙箱(见 sandbox_other.go). 真隔离要内核那一层
//	(landlock + 独立的 pid 命名空间), 那是 confine 那条路的事.

// secret 这个路径碰不得吗. p 是绝对路径.
func (t Toolbox) secret(p string) bool {
	if p == "" {
		return false
	}
	p = filepath.Clean(p)
	if procEnviron.MatchString(p) {
		return true
	}
	for _, s := range t.Secrets {
		s = filepath.Clean(s)
		if p == s || strings.HasPrefix(p, s+string(filepath.Separator)) {
			return true
		}
		// 符号链接绕一下也是同一个文件
		if real, err := filepath.EvalSymlinks(p); err == nil && real == s {
			return true
		}
	}
	return false
}

var procEnviron = regexp.MustCompile(`^/proc/[^/]+/environ$`)

// secretInCmd 命令里明着点名了碰不得的东西吗 —— 返回点名的那个.
func (t Toolbox) secretInCmd(cmd string) string {
	if strings.Contains(cmd, "environ") && strings.Contains(cmd, "/proc/") {
		return "/proc/*/environ"
	}
	for _, s := range t.Secrets {
		// 全路径, 或者 家目录下的相对写法(~/.neox-os/token、.neox-os/token)
		base := filepath.Base(s)
		parent := filepath.Base(filepath.Dir(s))
		for _, form := range []string{s, parent + "/" + base} {
			if strings.Contains(cmd, form) {
				return s
			}
		}
		// provider.json 这个名字本身就够特别, 单独出现也算
		if base == "provider.json" && strings.Contains(cmd, base) {
			return s
		}
	}
	return ""
}

// secretRefusal 拒的话 —— 说清为什么, 以及真要改配置该找谁.
func secretRefusal(what string) error {
	return fmt.Errorf("%s 是这台机器的密钥(模型 key / 访问 token), bot 不碰。"+
		"他要改模型或 key, 让他去设置页改", what)
}
