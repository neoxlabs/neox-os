package agent

import (
	"strings"
	"testing"
)

// **两条路都要认 radius**.
//
//	模型传了 radius=600, 走 address 那条分支时,
//	而另一条路径**把参数默默吞了** —— 账本里存的是缺省 250, 而代码却说
//	"半径 600 米"。这会让对外承诺与实际执行不一致, 而且没有任何一处报错。
func TestPlacePassesRadiusDownBothPaths(t *testing.T) {
	var hereR, addrR float64
	ts := NewToolSet(WithPlace(DefaultTools(),
		func(name string, r float64, _ bool) (string, error) { hereR = r; return "ok", nil },
		func(name, addr string, r float64) (string, error) { addrR = r; return "ok", nil }, nil))
	tool, ok := ts.Get("place")
	if !ok {
		t.Fatal("没有 place")
	}
	if _, err := tool.Run(Toolbox{}, map[string]any{
		"name": "家", "radius": "600"}); err != nil {
		t.Fatal(err)
	}
	if hereR != 600 {
		t.Errorf("记此刻位置那条没收到 radius: %v", hereR)
	}
	if _, err := tool.Run(Toolbox{}, map[string]any{
		"name": "家", "address": "蚌埠碧桂园", "radius": "600"}); err != nil {
		t.Fatal(err)
	}
	if addrR != 600 {
		t.Errorf("按地址记那条没收到 radius: %v —— 它会存成缺省值, "+
			"而模型已经对用户说了 600", addrR)
	}
}

// 工具说明里要提 radius —— 不提的话模型不知道能调
func TestPlaceToolMentionsRadius(t *testing.T) {
	tool, _ := NewToolSet(WithPlace(DefaultTools(),
		func(string, float64, bool) (string, error) { return "", nil }, nil, nil)).Get("place")
	if _, ok := tool.Args["radius"]; !ok {
		t.Fatal("place 没有 radius 参数 —— 圈小了他人在里面而系统说不认识")
	}
	if !strings.Contains(tool.Args["radius"], "250") {
		t.Error("没说清不填按多少算")
	}
}
