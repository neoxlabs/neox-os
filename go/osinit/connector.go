package osinit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 接入器 —— **跟采集端方向相反的那一半**.
//
// ── 两种数据源, 两种形状 ──
//
//	采集端(collector)  设备**往里推**: 手机报位置、家居桥报开门.
//	                   它们在 OS 外面, 可以有很多, 换一个不用改 OS.
//	接入器(connector)  OS **主动往外拉**: 天气、日历、待办、路况.
//	                   它们在 OS 里面 —— 因为拉这件事需要凭据和节奏,
//	                   而凭据不该散在一堆设备里.
//
//	这个区别不是分类癖: 一个装在手机里的东西**够不着**天气 API 的密钥,
//	而一份逐小时的天气预报也不该由五台设备各拉一遍.
//
// ── 拉回来的东西照样变成 Signal ──
//
//	不给接入器开第二条路. 它拉到的东西走**同一个总线、同一份账本、
//	同一套去重和有效期** —— 于是世界模型不需要知道"这条是推来的还是
//	拉来的", 而校准报告也照样数得到它.
//
// ── 拉不到要说话 ──
//
//	接入器跟采集端共用心跳那套: 拉失败就报 failing. 不报的话,
//	一个过期的 API 密钥会让天气这一路静默地死掉, 而用户看到的是
//	"它从来不提醒我带伞" —— 而不是"天气拉不到了".

// Connector 一个往外拉的数据源.
type Connector interface {
	// Name 它叫什么 —— 同时是信号的 source 和心跳的 source
	Name() string
	// Every 多久拉一次
	Every() time.Duration
	// Fetch 拉一次. 返回要投的信号; 拉不到就返回错误(会变成一次
	// "我还在但拿不到数据"的心跳)
	Fetch(ctx context.Context) ([]abi.Signal, error)
}

// Connectors 一台机器上所有接入器.
type Connectors struct {
	bus   *SignalBus
	mu    sync.Mutex
	items []Connector
}

func NewConnectors(bus *SignalBus) *Connectors {
	return &Connectors{bus: bus}
}

// Add 挂一个. nil 直接忽略 —— 没配凭据的接入器就是不存在
func (c *Connectors) Add(x Connector) {
	if x == nil {
		return
	}
	c.mu.Lock()
	c.items = append(c.items, x)
	c.mu.Unlock()
}

// Names 挂了哪几个 —— 界面上"这台机器能看见什么"照这个画
func (c *Connectors) Names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.items))
	for _, x := range c.items {
		out = append(out, x.Name())
	}
	return out
}

// Start 各拉各的, 返回一个停.
//
//	**每个接入器一条 goroutine**: 一个拉得慢(或者卡在一个没有超时的
//	HTTP 上)不该拖住别的. 天气拉不动的时候日历照样该更新.
func (c *Connectors) Start() func() {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	items := append([]Connector(nil), c.items...)
	c.mu.Unlock()

	var wg sync.WaitGroup
	for _, x := range items {
		wg.Add(1)
		go func(x Connector) {
			defer wg.Done()
			c.run(ctx, x)
		}(x)
	}
	return func() {
		cancel()
		wg.Wait()
	}
}

func (c *Connectors) run(ctx context.Context, x Connector) {
	every := x.Every()
	if every <= 0 {
		every = 30 * time.Minute
	}
	// **开机先拉一次**, 不要等第一个周期: 一个每小时拉一次的天气,
	// 重启之后会有整整一小时说"不知道" —— 而那一小时里用户会问
	c.once(ctx, x, every)

	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.once(ctx, x, every)
		}
	}
}

func (c *Connectors) once(ctx context.Context, x Connector, every time.Duration) {
	// **每次拉都要有超时**: 没有的话一个不回应的对端会让这条接入器
	// 永远停在那儿, 而心跳也跟着停 —— 于是它连"我拿不到数据"都说不出来
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	sigs, err := x.Fetch(cctx)
	if err != nil {
		// **"还没准备好"跟"坏了"要分开** —— 见 abi.ErrNotReady.
		//
		//	天气要先知道你在哪儿, 而位置可能等下一次定位就有了.
		//	把它报成坏了的话, 一台刚装好的机器会立刻推一条
		//	"采集端出问题了", 而它其实一切正常 —— 用户学到的那一课
		//	是"这些警报不用看", 而那一课学会了就再也改不回来.
		if errors.Is(err, abi.ErrNotReady) {
			c.bus.HeartbeatAlive(x.Name(), every)
			return
		}
		// 报 failing 而不是干脆不报: **"它还在但拿不到数据"和"它没了"
		// 是两件要去查不同地方的事** —— 前者去看凭据, 后者去看进程
		c.bus.HeartbeatFailing(x.Name(), every, err.Error())
		return
	}
	for _, s := range sigs {
		if strings.TrimSpace(s.Source) == "" {
			s.Source = x.Name()
		}
		if s.At == 0 {
			s.At = time.Now().UnixMilli()
		}
		c.bus.Ingest(s)
	}
	c.bus.Heartbeat(x.Name(), every)
}
