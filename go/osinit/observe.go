package osinit

// observe — 给**观察者**的口子.
//
//	ABI (unix socket) 是给被约束的进程用的: 它要 infer / recv / decide.
//	观察者需要读取事件和提交决议, 两者权限完全不同,
//	所以走两个口, 不复用一个.
//
//	为什么是 SSE 而不是 WebSocket:
//	 1. 观察是**单向**的. 事件从 OS 流向界面, 反向只有"决议/发言"两个动作,
//	    普通 POST 就够. 为了两个 POST 引入一个 WS 库不值得.
//	 2. 这个仓库除了 golang.org/x/sys **没有第三方依赖**. Go 标准库没有 WS,
//	    SSE 是纯 net/http. 保住零依赖比省几行代码重要.
//	 3. 断线重连是 EventSource 自带的, 而且我们本来就要按 fromSeq 补齐,
//	    重连语义天然吻合.
//
//	**没有特权观察者**: 这个 server 只读日志和提交决议, 不能改进程状态,
//	也不因为有没有人连着而改变 OS 的行为.

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/engine"
)

// ObserveServer 观察端口.
type ObserveServer struct {
	os       *OS
	token    string
	onCreate func(CreateRequest) (abi.ProcessID, error)
	onRevive func(abi.ProcessID) (abi.ProcessID, error)
	provider ProviderHooks
	policy   PolicyHooks
	output   OutputHook
	project  func(string) (any, error)
	revert   func(string, string) (string, error)
	spend    func() any
	forget   func([]abi.ProcessID) error
	move     func(string, string) error
	stop     func(string) error
	rebind   func(string, string) error
	role     func(string, string) error
	// sense 感知层的信号总线. nil = 这台机器不收信号
	sense *SignalBus
	// devices 设备登记处 —— 屋里有哪些东西, 各自能感知什么、能怎么说话
	devices *Devices
	// people 屋里住着谁 —— **归属, 不是鉴权**(见 people.go)
	people *People
	// world 此刻的世界 —— 它现在到底知道些什么
	world *World
	// places 它认得哪些地方 —— 用户教的那些
	places *Places
	// notes 它记住的事 —— 见 notes.go
	notes *Notes
	// agenda 他要做的事和日程 —— 见 agenda.go
	agenda *Agenda
	// timers 设着的提醒 —— 见 timers.go. **界面上一直看不到它们**,
	// 而"我让你提醒我什么了"是他最常问的一句
	timers *Timers
	// watches 盯着的事 —— 见 watch.go. 同上
	watches *Watches
	// zone 这台机器算在哪个时区 —— 见 zone.go
	zone *Zone
	// routine 它看出了什么规律
	routine *Routine
	// rules 条件触发
	rules *Rules
	// mapShot 一张静态地图 —— 手机上那张位置卡片的缩略图.
	//
	//	**由 OS 去取, 不是手机直接取**: AK 落到手机上就是发出去了;
	//	而且服务端 key 通常绑了出口 IP, 手机每换一次基站 IP 就变一个
	mapShot  func(ctx context.Context, lat, lon float64, w, h int) ([]byte, error)
	projects ProjectNameHooks
	rooms    func() map[string]string
	// enforced 这台机器上"写只能写进工作区"到底有没有人强制.
	//
	//	**不是按模式推的**: dev 模式下也可能有沙箱(macOS 的 sandbox-exec),
	//	而 confined 模式靠 landlock. 宿主最清楚, 所以由宿主说了算.
	enforced bool
	// blobs 用户发进来的图存在哪 —— 见 blobs.go
	blobs blobStore
	srv   *http.Server
	ln    net.Listener
}

// ObserveOptions 观察口配置
type ObserveOptions struct {
	// Addr 监听地址. 缺省**只允许回环** —— 这个口流的是进程说的每一句话,
	// 绑到 0.0.0.0 等于把整台机器的对话挂到局域网上.
	Addr string
	/**
	 * AllowRemote 显式放行非回环地址 —— 自托管形态(Docker/云主机)用.
	 *
	 *	原来"只绑回环"是 token 走 URL query 那个让步的安全边界.
	 *	放开它, 边界就换成了这三样, 宿主必须一起兑现:
	 *	  · token 是持久密钥, 不再是"每次启动随机"的一次性值
	 *	  · 传输要么在可信网络里(容器网络/内网/隧道), 要么过 TLS —— 由部署方保证
	 *	  · 这个开关必须是**用户显式设置**的结果(比如设了 NEOX_OBSERVE_ADDR),
	 *	    代码里没有任何路径替他做这个决定
	 */
	AllowRemote bool
	// Token 观察端凭据. 跟采集口一样: **不给就起不来, 没有匿名模式**.
	Token string
	/**
	 * UI 打进二进制的 Web 界面. nil = 只开 API.
	 *
	 *	静态文件**不过 token**: 它们是公开的壳(跟下载一个客户端等价),
	 *	数据全在 API 后面. 这跟 Jupyter 的形态一致 —— 页面谁都打得开,
	 *	不带 token 什么都看不见.
	 */
	UI fs.FS
	// Blobs 用户发进来的图存在哪个目录. 空 = 这台 OS 收不了图.
	//
	//	**存哪儿是宿主的事**(同 OnProvider): OS 只知道"要存成文件",
	//	落在哪个目录、跟不跟别的数据放一起, 是宿主的布局.
	Blobs string
	// OnCreate 新建一个进程.
	//
	//	**OS 不知道什么是"bot"**. 起什么样的进程、给什么能力、跑什么循环,
	//	那是宿主应用的事; 观察口只负责把"用户想新建一个"这件事转过去.
	//	不给这个钩子就没有新建能力, 接口回 501 而不是假装成功.
	OnCreate func(CreateRequest) (abi.ProcessID, error)
	/**
	 * OnRevive 话送到一个已经不在的进程时, 宿主把它拉起来.
	 *
	 *	进程会死, 对话不死 —— 开机时宿主会从名册把人再起一遍.
	 *	但开机之后如果这个进程自己退了(崩了、被杀、整轮跑完),
	 *	/say 原来只回 ok:false, 界面就显示"进程不在了".
	 *	刷新页面没用: 刷新不会再 spawn. 用户只能重启整个客户端.
	 *
	 *	这个钩子让"再说一句"跟开机走同一条路: 认出是谁, 起一个
	 *	新进程, 把话投给新人. 不给钩子就保持原样(如实说没送到).
	 */
	OnRevive func(dead abi.ProcessID) (abi.ProcessID, error)
	// OnProvider 读/写推理供应商配置.
	//
	//	**存哪儿是宿主的事**, 不是 OS 的事: OS 只认一个 Provider 接口,
	//	落盘位置、文件权限、要不要跟系统钥匙串走, 都属于宿主策略.
	OnProvider ProviderHooks
	// OnPolicy 读/写审批口径
	OnPolicy PolicyHooks
	/**
	 * ProjectNames 项目显示名的读写.
	 *
	 *	目录名是磁盘上的事实, 显示名是人起的 —— 分开存, 机器认路径,
	 *	人看名字. 不给钩子就没有这个能力, 接口回 501 而不是假装成功.
	 */
	ProjectNames ProjectNameHooks
	// RoomOwners 谁是群主(房间名 → bot 名). 只读 —— 群主在开房那一刻定
	RoomOwners func() map[string]string
	// OnOutput 一个 bot 干出来了什么 —— 见 OutputHook
	OnOutput OutputHook
	/**
	 * OnProject 一个项目现在什么状态: 谁在干、几条待合、主干上次谁改的.
	 *
	 *	**项目才是干活的单位**, 人是围着它转的. 少了这一页, "这摊活
	 *	现在怎么样"只能一个人一个人点开看.
	 */
	OnProject func(path string) (any, error)
	/**
	 * OnRevert 撤掉主干上的一条改动.
	 *
	 *	"合上去"是这套东西唯一不可逆的动作 —— 它改的是所有人的主干.
	 *	一个动作有多不可逆, 它就越该有一颗退回去的按钮.
	 */
	OnRevert func(project, hash string) (string, error)
	// OnSpend 这台机器上的账: 谁花了多少、干出了什么.
	//
	//	**OS 不定义"贵不贵"**: 它只有 token 数, 单价和汇率是宿主的事.
	OnSpend func() any
	// OnForget 删掉一段对话.
	//
	//	**删哪儿由宿主说了算**: 内存日志、盘上账本、名册、工作目录 ——
	//	OS 只知道前两个, 后两个是宿主的东西. 少删一个就是假删.
	OnForget func(pids []abi.ProcessID) error
	/**
	 * OnStop **刹车**: 停掉这一轮, 人还在.
	 *
	 *	一个会自己动手改文件、自己花钱的东西, 必须有一颗按下去就停的
	 *	按钮 —— 没有的话, 人对它的信任只能靠"希望它别出错"撑着.
	 */
	OnStop func(name string) error
	// OnMove 把一个 bot 换到别的房间(空 thread = 拉出来单聊)
	OnMove func(name, thread string) error
	// OnRebind 把一个 bot 换到另一个工作区.
	//
	//	跟换房间是同一种动作: **能力在进程启动时定死**, 所以"换个地方干活"
	//	只能是换一个进程. 对话不断 —— 历史跟着 bot 标签走.
	OnRebind func(name, work string) error
	// Sense 感知层的信号总线 —— 接上之后 /signal · /signals · /heartbeat
	// 就在**同一个口子上**.
	//
	//	neox-chat 那边是另起一个 SenseAPI 监听另一个端口, 因为终端那套
	//	里观察口和采集口的信任边界不一样. 到了 console 就不成立了:
	//	观察口的 token 已经能 /say /create /forget —— 它不比采集口
	//	宽松. 再开一个端口的代价却是实打实的: 反代要多一份配置、
	//	容器要多映一个口、手机要多存一个地址, 而这三处只要有一处
	//	忘了配, 症状就是"感知层静默地收不到东西".
	//
	//	**一个设备只该记住一个地址**. 这也是设备契约的前提.
	Sense *SignalBus
	// World 此刻的世界. 接上之后 /world 能读 —— **"它现在知道什么"
	// 必须有一个地方看得见**: 一个主动助理不肯说自己看得见什么的话,
	// 用户对它的信任只能靠猜
	World *World
	// MapShot 取一张静态地图. 接上之后 /mapshot 能用 —— 见 mapShot
	MapShot func(ctx context.Context, lat, lon float64, w, h int) ([]byte, error)
	// Places 它认得哪些地方. 接上之后 /places 能读能撤 ——
	// **教错了要能改**: 一个记错的"家"会让所有跟到家有关的判断都错,
	// 而它不会报错
	Places *Places
	// Notes 长期记忆. nil = 这台机器不给外面看/改它记住的事
	Notes *Notes
	// Agenda 待办和日程. nil = 这台机器不给外面看/改
	Agenda *Agenda
	// Timers 设着的提醒. nil = 不给外面看
	Timers *Timers
	// Watches 盯着的事. nil = 不给外面看
	Watches *Watches
	// Zone 时区. nil = 这台机器不给外面改时区
	Zone *Zone
	// Routine 它看出了什么规律. 接上之后 /routine 能读
	Routine *Routine
	// Rules 条件触发. 接上之后 /rules 能读能撤 —— 用户问"我让你盯
	// 什么了"要答得出, 而且要撤得掉
	Rules *Rules
	// People 屋里住着谁. 接上之后 /people 能读能记 —— 一台家用的 OS 上
	// 不止一个人, 而"这条位置是谁的""这条通知该给谁"要有人回答
	People *People
	// Devices 设备登记处. 跟 Sense 一起给 —— 一台设备报到和它投信号
	// 走的是同一个口子、同一把锁
	Devices *Devices
	// OnRole 改一个 bot 的岗位描述(进系统段的那句话).
	//
	//	**跟换工作区是同一种动作**: 系统段在进程起来的时候就定死了,
	//	改它只能是换一个进程. 历史跟着 bot 标签走, 所以对话不断.
	OnRole func(name, role string) error
	// Enforced 写边界有没有人强制. 界面据此说实话, 不写死"边界在内核".
	Enforced bool
}

