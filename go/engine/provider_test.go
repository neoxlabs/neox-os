package engine

import "testing"

// 窗口大小只认**已验证的上限**, 不认文档.
//
// 窗口值如果低于实际能力, 会把可用上下文提前折叠; 如果高于实际能力,
// 请求会在服务端被拒绝. 超限请求返回
// `maximum context length is 1048576 tokens`, v4-flash / v4-pro / chat /
// reasoner 四个模型的上限相同, 因此这里使用 1048576.
//
// 少算 8 倍不只是"没用满": 上下文预算按窗口算, 预算小就折叠早,
// 而每折一次就打断一次前缀缓存.
func TestDeepSeekContextWindowIsMeasuredNotGuessed(t *testing.T) {
	const measured = 1048576
	for _, m := range []string{
		"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-chat", "deepseek-reasoner",
	} {
		if got := knownContextTokens(m); got != measured {
			t.Fatalf("%s 的窗口是 %d, 实测是 %d —— 这个数只能来自实测", m, got, measured)
		}
	}
	// 不认识的仍然说不知道 —— 编一个数字比不知道更危险
	if got := knownContextTokens("some-new-model"); got != 0 {
		t.Fatalf("不认识的模型该返回 0, 得到 %d", got)
	}
}
