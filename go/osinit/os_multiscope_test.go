package osinit

import (
	"reflect"
	"testing"
)

// 一条决策授多个目标 —— 一次操作只该打扰用户一次.
//
// 安装一个 Python 包可能要两次审批(pypi.org + files.pythonhosted.org),
// 而这两次问的是同一件事, 因此同一决策需要合并多个目标.
func TestSplitScopes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"pypi.org,files.pythonhosted.org", []string{"pypi.org", "files.pythonhosted.org"}},
		{"a.com, b.com", []string{"a.com", "b.com"}},
		{"a.com a.com b.com", []string{"a.com", "b.com"}}, // 去重
		{"  ", nil},
		{"", nil},
		{"/site", []string{"/site"}}, // 路径也走同一套, 单个照旧
	}
	for _, c := range cases {
		if got := splitScopes(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitScopes(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
