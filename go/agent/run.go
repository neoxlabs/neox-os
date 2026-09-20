package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// run —— 跑一条命令.
//
// ── 为什么这件事在这个 OS 上才敢做 ──
//
// 给 agent 一个不受限的 shell, 在客户端里是不负责任的: 没有笼子,
// 一条命令就能删掉用户的家目录、把密钥发出去、把整台机器吃满.
// 所以客户端只能靠"工具给得少"来控制风险 —— 代价是它也就只会那几件事.
//
// 这里反过来: **边界在内核, 不在工具表**. 所以工具可以给到底.
//
//	landlock  子进程继承同一套文件规则. `rm -rf /` 在卷外一个字节都碰不到
//	netns     子进程继承同一个网络命名空间. 没有出网授权就是真的连不出去
//	cgroup    子进程记在同一个 cgroup 上. 内存/CPU/任务数一起算, 跑飞了一起被砍
//
// 三样都是 fork+exec 继承的, 这是内核的语义, 不是我们的约定 ——
// 也就是说 agent **没有办法**通过起子进程来逃出笼子.
//
// ── 这一个工具顶掉多少 ──
//
// 有了它, agent 会的不再是"六个文件工具", 而是这台机器上装的一切:
// git / go / npm / python / curl / jq / sed …… 而且是真跑, 不是模拟.
// 更重要的是**它终于能验证自己的工作**: 写完代码跑一次测试,
// 而不是"回读一遍确认字符写进去了"就报告完成.
//
// ── 三件必须做对的事 ──
//
//  1. **超时要真杀掉**. Go 杀不掉 goroutine, 但杀得掉进程 ——
//     这是子进程比内部循环更好管的地方. 见 killGroup 的说明.
//  2. **输出要留头也留尾**. 编译错误在开头, 测试失败在结尾,
//     只留前面那一段等于把最重要的信息扔了.
//  3. **非零退出码不是工具错误, 是结果**. 一次失败的测试是 agent
//     最需要看到的东西, 把它变成一句 err 会连输出一起丢掉.
const (
	runMaxBytes   = 16 * 1024
	runTimeout    = 3 * time.Minute
	runDefaultDir = "."
)

func runTool() Tool {
	return Tool{
		Name: "run",
		// ── 这三句原来在提示词里, 沉下来了 ──
		//
		//	它们是**这个工具的用法**, 不是跨场景的判断方式. 跟着工具表走,
		//	这台机器没挂 run 的话它们就不出现.
		Desc: "跑一条命令(sh -c)。结果第一行是退出码 —— 非零就是失败了, " +
			"输出里有多少行 PASS 都不作数。" +
			"试的东西、造的测试数据放 .tmp/：**绝不能**往他真正的账本、" +
			"数据库、配置里塞测试记录（你刚写的程序也算：它默认写到哪儿, " +
			"拿那个路径去验, 造的数据就留在交付物里了）。" +
			// ── 堵住"拿 grep 补工具缺口"那条路 ──
			//
			//	122 次 run 里有 55 次是 grep events.jsonl: 它想知道
			//	"我设了哪些提醒""我记着哪些地点""我去过哪儿" —— 而那些
			//	现在都有工具了(cancel 不给 id 就列, where places=1,
			//	where fresh=1)。不明说的话它会接着 grep, 因为 grep 什么
			//	都能查, 而"什么都能"正是它挑错的原因
			"账本有专门的入口：提醒和关注在 cancel（不给 id 就是列出来）、" +
			"地点和位置在 where、他收到过什么在 history。" +
			"grep 出来的是原始 JSON，同一件事在里面有好几种写法",
		Args: map[string]string{
			"cmd": "要跑的命令, 例如 go test ./... 或 git diff --stat",
			"dir": "在哪个目录跑, 相对工作目录。不给就是工作目录",
		},
		ArgOrder: []string{"cmd", "dir"},
		Optional: map[string]bool{"dir": true},
		// proc 轴. scope 是命令本身 —— 授权记录里要看得出批准的是什么,
		// 而不是一句"允许起子进程".
		Needs: abi.AxisProc, ScopeArg: "cmd",
		// 命令什么都干得了, 一律当写操作: 不进并发批次.
		//
		// 按名字猜的话 "run" 不以 write/edit 开头, 会被当成只读工具
		// 跟别的调用并发跑 —— 两条命令同时改同一个文件, 结果不确定.
		Mutates: true,
		// 它既改变世界也依赖世界: 改完代码再跑一次同一条测试,
		// 结果本该不同 —— 那是它该做的事, 不是原地转.
		WorldSensitive: true,
		Timeout:        runTimeout,
		Run:            execCommand,
	}
}

