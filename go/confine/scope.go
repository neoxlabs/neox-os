// Package confine 把 Capability 翻译成内核可执行的约束规则.
//
// 这是"能力从问询变强制"的关键一步:
//
//	旧 (agent IDE): can() 返回 false → **指望进程自己不写**.
//	                进程直接调 open 就绕过去了, 检查形同虚设.
//	新 (OS):        能力在 spawn 时翻译成 landlock ruleset + cgroup + netns,
//	                由内核在 syscall 边界上执行. 进程调不调 can() 都一样,
//	                它**做不到**没授权的事.
//
// plan/launcher/scope 全是纯函数, 所以能在任何平台上测.
// 真正施加规则要 Linux.
package confine

import "strings"

// never 路径穿越的哨兵值 —— 永远匹配不上任何东西
const never = "\x00never"

// ScopeMatches 是能力作用域匹配的**全工程唯一实现**.
//
// 两个消费者共用它: planFor (翻译成内核规则) 和 capabilityAllows (预检).
// 各写一份就是双真相源 —— 预检说能内核说不能, 而这种不一致
// 只在生产环境的特定路径上暴露.
func ScopeMatches(axis, granted, requested string) bool {
	switch axis {
	case "read", "write":
		// fs 轴上 '*' = 整个卷根, **不是**无条件放行 ——
		// 内核只可能授到卷内, 预检无条件 true 就跟内核对不上
		g := granted
		if g == "*" {
			g = "/"
		}
		return PathWithin(g, requested)
	case "net":
		return granted == "*" || HostMatches(granted, requested)
	case "proc", "secret":
		return granted == "*" || granted == requested
	}
	return false
}

// PathWithin 路径包含判断.
//
// "/a" 不匹配 "/ab" —— 必须按路径段比, 不能按字符串前缀比.
func PathWithin(grantedPrefix, requested string) bool {
	g := NormalizePath(grantedPrefix)
	r := NormalizePath(requested)
	if g == never || r == never {
		return false
	}
	if g == "/" || r == g {
		return true
	}
	if !strings.HasSuffix(g, "/") {
		g += "/"
	}
	return strings.HasPrefix(r, g)
}

// NormalizePath 路径规范化.
//
//   - 反斜杠统一成正斜杠
//   - **不压缩连续斜杠** —— UNC 路径 \\host\share 压缩后会指向别处
//   - 含 ".." 一律判不匹配, 不做解析 —— 解析等于给自己开后门
func NormalizePath(p string) string {
	out := strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	if len(out) > 1 && strings.HasSuffix(out, "/") {
		out = out[:len(out)-1]
	}
	for _, seg := range strings.Split(out, "/") {
		if seg == ".." {
			return never
		}
	}
	return out
}

// HostMatches host 匹配.
//
// "*.example.com" **不覆盖 example.com 本身** —— 通配符授权不连带父域.
// 端口写了就必须一致.
func HostMatches(granted, requested string) bool {
	gHost, gPort, gHasPort := splitHostPort(granted)
	rHost, rPort, _ := splitHostPort(requested)
	if gHasPort && gPort != rPort {
		return false
	}
	if strings.HasPrefix(gHost, "*.") {
		suffix := gHost[1:] // ".example.com"
		return strings.HasSuffix(rHost, suffix) && len(rHost) > len(suffix)
	}
	return gHost == rHost
}

func splitHostPort(s string) (host, port string, hasPort bool) {
	i := strings.LastIndex(s, ":")
	if i == -1 {
		return strings.ToLower(s), "", false
	}
	return strings.ToLower(s[:i]), s[i+1:], true
}

// CapabilityAllows 能力预检.
//
// 预检**不是安全边界** —— 真正的强制在内核, 进程调不调它都逃不掉.
// 它唯一的价值是让进程提前知道, 少吃一个莫名其妙的 EACCES.
// 但它必须跟内核规则**说同一件事**, 否则 agent 会撞看不见的墙,
// 或者放弃它本来有权做的事.
func CapabilityAllows(caps []Capability, axis, scope string) bool {
	for _, c := range caps {
		if !axisSatisfies(string(c.Axis), axis) {
			continue
		}
		if ScopeMatches(axis, c.Scope, scope) {
			return true
		}
	}
	return false
}

// axisSatisfies 轴的蕴含关系 —— 必须跟 PlanFor 的蕴含**完全一致**.
//
// write 蕴含 read: landlock 里 write 不隐含 read, 所以 PlanFor 授 write 时
// 会同时授 read. 预检不认这条就会告诉 agent "你不能读你自己能写的目录".
func axisSatisfies(granted, requested string) bool {
	return granted == requested || (granted == "write" && requested == "read")
}
