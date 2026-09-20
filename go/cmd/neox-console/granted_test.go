package main

import (
	"strings"
	"sync"
	"testing"
)

/**
 * **批了之后得有人叫它一声**.
 *
 *	例如申请连 pypi 装 pytest, 批了之后它回"授权已生效, 下一句话
 *	我就装并跑测试", 然后停在那儿 —— budget.py 已经写好躺在工作区里,
 *	主干还坏着, 中间只差一句"继续". 而刚点完同意的那个人, 最可能正在离开.
 *
 *	它没做错: 内核约束在进程启动时定死, 这一轮确实用不上新授权
 *	(见 agent/access.go). 问题在**谁来说那句"继续"**.
 */
func Test同一条决策只叫一次(t *testing.T) {
	nudged = sync.Map{}
	if _, again := nudged.LoadOrStore("d1", true); again {
		t.Fatal("第一次就说已经叫过了")
	}
	if _, again := nudged.LoadOrStore("d1", true); !again {
		t.Fatal("同一条决策叫了第二次 —— 那会变成催命")
	}
	if _, again := nudged.LoadOrStore("d2", true); again {
		t.Fatal("把别的决策也当成叫过了")
	}
}

// 说清楚批的是什么, 它才知道接着干哪件事
func Test叫它的时候说清批的是什么(t *testing.T) {
	text := "[系统] 你申请的「允许连这几个吗?」批下来了。**接着把刚才那件事做完**"
	if !strings.Contains(text, "接着把刚才那件事做完") {
		t.Fatal("没说要它接着干")
	}
	// 只取标题头一行: 决策标题里常常带着一串域名, 整段贴过去是噪音
	if got := firstLine("允许连这几个吗?\n  pypi.org\n  files.pythonhosted.org", 60); got != "允许连这几个吗?" {
		t.Fatalf("标题没取头一行: %q", got)
	}
}
