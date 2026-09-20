package osinit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 当前生效的东西 —— 让 agent 看得见自己设过什么.
//
// ── 为什么必须有 ──
//
// 撤销地点关注和用药提醒时，agent 若只能答"我这边没有删除的工具……假装撤了才是骗你".
//
// **这不是编造结果, 但功能缺口比"没有撤销工具"更深**:
// **它看不见已经设过什么**. 没有 id 就无从撤起, 加十个撤销工具也没用.
//
// 跟 S15 是同一个形状(拉的路径), 那就用同一个办法:
// **能做成环境事实的, 就不要做成记忆** —— 写成卷里的一个文件.
//
// ── 跟 signals.txt 的区别: 一个是流水, 一个是现状 ──
//
//	signals.txt  只增不改的流水. 问"我今天去过哪儿"看它
//	state.txt    **每次重写**的现状. 问"我让你盯什么了""有什么提醒"看它
//
// 混成一个的话, 撤掉的关注还留在文件里, agent 会照着念一个已经不存在的东西.
type StatusView struct {
	mu       sync.Mutex
	path     string
	places   *Places
	watches  *Watches
	timers   *Timers
	lastText string
}

const statusViewName = ".neox/state.txt"

func NewStatusView(volumeRoot string, p *Places, w *Watches, t *Timers) *StatusView {
	return &StatusView{
		path:   filepath.Join(volumeRoot, statusViewName),
		places: p, watches: w, timers: t,
	}
}

func (v *StatusView) Path() string { return statusViewName }

// Render 现状的文本.
//
// **带 id**: 没有 id 用户就只能说"撤掉那个到家的提醒", 而 agent 要靠
// 模糊匹配去猜是哪一条 —— 猜错一次是静默的(他以为撤了提醒, 实际撤的是关注).
func (v *StatusView) Render() string {
	var b strings.Builder
	b.WriteString("# 当前生效的东西(这个文件每次都重写, 撤掉的不会留在这儿)\n\n")

	b.WriteString("## 认得的地点\n")
	if pls := v.places.Known(); len(pls) == 0 {
		b.WriteString("(还没有。用户说'这儿是公司'时用 name_place 记)\n")
	} else {
		for _, p := range pls {
			fmt.Fprintf(&b, "- %s\n", p.Name)
		}
	}

	b.WriteString("\n## 盯着的事(用户让你长期关注的)\n")
	if ws := v.watches.List(); len(ws) == 0 {
		b.WriteString("(没有)\n")
	} else {
		for _, w := range ws {
			what := w.Kind
			if w.Place != "" {
				what = strings.TrimSpace(what + " @" + w.Place)
			}
			fmt.Fprintf(&b, "- [%s] %s", w.ID, what)
			if w.Raw != "" {
				fmt.Fprintf(&b, "    原话:「%s」", w.Raw)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## 待响的提醒\n")
	if ws := v.timers.Pending(); len(ws) == 0 {
		b.WriteString("(没有)\n")
	} else {
		now := time.Now()
		for _, w := range ws {
			fmt.Fprintf(&b, "- [%s] %s(%s后)  %s\n", w.ID,
				time.UnixMilli(w.At).Format("01-02 15:04"),
				humanGapMs(w.At-now.UnixMilli()), w.Text)
		}
	}

	b.WriteString("\n(要撤掉其中一条, 用 cancel 工具, 参数就是方括号里的 id。" +
		"**别猜 id, 照这个文件念**)\n")
	return b.String()
}

// Flush 落盘. 内容没变就不写 —— 这个文件会被频繁重算,
// 每次都写会让 mtime 一直在跳, 而 agent 有时会看 mtime 判断"新不新"
func (v *StatusView) Flush() error {
	text := v.Render()
	v.mu.Lock()
	if text == v.lastText {
		v.mu.Unlock()
		return nil
	}
	v.lastText = text
	v.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(v.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(v.path, []byte(text), 0o644)
}

// Start 定期重写
func (v *StatusView) Start(every time.Duration) (stop func()) {
	if every <= 0 {
		every = 3 * time.Second
	}
	_ = v.Flush()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = v.Flush()
			}
		}
	}()
	return func() { close(done) }
}

// Cancel 按 id 撤一条. **前缀自识别**: w=闹钟, g=关注.
//
// 撤错对象是静默的(他以为取消了提醒, 实际取消的是"到家告诉我"),
// 所以不做模糊匹配, 也不做"猜最近的那条" —— 认不出来就明说.
func (v *StatusView) Cancel(id string) (string, error) {
	id = strings.TrimSpace(id)
	switch {
	case strings.HasPrefix(id, "w"):
		if v.timers.Cancel(id) {
			return "提醒 " + id + " 撤了", nil
		}
		return "", fmt.Errorf("没有 %s 这条提醒 —— 先看 %s 里现在有哪些, 别猜 id",
			id, statusViewName)
	case strings.HasPrefix(id, "g"):
		if v.watches.Remove(id) {
			return "不盯 " + id + " 了", nil
		}
		return "", fmt.Errorf("没有 %s 这条关注 —— 先看 %s 里现在有哪些, 别猜 id",
			id, statusViewName)
	}
	return "", fmt.Errorf("认不出 %q 这个 id。提醒是 w 开头, 关注是 g 开头 —— "+
		"照 %s 里方括号里的写", id, statusViewName)
}

// humanGapMs 跟 agent 那侧的 humanGap 一个意思 ——
// 绝对时间看不出错, 相对时间一眼看得出
func humanGapMs(ms int64) string {
	if ms < 0 {
		return "已过期"
	}
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}
