package osinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/neox-os/neox-os/abi"
)

// AbiClient 进程侧的 ABI 客户端.
//
// 应用拿到的就是这个 —— 它拿不到 OS 实例, 只有一条 socket.
// 所以它写不进别人的日志、改不了别人的状态.
//
// 跟 TS 版 (packages/init/src/abiClient.ts) 认同一份线格式,
// 两边可以互换. 跨语言对拍测试锁着这一点.
type AbiClient struct {
	conn net.Conn
	dec  abi.Decoder

	mu      sync.Mutex
	nextID  int
	pending map[int]chan abi.Response
	closed  bool

	pid           abi.ProcessID
	abiVersion    string
	contextTokens int64
	hasSearch     bool
	hasVision     bool
	// done 连接断了就关 —— 应用据此知道 OS 没了, 自己收尾
	done chan struct{}
	once sync.Once
}

// DialABI 连 OS. socketPath 与 token 由 OS 通过 env 注入.
func DialABI(socketPath, token string) (*AbiClient, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("连 ABI 失败: %w", err)
	}
	c := &AbiClient{conn: conn, nextID: 1,
		pending: map[int]chan abi.Response{}, done: make(chan struct{})}
	go c.readLoop()

	res, err := c.call(abi.MHello, abi.HelloParams{Token: token, Wire: abi.WireVersion})
	if err != nil {
		c.Close()
		return nil, err
	}
	c.pid, c.abiVersion = res.PID, res.ABIVersion
	c.contextTokens = res.ContextTokens
	c.hasSearch = res.HasSearch
	c.hasVision = res.HasVision
	return c, nil
}

// FromEnv 按 OS 注入的环境变量连上 —— 被 spawn 的进程用这个
func FromEnv() (*AbiClient, error) {
	sock, token := os.Getenv("NEOX_ABI_SOCKET"), os.Getenv("NEOX_ABI_TOKEN")
	if sock == "" || token == "" {
		return nil, errors.New("缺 NEOX_ABI_SOCKET / NEOX_ABI_TOKEN —— 这个进程不是被 OS 起的")
	}
	return DialABI(sock, token)
}

func (c *AbiClient) PID() abi.ProcessID    { return c.pid }
func (c *AbiClient) Done() <-chan struct{} { return c.done }

func (c *AbiClient) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			frames, derr := c.dec.Push(buf[:n])
			if derr != nil {
				c.failAll(derr)
				return
			}
			for _, body := range frames {
				var res abi.Response
				if json.Unmarshal(body, &res) != nil {
					continue
				}
				// **按 id 归位**, 不按到达顺序 —— 并发请求的回复允许乱序.
				// 找不到归属的迟到回复直接丢, 不猜.
				c.mu.Lock()
				ch := c.pending[res.ID]
				delete(c.pending, res.ID)
				c.mu.Unlock()
				if ch != nil {
					ch <- res
				}
			}
		}
		if err != nil {
			c.failAll(err)
			return
		}
	}
}

// failAll 连接断了 = OS 没了. 在途请求必须立刻失败, 不能永远挂着.
func (c *AbiClient) failAll(err error) {
	c.mu.Lock()
	c.closed = true
	pend := c.pending
	c.pending = map[int]chan abi.Response{}
	c.mu.Unlock()
	for _, ch := range pend {
		ch <- abi.Response{Error: &abi.WireError{Code: abi.ErrInternal, Message: "ABI 连接断开"}}
	}
	c.once.Do(func() { close(c.done) })
}

func (c *AbiClient) call(method abi.Method, params any) (*abi.Result, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("ABI 未连接")
	}
	id := c.nextID
	c.nextID++
	ch := make(chan abi.Response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	frame, err := abi.EncodeFrame(abi.Request{ID: id, Method: method, Params: raw})
	if err != nil {
		return nil, err
	}
	if _, err := c.conn.Write(frame); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	res := <-ch
	if res.Error != nil {
		return nil, fmt.Errorf("[%s] %s", res.Error.Code, res.Error.Message)
	}
	return res.Result, nil
}

// Emit 产出结构化输出.
//
// **同步等 ack**. 早先做成即发即忘 (省一次往返), 结果进程回完话立刻退出,
// 最后那条输出还没发出去连接就断了 —— 用户看到的是"它什么都没说".
//
// 省下的是 unix socket 上的几十微秒, 而一次模型往返是秒级 ——
// 这个优化从一开始就不该做. 丢消息比慢 50µs 严重得多.
func (c *AbiClient) Emit(payload any) {
	_, _ = c.call(abi.MEmit, abi.EmitParams{Payload: payload})
}

// Can 能力预检. 跨进程只有异步版 —— 不做假的同步.
func (c *AbiClient) Can(axis abi.CapAxis, scope string) (bool, error) {
	r, err := c.call(abi.MCan, abi.CanParams{Axis: axis, Scope: scope})
	if err != nil {
		return false, err
	}
	if r.Value == nil {
		return false, errors.New("回复缺 value")
	}
	return *r.Value, nil
}

// Spend 记一笔消耗. 超止损线返回错误.
func (c *AbiClient) Spend(d abi.BudgetDelta) error {
	_, err := c.call(abi.MSpend, abi.SpendParams{Delta: d})
	return err
}