// AskMode 什么时候问你.
//
//	三档不是"安全等级", 是**问的频率**:
//	  always 每个副作用都问   —— 新起的 bot 才生效(见下)
//	  bounds 只有越界才问     —— 缺省
//	  never  不问, 自动批准   —— 仍然进日志, 由谁批的写着"自动"
//
//	always 只对**新起的 bot** 生效, 因为它是靠"少给能力"实现的,
//	而能力在进程启动时就定死 —— 这是内核语义(landlock 只能收紧
//	不能放宽), 不是我们的选择. 说清楚比假装立刻生效好.
type AskMode string

const (
	AskAlways AskMode = "always"
	AskBounds AskMode = "bounds"
	AskNever  AskMode = "never"
)

type PolicyHooks struct {
	Get func() AskMode
	Set func(AskMode) error
}

// ProviderConfig 推理供应商配置.
//
//	**APIKey 只进不出**: 读的时候永远返回空, 界面上显示的是 HasKey.
//	把 key 回吐给界面等于让它躺在渲染进程的内存和 devtools 里,
//	而界面从来不需要它 —— 它只需要知道"配没配".
type ProviderConfig struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	APIKey  string `json:"apiKey,omitempty"`
	HasKey  bool   `json:"hasKey"`
	/**
	 * KeyFrom 这把 key **是哪来的**: "file" 存了盘 / "env" 只活在这个进程 / "" 没有.
	 *
	 *	HasKey 只说"现在有没有", 而界面据此写着"已经存着一把" —— 环境变量
	 *	那把**从来不落盘**(见 save 里那段: 用户把 key 放进环境变量, 要的正是
	 *	"它只活在这个进程里"). 于是这句话在那种机器上是假的: 关掉再开,
	 *	key 就没了, 而用户以为它存着.
	 */
	KeyFrom string `json:"keyFrom,omitempty"`
	/**
	 * Protocol 走哪套 wire format. **空 = 自己认**.
	 *
	 *	"openai" 那套(/chat/completions)和 "anthropic" 那套(/v1/messages)
	 *	在能力上不是等价的: **DeepSeek 官方的联网搜索只挂在后一条上** ——
	 *	同一把 key 同一个模型, /chat/completions 传 web_search 直接 400,
	 *	/responses 接受但一次都不搜, 只有 /anthropic/v1/messages 是真搜
	 *	(只有 /anthropic/v1/messages 会执行搜索)。
	 *
	 *	所以 api.deepseek.com 缺省认成 anthropic。自建网关认不出来的
	 *	按 openai 走, 用户也能在这儿改死 —— 猜错一个自建网关的协议,
	 *	表现是整台机器不能推理, 那不该只能等我们发版。
	 */
	Protocol string `json:"protocol,omitempty"`
	// Ask 审批口径. 跟 provider 存一块儿是因为它们同属"这台机器的设置"
	Ask string `json:"ask,omitempty"`
	/**
	 * Vision 这个模型认不认图.
	 *
	 *	**只认显式声明, 不按模型名猜**(见 engine/vision.go 文件头):
	 *	猜错的表现不是报错, 是模型一本正经地描述一张它根本没收到的图.
	 *
	 *	但"显式声明"不该只有环境变量一条路 —— 那等于普通用户永远打不开
	 *	这个开关. 界面上给一个勾, 而且**勾了能当场验**(试一把会真的送一张
	 *	图过去): 谁配谁负责, 而配的人这下真能验证了.
	 */
	Vision bool `json:"vision,omitempty"`
	/**
	 * Think 让模型先想再答. **缺省关着 —— 要的是快**.
	 *
	 *	量过: 同一句"1+1=?", 关掉 completion 是 1 个 token, 开着 34–40 ——
	 *	三十几倍, 而那三十几个 token 是要等的时间. 助理绝大多数时候在做的是
	 *	"提醒我 5:30 打卡"这种事, 想不想都是同一个答案.
	 *
	 *	真要它想的时候(排查、算路程、要一份方案), 设置里开一下 ——
	 *	**那是他自己的决定**, 不是我们替他默认.
	 */
	Think bool `json:"think,omitempty"`
	/**
	 * SearchAPI / SearchURL / SearchKey 搜网走哪儿.
	 *
	 *	**供应商自带的搜索能力并不统一**: OpenAI 的 web_search、Gemini 的
	 *	grounding 是各家私货, 而 DeepSeek 没有对应能力 ——
	 *	其 tools[0].type 只认 function, 传 web_search 直接 400。
	 *
	 *	所以搜索是一个外挂的服务: 认识的那几家(brave/tavily/serper/bocha)
	 *	填个名字加 key 就行; **别的填一个 URL** —— 用户换第五家不该
	 *	等我们发版。
	 *
	 *	三个都空 = 这台机器不能搜网, 于是 web_search 这个工具**根本
	 *	不挂出来**, 而 bot 会照实说搜不了。
	 */
	SearchAPI string `json:"searchApi,omitempty"`
	SearchURL string `json:"searchUrl,omitempty"`
	SearchKey string `json:"searchKey,omitempty"`
	// SearchOn 配没配好 —— **只出不进**, 跟 HasKey 一个道理:
	// key 不回显, 但界面要说得出"已经配着一个"
	SearchOn bool `json:"searchOn,omitempty"`
	/**
	 * SearchNative 供应商**自己就会搜网** —— 也是只出不进.
	 *
	 *	DeepSeek 走 Messages 协议时是会的(server 端执行, 用同一把 key)。
	 *	界面据此不该再催人去配一个第三方的 —— 催了等于让人白花一笔钱,
	 *	而这台机器本来就能搜。
	 */
	SearchNative bool `json:"searchNative,omitempty"`
	/**
	 * CapTokens 一轮最多烧多少 token. **0 = 不限**(缺省).
	 *
	 *	为什么是"每轮"而不是"每个 bot 一共": 跑飞表现为**一轮停不下来**,
	 *	而一个干了三天的 bot 累计花得多是正常的. 按累计设上限, 到点之后
	 *	那个 bot 就永久残废了 —— 而那不是用户想要的.
	 *
	 *	缺省不限: 一道自己没设过的上限突然把活拦腰砍断, 比没有上限更糟.
	 */
	CapTokens int64 `json:"capTokens,omitempty"`
	/**
	 * AutoResume 掉线/重启之后, 手上还有没干完的活就**自己接着干**.
	 *
	 *	缺省关着: 自己动起来是件要用户点头的事.
	 */
	AutoResume bool `json:"autoResume,omitempty"`
	/**
	 * AutoHire 拉人不用问.
	 *
	 *	缺省关着: 多一个人 = 花费翻倍, 该由用户点头. 但天天一起干活的
	 *	用户嫌每次都按一下烦(原话: "如果不需要问, 就可以自动拉") ——
	 *	那是他的钱包他做主, 给一档.
	 */
	AutoHire bool `json:"autoHire,omitempty"`
}