func execCommand(t Toolbox, a map[string]any) (string, error) {
	cmdline := strings.TrimSpace(argStr(a, "cmd"))
	if cmdline == "" {
		return "", errors.New("cmd 是空的。给一条真正要跑的命令, 比如 go test ./...")
	}
	dir := argStr(a, "dir")
	if dir == "" {
		dir = runDefaultDir
	}
	workdir := t.resolve(dir)
	if st, err := os.Stat(workdir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("dir=%s 不是一个存在的目录。先 list_dir 确认路径, "+
			"或者干脆不给 dir(就在工作目录跑)", dir)
	}

	ctx := t.Ctx
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
	}

	// TMPDIR 得真的存在, 否则一堆工具会在"建不出临时文件"上失败,
	// 报出来的错跟真正的原因八竿子打不着.
	_ = os.MkdirAll(t.Root+"/.tmp", 0o755)

	/**
	 * 沙箱: 让"写只能写进你的工作区"这句话在没有内核约束的宿主上也成真.
	 *
	 *	**必须包在最外层**: 包在 sh 里面的话, sh 自己的重定向
	 *	(echo x > /tmp/y)不受约束 —— 重定向因此会绕过预期的写入边界.
	 */
	argv := []string{"/bin/sh", "-c", cmdline}
	if t.Sandbox {
		argv = sandboxWrap(t.Root, argv)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Env = commandEnv(t.Root)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	// 不给它 stdin. 交互式的命令 (git rebase -i、npm login、sudo)
	// 会立刻拿到 EOF 退出, 而不是挂在那里等一个永远不会来的输入 ——
	// 后者要等到超时才发现, 白等三分钟.
	cmd.Stdin = nil
	setPgid(cmd)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("起不来: %v。检查命令名拼写, 或者用 run cmd=\"which %s\" "+
			"确认这台机器上装没装", err, firstWord(cmdline))
	}

	// 记下这个进程组 —— 它可能留下后台的孩子(验证用的服务、watcher).
	// 这段对话结束时由宿主一起收掉, 见 Toolbox.Started.
	if t.Started != nil && cmd.Process != nil {
		t.Started(cmd.Process.Pid)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err := <-waited:
		code := exitCodeOf(err)
		if t.OnRan != nil {
			t.OnRan(cmdline, code)
		}
		return runResult(cmdline, buf.Bytes(), code), nil
	case <-ctx.Done():
		// **真杀掉, 连同它起的所有子孙.**
		//
		// 这里是子进程比 goroutine 强的地方: goroutine 超时之后只能
		// "不再等它", 它还在跑; 进程可以直接终止.
		//
		// 但必须杀**进程组**: 我们起的是 /bin/sh, 真正干活的是 sh 的孩子.
		// 只杀 sh 的话, `go test` 会变成孤儿继续跑 —— 占着 cgroup 的配额,
		// 而 agent 已经不看它了. 那正是"看起来停了其实没停"的经典来源.
		killGroup(cmd)
		<-waited
		return "", fmt.Errorf("超时, 已经杀掉这条命令和它起的所有进程。"+
			"要么它本来就很慢(缩小范围: 单个包、单个测试), "+
			"要么它在等输入(它没有 stdin)。**不要原样重跑**。\n已经产出的输出:\n%s",
			runResult(cmdline, buf.Bytes(), -1))
	}
}

