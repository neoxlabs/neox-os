package osinit

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 感知层 · 采集入口.
//
// ── 为什么入口在 OS 里, 而采集端在外面 ──
//
// 这一刀是整个感知层的形状:
//
//	采集端(手机 APP / 家居桥接 / 来电监听 / 一条 curl)   外部, 可换, 可以有很多
//	          │  POST /signal
//	          ▼
//	信号总线(OS)                                        唯一, 落事件日志, 唤醒进程
//
// **OS 只认 Signal 这一种形状**, 于是加一个新数据源不用改 OS 一行 ——
// 跟"UI 只是订阅者之一, 没有特权客户端"是同一个设计, 只是方向反过来.
//
// 入口跑在 OS 宿主进程里(跟出网代理 netproxy 一样), 不在被约束的进程里:
// 它要写事件日志、要唤醒别的进程, 那些都是 OS 的权力.

// ── 入口必须有上限 ──
//
// 一个 53M 的请求(手机断网一周之后补 20 万条)把宿主的 RSS
// 从 7MB 顶到 498MB, **而且不降回去** —— 那些信号还全躺在内存日志里.
//
// 而"断网补发是常态"是这套设计**自己写在去重注释里的**预期场景,
// 不是什么恶意流量. 宿主一死, 所有对话和全部状态跟着走 ——
// 这是最坏的一种死法, 而且触发它只需要一部关机一周的手机.
//
// **拒绝必须让采集端能取得进展**: 只回一句"太大了"的话, 采集端会拿
// 同一批一直重试 —— 补发永远补不上, 而那比直接丢还糟(它会一直重试、
// 一直失败、一直没人知道). 所以 413 里带着上限和怎么拆.
const (
	// maxBodyBytes 一次请求最多多大. 2MB 装得下约一万条过滤后的信号,
	// 而真实一天的量是几十条 —— 这个上限只挡异常, 不挡正常补发
	maxBodyBytes = 2 << 20
	// maxBatchSignals 一批最多几条. 字节上限之外还要有条数上限:
	// 一条一字节的一百万条同样能把内存日志顶满
	maxBatchSignals = 1000
)

// tooBig 统一的"太大了"应答 —— 带上限和拆法, 采集端才有得办
func tooBig(w http.ResponseWriter, what string, limit int, hint string) {
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
		"error": what, "limit": limit, "hint": hint,
	})
}

// SenseAPI 采集入口.
type SenseAPI struct {
	bus   *SignalBus
	token string
	srv   *http.Server
	ln    net.Listener
}

// SenseOptions 入口的配置
type SenseOptions struct {
	// Addr 监听地址. **缺省只听回环**.
	//
	// 手机要投进来当然得对外听, 但那必须是**显式**的决定:
	// 一个默认对全网开放的采集口, 等于把"这台机器看得见你的一切"
	// 这件事挂在公网上.
	Addr string
	// Token 采集端凭据. **不给就起不来**, 没有匿名模式.
	//
	// 这个口子收的是位置、来电、家里谁在 —— 它比任何一个 API 都敏感.
	// "本地开发先不加鉴权"这种事在这儿不能开口子, 因为它一定会被带上线.
	Token string
}