// AskModeOf 存着的口径; 没存过就是缺省"只在越界时问"
func (c ProviderConfig) AskModeOf() AskMode {
	switch AskMode(c.Ask) {
	case AskAlways:
		return AskAlways
	case AskNever:
		return AskNever
	}
	return AskBounds
}

// ProjectNameHooks 项目显示名 —— 存哪儿是宿主的事(同 OnProvider).
type ProjectNameHooks struct {
	Get func() map[string]string
	// Set 空名字 = 撤掉别名, 回到目录名
	Set func(path, name string) error
}

type ProviderHooks struct {
	Get func() ProviderConfig
	Set func(ProviderConfig) error
	// Test 拿一份配置**真发一次请求**, 不落盘.
	//
	//	为什么要有它: 填完 key 点保存, 界面只能说"存好了" —— 而这句话
	//	跟"它能用"完全是两件事. 地址少个 /v1、模型名拼错、key 过期,
	//	都要等到你交代完一件事、等了几秒, 才由 bot 回你一句"模型出错了".
	//	那时你已经分不清是配错了还是它自己坏了.
	Test func(ProviderConfig) ProviderProbe
	// Models 问供应商要一份模型名单.
	//
	//	模型名是抄不对的东西(deepseek-v4 不是合法名字, 只有 -pro/-flash),
	//	而拼错要等一次真请求打过去才报错. 供应商自己有名单, 问它一句
	//	比让人照着文档手敲可靠.
	Models func(ProviderConfig) ([]string, error)
}

