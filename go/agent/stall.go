package agent

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// 停滞检测 —— 提示词管不住的那部分.
//
// 提示词里写了"同方向连着两次失败就换角度", 但那只是**建议**.
// 模型卡住的时候恰恰是它判断力最差的时候, 这时候指望它自己看提示词
// 是不现实的. 所以要在 loop 里硬拦.
//
// 三种卡法, 症状不同, 处理也不同:
//
//	重复调用   同一个工具同一个参数反复调 → 它没意识到自己在原地转
//	反复失败   同一个错误连着出现 → 假设错了, 换参数没用
//	空转       连着好几步只读不写, 也没收工 → 在"研究"里迷路了
//
// 处理分两级: **先提醒, 再叫停**. 直接叫停太粗暴 ——
// 有时候读同一个文件两次是合理的 (中间改过).

// call 最近一步的记录. 存结构而不是把字段拼成字符串再切回来 ——
// 格式一变解析就悄悄错了, 而且不报错.
type call struct {
	fp   string
	tool string
	path string
	// id 这次调用的编号. 用来在结果被折掉之后把这条记录摘掉 ——
	// 见 forgetFolded
	id string
}

// ── 一条通则, 替掉三个补丁 ──
//
// "原地转"被我判错过三次, 每次都是**检测器精确地惩罚了提示词
// 明确要求它做的那件事**:
//
//	写完回读     提示词说"写完必须回读", 却被判成重复读
//	分片翻页     offset=1 → 401 是正常翻页, 指纹漏了 offset 就成了重复
//	改完重搜     改完再搜一遍确认遗漏, 是验证动作, 却被判成重复搜
//
// 三次我都是加一个补丁: 清该路径的读历史、指纹补上全部参数、
// 把路径相等改成范围覆盖. 补丁越堆越多, 而坑还会有第四次.
//
// 真正的成因只有一个: **判据错了.**
// 我判的是"这次调用跟上次长得一样吗", 而该判的是
// **"这次调用还会不会带来新信息"**.
//
// 于是通则是: 指纹 = 这次调用的意图 + **它所依赖的世界状态**.
//
//	读操作   结果取决于参数**和世界** → 指纹含"影响到它的写有几次"
//	         世界变过, 同样的读就会给出不同结果, 天然不算重复
//	写操作   效果只取决于它自己写什么 → 指纹**不含**世界状态
//	         写同样的内容就是无意义的, 跟世界变没变无关
//
// 这一条同时盖住上面三种情况, 而且**不再需要事后去清历史** ——
// 判断挪进了指纹本身, 那才是它该在的地方.
//
// 范围判断 (covers) 留着, 但它的角色变了: 从"清哪些历史"变成
// "这次读依赖哪些写". 试过用全局一个计数省掉它, 单元测试当场抓到 ——
// 写 b.txt 并不改变 a.txt, 全局计数会把"重复读 a"洗白.
type stallTracker struct {
	// recent 最近几步, 用来看重复
	recent []call
	// writes 成功改动过的路径, 按顺序.
	//
	// 用它算"这次读依赖的世界状态": 不是全局一个计数 ——
	// 写 b.txt 并不改变 a.txt, 全局计数会把"重复读 a"洗白.
	// 只数**盖得住这次读的范围**的那些写.
	writes []string
	// lastKind 上一次错误的**类别**. 按类别判同不同, 不按文本 ——
	// 文本里带可变部分, 按文本比会让同一类错误每次都算"新错误"
	lastKind errKind
	// everWrote 这一轮有没有真动过手. 没动过就不数空转 ——
	// 纯问答读六个文件是正当的
	everWrote bool
	// errStreak 同一个错误连着出现几次
	errStreak int
	// step 走到第几步了. 串行一次调用是一步, 并行**一个批次也是一步**
	step int
	// errStep 上一次记错误是在哪一步 —— 同一步里不重复累加
	errStep int
	// noWrite 连着多少步没有写入
	noWrite int
	// warned 已经提醒过哪些指纹 —— 同一件事只提醒一次, 提醒两次就该停了
	warned map[string]bool
}

func newStallTracker() *stallTracker {
	return &stallTracker{warned: map[string]bool{}}
}

