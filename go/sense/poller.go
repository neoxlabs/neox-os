package sense

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 采集器的运行体: 轮询 → 过滤 → 投递.
//
// 这里唯一有判断的地方是**投递失败怎么办**, 别的都是管道.

// Sink 把信号投给 OS 的采集入口
type Sink struct {
	URL   string // 形如 http://127.0.0.1:8787
	Token string
	HTTP  *http.Client
}

// maxPerPost 一次请求最多投几条.
//
// **比 OS 那边的上限(1000)小一截, 是故意的**: 采集端不该正好坐在
// 边界上 —— OS 侧的上限将来收紧一点, 坐在边界上的采集端就会
// 集体开始 413, 而它们是装在别人手机里的, 改不动.
const maxPerPost = 500

// Post 投一批. 返回 OS 侧的逐条处理结果.
//
// **超过一批的上限就自己拆.** OS 那边给 /signals 加了条数上限(理由是
// 一次 53M 的补发能把宿主的 RSS 从 7MB 顶到 498MB), 而一次全发的话,
// 攒了两千条的手机会永远 413 —— 补发永远补不上, 它自己看到的只是
// "投递失败", 一直重试, 一直没人知道. 那正是 OS 侧注释里写的
// "拒绝必须让采集端能取得进展", 这里是它的另一半.
func (s Sink) Post(sigs []abi.Signal) (map[string]int, error) {
	if len(sigs) == 0 {
		return nil, nil
	}
	if len(sigs) > maxPerPost {
		total := map[string]int{}
		for i := 0; i < len(sigs); i += maxPerPost {
			j := min(i+maxPerPost, len(sigs))
			c, err := s.postOne(sigs[i:j])
			if err != nil {
				// **前面几批是真的投进去了** —— 错误里要说清投到哪儿了,
				// 否则调用方只能整批重来, 而重来的那些会被去重挡掉,
				// 看起来像"投了但没进去"
				return total, fmt.Errorf("投到第 %d/%d 条时失败: %w", i, len(sigs), err)
			}
			for k, v := range c {
				total[k] += v
			}
		}
		return total, nil
	}
	return s.postOne(sigs)
}

func (s Sink) postOne(sigs []abi.Signal) (map[string]int, error) {
	body, err := json.Marshal(sigs)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", s.URL+"/signals", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	cli := s.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("采集入口返回 %d: %s", resp.StatusCode, trunc(string(raw), 200))
	}
	var out struct {
		Counts map[string]int `json:"counts"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Counts, nil
}

// Backlog 投不出去时的缓冲.
//
// ── 为什么必须有, 又为什么必须有界 ──
//
// OS 那侧会重启、会升级、网络会抖. 这几秒里 HA 的变化如果直接丢掉,
// 那些事件就**永久没有了** —— HA 的 last_changed 只保留最新一次,
// 下一轮轮询比出来的是"没变化"(状态已经稳定了), 谁也不知道中间开过门.
//
// 但缓冲不能无限长: 采集器跟 OS 断开一整天的话, 无限缓冲会吃光内存,
// 而且恢复时会一次性灌进去一天的信号 —— 那比丢掉更糟, 因为窗口会
// 被这一大坨历史冲垮, 而它们全都是迟到的.
//
// 所以: **有界, 满了丢最老的, 而且丢这件事要说出来.**
// 丢最老的而不是最新的, 因为新的更可能还有意义.
type Backlog struct {
	max     int
	items   []abi.Signal
	dropped int
}

func NewBacklog(max int) *Backlog {
	if max <= 0 {
		max = 500
	}
	return &Backlog{max: max}
}

// Add 加一批. 返回这次丢了多少 —— **丢弃必须能被上报**,
// 静默丢弃会让"为什么那段时间它什么都不知道"永远查不出来
func (b *Backlog) Add(sigs []abi.Signal) (dropped int) {
	b.items = append(b.items, sigs...)
	if over := len(b.items) - b.max; over > 0 {
		b.items = b.items[over:]
		b.dropped += over
		return over
	}
	return 0
}

func (b *Backlog) Take() []abi.Signal {
	out := b.items
	b.items = nil
	return out
}

func (b *Backlog) Len() int          { return len(b.items) }
func (b *Backlog) TotalDropped() int { return b.dropped }

// FetchStates 拉一次 HA 的全量状态
func FetchStates(cfg HAConfig, cli *http.Client) ([]HAState, error) {
	if cli == nil {
		cli = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequest("GET", cfg.BaseURL+"/api/states", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 HA(%s): %w", cfg.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("HA 拒绝了 token —— 检查 HA_TOKEN 是不是长期访问令牌")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HA 返回 %d: %s", resp.StatusCode, trunc(string(raw), 200))
	}
	return ParseStates(raw)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Beat 跟 OS 报到: "我还在, 我的节奏是每 N 秒一次".
//
// **没东西可投的时候也要打**. 一个采集端最常见的死法不是崩溃,
// 是**投不进去**(令牌过期、网断、OS 那边没起来) —— 长跑里真的发生了:
// HA 令牌过期, 采集器每次都报错重试, 而 OS 那边一个字都没有,
// 用户看到的是"今天很安静".
//
// 打不通不算错: 报到本来就是尽力而为的, 为它重试等于用一个次要的东西
// 去挤占投递的机会.
// Beat 只说"我还在" —— **不代表拿得到数据**. 拉取之前打的就是这个
func (s Sink) Beat(source string, pace time.Duration) { s.beat(source, pace, "", true) }

// BeatOK 我拿到数据了
func (s Sink) BeatOK(source string, pace time.Duration) { s.beat(source, pace, "", false) }

// BeatFailing 报到, 但如实说"我拿不到数据, 原因是…".
//
// **照打不误**: 不打的话这就退化成"失联", 而失联和"活着但瞎了"的
// 下一步不一样 —— 一个是去看进程还在不在, 一个是去看凭据.
// 那天真实发生的是后者(令牌过期), 而 OS 只看得出前者.
func (s Sink) BeatFailing(source string, pace time.Duration, why string) {
	s.beat(source, pace, why, false)
}

func (s Sink) beat(source string, pace time.Duration, why string, aliveOnly bool) {
	if source == "" || pace <= 0 {
		return
	}
	u := fmt.Sprintf("%s/heartbeat?source=%s&everySec=%d",
		s.URL, url.QueryEscape(source), int(pace.Seconds()))
	if why != "" {
		u += "&why=" + url.QueryEscape(why)
	} else if aliveOnly {
		u += "&alive=1"
	}
	req, err := http.NewRequest("POST", u, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	cli := s.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 10 * time.Second}
	}
	if resp, err := cli.Do(req); err == nil {
		resp.Body.Close()
	}
}