// ProviderProbe 试一次的结果 —— 成没成、多久、对面是谁.
type ProviderProbe struct {
	OK bool `json:"ok"`
	// Model 对面**自报**的模型名. 跟你填的那个不一定一样(网关会改写),
	// 而"我以为在用 A, 其实是 B"这件事只有这里能看出来
	Model     string `json:"model,omitempty"`
	LatencyMS int64  `json:"latencyMs"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
	/**
	 * Vision 勾了"能看图"的话, 这里是**真送一张图过去之后**的结果.
	 *
	 *	"这个模型认不认图"原来只能靠人自己相信自己填对了 —— 而填错的
	 *	表现不是报错, 是模型一本正经地描述一张它根本没收到的图.
	 *	送一张只有一种颜色的小图过去问它是什么颜色, 答对答错当场就知道.
	 */
	Vision string `json:"vision,omitempty"`
}

// OutputHook 一个 bot 干出来了什么 —— 判据是 git, 不是它自己说的话.
//
//	nil = 这台 OS 答不出这个问题(没有 git 或者没走分支隔离), 界面据此
//	**不显示那一栏**, 而不是显示一个空的.
type OutputHook func(bot string) (any, error)

// CreateRequest 新建一个进程的请求 —— 只有身份, 没有能力.
//
//	能力**不由客户端指定**: 界面能填 caps 的话, 授权模型就绕过去了.
//	宿主按自己的策略决定给什么, 不够用就走审批加.
type CreateRequest struct {
	Name   string `json:"name"`
	Thread string `json:"thread,omitempty"`
	// Work 这个 bot 在哪儿干活. 空 = 由上层给一个默认的.
	//
	//	**派项目就是派工作区**: 一个 bot 要去 ~/AI/notes 建一个记事本工程,
	//	它的可写范围就得是那儿, 而不是一个系统给它分的匿名目录.
	//	这条路径同时是两件事的真相源 —— 内核给它的 write 能力, 和
	//	提示词里"你的工作区是哪儿"那一句. 两者必须是同一个字符串,
	//	不然提示词比现实宽就撞墙, 比现实窄就白白少做事.
	//
	//	**这一层不校验**: 什么算合法的工作区(能不能是家目录、要不要
	//	自动建)是上层的策略, 观察口只负责原样送到.
	Work string `json:"work,omitempty"`
	// Role 他负责什么.
	//
	//	**这句话既是给模型的岗位说明, 也是给用户看的"他是谁"**:
	//	一列名字长得都差不多的 bot 里(小登/小记/小勤/报表), 只有这句话
	//	能让人分清谁管什么.
	Role string `json:"role,omitempty"`
}

func NewObserveServer(o *OS, opts ObserveOptions) (*ObserveServer, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, fmt.Errorf("观察口必须配 token —— 它流的是进程说的每一句话, 没有匿名模式")
	}
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:7717"
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:") && !opts.AllowRemote {
		return nil, fmt.Errorf("观察口缺省只绑回环, 收到 %q —— 要开远程访问, 宿主必须显式放行(见 AllowRemote 的三条边界)", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("观察口监听不了 %s: %w", addr, err)
	}
	s := &ObserveServer{os: o, token: opts.Token, blobs: blobStore{dir: opts.Blobs}, onCreate: opts.OnCreate, onRevive: opts.OnRevive, provider: opts.OnProvider, policy: opts.OnPolicy, output: opts.OnOutput, project: opts.OnProject, revert: opts.OnRevert, spend: opts.OnSpend, forget: opts.OnForget, move: opts.OnMove, stop: opts.OnStop, rebind: opts.OnRebind, role: opts.OnRole, sense: opts.Sense, devices: opts.Devices, people: opts.People, world: opts.World, places: opts.Places, notes: opts.Notes, agenda: opts.Agenda, timers: opts.Timers, watches: opts.Watches, zone: opts.Zone, routine: opts.Routine, rules: opts.Rules, mapShot: opts.MapShot, projects: opts.ProjectNames, rooms: opts.RoomOwners, enforced: opts.Enforced, ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/processes", s.guard(s.handleProcesses))
	mux.HandleFunc("/stream", s.guard(s.handleStream))
	mux.HandleFunc("/decide", s.guard(s.handleDecide))
	mux.HandleFunc("/say", s.guard(s.handleSay))
	mux.HandleFunc("/blob", s.guard(s.handleBlob))
	mux.HandleFunc("/create", s.guard(s.handleCreate))
	mux.HandleFunc("/health", s.guard(s.handleHealth))
	mux.HandleFunc("/provider", s.guard(s.handleProvider))
	mux.HandleFunc("/provider/test", s.guard(s.handleProviderTest))
	mux.HandleFunc("/provider/models", s.guard(s.handleProviderModels))
	mux.HandleFunc("/output", s.guard(s.handleOutput))
	mux.HandleFunc("/project", s.guard(s.handleProject))
	mux.HandleFunc("/revert", s.guard(s.handleRevert))
	mux.HandleFunc("/spend", s.guard(s.handleSpend))
	mux.HandleFunc("/policy", s.guard(s.handlePolicy))
	mux.HandleFunc("/forget", s.guard(s.handleForget))
	mux.HandleFunc("/move", s.guard(s.handleMove))
	mux.HandleFunc("/stop", s.guard(s.handleStop))
	mux.HandleFunc("/rebind", s.guard(s.handleRebind))
	mux.HandleFunc("/role", s.guard(s.handleRole))
	// 采集口. 挂在这儿而不是另起一个端口 —— 见 ObserveOptions.Sense
	if s.sense != nil {
		mux.HandleFunc("/signal", s.guard(s.handleSignal))
		mux.HandleFunc("/signals", s.guard(s.handleSignals))
		mux.HandleFunc("/heartbeat", s.guard(s.handleHeartbeat))
		mux.HandleFunc("/collectors", s.guard(s.handleCollectors))
	}
	if s.devices != nil {
		mux.HandleFunc("/device", s.guard(s.handleDevice))
		mux.HandleFunc("/devices", s.guard(s.handleDevices))
		mux.HandleFunc("/device/forget", s.guard(s.handleDeviceForget))
	}
	if s.people != nil {
		mux.HandleFunc("/person", s.guard(s.handlePerson))
		mux.HandleFunc("/people", s.guard(s.handlePeople))
		mux.HandleFunc("/person/forget", s.guard(s.handlePersonForget))
	}
	if s.world != nil {
		mux.HandleFunc("/world", s.guard(s.handleWorld))
	}
	if s.places != nil {
		mux.HandleFunc("/place", s.guard(s.handlePlaceName))
		mux.HandleFunc("/places", s.guard(s.handlePlaces))
		mux.HandleFunc("/place/forget", s.guard(s.handlePlaceForget))
	}
	if s.zone != nil {
		mux.HandleFunc("/timezone", s.guard(s.handleTimezone))
	}
	// ── 接下来有什么 ──
	//
	//	**一次给全**: 待办、日程、提醒、盯着的事本来是四个地方存的,
	//	而他问的是同一个问题 —— "这东西在管我哪些事". 让界面自己去
	//	拼四次请求, 拼出来的顺序还各不相同.
	mux.HandleFunc("/upcoming", s.guard(s.handleUpcoming))
	if s.timers != nil {
		mux.HandleFunc("/reminders", s.guard(s.handleReminders))
		mux.HandleFunc("/reminders/cancel", s.guard(s.handleReminderCancel))
	}
	if s.watches != nil {
		mux.HandleFunc("/watches", s.guard(s.handleWatches))
		mux.HandleFunc("/watches/remove", s.guard(s.handleWatchRemove))
	}
	if s.agenda != nil {
		mux.HandleFunc("/agenda", s.guard(s.handleAgenda))
		mux.HandleFunc("/agenda/done", s.guard(s.handleAgendaDone))
		mux.HandleFunc("/agenda/drop", s.guard(s.handleAgendaDrop))
	}
	if s.notes != nil {
		mux.HandleFunc("/notes", s.guard(s.handleNotes))
		mux.HandleFunc("/note", s.guard(s.handleNoteSet))
		mux.HandleFunc("/note/forget", s.guard(s.handleNoteForget))
	}
	if s.routine != nil {
		mux.HandleFunc("/routine", s.guard(s.handleRoutine))
	}
	if s.mapShot != nil {
		mux.HandleFunc("/mapshot", s.guard(s.handleMapShot))
	}
	if s.rules != nil {
		mux.HandleFunc("/rule", s.guard(s.handleRuleAdd))
		mux.HandleFunc("/rules", s.guard(s.handleRules))
		mux.HandleFunc("/rules/remove", s.guard(s.handleRuleRemove))
	}
	mux.HandleFunc("/projects", s.guard(s.handleProjects))
	mux.HandleFunc("/project/name", s.guard(s.handleProjectName))
	mux.HandleFunc("/rooms", s.guard(s.handleRooms))
	mux.HandleFunc("/toolchain", s.guard(s.handleToolchain))
	// 打进来的界面挂在根上 —— 具体路径的 API 在 ServeMux 里天然优先.
	// 没打界面就不注册: 根路径 404 比一页空壳诚实.
	if opts.UI != nil {
		mux.Handle("/", http.FileServer(http.FS(opts.UI)))
	}
	// SSE 没有写超时 —— 这条连接本来就要一直开着
	s.srv = &http.Server{Handler: cors(mux)}
	return s, nil
}

func (s *ObserveServer) Addr() string { return s.ln.Addr().String() }

func (s *ObserveServer) Start() { go func() { _ = s.srv.Serve(s.ln) }() }

func (s *ObserveServer) Close() error { return s.srv.Close() }

// guard 鉴权.
//
// **EventSource 发不了自定义头**, 所以流那条只能收 ?token=.
// 这个让步的边界是: 口子只绑回环, 而且 token 是一次性生成不是长期密钥.
// 其余接口照旧走 Authorization 头.
func (s *ObserveServer) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 开发时界面跑在 vite 的 5310 端口, 跟 7717 不同源.
		// 回环已经是边界, 这里不再做来源限制.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// **authorization 必须列进来**: 带自定义头的跨源请求会先发预检,
		// 预检不放行这个头, 浏览器连真请求都不会发 —— 而 fetch 只报一句
		// "Failed to fetch", 看不出是被预检挡的. curl 一切正常, 界面全空.
		w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *ObserveServer) handleProcesses(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.os.List())
}

// parseCursors 解析 ?from=pid:seq,pid:seq
//
// **每个进程一个游标, 不是一个全局游标**.
// 全局游标在多进程下必然错位: 进程 A 的 seq 跟进程 B 的 seq 没有可比性.
func parseCursors(raw string) map[abi.ProcessID]int {
	out := map[abi.ProcessID]int{}
	for _, part := range strings.Split(raw, ",") {
		if part == "" {
			continue
		}
		at := strings.LastIndex(part, ":")
		if at <= 0 {
			continue
		}
		seq, err := strconv.Atoi(part[at+1:])
		if err != nil || seq < 0 {
			continue
		}
		out[abi.ProcessID(part[:at])] = seq
	}
	return out
}

func (s *ObserveServer) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	cursors := parseCursors(r.URL.Query().Get("from"))

	// ── 补齐与实时之间不许有缝 ────────────────────────────────
	//
	// 顺序**必须是"先订阅, 后补齐"**:
	//   先补齐再订阅的话, 补齐读完到订阅生效之间产生的事件谁都收不到,
	//   而订阅方拿着一个看起来连续的流, **它自己不会知道少了一段**.
	//   反过来先订阅会重复, 而重复是可检测、可丢弃的 —— 缺失不是.
	//
	// 所以: 订阅先开着往 buffer 里灌, 然后读历史, 然后把 buffer 里
	// seq 已经被历史覆盖的丢掉, 剩下的补发, 之后转直通.
	var (
		mu      sync.Mutex
		buffer  []abi.Event
		direct  = false
		outbox  = make(chan abi.Event, 1024)
		dropped = false
	)
	unsubscribe := s.os.Log().SubscribeAll(func(ev abi.Event) {
		mu.Lock()
		if !direct {
			buffer = append(buffer, ev)
			mu.Unlock()
			return
		}
		mu.Unlock()
		select {
		case outbox <- ev:
		default:
			// 订阅者跟不上. **说出来, 不要假装没发生** ——
			// 界面据此知道自己该重连补齐, 而不是拿着一段有洞的流继续画.
			mu.Lock()
			dropped = true
			mu.Unlock()
		}
	})
	defer unsubscribe()

	// 按**日志里的** pid 补齐, 不是按进程表.
	//
	// 进程表里只有还活着的; 已经退出的进程, 它说过的话照样要补给
	// 刚连上来的界面 —— 否则一刷新, 历史里所有干完活的 bot 就都不见了.
	seen := map[abi.ProcessID]int{}
	for _, pid := range s.os.Log().PIDs() {
		for _, ev := range s.os.Log().Replay(pid, cursors[pid]) {
			if !writeEvent(w, flusher, ev) {
				return
			}
			seen[ev.PID] = ev.Seq + 1
		}
		if _, ok := seen[pid]; !ok {
			seen[pid] = cursors[pid]
		}
	}

	mu.Lock()
	pending := buffer
	buffer = nil
	direct = true
	mu.Unlock()
	for _, ev := range pending {
		if ev.Seq < seen[ev.PID] {
			continue // 历史里已经发过了
		}
		if !writeEvent(w, flusher, ev) {
			return
		}
		seen[ev.PID] = ev.Seq + 1
	}

	writeComment(w, flusher, "live")

	/**
	 * 心跳 —— **一条安静的流会被中间的每一跳掐掉**.
	 *
	 *	这条流绝大多数时间没有事件: 一个 24 小时助理本来就不该
	 *	一直说话. 而"没有字节在走"的连接, 路上每一层都会当它死了 ——
	 *	Cloudflare 100 秒、家用 NAT 通常 60-300 秒、手机运营商更短.
	 *
	 *	断了不会报错, 只是**从此不再收到任何事件** —— 而那跟
	 *	"今天很安静"长得一模一样. 这正是这个仓库里反复出现的
	 *	那类失败(S42/S61): 静默地死掉.
	 *
	 *	所以每 20 秒发一行注释. 注释不是事件, 订阅方按 SSE 规范
	 *	直接忽略 —— 它唯一的作用就是让这条线上一直有字节在走.
	 *
	 *	20 秒是按最紧的那一跳定的: 见过的最短空闲超时是 60 秒,
	 *	三倍余量.
	 */
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if !writeComment(w, flusher, "beat") {
				return
			}
		case ev := <-outbox:
			mu.Lock()
			lost := dropped
			dropped = false
			mu.Unlock()
			if lost {
				// 显式告诉对面: 这里断过一段, 拿着你的游标重连
				writeNamed(w, flusher, "gap", map[string]any{"reason": "slow-consumer"})
				return
			}
			if !writeEvent(w, flusher, ev) {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, f http.Flusher, ev abi.Event) bool {
	payload, err := json.Marshal(ev)
	if err != nil {
		return true // 一条画不出来不该拖垮整条流
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return false
	}
	f.Flush()
	return true
}

func writeNamed(w http.ResponseWriter, f http.Flusher, name string, body any) {
	payload, _ := json.Marshal(body)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload)
	f.Flush()
}

// writeComment 写一行 SSE 注释. 返回**写成功没有** ——
//
//	跟 writeEvent 一样要报告: 心跳是这条流上唯一定期发生的写,
//	对面断开时它是第一个会失败的. 不看返回值的话, 循环会一直转下去,
//	而它对着的是一个早就没了的连接 —— 一个订阅者留一个僵着的 goroutine.
func writeComment(w http.ResponseWriter, f http.Flusher, text string) bool {
	if _, err := fmt.Fprintf(w, ": %s\n\n", text); err != nil {
		return false
	}
	f.Flush()
	return true
}

type decideBody struct {
	DID    string         `json:"did"`
	Choice string         `json:"choice"`
	By     string         `json:"by"`
	Values map[string]any `json:"values,omitempty"`
}

func (s *ObserveServer) handleDecide(w http.ResponseWriter, r *http.Request) {
	var body decideBody
	if !readJSON(w, r, &body) {
		return
	}
	by := body.By
	if by == "" {
		by = "你"
	}
	ok := s.os.Decisions().Resolve(abi.DecisionID(body.DID), body.Choice, by, body.Values)
	// **已经解决过要说出来**, 不能静默吞掉 —— 否则界面上那张卡片会永远等下去
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}

type sayBody struct {
	PID string `json:"pid"`
	/**
	 * PIDs 一句话发给屋里好几个人 —— **一次调用说清楚**.
	 *
	 *	客户端自己循环调 N 次 /say 时,"这话还发给了谁、谁拆活"
	 *	这件事只有客户端知道 —— 它把那段提示拼进 Text 里. 换个客户端
	 *	(命令行、测试台、别人写的)协议就没了, 而 bot 完全看不出少了什么.
	 *	结果可能让两个人各搭一套 Vite 脚手架.
	 *
	 *	给了 PIDs 就由 OS 来投, 并且按名单顺序给每个人带上那一段.
	 */
	PIDs []string `json:"pids,omitempty"`
	Text string   `json:"text"`
	From string   `json:"from"`
	// Said 用户原话. 群聊投递时 Text 里裹着上下文信封, 这里才是他说的那句
	Said string `json:"said"`
	// Voice 他在用耳朵听这一轮的回话 —— 见 abi.RecvResult.Voice
	Voice bool `json:"voice,omitempty"`
	// Utterance 同一句话投给多个进程时共用的身份
	Utterance string `json:"utterance"`
	// Images 跟这句话一起发过来的图 —— base64. 见 blobs.go
	Images []sayImage `json:"images,omitempty"`
	// Files 跟这句话一起发过来的文件(非图) —— 落盘给 bot 读, 不进 /blob 读出口
	Files []sayImage `json:"files,omitempty"`
}

type sayImage struct {
	Name string `json:"name"`
	// Data base64. **不带 data: 前缀** —— 前缀是浏览器那边的事
	Data string `json:"data"`
}

func osinitDelivery(body sayBody, from string) Delivery {
	return Delivery{Text: body.Text, Said: body.Said, From: from,
		ID: body.Utterance, Voice: body.Voice}
}

func (s *ObserveServer) handleSay(w http.ResponseWriter, r *http.Request) {
	var body sayBody
	if !readJSON(w, r, &body) {
		return
	}
	from := body.From
	if from == "" {
		from = "你"
	}
	in := osinitDelivery(body, from)
	/**
	 * 一句话发给一屋子人 —— **两种叫法, 同一套协议**.
	 *
	 *	pid 空: 由 OS 投给 pids 里每一个(话都一样).
	 *	pid 给了: 只投给他, 但 pids 说明"这话是一屋子人一起收到的" ——
	 *	界面走这条, 因为每个人要捎的上下文不一样(他没看到的那几句).
	 *
	 *	不管哪条, **那段"还发给了谁、谁拆活"都由 OS 加** —— 它是协议,
	 *	不是某个客户端的实现细节.
	 */
	if len(body.PIDs) > 1 {
		if body.PID == "" {
			s.sayToMany(w, body, in)
			return
		}
		in.Text += fanoutNote(s.namesOf(body.PIDs), indexOf(body.PIDs, body.PID))
	}
	/**
	 * 图先落成文件, 再把路径写进这一轮的话里.
	 *
	 *	**存不下就当场说, 不静默丢**: 用户看着自己的图上了屏幕, 而
	 *	bot 那边根本没收到 —— 那种不一致他要过很久才发现.
	 */
	if len(body.Images) > 0 {
		refs, err := s.takeImages(body.Images)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		in.Images = refs
		// **那段话是给模型看的, 不是给人看的**: 账本里记原话(Said),
		// 不记我们贴上去的路径 —— 否则用户会看到自己的一句"这是什么"
		// 后面跟着一大段文件路径和使用说明
		if in.Said == "" {
			in.Said = in.Text
		}
		in.Text += imageNote(refs, s.os.Viewer() != nil)
	}
	// 文件跟图同一套待遇: 落盘、路径进给模型的那段话、账本只记名字
	if len(body.Files) > 0 {
		refs, err := s.takeFiles(body.Files)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		in.Files = refs
		if in.Said == "" {
			in.Said = in.Text
		}
		in.Text += fileNote(refs)
	}
	ok := s.deliver(abi.ProcessID(body.PID), in)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}

// deliver 投一句. 对面已经不在就让宿主拉起来再投一次.
func (s *ObserveServer) deliver(pid abi.ProcessID, in Delivery) bool {
	if s.os.Deliver(pid, in) {
		return true
	}
	if s.onRevive == nil {
		return false
	}
	live, err := s.onRevive(pid)
	if err != nil || live == "" {
		return false
	}
	return s.os.Deliver(live, in)
}

/**
 * sayToMany 一句话投给屋里好几个人.
 *
 *	**每个人拿到的那一份不一样**: 后面跟着"这话还发给了谁、谁拆活".
 *	名单顺序就是拆活的顺序 —— 确定的, 不需要任何来回.
 */
func (s *ObserveServer) sayToMany(w http.ResponseWriter, body sayBody, in Delivery) {
	names := s.namesOf(body.PIDs)
	sent := 0
	for i, pid := range body.PIDs {
		one := in
		one.Text = in.Text + fanoutNote(names, i)
		if s.deliver(abi.ProcessID(pid), one) {
			sent++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": sent > 0, "sent": sent})
}

// namesOf 这几个 pid 各叫什么 —— 名单顺序就是拆活的顺序
func (s *ObserveServer) namesOf(pids []string) []string {
	out := make([]string, 0, len(pids))
	for _, pid := range pids {
		if info, ok := s.os.Info(abi.ProcessID(pid)); ok && info.Spec.Name != "" {
			out = append(out, info.Spec.Name)
			continue
		}
		out = append(out, pid)
	}
	return out
}

func indexOf(pids []string, pid string) int {
	for i, one := range pids {
		if one == pid {
			return i
		}
	}
	return -1
}

// takeFiles 把界面发来的几个文件存下来 —— 存法见 blobStore.saveFile.
func (s *ObserveServer) takeFiles(files []sayImage) ([]blobRef, error) {
	out := make([]blobRef, 0, len(files))
	for _, one := range files {
		raw, err := base64.StdEncoding.DecodeString(one.Data)
		if err != nil {
			return nil, fmt.Errorf("%s 解不开: %w", one.Name, err)
		}
		ref, err := s.blobs.saveFile(one.Name, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// takeImages 把界面发来的几张图存下来.
func (s *ObserveServer) takeImages(images []sayImage) ([]blobRef, error) {
	out := make([]blobRef, 0, len(images))
	for _, one := range images {
		raw, err := base64.StdEncoding.DecodeString(one.Data)
		if err != nil {
			return nil, fmt.Errorf("%s 解不开: %w", one.Name, err)
		}
		ref, err := s.blobs.save(one.Name, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

/**
 * handleStop 停掉这一轮.
 *
 *	**按名字不按 pid**: 界面上人看到的是"这个 bot", 而它可能刚重启过,
 *	pid 早换了 —— 按 pid 停的话, 按钮会在最需要它的时候停错人.
 */
func (s *ObserveServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.stop == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 停不了"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.stop(strings.TrimSpace(body.Name)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProjects 项目显示名的清单 —— path → 名字. 没起过名的不在里面.
func (s *ObserveServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	if s.projects.Get == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不管项目名"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": s.projects.Get()})
}

// handleProjectName 给项目起显示名. 空名字 = 回到目录名.
func (s *ObserveServer) handleProjectName(w http.ResponseWriter, r *http.Request) {
	if s.projects.Set == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不管项目名"})
		return
	}
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.projects.Set(body.Path, body.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRooms 谁是群主 —— 房间名 → bot 名. 只读.
func (s *ObserveServer) handleRooms(w http.ResponseWriter, r *http.Request) {
	if s.rooms == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不记群主"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": s.rooms()})
}

// handleProject 这个项目现在什么状态 —— 见 ObserveOptions.OnProject
func (s *ObserveServer) handleProject(w http.ResponseWriter, r *http.Request) {
	if s.project == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 答不出项目状态"})
		return
	}
	got, err := s.project(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, got)
}

/**
 * handleRevert 撤掉主干上的一条改动.
 *
 *	**这条是写操作里最该说清楚回执的一个**: 撤成了要说撤的是哪一条,
 *	撤不掉要说为什么 —— 用户按这颗按钮的时候, 通常已经有点慌了.
 */
func (s *ObserveServer) handleRevert(w http.ResponseWriter, r *http.Request) {
	if s.revert == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放撤销"})
		return
	}
	var body struct {
		Project string `json:"project"`
		Hash    string `json:"hash"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	said, err := s.revert(body.Project, body.Hash)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": said})
}

