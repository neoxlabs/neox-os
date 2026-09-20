package agent

import (
	"strings"
	"testing"
)

/**
 * 合成了要**单独发一条** —— 主干是所有人共用的那一份.
 *
 *	埋在这个 bot 的工具结果里, 用户只有点进这间屋子才看得见; 而他
 *	可能正在别的会话里, 也可能不在电脑前.
 */
func Test合成了单独发一条(t *testing.T) {
	f := newFakeSys()
	tool := MergeUpTool(func() (string, error) { return "合进主干（master）了。3 files changed", nil })
	if _, err := tool.Run(Toolbox{Sys: f}, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var got map[string]any
	for _, ev := range f.events {
		if ev["phase"] == "merged" {
			got = ev
		}
	}
	if got == nil {
		t.Fatal("合进主干了却没发这条")
	}
	if text, _ := got["text"].(string); !strings.Contains(text, "主干") {
		t.Errorf("这条里没说清发生了什么: %v", got)
	}
}

/**
 * **被"先验一遍"拦下来的时候不许发** —— 那时候主干一个字都没变.
 *
 *	判据是"这次真的合上去了", 不是"调用没报错": 拦下来也是正常返回.
 */
func Test没合上去就不发(t *testing.T) {
	f := newFakeSys()
	tool := MergeUpTool(func() (string, error) {
		return "这次合并从主干带进了 1 个别人改过的文件，先在这儿验一遍", nil
	})
	if _, err := tool.Run(Toolbox{Sys: f}, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events {
		if ev["phase"] == "merged" {
			t.Fatalf("没合上去却发了「合进主干」: %v", ev)
		}
	}
}
