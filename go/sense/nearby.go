package sense

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 这儿大概是哪儿 —— 逆地理编码, **只在说不出名字的时候兜底**.
//
// ── 它不是"给地点起名"的替代品 ──
//
//	"这儿是公司"是一个**关系**, 而"安徽省蚌埠市蚌山区淮河社区
//	G206(东海大道)"只是一个地址. 判断要用的是前者:
//	"他到公司了"能推出很多事, "他到东海大道了"什么都推不出来.
//
//	所以命名那条路(name_place)不能被这个取代. 这个解决的是另一件事:
//	**在他还没起名之前, 别说"一个还没起过名的地方"** —— 那句话对用户
//	是零信息, 而且看起来像系统坏了.
//
// ── 绝对不能在信号路径上同步调 ──
//
//	描述一条信号发生在总线的观察链里, 那条链是同步的. 在那儿发一个
//	HTTP 请求的话, **每一条位置信号都要等一次网络往返** ——
//	而手机补传时是一次几百条.
//
//	所以: 查缓存, 没有就返回"不知道"并**在后台**去查一次. 下一次就有了.
//	反正位置这种事本来就不急在这 200 毫秒.
//
// ── 省着用 ──
//
//	个人配额是 300 次/天. 按 110 米一格缓存(跟采集端 120 米的移动门槛
//	对得上), 一个不动的手机一天只花一次. 再加一道日上限兜底 ——
//	配额用光之后百度回的是一个状态码, 而那时候整条路会静默地退回
//	"不知道".

const nearbyGrid = 1000.0 // 小数点后三位 ≈ 110 米

// maxStaticSide 静态图一边最多多少像素. **scale=2 之下是 512 不是 1024** ——
// 超了百度不回图, 回一段 JSON
const maxStaticSide = 512

// RGC 逆地理编码, 带缓存和日上限.
type RGC struct {
	AK   string
	HTTP *http.Client
	Base string
	// Cap 一天最多查几次. 0 = 用缺省
	Cap int

	mu       sync.Mutex
	cache    map[string]string
	inflight map[string]bool
	day      string
	used     int
}

// Name 这儿叫什么. **只读缓存, 从不阻塞**.
//
//	没有就返回 false, 并在后台去查一次 —— 见开头那段.
func (g *RGC) Name(lat, lon float64) (string, bool) {
	if g == nil || g.AK == "" {
		return "", false
	}
	key := gridKey(lat, lon)
	g.mu.Lock()
	if g.cache == nil {
		g.cache = map[string]string{}
		g.inflight = map[string]bool{}
	}
	if name, ok := g.cache[key]; ok {
		g.mu.Unlock()
		// **空串也是一个答案**: 查过了但那儿什么都没有(海上、荒地).
		// 不记的话每次都会再查一遍, 而配额就耗在这上面
		return name, name != ""
	}
	if g.inflight[key] || !g.spendLocked() {
		g.mu.Unlock()
		return "", false
	}
	g.inflight[key] = true
	g.mu.Unlock()

	go g.fetch(key, lat, lon)
	return "", false
}

// NameNow 同 Name, 但**缓存里没有就当场查一次** —— 只给"他此刻问我在哪"用.
//
//	Name 不阻塞是对的: 它跑在信号路径上. 但他问"我在哪条路"的时候,
//	那一轮本来就在等 —— 19:56 他在固镇的一个小区里, 后台那次还没查回来,
//	工具只给得出"一个还没起过名的地方", 于是它答
//	"公司和家都对不上", 而地图卡片上明明写着庙岗路.
func (g *RGC) NameNow(ctx context.Context, lat, lon float64) (string, bool) {
	if g == nil || g.AK == "" {
		return "", false
	}
	key := gridKey(lat, lon)
	g.mu.Lock()
	if g.cache == nil {
		g.cache = map[string]string{}
		g.inflight = map[string]bool{}
	}
	if name, ok := g.cache[key]; ok {
		g.mu.Unlock()
		return name, name != ""
	}
	if !g.spendLocked() {
		g.mu.Unlock()
		return "", false
	}
	g.mu.Unlock()
	name, err := g.lookup(ctx, lat, lon)
	if err != nil {
		return "", false
	}
	g.mu.Lock()
	g.cache[key] = name
	g.mu.Unlock()
	return name, name != ""
}

