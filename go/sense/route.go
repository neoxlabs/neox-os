package sense

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 路上要多久、走哪条、堵不堵 —— **「最晚几点出门」的全部依赖**.
//
// ── 为什么这一个绕不过去 ──
//
//	timers.go 那段注释自己写着"今天几点出门取决于今天的日程、此刻的路况、
//	你在不在家". 路况**没有任何办法本地算**: 它是此时此刻那条路上有多少车,
//	只有跑地图的那家公司知道.
//
// ── 为什么使用 v2 而不是轻量版 ──
//
//	只需要一个数("现在出发要多久")时, 轻量版够用. 如果问的是
//	"路线是什么? 要多久? 堵不堵车" —— 三个问题, 轻量版只答得了第二个.
//	缺少这些字段时, 模型可能**编造**另外两个答案: "走东海大道往北那条线"
//	(工具根本没给路名), 或"实时路况我这边看不到, 按平常速度算的"
//	(那个时间就是按实时路况算的).
//
//	v2 每一段都带路名和路况, 还有红绿灯数、打车费和备选路线. 同一把 key,
//	同一次请求. **把这些交给模型, 它就不用编了.**
//
// ── 坐标系 ──
//
//	传 coord_type=wgs84, 让百度自己转. 手机报的就是 WGS-84,
//	**在我们这一侧转的话, 就多了一处会跟内部存的那份不一致的地方** ——
//	而那种不一致是静默的: 路线规划从一个偏了一公里的起点开始算,
//	算出来的时间看起来完全正常.

// Mode 怎么去.
type Mode string

const (
	ByCar     Mode = "drive"
	ByBike    Mode = "ride"
	OnFoot    Mode = "walk"
	ByTransit Mode = "transit"
)

// ParseMode 模型会写"开车/公交/骑车/走路", 也会写 drive/bus —— 都认.
// 认不出按开车: 这台机器的用户是开车上班的, 猜错的代价最小.
func ParseMode(s string) Mode {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "", strings.Contains(s, "drive"), strings.Contains(s, "car"),
		strings.Contains(s, "开车"), strings.Contains(s, "驾车"), strings.Contains(s, "打车"):
		return ByCar
	case strings.Contains(s, "ride"), strings.Contains(s, "bike"),
		strings.Contains(s, "骑"), strings.Contains(s, "电动"):
		return ByBike
	case strings.Contains(s, "walk"), strings.Contains(s, "步行"), strings.Contains(s, "走路"):
		return OnFoot
	case strings.Contains(s, "transit"), strings.Contains(s, "bus"), strings.Contains(s, "公交"),
		strings.Contains(s, "地铁"), strings.Contains(s, "公共交通"):
		return ByTransit
	}
	return ByCar
}

// Seg 一条路上的一段. 连着的同名段已经合并.
type Seg struct {
	Road    string
	Meters  int
	Seconds int
	// Worst 这段路最堵的那一截: 0 没数据 1 畅通 2 缓行 3 拥堵 4 严重拥堵
	Worst int
	// Slow 缓行及以上的有多少米
	Slow int
}

// Route 一条路线.
type Route struct {
	Mode Mode
	// Duration 现在出发要多久. 开车那条**已经按此刻路况算过**
	Duration time.Duration
	Meters   int
	// Tag 百度给的这条路线的特点("大路多"/"少等灯"). 备选才用得上
	Tag     string
	Lights  int
	TaxiFee int
	Toll    bool
	Roads   []Seg
	// Rides 公交那一段段怎么坐, 已经说成人话
	Rides []string
	// Fare 公交票价(元). 0 = 不知道
	Fare float64
	// Live 这条路线有没有实时路况数据 —— 没有就不能说"一路畅通"
	Live bool
}