// handleHealth 这台 OS 现在到底有什么.
//
//	**推理配没配必须说实话**. 没配的话界面要明确显示"没接推理服务",
//	而不是让 bot 用套话糊过去 —— 用户看到的会是一个假装在思考的东西.
func (s *ObserveServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	provider := s.os.Provider()
	/**
	 * **边界是不是真的有人挡, 界面必须知道**.
	 *
	 *	设置页必须区分运行模式: confined 模式下 bot 跑的命令继承
	 *	同一套 landlock/netns/cgroup, 而 console 走的是 dev(in-proc) ——
	 *	后者可能让 bot 用 run 把文件
	 *	写到了工作区外面, 没有审批也没有拦截.
	 *
	 *	说了做不到的话, 后果不是"少一层保护", 是**用户以为有一层保护
	 *	而其实没有** —— 他会因此放心把一个没验过的 bot 放进真实项目目录.
	 */
	body := map[string]any{"ok": true, "inference": provider != nil,
		"mode": string(s.os.Mode()), "enforced": s.enforced}
	if provider != nil {
		body["model"] = provider.Model()
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *ObserveServer) handleProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if s.provider.Get == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放改推理配置"})
			return
		}
		got := s.provider.Get()
		// **只进不出** —— 两把 key 都是, 见 hideKeys
		hideKeys(&got)
		writeJSON(w, http.StatusOK, got)
		return
	}
	var body ProviderConfig
	if !readJSON(w, r, &body) {
		return
	}
	if s.provider.Set == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放改推理配置"})
		return
	}
	if strings.TrimSpace(body.BaseURL) == "" || strings.TrimSpace(body.Model) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "地址和模型都得填"})
		return
	}
	if err := s.provider.Set(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	got := s.provider.Get()
	hideKeys(&got)
	writeJSON(w, http.StatusOK, got)
}