// spendLocked 今天还能查吗 —— 调用方已经持锁
func (g *RGC) spendLocked() bool {
	today := time.Now().Format("2006-01-02")
	if g.day != today {
		g.day, g.used = today, 0
	}
	cap := g.Cap
	if cap <= 0 {
		// 个人配额 300/天, 留一截余量给别的服务和手动调试
		cap = 200
	}
	if g.used >= cap {
		return false
	}
	g.used++
	return true
}

func (g *RGC) fetch(key string, lat, lon float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	name, err := g.lookup(ctx, lat, lon)
	g.mu.Lock()
	delete(g.inflight, key)
	if err == nil {
		g.cache[key] = name
	}
	g.mu.Unlock()
}

type rgcResp struct {
	Status int    `json:"status"`
	Msg    string `json:"message"`
	Result struct {
		Address   string `json:"formatted_address"`
		Sematic   string `json:"sematic_description"`
		Component struct {
			District string `json:"district"`
			Street   string `json:"street"`
			City     string `json:"city"`
		} `json:"addressComponent"`
	} `json:"result"`
}

// api 这一侧的百度出口 —— 见 baidu.go. Base 在这里是**主机**(测试塞假服务器)
func (g *RGC) api() *Baidu { return &Baidu{AK: g.AK, HTTP: g.HTTP, Host: g.Base} }

// lookup 真去问一次
func (g *RGC) lookup(ctx context.Context, lat, lon float64) (string, error) {
	q := url.Values{}
	// 手机报的就是 WGS-84 —— 让百度自己转, 别在我们这侧转:
	// 多一处转换就多一处会跟内部存的那份不一致的地方
	q.Set("coordtype", "wgs84ll")
	q.Set("location", fmt.Sprintf("%.6f,%.6f", lat, lon))
	// **不带这个就没有"在哪栋楼里"**: sematic_description 只在要了 POI 时
	// 才回. 没有它, 他问"我在什么商业楼里", 能说的只有"蚌山区东海大道"
	q.Set("extensions_poi", "1")
	var m rgcResp
	if err := g.api().get(ctx, "/reverse_geocoding/v3/", "逆地理编码", q, &m); err != nil {
		return "", err
	}
	return shortAddress(m), nil
}

// shortAddress 说成一句短的.
//
//	**不要那串完整地址**: "安徽省蚌埠市蚌山区淮河社区G206(东海大道)"
//	里, 对判断有用的只有最后那一小截. 全串进上下文的话, 一天几十条
//	位置事实就是几千个字的省市区 —— 而那正是"把一条流塞进上下文,
//	模型看到的是噪音的海"要避免的.
//
//	**路名和楼都要**: 他问的就是"在哪条路边、哪栋楼里". 形状是
//	"固镇县庙岗路，汇金国际碧桂苑内" —— 区+路, 再加语义描述的第一截
//	(后半截是"xx服务站西南184米"这种参照物, 对他没用).
func shortAddress(m rgcResp) string {
	c := m.Result.Component
	road := ""
	if d := strings.TrimSpace(c.District); d != "" {
		road = d + roadName(c.Street)
	}
	near := strings.TrimSpace(m.Result.Sematic)
	if i := strings.IndexAny(near, ",，"); i > 0 {
		near = near[:i]
	}
	// **把"附近7米"那个尾巴去掉**: 百度给的是"大润发附近7米", 而人说
	// "在大润发那儿" —— 那个米数既不准也没人关心, 念出来还别扭
	near = trimMeters(near)
	switch {
	case road != "" && near != "":
		return road + "，" + near
	case road != "":
		return road
	case near != "":
		return near
	}
	// 兜底: 把省市砍掉, 只留后半截
	addr := strings.TrimSpace(m.Result.Address)
	if i := strings.Index(addr, "市"); i > 0 && i+3 < len(addr) {
		return addr[i+3:]
	}
	return addr
}

