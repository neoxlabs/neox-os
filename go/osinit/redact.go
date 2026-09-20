package osinit

import (
	"math"

	"github.com/neox-os/neox-os/abi"
)

// 落账本之前把最危险的那几样收一收.
//
// ── 这道闸为什么在"记"这一步 ──
//
//	账本只增不删 —— 那是这套系统的骨架. 也就是说: 这一刻记下去的东西,
//	一年以后还在, 而且会跟着备份、跟着迁移、跟着任何一次导出走.
//
//	感知层收的是位置、来电、心率. **一份精确到米、逐分钟的行踪记录,
//	是这台机器上最危险的一个文件** —— 比任何密钥都危险, 因为密钥换得掉.
//	它一旦落进去, 后面任何"读的时候过滤一下"都是自欺: 文件还在那儿.
//
// ── 判断不受影响 ──
//
//	观察链拿到的是**原始信号**(见 SignalBus.ingest 里 observe 那一行):
//	地点匹配、在场推断、世界模型、规则求值, 全都还在用精确值.
//	粗糙的只有那份留下来的.
//
//	换句话说: **精确值活在内存里, 跟着进程一起死; 账本里留的是够用的
//	那一份.** 这跟"进程会死, 状态在 OS 里"是同一条思路 ——
//	只是这次要故意让某些东西死掉.

// coordPlaces 小数点后留几位.
//
//	3 位 ≈ 110 米. 挑这个数是因为地点判定的缺省半径是 150 米
//	(defaultPlaceRadius) —— 也就是说, 再精确也不会让任何一次判断
//	变得更对, 而每多一位, 那份记录就更能还原出一条轨迹.
const coordPlaces = 3

// RedactForLedger 记进账本的那一份.
//
//	places 认得出这儿是哪儿的话, 就**只记地名, 坐标整个丢掉** ——
//	"在公司"是这条记录唯一有用的部分, 而经纬度只是它的一种更危险的写法.
//
//	认不出就降精度留着: 完全丢掉的话, 补传恢复(RecoverPending)那一路
//	会连"他当时在某个地方"都说不出来.
func RedactForLedger(places *Places) func(abi.Signal) map[string]any {
	return func(s abi.Signal) map[string]any {
		if len(s.Body) == 0 {
			return s.Body
		}
		lat, hasLat := floatOf(s.Body["lat"])
		lon, hasLon := floatOf(s.Body["lon"])
		if !hasLat || !hasLon {
			return s.Body
		}
		// 复制一份再改 —— **原件还要交给观察链**, 就地改的话
		// 地点匹配和世界模型拿到的会是被抹过的那份
		out := make(map[string]any, len(s.Body))
		for k, v := range s.Body {
			out[k] = v
		}
		if places != nil {
			if name := places.Lookup(lat, lon); name != "" {
				delete(out, "lat")
				delete(out, "lon")
				// 精度字段跟着坐标一起走 —— 单独留着它是没有意义的,
				// 而它同样能帮着还原轨迹
				delete(out, "acc")
				delete(out, "accuracy")
				out["place"] = name
				return out
			}
		}
		out["lat"] = roundTo(lat, coordPlaces)
		out["lon"] = roundTo(lon, coordPlaces)
		return out
	}
}

func roundTo(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
