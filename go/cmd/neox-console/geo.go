package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"

	"github.com/neox-os/neox-os/osinit"
	"github.com/neox-os/neox-os/sense"
)

// geo 地图这一侧的全部能力 —— **一处解析地名, 一处取"他此刻在哪"**.
//
// ── 为什么收成一个 ──
//
//	原来是 sense.go 里五个各写各的闭包: 问路只认起过名的地方, 找地方
//	只列不用, 起名不看他在不在那儿, 看地图又只认起过名的. 每一个都
//	"对", 合起来就会出现这种情况 —— 他说"到凤凰国际广场要多久", 问路
//	说不认得; 模型退一步把"这儿"起名成凤凰国际广场(他人在家), 于是
//	"0 分钟"; 他骂完, 它才想起有个 find_place.
//
//	现在任何一处要一个地方, 都走 resolve: 起过名的 → 地图上搜名字 →
//	当地址查. 任何一处要"他此刻在哪", 都走 fix: 旧了就现问手机, 问不到
//	就把"多旧"说出来. 规矩只写一遍, 就不会有哪一处漏了.
type geo struct {
	places  *osinit.Places
	devices *osinit.Devices
	rgc     *sense.RGC
	api     *sense.Baidu
	// log 账本(内存那份) —— 兜底用. nil = 答不了
	log *osinit.EventLog
	// ledger 落盘账本的路径. **优先读它** —— 内存那份被 CompactSense
	// 压过, 只剩最近一小段(见 osinit.ScanEvents)
	ledger string
	// now 可换 —— 测试要拨钟
	now func() time.Time
}

func newGeo(places *osinit.Places, devices *osinit.Devices, ak string,
	log *osinit.EventLog, ledger string) *geo {
	return &geo{places: places, devices: devices, log: log, ledger: ledger,
		rgc: &sense.RGC{AK: ak}, api: &sense.Baidu{AK: ak}, now: time.Now}
}

// eachSignal 一条条过感知层的信号 —— 落盘那份优先, 没有就用内存那份.
func eachSignal(ledger string, log *osinit.EventLog, fn func(abi.Event) bool) {
	if ledger != "" {
		if err := osinit.ScanEvents(ledger, func(e abi.Event) bool {
			if e.PID != abi.ProcessID("sense") {
				return true
			}
			return fn(e)
		}); err == nil {
			return
		}
	}
	if log == nil {
		return
	}
	for _, e := range log.Replay(abi.ProcessID("sense"), 0) {
		if !fn(e) {
			return
		}
	}
}

// spot 一个解析好了的地方. 坐标一律 WGS-84(见 osinit/coord.go).
type spot struct {
	Name string
	Addr string
	Lat  float64
	Lon  float64
	// How 怎么认出来的: saved 起过名 / poi 地图搜到 / address 按地址 / here 他此刻
	How string
	// Age 只对 here 有意义: 那条位置多旧
	Age time.Duration
}

// label 说给人听的那个名字 —— 搜到的要带地址, 他才认得出是不是那一个.
func (s spot) label() string {
	switch s.How {
	case "poi", "address":
		if s.Addr != "" && s.Addr != s.Name {
			return fmt.Sprintf("%s（%s）", s.Name, s.Addr)
		}
	case "here":
		return s.Name + staleNote(s.Age)
	}
	return s.Name
}

// staleNote 位置多旧 —— **旧就必须说出来**. 三分钟以内的算此刻.
//
//	原来是一分半, 而人不动的时候手机四分钟才报一次: 19:58 他在一个小区里
//	停了二十多分钟, 位置两分钟前的, 它却被这句逼着说"可能你已经走开了".
//	开着车的时候手机 25 秒一报, 用不着这道闸.
func staleNote(age time.Duration) string {
	if age < 3*time.Minute {
		return ""
	}
	return fmt.Sprintf("（这是他 %s 前的位置, 刚现问手机没问到新的）",
		roughly(age.Milliseconds()))
}

// street 这个点在哪条路、哪栋楼里 —— **当场查**, 最多等 4 秒.
//
//	只在他这一轮在等答案的地方用(where / 问路 / 地图). 信号路径上的那个
//	是 rgc.Name, 不能等.
func (g *geo) street(lat, lon float64) (string, bool) {
	if g == nil || g.rgc == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return g.rgc.NameNow(ctx, lat, lon)
}