// trimMeters "大润发附近7米" → "大润发附近"; "xx西南184米" → "xx西南".
//
//	反查给的距离是到那个 POI 中心的直线距离, 本身就有几十米的误差 ——
//	留着它是在一个不准的数上装精确
func trimMeters(s string) string {
	t := strings.TrimSuffix(s, "米")
	if t == s {
		return s
	}
	i := len(t)
	for i > 0 && t[i-1] >= '0' && t[i-1] <= '9' {
		i--
	}
	if i == len(t) || i == 0 {
		return s
	}
	return t[:i]
}

// roadName "G206(东海大道)" → "东海大道": 没人管东海大道叫 G206
func roadName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "("); i > 0 && strings.HasSuffix(s, ")") {
		if in := s[i+1 : len(s)-1]; in != "" {
			return in
		}
	}
	return s
}

// gridKey 按格子缓存 —— 110 米一格, 跟采集端 120 米的移动门槛对得上:
// 一个不动的手机一天只花一次配额
func gridKey(lat, lon float64) string {
	return fmt.Sprintf("%.0f,%.0f",
		math.Round(lat*nearbyGrid), math.Round(lon*nearbyGrid))
}

// ── 地址 → 坐标 ──
//
//	跟 name_place 的分工: 那个记的是"我**此刻**站的地方", 要人真的在那儿;
//	这个记的是"我上班在合肥市xx大厦" —— 他可以在家里说这句话.
//
//	**两条都要有**: 第一条更准(坐标是设备量出来的), 第二条更常用
//	(人想起要交代这件事的时候, 通常不在那儿).

type geoResp struct {
	Status int    `json:"status"`
	Msg    string `json:"message"`
	Result struct {
		Location struct {
			Lng float64 `json:"lng"`
			Lat float64 `json:"lat"`
		} `json:"location"`
		// Precise 1 = 精确匹配到门牌. 0 = 模糊(只匹配到街道甚至城市)
		Precise int `json:"precise"`
		// Confidence 0-100. 低的多半是地址写得太糊
		Confidence int `json:"confidence"`
	} `json:"result"`
}

// Locate 把一句地址变成坐标(WGS-84).
//
//	返回的是 WGS-84 而不是百度那套 —— **内部一律 WGS-84**(见
//	osinit/coord.go): 存一个 BD-09 进地点表的话, 它跟手机报上来的
//	坐标差一公里, 而"到家了"这件事就永远不会成立.
func (g *RGC) Locate(ctx context.Context, address string) (lat, lon float64, err error) {
	return g.Geocode(ctx, address, 60)
}

// Geocode 同 Locate, 但**多糊才拒**由调用方定.
//
//	记地点要严(见 Locate 那段: 糊的坐标存进去, "到公司了"会在路上成立);
//	查天气可以松 —— "合肥明天下不下雨"只要落到合肥市里就够了, 而一个
//	城市名的置信度本来就只有二三十.
func (g *RGC) Geocode(ctx context.Context, address string, minConfidence int) (lat, lon float64, err error) {
	if g == nil || g.AK == "" {
		return 0, 0, fmt.Errorf("没配地图 key(NEOX_BAIDU_AK)")
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return 0, 0, fmt.Errorf("地址是空的")
	}
	q := url.Values{}
	q.Set("address", address)
	// **要 GCJ-02 回来**: 不指定的话百度回的是 BD-09, 而那跟手机报的
	// 差一公里 —— 存进去之后"到家了"永远不会成立, 且不会报错
	q.Set("ret_coordtype", "gcj02ll")
	var m geoResp
	if err := g.api().get(ctx, "/geocoding/v3/", "地理编码", q, &m); err != nil {
		if strings.Contains(err.Error(), "说:") {
			return 0, 0, fmt.Errorf("查不到这个地址: %w", err)
		}
		return 0, 0, err
	}
	// **糊的地址要拒**, 不能收下.
	//
	//	置信度低通常意味着它只匹配到了区甚至市 —— 那个坐标离真正的
	//	目的地可能有几公里. 收下的话, "到公司了"会在他还在路上时成立,
	//	而且再也没人会怀疑那个坐标.
	if m.Result.Confidence < minConfidence {
		return 0, 0, fmt.Errorf(
			"「%s」这个地址太糊(置信度 %d) —— 写详细一点, 或者先用 find_place 按名字查",
			address, m.Result.Confidence)
	}
	// 百度回的是 GCJ-02(我们要的), 再转成 WGS-84 由调用方做 ——
	// 这个包不能 import osinit(会成环), 转换在那边
	return m.Result.Location.Lat, m.Result.Location.Lng, nil
}