// Route 从 A 到 B. 返回的第一条是推荐的, 后面是备选(只有开车有).
func (b *Baidu) Route(ctx context.Context, fromLat, fromLon, toLat, toLon float64,
	mode Mode) ([]Route, error) {
	q := url.Values{}
	q.Set("origin", fmt.Sprintf("%.6f,%.6f", fromLat, fromLon))
	q.Set("destination", fmt.Sprintf("%.6f,%.6f", toLat, toLon))
	q.Set("coord_type", "wgs84")
	switch mode {
	case ByBike, OnFoot, ByTransit:
		return b.liteRoute(ctx, q, mode)
	}
	// 要备选: "另一条要多久"正是他决定走哪条的依据, 而多一条路线
	// 是同一次请求里顺带回来的
	q.Set("alternatives", "1")
	var m struct {
		Result struct {
			Routes []struct {
				Distance int    `json:"distance"`
				Duration int    `json:"duration"`
				Tag      string `json:"tag"`
				Lights   int    `json:"traffic_light"`
				TaxiFee  int    `json:"taxi_fee"`
				Toll     int    `json:"toll"`
				Steps    []struct {
					Road     string `json:"road_name"`
					Distance int    `json:"distance"`
					Duration int    `json:"duration"`
					Traffic  []struct {
						Status   int     `json:"status"`
						Distance float64 `json:"distance"`
					} `json:"traffic_condition"`
				} `json:"steps"`
			} `json:"routes"`
		} `json:"result"`
	}
	if err := b.get(ctx, "/direction/v2/driving", "驾车路线规划", q, &m); err != nil {
		return nil, err
	}
	var out []Route
	for _, r := range m.Result.Routes {
		rt := Route{Mode: ByCar, Duration: time.Duration(r.Duration) * time.Second,
			Meters: r.Distance, Tag: r.Tag, Lights: r.Lights, TaxiFee: r.TaxiFee,
			Toll: r.Toll > 0}
		for _, s := range r.Steps {
			seg := Seg{Road: s.Road, Meters: s.Distance, Seconds: s.Duration}
			for _, c := range s.Traffic {
				if c.Status > 0 {
					rt.Live = true
				}
				if c.Status > seg.Worst {
					seg.Worst = c.Status
				}
				if c.Status >= 2 {
					seg.Slow += int(c.Distance)
				}
			}
			rt.Roads = appendSeg(rt.Roads, seg)
		}
		out = append(out, rt)
	}
	if len(out) == 0 {
		// 两点之间没有可行的驾车路线(隔着海、其中一个在境外)——
		// **这不是故障**, 但也不能装作算出来了
		return nil, fmt.Errorf("这两点之间没算出驾车路线")
	}
	return out, nil
}

var boldRoad = regexp.MustCompile(`(?:进入|沿|走)<b>([^<]+)</b>`)

// liteRoute 骑车/走路/公交 —— 轻量版就够, 它们不看路况.
func (b *Baidu) liteRoute(ctx context.Context, q url.Values, mode Mode) ([]Route, error) {
	path, name := "/directionlite/v1/riding", "骑行路线规划"
	switch mode {
	case OnFoot:
		path, name = "/directionlite/v1/walking", "步行路线规划"
	case ByTransit:
		path, name = "/directionlite/v1/transit", "公交路线规划"
	}
	type step struct {
		Distance    int    `json:"distance"`
		Duration    int    `json:"duration"`
		Type        int    `json:"type"`
		Instruction string `json:"instruction"`
		// Name 骑行每一步直接给路名; 步行没有, 只能从指令里抽
		Name    string `json:"name"`
		Vehicle struct {
			Name     string `json:"name"`
			Dir      string `json:"direct_text"`
			From     string `json:"start_name"`
			To       string `json:"end_name"`
			Stops    int    `json:"stop_num"`
			LastTime string `json:"end_time"`
		} `json:"vehicle"`
	}
	var m struct {
		Result struct {
			Routes []struct {
				Distance int      `json:"distance"`
				Duration int      `json:"duration"`
				Price    float64  `json:"price"`
				Steps    rawSteps `json:"steps"`
			} `json:"routes"`
		} `json:"result"`
	}
	if err := b.get(ctx, path, name, q, &m); err != nil {
		return nil, err
	}
	// 轻量版的备选对这三种没意义 —— 取第一条就够
	if len(m.Result.Routes) == 0 {
		return nil, fmt.Errorf("这两点之间没算出%s", strings.TrimSuffix(name, "规划"))
	}
	{
		r := m.Result.Routes[0]
		rt := Route{Mode: mode, Duration: time.Duration(r.Duration) * time.Second,
			Meters: r.Distance, Fare: r.Price}
		var steps []step
		// 公交的 steps 是二维的(每一段里可能有几个可换乘的方案, 取第一个);
		// 骑行/步行是一维的. 两种都认
		r.Steps.each(func(raw []byte) { steps = append(steps, decodeStep[step](raw)) })
		for _, s := range steps {
			if mode == ByTransit {
				if s.Vehicle.Name == "" {
					if s.Distance > 0 {
						rt.Rides = append(rt.Rides, fmt.Sprintf("步行 %d 米", s.Distance))
					}
					continue
				}
				ride := fmt.Sprintf("%s 上 %s", s.Vehicle.From, s.Vehicle.Name)
				if s.Vehicle.Dir != "" {
					ride += "（" + s.Vehicle.Dir + "）"
				}
				if s.Vehicle.Stops > 0 {
					ride += fmt.Sprintf("，坐 %d 站", s.Vehicle.Stops)
				}
				ride += "到 " + s.Vehicle.To
				if s.Vehicle.LastTime != "" {
					ride += "（末班 " + s.Vehicle.LastTime + "）"
				}
				rt.Rides = append(rt.Rides, ride)
				continue
			}
			road := strings.TrimSpace(s.Name)
			if road == "" {
				if mm := boldRoad.FindStringSubmatch(s.Instruction); mm != nil {
					road = mm[1]
				}
			}
			rt.Roads = appendSeg(rt.Roads, Seg{Road: road, Meters: s.Distance, Seconds: s.Duration})
		}
		return []Route{rt}, nil
	}
}

