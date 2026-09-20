package osinit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// AbiServer 把 ProcessContext 从"同进程函数调用"变成"跨进程 IPC".
//
// 这一步买到三件事:
//
//  1. **执行体崩了 OS 不崩**. 之前进程是同一个堆里的闭包, 它抛什么
//     OS 就吃什么. 现在它死了只是一条连接断掉.
//  2. **语言边界**. 系统层 Go, 引擎层 TS, 两边只认线格式.
//  3. **权限边界**. 进程只拿得到 socket, 拿不到 OS 实例, 所以它
//     写不进别人的日志、改不了别人的状态.
//
// 身份: 每个进程 spawn 时发一个一次性 token (走 env), 连上先 hello.
// token → pid 映射只在 OS 内存里; 进程终态后 token 立即失效.
//
// sun_path 长度限制: macOS 104 字节 / Linux 108 字节.
// socket 路径必须短, 放 /run/neox-os/<pid>.sock 而不是深目录.

const MaxSocketPath = 100

var ErrSocketPathTooLong = errors.New("socket 路径过长")

type AbiServer struct {
	path    string
	resolve func(token string) (abi.ProcessID, ProcessContext, bool)
	onError func(error)
	// os 用于分发 infer —— 推理是 OS 提供的服务
	os *OS
	// resumeEvents 这个进程被授权接续的那段对话.
	//
	// **由 OS 在起进程时定死**, 不接受进程自己指定 ——
	// 认参数就等于一个进程能读到别人的对话.
	resumeEvents []abi.Event

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
}

func NewAbiServer(path string,
	resolve func(string) (abi.ProcessID, ProcessContext, bool),
	onError func(error)) (*AbiServer, error) {
	if len(path) > MaxSocketPath {
		// 提前炸掉, 否则 bind 会给一个很难懂的 EINVAL/ENAMETOOLONG
		return nil, fmt.Errorf("%w (%d > %d): %s", ErrSocketPathTooLong, len(path), MaxSocketPath, path)
	}
	return &AbiServer{path: path, resolve: resolve, onError: onError,
		conns: map[net.Conn]struct{}{}}, nil
}

// WithOS 让这个服务能分发 infer. 不设就等于这台机器不提供推理.
func (s *AbiServer) WithOS(o *OS) *AbiServer { s.os = o; return s }

func (s *AbiServer) Listen() error {
	// 残留的 socket 文件会让 bind 报 address already in use ——
	// 上次没干净退出时常见
	_ = os.Remove(s.path)
	l, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = l
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return // listener 关了
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				_ = c.Close()
				return
			}
			s.conns[c] = struct{}{}
			s.mu.Unlock()

			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handle(c)
			}()
		}
	}()
	return nil
}