// 阈值. 定得保守一点 —— 误报的代价是白花一轮提醒,
// 漏报的代价是在同一个坑里烧几十轮.
const (
	repeatWarnAt = 2 // 同一个调用第 2 次 → 提醒
	repeatStopAt = 3 // 第 3 次 → 停
	errWarnAt    = 2 // 同一个错误第 2 次 → 提醒换角度
	errStopAt    = 3
	noWriteWarn  = 6 // 连着 6 步只读不写 → 提醒该动手了
)

type stallVerdict int

const (
	stallNone stallVerdict = iota
	// stallWarn 注入一条提醒当作观察, 让模型自己纠正
	stallWarn
	// stallStop 停止, 交还给用户 —— 继续只会烧钱
	stallStop
)

// observe 记一步, 返回该不该干预.
// forgetFolded 结果已经被折出上下文的那些调用, **不再算作"上一次"**.
//
// ── 为什么这属于"世界变了" ──
//
// 上面那条通则说得清楚: 该判的不是"这次调用跟上次长得一样吗", 而是
// **"这次调用还会不会带来新信息"**; 指纹 = 意图 + 它所依赖的世界状态.
//
// 折叠就是另一种"世界变了" —— 结果被腾出上下文之后, 同样的读**确实**
// 带来新信息(把内容重新装回来). 原来的指纹只算了文件写入, 没算上下文驱逐.
//
// 中途补充的要求("每片也统计 IP")可能是**追溯性**的,
// 于是必须重读已经读过的片 —— 而那些片的结果早被折掉了, 模型会说
// "原文已被系统腾掉, 我需要重读". 系统先扔掉结果, 再因为它去重取而
// 责备它"第 2 次用同样的参数, 结果不会变, 基于已有结果继续" ——
// **那条建议根本没法执行**, 已有结果不存在了.
//
// 而且这不只是噪音: 第 3 次会直接判 stallStop 停掉整轮.
// 系统自己造成的状况, 算在 agent 头上并停它的活.
func (t *stallTracker) forgetFolded(ids map[string]bool) {
	if len(ids) == 0 {
		return
	}
	kept := t.recent[:0]
	for _, r := range t.recent {
		if r.id != "" && ids[r.id] {
			continue // 它的结果已经不在上下文里了, 重取是正当的
		}
		kept = append(kept, r)
	}
	t.recent = kept
}

func (t *stallTracker) observe(tool string, args map[string]any, obs Observation,
	mutates, worldSensitive bool) (stallVerdict, string) {
	// 串行的一次调用**就是一步** —— 并行批次那条路由调用方自己划步
	t.nextStep()
	return t.observeCall("", tool, args, obs, mutates, worldSensitive)
}

/**
 * nextStep 走到下一步.
 *
 *	"连着出现"数的是**步**, 不是调用次数: 一个并行批次里那几个调用是
 *	同时发出去的一次尝试, 它还来不及从第一个失败里学到任何东西.
 *	调用方在每一步(串行的一次调用 / 并行的一个批次)之前叫一次.
 */
func (t *stallTracker) nextStep() { t.step++ }

func (t *stallTracker) observeCall(id, tool string, args map[string]any, obs Observation,
	mutates, worldSensitive bool) (stallVerdict, string) {
	return t.observeShared(id, tool, args, obs, mutates, worldSensitive, false)
}

/**
 * observeShared 带上"这个工具的结果是不是由别人决定的".
 *
 *	shared = true 的不参与**重复调用**那条判据. 理由见 Tool.Shared:
 *	merge_up 的正常流程本来就是两步(第一次"先验一遍", 验完再调一次),
 *	如果系统把第二次调用当成重复调用, 就会因遵循协议而停掉流程.
 *
 *	**反复失败那条照旧管着它**: 那条判的是"错了还在同一个地方硬撞",
 *	跟"重复调用"是两回事.
 */
