package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 工具表 —— 可定制.
//
// 工具不是硬编码的三个, 而是一张表. 加一个工具 = 往表里塞一条,
// 提示词会自动跟着变 —— 不用两处改.
//
// 一旦审批下沉到内核, 工具就不再是安全边界, 只是便利层.
// 所以工具**该怎么方便怎么定**, 安全由能力集和内核管.

type Tool struct {
	Name string
	// Desc 一句话说清它干什么. 会原样进提示词, 所以要短
	Desc string
	// Args 参数名 → 一句话说明. 顺序由 ArgOrder 定, 保证提示词字节稳定
	Args     map[string]string
	ArgOrder []string
	// Optional 哪些参数可以不给. 其余的缺了就直接拒 ——
	// **不能让缺参数变成一句系统错误**: 模型漏传 path 时,
	// 报出来的是 "read /agentwork: is a directory", 它完全看不懂,
	// 只能瞎猜着重试, 白烧一轮.
	// 参数校验要在工具跑之前做, 并说清缺了什么、这个工具要什么.
	Optional map[string]bool
	/**
	 * MayBeEmpty 哪些参数**空着也算给了**.
	 *
	 *	缺省把空串当成"没给" —— 对 path、pattern 这些是对的(一个空路径
	 *	什么也做不了). 但 write_file 的 content 不是: **写一个空文件是
	 *	正当的**, tests/__init__.py 就是那个经典例子.
	 *
	 *	否则创建 tests/__init__.py 会收到
	 *	"调 write_file 缺参数: content …你这次给的是: content=, path=…"
	 *	—— 一句自相矛盾的话(说缺, 又把它列出来了), 虽然调用本身正确.
	 */
	MayBeEmpty map[string]bool
	// Needs 这个工具要哪条能力轴, ScopeArg 说明哪个参数是它的 scope.
	//
	// **声明代替猜名字.** 原来预检是靠工具名前缀猜的 (write/edit/delete
	// 开头算写), 漏了 edit 一次, 于是 edit_file 越界时直接撞内核拿 EPERM,
	// 用户从来没被问过 —— 审批路径整个失效. 加一个工具就要记得改那个前缀表,
	// 这种"隐式白名单"迟早会再漏一次.
	//
	// Needs 为空表示这个工具不碰任何受管资源, 不做预检.
	Needs    abi.CapAxis
	ScopeArg string
	// ScopeNorm 把参数值翻成**能力集认识的那个 scope**. nil = 原样用.
	//
	// 需要它是因为参数和 scope 不总是同一个东西: `fetch` 的参数是一整条
	// URL, 而 net 轴的 scope 语义是主机名 (confine.HostMatches 是唯一实现).
	// 不翻的话拿 "https://pypi.org/simple/x" 去比 "pypi.org" 永远不匹配 ——
	// 于是**每取一个页面都问一次人**, 用户还看不懂自己在批什么.
	//
	// 归一化只影响预检和问人时那句话. 真强制在代理: 它看的是 CONNECT 的
	// 主机名, 跟这里翻出来的是同一个东西, 所以两边不会分叉.
	ScopeNorm func(string) string
	// Mutates 会不会**改变**世界的状态. 决定能不能进并发批次.
	//
	// 同样是声明而非猜名字: `run` 不以 write/edit/delete 开头, 但它
	// 什么都干得了 —— 按名字判会把它当只读工具丢进并发批次.
	Mutates bool
	/**
	 * Shared 这个工具的结果**由别人决定**, 不是由它自己写了什么决定.
	 *
	 * ── 为什么非要单独一条 ──
	 *
	 *	停滞检测的"世界"只数**它自己的写**: 改了文件, 世界就变了,
	 *	同样的读就不算原地转. 那套判据对 read/run 是对的.
	 *
	 *	而 merge_up / sync_down 的结果取决于**主干和同屋的人** ——
	 *	它自己一个字都没写, 世界照样会变. 更要命的是: merge_up 的
	 *	正常流程**本来就是两步**(第一次先验一遍, 验完再调一次).
	 *	重复调用判据若把这两步视为原地转, 就会按系统要求再次调用后
	 *	又因为这次调用而停止执行.
	 *
	 *	声明成 Shared 的工具不参与"重复调用"那条判据 —— 跑飞照样由
	 *	反复失败、步数、花费三条线兜着.
	 */
	Shared bool
	// WorldSensitive 同样的调用在世界变了之后**会不会给出不同结果**.
	//
	// ── 这跟 Mutates 是两件事, 混了就必然误报 ──
	//
	//	          改变世界   结果依赖世界
	//	write/edit   是         否   同样内容写两遍, 世界怎么变都是原地转
	//	read/search  否         是   写过之后再读同一个文件, 结果本该不同
	//	run          是         是   **唯一两者都占的**
	//
	// 把 run 只声明成 Mutates 时, 它的指纹会按写工具
	// 的规矩算(工具+path+改动内容), 而 run 既没有 path 也没有 content ——
	// **所有 run 调用指纹全都一样**, 不同命令互相撞.
	// 而且改完代码再跑一次同一条测试被判成原地转 —— 那恰恰是它该做的事.
	WorldSensitive bool
	// Timeout 这个工具单次调用的上限. 0 = 用全局缺省.
	//
	// 必须能按工具定: 一次 read_file 跑 30 秒是文件系统坏了,
	// 而一次 `go test ./...` 跑 3 分钟完全正常. 用同一个数字砍两者,
	// 要么放过真卡死, 要么把正常的活砍掉.
	Timeout time.Duration
	// WaitsForHuman 这个工具会阻塞等待决策 —— **一律不设时限**.
	//
	// 时间闸砍的是"机器不响应", 不是"人还没回答". 这两件事混了就会
	// 砍掉这个 OS 最核心的那条性质: 进程可以等人几小时而不占计算.
	//
	// request_access 弹出决策后, 30 秒的通用工具时限会把它砍掉 —— 报出来的还是一句
	// "可能路径指向一个很大的目录树", 跟真实原因八竿子打不着.
	// 决策本身还挂在那儿, 而 agent 已经当它失败了, 接着去瞎试别的.
	WaitsForHuman bool
	// Run 真正干活的. path 已由调用方做过能力预检.
	//
	// 返回的字符串是给**模型**看的, 不是给人看的日志 ——
	// 所以要写成"下一步该怎么办"的形式, 而不是一句状态播报.
	// 例: 文件不存在时说"用 list_dir 看看真实文件名", 而不是 "ENOENT".
	Run func(t Toolbox, args map[string]any) (string, error)
}