// hideKeys 出门前把两把 key 摘掉, 换成"配没配"那两位.
//
//	**抄成两份的东西迟早漏一份**: 读和写各写一遍的时候, 新加的
//	SearchNative 只会出现在其中一处, 而那种错的表现是界面时有时无。
func hideKeys(c *ProviderConfig) {
	c.SearchOn = strings.TrimSpace(c.SearchKey) != "" ||
		strings.TrimSpace(c.SearchURL) != ""
	c.SearchNative = engine.WantsAnthropic(c.Protocol, c.BaseURL) &&
		strings.TrimSpace(c.APIKey) != ""
	c.APIKey = ""
	c.SearchKey = ""
}

// handleProviderTest 真发一次请求, **不保存**.
//
//	不保存是判据的一部分: 试一把是"看看这条配置行不行", 而保存是
//	"从现在起全体 bot 都用它". 把两件事合成一个按钮, 人就没法在
//	不影响正在干活的 bot 的前提下试一个新模型.
func (s *ObserveServer) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	if s.provider.Test == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放试推理配置"})
		return
	}
	var body ProviderConfig
	if r.ContentLength > 0 && !readJSON(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, s.provider.Test(body))
}

// handleProviderModels 拉一份模型名单. 跟试一把一样: **不保存**.
func (s *ObserveServer) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	if s.provider.Models == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放列模型"})
		return
	}
	var body ProviderConfig
	if r.ContentLength > 0 && !readJSON(w, r, &body) {
		return
	}
	models, err := s.provider.Models(body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// handleOutput 这个 bot 的产物: 它自己那条分支上的提交, 加上还没提交的改动.
func (s *ObserveServer) handleOutput(w http.ResponseWriter, r *http.Request) {
	if s.output == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 答不出产物"})
		return
	}
	bot := strings.TrimSpace(r.URL.Query().Get("bot"))
	if bot == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "要说清是谁"})
		return
	}
	got, err := s.output(bot)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// handleSpend 这台机器上的账.
func (s *ObserveServer) handleSpend(w http.ResponseWriter, r *http.Request) {
	if s.spend == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不记账"})
		return
	}
	writeJSON(w, http.StatusOK, s.spend())
}

func (s *ObserveServer) handlePolicy(w http.ResponseWriter, r *http.Request) {
	if s.policy.Get == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放改审批口径"})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"mode": s.policy.Get()})
		return
	}
	var body struct {
		Mode AskMode `json:"mode"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	switch body.Mode {
	case AskAlways, AskBounds, AskNever:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "不认识这个口径"})
		return
	}
	if err := s.policy.Set(body.Mode); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": s.policy.Get()})
}

func (s *ObserveServer) handleMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Thread string `json:"thread"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.move == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放换房间"})
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没说换谁"})
		return
	}
	if err := s.move(body.Name, strings.TrimSpace(body.Thread)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRole 改一个 bot 的岗位描述.
//
//	**允许改成空**: 空岗位是一个合法的状态(通用助手), 而"清空"必须和
//	"没填"分得开 —— 所以这里不像 name 那样把空当成参数缺失.
func (s *ObserveServer) handleRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.role == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 没开放改岗位"})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "得说清楚改谁"})
		return
	}
	if err := s.role(name, strings.TrimSpace(body.Role)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRebind 把一个 bot 派到另一个工作区.
func (s *ObserveServer) handleRebind(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Work string `json:"work"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.rebind == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 没开放换工作区"})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "得说清楚换谁"})
		return
	}
	// **失败要说原因**: 工作区是有边界的(家目录、根、账本所在一律拒),
	// 只回一个 false 的话用户对着一个没反应的按钮点第二次
	if err := s.rebind(name, strings.TrimSpace(body.Work)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *ObserveServer) handleForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PIDs []abi.ProcessID `json:"pids"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.forget == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 不开放删除"})
		return
	}
	if len(body.PIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没说删哪个"})
		return
	}
	if err := s.forget(body.PIDs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *ObserveServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body CreateRequest
	if !readJSON(w, r, &body) {
		return
	}
	if s.onCreate == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "这台 OS 没开放新建"})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len([]rune(name)) > 24 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "名字得有, 而且不超过 24 个字"})
		return
	}
	pid, err := s.onCreate(CreateRequest{
		Name: name, Thread: strings.TrimSpace(body.Thread),
		Work: strings.TrimSpace(body.Work), Role: strings.TrimSpace(body.Role)})
	if err != nil {
		// 起不来要说原因, 不能只回一个 false —— 用户对着一个没反应的按钮
		// 点第二次, 只会多起一个失败的进程
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pid": pid})
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(into); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}

// ── 采集口 ── 见 ObserveOptions.Sense.
//
//	正文全在 senseapi.go 里, 这三个只是**把观察口的鉴权接上去** ——
//	两处各写一份的话, 上限和迟到判定迟早会不一样, 而采集端只会看到
//	"换个地址投, 行为就变了".