func (s *AbiServer) handle(c net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.Close()
	}()

	var dec abi.Decoder
	// 未 hello 之前不给任何能力 —— 缺省拒绝
	var bound ProcessContext
	// 写 socket 要串行 —— 两个 goroutine 同时写会把帧交错撕碎
	var writeMu sync.Mutex

	reply := func(r abi.Response) {
		frame, err := abi.EncodeFrame(r)
		if err != nil {
			return
		}
		writeMu.Lock()
		_, _ = c.Write(frame)
		writeMu.Unlock()
	}
	fail := func(id int, code abi.ErrorCode, msg string) {
		reply(abi.Response{ID: id, Error: &abi.WireError{Code: code, Message: msg}})
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			frames, derr := dec.Push(buf[:n])
			if derr != nil {
				// 帧坏了不试图恢复 —— 恢复就是给攻击者留缝
				if s.onError != nil {
					s.onError(derr)
				}
				return
			}
			for _, body := range frames {
				var req abi.Request
				if json.Unmarshal(body, &req) != nil || req.Method == "" {
					fail(0, abi.ErrBadRequest, "缺 id 或 method")
					continue
				}

				if req.Method == abi.MHello {
					var p abi.HelloParams
					_ = json.Unmarshal(req.Params, &p)
					if p.Wire != abi.WireVersion {
						fail(req.ID, abi.ErrWireVersion,
							fmt.Sprintf("需要线格式 v%d", abi.WireVersion))
						return
					}
					pid, ctx, ok := s.resolve(p.Token)
					if !ok {
						fail(req.ID, abi.ErrUnauthenticated, "token 不认")
						return
					}
					bound = ctx
					reply(abi.Response{ID: req.ID, Result: &abi.Result{
						Kind: "hello", PID: pid, ABIVersion: ctx.ABIVersion(),
						ContextTokens: s.contextTokens(),
						HasSearch:     s.os != nil && s.os.Searcher() != nil,
						HasVision:     s.os != nil && s.os.Viewer() != nil}})
					continue
				}

				if bound == nil {
					fail(req.ID, abi.ErrUnauthenticated, "必须先 hello")
					return
				}
				// 每个请求各跑一个 goroutine —— decide 可能挂几小时,
				// 不能把同一条连接上的其它调用堵死
				go s.dispatch(req, bound, reply)
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *AbiServer) dispatch(req abi.Request, ctx ProcessContext, reply func(abi.Response)) {
	ok := func(r *abi.Result) { reply(abi.Response{ID: req.ID, Result: r}) }
	bad := func(code abi.ErrorCode, msg string) {
		reply(abi.Response{ID: req.ID, Error: &abi.WireError{Code: code, Message: msg}})
	}

	switch req.Method {
	case abi.MEmit:
		var p abi.EmitParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		ctx.Emit(p.Payload)
		ok(&abi.Result{Kind: "ok"})

	case abi.MCan:
		var p abi.CanParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		v := ctx.Can(string(p.Axis), p.Scope)
		ok(&abi.Result{Kind: "bool", Value: &v})

	case abi.MSpend:
		var p abi.SpendParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		if err := ctx.Spend(p.Delta); err != nil {
			bad(abi.ErrBudgetExceeded, err.Error())
			return
		}
		ok(&abi.Result{Kind: "ok"})

	case abi.MDecide:
		var p abi.DecideParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		// **空白的审批必须当场拒掉, 不能端到人面前.**
		//
		// 外部调用方可能把 decide 的参数摊平,
		// 少了一层 request —— Go 侧解出一个零值 DecisionRequest,
		// 于是弹给用户的是一个**标题为空、什么都没有的审批框**.
		//
		// 让人去批准一个看不懂的东西, 比直接报错危险得多: 他要么
		// 乱点一个, 要么以为系统坏了. 而且批准之后 scope 也是空的,
		// 授权同样落不下去 —— 从头到尾没有一处会说"你刚才批的是空的".
		//
		// 参数不对是**调用方的错**, 该让调用方看见, 不该转嫁给用户.
		if strings.TrimSpace(p.Request.Present.Title) == "" {
			bad(abi.ErrBadRequest,
				"decide 缺 present.title —— 不会把一个空白审批框端给用户。"+
					"检查参数形状: 决策要放在 params.request 里, 不是摊平在 params 上")
			return
		}
		// 这个请求可能几小时后才回 —— 决策活得比任何一次运行都长.
		// 刻意不设超时: 超时该由 DecisionRequest.OnTimeout 表达,
		// 传输层不决定请求何时超时.
		res, err := ctx.Decide(p.Request)
		if err != nil {
			bad(abi.ErrProcessGone, err.Error())
			return
		}
		ok(&abi.Result{Kind: "decision", Resolution: &res})

	case abi.MHistory:
		// 只给它 OS 授权它接续的那个 pid —— 参数里说取谁一概不认.
		// 认参数就等于一个进程能读到别人的对话.
		evs := s.resumeEvents
		ok(&abi.Result{Kind: "history", History: &abi.HistoryResult{Events: evs}})

	case abi.MRecv:
		var rp abi.RecvParams
		_ = json.Unmarshal(req.Params, &rp)
		// TimeoutMs < 0 = **只看一眼, 不等**.
		//
		// 这个字段在协议里早就声明了("0 = 一直等"), 但服务端从来没读过它 ——
		// 又一个"声明了却不生效"的东西. 长任务跑到一半用户插话, 靠的就是它.
		if rp.TimeoutMs < 0 {
			msg, has := ctx.TryRecv()
			if !has {
				// 空 Text 且没关 = 现在没有新话. 见 abi.RecvResult 的说明
				ok(&abi.Result{Kind: "recv", Recv: &abi.RecvResult{}})
				return
			}
			ok(&abi.Result{Kind: "recv", Recv: &msg})
			return
		}
		// 可能挂很久 —— 跟 decide 一样, 刻意不设传输层超时.
		// 待命进程就该一直等着.
		msg, err := ctx.Recv()
		if err != nil {
			bad(abi.ErrProcessGone, err.Error())
			return
		}
		ok(&abi.Result{Kind: "recv", Recv: &msg})

	case abi.MInfer:
		var p abi.InferParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		if s.os == nil || s.os.Provider() == nil {
			bad(abi.ErrInferFailed, "这台机器没有配置推理服务")
			return
		}
		res, err := s.os.Provider().Infer(context.Background(), p)
		if err != nil {
			bad(abi.ErrInferFailed, err.Error())
			return
		}
		// **token 由 OS 按供应商回报的真实用量记账** ——
		// agent 报不报、报多少都一样, 止损线躲不掉.
		total := billableTokens(res)
		if serr := ctx.Spend(abi.BudgetDelta{Tokens: &total}); serr != nil {
			// 已经花掉了才发现超限: 结果照给 (钱都付了), 但把超限报出来,
			// agent 下一次调用就会被 Spend 拦住
			ok(&abi.Result{Kind: "infer", Infer: &res})
			return
		}
		ok(&abi.Result{Kind: "infer", Infer: &res})

	case abi.MSearch:
		var p abi.SearchParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		if s.os == nil || s.os.Searcher() == nil {
			// **这句话要说清"是这台机器没配", 不是"搜索失败了"**:
			// 后者会让 agent 换个词重试, 而那件事永远不会成
			bad(abi.ErrSearchFailed, "这台机器没有配置搜索服务")
			return
		}
		res, err := s.os.Searcher().Search(context.Background(), p)
		if err != nil {
			bad(abi.ErrSearchFailed, err.Error())
			return
		}
		// 搜索也是往外花钱的动作, 但**它按次计价而不是按 token** ——
		// 记进事件日志留痕, 不混进 token 账: 混了的话止损线上那个数字
		// 就不再等于"模型烧了多少", 排查时没人分得清
		ctx.Emit(map[string]any{"phase": "search", "query": p.Query,
			"provider": res.Provider, "hits": len(res.Hits)})
		ok(&abi.Result{Kind: "search", Search: &res})

	case abi.MSee:
		var p abi.SeeParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		if s.os == nil || s.os.Viewer() == nil {
			// 同搜索: 说清"这台机器不认图", 不是"看图失败了" ——
			// 后者会让 agent 换张图重试, 而那件事永远不会成
			bad(abi.ErrVisionFailed, "这台机器不认图(没有配置视觉模型)")
			return
		}
		res, err := s.os.Viewer().See(context.Background(), p)
		if err != nil {
			bad(abi.ErrVisionFailed, err.Error())
			return
		}
		// 留痕: 问了什么、谁看的. **不记图片本身** ——
		// 账本是给人翻的, 一张 base64 图会把它冲垮
		ctx.Emit(map[string]any{"phase": "see", "question": p.Question,
			"model": res.Model, "bytes": len(p.DataB64)})
		/**
		 * **看图那笔钱走跟推理同一条路**.
		 *
		 *	发一条一模一样的 usage, 于是花费页、每轮上限、测试台的判据
		 *	三处一次性都对上了 —— 它们本来就都在听这一条.
		 *
		 *	如果这笔钱不记账, "看得见图"的一轮可能报 spent: 354,
		 *	但刚刚已经看完一张图. 成本是重要指标, 一笔查不到的开销
		 *	比数字大一点更糟.
		 */
		if res.PromptTokens > 0 || res.CompletionTokens > 0 {
			ctx.Emit(map[string]any{"phase": "usage", "prompt": res.PromptTokens,
				"completion": res.CompletionTokens, "cached": 0, "what": "看图"})
		}
		ok(&abi.Result{Kind: "see", See: &res})

	case abi.MRecall:
		var p abi.RecallParams
		if json.Unmarshal(req.Params, &p) != nil {
			bad(abi.ErrBadRequest, "参数解不开")
			return
		}
		if strings.TrimSpace(p.Query) == "" {
			bad(abi.ErrBadRequest, "query 是空的。给一组关键词")
			return
		}
		if s.os == nil {
			bad(abi.ErrBadRequest, "这台机器没有事件日志")
			return
		}
		res := s.os.Recall(ctx.PID(), p)
		// 留痕: 查了什么、命中几条. 不记正文 —— 正文已经在账本里
		ctx.Emit(map[string]any{"phase": "recall", "query": p.Query, "hits": len(res.Hits)})
		ok(&abi.Result{Kind: "recall", Recall: &res})

	default:
		bad(abi.ErrBadRequest, "未知方法: "+string(req.Method))
	}
}

func (s *AbiServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	l := s.listener
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	if l != nil {
		_ = l.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
	_ = os.Remove(s.path)
	return nil
}

// contextTokens 模型窗口, hello 时告诉进程.
//
// 供应商不知道就返回 0, 让进程用保守缺省 —— 编一个数字比不知道更危险.
func (s *AbiServer) contextTokens() int64 {
	if s.os == nil || s.os.provider == nil {
		return 0
	}
	if cs, ok := s.os.provider.(engine.ContextSized); ok {
		return cs.ContextTokens()
	}
	return 0
}

// billableTokens 这一次推理该记多少账.
//
// ── 缓存命中不能按全价算 ──
//
// 原来是 prompt + completion 一把加. 那在**有前缀缓存**的场景下是严重高估:
// 一次做工程的对话, 缓存命中率稳定在 97~100% —— 也就是说
// 记账数字比真实开销大了将近十倍, 止损线也就提前十倍触发.
//
// 现象非常有迷惑性: 进程停在"止损线到了", 而对话本身才十几轮、
// 上下文才 11K token. 看着像是预算配小了, 其实是**记账口径错了** ——
// 同一段前缀每轮都被按新 token 重新收了一次费.
//
// ── 为什么是 1/10 而不是 0 ──
//
// 缓存命中**不是免费**的, 主流供应商大致收到未命中价的十分之一.
// 记成 0 会让长对话看起来永远不花钱, 那是另一个方向的错.
// 这个系数不追求跟某一家的价目表逐分对齐 —— 止损线要的是
// "别跑飞", 不是账单.
const cachedTokenWeightDiv = 10

func billableTokens(r abi.InferResult) int64 {
	cached := r.CachedTokens
	// 供应商偶尔报出 cached > prompt 的数 —— 直接相减会变成负数,
	// 于是这一轮不但不花钱还**倒退**, 止损线永远到不了.
	if cached > r.PromptTokens {
		cached = r.PromptTokens
	}
	if cached < 0 {
		cached = 0
	}
	fresh := r.PromptTokens - cached
	return fresh + cached/cachedTokenWeightDiv + r.CompletionTokens
}