// Toolbox 工具能用的环境
type Toolbox struct {
	// Root 工作目录. 相对路径都相对它
	Root string
	// AutoHire 拉人不用问 —— 用户在权限页开的档, 现问现用.
	// nil/false = 老规矩, 每次拉人都过审批卡
	AutoHire func() bool
	// Ctx 用来取消**我们自己写的循环** (search 走目录树、glob 遍历).
	//
	// 单次 os.ReadFile 之类的系统调用打断不了 —— 那是内核的事.
	// 但我们自己的循环能查, 而恰恰是那些循环才会真的跑很久:
	// 一个 search 在超大目录树上能走几分钟, 而 read 一个文件不会.
	//
	// nil 表示不取消 —— 测试和简单场景不用管它.
	Ctx context.Context
	// Sys 通向 OS 的系统调用. 只有需要 OS 服务的工具才用得着
	// (目前是 request_access —— 申请能力只能由 OS 决定).
	//
	// nil 表示没有 OS (测试/离线场景), 相关工具要自己说清楚而不是崩.
	Sys Syscalls
	// Sandbox 把 run 起的命令关进沙箱, 只许写 Root.
	//
	//	给**没有内核约束的宿主**用(console 是 in-proc 的). 真内核那条路
	//	不需要它: landlock 已经在管了, 再套一层只是多一个失败点.
	//
	//	能不能关得住由 CanSandbox() 说了算 —— 关不住就照实说"靠你自觉",
	//	不假装有保护.
	Sandbox bool
	// Seen 这个 bot 读到过哪些文件、读到的是哪一版.
	//
	//	用来挡"两个人写同一个文件, 一个人的活悄悄没了" —— 拉人进来是
	//	同一个工作区, 这件事迟早发生. nil = 不挡(单人场景/测试).
	Seen *seenFiles
	// Heard 他最近几句原话(这一轮的 + 前几轮的), 最新的在最后.
	//
	//	给"要写他的数据"的那几个工具核对用: 立规矩(notify_when)要填
	//	他的原话, 记待办(agenda)要是他交代的事. 真机 08:49 他问"我设了
	//	哪些提醒", 它加了两条待办和一条"我上课呢接不了电话, 你先给我拦住"
	//	—— 这句话他从来没说过. nil = 不核对(测试/没有对话的场景).
	Heard func() []string
	// Secrets 碰不得的文件(模型 key、token) —— 见 secrets.go. 空 = 不拦
	Secrets []string
	// Relayed 这一轮的活是**同屋的人交办的**, 不是用户交代的.
	//
	//	handoff 靠它卡住转包: 被交办的人不能再往下转. 见 handoff.go.
	Relayed bool
	// Started 每起一个子进程叫一次, 参数是它的**进程组 id**.
	//
	//	提示词里对模型许过一句: "你起的后台进程活不过这段对话".
	//	那句话在真内核那条路上是命名空间保证的; 而 console 这条路是
	//	in-proc 的, **没有命名空间** —— 一个 python -m http.server &
	//	能一直活到 app 退出, 占着端口.
	//
	//	不承诺做不到的事: 要么改口, 要么让它成真. 这里选后者 ——
	//	宿主记下每个进程组, 这段对话结束时一起收掉.
	//	nil = 不记(测试/别的宿主自己管).
	Started func(pgid int)
	/**
	 * Elsewhere 一份**同一个项目的另一个地址**.
	 *
	 *	走分支隔离之后, 它干活的地方从项目根挪到了自己的 worktree.
	 *	但**旧地址会一直冒出来**: 历史记录、计划文件和交办内容里
	 *	写的都是项目根下的路径. 直接使用旧地址会被能力拦下, 随后申请
	 *	写整个项目根又会让隔离失效.
	 *
	 *	所以不是放权, 是**把旧地址落到它自己的那份副本上**:
	 *	<项目根>/x 就是 <它的工作区>/x, 同一个文件的两个地址.
	 *	空 = 没有第二个地址(没走隔离), 什么都不做.
	 */
	Elsewhere string
	/**
	 * Missing 一个路径不存在的时候, **别人那儿有没有**.
	 *
	 *	几个人分头干活的时候, "这个文件不存在"最常见的原因不是路径写错,
	 *	而是**写它的人还没合上来**. 路径缺失时若只提示重试或列目录,
	 *	调用方可能创建重复文件; 它需要知道的是该文件仍在另一条分支上.
	 *
	 *	这件事 agent 自己答不出来: 它看得见的只有自己那份工作区.
	 *	所以留一个口子, 由知道分支布局的宿主来答.
	 *	nil = 没人答得上来, 什么都不补.
	 */
	Missing func(rel string) string
	/**
	 * OnRan 每跑完一条命令叫一声, 带**真实退出码**.
	 *
	 *	验收这件事只能这么做. 让模型自己说"我验过了、通过了"是没用的:
	 *	那句话跟"我没验但我觉得没问题"在文本上一模一样, 而它偏偏最爱
	 *	在收尾时说前者. 退出码是它编不出来的东西 —— 命令是它挑的,
	 *	结果不是它给的.
	 */
	OnRan func(cmd string, exit int)
}

