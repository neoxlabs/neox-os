package sense

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const meteoBody = `{
  "current": {"temperature_2m": 21.4, "precipitation": 0, "weather_code": 3},
  "hourly": {
    "time": ["2026-09-10T13:00","2026-09-10T14:00","2026-09-10T15:00"],
    "precipitation_probability": [10, 35, 82]
  }
}`

func fakeMeteo(t *testing.T, body string, code int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func here() (float64, float64, bool) { return 31.86, 117.28, true }

func TestWeatherSaysConclusionNotNumbers(t *testing.T) {
	srv := fakeMeteo(t, meteoBody, 200)
	w := &Weather{Where: here, Base: srv.URL, HTTP: srv.Client()}

	sigs, err := w.Fetch(context.Background())
	if err != nil {
		t.Fatalf("拉不到: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("该出一条信号, 得到 %d", len(sigs))
	}
	text, _ := sigs[0].Body["text"].(string)
	// **峰值不是平均**: 一整天平均 42% 会把"下午 3 点 82%"抹平,
	// 而"要不要带伞"问的正是后者
	if !strings.Contains(text, "15 点") || !strings.Contains(text, "82") {
		t.Fatalf("没说清什么时候下、多大概率: %q", text)
	}
	if !strings.Contains(text, "21°C") {
		t.Fatalf("没说温度: %q", text)
	}
	// 规则要判的那个数得留在 body 里 —— 只有一句话的话, 条件触发
	// 就只能去正则匹配中文, 那是必然会错的
	if p, _ := sigs[0].Body["rainProb"].(float64); p != 82 {
		t.Fatalf("rainProb 不对: %v —— 规则没有别的数可判", sigs[0].Body["rainProb"])
	}
}

// 天气**知道自己什么时候作废** —— 说出来, 别让 OS 去猜
func TestWeatherDeclaresValidity(t *testing.T) {
	srv := fakeMeteo(t, meteoBody, 200)
	w := &Weather{Where: here, Base: srv.URL, HTTP: srv.Client(), Pace: 30 * time.Minute}
	sigs, _ := w.Fetch(context.Background())
	if v, _ := sigs[0].Body["validSec"].(int); v != 3600 {
		t.Fatalf("没声明有效期或者算错了: %v", sigs[0].Body["validSec"])
	}
}

// 40% 以下不提雨 —— 提了的话每天都在提, 而那正是打扰预算要挡的东西
func TestWeatherStaysQuietOnLowChance(t *testing.T) {
	body := `{"current":{"temperature_2m":18,"precipitation":0,"weather_code":0},
	  "hourly":{"time":["2026-09-10T13:00"],"precipitation_probability":[15]}}`
	srv := fakeMeteo(t, body, 200)
	w := &Weather{Where: here, Base: srv.URL, HTTP: srv.Client()}
	sigs, _ := w.Fetch(context.Background())
	text, _ := sigs[0].Body["text"].(string)
	if strings.Contains(text, "雨") {
		t.Fatalf("15%% 的概率也在提雨: %q —— 那就是每天都在提", text)
	}
}

// 不知道在哪儿**不是故障, 但必须说出来**: 否则用户看到的是
// "天气这一路从来没工作过", 而他不知道差的只是一次定位
func TestWeatherSaysWhenItDoesNotKnowWhere(t *testing.T) {
	w := &Weather{Where: func() (float64, float64, bool) { return 0, 0, false }}
	_, err := w.Fetch(context.Background())
	if err == nil {
		t.Fatal("不知道位置却装作拉到了")
	}
	if !strings.Contains(err.Error(), "在哪儿") {
		t.Fatalf("报错没说清差的是什么: %v", err)
	}
}

func TestWeatherReportsBadStatus(t *testing.T) {
	srv := fakeMeteo(t, `{}`, 500)
	w := &Weather{Where: here, Base: srv.URL, HTTP: srv.Client()}
	if _, err := w.Fetch(context.Background()); err == nil {
		t.Fatal("接口 500 也当成拉到了 —— 那这一路会静默地死掉")
	}
}

// **天气只能有一个源**: 配了百度就走百度, 别让世界模型和 where(weather=)
// 各说各的 —— 一个说"12 点前后有雨 60%", 一个说"最高 0% 不用带伞"
func TestWeatherPrefersBaiduWhenConfigured(t *testing.T) {
	var hit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path
		_, _ = w.Write([]byte(`{"status":0,"result":{
		  "now":{"text":"多云","temp":24},
		  "forecast_hours":[{"pop":10,"data_time":"2026-09-12 12:00:00"},
		                    {"pop":60,"data_time":"2026-09-12 15:00:00"}]}}`))
	}))
	defer srv.Close()
	w := &Weather{
		Where: func() (float64, float64, bool) { return 32.919, 117.357, true },
		Baidu: &Baidu{AK: "x", Host: srv.URL, HTTP: srv.Client()},
	}
	sigs, err := w.Fetch(context.Background())
	if err != nil || len(sigs) != 1 {
		t.Fatalf("%v %d", err, len(sigs))
	}
	b := sigs[0].Body
	if hit != "/weather/v1/" {
		t.Fatalf("没走百度那条: %q", hit)
	}
	if got := b["rainProb"]; got != float64(60) {
		t.Fatalf("降水概率取的不是峰值: %v", got)
	}
	text, _ := b["text"].(string)
	if !strings.Contains(text, "多云 24°C") || !strings.Contains(text, "15 点 前后有雨") {
		t.Fatalf("那句话不对: %q", text)
	}
}
