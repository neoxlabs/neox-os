package agent

import "github.com/neox-os/neox-os/abi"

// Syscalls 是 agent 用到的那几个系统调用.
//
// ── 为什么要有这个接口 ──
//
// 之前 Agent.ABI 是具体的 *osinit.AbiClient, 于是主循环**跑不起来**
// 除非有一个真的 OS: 真的 unix socket、真的进程、真的模型.
// 结果是**整个 agent 最核心的一段代码, 唯一没有测试保护**.
//
// 代价是实打实的: 上一轮那个"空的 done 被当成完成"的 bug ——
// 模型发 {"done":""} 时, 终端只显示完成提示而没有实际回复
// 而系统认为成功 —— 只能靠真机偶然撞出来. 真机撞不出来的分支
// (预算耗尽、决策被拒、连续模型错误) 至今没人验证过.
//
// ── 为什么定在这里而不是 osinit ──
//
// 接口按**使用方**定, 不是把整个客户端照搬过来.
// agent 只用六个方法; 定在 osinit 就变成"实现方声明自己有什么",
// 那样每加一个 ABI 方法, agent 这边的假实现都得跟着改一遍,
// 而它根本不关心那些方法.
type Syscalls interface {
	// Emit 往事件日志里写一条. 不返回错误 —— 观测失败不该影响正事
	Emit(payload any)
	// Can 预检能力. 拦不住就该拒绝, 不是降级
	Can(axis abi.CapAxis, scope string) (bool, error)
	// Spend 上报消耗. 返回错误表示止损线到了
	Spend(d abi.BudgetDelta) error
	// Decide 请求外部决策. **会阻塞**, 可能等很久 —— 这表示进程处于待命状态
	Decide(req abi.DecisionRequest) (abi.DecisionResolution, error)
	// Infer 推理. 凭据只有 OS 持有, agent 不需要出网能力
	Infer(p abi.InferParams) (abi.InferResult, error)
	// Recv 等下一句用户输入. 会阻塞
	Recv() (abi.RecvResult, error)
	// TryRecv 看一眼有没有新话, **不等**. ok=false = 现在没有.
	//
	// 一轮活跑起来之后, Recv 要等到这轮结束才会被调 —— 那段时间里
	// 进程是聋的. 用户中途说"停"或者补一句要求, 只有靠它才听得见.
	TryRecv() (abi.RecvResult, bool)
}