// canceled 我们自己的循环该不该停下
func (t Toolbox) canceled() bool {
	if t.Ctx == nil {
		return false
	}
	select {
	case <-t.Ctx.Done():
		return true
	default:
		return false
	}
}

func (t Toolbox) resolve(p string) string {
	if !filepath.IsAbs(p) {
		return filepath.Join(t.Root, p)
	}
	// 旧地址落到自己的副本上 —— 见 Elsewhere
	if t.Elsewhere != "" {
		if rel, err := filepath.Rel(t.Elsewhere, p); err == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return filepath.Join(t.Root, rel)
		}
	}
	return p
}

// okResult 结构化的成功结果.
//
// 为什么不是纯字符串: 模型要能分清 "success" 和 "already_done" ——
// 前者意味着刚改过, 后者意味着本来就是目标状态.
// 分不清的话它会在 already_done 之后再改一次, 或者反过来以为没生效.
//
// 只有两个字段, 刻意不做成大对象: 字段越多模型越容易只读一半.
func okResult(status, detail string) string {
	return fmt.Sprintf(`{"status":"%s","detail":%q}`, status, detail)
}

// actionable 把系统错误包成"下一步该怎么办".
//
// 模型看到 "ENOENT" 只会瞎试; 看到"先 list_dir 看看真实文件名"才会做对.
// 错误信息是提示词的一部分 —— 它是唯一在**出错那一刻**才被读到的指令.
// notFound 路径不存在那条报错 —— 顺带问一句"别人那儿有没有".
//
//	见 Toolbox.Missing: 几个人分头干活的时候, 最常见的原因不是路径写错.
func (t Toolbox) notFound(err error, path, hint string) error {
	if os.IsNotExist(err) {
		/**
		 * **"名字写错了"和"还没有这个东西"是两件事**.
		 *
		 *	原来一律说"别重试同一路径——先 list_dir 看看真实的文件名",
		 *	那句话断言的是**名字错了**. 而账本里最高频的那几条恰恰不是:
		 *
		 *	    PLAN.md: 文件不存在。别重试同一路径——先 list_dir…   ×5
		 *	    CHARTER.md: 文件不存在。…                          ×2
		 *
		 *	它按规矩开工先读章程, 而那个项目还没人建过章程 —— 路径一个
		 *	字都没错. 被这么一说, 它去 list_dir、去猜别的名字, 白花一步.
		 *
		 *	上一级在不在, 是这两件事之间唯一查得实的分界: 上一级都没有,
		 *	那多半真是路径不对; 上一级好好的而它不在, 那就是还没建.
		 */
		if parentThere(t.resolve(path)) {
			out := fmt.Errorf("%s: 这儿还没有这个东西", path)
			if t.Missing != nil {
				if note := t.Missing(path); note != "" {
					return fmt.Errorf("%v。%s", out, note)
				}
			}
			return fmt.Errorf("%v —— 要就自己建一份", out)
		}
	}
	out := actionable(err, path, hint)
	if t.Missing == nil || !os.IsNotExist(err) {
		return out
	}
	if note := t.Missing(path); note != "" {
		return fmt.Errorf("%v。%s", out, note)
	}
	return out
}

// parentThere 这个路径的上一级在不在 —— 分开"名字写错了"和"还没建"
func parentThere(full string) bool {
	info, err := os.Stat(filepath.Dir(full))
	return err == nil && info.IsDir()
}

func actionable(err error, path, hint string) error {
	if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
		return fmt.Errorf("%s: %s", path, hint)
	}
	if os.IsPermission(err) || strings.Contains(err.Error(), "permission denied") {
		return fmt.Errorf("%s: 权限被拒。这个路径在你的可写范围外——"+
			"别换个写法重试(那只会再打扰用户一次), 直接告诉用户你需要什么权限", path)
	}
	return fmt.Errorf("%s: %v", path, err)
}