// commandEnv 子进程的环境.
//
// ── 白名单, 但必须带上 OS 发下来的配置 ──
//
// 这里原来是一张纯固定的表, 理由是"不继承宿主环境". **那条规矩用错了层.**
//
// 不继承宿主环境是 **OS → 进程** 那道边界的规矩: 继承来的东西摘不掉,
// 临时约束会变成永久约束. 而这里是笼子**里面**, 再刷一次的结果是把
// OS 特意发给这个进程的东西也一并扔了 —— 出网代理就是这么丢的:
//
//	OS 建好 netns、起好代理、把 http_proxy 写进进程的环境
//	→ run 起 curl 时不传 → curl 自己去做 DNS → netns 里没有 DNS
//	→ 报 "Could not resolve host"
//
// 授权记上了、plan.net 有值、netns 也建了, 每一环看着都对却仍然不通 —
// 因为断点在最后一米.
//
// 凭据仍然不传: NEOX_ABI_TOKEN / NEOX_ABI_SOCKET 是发给 **agent** 的,
// 不是发给它随手起的任何一条命令的. 一条 `run curl` 不该有能力冒充
// agent 去调 OS.
//
// HOME/TMPDIR 指向工作目录, 因为很多工具要写缓存 (go 的 GOCACHE、
// npm 的 _cacache、pip 的 wheel cache). 指向卷外的话它们会撞在
// landlock 上, 报出来的是一句莫名其妙的权限错误, 而不是"你越界了".
func commandEnv(root string) []string {
	env := []string{
		"PATH=" + commandPath(root),
		"HOME=" + root,
		"TMPDIR=" + root + "/.tmp",
		"LANG=C.UTF-8",
		"TERM=dumb", // 别输出 ANSI 颜色码, 那些字节进上下文纯属浪费
		"CI=1",      // 大多数工具据此关掉进度条和交互提示
	}
	// ── 子命令的钟点要跟 OS 一致 ──
	//
	//	OS 那侧的时区改在 time.Local 上(见 osinit/zone.go), 而子进程
	//	看的是 TZ 环境变量 —— 不带的话 `date` 报 UTC, 而 bot 刚在
	//	提醒卡片上看到的是北京时间.
	//
	//	如果时区没有传递, 同一句话里就会出现两个钟点("现在 09:09 UTC
	//	（北京时间 17:09）"), 然后开始自己解释这 8 小时差 —— 而那
	//	不是它该操心的事.
	if tz := childTZ(time.Local, time.Now()); tz != "" {
		env = append(env, "TZ="+tz)
	}
	// 下载回来的包一台机器上只装一遍 —— 见 sharedCache
	env = append(env, cacheEnv(sharedCache())...)
	// 有 venv 就明说. PATH 已经够让 python/pip 指过去了, 但很多工具
	// (pip 自己、构建后端、IDE 类脚本) 是看这个变量判断"在不在虚拟环境里"的,
	// 少了它们会以为在系统环境, 然后拒绝装或者装错地方.
	if exists(root + "/.venv/bin/python3") {
		env = append(env, "VIRTUAL_ENV="+root+"/.venv")
	}
	for _, k := range passThroughEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// childTZ 给子进程的 TZ.
//
// ── 名字不一定认得出 ──
//
//	TZ=Asia/Shanghai 要子进程去 /usr/share/zoneinfo 里找那个文件. Alpine
//	镜像默认没有那个目录, 而 musl 找不到的时候**不报错, 静默当成 UTC** ——
//	2026-09-11 线上就是这样: OS 这侧是北京时间, bot 一 `date` 出来是
//	03:51 UTC. 它的每日巡检第一步就是 `date +%u` 判工作日, 周一早上
//	06:40 在 UTC 还是周日 22:40 —— 会被当成周末直接跳过, 一声不吭.
//
//	所以文件不在就退成 POSIX 的固定偏移写法(`<+08>-8`), 那个不查任何
//	文件. 代价是夏令时切换那一刻不准 —— 对没有夏令时的地方没有代价,
//	对有的地方也好过整整差 8 小时. 镜像里装了 tzdata 的话用不上这条.
var zoneinfoDir = "/usr/share/zoneinfo/" // 测试换成一个空目录

func childTZ(loc *time.Location, now time.Time) string {
	name := loc.String()
	if name == "" || name == "Local" {
		return ""
	}
	if name == "UTC" || exists(zoneinfoDir+name) {
		return name
	}
	_, off := now.In(loc).Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	h, m := off/3600, off%3600/60
	// POSIX 的符号跟人的直觉相反: 东八区写成 -8
	posixSign := "-"
	if sign == "-" {
		posixSign = ""
	}
	if m == 0 {
		return fmt.Sprintf("<%s%02d>%s%d", sign, h, posixSign, h)
	}
	return fmt.Sprintf("<%s%02d%02d>%s%d:%02d", sign, h, m, posixSign, h, m)
}

// 卷里装过的东西, 下一轮必须还找得到.
//
// ── 持久环境缺失时的反面 ──
//
// 上一轮它申请出网、下 get-pip.py、在 /agentwork/.venv 里装好 openpyxl,
// 还专门交代"以后用 .venv/bin/python". 下一个活("把流水做成 Excel")
// 第一句话就是: **"openpyxl 没装, 机器上也没有 pip/uv 可以装"** ——
// 即使后续任务仍需要这个环境, 进程也不会自动得到上一轮的 PATH.
//
// 根子不在它记性差. 那句交代随着上一段对话一起死了, 而**装好的东西
// 没有落在任何进程看得见的地方**: 卷是持久的, PATH 不是.
//
// 这正是 OS 该干的事. 一台机器上装了什么, 不该靠谁记得住一句话 ——
// 靠的是环境本身: PATH 指过去, `python3 -c "import openpyxl"` 就成了.
// 所以修在环境, 不修在提示词.
//
// ── 只认卷里的, 而且要真能跑 ──
//
// 三条路径都在卷内, 也就都在笼子里 —— 加它们不放宽任何边界.
// 顺序固定(venv → .local → node_modules), 不看目录遍历顺序.
// 判据是**解释器/目录真的在那儿**: 半个残破的 venv 挂在 PATH 最前面,
// 会把所有 python 命令一起带沟里, 那比找不到包糟得多.
func commandPath(root string) string {
	const base = "/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"
	var pre []string
	// venv: 只有解释器真在才认. 空壳目录挂上去会毁掉所有 python 命令
	if root != "" && exists(root+"/.venv/bin/python3") {
		pre = append(pre, root+"/.venv/bin")
	}
	// pip install --user 的落点
	if root != "" && exists(root+"/.local/bin") {
		pre = append(pre, root+"/.local/bin")
	}
	// npm 装在工程里的可执行文件
	if root != "" && exists(root+"/node_modules/.bin") {
		pre = append(pre, root+"/node_modules/.bin")
	}
	if len(pre) == 0 {
		return base
	}
	return strings.Join(pre, ":") + ":" + base
}

// passThroughEnv OS 发给进程、必须原样传给子命令的配置.
//
// 只有出网这一类 —— 它们不是凭据, 是"往哪走"的路由信息.
// 大小写两套都要: curl 认小写, Java/.NET 认大写, 少一套就有一半程序连不出去.
var passThroughEnv = []string{
	"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY",
	"no_proxy", "NO_PROXY",
	"npm_config_proxy", "npm_config_https_proxy",
}

// runResult 给模型看的结果.
//
// 退出码放**第一行**: 它是这次调用最重要的一个事实, 埋在几百行输出
// 后面的话模型会漏掉, 然后把一次失败的构建当成成功报上去.
func runResult(cmdline string, out []byte, code int) string {
	var head string
	switch {
	case code == 0:
		head = "[退出码 0 · 成功]"
	case code < 0:
		head = "[被杀掉 · 超时]"
	default:
		head = fmt.Sprintf("[退出码 %d · **失败**]", code)
	}
	body := strings.TrimRight(string(out), "\n")
	if body == "" {
		body = "(没有任何输出)"
	}
	return head + "\n" + clampMiddle(body, runMaxBytes) + gitWriteNote(body)
}

/**
 * gitWriteNote —— 说明修改 git 账本的命令不属于进程的职责.
 *
 * ── 为什么必须在这儿说 ──
 *
 *	分支隔离是这么做到的: 它的 worktree 能写, 而账本(.git)在项目根上,
 *	不在它的写范围里. 于是 `git add` 报的是
 *
 *	    fatal: Unable to create '…/.git/worktrees/小乙/index.lock':
 *	    Operation not permitted
 *
 *	**这句话跟真正的原因八竿子打不着**. 进程可能据此误以为权限
 *	不够, 接着去查 refs、查 reflog、试各种绕法 —— 而它其实一件事都不
 *	用做: 收活是宿主替它做的(见 cmd/neox-console 的提交/合入), 它只管
 *	改文件.
 *
 *	内核报的是"你越界了", 而它需要知道的是
 *	"这件事本来就不归你做". 报错不改, 只在后面补一句真正的原因.
 */
func gitWriteNote(out string) string {
	if !strings.Contains(out, "Operation not permitted") {
		return ""
	}
	if !strings.Contains(out, "/.git/") && !strings.Contains(out, ".git/index.lock") {
		return ""
	}
	return "\n\n[改 git 账本这件事不归你做 —— 你只管改文件, 收活和合入我来。" +
		"要把活合上去用 merge_up, 要拉别人的新东西用 sync_down。]"
}

// clampMiddle 太长就砍中间, 留头也留尾.
//
// 只留前面那一段是最糟的砍法: 编译错误确实在开头, 但**测试失败的摘要
// 在结尾**, `go test` 的 FAIL 行、npm 的 error summary 全在最后.
// 砍掉尾巴等于让模型看着一屏 PASS 宣布测试通过了.
func clampMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	head := s[:half]
	tail := s[len(s)-half:]
	// 从行边界切, 不然头尾都是半截行, 报错信息读不出来
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	}
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	return fmt.Sprintf("%s\n\n…(输出共 %d 字节, 中间省略了 %d 字节。"+
		"要看完整的就把范围缩小再跑一次, 比如只跑一个包/一个测试)…\n\n%s",
		head, len(s), len(s)-len(head)-len(tail), tail)
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func firstWord(s string) string {
	if i := strings.IndexAny(s, " \t"); i > 0 {
		return s[:i]
	}
	return s
}
