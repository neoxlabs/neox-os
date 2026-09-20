package osinit

import (
	"math"
	"testing"
)

// metersApart 两点差多少米 —— 判精度只能用米, 度数看不出大小
func metersApart(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := (lat2 - lat1) * 111320.0
	dLon := (lon2 - lon1) * 111320.0 * math.Cos((lat1+lat2)/2*math.Pi/180)
	return math.Sqrt(dLat*dLat + dLon*dLon)
}

// 拿**百度官方接口的返回**当基准.
//
//	这一条是这组测试的全部意义: 一套自己写的偏移公式, 唯一能证明它对
//	的办法是跟权威实现比. 数是坐标点(32.919, 117.357)从
//	api.map.baidu.com/geoconv/v1 换回来的.
//
//	差一米以内就算过 —— 一米对我们是零, 地点判定的半径是 150 米.
func TestWGS84ToBD09MatchesBaidu(t *testing.T) {
	const (
		lat, lon       = 32.919, 117.357
		wantLat        = 32.923617318555397
		wantLon        = 117.36922210983838
		tolerateMeters = 1.0
	)
	gotLat, gotLon := WGS84ToBD09(lat, lon)
	if d := metersApart(wantLat, wantLon, gotLat, gotLon); d > tolerateMeters {
		t.Fatalf("跟百度官方差了 %.1f 米: 我们 %.8f,%.8f 官方 %.8f,%.8f",
			d, gotLat, gotLon, wantLat, wantLon)
	}
}

// 偏移**有多大**要有个测试说出来 —— 它是"为什么非转不可"的依据.
//
//	该坐标点上的偏移是 1.2 公里. 不是几十米的事: 不转换就把"家"标到了
//	一公里外的另一个小区
func TestOffsetIsBigEnoughToMatter(t *testing.T) {
	lat, lon := 32.919, 117.357
	bLat, bLon := WGS84ToBD09(lat, lon)
	if d := metersApart(lat, lon, bLat, bLon); d < 500 {
		t.Fatalf("偏移只有 %.0f 米 —— 那这套转换的必要性要重新想", d)
	}
}

// 来回一趟要回得来 —— 回不来的话, 任何"存百度坐标、按 WGS 匹配"的
// 代码都会静默地漂
func TestRoundTrip(t *testing.T) {
	for _, p := range [][2]float64{
		{32.919, 117.357},   // 真机那个点
		{39.9042, 116.4074}, // 北京
		{31.2304, 121.4737}, // 上海
		{22.5431, 114.0579}, // 深圳
	} {
		bLat, bLon := WGS84ToBD09(p[0], p[1])
		back1, back2 := BD09ToWGS84(bLat, bLon)
		if d := metersApart(p[0], p[1], back1, back2); d > 5 {
			t.Fatalf("%v 来回一趟差了 %.1f 米", p, d)
		}
	}
}

// **境外不偏** —— 在境外照样加的话, 一个在东京的人会被挪到几百米外,
// 而那时候没有任何一处会说
func TestOutOfChinaUntouched(t *testing.T) {
	for _, p := range [][2]float64{
		{35.6762, 139.6503},  // 东京
		{37.7749, -122.4194}, // 旧金山
	} {
		gLat, gLon := WGS84ToGCJ02(p[0], p[1])
		if gLat != p[0] || gLon != p[1] {
			t.Fatalf("%v 在境外却被偏移了: %v,%v", p, gLat, gLon)
		}
	}
}