func argStr(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

// ReadOnly 这个工具会不会改东西.
//
// 并行执行只对只读工具安全: 两个写操作可能落在同一个文件上,
// 并发跑结果不确定. 读操作之间没有这个问题.
func (t Tool) ReadOnly() bool { return !t.Mutates }

// DefaultTools 缺省工具表. 想加就往这里加, 或者自己组一张表传进去.
// DefaultTools 缺省工具表.
//
// remind 为 nil 时不加 remind_me —— **不承诺做不到的事**:
// 没有闹钟服务的环境里摆着这个工具, 模型会照着调然后拿到一句
// "这个环境里没有闹钟服务", 而它已经对用户许过诺了.
func DefaultToolsWith(remind func(atMs int64, text string) error,
	namePlace func(string) (string, error),
	watch func(kind, place, say, raw string) error,
	cancel func(id string) (string, error),
	search func(query string, limit int) ([]abi.SearchHit, error),
	see func(mediaType, dataB64, question string) (abi.SeeResult, error),
	recall func(query string, limit int) ([]abi.RecallHit, error),
	hire func(name, role string) (string, error),
	pass func(who, task string) (string, error)) []Tool {
	ts := DefaultTools()
	// 认图的机器上, read_file 撞到图片要指向 view_image;
	// 不认图的机器上那句话会指向一个不存在的工具, 所以两处必须一起变
	if see != nil {
		ts = append(ts, ViewImageTool(see))
		// read_file 和 fetch 撞上图片时都会说"下一步用什么",
		// 两句话都要跟着这台机器变
		for i := range ts {
			switch ts[i].Name {
			case "read_file":
				ts[i] = readFileTool(true)
			case "fetch":
				ts[i] = fetchTool(true)
			}
		}
	}
	// search 为 nil = 这台机器没配搜索服务. **同一条规矩**:
	// 摆着这个工具的话, 模型会照着调然后拿到一句"没配搜索",
	// 而它已经对用户许过诺了.
	//
	// 注意 fetch 不在这个开关下 —— 它不需要任何配置,
	// 出网能力有没有由能力集说了算, 而那是运行时才知道的事.
	if search != nil {
		ts = append(ts, SearchTool(search))
	}
	if recall != nil {
		ts = append(ts, RecallTool(recall))
	}
	// 交办: 只有在房间里才挂 —— 一个人待着的 bot 没有"同屋的人",
	// 摆着这个工具它会照着调, 然后拿到一句"这儿没有别人"
	if pass != nil {
		ts = append(ts, HandoffTool(pass))
	}
	// 拉人: 这台机器起得了新 bot 才挂. 同一条规矩 ——
	// 摆一个用不了的工具, 它会照着调、许下做不到的承诺
	if hire != nil {
		ts = append(ts, RecruitTool(hire))
	}
	if remind != nil {
		// daily 那一半由 WithDaily 补上 —— DefaultToolsWith 的参数已经太多
		ts = append(ts, RemindTool(remind, nil))
	}
	if watch != nil {
		ts = append(ts, WatchTool(watch))
	}
	// **能加就得能减.** 一个只能加不能减的系统, 用起来一次就够让人
	// 不敢再用: 说错一句话就永久多一条通知
	if cancel != nil {
		// list 那一半由 WithPending 补上 —— DefaultToolsWith 的参数已经太多
		ts = append(ts, CancelTool(cancel, nil))
	}
	return ts
}

func DefaultTools() []Tool {
	return []Tool{
		accessTool(),
		editTool(),
		fetchTool(false),
		globTool(),
		runTool(),
		searchTool(),
		showTool(),
		{
			Name: "list_dir", Desc: "列目录",
			Needs: abi.AxisRead, ScopeArg: "path", WorldSensitive: true,
			Args: map[string]string{"path": "目录路径, 不给就是当前目录"}, ArgOrder: []string{"path"},
			// path 可省 —— 不给就是当前目录, 这是有意义的默认
			Optional: map[string]bool{"path": true},
			Run: func(t Toolbox, a map[string]any) (string, error) {
				p := argStr(a, "path")
				if p == "" {
					p = "."
				}
				entries, err := os.ReadDir(t.resolve(p))
				if err != nil {
					return "", t.notFound(err, p, "目录不存在。用 list_dir 看看上一级有什么")
				}
				var names []string
				for _, e := range entries {
					n := e.Name()
					if e.IsDir() {
						n += "/"
					}
					names = append(names, n)
				}
				if len(names) == 0 {
					return "(空目录)", nil
				}
				return strings.Join(names, " "), nil
			},
		},
		readFileTool(false),
		{
			Name: "write_file", Desc: "写文件(会覆盖)",
			Args:     map[string]string{"path": "文件路径", "content": "完整内容"},
			ArgOrder: []string{"path", "content"},
			// 空文件是正当的(tests/__init__.py) —— 见 Tool.MayBeEmpty
			MayBeEmpty: map[string]bool{"content": true},
			Needs:      abi.AxisWrite, ScopeArg: "path", Mutates: true,
			Run: func(t Toolbox, a map[string]any) (string, error) {
				p, c := argStr(a, "path"), argStr(a, "content")
				full := t.resolve(p)
				/**
				 * **不许盖掉你没读过的那一版**.
				 *
				 *	write_file 原来是无条件覆盖的. 同一个工作区里两个 bot
				 *	一起干活时, 后写的那个会把先写的整份盖掉 —— 两边都
				 *	显示成功, 而其中一个人的活没了, 谁都不会发现.
				 *
				 *	这是提示词里那条("不改你没读过的东西")的强制版:
				 *	原来只是劝, 现在真的拦得住.
				 */
				if stale, why := t.Seen.stale(full); stale {
					return "", blockedf("%s: %s。你手里这份和磁盘上那份对不上, "+
						"整份写回去会把中间那次改动盖掉 —— 先 read_file "+
						"看一眼现在是什么样", p, why)
				}
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return "", actionable(err, p, "建不了父目录——可能超出了可写范围")
				}
				if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
					return "", actionable(err, p,
						"写不了。是权限的话, 这个路径不在你的可写范围内")
				}
				// 自己刚写的这一版就是它"读到过"的那一版 —— 否则接着再写
				// 一次会被自己的闸拦住
				t.Seen.note(full, []byte(c))
				// 明确提示下一步 —— 写完必须回读, 这是唯一的验证手段
				return okResult("success", fmt.Sprintf(
					"已写入 %s (%d 字节)。用 read_file 回读确认。", p, len(c))), nil
			},
		},
	}
}

