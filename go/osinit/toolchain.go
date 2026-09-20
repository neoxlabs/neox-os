package osinit

import (
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// 工具链自检 —— **这台 OS 派得动多少活, 取决于它脚下有什么**.
//
//	镜像形态自带整套工具链(Dockerfile.console 里装死的), 所以这份自检
//	在那儿永远是满的. 真正需要它的是**桌面客户端的本机模式**: bot 干活
//	用的是用户这台机器上已有的东西, 而一台干净的 Mac 上很可能连 git
//	都没有 —— 那种情况下派下去的第一摊活会以一句看不懂的报错收场.
//
//	**它只报告, 不代装**: 装系统级工具要提权、要选包管理器, 那是用户的
//	决定不是 OS 的. 所以每一条缺的都附一句**能直接粘的命令**, 到此为止.
//
//	远程模式下这份自检问的是**对面那台 OS**(容器里那套), 而不是客户端
//	所在的机器 —— 这正是想要的: 活是在那边干的.

// Tool 一件工具的下落.
type Tool struct {
	Name string `json:"name"`
	// Have 在不在. Path 在的话给出具体位置 —— "装了但不是我以为的那个"
	// 是真实发生过的事(brew 的 python3 和系统的 python3)
	Have bool   `json:"have"`
	Path string `json:"path,omitempty"`
	// Core 缺了会**明显影响**干活的那几件. 非 core 的缺了只是少条路.
	Core bool `json:"core"`
	// What 拿它干什么 —— 用户得知道值不值得为它装一趟
	What string `json:"what"`
	// Fix 一句能直接粘进终端的话. 按当前平台给, 给不出就空着.
	Fix string `json:"fix,omitempty"`
}

// Toolchain 一次自检的全部结果.
type Toolchain struct {
	// Platform 这台 OS 站在什么上面 —— 界面据此决定话怎么说
	Platform string `json:"platform"`
	Tools    []Tool `json:"tools"`
	// Missing 缺了几件 core 的 —— 界面只要一个数就能决定弹不弹
	Missing int `json:"missing"`
}

// toolSpec 要查哪些, 以及每件的说辞.
//
//	fixes 按 GOOS 分: 同一件东西在 mac 上是 brew、Debian 系是 apt、
//	Alpine 是 apk. 给错平台的命令比不给更糟 —— 用户粘进去只会看到
//	command not found, 然后以为是这个软件坏了.
type toolSpec struct {
	name  string
	core  bool
	what  string
	fixes map[string]string
}

var toolSpecs = []toolSpec{
	{
		name: "git", core: true,
		what: "bot 的工件以 git 为准 —— 没有它就没有 worktree、没有改动记录、没法回滚",
		fixes: map[string]string{
			"darwin": "xcode-select --install",
			"linux":  "sudo apt install git   # Alpine: apk add git",
		},
	},
	{
		name: "python3", core: false,
		what: "bot 写脚本、跑数据、做小工具最常伸手拿的那个",
		fixes: map[string]string{
			"darwin": "brew install python",
			"linux":  "sudo apt install python3 python3-pip   # Alpine: apk add python3 py3-pip",
		},
	},
	{
		name: "node", core: false,
		what: "bot 搭前端、跑 npm 要它。只让 bot 写文档和脚本的话可以不装",
		fixes: map[string]string{
			"darwin": "brew install node",
			"linux":  "sudo apt install nodejs npm   # Alpine: apk add nodejs npm",
		},
	},
	{
		name: "rg", core: false,
		what: "满仓库找东西快得多。没有的话 bot 退回 grep，慢但能用",
		fixes: map[string]string{
			"darwin": "brew install ripgrep",
			"linux":  "sudo apt install ripgrep   # Alpine: apk add ripgrep",
		},
	},
	{
		name: "curl", core: false,
		what: "bot 拉接口、下东西用",
		fixes: map[string]string{
			"darwin": "", // mac 自带, 走到这儿说明系统被改过, 给不出通用解法
			"linux":  "sudo apt install curl   # Alpine: apk add curl",
		},
	},
}

// whatEN 英文界面下每件工具的说辞. 键是工具名; 缺了就退回中文 —— 漏译只是
// 显示中文, 不会显示空的.
var whatEN = map[string]string{
	"git":     "Agent output is Git-backed: without it there are no worktrees, no change history, and no rollback",
	"python3": "The tool agents reach for most when writing scripts, processing data, or building small utilities",
	"node":    "Needed for front-end builds and npm. Not required if agents only write documents and scripts",
	"rg":      "Much faster repository-wide search. Without it, agents fall back to grep: slower but works",
	"curl":    "Used by agents to call APIs and download files",
}

// CheckToolchain 现查一遍.
//
//	**不缓存**: 用户看到提示、开另一个终端装完、回来点"再查一遍" ——
//	这是这个界面唯一的用法, 缓存会让它永远显示装之前的样子.
//
//	说辞按 NEOX_LANG 出: 这段话直接显示在首启那张卡上, 英文界面配中文说明
//	等于没翻. 装法命令不翻 —— 命令没有语言.
func CheckToolchain() Toolchain {
	english := strings.HasPrefix(strings.ToLower(os.Getenv("NEOX_LANG")), "en")
	out := Toolchain{Platform: runtime.GOOS, Tools: make([]Tool, 0, len(toolSpecs))}
	for _, spec := range toolSpecs {
		what := spec.what
		if english {
			if en, ok := whatEN[spec.name]; ok {
				what = en
			}
		}
		tool := Tool{Name: spec.name, Core: spec.core, What: what, Fix: spec.fixes[runtime.GOOS]}
		if path, err := exec.LookPath(spec.name); err == nil {
			tool.Have, tool.Path = true, path
		} else if spec.core {
			out.Missing++
		}
		out.Tools = append(out.Tools, tool)
	}
	return out
}

// handleToolchain GET /toolchain.
func (s *ObserveServer) handleToolchain(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, CheckToolchain())
}