// rawSteps 公交的 steps 是 [][]step(每一段里有几个可换乘的方案), 骑行/步行
// 是 []step. 一个类型两种都认, 公交那种取每段的第一个方案.
type rawSteps []json.RawMessage

func (r rawSteps) each(fn func([]byte)) {
	for _, item := range r {
		t := bytes.TrimSpace(item)
		if len(t) > 0 && t[0] == '[' {
			var inner []json.RawMessage
			if json.Unmarshal(t, &inner) == nil && len(inner) > 0 {
				fn(inner[0])
			}
			continue
		}
		fn(t)
	}
}

func decodeStep[T any](raw []byte) T {
	var v T
	_ = json.Unmarshal(raw, &v)
	return v
}

// appendSeg 连着的同名段并成一段 —— "东海大道 17 段"对人是噪音.
// 没名字的接到上一段里: 它多半是路口、匝道, 算进旁边那条路没毛病.
func appendSeg(segs []Seg, s Seg) []Seg {
	if s.Road == "无名路" {
		s.Road = ""
	}
	if n := len(segs); n > 0 && (segs[n-1].Road == s.Road || s.Road == "") {
		last := &segs[n-1]
		last.Meters += s.Meters
		last.Seconds += s.Seconds
		last.Slow += s.Slow
		if s.Worst > last.Worst {
			last.Worst = s.Worst
		}
		return segs
	}
	return append(segs, s)
}

var jamWord = [...]string{"", "畅通", "缓行", "拥堵", "严重拥堵"}

// Text 说成几行人话. **给人和模型的都是这一份** ——
// 一个"1847 秒"的数, 模型会自己去换算, 而它算错的时候看起来跟算对一样.
func (r Route) Text() string {
	var b strings.Builder
	verb := map[Mode]string{ByCar: "开车", ByBike: "骑车", OnFoot: "走路", ByTransit: "公交"}[r.Mode]
	fmt.Fprintf(&b, "%s %s（%s", verb, human(r.Duration), km(r.Meters))
	if r.Mode == ByCar {
		// **明说是按此刻路况算的**: 上一版只给一个数, 模型就对他说
		// "这是按平常速度推的, 实时路况我看不到" —— 恰恰相反
		if r.Live {
			b.WriteString("，已按此刻路况算")
		}
	}
	b.WriteString("）")
	if r.Lights > 0 {
		fmt.Fprintf(&b, "，%d 个红绿灯", r.Lights)
	}
	if r.TaxiFee > 0 {
		fmt.Fprintf(&b, "，打车约 %d 元", r.TaxiFee)
	}
	if r.Toll {
		b.WriteString("，有收费路段")
	}
	if r.Fare > 0 {
		fmt.Fprintf(&b, "，票价 %.0f 元", r.Fare)
	}
	if names := r.mainRoads(6); len(names) > 0 {
		b.WriteString("\n走：" + strings.Join(names, " → "))
	}
	if len(r.Rides) > 0 {
		b.WriteString("\n怎么坐：" + strings.Join(r.Rides, " → "))
	}
	if r.Mode == ByCar {
		b.WriteString("\n路况：" + r.jams())
	}
	return b.String()
}