// fix 他此刻在哪.
//
//	**比 maxAge 旧就现问手机, 最多等 wait**. 采集端按变化触发(人没挪
//	120 米一条都不报), 所以手上那条可能是十分钟前的 —— 开着车的人
//	十分钟是五公里. 问不到就用手上那条, 并把它有多旧交给调用方.
func (g *geo) fix(maxAge, wait time.Duration) (lat, lon float64, age time.Duration, ok bool) {
	if g.places == nil {
		return 0, 0, 0, false
	}
	since := g.places.HereAt()
	if since == 0 || g.now().Sub(time.UnixMilli(since)) > maxAge {
		if g.devices != nil && g.devices.Ask("location", "") > 0 {
			g.places.WaitFresher(since, wait)
		}
	}
	lat, lon, ok = g.places.Here()
	if !ok {
		return 0, 0, 0, false
	}
	return lat, lon, g.now().Sub(time.UnixMilli(g.places.HereAt())), true
}

// here 他此刻在哪, 说成一个 spot.
func (g *geo) here(maxAge, wait time.Duration) (spot, error) {
	lat, lon, age, ok := g.fix(maxAge, wait)
	if !ok {
		return spot{}, fmt.Errorf("还不知道他在哪 —— 手机一次位置都没报过。**照实说**, 别猜")
	}
	name := g.places.Lookup(lat, lon)
	if name == "" {
		if near, got := g.street(lat, lon); got {
			name = near
		} else {
			name = "他现在的位置"
		}
	}
	return spot{Name: name, Lat: lat, Lon: lon, How: "here", Age: age}, nil
}

var hereWords = []string{"这儿", "这里", "我这", "当前位置", "现在的位置", "我现在的位置", "我", "here"}

// resolve 一个名字 → 一个地方.
//
//	顺序是**从他自己的说法到地图的说法**: 他起过名的("公司")最准, 因为
//	那是他站在那儿量出来的; 然后地图上按名字搜(凤凰国际广场、万达);
//	最后当成地址查(东海大道 128 号). 三条都不中才说找不到 ——
//	**绝不拿手边的坐标顶**, 否则查询一个未知地点也会被错误回答成当前位置.
func (g *geo) resolve(ctx context.Context, q string) (spot, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return spot{}, fmt.Errorf("去哪儿?")
	}
	for _, w := range hereWords {
		if q == w {
			return g.here(2*time.Minute, 6*time.Second)
		}
	}
	if s, ok := g.saved(q); ok {
		return s, nil
	}
	if !g.api.Ready() {
		return spot{}, fmt.Errorf("不认得「%s」, 而这台机器没配地图, 查不了。认得的只有: %s",
			q, g.knownNames())
	}
	// 地图上搜名字 —— 按他此刻的位置排, "万达"在一个市里有三个
	var nLat, nLon float64
	if lat, lon, ok := g.places.Here(); ok {
		nLat, nLon = osinit.WGS84ToGCJ02(lat, lon)
	}
	if got, err := g.rgc.Find(ctx, q, "", nLat, nLon); err == nil && len(got) > 0 {
		best := got[0]
		for _, p := range got {
			// 名字对得上的优先 —— 按距离排的第一个可能是"xx停车场出口"
			if p.Name == q {
				best = p
				break
			}
		}
		lat, lon := osinit.GCJ02ToWGS84(best.Lat, best.Lon)
		return spot{Name: best.Name, Addr: best.Address, Lat: lat, Lon: lon, How: "poi"}, nil
	}
	if gLat, gLon, err := g.rgc.Locate(ctx, q); err == nil {
		lat, lon := osinit.GCJ02ToWGS84(gLat, gLon)
		return spot{Name: q, Lat: lat, Lon: lon, How: "address"}, nil
	}
	return spot{}, fmt.Errorf("地图上没找到「%s」—— 问他是哪个城市、哪条路上的, 别猜", q)
}