// StaticMap 一张静态地图图片 —— **微信那种位置卡片上的缩略图**.
//
//	返回 PNG 的字节. 由 OS 去取而不是让手机直接取, 有两个理由,
//	而且两个都是硬的:
//
//	  **AK 不该落到手机上**   落了就是发出去了 —— 装到多少台设备上,
//	                          就有多少个地方能把它抠出来
//	  **IP 白名单**           服务端 key 通常绑了出口 IP, 而手机的 IP
//	                          每换一次基站就变一个
func (g *RGC) StaticMap(ctx context.Context, lat, lon float64, w, h int) ([]byte, error) {
	if g == nil || g.AK == "" {
		return nil, fmt.Errorf("没配地图 key(NEOX_BAIDU_AK)")
	}
	// **scale=2 的时候上限减半**(百度那边是 512, 不是 1024).
	//
	//	超了它不回图, 回一段 `{"message":"width : 超出范围"}` ——
	//	而那段 JSON 长得像一张损坏的图片: 不认出来的话, 手机上那张卡里
	//	会挂一个永远转圈的图, 没有任何一处说得出为什么.
	//	(下面那道"回的是 JSON 就报错"的闸就是这么发现这件事的)
	if w <= 0 || w > maxStaticSide {
		w = 512
	}
	if h <= 0 || h > maxStaticSide {
		h = 256
	}
	base := strings.TrimRight(g.Base, "/")
	if base == "" {
		base = baiduHost
	}
	base += "/staticimage/v2"
	q := url.Values{}
	q.Set("ak", g.AK)
	q.Set("width", fmt.Sprint(w))
	q.Set("height", fmt.Sprint(h))
	q.Set("zoom", "16")
	// 静态图接口**只认百度自己那套坐标**, 没有 coord_type 这个参数 ——
	// 所以这一处必须在我们这侧转. 不转的话图上那个点偏一公里,
	// 而图**看起来完全正常**(它就是另一个地方的一张正常地图)
	q.Set("center", fmt.Sprintf("%.6f,%.6f", lon, lat))
	q.Set("markers", fmt.Sprintf("%.6f,%.6f", lon, lat))
	q.Set("markerStyles", "l,A")
	// 高清屏上 1 倍图是糊的
	q.Set("scale", "2")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	cli := g.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("地图图片接口回了 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	// **出错的时候它回的是一段 JSON, 不是图片**. 不认出来的话, 手机上
	// 那张卡里会挂一个永远加载不出来的图, 而没有任何一处会说为什么
	if len(raw) > 0 && raw[0] == '{' {
		return nil, fmt.Errorf("地图图片接口回了一段错误: %s", trimTo(raw, 120))
	}
	return raw, nil
}

func trimTo(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// Place 查到的一个地方.
type Place struct {
	Name    string
	Address string
	Lat     float64
	Lon     float64
	// Meters 离他多远. 0 = 不知道他在哪, 没法算
	Meters float64
}

// 注意: Lat/Lon 是**百度回来的 GCJ-02**, 不是 WGS-84.
//
//	**转换留给调用方**: sense 这一层不能 import osinit(那边已经
//	import 了这边, 反过来就循环了), 而坐标系转换在 osinit/coord.go。
//
//	不转的后果是差 575 米 —— 存进去之后"到公司了"永远不会成立,
//	而且一次都不报错; 这会让 rememberPlace 静默失效。

// Find 按**名字**找地方 —— "凤凰国际广场在哪"。
//
// ── 为什么 Locate 不够 ──
//
//	Locate 走的是地理编码(geocoding), 它要的是**一个地址**:
//	"蚌埠市蚌山区东海大道 128 号"。而人说的是**名字**:
//	"凤凰国际广场"、"万达"、"我家楼下那个华润万家"。
//
//	拿名字去问地理编码, 回来的是一个置信度很低的点(它只匹配到了区),
//	而那个点离真正的目的地可能有几公里: 它可能把
//	"凤凰国际广场"记成了用户家附近的坐标, 然后一本正经地用它算路程。
//
//	POI 检索(place/v2/search)才是回答"这地方在哪"的那条路。
//
// ── near 给了就按距离排 ──
//
//	"万达"在一个市里有三个。他问的永远是离他最近的那个 —— 而不给
//	参照点的话, 返回顺序是百度按热度排的, 第一个可能在城市另一头。
//
// nearLat/nearLon 也是 GCJ-02(0 = 不知道他在哪).
func (g *RGC) Find(ctx context.Context, query, region string,
	nearLat, nearLon float64) ([]Place, error) {
	if g == nil || g.AK == "" {
		return nil, fmt.Errorf("没配地图 key(NEOX_BAIDU_AK)")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("要找什么?")
	}
	q := url.Values{}
	q.Set("query", query)
	// scope=2 才带回详细信息; ret_coordtype 跟 Locate 一致 ——
	// **两条路必须回同一种坐标系**, 否则同一个地方存进去是两个点
	q.Set("scope", "2")
	q.Set("ret_coordtype", "gcj02ll")
	if nearLat != 0 || nearLon != 0 {
		// 按他所在的位置找 —— "万达"在一个市里有三个, 他问的永远是
		// 离他最近的那个.
		//
		//	**coord_type=2 一定要带**: 不带的话百度把这个点当成它自己
		//	那套 BD-09, 参照点就偏了七八百米 —— 排序照样出来, 只是按
		//	一个错的点排的, 看不出来
		q.Set("location", fmt.Sprintf("%.6f,%.6f", nearLat, nearLon))
		q.Set("coord_type", "2")
		q.Set("radius", "50000")
		q.Set("radius_limit", "false")
	} else if strings.TrimSpace(region) != "" {
		q.Set("region", region)
	} else {
		q.Set("region", "全国")
	}
	var m struct {
		Results []struct {
			Name     string `json:"name"`
			Address  string `json:"address"`
			Location struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			} `json:"location"`
		} `json:"results"`
	}
	if err := g.api().get(ctx, "/place/v2/search", "地点检索", q, &m); err != nil {
		return nil, err
	}
	out := make([]Place, 0, len(m.Results))
	for _, r := range m.Results {
		if r.Name == "" || (r.Location.Lat == 0 && r.Location.Lng == 0) {
			continue
		}
		// **回的是 GCJ-02, 转换由调用方做** —— 见 Place 那段
		p := Place{Name: r.Name, Address: r.Address,
			Lat: r.Location.Lat, Lon: r.Location.Lng}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没找到「%s」—— 换个说法, 或者加上城市名", query)
	}
	return out, nil
}

// City 这个点在哪个城市 —— **同步问**, 按 0.1° 一格(约 10 公里)缓存.
//
//	给"朝阳路堵不堵"用: 全国叫朝阳路的有几百条, 路况接口必须带城市,
//	而他不会说"蚌埠的朝阳路" —— 他人就在蚌埠. 城市一天不会变几次,
//	所以格子可以粗, 一天花不了几次配额.
func (g *RGC) City(ctx context.Context, lat, lon float64) (string, error) {
	if g == nil || g.AK == "" {
		return "", fmt.Errorf("没配地图 key(NEOX_BAIDU_AK)")
	}
	key := fmt.Sprintf("city:%.1f,%.1f", lat, lon)
	g.mu.Lock()
	if g.cache == nil {
		g.cache = map[string]string{}
		g.inflight = map[string]bool{}
	}
	if c, ok := g.cache[key]; ok && c != "" {
		g.mu.Unlock()
		return c, nil
	}
	g.mu.Unlock()
	q := url.Values{}
	q.Set("coordtype", "wgs84ll")
	q.Set("location", fmt.Sprintf("%.6f,%.6f", lat, lon))
	var m rgcResp
	if err := g.api().get(ctx, "/reverse_geocoding/v3/", "逆地理编码", q, &m); err != nil {
		return "", err
	}
	city := strings.TrimSpace(m.Result.Component.City)
	if city == "" {
		return "", fmt.Errorf("这个位置认不出是哪个城市")
	}
	g.mu.Lock()
	g.cache[key] = city
	g.mu.Unlock()
	return city, nil
}
