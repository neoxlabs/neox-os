package sense

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 天气 —— 第一个真正意义上的"外面的世界".
//
// ── 为什么第一个接的是天气 ──
//
//	它是**验证整条链最便宜的那一个**: 不要凭据、不要用户授权、
//	不要在手机上装东西, 而它足以跑通一条完整的主动智能:
//
//	  拉到降水概率 → 变成一条事实 → 规则判断 → 打扰预算 → 推到手机
//	  "今天下午有雨, 出门带伞"
//
//	日历和待办更有用, 但它们都要过 OAuth —— 那是另一段工程,
//	而在它做完之前, 整条链一天都验证不了.
//
// ── 为什么是 Open-Meteo ──
//
//	**它不要 API key**. 这一条压倒其它所有考量: 一个要配密钥的默认
//	数据源, 在用户配上之前等于不存在, 而"装完就能用"和"装完还要配"
//	之间的差别, 比任何数据质量的差别都大.

const openMeteo = "https://api.open-meteo.com/v1/forecast"

// Weather 拉当前天气和未来几小时的降水.
type Weather struct {
	// Where 查哪儿的天气. 返回 ok=false 表示还不知道 ——
	// **这不是故障**: 一台还没收到过位置信号的机器本来就查不了
	Where func() (lat, lon float64, ok bool)
	// Pace 多久拉一次. 天气一小时一变, 拉太勤只是浪费
	Pace time.Duration
	// HTTP 可换 —— 测试要塞一个假的
	HTTP *http.Client
	// Base 接口地址, 空 = 官方. 测试用
	Base string
	// Baidu 配了地图 key 就走百度 —— **天气只能有一个源**.
	//
	//	真机测试里抓到的: 世界模型说"12 点前后有雨（60%）"(Open-Meteo),
	//	而他问"今天要带伞吗"答"降水概率最高 0%, 不用带"(百度) ——
	//	同一轮对话里两句话打架, 而他信的是后一句。
	//
	//	在国内百度那份更贴谱(本地预警、逐小时中文), 所以配了就用它,
	//	没配才退回 Open-Meteo。
	Baidu *Baidu
}

func (w *Weather) Name() string { return "weather" }

func (w *Weather) Every() time.Duration {
	if w.Pace <= 0 {
		return 30 * time.Minute
	}
	return w.Pace
}

// meteoResp 只取用得上的那几个字段.
//
//	**不要把整份响应存下来**: 它有几十个字段, 而这里要回答的问题只有
//	两个 —— "现在什么天"和"接下来几小时会不会下". 多存的每一个字段
//	都会有人拿去做判断, 而那些判断没有一处测过.
type meteoResp struct {
	Current struct {
		Temp          float64 `json:"temperature_2m"`
		Precipitation float64 `json:"precipitation"`
		Code          int     `json:"weather_code"`
	} `json:"current"`
	Hourly struct {
		Time []string  `json:"time"`
		Prob []float64 `json:"precipitation_probability"`
	} `json:"hourly"`
}

// baiduNow 从百度那份预报里取出这条信号要的几个数.
//
//	跟 Baidu.Forecast 同一个接口、同一次形状 —— 那边出的是给人看的几行话,
//	这边出的是给规则判的几个数(见 rainProb: "今天会不会下雨"没有别的答法).
func (w *Weather) baiduNow(ctx context.Context, lat, lon float64) (map[string]any, error) {
	q := url.Values{}
	q.Set("location", fmt.Sprintf("%.6f,%.6f", lon, lat)) // 经度在前
	q.Set("coordtype", "wgs84")
	q.Set("data_type", "all")
	var m struct {
		Result struct {
			Now struct {
				Text string  `json:"text"`
				Temp float64 `json:"temp"`
			} `json:"now"`
			Hours []struct {
				Pop int    `json:"pop"`
				At  string `json:"data_time"`
			} `json:"forecast_hours"`
		} `json:"result"`
	}
	if err := w.Baidu.get(ctx, "/weather/v1/", "天气查询", q, &m); err != nil {
		return nil, err
	}
	r := m.Result
	peak, at := 0, ""
	for i, h := range r.Hours {
		if i == 12 {
			break
		}
		if h.Pop > peak {
			peak, at = h.Pop, h.At
		}
	}
	// 百度的时刻是 "2006-01-02 15:04:05", 而 hourOf 认的是 ISO ——
	// 统一成后者, 免得那句话变成"接下来有雨"丢掉钟点
	if t, err := time.Parse("2006-01-02 15:04:05", at); err == nil {
		at = t.Format("2006-01-02T15:04")
	}
	text := strings.TrimSpace(r.Now.Text)
	if text == "" {
		return nil, fmt.Errorf("百度天气没给实况")
	}
	body := map[string]any{
		"text":     rainSuffix(fmt.Sprintf("%s %.0f°C", text, r.Now.Temp), float64(peak), at),
		"tempC":    r.Now.Temp,
		"rainNow":  strings.Contains(text, "雨"),
		"rainProb": float64(peak),
		"rainAt":   at,
		"validSec": int(w.Every().Seconds()) * 2,
	}
	return body, nil
}

// rainSuffix 把"几点前后有雨"接在实况后面 —— 跟 describeWeather 同一条规矩:
// 40% 以下不提, 不然每天都在提雨
func rainSuffix(head string, prob float64, at string) string {
	if prob < 40 {
		return head
	}
	if hhmm := hourOf(at); hhmm != "" {
		return fmt.Sprintf("%s，%s 前后有雨（%.0f%%）", head, hhmm, prob)
	}
	return fmt.Sprintf("%s，接下来有雨（%.0f%%）", head, prob)
}