// saved 起过名的地方. 先认全名, 再认"一个包含另一个"(他说"碧桂园",
// 起的名是"蚌埠碧桂园") —— 但只在**唯一**的时候认, 两个都沾边就不猜.
func (g *geo) saved(q string) (spot, bool) {
	if g.places == nil {
		return spot{}, false
	}
	var loose []osinit.Place
	for _, p := range g.places.Known() {
		if p.Name == q {
			return spot{Name: p.Name, Lat: p.Lat, Lon: p.Lon, How: "saved"}, true
		}
		if strings.Contains(p.Name, q) || strings.Contains(q, p.Name) {
			loose = append(loose, p)
		}
	}
	if len(loose) == 1 {
		p := loose[0]
		return spot{Name: p.Name, Lat: p.Lat, Lon: p.Lon, How: "saved"}, true
	}
	return spot{}, false
}

func (g *geo) knownNames() string {
	var names []string
	if g.places != nil {
		for _, p := range g.places.Known() {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return "（还没起过名的地方）"
	}
	return strings.Join(names, "、")
}

// route 从 from 到 to 怎么走、要多久、堵不堵. from 空 = 他此刻的位置.
func (g *geo) route(to, from, mode string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dst, err := g.resolve(ctx, to)
	if err != nil {
		return "", err
	}
	var src spot
	if strings.TrimSpace(from) == "" {
		// 起点**两分钟内**的才算此刻 —— 开车的人两分钟是一公里多
		src, err = g.here(2*time.Minute, 6*time.Second)
	} else {
		src, err = g.resolve(ctx, from)
	}
	if err != nil {
		return "", err
	}
	// ── 已经到了就说到了 ──
	//
	//	"到凤凰国际广场大约 0 分钟"就是从这儿出去的. 一个
	//	0 分钟的路线**不是答案**, 是"起点和终点是同一个点"的症状 ——
	//	说出来, 让它回头看是不是哪个地方记错了.
	if d := osinit.MetersBetween(src.Lat, src.Lon, dst.Lat, dst.Lon); d < 200 {
		return fmt.Sprintf("起点「%s」和终点「%s」只隔 %.0f 米 —— 他已经在那儿了。"+
			"要是他说还没到, 就是其中一个地方记错了: 用 find_place 核对, 别报一个零分钟的路程",
			src.label(), dst.label(), d), nil
	}
	routes, err := g.api.Route(ctx, src.Lat, src.Lon, dst.Lat, dst.Lon, sense.ParseMode(mode))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("从%s到%s:\n%s", src.label(), dst.label(), sense.Summary(routes)), nil
}

// traffic 一条路或者他周边此刻堵不堵. road 空或"1" = 他周边.
func (g *geo) traffic(road string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	road = strings.TrimSpace(road)
	if road == "" || road == "1" || road == "true" {
		h, err := g.here(5*time.Minute, 6*time.Second)
		if err != nil {
			return "", err
		}
		out, err := g.api.AroundTraffic(ctx, h.Lat, h.Lon, 1000)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s周边一公里:\n%s", h.label(), out), nil
	}
	// 路名前面带了城市("合肥长江路")就用它的; 否则按他所在的城市
	city := ""
	if lat, lon, ok := g.places.Here(); ok {
		city, _ = g.rgc.City(ctx, lat, lon)
	}
	if i := strings.IndexAny(road, " 　"); i > 0 {
		city, road = road[:i], strings.TrimSpace(road[i:])
	}
	if city == "" {
		return "", fmt.Errorf("不知道「%s」在哪个城市 —— 问他一句, 或者写成「城市 路名」", road)
	}
	return g.api.RoadTraffic(ctx, road, city)
}

// weather 某处今天明天的预报. where 空或"1" = 他那儿.
func (g *geo) weather(where string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	where = strings.TrimSpace(where)
	var lat, lon float64
	if where == "" || where == "1" || where == "true" {
		// 天气半小时内的位置就够 —— 不值得为它把手机叫醒
		h, err := g.here(30*time.Minute, 3*time.Second)
		if err != nil {
			return "", err
		}
		lat, lon = h.Lat, h.Lon
	} else if s, ok := g.saved(where); ok {
		lat, lon = s.Lat, s.Lon
	} else {
		// 城市名的置信度只有二三十 —— 查天气落到那个城市里就够
		gLat, gLon, err := g.rgc.Geocode(ctx, where, 0)
		if err != nil {
			return "", fmt.Errorf("不知道「%s」在哪: %w", where, err)
		}
		lat, lon = osinit.GCJ02ToWGS84(gLat, gLon)
	}
	return g.api.Forecast(ctx, lat, lon)
}

// nameHere 把他此刻站的地方起名 —— **先确认他真在那儿**.
//
// ── 为什么必须先确认 ──
//
//	08:06 他在家, 说"我到凤凰国际广场 8:30 到就行, 帮我看看要多久".
//	模型调了 place(name=凤凰国际广场) —— 于是他家门口被记成了他单位,
//	接着"到凤凰国际广场 0 分钟". 工具照做了, 因为它从来不问一句
//	"他现在真的站在凤凰国际广场吗".
//
//	两道闸, 都便宜:
//	  ① 他此刻在另一个起过名的地方里 → 不是这儿(他在家就不在单位)
//	  ② 地图上这个名字在一公里之外 → 不是这儿(除非他亲口说"就是这儿")
//	sure=1 越过这两道 —— 那是他明确说了"我就站在这儿"的时候.
func (g *geo) nameHere(name string, radius float64, sure bool) (string, error) {
	// "这儿"必须是此刻: 三分钟内的才认, 旧了现问
	h, err := g.here(3*time.Minute, 6*time.Second)
	if err != nil {
		return "", err
	}
	// **他亲口说"就是这儿"的时候, 这道闸也该让开**: sure 本来就是
	// 这个的出口(下面那两道都认它), 而时效这道却不认 —— 真机测试里他说
	// "不管你怎么认, 这儿就是姥姥家", 位置才 4 分钟旧, 照样被拒.
	// 放宽到半小时: 再旧就真不是"这儿"了, 那时候该问他要地址
	limit := 3 * time.Minute
	if sure {
		limit = 30 * time.Minute
	}
	if h.Age > limit {
		return "", fmt.Errorf("手机最新的位置是 %s 前的, 当不了「这儿」 —— "+
			"给个地址或者店名, 我照着记", roughly(h.Age.Milliseconds()))
	}
	if !sure {
		if other := g.places.Lookup(h.Lat, h.Lon); other != "" && other != name {
			return "", fmt.Errorf("他此刻在「%s」的范围里, 这儿不是「%s」。"+
				"他说的是别处就先 find_place 查到, 再用 place 带 address 记; "+
				"他明确说「就是这儿」才加 sure=1", other, name)
		}
		if g.api.Ready() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			gLat, gLon := osinit.WGS84ToGCJ02(h.Lat, h.Lon)
			if got, err := g.rgc.Find(ctx, name, "", gLat, gLon); err == nil {
				for _, p := range got {
					if p.Name != name {
						continue
					}
					lat, lon := osinit.GCJ02ToWGS84(p.Lat, p.Lon)
					limit := 3 * radius
					if limit < 1000 {
						limit = 1000
					}
					if d := osinit.MetersBetween(h.Lat, h.Lon, lat, lon); d > limit {
						return "", fmt.Errorf("地图上的「%s」在 %s, 离他此刻 %.1f 公里 —— 他人不在那儿。"+
							"要记那个就 place(name, address=%s); 他明确说「就是这儿」才加 sure=1",
							name, p.Address, d/1000, p.Address)
					}
					break
				}
			}
		}
	}
	pl, err := g.places.Add(name, h.Lat, h.Lon, radius)
	if err != nil {
		return "", err
	}
	where := ""
	if near, ok := g.street(h.Lat, h.Lon); ok {
		where = near + ", "
	}
	// **把圈多大、在哪儿都说出来**: 他家是个小区还是一间屋, 只有他知道 ——
	// 说出来他才有机会说"太小了"或者"不是这儿"
	return fmt.Sprintf("记下了: 这儿(%s手机 %s前的位置)叫「%s」, 方圆 %.0f 米内都算",
		where, ageWords(h.Age), pl.Name, pl.Radius) + g.neighbors(pl), nil
}