func NewSenseAPI(bus *SignalBus, o SenseOptions) (*SenseAPI, error) {
	if strings.TrimSpace(o.Token) == "" {
		return nil, fmt.Errorf("采集入口必须配 token —— 这个口子收的是位置和来电, 没有匿名模式")
	}
	addr := o.Addr
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("采集入口监听不了 %s: %w", addr, err)
	}
	s := &SenseAPI{bus: bus, token: o.Token, ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/signal", s.ingestOne)
	mux.HandleFunc("/signals", s.ingestBatch)
	mux.HandleFunc("/heartbeat", s.heartbeat)
	s.srv = &http.Server{
		Handler: mux,
		// 采集端在移动网络上, 慢连接是常态, 但也不能挂着不放
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s, nil
}

func (s *SenseAPI) Addr() string { return s.ln.Addr().String() }

func (s *SenseAPI) Start() {
	go func() { _ = s.srv.Serve(s.ln) }()
}

func (s *SenseAPI) Close() error { return s.srv.Close() }

// authed 常数时间比对 —— 这个 token 是长期有效的, 值得防时序侧信道
func (s *SenseAPI) authed(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *SenseAPI) health(w http.ResponseWriter, r *http.Request) {
	// health 不鉴权 —— 采集端要靠它判断"是网断了还是 token 错了".
	// 这两件事分不开的话, 手机端只能盲目重试
	writeJSON(w, 200, map[string]any{
		"ok": true, "pending": s.bus.Pending(), "watermark": s.bus.Watermark(),
	})
}

// ingestOne 投一条.
//
// 返回**它去了哪儿**(accepted/urgent/duplicate/late). 采集端必须能知道:
// 静默地把信号归入迟到, 会让手机端以为一切正常, 而实际上它的时钟慢了半小时.
func (s *SenseAPI) ingestOne(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		writeJSON(w, 401, map[string]any{"error": "token 不对"})
		return
	}
	IngestOne(s.bus, w, r)
}

// IngestOne 投一条 —— **鉴权在外面, 这里只管信号**.
//
//	抽出来是因为它有两个入口: neox-chat 起的独立采集口(SenseAPI),
//	和 console 的观察口(见 ObserveOptions.Sense). 两边各写一份的话,
//	上限、迟到判定、错误文案迟早会不一样 —— 而那种不一样是静默的:
//	采集端换个地址投, 行为就变了.
func IngestOne(bus *SignalBus, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "只收 POST"})
		return
	}
	// 挡在解析之前 —— 解完再判就已经把内存吃掉了
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var sig abi.Signal
	if err := json.NewDecoder(r.Body).Decode(&sig); err != nil {
		if isTooLarge(err) {
			tooBig(w, "请求体超过上限", maxBodyBytes, "一条信号不该这么大, 检查 body 里塞了什么")
			return
		}
		writeJSON(w, 400, map[string]any{"error": "解不开的 JSON: " + err.Error()})
		return
	}
	if err := validateSignal(sig); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"result": string(bus.Ingest(sig))})
}

// ingestBatch 一次投一批.
//
// ── 这个端点不是"优化", 是必需 ──
//
// 手机离线两小时之后会一次性补传几百条. 一条一个请求的话:
// 几百次 TLS 握手、几百次唤醒, 而且中间断一次就得从头对账.
//
// **逐条返回处理结果**, 因为一批里各条的下场可能完全不同 ——
// 补传的那批里, 早的几条会判迟到, 晚的几条正常入窗.
func (s *SenseAPI) ingestBatch(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		writeJSON(w, 401, map[string]any{"error": "token 不对"})
		return
	}
	IngestBatch(s.bus, w, r)
}

// IngestBatch 投一批 —— 理由同 IngestOne
func IngestBatch(bus *SignalBus, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "只收 POST"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var sigs []abi.Signal
	if err := json.NewDecoder(r.Body).Decode(&sigs); err != nil {
		if isTooLarge(err) {
			tooBig(w, "请求体超过上限", maxBodyBytes,
				fmt.Sprintf("拆成多批, 每批不超过 %d 条", maxBatchSignals))
			return
		}
		writeJSON(w, 400, map[string]any{"error": "解不开的 JSON: " + err.Error()})
		return
	}
	// **整批拒, 不收一半**: 半批进去之后采集端不知道该从哪儿续,
	// 而它自己看到的是"失败了" —— 于是要么重发造成重复, 要么跳过造成空洞.
	// 两种都比整批拒更难查
	if len(sigs) > maxBatchSignals {
		tooBig(w, "一批信号条数超过上限", maxBatchSignals,
			fmt.Sprintf("拆成多批, 每批不超过 %d 条", maxBatchSignals))
		return
	}
	results := make([]map[string]any, 0, len(sigs))
	counts := map[string]int{}
	for _, sig := range sigs {
		if err := validateSignal(sig); err != nil {
			results = append(results, map[string]any{"id": sig.ID, "error": err.Error()})
			counts["invalid"]++
			continue
		}
		res := string(bus.Ingest(sig))
		results = append(results, map[string]any{"id": sig.ID, "result": res})
		counts[res]++
	}
	writeJSON(w, 200, map[string]any{"n": len(sigs), "counts": counts, "results": results})
}

