package osinit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 信号的可读视图 —— 让 agent 能回答"我今天去过哪儿".
//
// ── 信号既需要主动推送, 也需要按需查询 ──
//
// "推"的路径 (信号 → 摘要 → 主动进程 → 打扰预算) 不提供按需查询.
// 要回答"我今天去过哪儿", agent 还需要能主动读取信号记录的"拉"路径.
//
// ── 为什么是一个文件, 不是一个工具 ──
//
// **能做成环境事实的, 就不要做成记忆.** 信号记录放在可查询的卷文件中,
// 让 agent 按需读取, 不依赖模型记住持续变化的事件.
//
// 三种方案的代价:
//
//	加一个 recall 系统调用   要动 wire/server/client 三处, 而且这是
//	                        **需要回值**的调用(Emit 是单向的)
//	塞进系统提示词           它每分钟都在变, 而提示词必须逐字节稳定 ——
//	                        一变整段前缀作废, 这是这个系统最贵的东西
//	**写成卷里的一个文件**    agent 用现成的 search / read_file 就能查
//
// 第三条不需要新 ABI、不需要新工具、不破坏前缀. 而且它天然符合
// "先想清楚缺哪条信息再去拿那一条" —— 它可以 search 一个日期、
// 一个地名、一种事件, 而不是把一天的信号全塞进上下文.
//
// ── 代价说清楚 ──
//
// 这个文件在卷里, 而 agent 对卷有写权限 —— **它能改这份视图**.
// 但改了不影响任何东西: 事件日志才是真相源, 视图是派生的, 下次重写就回来了.
// (账本本身在卷外, agent 无法修改.)
type SignalView struct {
	mu   sync.Mutex
	path string
	// places 把坐标换成地名 —— 视图是给人和模型看的, "31.86001"没有意义
	places *Places
	buf    []string
	// wrote 已经写进去多少行 —— 只增不改, 跟事件日志一个语义
	wrote int
}

// signalViewName 卷里的路径.
//
// 放在 .neox/ 下而不是根目录: 用户的工作目录是他的, 我们往里放东西
// 要放在一个一眼看得出"这是系统的"的地方.
const signalViewName = ".neox/signals.txt"

func NewSignalView(volumeRoot string, places *Places) *SignalView {
	return &SignalView{path: filepath.Join(volumeRoot, signalViewName), places: places}
}

// Path 视图文件在卷里的相对路径 —— 提示词要告诉 agent 去哪儿找
func (v *SignalView) Path() string { return signalViewName }

// Append 记一条.
//
// **一行一条, 人话, 可 grep** —— 它的读者是 search 和模型, 不是解析器.
// 写成 JSON 的话, 模型要么整行读进来(浪费), 要么自己解析(会错).
func (v *SignalView) Append(s abi.Signal) {
	line := v.render(s)
	v.mu.Lock()
	v.buf = append(v.buf, line)
	v.mu.Unlock()
}

// render 一条信号的一行.
//
// 形如:
//
//	2026-08-16 09:12  到达  公司
//	2026-08-16 18:03  离开  公司  待了 8.8 小时
//	2026-08-16 18:40  来电  138xxxx
//
// 时间放最前面, 因为**最常见的查法是按天/按时段** —— search "2026-08-16"
// 就能把一天捞出来.
func (v *SignalView) render(s abi.Signal) string {
	at := time.UnixMilli(s.At).Format("2006-01-02 15:04")
	what := signalLabel(s.Kind)
	var parts []string

	// 地名优先于坐标 —— 视图是给人看的
	if v.places != nil {
		if lat, ok1 := floatOf(s.Body["lat"]); ok1 {
			if lon, ok2 := floatOf(s.Body["lon"]); ok2 {
				if name := v.places.Lookup(lat, lon); name != "" {
					parts = append(parts, name)
				} else {
					parts = append(parts, fmt.Sprintf("(%.4f,%.4f)", lat, lon))
				}
			}
		}
	}
	// 其余字段按键名排序, 跳过已经用掉的和纯噪音的
	keys := make([]string, 0, len(s.Body))
	for k := range s.Body {
		switch k {
		case "lat", "lon", "acc", "unit":
		default:
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := humanValue(s.Body[k])
		if strings.HasSuffix(k, "Ms") {
			val = humanDuration(int64(mustFloat(s.Body[k])))
			parts = append(parts, "待了 "+val)
			continue
		}
		if val == "" {
			continue
		}
		parts = append(parts, val)
	}
	return fmt.Sprintf("%s  %s  %s", at, what, strings.Join(parts, "  "))
}

// signalLabel 种类的人话名.
//
// 认不出来的**原样保留**, 不要翻成"其它" —— 一个没见过的种类,
// 原始名字至少还能被 search 到, 翻成"其它"就彻底查不出来了.
func signalLabel(kind string) string {
	switch kind {
	case "place.arrived":
		return "到达"
	case "place.left":
		return "离开"
	case "call.incoming":
		return "来电"
	case "call.missed":
		return "未接"
	case "battery.level":
		return "电量"
	case "battery.charging":
		return "充电"
	case "lock.opened":
		return "开锁"
	case "lock.closed":
		return "上锁"
	case "door.opened":
		return "开门"
	case "door.closed":
		return "关门"
	case "presence.changed":
		return "在家状态"
	case "presence.motion":
		return "有人活动"
	case "alarm.fired":
		return "警报"
	}
	return kind
}

// Flush 落盘.
//
// ── 为什么攒一批再写, 而不是来一条写一条 ──
//
// 信号是成批到的(一次上传几条、一次轮询几条). 来一条开一次文件,
// 一天几万次系统调用换不到任何东西 —— 而这个文件的读者是"用户偶尔问一句",
// 延迟几秒完全无所谓.
func (v *SignalView) Flush() error {
	v.mu.Lock()
	if len(v.buf) == 0 {
		v.mu.Unlock()
		return nil
	}
	batch := v.buf
	v.buf = nil
	v.wrote += len(batch)
	v.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(v.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(v.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(batch, "\n") + "\n")
	return err
}

// Start 定期落盘. 返回停止函数
func (v *SignalView) Start(every time.Duration) (stop func()) {
	if every <= 0 {
		every = 5 * time.Second
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				_ = v.Flush()
				return
			case <-t.C:
				_ = v.Flush()
			}
		}
	}()
	return func() { close(done) }
}

// Lines 已经写出去多少行 —— 排查"为什么查不到"时要看它
func (v *SignalView) Lines() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.wrote
}

func mustFloat(v any) float64 {
	f, _ := floatOf(v)
	return f
}