// Decide 请求一个人类决策.
//
// **这个调用可能几小时才回** —— 进程就该在这儿等着.
// 它不占 CPU, 也不该有传输层超时: 超时要由 OnTimeout 表达.
func (c *AbiClient) Decide(req abi.DecisionRequest) (abi.DecisionResolution, error) {
	r, err := c.call(abi.MDecide, abi.DecideParams{Request: req})
	if err != nil {
		return abi.DecisionResolution{}, err
	}
	if r.Resolution == nil {
		return abi.DecisionResolution{}, errors.New("回复缺 resolution")
	}
	return *r.Resolution, nil
}

// Infer 请求一次推理.
//
// agent **不自己连模型** —— key 在 OS 手里, 它也不需要出网能力.
func (c *AbiClient) Infer(p abi.InferParams) (abi.InferResult, error) {
	r, err := c.call(abi.MInfer, p)
	if err != nil {
		return abi.InferResult{}, err
	}
	if r.Infer == nil {
		return abi.InferResult{}, errors.New("回复缺 infer")
	}
	return *r.Infer, nil
}

// Search 搜一次公开网络.
//
// 跟 Infer 同一条: **key 在 OS 手里**, 进程不需要出网能力.
// 拿回来的只有标题/链接/摘要 —— 要正文得用 fetch, 而那一步过能力集.
func (c *AbiClient) Search(p abi.SearchParams) (abi.SearchResult, error) {
	r, err := c.call(abi.MSearch, p)
	if err != nil {
		return abi.SearchResult{}, err
	}
	if r.Search == nil {
		return abi.SearchResult{}, errors.New("回复缺 search")
	}
	return *r.Search, nil
}

// See 让模型看一张图并回答一个问题.
//
// 图片走 base64 送给 OS, 不送路径: OS 未必看得见同一个文件系统,
// 而且传路径等于给了一条"让 OS 替我读任意文件"的旁路.
func (c *AbiClient) See(p abi.SeeParams) (abi.SeeResult, error) {
	r, err := c.call(abi.MSee, p)
	if err != nil {
		return abi.SeeResult{}, err
	}
	if r.See == nil {
		return abi.SeeResult{}, errors.New("回复缺 see")
	}
	return *r.See, nil
}

// Recall 查以前的对话. 当前这段 OS 会排除 —— 它已经在 Window 里.
func (c *AbiClient) Recall(p abi.RecallParams) (abi.RecallResult, error) {
	r, err := c.call(abi.MRecall, p)
	if err != nil {
		return abi.RecallResult{}, err
	}
	if r.Recall == nil {
		return abi.RecallResult{}, errors.New("回复缺 recall")
	}
	return *r.Recall, nil
}

// HasVision 这台机器认不认图. OS 在握手时告诉我们.
func (c *AbiClient) HasVision() bool { return c.hasVision }

// History 取 OS 授权本进程接续的那段对话.
//
// 取不到就是空 —— 这是新对话的正常情况, 不是错误.
func (c *AbiClient) History() ([]abi.Event, error) {
	r, err := c.call(abi.MHistory, abi.HistoryParams{})
	if err != nil {
		return nil, err
	}
	if r.History == nil {
		return nil, nil
	}
	return r.History.Events, nil
}

// ContextTokens 模型窗口, OS 在握手时告诉我们.
//
// 0 = OS 也不知道. 上层要用保守缺省, **不许自己查表编一个** ——
// 进程不知道自己在用哪个模型, 编出来的数字换个模型就是错的.
func (c *AbiClient) ContextTokens() int64 { return c.contextTokens }

// HasSearch 这台机器有没有搜索服务. OS 在握手时告诉我们.
//
// false 时**不要把 web_search 挂进工具表** —— 见 abi.Result.HasSearch.
func (c *AbiClient) HasSearch() bool { return c.hasSearch }

// Recv 等下一句用户输入. 会阻塞, 可能很久 —— 那正是待命的样子.
func (c *AbiClient) Recv() (abi.RecvResult, error) {
	r, err := c.call(abi.MRecv, abi.RecvParams{})
	if err != nil {
		return abi.RecvResult{}, err
	}
	if r.Recv == nil {
		return abi.RecvResult{}, errors.New("回复缺 recv")
	}
	return *r.Recv, nil
}

// TryRecv 看一眼有没有新话, **不等**. ok=false = 现在没有.
//
// 长任务跑到一半用户插话("停, 别读了""顺便也统计一下 IP"), 只有靠它
// 才听得见 —— Serve 那个 Recv 只在两轮之间调, 一轮跑起来进程就是聋的.
func (c *AbiClient) TryRecv() (abi.RecvResult, bool) {
	r, err := c.call(abi.MRecv, abi.RecvParams{TimeoutMs: -1})
	if err != nil || r.Recv == nil {
		return abi.RecvResult{}, false
	}
	// 空 Text 且没关 = 现在没有新话
	if r.Recv.Text == "" && !r.Recv.Closed {
		return abi.RecvResult{}, false
	}
	return *r.Recv, true
}

func (c *AbiClient) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	_ = c.conn.Close()
	c.once.Do(func() { close(c.done) })
}
