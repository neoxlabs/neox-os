package agent

import "fmt"

// Scripted 确定性"模型" —— 按脚本产出工具调用.
//
// 为什么先用它验证:
//
//	OS 侧的正确性**不该依赖某个模型的心情**. 用真模型跑, 一次失败你分不清
//	是 OS 错了还是模型抽了. 脚本化之后, 任何失败都一定是 OS 的问题.
//
// 它产出的是**真的工具调用**, 落到真的文件系统上, 被真的内核约束 ——
// 假的只有"决定下一步做什么"这一环.
type Scripted struct {
	Steps []Step
	i     int
}

func (s *Scripted) Name() string { return "scripted" }

func (s *Scripted) Next(task string, history []Observation) (Step, error) {
	if s.i >= len(s.Steps) {
		return Step{}, fmt.Errorf("脚本走完了但没有收工步")
	}
	st := s.Steps[s.i]
	s.i++
	return st, nil
}

// FrontendProject 一个真实的小任务: 建一个能打开的前端工程.
//
// 刻意包含一步**越界写入** (/etc/neox-test.conf) —— 那是用来验证
// "越界不是崩, 是问人"的. 能力集只给了 write:/work/site.
func FrontendProject() []Step {
	return []Step{
		{Thought: "先看看工作目录里有什么", Tool: "list_dir", Args: map[string]any{"path": "."}},
		{Thought: "写页面骨架", Tool: "write_file", Args: map[string]any{
			"path": "site/index.html", "content": indexHTML}},
		{Thought: "写样式", Tool: "write_file", Args: map[string]any{
			"path": "site/style.css", "content": styleCSS}},
		{Thought: "写交互脚本", Tool: "write_file", Args: map[string]any{
			"path": "site/app.js", "content": appJS}},
		{Thought: "顺手把配置写到 /etc 去", Tool: "write_file", Args: map[string]any{
			"path": "/etc/neox-test.conf", "content": "这一步应当越界"}},
		{Thought: "回读确认页面写对了", Tool: "read_file", Args: map[string]any{
			"path": "site/index.html"}},
		{Thought: "列一下产出", Tool: "list_dir", Args: map[string]any{"path": "site"}},
		{Done: "前端工程建好了: index.html + style.css + app.js"},
	}
}

const indexHTML = `<!doctype html>
<html lang="zh">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Neox OS · 计数器</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <main>
    <h1>跑在 Neox OS 上</h1>
    <p class="hint">这个页面由一个被内核约束的 agent 进程写出来</p>
    <button id="btn">点了 <span id="n">0</span> 次</button>
  </main>
  <script src="app.js"></script>
</body>
</html>
`

const styleCSS = `:root { color-scheme: light dark; }
body {
  margin: 0; min-height: 100vh; display: grid; place-items: center;
  font: 16px/1.6 system-ui, sans-serif;
}
main { text-align: center; }
h1 { font-weight: 600; margin-bottom: .25rem; }
.hint { opacity: .6; margin-top: 0; }
button {
  font: inherit; padding: .6rem 1.2rem; border-radius: 8px;
  border: 1px solid currentColor; background: transparent; cursor: pointer;
}
`

const appJS = `const btn = document.getElementById('btn');
const n = document.getElementById('n');
let count = 0;
btn.addEventListener('click', () => { n.textContent = ++count; });
`
