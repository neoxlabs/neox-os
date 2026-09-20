package osinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

func newView(t *testing.T) (*SignalView, *Places, string) {
	t.Helper()
	dir := t.TempDir()
	p := NewPlaces(nil)
	return NewSignalView(dir, p), p, dir
}

func at(s string) int64 {
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return tm.UnixMilli()
}

// **"我今天去过哪儿"要答得上来.**
//
// 补传那一段文档写着"它们是用来回答这类问题的", 而实际上用户这么问,
// agent 无从答起 —— 推的路径建完了, 拉的路径一条都没有.
func TestViewAnswersWhereWasI(t *testing.T) {
	v, p, dir := newView(t)
	p.Add("公司", 31.86, 117.28, 0)
	p.Add("家", 31.88, 117.28, 0)

	v.Append(abi.Signal{Kind: "place.arrived", At: at("2026-08-16 09:12"),
		Body: map[string]any{"lat": 31.86001, "lon": 117.28}})
	v.Append(abi.Signal{Kind: "place.left", At: at("2026-08-16 18:03"),
		Body: map[string]any{"lat": 31.86001, "lon": 117.28,
			"stayedMs": float64(8.5 * 3600 * 1000)}})
	if err := v.Flush(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, signalViewName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	t.Logf("视图内容:\n%s", got)

	for _, want := range []string{"2026-08-16 09:12", "到达", "公司", "离开", "待了 8.5 小时"} {
		if !strings.Contains(got, want) {
			t.Fatalf("视图里缺 %q:\n%s", want, got)
		}
	}
	// **坐标不该出现** —— 认出地名之后它是纯噪音, 而且模型会念给用户听
	if strings.Contains(got, "31.86001") {
		t.Fatalf("认出地名之后还留着坐标:\n%s", got)
	}
}

// 时间放最前面 —— **最常见的查法是按天** —— search "2026-08-16" 要能捞出一天
func TestViewIsGreppableByDay(t *testing.T) {
	v, _, dir := newView(t)
	v.Append(abi.Signal{Kind: "call.incoming", At: at("2026-08-15 22:10"),
		Body: map[string]any{"from": "138xxxx"}})
	v.Append(abi.Signal{Kind: "call.incoming", At: at("2026-08-16 09:00"),
		Body: map[string]any{"from": "139xxxx"}})
	v.Flush()

	raw, _ := os.ReadFile(filepath.Join(dir, signalViewName))
	var today int
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "2026-08-16") {
			today++
		}
	}
	if today != 1 {
		t.Fatalf("按天 grep 捞出 %d 行, 该是 1 行:\n%s", today, raw)
	}
}

// 没命名过的地方保留坐标 —— 至少还能说"你在某个我不认识的地方待了半小时"
func TestViewKeepsCoordsForUnknownPlaces(t *testing.T) {
	v, _, dir := newView(t)
	v.Append(abi.Signal{Kind: "place.arrived", At: at("2026-08-16 12:00"),
		Body: map[string]any{"lat": 30.1, "lon": 120.2}})
	v.Flush()
	raw, _ := os.ReadFile(filepath.Join(dir, signalViewName))
	if !strings.Contains(string(raw), "30.1000") {
		t.Fatalf("没命名过的地方把坐标也丢了:\n%s", raw)
	}
}

// 认不出来的种类**原样保留**, 不要翻成"其它" ——
// 原始名字至少还能被 search 到, 翻成"其它"就彻底查不出来了
func TestViewKeepsUnknownKindsVerbatim(t *testing.T) {
	v, _, dir := newView(t)
	v.Append(abi.Signal{Kind: "水表.漏水", At: at("2026-08-16 03:00"),
		Body: map[string]any{"note": "厨房"}})
	v.Flush()
	raw, _ := os.ReadFile(filepath.Join(dir, signalViewName))
	if !strings.Contains(string(raw), "水表.漏水") {
		t.Fatalf("没见过的种类被翻没了:\n%s", raw)
	}
}

// 只增不改 —— 跟事件日志一个语义. 重开一次不该把之前的冲掉
func TestViewAppendsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	v1 := NewSignalView(dir, nil)
	v1.Append(abi.Signal{Kind: "call.incoming", At: at("2026-08-16 09:00")})
	v1.Flush()

	v2 := NewSignalView(dir, nil)
	v2.Append(abi.Signal{Kind: "call.incoming", At: at("2026-08-16 10:00")})
	v2.Flush()

	raw, _ := os.ReadFile(filepath.Join(dir, signalViewName))
	if n := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; n != 2 {
		t.Fatalf("重开之后只剩 %d 行 —— 视图被冲掉了:\n%s", n, raw)
	}
}

// 时长要人话 —— 跟摘要那边同一条: 每一次让模型自己算的东西,
// 都是一次它可能算错而没人会发现的地方
func TestViewRendersDurationsAsText(t *testing.T) {
	v, _, dir := newView(t)
	v.Append(abi.Signal{Kind: "place.left", At: at("2026-08-16 18:00"),
		Body: map[string]any{"stayedMs": float64(75216)}})
	v.Flush()
	raw, _ := os.ReadFile(filepath.Join(dir, signalViewName))
	if strings.Contains(string(raw), "75216") {
		t.Fatalf("时长还是毫秒原文:\n%s", raw)
	}
	if !strings.Contains(string(raw), "待了 1 分钟") {
		t.Fatalf("时长没换算:\n%s", raw)
	}
}

// 空的不写文件 —— 一个空文件会让人以为"采集在跑但什么都没发生",
// 而真相可能是采集端根本没接上
func TestViewDoesNotCreateEmptyFile(t *testing.T) {
	v, _, dir := newView(t)
	if err := v.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, signalViewName)); !os.IsNotExist(err) {
		t.Fatal("一条信号都没有却建了文件")
	}
}
