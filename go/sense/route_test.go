package sense

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// fakeBaidu 一个假的百度: 按路径回不同的响应, 顺手把每次请求记下来.
func fakeBaidu(t *testing.T, bodies map[string]string) (*Baidu, *[]*url.URL) {
	t.Helper()
	var seen []*url.URL
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL)
		if r.URL.Query().Get("ak") == "" {
			t.Error("没带 ak")
		}
		body, ok := bodies[r.URL.Path]
		if !ok {
			t.Errorf("没想到会问 %s", r.URL.Path)
			body = `{"status":1,"message":"unexpected"}`
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return &Baidu{AK: "x", Host: s.URL, HTTP: s.Client()}, &seen
}

// 形状照 2026-09-11 在服务器上真问回来的那份删减 —— 字段名一个都没改
const driveBody = `{"status":0,"message":"成功","result":{"routes":[
 {"tag":"大路多","distance":7641,"duration":1037,"traffic_light":14,"taxi_fee":16,"toll":0,"steps":[
  {"road_name":"无名路","distance":67,"duration":10,"traffic_condition":[{"status":0,"distance":67}]},
  {"road_name":"东海大道","distance":900,"duration":100,"traffic_condition":[{"status":1,"distance":900}]},
  {"road_name":"东海大道","distance":582,"duration":60,"traffic_condition":[{"status":1,"distance":582}]},
  {"road_name":"朝阳路","distance":4291,"duration":600,"traffic_condition":[{"status":1,"distance":3091},{"status":3,"distance":1200}]},
  {"road_name":"朝阳北路","distance":571,"duration":80,"traffic_condition":[{"status":1,"distance":571}]},
  {"road_name":"淮上大道","distance":823,"duration":100,"traffic_condition":[{"status":1,"distance":823}]},
  {"road_name":"永平街","distance":173,"duration":30,"traffic_condition":[{"status":1,"distance":173}]}]},
 {"tag":"少等灯","distance":7576,"duration":1049,"traffic_light":12,"taxi_fee":16,"toll":0,"steps":[
  {"road_name":"东海大道","distance":947,"duration":100,"traffic_condition":[{"status":1,"distance":947}]},
  {"road_name":"工农路","distance":2261,"duration":300,"traffic_condition":[{"status":1,"distance":2261}]},
  {"road_name":"朝阳路","distance":2048,"duration":300,"traffic_condition":[{"status":1,"distance":2048}]}]}]}}`

// 只给“17 分钟”会让回答编造路名和路况；路线、实时路况和备选路线三样都要给到。
func TestRouteSaysRoadsTrafficAndAlternative(t *testing.T) {
	b, seen := fakeBaidu(t, map[string]string{"/direction/v2/driving": driveBody})
	got, err := b.Route(context.Background(), 32.920, 117.363, 32.963, 117.350, ByCar)
	if err != nil {
		t.Fatalf("问不到: %v", err)
	}
	q := (*seen)[0].Query()
	// **参数要真的传对** —— 传错的话线上会静默地从一个偏了一公里的起点开始算
	if q.Get("coord_type") != "wgs84" || q.Get("alternatives") != "1" {
		t.Fatalf("参数不对: %v", q)
	}
	if q.Get("origin") != "32.920000,117.363000" {
		t.Fatalf("起点是 纬度,经度: %q", q.Get("origin"))
	}
	if len(got) != 2 || got[0].Duration != 1037*time.Second {
		t.Fatalf("路线拿错了: %+v", got)
	}
	s := Summary(got)
	for _, want := range []string{
		"开车 17 分钟", "7.6 公里", "已按此刻路况算", "14 个红绿灯", "打车约 16 元",
		"东海大道 → 朝阳路 → 朝阳北路 → 淮上大道", // 无名路不出现, 同名段并成一段
		"朝阳路 1.2 公里拥堵",
		"另一条（少等灯）", "工农路",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("少了「%s」:\n%s", want, s)
		}
	}
	if strings.Contains(s, "无名路") || strings.Contains(s, "永平街") {
		t.Errorf("路口和短路不该算'走哪条':\n%s", s)
	}
}

