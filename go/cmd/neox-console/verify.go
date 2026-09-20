package main

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

/**
 * verify —— **它验没验过, 由退出码说了算**.
 *
 * ── 为什么不能信它自己说的 ──
 *
 *	提示词里让它"干完自己验一遍". 而"我跑了测试, 全绿"这句话, 跟
 *	"我没跑但我觉得没问题"在文本上一模一样 —— 偏偏收尾的时候模型最爱
 *	说前者. 用户的原话是"验收闭环要靠模型自己来验收闭环…… 通过 harness
 *	让 BOT 自己去验证": 那正是这里做的 —— 验的动作归它, **证据归系统**.
 *
 *	证据是它跑过的命令和真实退出码(见 Toolbox.OnRan). 命令是它挑的,
 *	结果不是它给的.
 *
 * ── 哪些命令算"验证" ──
 *
 *	不是所有 run 都算. ls、cat、git log 不是验证, 把它们记进提交里等于
 *	把噪音当证据. 判据是**这条命令是不是在检验代码**: 测试、构建、
 *	类型检查、lint. 认不出来的一律不算 —— 宁可少记一条, 不能记一条假的.
 */

// verifyRun 一次验证性的运行.
type verifyRun struct {
	Cmd  string
	Exit int
}

// verifyLog 一个 bot 这一轮跑过的验证.
//
//	按 bot 存, 而不是按进程: 进程会死对话不死, 而"这一轮验过什么"
//	是对话上的事.
type verifyLog struct {
	mu   sync.Mutex
	runs map[string][]verifyRun
}

func newVerifyLog() *verifyLog {
	return &verifyLog{runs: map[string][]verifyRun{}}
}

// note 记一条 —— 只记看得出是验证的那些.
func (v *verifyLog) note(bot, cmd string, exit int) {
	if v == nil || !isVerification(cmd) {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	// 只留最近几条: 一轮里跑二十遍测试是常事(改一次跑一次),
	// 而进提交的是**最后的结论**, 不是过程
	list := append(v.runs[bot], verifyRun{Cmd: firstLine(cmd, 120), Exit: exit})
	if len(list) > 4 {
		list = list[len(list)-4:]
	}
	v.runs[bot] = list
}

// peekFor 看一眼但**不取走** —— 合入要读它做判断, 而这一轮的提交
// 还要拿同一批当证据. 取走的话两边只有一个拿得到.
func (v *verifyLog) peekFor(bot string) []verifyRun {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]verifyRun(nil), v.runs[bot]...)
}

// takeFor 取走这个 bot 这一轮的验证记录 —— 取完就清, 下一轮从头记.
func (v *verifyLog) takeFor(bot string) []verifyRun {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	got := v.runs[bot]
	delete(v.runs, bot)
	return got
}

/**
 * isVerification 这条命令是不是在检验代码.
 *
 *	**宁可少认一条, 不能多认一条**: 少认了只是提交里少一行证据;
 *	多认了(比如把 ls 当成验证)就是在提交里写了一句假话, 而那种假话
 *	正是这套东西要消灭的.
 */
func isVerification(cmd string) bool {
	lower := strings.ToLower(cmd)
	// 先排掉一眼就不是的: 只是看看而已的那些
	if lookOnly.MatchString(lower) {
		return false
	}
	return verifying.MatchString(lower)
}

var (
	// 跑测试 / 构建 / 类型检查 / lint —— 各语言常见的那几条
	verifying = regexp.MustCompile(`\b(go test|go build|go vet|gofmt -l|golangci-lint|` +
		// **python3 也要认**: 常见命令是 `python3 -m unittest discover -s tests`,
		// 而这里原来只写了 `python -m unittest` —— 一个 3 就让整轮验证的
		// 证据全丢了(闸门也就白设了). macOS 上根本没有裸 python 这个命令.
		`pytest|python3? -m pytest|python3? -m unittest|tox|` +
		`npm (run )?(test|build|lint|typecheck)|yarn (test|build|lint)|pnpm (test|build|lint)|` +
		`npx (tsc|vitest|jest|eslint)|tsc|vitest|jest|eslint|` +
		`cargo (test|build|clippy)|mvn (test|verify)|gradle (test|build)|make (test|check|build))\b`)
	// 只是看看, 或者只是在准备环境: 这些不算验证.
	//
	//	**装依赖那条尤其要排掉**: `pip install pytest` 里有 pytest 这个词,
	//	按关键词匹配会把它当成"跑了测试" —— 依赖安装命令就会混进验证记录.
	//	而装依赖成功跟代码没坏是两件毫不相干的事.
	//	**cd 不在这一组里**: "cd app && pytest" 是最常见的跑测试写法之一,
	//	把它排掉等于把一大半验证漏掉.
	lookOnly = regexp.MustCompile(`(^\s*(ls|cat|head|tail|pwd|echo|mkdir|git (log|status|diff|show))\b)|` +
		`\b(pip3? install|pip3? download|npm (i|install|ci)|yarn add|pnpm add|go get|cargo add|apt-get|brew install)\b`)
)

/**
 * verifyTrailer 把验证记录写成提交尾注.
 *
 *	写成 git 的 trailer(Key: value)格式 —— 那是公开约定, interpret-trailers
 *	和 GitHub 都认, 将来要统计"哪些提交是验过的"不用另发明一套.
 *
 *	**失败的也要写**. 一次没通过的测试是这次提交最重要的事实之一:
 *	它说明这条改动是在已知不通过的情况下留下的. 藏起来只会让下一个人
 *	以为它是绿的.
 */
func verifyTrailer(runs []verifyRun) string {
	if len(runs) == 0 {
		return ""
	}
	var lines []string
	for _, run := range runs {
		result := "通过"
		if run.Exit != 0 {
			result = fmt.Sprintf("**没通过**(退出码 %d)", run.Exit)
		}
		lines = append(lines, fmt.Sprintf("Neox-Verify: %s → %s", run.Cmd, result))
	}
	return strings.Join(lines, "\n")
}