func ageWords(age time.Duration) string {
	if age < time.Minute {
		return "刚刚"
	}
	return roughly(age.Milliseconds())
}

// byAddress "我上班在xx大厦" —— 他可以在家里说这句话.
//
//	先当地址查; 查不准再当名字搜 —— 人说的"地址"一半其实是个名字
//	("蚌埠碧桂园淮上"是个小区名, 不是门牌)
func (g *geo) byAddress(name, address string, radius float64) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var lat, lon float64
	gLat, gLon, err := g.rgc.Locate(ctx, address)
	if err == nil {
		lat, lon = osinit.GCJ02ToWGS84(gLat, gLon)
	} else if s, ferr := g.resolve(ctx, address); ferr == nil && s.How == "poi" {
		lat, lon = s.Lat, s.Lon
		address = s.label()
	} else {
		return "", err
	}
	// **存进去的必须是 WGS-84**: 百度回的是 GCJ-02, 差 575 米 ——
	// 存错了的话"到公司了"永远不会成立, 而且不会报错
	pl, err := g.places.Add(name, lat, lon, radius)
	if err != nil {
		return "", err
	}
	// **圈多大也要说出来, 而且要说真的那个** —— 上一版把 radius 默默吞了
	return fmt.Sprintf("记下了: 「%s」= %s, 方圆 %.0f 米内都算", pl.Name, address, pl.Radius) +
		g.neighbors(pl), nil
}