// validateSignal 缺什么要说清楚 —— 这条错误信息是写采集端的人唯一的线索.
//
// 特意**不校验 Body 的形状**: 采集端自己的结构 OS 不解释.
// 一旦开始校验 body, 加一种传感器就要改 OS, 那条边界就白切了.
func validateSignal(s abi.Signal) error {
	if strings.TrimSpace(s.Source) == "" {
		return fmt.Errorf("缺 source: 谁报的(如 phone.mk / ha.livingroom)")
	}
	if strings.TrimSpace(s.Kind) == "" {
		return fmt.Errorf("缺 kind: 什么事(如 location / call.incoming)")
	}
	// At 可以不给(总线按到达时间补并标注), 但给了就不能是明显错的:
	// 一个 1970 年或者 2099 年的时间戳一定是单位搞错了(秒当成了毫秒)
	if s.At != 0 {
		t := time.UnixMilli(s.At)
		if t.Year() < 2000 || t.Year() > 2100 {
			return fmt.Errorf("at=%d 不像毫秒时间戳(解出来是 %d 年)——秒要乘 1000",
				s.At, t.Year())
		}
	}
	return nil
}

// isTooLarge MaxBytesReader 触顶时的错误. Go 里它是一个私有类型,
// 只能认错误文本 —— 认错了的后果只是返回 400 而不是 413,
// 采集端照样知道这批不行, 不会静默收下
func isTooLarge(err error) bool {
	return err != nil && strings.Contains(err.Error(), "request body too large")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// heartbeat 采集端报到: "我还在, 我的节奏是每 N 秒一次".
//
// **为什么需要它**: 长跑里 HA 的令牌过期, 采集器每次都报错重试,
// 而 OS 一个字都没有 —— 八分钟没有任何信号, 用户看到的是"今天很安静".
// 整个感知层可以静默地死掉, 而这是唯一能发现它的东西.
//
// **为什么不做成一条信号**: 心跳是采集端跟 OS 之间的事, 不是世界的事件.
// 做成信号的话它会进账本、进窗口、进摘要 —— 一天几千条"我还活着",
// 那正是感知层最该避免的东西.
func (s *SenseAPI) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		writeJSON(w, 401, map[string]any{"error": "token 不对"})
		return
	}
	Heartbeat(s.bus, w, r)
}

// Heartbeat 采集端报到 —— 理由同 IngestOne
func Heartbeat(bus *SignalBus, w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if strings.TrimSpace(source) == "" {
		writeJSON(w, 400, map[string]any{"error": "缺 source: 谁在报到"})
		return
	}
	// 节奏由采集端自己报 —— OS 不该知道一个家居桥接多久拉一次
	sec, _ := strconv.Atoi(r.URL.Query().Get("everySec"))
	if sec <= 0 {
		writeJSON(w, 400, map[string]any{
			"error": "缺 everySec: 你多久报一次 —— 不说的话 OS 没办法判断你多久没消息算不正常"})
		return
	}
	// why 非空 = "我还在, 但我拿不到数据". **两件事要分开**:
	// 失联是去看进程还在不在, 瞎了是去看凭据 —— 混成一句话的话,
	// 用户会照着错的方向去查
	if why := strings.TrimSpace(r.URL.Query().Get("why")); why != "" {
		bus.HeartbeatFailing(source, time.Duration(sec)*time.Second, why)
	} else if r.URL.Query().Get("alive") != "" {
		// 只说"我还在" —— 不代表它拿得到数据
		bus.HeartbeatAlive(source, time.Duration(sec)*time.Second)
	} else {
		bus.Heartbeat(source, time.Duration(sec)*time.Second)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
