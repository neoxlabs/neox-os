package agent

import (
	"errors"
	"strings"
)

// 这两个工具只在**走了分支隔离**的机器上挂出来(见 WithGit).
// 真被调到而回调是空的, 说明接线出了问题 —— 说清楚, 别假装做了.
var errNoTeam = errors.New("这台机器上没有分支隔离，合入这件事得人来做")

/**
 * gitteam —— 分头干完之后, **它自己把活合上去**.
 *
 * ── 为什么这两件事要做成工具, 而不是让它自己敲 git ──
 *
 *	它手里有 run, 理论上什么 git 命令都敲得出来. 但"把自己的分支合进
 *	主干"这件事有一个它敲不出来的部分: 主干那个工作目录**不在它的
 *	写能力范围里**(隔离正是这么做到的). 它在自己的 worktree 里 merge
 *	是可以的, 但最后那一步必须由宿主来做.
 *
 *	更要紧的是顺序: 先把主干合进自己(冲突落在自己地盘, 自己解), 再让
 *	宿主做快进. 顺序反了就是"在主干上解冲突" —— 那会把一堆冲突标记
 *	留在所有人共用的那份代码里.
 *
 *	所以这里给的不是"一条 git 命令的壳", 是**那个顺序**.
 */

// MergeUpTool 把自己这条分支上的活合进主干.
//
//	merge 为 nil 时不挂这个工具 —— 不承诺做不到的事.
func MergeUpTool(merge func() (string, error)) Tool {
	return Tool{
		Name: "merge_up",
		// 声明只说"是什么". 判断和细节全在返回值里 —— 那份在用到的那一刻
		// 才付, 而这几行是每一轮都付.
		Desc: "把你干完的活合进主干",
		Args: map[string]string{},
		// 它改的是主干那份共用的代码 —— 一律当写操作, 不进并发批次
		Mutates: true,
		// 合完之后世界变了(主干上多了东西), 同一条命令再跑一次结果本该不同
		WorldSensitive: true,
		// **正常流程本来就是两步**: 第一次说"先验一遍", 验完再调一次.
		// 不声明的话停滞检测会因为它照着协议做而停掉它 —— 见 Tool.Shared
		Shared: true,
		Run: func(t Toolbox, _ map[string]any) (string, error) {
			if merge == nil {
				return "", errNoTeam
			}
			said, err := merge()
			/**
			 * 合成了就**单独发一条**.
			 *
			 *	这是所有事件里最该让人知道的一件: 主干变了, 而主干是
			 *	所有人共用的那一份. 用户可能正在别的会话里、也可能不在
			 *	电脑前 —— 埋在这个 bot 的工具结果里, 他只有点进这间屋子
			 *	才看得见.
			 *
			 *	判据是**这次真的合上去了**, 不是"调用没报错": 被"先验一遍"
			 *	拦下来也是正常返回, 那时候主干一个字都没变.
			 */
			if err == nil && strings.Contains(said, "合进主干") {
				t.Sys.Emit(map[string]any{"phase": "merged", "text": oneLine(said, 120)})
			}
			return said, err
		},
	}
}

// SyncDownTool 把主干上别人合进去的新东西拉到自己这儿.
//
//	跟 merge_up 分开是有意的: "我想在最新的代码上接着干"跟"我干完了要
//	合上去"不是同一个意图. 合成一个的话, 想同步的人被迫先把自己的
//	半成品合上主干.
func SyncDownTool(sync func() (string, error)) Tool {
	return Tool{
		Name:           "sync_down",
		Desc:           "把主干上的新东西拉到你这儿",
		Args:           map[string]string{},
		Mutates:        true,
		WorldSensitive: true,
		// 主干在别人手里变 —— 隔一会儿再拉一次是正当的, 不是原地转.
		// 见 Tool.Shared
		Shared: true,
		Run: func(_ Toolbox, _ map[string]any) (string, error) {
			if sync == nil {
				return "", errNoTeam
			}
			return sync()
		},
	}
}

// HandOverTool 把自己这摊活交给同项目的另一个人.
//
//	跟 handoff(交办一件事)**不是一回事**: 交办是"这件事你做", 我还在;
//	交接是"这摊活归你了", 我退出. 后者要把分支、进度、没做完的部分
//	一起给过去 —— 少一样接手的人就得从头猜.
func HandOverTool(hand func(to string) (string, error)) Tool {
	return Tool{
		Name: "hand_over",
		Desc: "把你这摊活交给同项目的另一个人",
		Args: map[string]string{
			"to": "接手的人叫什么",
		},
		ArgOrder:       []string{"to"},
		Mutates:        true,
		WorldSensitive: true,
		Run: func(_ Toolbox, args map[string]any) (string, error) {
			if hand == nil {
				return "", errNoTeam
			}
			to, _ := args["to"].(string)
			if strings.TrimSpace(to) == "" {
				return "", errors.New("要说清交给谁")
			}
			return hand(to)
		},
	}
}

// WithGit 给一份工具表接上"合入/同步".
//
//	两个都为 nil 时原样返回 —— 没走分支隔离的机器上, 工具表里就不该
//	出现这两个: 摆一个用不了的工具比没有更糟, 它会照着调、许下做不到的承诺.
func WithGit(tools []Tool, mergeUp, syncDown func() (string, error), handOver func(to string) (string, error)) []Tool {
	if mergeUp != nil {
		tools = append(tools, MergeUpTool(mergeUp))
	}
	if syncDown != nil {
		tools = append(tools, SyncDownTool(syncDown))
	}
	if handOver != nil {
		tools = append(tools, HandOverTool(handOver))
	}
	return tools
}

// oneLine 取头一行, 长了截断 —— 通知栏和一行的行里放不下一整段
func oneLine(text string, most int) string {
	if at := strings.IndexAny(text, "\r\n"); at >= 0 {
		text = text[:at]
	}
	runes := []rune(text)
	if len(runes) > most {
		return string(runes[:most]) + "…"
	}
	return text
}
