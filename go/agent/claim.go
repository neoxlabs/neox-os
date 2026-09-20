package agent

import "strings"

// 说了"记下了" —— 见 Agent.Run 回复那一段.

// recordTools 往他的数据里记东西的那几个工具.
var recordTools = map[string]bool{
	"remember": true, "agenda": true, "place": true,
	"notify_when": true, "remind_me": true,
}

// claimWords 过去时的明说. **不收"会提醒你"这类**: 那常常是在描述已有的
// 安排, 当成"没做到"去补, 补出来的是一条重复的.
var claimWords = []string{"记下了", "记住了", "已记下", "记好了", "已经记下", "给你记上", "记上了", "帮你记了"}

func claimsRecorded(reply string) bool {
	for _, w := range claimWords {
		if strings.Contains(reply, w) {
			return true
		}
	}
	return false
}

// recordedIn 这一轮有没有**成功**调过记录工具.
func recordedIn(history []Observation) bool {
	for _, o := range history {
		if recordTools[o.Tool] && o.Err == "" {
			return true
		}
	}
	return false
}

func (a *Agent) hasRecordTool() bool {
	if a.Tools == nil {
		return false
	}
	for name := range recordTools {
		if _, ok := a.Tools.Get(name); ok {
			return true
		}
	}
	return false
}

// **只说事实, 不规定它下一句说什么**: 原来这段最后一句是"做完只回一个字
// 「好」" —— 而这一轮本来就对他静默, 说什么都到不了他眼前.
const claimNudge = "[系统核对，不是他说的] 你刚对他说了「记下了」，但这一轮没有调用任何记录工具" +
	"（remember / agenda / place / notify_when / remind_me），所以什么都没存下。" +
	"这一轮他看不见，你要补就现在补。早就记过的话（where 里看得到）就不用管。"
