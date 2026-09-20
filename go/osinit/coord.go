package osinit

import "math"

// 坐标系 —— 国内那套偏移, **本地算, 不调接口**.
//
// ── 为什么这件事非做不可 ──
//
//	手机报的是 WGS-84(安卓 LocationManager 的原始输出, 全世界通用).
//	而国内的地图服务用的是另外两套:
//
//	  GCJ-02  "火星坐标". 高德、腾讯、以及国内所有公开出版的地图
//	  BD-09   百度在 GCJ-02 上又加了一层偏移
//
//	差多少? 在(32.919, 117.357)这个点, 以百度官方 geoconv 为基准:
//	**WGS-84 → BD-09 偏了 1.25 公里**. 不是几十米的事 ——
//	不转换就把"家"标到了一公里外的另一个小区.
//
// ── 为什么不调百度那个接口 ──
//
//	调得通, 但没必要, 而且是三重代价: 一个要申请、要实名、有日配额、
//	还得配 IP 白名单的东西, 换来的是一段**十几行的纯函数**.
//
//	而且它是同步依赖: 转一次坐标要一次网络往返, 失败了这条路就断.
//	对一个 24 小时跑着的东西来说, 每一个外部依赖都是一处会在半夜坏掉
//	的地方.
//
// ── 精度 ──
//
//	这套公式跟百度官方接口的差在**一米以内**(coord_test.go 拿官方返回
//	当基准锁着). 一米对我们是零 —— 地点判定的半径是 150 米.
//
// ── 什么时候用 ──
//
//	**内部一律用 WGS-84**: 手机报什么就存什么, 地点匹配是坐标比坐标,
//	两边同一套, 偏移自己抵消. 转换只在**跟国内地图服务打交道**的那一刻
//	发生 —— 画在百度地图上、送进百度路线规划. 转早了的话, 整个账本
//	就变成了一套没人说得清是哪个坐标系的数.

const (
	// xPi GCJ-02 那套公式里的常数
	xPi = math.Pi * 3000.0 / 180.0
	// ee 克拉索夫斯基椭球的偏心率平方
	ee = 0.00669342162296594323
	// aAxis 长半轴(米)
	aAxis = 6378245.0
)

// WGS84ToGCJ02 → 火星坐标(高德/腾讯/国内出版地图)
func WGS84ToGCJ02(lat, lon float64) (float64, float64) {
	// **境外不偏**: 那套偏移只加在国境内. 在境外照样加的话, 一个在
	// 东京的人会被挪到几百米外 —— 而那时候没有任何一处会说
	if outOfChina(lat, lon) {
		return lat, lon
	}
	dLat := transformLat(lon-105.0, lat-35.0)
	dLon := transformLon(lon-105.0, lat-35.0)
	radLat := lat / 180.0 * math.Pi
	magic := math.Sin(radLat)
	magic = 1 - ee*magic*magic
	sqrtMagic := math.Sqrt(magic)
	dLat = (dLat * 180.0) / ((aAxis * (1 - ee)) / (magic * sqrtMagic) * math.Pi)
	dLon = (dLon * 180.0) / (aAxis / sqrtMagic * math.Cos(radLat) * math.Pi)
	return lat + dLat, lon + dLon
}

// GCJ02ToWGS84 反过来 —— **是近似的**.
//
//	那套偏移不可逆(它是一个查不到闭式解的变换), 通用做法是拿正向变换
//	迭代逼近. 这里用最简单的一次反推: 误差在几米量级, 对 150 米的
//	地点半径完全够.
func GCJ02ToWGS84(lat, lon float64) (float64, float64) {
	if outOfChina(lat, lon) {
		return lat, lon
	}
	mLat, mLon := WGS84ToGCJ02(lat, lon)
	return lat*2 - mLat, lon*2 - mLon
}

// GCJ02ToBD09 百度在火星坐标上又加的那一层
func GCJ02ToBD09(lat, lon float64) (float64, float64) {
	z := math.Sqrt(lon*lon+lat*lat) + 0.00002*math.Sin(lat*xPi)
	theta := math.Atan2(lat, lon) + 0.000003*math.Cos(lon*xPi)
	return z*math.Sin(theta) + 0.006, z*math.Cos(theta) + 0.0065
}

// BD09ToGCJ02 剥掉百度那一层
func BD09ToGCJ02(lat, lon float64) (float64, float64) {
	x, y := lon-0.0065, lat-0.006
	z := math.Sqrt(x*x+y*y) - 0.00002*math.Sin(y*xPi)
	theta := math.Atan2(y, x) - 0.000003*math.Cos(x*xPi)
	return z * math.Sin(theta), z * math.Cos(theta)
}

// WGS84ToBD09 手机报的 → 百度那套. 送进百度任何接口之前都要过这一道
func WGS84ToBD09(lat, lon float64) (float64, float64) {
	g1, g2 := WGS84ToGCJ02(lat, lon)
	return GCJ02ToBD09(g1, g2)
}

// BD09ToWGS84 百度那套 → 手机报的
func BD09ToWGS84(lat, lon float64) (float64, float64) {
	g1, g2 := BD09ToGCJ02(lat, lon)
	return GCJ02ToWGS84(g1, g2)
}

// outOfChina 在不在国境内(粗框).
//
//	粗一点是**故意的**: 判错的代价不对称 —— 把境内点判成境外, 结果是
//	地图上偏一公里; 把境外点判成境内, 结果是偏几百米. 而这个粗框
//	宁可多框一点海和边境, 也不漏掉边境上的城市.
func outOfChina(lat, lon float64) bool {
	return lon < 72.004 || lon > 137.8347 || lat < 0.8293 || lat > 55.8271
}

func transformLat(x, y float64) float64 {
	ret := -100.0 + 2.0*x + 3.0*y + 0.2*y*y + 0.1*x*y + 0.2*math.Sqrt(math.Abs(x))
	ret += (20.0*math.Sin(6.0*x*math.Pi) + 20.0*math.Sin(2.0*x*math.Pi)) * 2.0 / 3.0
	ret += (20.0*math.Sin(y*math.Pi) + 40.0*math.Sin(y/3.0*math.Pi)) * 2.0 / 3.0
	ret += (160.0*math.Sin(y/12.0*math.Pi) + 320*math.Sin(y*math.Pi/30.0)) * 2.0 / 3.0
	return ret
}

func transformLon(x, y float64) float64 {
	ret := 300.0 + x + 2.0*y + 0.1*x*x + 0.1*x*y + 0.1*math.Sqrt(math.Abs(x))
	ret += (20.0*math.Sin(6.0*x*math.Pi) + 20.0*math.Sin(2.0*x*math.Pi)) * 2.0 / 3.0
	ret += (20.0*math.Sin(x*math.Pi) + 40.0*math.Sin(x/3.0*math.Pi)) * 2.0 / 3.0
	ret += (150.0*math.Sin(x/12.0*math.Pi) + 300.0*math.Sin(x/30.0*math.Pi)) * 2.0 / 3.0
	return ret
}
