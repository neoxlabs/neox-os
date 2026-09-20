package agent

import "sync"

// 上下文预算怎么定.
//
// 之前是写死 60KB 加一个 env 覆盖. 不同模型的窗口差一个数量级,
// 写死一个数**一定有一头是错的**: 大窗口模型白白浪费, 小窗口模型直接爆.
//
// ── 两种单位不能混比 ──
//
// 预算算的是**字节**(页表量的是字节), 模型窗口给的是 **token**.
// 中文一个 token 大概 1.5~2 字节, 英文和代码大概 3.5~4 字节 ——
// 差一倍多. 拍一个换算系数, 等于在两种内容上各错一半.
//
// 所以系数直接取每一轮同时可得的两个数据:
//
//	发出去多少字节   (自己拼的 messages, 精确)
//	算成多少 token   (供应商在 usage 里回报, 权威)
//
// 比率就是这两个数的商, 不用猜. 而且它随内容自适应 ——
// 一个中文对话和一个代码仓库的比率天然不同, 各自会收敛到各自的值.
//
// ── 预算不等于窗口 ──
//
// 窗口是硬上限, 预算必须留出余量给:
//
//	系统提示词   4KB 上下, 每轮都发
//	模型输出     最多 16k tokens, 写整个文件那一步就是这么费
//	推理         V4 这类模型的 reasoning 也占 completion
//
// 所以只拿窗口的一半当历史预算. 留少了的代价是**整轮任务失败**
// (输出被截断), 留多了只是历史短一点 —— 代价完全不对称.
const budgetShare = 0.5

// 没有观测数据时的起步系数. 取偏小的那一头(中文) ——
// 猜大了会真的爆窗口, 猜小了只是第一轮历史短一点.
const initialBytesPerToken = 1.8

// 供应商没说窗口时的保守缺省 (tokens).
// 宁可小: 小了只是历史短, 大了是每一轮都报错.
const fallbackContextTokens = 32000

// Budgeter 把模型窗口换算成字节预算.
//
// 并发安全: 观测来自推理回调, 读取来自拼 messages, 不在同一条路径上.
type Budgeter struct {
	mu sync.Mutex
	// contextTokens 模型窗口, OS 在 hello 时告诉我们.
	// agent 自己不查表 —— 它根本不知道自己在用哪个模型, 那是 OS 的事.
	contextTokens int64
	bytesPerToken float64
	samples       int
}

func NewBudgeter(contextTokens int64) *Budgeter {
	if contextTokens <= 0 {
		contextTokens = fallbackContextTokens
	}
	return &Budgeter{contextTokens: contextTokens, bytesPerToken: initialBytesPerToken}
}

// Observe 记一次真实的"字节 → token"换算.
//
// promptTokens 是**整个请求**的量(含系统段), sentBytes 也要是整个请求的,
// 两边口径必须一致 —— 拿历史的字节数去除以整个请求的 token 数,
// 会系统性低估比率, 于是预算定得偏小, 白白浪费窗口.
func (b *Budgeter) Observe(sentBytes int, promptTokens int64) {
	if sentBytes <= 0 || promptTokens <= 0 {
		return
	}
	r := float64(sentBytes) / float64(promptTokens)
	// 任何真实文本的字节/token 都落在 1~5 之间 (中文一点几, 代码三四).
	// 跑到区间外说明是**记账异常**而不是内容特征 —— 比如供应商某一轮
	// 只报了未命中缓存的那部分 token. 拿它去调预算, 一次就能把预算打飞
	// 3 倍 (单元测试抓到的就是这个), 然后折叠时而触发时而不触发.
	//
	// 夹住而不是丢弃: 真有偏向性的内容(纯代码)该让比率往上走,
	// 只是不许一步走到不可能的地方.
	const loR, hiR = 0.8, 6.0
	if r < loR {
		r = loR
	} else if r > hiR {
		r = hiR
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// 滑动平均, 不是直接替换 —— 单轮可能碰上一个纯代码的大文件,
	// 让比率跳到 4 然后下一轮又跳回 1.6. 预算来回抖动会让折叠
	// 时而触发时而不触发, 前缀缓存跟着反复作废.
	b.samples++
	w := 1.0 / float64(min(b.samples, 8))
	b.bytesPerToken = b.bytesPerToken*(1-w) + r*w
}

// Bytes 当前该给历史多少字节
func (b *Budgeter) Bytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(float64(b.contextTokens) * b.bytesPerToken * budgetShare)
}

// Stats 给事件日志看 —— 预算是怎么算出来的必须看得见,
// 否则"为什么这轮折叠了"永远查不清
func (b *Budgeter) Stats() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{
		"contextTokens": b.contextTokens,
		"bytesPerToken": float64(int(b.bytesPerToken*100)) / 100,
		"samples":       b.samples,
		"budgetBytes":   int(float64(b.contextTokens) * b.bytesPerToken * budgetShare),
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
