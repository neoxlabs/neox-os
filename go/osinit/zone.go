package osinit

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 时区 —— **这台机器算在哪儿**.
//
// ── 为什么它是个正经问题, 不是显示格式 ──
//
// Docker 镜像里没有时区, 默认 UTC. 而北京时间比它早 8 小时, 于是:
//
//	日报本该 21:00 发       → 凌晨 5 点发
//	规则"晚上七点到十点"    → 判成凌晨三点到六点
//	"明天早上八点叫我"      → 差 8 小时
//	停留算在哪一天          → 跨过午夜的那些全归错了日子
//
// **一处都不会报错.** 日报照发、规则照判、闹钟照响 —— 只是全都在
// 错的时刻. 这是这套系统最怕的那种坏法.
//
// ── 为什么不用 TZ 环境变量了事 ──
//
// 用它当兜底可以, 当唯一的入口不行: 那样这件事只有开容器的人改得动,
// 而用户手里只有一个手机. 落账之后它跟地点、规则一样是这台 OS 的
// 一份设定 —— 界面上改得动, 而且**手机报到时能自己顶上来**.
//
// ── 优先级 ──
//
//	① 明确设过的(用户在界面上、或者他让 bot 设的) —— 最高.
//	   设过之后采集端报的**不再顶掉它**: 他去美国出差两周, 不该
//	   让家里的日报跟着漂过去; 真要跟着走, 他自己再说一句
//	② 采集端报来的(手机每次报到带一个 tz) —— 没明确设过就采纳
//	③ TZ 环境变量 —— 开容器的人的意思
//	④ UTC, 并且**在 stderr 上说出来**: 一台以为自己在 UTC 的机器,
//	   它的每一条时间判断都是错的, 而这件事一处都不会报错
//
// ── 为什么直接改 time.Local ──
//
// 时间进判断的地方有二十多处(日报、规则、停留、闹钟、摘要时间戳),
// 全都隐式读 time.Local. 把一个 *time.Location 穿到每一处去, 代价不是
// 麻烦, 是**漏一处就是静默错误** —— 而这正是要修的那种病.
//
// 所以只改一处: time.Local. 它是个指针赋值, 在启动时改是 Go 自己文档
// 认可的做法; 运行中改(界面上换一个)是同一个指针赋值, 由 Zone 的锁
// 串起来, 读的那一侧拿到的要么是旧的要么是新的, 不会拿到半个.
type Zone struct {
	mu    sync.Mutex
	log   *EventLog
	name  string
	fixed bool
	// said 兜底成 UTC 这件事只说一次 —— 每条信号都喊一遍就成了噪音
	said bool
}

func NewZone(log *EventLog) *Zone { return &Zone{log: log, name: "UTC"} }

// apply 真正生效的那一下. 调用方持锁.
//
// ── 为什么两处都要改 ──
//
//	time.Local 管的是 OS 自己的判断(日报几点发、规则的时段).
//	TZ 环境变量管的是**子进程** —— 如果不一起设置, bot 用 `date`
//	看到的是 UTC, 提醒卡片上却是北京时间. 它会在同一句话里说出
//	两个钟点, 然后自行解释这 8 小时差, 而不是使用一致的本地时间.
func (z *Zone) apply(loc *time.Location) {
	z.name = loc.String()
	time.Local = loc
	_ = os.Setenv("TZ", loc.String())
}

// Name 现在算哪个时区
func (z *Zone) Name() string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.name
}

// Fixed 是不是被明确设过(而不是从采集端采纳来的)
func (z *Zone) Fixed() bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.fixed
}

// Set 明确设定. 从此采集端报的不再顶掉它.
func (z *Zone) Set(name string) error {
	loc, err := loadZone(name)
	if err != nil {
		return err
	}
	z.mu.Lock()
	z.fixed = true
	z.apply(loc)
	z.mu.Unlock()
	if z.log != nil {
		z.log.Append(signalPID, abi.EvZoneSet, map[string]any{
			"zone": loc.String(), "fixed": true})
	}
	return nil
}

