package main

import "testing"

// 批准之后要自己把话接上 —— 用户点"允许"就是"去吧", 没有第二种解释.
//
// 反面情况是批了 pypi 三个主机后, agent 只能收尾说"下一句话随便说句
// '继续'我就装". 用户点了允许, 结果什么都没发生.

func TestApprovalResumesOnceTheTurnEnds(t *testing.T) {
	var g grantResumer
	if g.Fire() {
		t.Fatal("没批过就自己接着干 —— 那是没人叫它它自己动")
	}
	if !g.Arm("yes") {
		t.Fatal("批准要上膛")
	}
	if !g.Fire() {
		t.Fatal("批过了, 收尾之后必须自动接着干")
	}
}

// 只响一次. 不然接下来每一轮结束都自动再说一句, 那是个永动机 ——
// 用户没说话, 它自己跟自己聊下去, 而且每一句都在烧钱.
func TestApprovalFiresOnlyOnce(t *testing.T) {
	var g grantResumer
	g.Arm("yes")
	if !g.Fire() {
		t.Fatal("第一次该响")
	}
	for i := 0; i < 3; i++ {
		if g.Fire() {
			t.Fatalf("第 %d 次又响了 —— 落膛没落干净", i+2)
		}
	}
}

// 拒绝不重开进程: 被拒的能力本来就没有, 旧进程拿到"不行"可以接着想别的办法.
// 对拒绝也重开等于白扔一轮上下文.
func TestDenialDoesNotResume(t *testing.T) {
	for _, ans := range []string{"no", "", "later", "YES"} {
		var g grantResumer
		if g.Arm(ans) {
			t.Fatalf("%q 不是批准, 不该上膛", ans)
		}
		if g.Fire() {
			t.Fatalf("%q 之后不该自动接着干", ans)
		}
	}
}