// readFileTool 读文件.
//
// canSee = 这台机器上有没有 view_image. 它只影响**撞上图片时说什么**:
//
//	有   "这是图片, 用 view_image 看"
//	没有 "这台机器不认图" —— 而不是指向一个不存在的工具
//
// 这跟提示词里那条搜索策略是同一个毛病的同一种修法: 指向一个不存在的
// 工具, 模型会照着调然后撞墙, 而它没有任何线索可以自纠.
func readFileTool(canSee bool) Tool {
	return Tool{
		Name: "read_file", Desc: "读文件。大文件会分片, 按提示用 offset 接着读",
		Args: map[string]string{
			"path":   "文件路径",
			"offset": "从第几行开始读, 1 起. 不给就从头",
		},
		ArgOrder: []string{"path", "offset"},
		Optional: map[string]bool{"offset": true},
		Needs:    abi.AxisRead, ScopeArg: "path", WorldSensitive: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			p := argStr(a, "path")
			b, err := os.ReadFile(t.resolve(p))
			if err != nil {
				return "", t.notFound(err, p,
					"文件不存在 —— 上一级里真实的文件名用 list_dir 看得到")
			}
			// **记下它读到的这一版**: 之后 write_file 拿它来判"我读过之后
			// 有没有人动过". 分片读也算读过 —— 覆盖的是整个文件,
			// 而它至少看过其中一段, 比完全没看过强得多
			t.Seen.note(t.resolve(p), b)
			if len(b) == 0 {
				return "(空文件)", nil
			}
			// 图片单独说 —— 它跟"一个 ELF 可执行文件"完全不是一回事:
			// 后者读进来确实没用, 而图片是**有内容的**, 只是得换个工具看
			if _, isImg := imageType(b); isImg {
				if canSee {
					return "", fmt.Errorf("%s 是图片, read_file 读不了 —— 看图用 view_image", p)
				}
				return "", fmt.Errorf("%s 是图片, 而这台机器**不认图**(没配视觉模型)", p)
			}
			if isBinary(b) {
				return "", fmt.Errorf(
					"%s 是二进制文件, 读进来对你没用还会挤爆上下文。"+
						"如果你要的是文件大小/存在性, list_dir 就够了", p)
			}
			return readSlice(p, string(b), argInt(a, "offset")), nil
		},
	}
}

// 分片读 —— 一个大文件不能把上下文冲垮.
//
// 上下文是**跨轮累积**的: 一个 200KB 的文件读两次就把窗口占满了,
// 而且它挤掉的是前面的对话历史 —— 代价比看不到文件尾部大得多.
//
// 两条铁律:
//
//	① 截断必须**说出来**, 并且给出接着读的确切办法.
//	   静默截断最糟: 模型以为自己看完了整个文件, 然后基于半截内容下结论.
//	② 输出**不许加行号前缀**. 编辑是内容寻址的 —— 模型要逐字复制它看到的原文,
//	   加了行号它会把行号一起复制进 old_string, 永远匹配不上.
//	   (行号只出现在"提示怎么接着读"那句话里, 不混进内容.)
const (
	readMaxLines = 400
	readMaxBytes = 24 * 1024
)

func readSlice(path, content string, offset int) string {
	return sliceOf("read_file", "path", path, content, offset)
}