func (w *Weather) Fetch(ctx context.Context) ([]abi.Signal, error) {
	lat, lon, ok := w.Where()
	if !ok {
		// **这不是坏了, 是还没准备好** —— 见 abi.ErrNotReady.
		//
		//	一台刚装好、还没接位置采集端的机器查不了天气, 是正常的.
		//	报成"坏了"的话它会立刻推一条"采集端出问题了", 而用户学到的
		//	那一课是"这些警报不用看".
		return nil, fmt.Errorf("还不知道你在哪儿(等一次位置信号, "+
			"或者配上 NEOX_HOME_LATLON): %w", abi.ErrNotReady)
	}
	// **配了百度就用百度** —— 见 Weather.Baidu: 两个源会在同一轮对话里打架
	if w.Baidu != nil && w.Baidu.Ready() {
		if body, err := w.baiduNow(ctx, lat, lon); err == nil {
			return []abi.Signal{{
				ID:     fmt.Sprintf("weather-%d", time.Now().Unix()/60),
				Source: "weather", Kind: "weather.now",
				At: time.Now().UnixMilli(), Body: body,
			}}, nil
		}
		// 百度那条不通就退回 Open-Meteo —— 没天气比打架还糟
	}
	base := w.Base
	if base == "" {
		base = openMeteo
	}
	url := fmt.Sprintf("%s?latitude=%.4f&longitude=%.4f"+
		"&current=temperature_2m,precipitation,weather_code"+
		"&hourly=precipitation_probability&forecast_hours=12&timezone=auto",
		base, lat, lon)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	cli := w.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("天气接口回了 HTTP %d", resp.StatusCode)
	}
	var m meteoResp
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("天气接口的响应解不开: %w", err)
	}

	peak, whenPeak := peakRain(m.Hourly.Prob, m.Hourly.Time)
	body := map[string]any{
		"text":    describeWeather(m.Current.Code, m.Current.Temp, peak, whenPeak),
		"tempC":   m.Current.Temp,
		"code":    m.Current.Code,
		"rainNow": m.Current.Precipitation > 0,
		// rainProb 未来 12 小时里最大的降水概率(0-100).
		// **规则要判的就是这个数** —— "今天会不会下雨"没有别的答法
		"rainProb": peak,
		"rainAt":   whenPeak,
		// 天气**知道自己什么时候作废**. 说出来, 别让 OS 去猜 ——
		// 见 osinit.World 里那张 TTL 表
		"validSec": int(w.Every().Seconds()) * 2,
	}
	return []abi.Signal{{
		ID: fmt.Sprintf("weather-%d", time.Now().Unix()/60),
		// **ID 按分钟取整**: 同一分钟内拉两次(重启撞上周期)是同一条事实,
		// 让去重把它挡掉 —— 而不是在账本里留两条一样的
		Source: "weather", Kind: "weather.now",
		At: time.Now().UnixMilli(), Body: body,
	}}, nil
}

// peakRain 未来几小时里下得最凶的那一刻.
//
//	**取峰值而不是平均**: "今天下午 3 点 80% 的雨"和"一整天平均 20%"
//	对"要不要带伞"是完全不同的答案, 而平均会把前者抹平.
func peakRain(prob []float64, times []string) (float64, string) {
	best, at := 0.0, ""
	for i, p := range prob {
		if p > best {
			best = p
			if i < len(times) {
				at = times[i]
			}
		}
	}
	return best, at
}

// describeWeather 说成一句人话.
//
//	**结论不是数字**: "下午 3 点前后有雨, 出门带伞"是判断依据,
//	"precipitation_probability: 0.82"不是 —— 而模型拿到后者会自己
//	换算, 换错的时候看起来跟换对一模一样.
func describeWeather(code int, temp, prob float64, at string) string {
	var b strings.Builder
	b.WriteString(weatherWord(code))
	fmt.Fprintf(&b, " %.0f°C", temp)
	// 40% 以下不提 —— 提了的话每天都在提雨, 而那正是打扰预算要挡的东西
	if prob >= 40 {
		if hhmm := hourOf(at); hhmm != "" {
			fmt.Fprintf(&b, "，%s 前后有雨（%.0f%%）", hhmm, prob)
		} else {
			fmt.Fprintf(&b, "，接下来有雨（%.0f%%）", prob)
		}
	}
	return b.String()
}

// hourOf 从 "2026-09-10T15:00" 取 "15 点"
func hourOf(iso string) string {
	if len(iso) < 13 {
		return ""
	}
	return strings.TrimPrefix(iso[11:13], "0") + " 点"
}

// weatherWord WMO 天气代码 → 一个词.
//
//	只分到"人会据此改变行为"的粒度: 晴/阴/雨/雪/雷. 再细的分类
//	(小雨中雨大雨)对"要不要带伞"这个决定没有区别
func weatherWord(code int) string {
	switch {
	case code == 0:
		return "晴"
	case code <= 3:
		return "多云"
	case code <= 49:
		return "有雾"
	case code <= 69:
		return "有雨"
	case code <= 79:
		return "有雪"
	case code <= 84:
		return "阵雨"
	case code <= 99:
		return "雷雨"
	}
	return "天气不明"
}
