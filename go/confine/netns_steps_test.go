package confine

import (
	"strings"
	"testing"
)

// 只有回环的命名空间: lo 必须拉起来, 而且不能有 veth.
//
// 没有出网授权时, 进程若只进入 `unshare --net` 的匿名 ns, 里面的 lo
// 是**存在但 DOWN 的**(内核默认). 于是 127.0.0.1 谁也连不上, HTTP
// 服务和本地验证都会失败, 直到额外执行 `ip link set lo up`.
// 一件本该由 OS 保证的事不能变成调用方的补救步骤.
func TestLoopbackOnlyBringsLoUp(t *testing.T) {
	steps := loopbackOnlySteps("neox-p1", "/sbin/ip")
	var flat []string
	for _, s := range steps {
		flat = append(flat, strings.Join(s, " "))
	}
	joined := strings.Join(flat, " | ")
	if !strings.Contains(joined, "link set lo up") {
		t.Fatalf("lo 默认是 down 的, 必须拉起来, 否则 127.0.0.1 都连不上: %s", joined)
	}
	if !strings.Contains(joined, "netns add neox-p1") {
		t.Fatalf("没建命名空间: %s", joined)
	}
}

// 没有出网授权就是**真的没有路可走**, 不是"规则拒绝".
// 一旦这里冒出 veth 或地址或路由, 断网就从拓扑保证退化成配置约定.
func TestLoopbackOnlyHasNoWayOut(t *testing.T) {
	joined := ""
	for _, s := range loopbackOnlySteps("neox-p1", "/sbin/ip") {
		joined += strings.Join(s, " ") + " | "
	}
	for _, forbidden := range []string{"veth", "addr add", "route", "default"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("只有回环的 ns 里出现了 %q —— 断网不再由拓扑保证: %s", forbidden, joined)
		}
	}
}
