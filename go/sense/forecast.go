package sense

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 天气预报 —— **问的时候现查**, 跟 weather.go 那个接入器分工不同.
//
//	那个每 30 分钟拉一次, 变成世界里的一条事实, 给规则("下雨提醒我
//	带伞")用; 它只关心"现在"和"接下来几小时下不下".
//
//	询问"今天蚌埠天气怎么样"时, 如果只返回"现在多云, 二十度出头" ——
//	**问的是今天, 答的是此刻**. 事实里只有此刻, 它就只能答此刻.
//	今天、明天、几点开始下、有没有预警, 得问预报.
//
//	百度这个接口一次全给: 实况、逐小时(带降水概率)、五天、预警、生活指数.
//	同一把 key, 按坐标问(手机报的 WGS-84 直接给), 不用先查城市编码.

// Forecast 某个地方的预报, 已经说成几行人话.
func (b *Baidu) Forecast(ctx context.Context, lat, lon float64) (string, error) {
	q := url.Values{}
	q.Set("location", fmt.Sprintf("%.6f,%.6f", lon, lat)) // 注意: 经度在前
	q.Set("coordtype", "wgs84")
	q.Set("data_type", "all")
	var m struct {
		Result struct {
			Location struct {
				City string `json:"city"`
				Name string `json:"name"`
			} `json:"location"`
			Now struct {
				Text  string  `json:"text"`
				Temp  float64 `json:"temp"`
				Feels float64 `json:"feels_like"`
				RH    int     `json:"rh"`
				Wind  string  `json:"wind_class"`
				Dir   string  `json:"wind_dir"`
				AQI   int     `json:"aqi"`
			} `json:"now"`
			Days []struct {
				Date      string  `json:"date"`
				Week      string  `json:"week"`
				Day       string  `json:"text_day"`
				Night     string  `json:"text_night"`
				High      float64 `json:"high"`
				Low       float64 `json:"low"`
				WindDay   string  `json:"wc_day"`
				WindDirDy string  `json:"wd_day"`
			} `json:"forecasts"`
			Hours []struct {
				Text string  `json:"text"`
				Temp float64 `json:"temp_fc"`
				Pop  int     `json:"pop"`
				At   string  `json:"data_time"`
			} `json:"forecast_hours"`
			Alerts []struct {
				Type  string `json:"type"`
				Level string `json:"level"`
				Title string `json:"title"`
			} `json:"alerts"`
		} `json:"result"`
	}
	if err := b.get(ctx, "/weather/v1/", "天气查询", q, &m); err != nil {
		return "", err
	}
	r := m.Result
	var out strings.Builder
	place := r.Location.City
	if r.Location.Name != "" && r.Location.Name != place {
		place += r.Location.Name
	}
	fmt.Fprintf(&out, "%s 现在%s %.0f°C（体感 %.0f°C），%s%s，湿度 %d%%",
		place, r.Now.Text, r.Now.Temp, r.Now.Feels, r.Now.Dir, r.Now.Wind, r.Now.RH)
	if r.Now.AQI > 0 {
		fmt.Fprintf(&out, "，空气 %s（AQI %d）", aqiWord(r.Now.AQI), r.Now.AQI)
	}
	for i, d := range r.Days {
		if i == 3 {
			break
		}
		label := []string{"今天", "明天", "后天"}[i]
		sky := d.Day
		if d.Night != "" && d.Night != d.Day {
			sky += "转" + d.Night
		}
		date := d.Date
		if len(date) == len("2006-01-02") {
			date = date[5:] // 年份是噪音
		}
		fmt.Fprintf(&out, "\n%s（%s %s）%s %.0f~%.0f°C，%s%s",
			label, date, d.Week,
			sky, d.Low, d.High, d.WindDirDy, d.WindDay)
	}
	// 接下来 12 小时里**哪几个钟点可能下** —— "几点开始下"比"今天有雨"有用
	var wet []string
	peak := 0
	for i, h := range r.Hours {
		if i == 12 {
			break
		}
		if h.Pop >= 40 {
			hh := h.At
			if t, err := time.Parse("2006-01-02 15:04:05", h.At); err == nil {
				hh = t.Format("15点")
			}
			wet = append(wet, fmt.Sprintf("%s %d%%", hh, h.Pop))
		}
		if h.Pop > peak {
			peak = h.Pop
		}
	}
	if len(wet) > 0 {
		out.WriteString("\n接下来 12 小时可能下雨：" + strings.Join(wet, "、"))
	} else if len(r.Hours) > 0 {
		fmt.Fprintf(&out, "\n接下来 12 小时降水概率最高 %d%%，不用带伞", peak)
	}
	for _, a := range r.Alerts {
		fmt.Fprintf(&out, "\n⚠ %s", firstNonEmpty(a.Title, a.Type+a.Level+"预警"))
	}
	return out.String(), nil
}

func aqiWord(aqi int) string {
	switch {
	case aqi <= 50:
		return "优"
	case aqi <= 100:
		return "良"
	case aqi <= 150:
		return "轻度污染"
	case aqi <= 200:
		return "中度污染"
	}
	return "重度污染"
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