// Follow 回到"跟着采集端走".
//
//	**定死了就要能松开**: 没有这一条的话, 用户在界面上点一次之后
//	永远回不到自动 —— 他搬了城市, 手机报的新时区被自己两个月前
//	那一下挡着, 而界面上看不出是被什么挡着的.
//
//	松开之后不猜: 保持当前这个时区不变, 等下一次心跳来顶
func (z *Zone) Follow() {
	z.mu.Lock()
	was := z.fixed
	z.fixed = false
	z.mu.Unlock()
	if was && z.log != nil {
		z.log.Append(signalPID, abi.EvZoneSet, map[string]any{
			"zone": z.Name(), "fixed": false})
	}
}

// Adopt 采集端报来的时区.
//
//	**只在没明确设过时采纳**, 而且只在真的变了的时候落一条账 ——
//	手机每次报到都带 tz, 每条都落账的话一天几百条
func (z *Zone) Adopt(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	loc, err := loadZone(name)
	if err != nil {
		return false
	}
	z.mu.Lock()
	if z.fixed || z.name == loc.String() {
		z.mu.Unlock()
		return false
	}
	z.apply(loc)
	z.mu.Unlock()
	if z.log != nil {
		z.log.Append(signalPID, abi.EvZoneSet, map[string]any{
			"zone": loc.String(), "fixed": false})
	}
	return true
}

// UseEnv 拿 TZ 环境变量当兜底 —— **只在什么都没有的时候**.
//
//	返回 false 表示这台机器最后落在 UTC 上, 调用方该说出来
func (z *Zone) UseEnv(tz string) bool {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return false
	}
	loc, err := loadZone(tz)
	if err != nil {
		return false
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.fixed || z.name != "UTC" {
		return true
	}
	z.apply(loc)
	return true
}

// Restore 开机装回来.
//
//	**必须在别的恢复之前**: 日报"今天发过没有"、打扰额度"今天用了几次"、
//	停留"算在哪一天"都是按本地日子算的. 顺序反了的话, 装回来的那些
//	按 UTC 的日子分组, 而之后按本地的日子查 —— 于是日报当天再发一遍,
//	额度从零开始
func (z *Zone) Restore(events []abi.Event) {
	for _, e := range events {
		if e.Kind != abi.EvZoneSet {
			continue
		}
		m, ok := e.Payload.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["zone"].(string)
		loc, err := loadZone(name)
		if err != nil {
			continue
		}
		fixed, _ := m["fixed"].(bool)
		z.mu.Lock()
		z.fixed = fixed
		z.apply(loc)
		z.mu.Unlock()
	}
}

// Note 开机时在 stderr 上说的那一句.
//
//	**兜底成 UTC 一定要说出来**: 一台以为自己在 UTC 的机器, 它的每一条
//	时间判断都是错的, 而没有任何一处会报错
func (z *Zone) Note() string {
	z.mu.Lock()
	name, fixed := z.name, z.fixed
	z.mu.Unlock()
	now := time.Now().Format("15:04")
	if name == "UTC" {
		return "⏰ 时区没设, 按 UTC 算(现在 " + now + ") —— 日报、闹钟、" +
			"规则里的时段会全部差几个小时。手机连上来会自动带上时区, " +
			"或者在设置里选一个"
	}
	how := "手机报的"
	if fixed {
		how = "设定的"
	}
	return fmt.Sprintf("⏰ 时区 %s(%s), 现在 %s", name, how, now)
}

// loadZone 认一个时区名. 认不出来的话**把认得的几个报出来** ——
// 只说"不认识"的话, 调用方只能再猜一次
func loadZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("时区名是空的。照 IANA 那套写, 比如 %s",
			strings.Join(commonZones(), " / "))
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("不认识时区 %q。照 IANA 那套写, 比如 %s",
			name, strings.Join(commonZones(), " / "))
	}
	return loc, nil
}

// CommonZones 界面上给人选的那几个 —— 见 commonZones.
func CommonZones() []string { return commonZones() }

// commonZones 界面上给人选的那几个.
//
//	**不列全世界 500 个**: 一个滚不到底的列表比几个常见的更难用.
//	不在里面的照 IANA 名字填得进去(loadZone 认所有的)
func commonZones() []string {
	return []string{
		"Asia/Shanghai", "Asia/Hong_Kong", "Asia/Taipei", "Asia/Tokyo",
		"Asia/Singapore", "Asia/Dubai", "Europe/London", "Europe/Berlin",
		"America/New_York", "America/Los_Angeles", "Australia/Sydney", "UTC",
	}
}