func (s *ObserveServer) handleSignal(w http.ResponseWriter, r *http.Request) {
	IngestOne(s.sense, w, r)
}

func (s *ObserveServer) handleSignals(w http.ResponseWriter, r *http.Request) {
	IngestBatch(s.sense, w, r)
}

func (s *ObserveServer) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	// 心跳顺手记一下"最后一次见到这台设备".
	//
	//	**不落账**: 一天几千条"我还在"进账本, 正是感知层最该避免的东西.
	//	但内存里那个时间要有 —— 界面上"这台设备上次报到是什么时候"
	//	是判断它死没死的唯一依据, 而心跳本来就带着这个事实
	if s.devices != nil {
		if src := strings.TrimSpace(r.URL.Query().Get("source")); src != "" {
			s.devices.Seen(src, time.Now().UnixMilli())
		}
	}
	// ── 时区跟着心跳上来 ──
	//
	//	**让设备自己说, 而不是让人配**: Docker 里默认 UTC, 而用户手里
	//	只有一个手机 —— 让他去找一个设置项填 "Asia/Shanghai", 是把
	//	一件机器自己知道的事推给他.
	//
	//	挂在心跳上而不是信号 body 里: 时区是设备的**属性**, 不是一条
	//	观测. 塞进 body 的话它会出现在给模型看的摘要里("最新: tz=…"),
	//	纯噪音. 心跳本来也不落账.
	//
	//	明确设过就顶不掉(见 Zone.Adopt)
	if s.zone != nil {
		if tz := strings.TrimSpace(r.URL.Query().Get("tz")); tz != "" {
			s.zone.Adopt(tz)
		}
	}
	Heartbeat(s.sense, w, r)
}

// handleDevice 一台设备报到 —— 我是谁, 我能感知什么, 我能怎么告诉你.
//
//	见 osinit/devices.go 开头那段: 把"怎么告诉用户"从写死的安卓通知
//	翻成设备自己声明的能力, 换个硬件就不用改 OS.
func (s *ObserveServer) handleDevice(w http.ResponseWriter, r *http.Request) {
	var dev Device
	if !readJSON(w, r, &dev) {
		return
	}
	got, err := s.devices.Declare(dev)
	if err != nil {
		// **说清楚缺什么**: 这条错误信息是写采集端的人唯一的线索,
		// 而写它的人多半正拿着一块 ESP32 和一份 curl
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": got})
}

// handleDevices 屋里有哪些设备. 界面照这个画"已经接进来的东西"
func (s *ObserveServer) handleDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"devices": s.devices.List()})
}

// handleWorld 它现在到底知道些什么.
//
//	**这一页必须有**: 一个会主动开口的助理, 如果不肯说自己看得见什么,
//	用户对它的信任只能靠猜 —— 而猜出来的信任一次误报就没了.
//
//	返回的是**已经过期筛选之后**的那一份: 世界模型不回答自己不确定
//	的事(见 World.Snapshot).
func (s *ObserveServer) handleWorld(w http.ResponseWriter, r *http.Request) {
	// ?who= 谁在问. 不给就只看屋子的那些 —— **不是"看全部"**:
	// 把两个人的位置一起端出来, 问的人分不清哪条是自己的
	who := strings.TrimSpace(r.URL.Query().Get("who"))
	writeJSON(w, http.StatusOK, map[string]any{
		"facts": s.world.Snapshot(who),
		"text":  s.world.Text(who),
	})
}

// handleRules 我让你盯什么了.
func (s *ObserveServer) handleRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.rules.List()})
}

// handleRuleAdd 立一条规则.
//
//	bot 有 when 这个工具, 但**人也得能直接立一条**: "下雨提醒我带伞"
//	这种规矩, 让用户每次都去跟 bot 说一遍是没道理的 —— 而且他要能
//	看着现有的几条去改.
func (s *ObserveServer) handleRuleAdd(w http.ResponseWriter, r *http.Request) {
	var rule Rule
	if !readJSON(w, r, &rule) {
		return
	}
	got, err := s.rules.Add(rule)
	if err != nil {
		// **报错要能照着改**: 立规则的人手上只有这句话
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": got})
}

// handleRuleRemove 撤掉一条.
//
//	**能立就要能撤**: 一条撤不掉的规则, 用户唯一的处置办法是把整个
//	通道关掉 —— 而那会把真正要紧的那次也一起关掉.
func (s *ObserveServer) handleRuleRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.rules.Remove(strings.TrimSpace(body.ID)) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "没有这条规则 —— 先读 /rules 照着 id 念, 别猜"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePerson 记住一个人 —— 手机登录之后把"我是谁"告诉这台 OS.
//
//	**OS 不验证这个 id**(见 people.go 那段"这不是鉴权"): 设备已经
//	拿着 token 了, 再验一道也拦不住什么. 它解决的是归属, 不是权限.
func (s *ObserveServer) handlePerson(w http.ResponseWriter, r *http.Request) {
	var x Person
	if !readJSON(w, r, &x) {
		return
	}
	got, err := s.people.Know(x)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "person": got})
}

// handlePeople 屋里住着谁
func (s *ObserveServer) handlePeople(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"people": s.people.List()})
}

// handleDeviceForget 删掉一台设备 —— 换手机、卖掉一块表.
//
//	删不掉的话列表里会永远躺着一台三年前的手机, 而 OS 还在等它报到.
func (s *ObserveServer) handleDeviceForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.devices.Forget(strings.TrimSpace(body.ID)) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "没有这台设备 —— 先读 /devices 照着 id 念"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePersonForget 忘掉一个人.
//
//	**他的记录不会跟着没**: 那些在账本里, 而账本只增不删. 忘掉的只是
//	"屋里住着谁"这份名单.
func (s *ObserveServer) handlePersonForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.people.Forget(strings.TrimSpace(body.ID)) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "没有这个人 —— 先读 /people 照着 id 念"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMapShot 一张静态地图 —— 手机上那张位置卡片的缩略图.
//
//	**走 ?token= 而不是 Authorization 头**: 图片是 <img src> 加载的,
//	那条路加不了自定义头 —— 跟 /stream 让步的理由一模一样(见 guard).
func (s *ObserveServer) handleMapShot(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(q.Get("lon"), 64)
	if err1 != nil || err2 != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "lat/lon 得是数字"})
		return
	}
	// 尺寸交给取图那一侧去夹 —— 上限是百度定的, 而它跟 scale 有关,
	// 在这儿抄一份的话两处迟早会不一样
	width, _ := strconv.Atoi(q.Get("w"))
	height, _ := strconv.Atoi(q.Get("h"))

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	raw, err := s.mapShot(ctx, lat, lon, width, height)
	if err != nil {
		// **不要回一张占位图**: 手机上那张卡会安静地显示一块灰色,
		// 而真正的原因(key 没配、配额用光)一个字都看不到
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	// 一个点的地图不会变 —— 让手机自己缓存, 别每次滚动列表都回来取一遍
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(raw)
}

// handlePlaces 它认得哪些地方 —— 用户教的那些.
func (s *ObserveServer) handlePlaces(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"places": s.places.Known()})
}