// sliceOf 分片, 并且**用调用方自己的工具名和参数名**告诉它怎么接着读.
//
// 分片规则对 read_file 和 fetch 是同一条 (判据①: 有没有第二个地方在回答
// 同一个问题), 但那句"接着读"的提示不能共用一份写死的文字 —— 对一个
// fetch 回来的网页说 "read_file path=https://…" 是**教它做一件做不成的事**,
// 它会照着调, 然后拿一句"文件不存在"再猜一轮.
func sliceOf(tool, argName, target, content string, offset int) string {
	lines := strings.Split(content, "\n")
	if offset < 1 {
		offset = 1
	}
	if offset > len(lines) {
		return fmt.Sprintf("%s 只有 %d 行, 没有第 %d 行。整个内容你已经读完了。",
			target, len(lines), offset)
	}
	start := offset - 1

	// 单行就超上限 —— minified bundle 就是这样, 整个文件可能只有一行.
	// 按行数算的上限对它完全无效 (单元测试抓到: 200KB 单行原样吐出来了),
	// 所以这里按字节硬砍.
	// 砍到一半的行是**没法用 edit_file 改**的, 必须说清楚, 否则模型
	// 会拿着半截原文去匹配, 然后困在 string_not_found 里.
	if len(lines[start]) > readMaxBytes {
		return fmt.Sprintf(
			"(第 %d 行有 %d 字节, 太长, 只给你前 %d 字节。"+
				"这一行被砍断了, **不要拿它当 edit_file 的 old_string** —— 匹配不上。"+
				"要改这种文件就用 write_file 整个重写。)\n%s",
			offset, len(lines[start]), readMaxBytes, lines[start][:readMaxBytes])
	}

	end, bytes := start, 0
	for end < len(lines) && end-start < readMaxLines && bytes < readMaxBytes {
		bytes += len(lines[end]) + 1
		end++
	}
	body := strings.Join(lines[start:end], "\n")

	// 完整读完 (且是从头读的) —— 不加任何噪音, 保持内容原样
	if start == 0 && end == len(lines) {
		return body
	}
	if end >= len(lines) {
		return fmt.Sprintf("(第 %d-%d 行, 到文件末尾)\n%s", offset, end, body)
	}
	// 还有后文 —— 说清楚怎么接着读, 别让它猜
	return fmt.Sprintf(
		"(第 %d-%d 行, 共 %d 行。后面还有 %d 行没读: %s %s=%s offset=%d)\n%s",
		offset, end, len(lines), len(lines)-end, tool, argName, target, end+1, body)
}

// isBinary 粗判. 只看开头一段有没有 NUL ——
// 判错的代价不对称: 把文本误判成二进制只是少读一个文件,
// 把二进制塞进上下文是几万个垃圾 token.
func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

// argInt 取整数参数. 模型经常把数字写成字符串, 两种都收
func argInt(a map[string]any, k string) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		// **int64 也要收.** 真实路径上参数过一趟 JSON 都是 float64,
		// 所以这个缺口一直没暴露 —— 直到有人(测试或别的调用方)直接传
		// int64, 拿到的是**静默的 0**. 而 0 在 remind_me 那儿的意思是
		// "at 没给", 报错会指向一个完全不相干的方向.
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return 0
}

// argFloat 跟 argInt 一样, 只是不砍小数.
//
//	**分开一个函数而不是复用 argInt**: 半径、经纬度这类东西砍成整数
//	是静默的精度损失, 而 argInt 那个转换在别处是对的
func argFloat(a map[string]any, k string) float64 {
	switch v := a[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return n
		}
	}
	return 0
}

// ToolSet 一张查得动的工具表
type ToolSet struct {
	list   []Tool
	byName map[string]Tool
}