// forget 删掉一个起过名的地方.
func (g *geo) forget(name string) (string, error) {
	if g.places == nil || !g.places.Forget(name) {
		return "", fmt.Errorf("没有叫「%s」的地方。认得的: %s", name, g.knownNames())
	}
	return fmt.Sprintf("删掉了「%s」。现在认得: %s", name, g.knownNames()), nil
}

// neighbors 圈跟它叠着的别的地方 —— **只报事实, 怎么办由它定**.
//
//	"公司"和"凤凰国际广场"隔 190 米, 是同一栋楼的两个名字.
//	它其实知道(它说"在公司，凤凰国际广场"), 只是从来没有一处把这件事
//	摆到它眼前 —— 于是没人收拾, 最后被推回给了他.
func (g *geo) neighbors(p osinit.Place) string {
	if g.places == nil {
		return ""
	}
	var near []string
	for _, o := range g.places.Known() {
		if o.Name == p.Name {
			continue
		}
		if d := osinit.MetersBetween(p.Lat, p.Lon, o.Lat, o.Lon); d < p.Radius+o.Radius {
			near = append(near, fmt.Sprintf("「%s」只隔 %.0f 米", o.Name, d))
		}
	}
	if len(near) == 0 {
		return ""
	}
	return "（跟" + strings.Join(near, "、") + "，圈是叠着的）"
}

