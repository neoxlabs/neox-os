package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

/**
 * handoff_log —— **谁把活交给了谁, 要留得下来**.
 *
 * ── 为什么不能只留在对话里 ──
 *
 *	交接发生在一句话里: 接手的人收到一段"这摊活给你了". 而三天之后
 *	想知道"登录这块现在归谁", 得去翻两个人的聊天记录 —— 那时候谁都
 *	记不清是哪一天、哪一句.
 *
 *	而交接恰恰是**项目层面**的事实: 它改变了"这个项目里谁负责什么".
 *	所以它该待在项目卡上, 跟"谁手上还有没合的"排在一起.
 *
 * ── 为什么单独落一份, 不从 git 里推 ──
 *
 *	交接在 git 里的痕迹是"一条分支被 reset 到另一条上" —— 事后从
 *	提交图里认出这件事既不可靠也不直观(reset 不留痕). 一行 JSON
 *	便宜得多, 而且它记的正是那件事本身: 谁、给谁、什么时候、几个文件.
 */

type handoffLine struct {
	Project string   `json:"project"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	At      int64    `json:"at"`
	Files   []string `json:"files,omitempty"`
	Commits int      `json:"commits,omitempty"`
}

var handoffMu sync.Mutex

func handoffPath() string { return filepath.Join(neoxHome(), "handoffs.jsonl") }

// noteHandoff 记一笔. **写不进去不算错** —— 交接本身已经成了,
// 少一行记录不该让那件事失败.
func noteHandoff(project string, got Handover) {
	line := handoffLine{
		Project: realPath(project), From: got.From, To: got.To,
		At: time.Now().UnixMilli(), Files: got.Files, Commits: got.Commits,
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return
	}
	handoffMu.Lock()
	defer handoffMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(handoffPath()), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(handoffPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}

// handoffsIn 这个项目里发生过的交接, 最近的在前
func handoffsIn(project string) []handoffLine {
	handoffMu.Lock()
	defer handoffMu.Unlock()
	raw, err := os.ReadFile(handoffPath())
	if err != nil {
		return nil
	}
	want := realPath(project)
	var out []handoffLine
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var one handoffLine
		if json.Unmarshal([]byte(line), &one) != nil || one.Project != want {
			continue
		}
		out = append(out, one)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}