func NewToolSet(tools []Tool) *ToolSet {
	// 按名字排序 —— 提示词必须字节稳定, 否则缓存全废
	sorted := append([]Tool(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	m := make(map[string]Tool, len(sorted))
	for _, t := range sorted {
		m[t.Name] = t
	}
	return &ToolSet{list: sorted, byName: m}
}

// All 这一份工具表里都有什么 —— 拿去重建一份新的(见 Agent.ToolsNow).
func (ts *ToolSet) All() []Tool {
	if ts == nil {
		return nil
	}
	return append([]Tool(nil), ts.list...)
}

func (ts *ToolSet) Get(name string) (Tool, bool) {
	t, ok := ts.byName[name]
	return t, ok
}

// Validate 跑之前检查参数.
//
// 返回的错误是给**模型**看的: 要说清缺了哪个参数、该给什么,
// 而不是让它去猜一句系统报错的含义.
func (t Tool) Validate(args map[string]any) error {
	var missing []string
	for _, k := range t.ArgOrder {
		if t.Optional[k] {
			continue
		}
		v, ok := args[k]
		if !ok || v == nil {
			missing = append(missing, k)
			continue
		}
		// 空着也算给了的那几个 —— 见 Tool.MayBeEmpty
		if v == "" && !t.MayBeEmpty[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// **把它实际给了什么也说出来.**
	//
	// 只说"缺 pattern"的话, 模型如果自以为给了(比如键名写成 query、
	// 或者值是空串), 它没有任何线索能自纠, 只会原样再试一次 ——
	// 否则同一份错误输入会重复尝试 5 次后被停滞检测停止.
	//
	// 而且这条信息对**排查的人**同样关键: "缺 pattern"与账本里
	// "确实给了 pattern"的矛盾必须直接呈现, 否则会误导排查方向.
	got := make([]string, 0, len(args))
	for k := range args {
		got = append(got, fmt.Sprintf("%s=%.40v", k, args[k]))
	}
	sort.Strings(got)
	gotStr := strings.Join(got, ", ")
	if gotStr == "" {
		gotStr = "(什么都没给)"
	}
	return fmt.Errorf("调 %s 缺参数: %s。这个工具要 %s。你这次给的是: %s",
		t.Name, strings.Join(missing, ", "), strings.Join(t.ArgOrder, " + "), gotStr)
}

// Names 所有工具名 —— 报"没有这个工具"时要一并告诉它有哪些
func (ts *ToolSet) Names() []string {
	out := make([]string, len(ts.list))
	for i, t := range ts.list {
		out[i] = t.Name
	}
	return out
}

// Describe 生成提示词里的工具清单.
//
// **字节稳定**: 工具表排过序, 参数按 ArgOrder 输出 —— 同一张表永远生成同一段文字.
// 这是前缀缓存能命中的前提.
func (ts *ToolSet) Describe() string {
	var b strings.Builder
	for _, t := range ts.list {
		fmt.Fprintf(&b, "  %-11s %s  %s\n", t.Name, t.Desc, orderedExample(t))
	}
	return strings.TrimRight(b.String(), "\n")
}

// orderedExample 按 ArgOrder 生成有序的示例对象.
// 用 json.Marshal 直接编 map 会按键名重排, 那样参数顺序就跟声明的不一致了.
func orderedExample(t Tool) json.RawMessage {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range t.ArgOrder {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(t.Args[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}

/*
WithEveryDay 给这台机器加上"到点看一眼"的能力.

	── 为什么是叠加, 不是加一个参数 ──

	DefaultToolsWith 已经有九个参数, 全是"这台机器有没有这件能力"
	的开关。再加一个的话, **每一处调用点和每一条测试都要跟着补一个 nil**
	—— 而那种改动里最容易出的错就是位置串了一格, 于是某台机器上
	"搜索"变成了"招人"。

	叠加式(跟 WithMoveWork 一样)让"加一件能力"只碰真正要它的那一处。

/*
WithDaily 让 remind_me 也能设"每天几点".

	── 为什么是替换而不是再加一个工具 ──

		check_every 是**轮询**工具: 为了盯住 7:05–8:00 那一小时,
		模型每 10 分钟醒一次, 一天 144 次空转, 账本会留下三条几乎一样的巡检.

		按时刻之后没必要再占一个工具位: "到点说一句"和"每天到点看一眼"
		是同一件事的两种时间说法. 四个"以后提醒我"的工具
		(remind_me/check_every/when/watch_for)合计 2752 字节, 而模型每轮都要在其中选择.
*/
func WithDaily(ts []Tool, remind func(atMs int64, text string) error,
	daily func(atMs int64, what string) error) []Tool {
	if remind == nil || daily == nil {
		return ts
	}
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		if t.Name == "remind_me" {
			continue
		}
		out = append(out, t)
	}
	return append(out, RemindTool(remind, daily))
}

// WithWhen 给这台机器加上条件触发. 叠加式, 理由同 WithEveryDay.
//
//	只有接了世界模型的机器才叠 —— 没有事实可判的话, 每一条规则都会
//	永远不成立, 却仍会被登记为可用
func WithWhen(ts []Tool, add func(fact, field, op string, value any,
	from, to, say, why, raw string, urgent bool) (string, error),
	watch func(kind, place, say, raw string) error) []Tool {
	if add == nil && watch == nil {
		return ts
	}
	// **watch_for 并进来了**: 两个工具答的是同一句话"以后…就告诉我",
	// 而模型每次都要在两个里挑 —— 挑错不报错, 只是那条关注永远不触发
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		if t.Name == "watch_for" {
			continue
		}
		out = append(out, t)
	}
	return append(out, WhenTool(add, watch))
}

// WithNotes 给这台机器加上长期记忆(记住/忘掉). 叠加式.
//
//	两个一起叠, 不分开挂: 只给"记住"不给"忘掉", 换来的是一份只增不减
//	的偏好表 —— 说错一句话就永久多一条规矩. 跟 remind/cancel 同一条规矩
func WithNotes(ts []Tool, remember func(key, text string) (string, error),
	forget func(key string) (string, error)) []Tool {
	if remember == nil || forget == nil {
		return ts
	}
	return append(ts, RememberTool(remember), ForgetThatTool(forget))
}

// WithTimezone 给这台机器加上"换时区". 叠加式.
//
//	**这台机器改得动才挂**: 桌面壳那条路时区跟着宿主系统走, 改不了 ——
//	摆一个改不动的工具, 它会照着调然后说"改好了"
func WithTimezone(ts []Tool, set func(zone string) (string, error)) []Tool {
	if set == nil {
		return ts
	}
	return append(ts, SetTimezoneTool(set))
}

// WithWhere 给这台机器加上"现在什么情况" —— 六个位置工具收成的那一个.
//
//	Now 是必须的(没有它这个工具答不了任何东西); 其余的可以是 nil,
//	那两个用法就不出现在工具说明里 —— **摆一个用不了的用法, 它会照着调**
func WithWhere(ts []Tool, k WhereKit) []Tool {
	if k.Now == nil {
		return ts
	}
	return append(ts, WhereTool(k))
}

// WithPlace 给这台机器加上"给一个地方起名".
//
//	here 是必须的; byAddress 没配地图就是 nil, 那个用法不出现
func WithPlace(ts []Tool, here func(name string, radius float64, sure bool) (string, error),
	byAddress func(name, address string, radius float64) (string, error),
	forget func(name string) (string, error)) []Tool {
	if here == nil {
		return ts
	}
	return append(ts, PlaceTool(here, byAddress, forget))
}

// WithHistory 给这台机器加上"他那天收到过什么" —— 见 HistoryTool.
func WithHistory(ts []Tool, look func(what, day string, limit int) (string, error)) []Tool {
	if look == nil {
		return ts
	}
	return append(ts, HistoryTool(look))
}

// WithHome 给这台机器加上"开关家里的东西" —— 见 HomeTool.
func WithHome(ts []Tool, act func(name, do string) (string, error)) []Tool {
	if act == nil {
		return ts
	}
	return append(ts, HomeTool(act))
}

// WidestForAudit 这台机器挂得出来的最贵一张工具表 —— **给运维量字节用**.
//
//	跟测试里那份是同一个东西, 但测试里的进不了 main. 量工具声明的
//	字节数是这份提示词的下一刀该落在哪儿的唯一依据
func WidestForAudit() *ToolSet { return widestToolSet() }

func widestToolSet() *ToolSet {
	ts := DefaultToolsWith(
		func(int64, string) error { return nil },
		nil, // 起地名走 WithPlace 那一个
		func(kind, place, say, raw string) error { return nil },
		func(string) (string, error) { return "", nil },
		func(string, int) ([]abi.SearchHit, error) { return nil, nil },
		func(mediaType, dataB64, question string) (abi.SeeResult, error) { return abi.SeeResult{}, nil },
		func(string, int) ([]abi.RecallHit, error) { return nil, nil },
		func(name, role string) (string, error) { return "", nil },
		func(who, task string) (string, error) { return "", nil })
	// **一处一处照着 components.toolsFor 抄**: 那儿挂什么这儿就要有什么,
	// 少一个就是又一次"闸装在小头上"
	ts = WithMoveWork(ts, func(string) (string, error) { return "", nil })
	ts = WithNotes(ts,
		func(k, v string) (string, error) { return "", nil },
		func(string) (string, error) { return "", nil })
	ts = WithTimezone(ts, func(string) (string, error) { return "", nil })
	ts = WithWhere(ts, WhereKit{
		Now:     func() string { return "" },
		Route:   func(a, b, c string) (string, error) { return "", nil },
		Traffic: func(string) (string, error) { return "", nil },
		Weather: func(string) (string, error) { return "", nil },
		Habits:  func() string { return "" },
		Card:    func(string) (map[string]any, error) { return nil, nil },
		Fresh:   func() string { return "" },
		Places:  func() string { return "" },
		Trail:   func(string) (string, error) { return "", nil },
	})
	ts = WithPlace(ts,
		func(string, float64, bool) (string, error) { return "", nil },
		func(a, b string, r float64) (string, error) { return "", nil },
		func(string) (string, error) { return "", nil })
	ts = WithFindPlace(ts, func(a, b string) (string, error) { return "", nil })
	ts = WithHistory(ts, func(a, b string, n int) (string, error) { return "", nil })
	ts = WithHome(ts, func(a, b string) (string, error) { return "", nil })
	ts = WithAgenda(ts,
		func(string, int64, string) (string, error) { return "", nil },
		func(bool) string { return "" },
		func(string) (string, error) { return "", nil },
		func(string) (string, error) { return "", nil })
	ts = WithWhen(ts, func(a, b, c string, d any, e, f, g, h, i string, j bool) (string, error) {
		return "", nil
	}, func(kind, place, say, raw string) error { return nil })
	ts = WithPending(ts,
		func(string) (string, error) { return "", nil },
		func() string { return "" })
	ts = WithDaily(ts,
		func(int64, string) error { return nil },
		func(int64, string) error { return nil })
	ts = WithGit(ts,
		func() (string, error) { return "", nil },
		func() (string, error) { return "", nil },
		func(to string) (string, error) { return "", nil })
	return NewToolSet(ts)
}

// WithAgenda 给这台机器加上待办和日程. 叠加式.
func WithAgenda(ts []Tool,
	add func(what string, atMs int64, where string) (string, error),
	list func(withDone bool) string,
	done func(id string) (string, error),
	drop func(id string) (string, error)) []Tool {
	if add == nil {
		return ts
	}
	return append(ts, AgendaTool(add, list, done, drop))
}

// WithPending 让 cancel 也能"先看看有什么".
//
//	**能撤的前提是看得见**: 只能撤而不能列出对象时, 调用方会按说明去读
//	`.neox/state.txt`; 该路径被尝试过 8 次。
func WithPending(ts []Tool, cancel func(id string) (string, error),
	list func() string) []Tool {
	if cancel == nil || list == nil {
		return ts
	}
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		if t.Name == "cancel" {
			continue
		}
		out = append(out, t)
	}
	return append(out, CancelTool(cancel, list))
}

// WithFindPlace 给这台机器加上"查一个地方在哪". 叠加式.
//
//	没配地图 key 就不挂 —— 摆一个用不了的工具, 它会照着调然后
//	退回去拿手边的坐标顶, 而那正是要治的病
func WithFindPlace(ts []Tool, find func(query, city string) (string, error)) []Tool {
	if find == nil {
		return ts
	}
	return append(ts, FindPlaceTool(find))
}
