package confine

// 命名空间怎么建 —— 单独拎出来是为了**能在任何平台上验**.
//
// 真正的建立只能在 Linux 上跑(要 root、要 iproute2), 而闸是在开发机上跑的.
// 把"建哪几步"做成纯函数, 于是"只有回环、没有 veth"和"lo 必须拉起来"
// 这两条就不再只能靠真机复验兜着.

// loopbackOnlySteps 只有回环的命名空间: 建 ns, 把 lo 拉起来, 到此为止.
//
// **不加 veth、不加地址、不加路由** —— 没有出网授权就是真的没有路可走,
// 不是"规则拒绝". 而 lo 必须拉起来: 新命名空间里它默认是 DOWN 的,
// 不拉的话 127.0.0.1 谁也连不上, "起个服务再打它验证"整类活全废.
func loopbackOnlySteps(nsName, ipBin string) [][]string {
	return [][]string{
		{"netns", "add", nsName},
		{"netns", "exec", nsName, ipBin, "link", "set", "lo", "up"},
	}
}