func (t *stallTracker) observeShared(id, tool string, args map[string]any, obs Observation,
	mutates, worldSensitive, shared bool) (stallVerdict, string) {
	fp := t.fingerprint(tool, args, worldSensitive)
	path := argStr(args, "path")

	// 成功的写记一笔. 失败的写没有改变世界, 不记 ——
	// 否则一次失败的写就能把原地转洗白.
	if mutates && obs.Err == "" && path != "" {
		t.writes = append(t.writes, path)
	}

	// ── 重复调用 ──
	//
	//	**结果由别人决定的那些不算**: 主干变没变、同屋的人合没合,
	//	不取决于它自己写了什么. 见 Tool.Shared
	n := 0
	if !shared {
		for _, r := range t.recent {
			if r.fp == fp {
				n++
			}
		}
		t.recent = append(t.recent, call{fp: fp, tool: tool, path: path, id: id})
		if len(t.recent) > 8 {
			t.recent = t.recent[1:]
		}
	}

	// ── 反复失败 ──
	//
	// 按**类别**判同不同, 不按文本. 错误文本里常带可变部分
	// (出现了几次、哪个路径、退出码多少), 按文本比会让同一类错误
	// 每次都算"新错误", 计数永远起不来.
	if obs.Err != "" {
		k := classify(obs.Err)
		switch {
		case k != t.lastKind:
			t.errStreak = 1
		/**
		 * **一个并行批次只算一次**.
		 *
		 *	"连着出现"说的是: 撞了墙, 没改做法, 又撞一次. 而并行批次里
		 *	那几个调用是**同时发出去的一次尝试** —— 它还来不及从第一个
		 *	失败里学到任何东西.
		 *
		 *	如果并行读四个不存在的文件时把每个失败都计入连续次数,
		 *	一个批次就会直接把计数顶到 4, 这一轮当场被停滞检测停掉.
		 *	而"一次查一批文件在不在"恰恰是最常见的并行用法.
		 *
		 *	批次之间照旧累加: 一批全挂、换个参数再来一批还全挂,
		 *	那才是真的在原地转.
		 */
		case t.errStep == t.step:
		default:
			t.errStreak++
		}
		t.lastKind = k
		t.errStep = t.step
	} else {
		t.errStreak = 0
		t.lastKind = errNone
	}

	// ── 空转 ──
	//
	// **只在这一轮真动过手之后才数.** 纯问答任务(用户问"这个项目是干嘛的")
	// 读六个文件是完全正当的, 提醒它"该动手了"是误报.
	if mutates {
		t.noWrite = 0
		t.everWrote = true
	} else if t.everWrote {
		t.noWrite++
	}

	// 判定顺序: 先看最确定的信号 (重复调用), 再看错误, 最后看空转
	switch {
	case n+1 >= repeatStopAt:
		return stallStop, fmt.Sprintf(
			"你已经用同样的参数调了 %d 次 %s，结果没变。"+
				"这一轮到此为止 —— 剩下的话由你说。", n+1, tool)

	case n+1 >= repeatWarnAt && !t.warned[fp]:
		t.warned[fp] = true
		return stallWarn, fmt.Sprintf(
			"这是你第 %d 次用同样的参数调 %s，结果一样。", n+1, tool)

	case t.errStreak >= errStopAt:
		msg := fmt.Sprintf("同一类错误连着出现 %d 次了。", t.errStreak)
		if a := advise(t.lastKind, t.errStreak); a != "" {
			// 停之前把最后一条具体建议给出去 —— 用户会看到这句话,
			// 他要判断的是"值不值得让它再试", 而不是"它到底卡在哪"
			msg += a + " "
		}
		return stallStop, msg + "这一轮到此为止。"

	case t.errStreak >= errWarnAt:
		// **给能直接照做的一条指令, 不给"换个思路"**.
		//
		// 模型卡住的时候恰恰是它判断力最差的时候, 让它自己想办法
		// 跟没说一样. 每一级给不同的动作, 重复上一级等于告诉它
		// "你没听懂" —— 而它确实没辙.
		if a := advise(t.lastKind, t.errStreak); a != "" {
			return stallWarn, fmt.Sprintf("同一类错误第 %d 次了。%s", t.errStreak, a)
		}
		return stallWarn, fmt.Sprintf("同一类错误第 %d 次了。", t.errStreak)

	case t.noWrite == noWriteWarn:
		return stallWarn, fmt.Sprintf("你已经连着 %d 步只在看、没有动手。", t.noWrite)
	}
	return stallNone, ""
}

// worldFor 有多少次写可能影响到这次读.
//
// 只数**盖得住 readPath 的**那些写:
//
//	读 a.txt, 写 b.txt      → 不影响, 重复读 a 仍是原地转
//	读 site/proj (目录), 写 site/proj/README.md → 影响, 再搜一遍是验证
//
// 目录这一条是真机撞出来的: 改完 README 再 search 整个目录确认遗漏,
// 按"路径相等"判会漏掉, 于是把提示词要求的验证动作判成了原地转.
func (t *stallTracker) worldFor(readPath string) int {
	n := 0
	for _, w := range t.writes {
		if covers(readPath, w) {
			n++
		}
	}
	return n
}

