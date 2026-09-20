package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// neoxHome 这台 OS 的状态根: 账本、名册、供应商配置、匿名工作区都在这儿.
//
// **必须能挪**. 不是为了灵活, 是为了"能起第二个实例而不碰第一个的数据":
// 用户的 console 正开着的时候, 一次真机验证如果写同一份 append-only 账本
// 和同一份名册, 轻则界面上多出几个分身, 重则两个写者把账本写坏 ——
// 而账本坏了是**不可逆**的, 那是全部历史.
//
// 默认仍然是 ~/.neox-os, 所以对用户什么都没变.
// secretPaths 家目录里 bot 碰不得的那几个文件 —— 见 agent/secrets.go.
// 路径跟写它们的地方(writeTokenFile / newProviderStore)对齐
func secretPaths() []string {
	return []string{filepath.Join(neoxHome(), "provider.json"), filepath.Join(neoxHome(), "token")}
}

func neoxHome() string {
	if v := strings.TrimSpace(os.Getenv("NEOX_HOME")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".neox-os")
}

// resolveWork 把用户填的项目目录翻成一个可以直接当能力 scope 用的绝对路径.
//
// 空 = 不指定, 由 workRoot 给一个匿名目录.
//
// ── 这里为什么要有策略 ──
//
// 这条路径会**原样变成内核给这个 bot 的 write 能力**. 也就是说填什么,
// 它就真的能改什么 —— 这是整条链上唯一一处由人决定边界的地方,
// 后面全是执行. 所以三条硬拒:
//
//	家目录本身 / 根   给的是"这台机器上你的一切". 用户想的是"帮我管文件",
//	                  实际给的是"可以删掉 ~/Documents". 差得太远, 不接受.
//	~/.neox-os 底下   那儿放着 provider.json(有 API key)、events.jsonl(全部
//	                  历史). bot 能写它 = 能读改自己的账本、能拿到密钥,
//	                  而"凭据不进被约束的进程"是这套东西的一条底线.
//	                  例外是 work/ —— 那本来就是分给 bot 的地方.
//	相对路径           相对谁? console 的 cwd 跟用户心里想的不是一个东西.
//	                  含糊的边界比窄的边界危险得多.
func resolveWork(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到家目录: %w", err)
	}
	if raw == "~" {
		raw = home
	} else if strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(home, raw[2:])
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("工作区要写绝对路径(或 ~/ 开头), 收到 %q", raw)
	}
	path := filepath.Clean(raw)
	if path == "/" || path == filepath.Clean(home) {
		return "", fmt.Errorf("不能把整个家目录或根目录派给一个 bot —— "+
			"给它一个具体的项目目录, 比如 %s", filepath.Join(home, "AI", "项目名"))
	}
	neox := neoxHome()
	work := filepath.Join(neox, "work")
	if within(path, neox) && !within(path, work) {
		return "", fmt.Errorf("%s 底下放着账本和密钥, 不能当工作区", neox)
	}
	return path, nil
}

// within a 是不是在 root 里面(或就是它).
//
// **必须按路径段比, 不能按字符串前缀**: /a/bc 会被 strings.HasPrefix
// 判成在 /a/b 里面, 于是一个叫 .neox-os-backup 的目录会被当成 .neox-os
// 底下的东西 —— 反过来也一样, 一个真在里面的目录可能被放行.
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// roleLabel 岗位说明压成**一句**, 给标签用.
//
// 标签跟着每条创建事件进账本, 而完整的岗位说明能有几百字 —— 那是每次
// 开机都要落一遍的钱, 而界面上只需要一句话.
func roleLabel(role string) string {
	one := strings.Join(strings.Fields(role), " ")
	// 按**字符**截不按字节: 中文一个字三字节, 按字节截会切出半个字
	r := []rune(one)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return one
}
