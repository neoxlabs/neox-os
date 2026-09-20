package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
	"github.com/neox-os/neox-os/sense"
)

// 固定的几个 WGS-84 测试点: 家和单位.
const (
	homeLat, homeLon   = 32.963076, 117.350713
	phoenixGCJLat      = 32.917667 // 百度回的是 GCJ-02
	phoenixGCJLon      = 117.362455
	phoenixName        = "凤凰国际广场"
	phoenixAddr        = "东海大道与航苑路交叉口东南侧"
	phoenixSearchReply = `{"status":0,"results":[{"name":"凤凰国际广场","address":"东海大道与航苑路交叉口东南侧",
	  "location":{"lat":32.917667,"lng":117.362455}}]}`
)

// testGeo 一个接了假百度的 geo. hits 记下每次问了哪条接口.
func testGeo(t *testing.T, bodies map[string]string) (*geo, *[]string) {
	t.Helper()
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		body, ok := bodies[r.URL.Path]
		if !ok {
			body = `{"status":1,"message":"没配这条"}`
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	log := osinit.NewEventLog(func() int64 { return time.Now().UnixMilli() })
	g := &geo{
		log:     log,
		places:  osinit.NewPlaces(log),
		devices: osinit.NewDevices(log),
		rgc:     &sense.RGC{AK: "x", Base: srv.URL, HTTP: srv.Client()},
		api:     &sense.Baidu{AK: "x", Host: srv.URL, HTTP: srv.Client()},
		now:     time.Now,
	}
	return g, &hits
}

// at 手机报了一条位置, 时间是 ago 之前.
func at(g *geo, lat, lon float64, ago time.Duration) {
	g.places.Observe(abi.Signal{Kind: "location", At: time.Now().Add(-ago).UnixMilli(),
		Body: map[string]any{"lat": lat, "lon": lon}})
}

// **真机 08:06 那一幕**: 他在家, 模型把"这儿"起名成凤凰国际广场.
// 他此刻在另一个起过名的地方里 → 这儿不是那儿.
func TestNameHereRefusesWhileHeIsInsideAnotherPlace(t *testing.T) {
	g, _ := testGeo(t, nil)
	if _, err := g.places.Add("家", homeLat, homeLon, 600); err != nil {
		t.Fatal(err)
	}
	at(g, homeLat+0.0001, homeLon+0.0009, time.Minute)
	_, err := g.nameHere(phoenixName, 250, false)
	if err == nil || !strings.Contains(err.Error(), "「家」的范围里") {
		t.Fatalf("他人在家却把这儿记成了单位: %v", err)
	}
	if g.places.Has(phoenixName) {
		t.Fatal("拒了还是存进去了")
	}
}

// 地图上这个名字在几公里外 → 他人不在那儿. 要把地图上那个地址交给模型,
// 它下一步才知道该怎么记
func TestNameHereRefusesWhenTheMapSaysItIsElsewhere(t *testing.T) {
	g, _ := testGeo(t, map[string]string{"/place/v2/search": phoenixSearchReply})
	at(g, homeLat, homeLon, 30*time.Second)
	_, err := g.nameHere(phoenixName, 250, false)
	if err == nil || !strings.Contains(err.Error(), phoenixAddr) || !strings.Contains(err.Error(), "公里") {
		t.Fatalf("地图说在别处却照记了: %v", err)
	}
}

// 他亲口说"就是这儿" —— 那是他的判断, 工具让路
func TestNameHereSureOverrides(t *testing.T) {
	g, _ := testGeo(t, map[string]string{"/place/v2/search": phoenixSearchReply})
	g.places.Add("家", homeLat, homeLon, 600)
	at(g, homeLat, homeLon, 10*time.Second)
	got, err := g.nameHere("小区门口", 100, true)
	if err != nil || !strings.Contains(got, "「小区门口」") || !strings.Contains(got, "100 米") {
		t.Fatalf("%q %v", got, err)
	}
}

// 位置太旧而手机也没回 → 不能当成"这儿"
func TestNameHereRefusesStaleFix(t *testing.T) {
	g, _ := testGeo(t, nil)
	at(g, homeLat, homeLon, 20*time.Minute)
	_, err := g.nameHere("公司", 250, false)
	if err == nil || !strings.Contains(err.Error(), "前的") {
		t.Fatalf("二十分钟前的位置被当成了此刻: %v", err)
	}
}

// 没起过名的地方, 问路要**自己去地图上搜** —— 原来答"不认得"
func TestRouteSearchesUnknownDestination(t *testing.T) {
	g, hits := testGeo(t, map[string]string{
		"/place/v2/search": phoenixSearchReply,
		"/direction/v2/driving": `{"status":0,"result":{"routes":[{"distance":7641,"duration":1037,
		  "steps":[{"road_name":"朝阳路","distance":7641,"duration":1037,"traffic_condition":[{"status":1,"distance":7641}]}]}]}}`,
	})
	g.places.Add("家", homeLat, homeLon, 600)
	at(g, homeLat, homeLon, 10*time.Second)
	got, err := g.route(phoenixName, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"从家到凤凰国际广场（" + phoenixAddr + "）", "开车 17 分钟", "朝阳路", "一路畅通"} {
		if !strings.Contains(got, want) {
			t.Errorf("少了「%s」:\n%s", want, got)
		}
	}
	if (*hits)[0] != "/place/v2/search" {
		t.Errorf("该先搜地方: %v", *hits)
	}
}

// 起点和终点是同一个点 → **不报 0 分钟**, 说出是哪儿记错了
func TestRouteRefusesZeroMinuteTrip(t *testing.T) {
	g, hits := testGeo(t, nil)
	g.places.Add(phoenixName, homeLat+0.0001, homeLon+0.0009, 250)
	at(g, homeLat, homeLon, 10*time.Second)
	got, err := g.route(phoenixName, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "0 分钟") || !strings.Contains(got, "已经在那儿") {
		t.Fatalf("起点终点同一个点还算了路线: %q", got)
	}
	for _, h := range *hits {
		if strings.Contains(h, "direction") {
			t.Fatal("同一个点不该去问路线")
		}
	}
}

// 起点可以是任何地方 —— "从碧桂园到凤凰国际广场", 他不一定在碧桂园
func TestRouteFromNamedPlace(t *testing.T) {
	g, _ := testGeo(t, map[string]string{
		"/direction/v2/driving": `{"status":0,"result":{"routes":[{"distance":7641,"duration":1037,"steps":[]}]}}`,
	})
	g.places.Add("家", homeLat, homeLon, 600)
	phLat, phLon := osinit.GCJ02ToWGS84(phoenixGCJLat, phoenixGCJLon)
	g.places.Add("公司", phLat, phLon, 300)
	got, err := g.route("家", "公司", "")
	if err != nil || !strings.HasPrefix(got, "从公司到家") {
		t.Fatalf("%q %v", got, err)
	}
}

// 起过名的地方要能用半个名字认出来, 但**两个都沾边就不猜**
func TestSavedMatchesUniquePartialOnly(t *testing.T) {
	g, _ := testGeo(t, nil)
	g.places.Add("蚌埠碧桂园", homeLat, homeLon, 600)
	if s, ok := g.saved("碧桂园"); !ok || s.Name != "蚌埠碧桂园" {
		t.Fatalf("半个名字没认出来: %+v", s)
	}
	g.places.Add("碧桂园幼儿园", homeLat, homeLon, 100)
	if _, ok := g.saved("碧桂园"); ok {
		t.Fatal("两个都沾边还猜了一个")
	}
}

func TestStaleNoteOnlyWhenOld(t *testing.T) {
	if staleNote(30*time.Second) != "" {
		t.Error("半分钟前的算此刻")
	}
	if n := staleNote(10 * time.Minute); !strings.Contains(n, "10 分钟") {
		t.Errorf("%q", n)
	}
}

// 同一栋楼起了两个名字 —— 列出来时要看得出, 而且它自己删得掉
func TestPlacesShowOverlapAndCanBeForgotten(t *testing.T) {
	g, _ := testGeo(t, nil)
	g.places.Add("公司", 32.919, 117.357, 150)
	g.places.Add("凤凰国际广场", 32.92031, 117.35570, 250)
	g.places.Add("家", homeLat, homeLon, 600)
	list := g.list()
	if !strings.Contains(list, "「凤凰国际广场」只隔") || !strings.Contains(list, "圈是叠着的") {
		t.Fatalf("两个名字是同一处, 列表里看不出: %q", list)
	}
	if strings.Count(list, "叠着") != 2 {
		t.Fatalf("家不该跟它们叠: %q", list)
	}
	out, err := g.forget("凤凰国际广场")
	if err != nil || !strings.Contains(out, "公司") {
		t.Fatalf("%q %v", out, err)
	}
	if strings.Contains(g.list(), "叠着") {
		t.Fatal("删完了还说叠着")
	}
	if _, err := g.forget("凤凰国际广场"); err == nil {
		t.Fatal("删两次都说成功")
	}
}

// "我今天去过哪儿" —— 账本里躺着每一条位置信号, 而它原来只能 run grep
func TestTrailReadsStaysFromLedger(t *testing.T) {
	g, _ := testGeo(t, nil)
	g.places.Add("家", homeLat, homeLon, 250)
	// **按本地零点算**: Truncate 是按 UTC 切的, 在 -0700 上它给的是昨天 17:00
	n := time.Now()
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location())
	put := func(lat, lon float64, at time.Time) {
		g.log.Append(abi.ProcessID("sense"), abi.EvSignal, map[string]any{
			"at": float64(at.UnixMilli()), "kind": "location",
			"body": map[string]any{"lat": lat, "lon": lon},
		})
	}
	// 在家待了一个钟头, 然后挪到两公里外待了半小时, 路上那两条不算停留
	put(homeLat, homeLon, day.Add(8*time.Hour))
	put(homeLat+0.0005, homeLon, day.Add(9*time.Hour))
	put(homeLat+0.01, homeLon+0.01, day.Add(9*time.Hour+20*time.Minute))
	put(homeLat+0.02, homeLon+0.02, day.Add(10*time.Hour))
	put(homeLat+0.02, homeLon+0.02, day.Add(10*time.Hour+40*time.Minute))
	out, err := g.trail("今天")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "家") {
		t.Fatalf("起过名的地方没认出来: %q", out)
	}
	// 09:00 还在家, 下一条(别处)是 09:20 —— 那就是待到快 9:20 才出门,
	// 跟 osinit/routine.go 同一个读法
	if !strings.Contains(out, "- 08:00–09:20 家") {
		t.Fatalf("在家那一段不对: %q", out)
	}
	if !strings.Contains(out, "- 10:00–10:40") {
		t.Fatalf("第二个地方漏了: %q", out)
	}
	// 09:20 那条是路上随手一条定位, 后面 40 分钟没信号 —— 它可以是"在家待到
	// 9:20", 但不该自己单开一行变成"在路口待了 40 分钟"
	if strings.Contains(out, "- 09:20") {
		t.Fatalf("把路过算成了停留: %q", out)
	}
	if n := strings.Count(out, "\n- "); n != 2 {
		t.Fatalf("该是两段, 实际 %d: %q", n, out)
	}
	if _, err := g.trail("大前天"); err == nil {
		t.Fatal("看不懂的日子要说看不懂")
	}
}

// 他亲口说"就是这儿"的时候, 时效那道闸也该让开 —— 真机测试里他说
// "不管你怎么认, 这儿就是姥姥家", 位置才 4 分钟旧, 照样被拒
func TestNameHereSureRelaxesStaleness(t *testing.T) {
	g, _ := testGeo(t, nil)
	at(g, homeLat, homeLon, 4*time.Minute)
	if _, err := g.nameHere("姥姥家", 250, false); err == nil {
		t.Fatal("不坚持的时候 4 分钟前的位置不该当成「这儿」")
	}
	out, err := g.nameHere("姥姥家", 250, true)
	if err != nil {
		t.Fatalf("他都明说了还拒: %v", err)
	}
	if !strings.Contains(out, "姥姥家") {
		t.Fatalf("%q", out)
	}
	// 半小时以前的还是不认 —— 那时候真不是"这儿"了.
	// **另起一个**: 往同一个 geo 上再灌一条旧的没用, 最新那条才算数
	g2, _ := testGeo(t, nil)
	at(g2, homeLat, homeLon, 50*time.Minute)
	if _, err := g2.nameHere("很久以前", 250, true); err == nil {
		t.Fatal("五十分钟前的位置不该当成「这儿」")
	}
}