// mainRoads 主要走哪几条路 —— 太短的是路口和掉头, 不算"走哪条".
func (r Route) mainRoads(max int) []string {
	var out []string
	for _, s := range r.Roads {
		if s.Road == "" || s.Meters < 200 {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == s.Road {
			continue
		}
		out = append(out, s.Road)
		if len(out) == max {
			break
		}
	}
	return out
}

// jams 哪儿堵. **没数据就说没数据**, 不能说"一路畅通" ——
// 他会照着那句话晚出门十分钟.
func (r Route) jams() string {
	if !r.Live {
		return "这条路线没有实时路况数据"
	}
	var parts []string
	for _, s := range r.Roads {
		if s.Worst < 2 || s.Slow < 100 {
			continue
		}
		name := s.Road
		if name == "" {
			name = "一段小路"
		}
		parts = append(parts, fmt.Sprintf("%s %s%s", name, km(s.Slow), jamWord[s.Worst]))
	}
	if len(parts) == 0 {
		return "一路畅通"
	}
	return strings.Join(parts, "、")
}

// Summary 推荐那条 + 备选里最值得一提的那条.
func Summary(routes []Route) string {
	if len(routes) == 0 {
		return ""
	}
	out := routes[0].Text()
	for _, alt := range routes[1:] {
		// 只提**快一点或者差不多**的那条 —— 慢十分钟的备选没人会走
		if alt.Duration > routes[0].Duration+5*time.Minute {
			continue
		}
		tag := alt.Tag
		if tag == "" {
			tag = "备选"
		}
		out += fmt.Sprintf("\n另一条（%s）：%s（%s）", tag, human(alt.Duration), km(alt.Meters))
		if names := alt.mainRoads(5); len(names) > 0 {
			out += "，走 " + strings.Join(names, " → ")
		}
		if alt.Live {
			out += "，" + alt.jams()
		}
		break
	}
	return out
}

func human(d time.Duration) string {
	mins := int(d.Round(time.Minute).Minutes())
	if mins < 1 {
		mins = 1
	}
	if mins >= 60 {
		return fmt.Sprintf("%d 小时 %d 分", mins/60, mins%60)
	}
	return fmt.Sprintf("%d 分钟", mins)
}

func km(m int) string {
	if m < 1000 {
		return fmt.Sprintf("%d 米", m)
	}
	return fmt.Sprintf("%.1f 公里", float64(m)/1000)
}

// ── 实时路况 ──
//
//	路线里的路况只管**这一条路线**. 他问"朝阳路堵不堵"、"我这附近堵不堵",
//	问的是一条路或者一片区域 —— 那是另外两个接口, 同一把 key.

type trafficResp struct {
	Description string `json:"description"`
	Evaluation  struct {
		Status int    `json:"status"`
		Desc   string `json:"status_desc"`
	} `json:"evaluation"`
	Roads []struct {
		Name     string `json:"road_name"`
		Sections []struct {
			Desc     string  `json:"section_desc"`
			Status   int     `json:"status"`
			Speed    float64 `json:"speed"`
			Distance int     `json:"congestion_distance"`
			Trend    string  `json:"congestion_trend"`
		} `json:"congestion_sections"`
	} `json:"road_traffic"`
}

func (t trafficResp) text() string {
	out := strings.TrimSpace(t.Description)
	if out == "" {
		out = t.Evaluation.Desc
	}
	n := 0
	for _, r := range t.Roads {
		for _, s := range r.Sections {
			if n == 4 {
				break
			}
			line := fmt.Sprintf("\n· %s %s", r.Name, s.Desc)
			if s.Status >= 1 && s.Status < len(jamWord) {
				line += " " + jamWord[s.Status]
			}
			if s.Distance > 0 {
				line += " " + km(s.Distance)
			}
			if s.Speed > 0 {
				line += fmt.Sprintf("，时速约 %.0f", s.Speed)
			}
			if s.Trend != "" {
				line += "，" + s.Trend
			}
			out += line
			n++
		}
	}
	return out
}

// RoadTraffic 一条路此刻堵不堵. city 要带: 全国叫"人民路"的有几百条.
func (b *Baidu) RoadTraffic(ctx context.Context, road, city string) (string, error) {
	road = strings.TrimSpace(road)
	if road == "" {
		return "", fmt.Errorf("哪条路?")
	}
	if strings.TrimSpace(city) == "" {
		return "", fmt.Errorf("「%s」在哪个城市? 全国同名的路很多", road)
	}
	q := url.Values{}
	q.Set("road_name", road)
	q.Set("city", city)
	var m trafficResp
	if err := b.get(ctx, "/traffic/v1/road", "实时路况", q, &m); err != nil {
		return "", err
	}
	return m.text(), nil
}

// AroundTraffic 一个点周边此刻堵不堵. radius 百度上限 1000 米.
func (b *Baidu) AroundTraffic(ctx context.Context, lat, lon float64, radius int) (string, error) {
	if radius <= 0 || radius > 1000 {
		radius = 1000
	}
	q := url.Values{}
	q.Set("center", fmt.Sprintf("%.6f,%.6f", lat, lon))
	q.Set("radius", fmt.Sprint(radius))
	q.Set("coord_type_input", "wgs84")
	var m trafficResp
	if err := b.get(ctx, "/traffic/v1/around", "实时路况", q, &m); err != nil {
		return "", err
	}
	return m.text(), nil
}
