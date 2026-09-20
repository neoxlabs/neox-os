//go:build linux

package confine

import "testing"

// 由 open 标志推能力轴 —— 判错方向比判得粗危险得多:
// 一次 open(O_WRONLY) 被当成读, 就等于用读权限放行了一次写.
func TestAxisOfOpenFlags(t *testing.T) {
	cases := []struct {
		name  string
		flags uint64
		want  string
	}{
		{"O_RDONLY", 0, "read"},
		{"O_WRONLY", 0o1, "write"},
		{"O_RDWR", 0o2, "write"},
		{"O_CREAT 单独也算写", 0o100, "write"},
		{"O_TRUNC 也算写", 0o1000, "write"},
		{"O_APPEND 也算写", 0o2000, "write"},
		{"O_RDONLY|O_CLOEXEC 仍是读", 0o2000000, "read"},
		{"O_WRONLY|O_CREAT|O_TRUNC", 0o1 | 0o100 | 0o1000, "write"},
	}
	for _, c := range cases {
		if got := axisOfOpenFlags(c.flags); got != c.want {
			t.Fatalf("[%s] flags=%o got %s want %s", c.name, c.flags, got, c.want)
		}
	}
}