// handlePlaceForget 忘掉一个地方.
//
//	**教错了要能改**: 一个记错的"家"会让所有跟到家有关的判断都错 ——
//	到家提醒在他还在路上时响、"我在哪"答错、通勤时间从错的起点算.
//	而这些**一个都不会报错**.
func (s *ObserveServer) handlePlaceForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.places.Forget(strings.TrimSpace(body.Name)) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "没有叫这个名字的地方 —— 先读 /places"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTimezone 这台机器算在哪个时区.
//
//	GET  现在算哪个, 是设定的还是手机报的, 加上给人选的那几个
//	POST 换一个
//
// ── 为什么这一条必须有界面 ──
//
//	Docker 里默认是 UTC, 而时区进的是**判断**不是显示: 日报几点发、
//	规则的"晚上七点到十点"算哪一段、闹钟"明天早上八点"是哪一刻.
//	全都差 8 小时, **而一处都不会报错**.
//
//	手机连上来会自己带 tz, 所以绝大多数时候没人需要动这一页.
//	它是给那些手机没连上来、或者要按另一个地方的时间过日子的场合留的
func (s *ObserveServer) handleTimezone(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"zone":  s.zone.Name(),
			"fixed": s.zone.Fixed(),
			"now":   time.Now().Format("2006-01-02 15:04"),
			"pick":  CommonZones(),
		})
		return
	}
	var body struct {
		Zone string `json:"zone"`
		// Auto 回到"跟着手机走". **定死了就要能松开** ——
		// 没有它的话用户点过一次就永远回不到自动, 而界面上
		// 看不出新报上来的时区是被什么挡着的
		Auto bool `json:"auto"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Auto {
		s.zone.Follow()
		writeJSON(w, http.StatusOK, map[string]any{
			"zone": s.zone.Name(), "fixed": false,
			"now": time.Now().Format("2006-01-02 15:04")})
		return
	}
	if err := s.zone.Set(body.Zone); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"zone": s.zone.Name(), "now": time.Now().Format("2006-01-02 15:04")})
}

// handleUpcoming 它在管他哪些事 —— **一次给全**.
//
// ── 为什么要有这一条 ──
//
//	待办、日程、提醒、盯着的事分在四个地方存, 那是对的(它们的语义
//	和生命周期都不一样). 但他问的是同一个问题: **"这东西到底在管我
//	什么"**.
//
//	让界面拼四次请求的话, 每个客户端都要自己排一遍序, 而排出来的
//	顺序各不相同 —— 手机上"接下来"的第一条和桌面上不是同一件事,
//	那种不一致查起来最费劲: 两边都"对".
//
//	用户的原话: "我一眼能看到我现在有哪些东西, 但在界面上我根本
//	找不到这些东西"。
func (s *ObserveServer) handleUpcoming(w http.ResponseWriter, r *http.Request) {
	who := strings.TrimSpace(r.URL.Query().Get("who"))
	out := map[string]any{}
	if s.agenda != nil {
		out["tasks"] = s.agenda.List(who, false)
	}
	if s.timers != nil {
		// 只给还没响的 —— 响过的那些是流水, 不是"它在管我什么"
		out["reminders"] = s.timers.Pending()
	}
	if s.watches != nil {
		out["watches"] = s.watches.List()
	}
	if s.rules != nil {
		out["rules"] = s.rules.List()
	}
	if s.places != nil {
		out["places"] = s.places.Known()
	}
	if s.notes != nil {
		out["notes"] = s.notes.List(who)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReminders 设着的提醒.
//
//	**界面上一直看不到它们**, 而"我让你提醒我什么了"是他最常问的一句.
//	如果 bot 只能去读一个文本文件, 就会说"撤不掉,
//	系统没给我可撤的编号"
func (s *ObserveServer) handleReminders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"reminders": s.timers.Pending()})
}

// handleReminderCancel 撤一条. **能设就得能撤**
func (s *ObserveServer) handleReminderCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.timers.Cancel(strings.TrimSpace(body.ID)) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "没有这条提醒 —— 先读 /reminders"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *ObserveServer) handleWatches(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"watches": s.watches.List()})
}

func (s *ObserveServer) handleWatchRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.watches.Remove(strings.TrimSpace(body.ID)) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "没有这条 —— 先读 /watches"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAgenda 他要做的事和日程.
//
//	GET  ?who=&done=1 列出来. done=1 连划掉的一起给
//	POST 记一件
func (s *ObserveServer) handleAgenda(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		who := strings.TrimSpace(r.URL.Query().Get("who"))
		withDone := r.URL.Query().Get("done") != ""
		writeJSON(w, http.StatusOK, map[string]any{
			"tasks": s.agenda.List(who, withDone)})
		return
	}
	var body struct {
		Who   string `json:"who"`
		What  string `json:"what"`
		At    int64  `json:"at"`
		Where string `json:"where"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	task, err := s.agenda.Add(strings.TrimSpace(body.Who), body.What, body.At, body.Where)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

// handleAgendaDone 划掉一件. **不删** —— 划掉的他还要能看见
func (s *ObserveServer) handleAgendaDone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if _, ok := s.agenda.Done(body.ID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这一条"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAgendaDrop 扔掉一件 —— 记错了、不做了
func (s *ObserveServer) handleAgendaDrop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.agenda.Drop(body.ID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "没有这一条"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNotes 它记住的事.
//
//	**这一页存在的理由跟 /places 一样**: 它记着的东西一旦看不见,
//	用户对它的信任只能靠猜 —— 而猜出来的信任, 一次答错就没了.
func (s *ObserveServer) handleNotes(w http.ResponseWriter, r *http.Request) {
	who := strings.TrimSpace(r.URL.Query().Get("who"))
	var list []Note
	if who == "" {
		list = s.notes.All()
	} else {
		list = s.notes.List(who)
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": list})
}

// handleNoteSet 记一条 / 改一条. 同键覆盖
func (s *ObserveServer) handleNoteSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Who  string `json:"who"`
		Key  string `json:"key"`
		Text string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	note, err := s.notes.Set(strings.TrimSpace(body.Who), body.Key, body.Text)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"note": note})
}

// handleNoteForget 忘掉一条.
//
//	**能记就得能忘**: 一条记错的偏好会一直影响它的回话, 而用户唯一的
//	处置办法本来是把整件事再说一遍 —— 而那只会多一条
func (s *ObserveServer) handleNoteForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Who string `json:"who"`
		Key string `json:"key"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.notes.Forget(strings.TrimSpace(body.Who), body.Key) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "没有这条 —— 先读 /notes"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRoutine 它看出了什么规律.
//
//	?who= 谁的. 不给就是屋子的那份(没有主人的设备报的)
func (s *ObserveServer) handleRoutine(w http.ResponseWriter, r *http.Request) {
	who := strings.TrimSpace(r.URL.Query().Get("who"))
	habits := s.routine.Habits(who)
	out := make([]map[string]any, 0, len(habits))
	for _, h := range habits {
		row := map[string]any{
			"lat": h.Lat, "lon": h.Lon, "days": h.Days,
			"minutes": int(h.Total.Minutes()), "guess": h.Guess, "why": h.Why,
		}
		// 已经起过名的就报名字 —— "你常去的那个地方"和"公司"是同一处,
		// 报后者才有用
		if s.places != nil {
			if name := s.places.Lookup(h.Lat, h.Lon); name != "" {
				row["name"] = name
			}
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"habits": out})
}

// handlePlaceName 给一个**坐标**起名.
//
// ── 为什么这一条非有不可 ──
//
//	规律层看出来"这儿像你上班的地方"之后, 用户得能**当场确认**.
//	没有这条接口的话, 他看到了那个推断却没法处置 —— 只能回聊天里
//	跟 bot 说一句, 而那时候他人多半不在那儿(name_place 要人在场),
//	也未必说得出门牌号(remember_place 要一句地址).
//
//	**一个看得见却按不下去的推断, 比不推断更让人烦.**
func (s *ObserveServer) handlePlaceName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string  `json:"name"`
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		// Radius 方圆多少米算在这儿. 0 = 按缺省.
		//
		//	**这个字段不可缺少, 否则界面上改不了圈**: 圈太小,
		//	他人在家而系统说不认识, 而且一次都不报错
		Radius float64 `json:"radius"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺 name"})
		return
	}
	if body.Lat == 0 && body.Lon == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "缺坐标 —— 要给当前位置起名的话, 让 bot 调 name_place"})
		return
	}
	pl, err := s.places.Add(name, body.Lat, body.Lon, body.Radius)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "place": pl})
}

// handleCollectors 哪几路采集还在报到.
//
// ── 为什么要单独看得见 ──
//
//	"这台设备上次报到是什么时候"(见 /devices)跟"总线认不认它还活着"
//	**是两件事** —— 前者是界面记的, 后者是位置事实要不要过期的依据
//	(见 World.alive). 两者不一致的时候, 症状是"设备看着好好的,
//	而它说不知道你在哪", 而没有任何一处说得出为什么.
func (s *ObserveServer) handleCollectors(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Source  string `json:"source"`
		Alive   bool   `json:"alive"`
		PaceSec int    `json:"paceSec"`
		QuietMs int64  `json:"quietMs"`
		Blind   bool   `json:"blind,omitempty"`
		Why     string `json:"why,omitempty"`
	}
	seen := map[string]*row{}
	for _, st := range s.sense.StaleCollectors() {
		seen[st.Source] = &row{Source: st.Source, PaceSec: int(st.Pace.Seconds()),
			QuietMs: st.Silent.Milliseconds(), Blind: st.Blind, Why: st.Why}
	}
	// 活着的那些不在 StaleCollectors 里 —— 问一遍总线
	for _, dev := range devSources(s) {
		if _, ok := seen[dev]; ok {
			continue
		}
		seen[dev] = &row{Source: dev, Alive: s.sense.Alive(dev),
			PaceSec: int(s.sense.PaceOf(dev).Seconds()),
			QuietMs: s.sense.QuietOf(dev).Milliseconds()}
	}
	out := make([]row, 0, len(seen))
	for _, r := range seen {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	writeJSON(w, http.StatusOK, map[string]any{"collectors": out})
}

// devSources 屋里已知的那几个来源 —— 设备表加接入器
func devSources(s *ObserveServer) []string {
	var out []string
	if s.devices != nil {
		for _, d := range s.devices.List() {
			out = append(out, d.ID)
		}
	}
	return out
}