// list 认得的地方, 每个带上在哪、跟谁叠着 —— where(places=1) 用.
func (g *geo) list() string {
	if g.places == nil {
		return "这台机器还不认得任何地方。"
	}
	known := g.places.Known()
	if len(known) == 0 {
		return "还没教过它任何地方。到了某处跟它说一句「这儿是家」就行。"
	}
	var b strings.Builder
	for _, p := range known {
		fmt.Fprintf(&b, "- %s（方圆 %.0f 米", p.Name, p.Radius)
		if addr, ok := g.street(p.Lat, p.Lon); ok {
			b.WriteString("，" + addr)
		}
		b.WriteString("）" + g.neighbors(p) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// find 按名字查地方, 列出来让他认 —— "万达"在一个市里有三个.
func (g *geo) find(query, city string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var gLat, gLon float64
	hLat, hLon, here := g.places.Here()
	if here && city == "" {
		gLat, gLon = osinit.WGS84ToGCJ02(hLat, hLon)
	}
	got, err := g.rgc.Find(ctx, query, city, gLat, gLon)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, p := range got {
		if i >= 5 {
			break
		}
		lat, lon := osinit.GCJ02ToWGS84(p.Lat, p.Lon)
		fmt.Fprintf(&b, "- %s | %s | %.6f,%.6f", p.Name, p.Address, lat, lon)
		if here {
			fmt.Fprintf(&b, " | 离他 %.1f 公里", osinit.MetersBetween(hLat, hLon, lat, lon)/1000)
		}
		b.WriteString("\n")
	}
	b.WriteString("（要记成一个地方就用 place 带 address；问怎么去直接 where to=名字）")
	return b.String(), nil
}

// card 给他看一张地图 —— **卡片走 ui 通道那条现成的路**(见 agent/show.go).
// place 空 = 他此刻在哪; 否则任何 resolve 得出来的地方都行.
func (g *geo) card(place string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var s spot
	var err error
	if place == "" {
		s, err = g.here(time.Minute, 3*time.Second)
	} else {
		s, err = g.resolve(ctx, place)
	}
	if err != nil {
		return nil, err
	}
	title, sub := s.Name, s.Addr
	if s.How == "here" {
		title = "我现在在这儿"
		if name := g.places.Lookup(s.Lat, s.Lon); name != "" {
			title = name
		}
	}
	if sub == "" {
		if near, got := g.street(s.Lat, s.Lon); got {
			sub = near
		}
	}
	return map[string]any{"type": "map", "title": title, "sub": sub,
		"lat": s.Lat, "lon": s.Lon}, nil
}

// ── 他今天去过哪儿 ──
//
//	账本里躺着每一条位置信号, 而它原来够不着: 为了回答这类问题
//	去 run grep events.jsonl, 27 次 —— 拿回来的是原始 JSON, 读不准,
//	而且每次都要现写一段 python.
//
//	这不是"再加一个工具", 是**把已经有的数据接出来**: 谁都答得上
//	"我今天去过哪儿", 除了手里攥着全部位置记录的那一个.

// trailStep 两条位置离多远算换了地方. 比命名的圈小一号 —— 圈是"算不算
// 到了", 这个是"是不是同一次停留"
const trailStep = 200.0

// trailMin 待多久才算一次停留. 短的是等红灯、路过
const trailMin = 10 * time.Minute

// trailSilence 一条孤零零的位置之后静了多久, 才当他是在那儿待着.
//
//	**"沉默即停留"要分两种沉默**: 人不动时采集端不报(那是真的待着),
//	而开着车的时候它 25 秒一条 —— 路上随手一条定位, 后面 40 分钟没信号,
//	按沉默算就成了"他在路口待了 40 分钟". 所以: 自己这一簇里前后
//	差够久才算待过; 只有一条的, 要后面静够久(多半是手机睡了或人停了)
const trailSilence = 90 * time.Minute

// trailNames 最多现查几个地名 —— 每次是一趟网络往返, 而他在等这句话
const trailNames = 5

// trail 某一天他在哪儿待过. day: 空/今天/昨天/09-11/2026-09-11.
func (g *geo) trail(day string) (string, error) {
	if g.log == nil || g.places == nil {
		return "", fmt.Errorf("这台机器没接感知层, 没有位置记录")
	}
	from, to, label, err := dayRange(day, g.now())
	if err != nil {
		return "", err
	}
	pts := g.pointsIn(from, to)
	if len(pts) == 0 {
		return fmt.Sprintf("%s账本里没有位置记录 —— 那天手机可能没上报。", label), nil
	}
	// **有点但凑不成停留, 要说"只有几条", 不能说"没有"**: 真机测试里
	// 当天只有一条定位, 它答"账本里是空的", 然后拿一条微信里的地名
	// (那是他爸说的)当成了他的行踪
	if len(pts) < 2 {
		name := g.places.Lookup(pts[0].lat, pts[0].lon)
		if name == "" {
			if near, ok := g.street(pts[0].lat, pts[0].lon); ok {
				name = near
			} else {
				name = "一个没起过名的地方"
			}
		}
		return fmt.Sprintf("%s只有一条位置记录: %s 在%s。凑不出行程。",
			label, time.UnixMilli(pts[0].at).Format("15:04"), name), nil
	}
	stays := clusterStays(pts)
	var b strings.Builder
	fmt.Fprintf(&b, "%s他待过的地方(按账本里的位置信号还原):\n", label)
	named, n := 0, 0
	for _, s := range stays {
		if !s.stayed() {
			continue
		}
		name := g.places.Lookup(s.lat, s.lon)
		if name == "" && named < trailNames {
			if near, ok := g.street(s.lat, s.lon); ok {
				name, named = near, named+1
			}
		}
		if name == "" {
			name = "一个没起过名的地方"
		}
		fmt.Fprintf(&b, "- %s–%s %s\n",
			time.UnixMilli(s.from).Format("15:04"),
			time.UnixMilli(s.to).Format("15:04"), name)
		n++
	}
	if n == 0 {
		return fmt.Sprintf("%s他没在哪儿待满 %s —— 一直在动。",
			label, roughly(int64(trailMin/time.Millisecond))), nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// trailPoint 账本里的一条位置
type trailPoint struct {
	at       int64
	lat, lon float64
}

// trailStay 一次停留. span 是这一簇自己的头尾, to 可能因为后面的沉默而延长
type trailStay struct {
	from, to int64
	span     int64
	lat, lon float64
}

// stayed 这一簇算不算"在那儿待过" —— 见 trailSilence.
func (s trailStay) stayed() bool {
	if s.span >= int64(trailMin/time.Millisecond) {
		return true
	}
	return s.to-s.from >= int64(trailSilence/time.Millisecond)
}

// pointsIn 这段时间里的位置信号, 按时间排好
func (g *geo) pointsIn(from, to int64) []trailPoint {
	var pts []trailPoint
	eachSignal(g.ledger, g.log, func(e abi.Event) bool {
		if e.Kind != abi.EvSignal {
			return true
		}
		p, ok := e.Payload.(map[string]any)
		if !ok {
			return true
		}
		body, _ := p["body"].(map[string]any)
		if body == nil {
			return true
		}
		lat, lon := numOf(body["lat"]), numOf(body["lon"])
		if lat == 0 && lon == 0 {
			return true
		}
		// **用信号自己的时刻**: 补传上来的那些, 收到的时间比发生的晚几小时
		at := int64(numOf(p["at"]))
		if at == 0 {
			at = e.At
		}
		if at < from || at >= to {
			return true
		}
		pts = append(pts, trailPoint{at: at, lat: lat, lon: lon})
		return true
	})
	sort.Slice(pts, func(i, j int) bool { return pts[i].at < pts[j].at })
	return pts
}

// clusterStays 把一串位置并成几次停留.
//
//	**沉默即停留** —— 跟 osinit/routine.go 同一条道理: 人不动的时候
//	采集端不报, 所以"离开"不是一个事件, 是下一个地方的第一条信号.
func clusterStays(pts []trailPoint) []trailStay {
	var out []trailStay
	for _, p := range pts {
		if len(out) > 0 {
			cur := &out[len(out)-1]
			if osinit.MetersBetween(cur.lat, cur.lon, p.lat, p.lon) <= trailStep {
				cur.to, cur.span = p.at, p.at-cur.from
				continue
			}
			// 换地方了: 上一次延到此刻为止, 但**自己那一簇有多长是另一回事**
			cur.to = p.at
		}
		out = append(out, trailStay{from: p.at, to: p.at, lat: p.lat, lon: p.lon})
	}
	return out
}

// dayRange 哪一天. 返回这一天的起止和一句抬头.
func dayRange(day string, now time.Time) (from, to int64, label string, err error) {
	day = strings.TrimSpace(day)
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch day {
	case "", "1", "true", "今天", "today":
		label = "今天"
	case "昨天", "yesterday":
		d, label = d.AddDate(0, 0, -1), "昨天"
	default:
		var t time.Time
		for _, layout := range []string{"2006-01-02", "01-02", "1-2"} {
			if t, err = time.ParseInLocation(layout, day, now.Location()); err == nil {
				if t.Year() == 0 {
					t = t.AddDate(now.Year(), 0, 0)
				}
				d, label = t, t.Format("01-02")
				break
			}
		}
		if err != nil {
			return 0, 0, "", fmt.Errorf("看不懂「%s」是哪天 —— 写今天/昨天, 或者 09-11", day)
		}
	}
	return d.UnixMilli(), d.AddDate(0, 0, 1).UnixMilli(), label, nil
}

// numOf 账本里的数可能是 float64, 也可能是 json.Number 或者整数
func numOf(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}