// covers 这次读的范围盖不盖得住那个被改的路径
func covers(readPath, writtenPath string) bool {
	if readPath == writtenPath {
		return true
	}
	if readPath == "" || readPath == "." {
		return true // 读的是当前目录, 盖住一切
	}
	return strings.HasPrefix(writtenPath, strings.TrimSuffix(readPath, "/")+"/")
}

// ── 这个工具会不会改动世界, 由**工具表声明**, 不按名字猜 ──
//
// 原来这里是 `HasPrefix(tool, "write"/"edit"/"delete")`. 同一个"按名字
// 前缀猜"的错我在 agent.go 已经改成声明式了 (Tool.Mutates), **这里漏了**
// —— 改了一处漏一处, 正是隐式白名单必然的下场.
//
// 漏的代价是实打实的, 而且 `run` 一加进来就中招:
//
//	`run` 不以那三个前缀开头 → 被当成只读工具
//	  ① noWrite 计数一路涨: agent 明明在用 run 干活(跑构建、改文件),
//	    第 6 步却被提醒"你连着 6 步只在看"
//	  ② worldFor() 拿不到 run 造成的世界变化: run 改完文件再读同一个文件,
//	    会被判成原地转 —— **干得越对死得越快**, 跟当年写工具指纹那个坑同源
//
// 所以判定必须跟着调用一起传进来, 由工具表说了算.

// fingerprint 一次调用的指纹.
//
// 读和写的判据是相反的, 因为"重复"对两者意味着不同的事:
//
//	只读工具   指纹含**全部参数**.
//	  参数不同 = 要的东西不同 = 有进展. 翻页 (offset=1 → 401) 是
//	  这是正常的分页行为, 判成卡住会把它拦死 —— 第 3 页被停后,
//	  模型只能退回第 1 页.
//
//	写工具     指纹含 path **和"这次动的是哪儿/写的是什么"**.
//	  真正的卡住是**一模一样地再写一遍**, 不是"又改了同一个文件".
//
// ── 写工具必须区分同一文件上的不同改动 ──
//
// 上一版写的是"写工具只含 path", 理由是"反复重写同一个文件恰恰是卡住的信号".
// 那句话对 write_file 勉强成立, 对 edit_file **完全错**:
// 一次改动本来就该拆成好几个小 edit, 每个动一处 —— 那正是
// 提示词里要求的"只动需要动的地方".
//
// 给待办清单加一个按钮时, 5 个 edit **全部成功**
// (app.js 1653→1723→1798→2000, 每次都真的改了),
// 却被判成"用同样的参数调了 3 次"然后硬停. 干得越对, 死得越快.
//
// 现在按"这次动的是哪儿"区分: edit_file 看 old_string(锚点),
// write_file 看 content. 一模一样地再来一遍才算原地转.
func (t *stallTracker) fingerprint(tool string, args map[string]any, worldSensitive bool) string {
	var b strings.Builder
	// **依赖世界的调用, 世界状态进指纹.**
	//
	// 它变过, 同样的调用就会给出不同结果 —— 那不是原地转.
	// 这一项替掉了"清该路径读历史"整段补丁, 也是 `run` 反复跑同一条
	// 测试不被误判的原因: 改完代码再跑, 世界变了, 指纹自然不同.
	//
	// 反过来, 纯写工具不含世界状态: 同样的内容写两遍, 世界怎么变
	// 都是原地转. 含了的话第二次写会因为第一次写改变了世界而被放过.
	if worldSensitive {
		fmt.Fprintf(&b, "w%d|", t.worldFor(argStr(args, "path")))
	}
	b.WriteString(tool)
	// 参数全进指纹, 按键名排序 —— map 遍历顺序随机, 不排就不稳定.
	// 值取哈希: 指纹要短, 而且不该把整个文件内容留在检测器的历史里.
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%s", k, short(fmt.Sprint(args[k])))
	}
	return b.String()
}

// short 值的短摘要. 超过一定长度就取哈希 ——
// 指纹要短且稳定, 而且不该把整个文件内容留在检测器的历史里.
func short(v string) string {
	if len(v) <= 64 {
		return v
	}
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:8])
}

func digest(args map[string]any) string {
	body := argStr(args, "old_string")
	if body == "" {
		body = argStr(args, "content")
	}
	if body == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha1.Sum([]byte(body)))[:12]
}