// **没数据不能说"一路畅通"** —— 他会照着这句话晚出门
func TestRouteWithoutLiveTrafficSaysSo(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/direction/v2/driving": `{"status":0,"result":{"routes":[
	 {"distance":4200,"duration":900,"steps":[{"road_name":"乡道","distance":4200,"duration":900,
	   "traffic_condition":[{"status":0,"distance":4200}]}]}]}}`})
	got, err := b.Route(context.Background(), 1, 1, 2, 2, ByCar)
	if err != nil {
		t.Fatal(err)
	}
	s := got[0].Text()
	if strings.Contains(s, "畅通") || strings.Contains(s, "已按此刻路况") {
		t.Fatalf("没有路况数据却说了路况: %q", s)
	}
	if !strings.Contains(s, "没有实时路况数据") || !strings.Contains(s, "15 分钟") {
		t.Fatalf("该照实说没数据: %q", s)
	}
}

func TestRouteAllClearSaysClear(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/direction/v2/driving": `{"status":0,"result":{"routes":[
	 {"distance":4200,"duration":600,"steps":[{"road_name":"东海大道","distance":4200,"duration":600,
	   "traffic_condition":[{"status":1,"distance":4200}]}]}]}}`})
	got, _ := b.Route(context.Background(), 1, 1, 2, 2, ByCar)
	if s := got[0].Text(); !strings.Contains(s, "一路畅通") {
		t.Fatalf("%q", s)
	}
}

// 公交的 steps 是二维的 —— 解成一维会丢掉整段乘车
func TestTransitSaysWhichBus(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/directionlite/v1/transit": `{"status":0,"result":{"routes":[
	 {"distance":8523,"duration":2799,"price":2,"steps":[
	  [{"distance":395,"duration":338,"type":5,"instruction":"步行395米","vehicle":{"name":""}}],
	  [{"distance":7832,"duration":1608,"type":3,"vehicle":{"name":"131路","direct_text":"蚌医二附院首末站方向",
	    "start_name":"延安路·东海大道站","end_name":"淮上区政府站","stop_num":10,"end_time":"19:00"}}],
	  [{"distance":296,"duration":253,"type":5,"vehicle":{"name":""}}]]}]}}`})
	got, err := b.Route(context.Background(), 1, 1, 2, 2, ByTransit)
	if err != nil {
		t.Fatal(err)
	}
	s := got[0].Text()
	for _, want := range []string{"公交 47 分钟", "步行 395 米", "延安路·东海大道站 上 131路", "坐 10 站到 淮上区政府站", "末班 19:00", "票价 2 元"} {
		if !strings.Contains(s, want) {
			t.Errorf("少了「%s」: %q", want, s)
		}
	}
	if strings.Contains(s, "路况") {
		t.Errorf("公交不看路况: %q", s)
	}
}

func TestWalkingPicksRoadNamesFromInstructions(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/directionlite/v1/walking": `{"status":0,"result":{"routes":[
	 {"distance":1301,"duration":1172,"steps":[
	  {"distance":50,"duration":40,"instruction":"向正西方向出发,走50米,<b>左转</b>"},
	  {"distance":1100,"duration":1000,"instruction":"走120米,<b>右转</b>进入<b>东海大道</b>"}]}]}}`})
	got, err := b.Route(context.Background(), 1, 1, 2, 2, ParseMode("走路"))
	if err != nil {
		t.Fatal(err)
	}
	if s := got[0].Text(); !strings.Contains(s, "走路 20 分钟") || !strings.Contains(s, "东海大道") {
		t.Fatalf("%q", s)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ByCar, "开车": ByCar, "drive": ByCar,
		"公交": ByTransit, "坐地铁": ByTransit, "骑电动车": ByBike, "步行": OnFoot, "乱写": ByCar} {
		if got := ParseMode(in); got != want {
			t.Errorf("%q → %v, 要 %v", in, got, want)
		}
	}
}

// **没配 key 是"还没准备好", 不是"坏了"** —— 见 abi.ErrNotReady.
// 而且不该去撞一个必然失败的请求
func TestRouteWithoutKeyIsNotReady(t *testing.T) {
	_, err := (&Baidu{}).Route(context.Background(), 1, 1, 2, 2, ByCar)
	if !errors.Is(err, abi.ErrNotReady) {
		t.Fatalf("没配 key 却不是'还没准备好': %v", err)
	}
}

// 240 要翻成人话, 而且要说出**是哪一项没勾**
func TestBaiduExplains240WithService(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/traffic/v1/road": `{"status":240,"message":"APP 服务被禁用"}`})
	_, err := b.RoadTraffic(context.Background(), "朝阳路", "蚌埠市")
	if err == nil || !strings.Contains(err.Error(), "服务端") || !strings.Contains(err.Error(), "实时路况") {
		t.Fatalf("240 没说清该去改什么: %v", err)
	}
}

func TestBaiduQuotaIsNamed(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/direction/v2/driving": `{"status":302,"message":"天配额超限，限制访问"}`})
	_, err := b.Route(context.Background(), 1, 1, 2, 2, ByCar)
	if err == nil || !strings.Contains(err.Error(), "配额用完") {
		t.Fatalf("配额用完要照实说: %v", err)
	}
}

// 算不出路线不是故障, 但也不能装作算出来了
func TestRouteNoRoute(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/direction/v2/driving": `{"status":0,"result":{"routes":[]}}`})
	if _, err := b.Route(context.Background(), 1, 1, 2, 2, ByCar); err == nil {
		t.Fatal("一条路线都没有却当成算出来了")
	}
}

func TestRoadTrafficListsJams(t *testing.T) {
	b, seen := fakeBaidu(t, map[string]string{"/traffic/v1/road": `{"status":0,"description":"朝阳路：北向南拥堵。",
	 "evaluation":{"status":3,"status_desc":"拥堵"},
	 "road_traffic":[{"road_name":"朝阳路","congestion_sections":[
	  {"section_desc":"淮河桥到胜利路","status":3,"speed":11.5,"congestion_distance":800,"congestion_trend":"持平"}]}]}`})
	s, err := b.RoadTraffic(context.Background(), "朝阳路", "蚌埠市")
	if err != nil {
		t.Fatal(err)
	}
	if q := (*seen)[0].Query(); q.Get("city") != "蚌埠市" || q.Get("road_name") != "朝阳路" {
		t.Fatalf("参数不对: %v", q)
	}
	for _, want := range []string{"北向南拥堵", "淮河桥到胜利路", "800 米", "时速约 12", "持平"} {
		if !strings.Contains(s, want) {
			t.Errorf("少了「%s」: %q", want, s)
		}
	}
}

// 全国叫"人民路"的有几百条 —— 不给城市就别问
func TestRoadTrafficNeedsCity(t *testing.T) {
	b, seen := fakeBaidu(t, map[string]string{})
	if _, err := b.RoadTraffic(context.Background(), "人民路", ""); err == nil {
		t.Fatal("没城市也问了")
	}
	if len(*seen) != 0 {
		t.Fatal("不该出网")
	}
}

func TestAroundTrafficUsesWGS84(t *testing.T) {
	b, seen := fakeBaidu(t, map[string]string{"/traffic/v1/around": `{"status":0,"description":"该区域整体畅通。"}`})
	s, err := b.AroundTraffic(context.Background(), 32.92, 117.36, 5000)
	if err != nil || s != "该区域整体畅通。" {
		t.Fatalf("%q %v", s, err)
	}
	q := (*seen)[0].Query()
	if q.Get("coord_type_input") != "wgs84" || q.Get("radius") != "1000" {
		t.Fatalf("%v", q)
	}
}

// 问的是“今天”，不能只答“此刻”；当天预报决定出行安排。
func TestForecastSaysTodayTomorrowAndRainHours(t *testing.T) {
	b, seen := fakeBaidu(t, map[string]string{"/weather/v1/": `{"status":0,"result":{
	 "location":{"city":"蚌埠市","name":"蚌山区"},
	 "now":{"text":"多云","temp":27,"feels_like":28,"rh":50,"wind_class":"2级","wind_dir":"东南风","aqi":43},
	 "forecasts":[{"date":"2026-09-11","week":"星期五","text_day":"阴","text_night":"小雨","high":26,"low":19,"wc_day":"3~4级","wd_day":"东风"},
	              {"date":"2026-09-12","week":"星期六","text_day":"阴","text_night":"阴","high":25,"low":20,"wc_day":"<3级","wd_day":"东南风"}],
	 "forecast_hours":[{"pop":10,"data_time":"2026-09-11 14:00:00"},{"pop":60,"data_time":"2026-09-11 18:00:00"}],
	 "alerts":[{"type":"暴雨","level":"蓝色","title":"蚌埠市气象台发布暴雨蓝色预警"}]}}`})
	s, err := b.Forecast(context.Background(), 32.92, 117.36)
	if err != nil {
		t.Fatal(err)
	}
	if q := (*seen)[0].Query(); q.Get("location") != "117.360000,32.920000" || q.Get("coordtype") != "wgs84" {
		t.Fatalf("天气接口是 经度,纬度: %v", q)
	}
	for _, want := range []string{"蚌埠市蚌山区 现在多云 27°C", "今天（09-11 星期五）阴转小雨 19~26°C",
		"明天（09-12 星期六）", "18点 60%", "暴雨蓝色预警", "空气 优"} {
		if !strings.Contains(s, want) {
			t.Errorf("少了「%s」:\n%s", want, s)
		}
	}
}

// 骑行的每一步直接携带路名，路线摘要据此列出经过的道路。
func TestRidingUsesStepNames(t *testing.T) {
	b, _ := fakeBaidu(t, map[string]string{"/directionlite/v1/riding": `{"status":0,"result":{"routes":[
	 {"distance":7700,"duration":2580,"steps":[
	  {"distance":51,"duration":15,"instruction":"骑行50米","name":""},
	  {"distance":1494,"duration":452,"instruction":"<b>东海大道</b>,骑行1.5公里","name":"东海大道"},
	  {"distance":2846,"duration":862,"instruction":"<b>朝阳路</b>,骑行2.8公里","name":"朝阳路"}]}]}}`})
	got, err := b.Route(context.Background(), 1, 1, 2, 2, ByBike)
	if err != nil {
		t.Fatal(err)
	}
	if s := got[0].Text(); !strings.Contains(s, "骑车 43 分钟") || !strings.Contains(s, "东海大道 → 朝阳路") {
		t.Fatalf("%q", s)
	}
}
