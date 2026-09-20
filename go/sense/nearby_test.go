package sense

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const rgcBody = `{"status":0,"result":{
  "formatted_address":"安徽省蚌埠市蚌山区淮河社区G206(东海大道)",
  "sematic_description":"",
  "addressComponent":{"city":"蚌埠市","district":"蚌山区","street":"东海大道"}}}`

func fakeRGC(t *testing.T, body string, hits *int64) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		if r.URL.Query().Get("coordtype") != "wgs84ll" {
			t.Errorf("没告诉百度这是 WGS-84: %q", r.URL.Query().Get("coordtype"))
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

// 等到缓存里有东西 —— 后台那次查是异步的
func waitName(g *RGC, lat, lon float64) (string, bool) {
	for i := 0; i < 100; i++ {
		if n, ok := g.Name(lat, lon); ok {
			return n, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", false
}

// **第一次一定返回 false, 而且不能阻塞** —— describe 跑在总线的同步
// 观察链上, 在那儿等一次网络往返的话, 手机补传几百条时整条链会卡住
func TestNearbyNeverBlocks(t *testing.T) {
	var hits int64
	srv := fakeRGC(t, rgcBody, &hits)
	g := &RGC{AK: "x", Base: srv.URL, HTTP: srv.Client()}

	start := time.Now()
	if _, ok := g.Name(32.919, 117.357); ok {
		t.Fatal("第一次就返回了答案 —— 那说明它同步等了网络")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("第一次花了 %v —— 它在信号路径上, 不能等", d)
	}
	name, ok := waitName(g, 32.919, 117.357)
	if !ok {
		t.Fatal("后台查完了却没进缓存")
	}
	// **不要那串完整地址**: 省市区全串进上下文的话, 一天几十条位置
	// 事实就是几千个字的省市
	if name != "蚌山区东海大道" {
		t.Fatalf("地址没砍短: %q", name)
	}
}

// 按格子缓存 —— 一个不动的手机一天只该花一次配额
func TestNearbyCachesByGrid(t *testing.T) {
	var hits int64
	srv := fakeRGC(t, rgcBody, &hits)
	g := &RGC{AK: "x", Base: srv.URL, HTTP: srv.Client()}
	waitName(g, 32.919, 117.357)
	// 挪了几十米 —— 同一格
	for i := 0; i < 5; i++ {
		g.Name(32.9192, 117.3572)
	}
	time.Sleep(60 * time.Millisecond)
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Fatalf("同一格查了 %d 次 —— 配额一天就烧光了", n)
	}
}

// **日上限要兜住**: 配额用光之后百度回的是一个状态码, 而那时候整条路
// 会静默地退回"不知道" —— 自己先停下来至少是可预期的
func TestNearbyStopsAtDailyCap(t *testing.T) {
	var hits int64
	srv := fakeRGC(t, rgcBody, &hits)
	g := &RGC{AK: "x", Base: srv.URL, HTTP: srv.Client(), Cap: 2}
	for i := 0; i < 10; i++ {
		g.Name(30.0+float64(i)*0.01, 120.0)
	}
	time.Sleep(80 * time.Millisecond)
	if n := atomic.LoadInt64(&hits); n > 2 {
		t.Fatalf("上限是 2, 却查了 %d 次", n)
	}
}

// 没配 key 就安静地说不知道, 不是报错 —— 这条路本来就是兜底的
func TestNearbyWithoutKey(t *testing.T) {
	g := &RGC{}
	if _, ok := g.Name(1, 1); ok {
		t.Fatal("没配 key 却给了答案")
	}
}

// 查过但那儿什么都没有(海上、荒地)也要记住 ——
// 不记的话每次都会再查一遍, 配额就耗在这上面
func TestNearbyRemembersEmptyAnswer(t *testing.T) {
	var hits int64
	srv := fakeRGC(t, `{"status":0,"result":{"formatted_address":"","addressComponent":{}}}`, &hits)
	g := &RGC{AK: "x", Base: srv.URL, HTTP: srv.Client()}
	for i := 0; i < 5; i++ {
		g.Name(0.5, 100.0)
		time.Sleep(15 * time.Millisecond)
	}
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Fatalf("空答案没记住, 查了 %d 次", n)
	}
}

// **按他的位置找时 coord_type=2 一定要带** —— 不带的话百度把参照点当成
// BD-09, 偏七八百米, 排序照样出来只是按一个错的点排的
func TestFindNearHimSaysCoordType(t *testing.T) {
	b, seen := fakeBaidu(t, map[string]string{"/place/v2/search": `{"status":0,"results":[
	 {"name":"凤凰国际广场","address":"东海大道与航苑路交叉口东南侧","location":{"lat":32.917667,"lng":117.362455}}]}`})
	g := &RGC{AK: b.AK, HTTP: b.HTTP, Base: b.Host}
	got, err := g.Find(context.Background(), "凤凰国际广场", "", 32.96, 117.35)
	if err != nil || len(got) != 1 || got[0].Name != "凤凰国际广场" {
		t.Fatalf("%+v %v", got, err)
	}
	q := (*seen)[0].Query()
	if q.Get("coord_type") != "2" || q.Get("ret_coordtype") != "gcj02ll" || q.Get("location") == "" {
		t.Fatalf("参照点坐标系没说清: %v", q)
	}
}

// 他问"我在哪条路边、在什么楼里" —— 要路名也要楼. 19:56 那一次百度回的就是这样
func TestShortAddressHasRoadAndBuilding(t *testing.T) {
	var m rgcResp
	m.Result.Sematic = "汇金国际碧桂苑内,固镇县城关镇浍河社区退役军人服务站西南184米"
	m.Result.Component.District = "固镇县"
	m.Result.Component.Street = "庙岗路"
	if got := shortAddress(m); got != "固镇县庙岗路，汇金国际碧桂苑内" {
		t.Fatalf("%q", got)
	}
	// 距离那个尾巴要去掉 —— 反查的米数本身就有几十米误差
	if got := trimMeters("大润发附近7米"); got != "大润发附近" {
		t.Fatalf("%q", got)
	}
	if got := trimMeters("没有米数"); got != "没有米数" {
		t.Fatalf("不该动: %q", got)
	}
	m.Result.Sematic = "凤凰国际广场内,兴业银行(蚌埠分行)西南67米"
	m.Result.Component.District = "蚌山区"
	m.Result.Component.Street = "G206(东海大道)"
	if got := shortAddress(m); got != "蚌山区东海大道，凤凰国际广场内" {
		t.Fatalf("没人管东海大道叫 G206: %q", got)
	}
}

// 他问的那一轮本来就在等 —— 缓存里没有就当场查, 而且查完进缓存
func TestNameNowWaitsOnce(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.URL.Query().Get("extensions_poi") != "1" {
			t.Errorf("没要 POI —— 那样就说不出在哪栋楼里")
		}
		_, _ = w.Write([]byte(rgcBody))
	}))
	t.Cleanup(srv.Close)
	g := &RGC{AK: "x", Base: srv.URL, HTTP: srv.Client()}
	name, ok := g.NameNow(context.Background(), 32.919, 117.357)
	if !ok || name != "蚌山区东海大道" {
		t.Fatalf("当场查没拿到: %q %v", name, ok)
	}
	if n, ok := g.Name(32.919, 117.357); !ok || n != name {
		t.Fatalf("查完没进缓存, 信号路径那边还是说不出: %q", n)
	}
	g.NameNow(context.Background(), 32.9191, 117.3571)
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Fatalf("同一格查了 %d 次", n)
	}
}
