package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 没配搜索 = 工具表里根本没有 web_search.
//
// **摆一个配不出结果的工具比没有更糟**: 模型会先对用户许诺"我去搜一下",
// 再拿到一句"这台机器没配搜索" —— 用户看到的是一次失约
func TestNoSearchMeansNoTool(t *testing.T) {
	ts := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil, nil, nil, nil))
	if _, ok := ts.Get("web_search"); ok {
		t.Fatal("没配搜索却挂上了 web_search")
	}
	// fetch 不在这个开关下 —— 它不需要任何配置,
	// 能不能出网由能力集说了算, 而那是运行时才知道的事
	if _, ok := ts.Get("fetch"); !ok {
		t.Fatal("fetch 该一直在: 它不依赖任何配置")
	}

	with := NewToolSet(DefaultToolsWith(nil, nil, nil, nil,
		func(string, int) ([]abi.SearchHit, error) { return nil, nil },
		func(mediaType, dataB64, question string) (abi.SeeResult, error) { return abi.SeeResult{}, nil },
		nil, nil, nil))
	if _, ok := with.Get("web_search"); !ok {
		t.Fatal("配了搜索却没挂上工具")
	}
}

// 搜索结果里的链接单独成行, 便于原样传给 fetch —— 混在一段话里抄错一个字符就是一次白跑
func TestHitsAreCopyable(t *testing.T) {
	got := formatWebHits("landlock abi", []abi.SearchHit{
		{Title: "Landlock ABI 版本", URL: "https://docs.kernel.org/x/landlock.html",
			Snippet: "ABI 4 加了几个访问位"},
	})
	if !strings.Contains(got, "\n   https://docs.kernel.org/x/landlock.html\n") {
		t.Fatalf("链接没有单独一行: %q", got)
	}
	// 只给摘要的话模型会拿它当事实直接回答, 而摘要是搜索引擎截的, 常常过期
	if !strings.Contains(got, "fetch") {
		t.Fatalf("没提醒它去取正文: %q", got)
	}
}

// 空结果不是错误, 是一个真实的答案: 换词, 别原样重搜
func TestEmptyResultTellsItToRephrase(t *testing.T) {
	tool := SearchTool(func(string, int) ([]abi.SearchHit, error) { return nil, nil })
	_, err := tool.Run(Toolbox{}, map[string]any{"query": "某个搜不到的词"})
	if err == nil {
		t.Fatal("空结果要说出来")
	}
	if !strings.Contains(err.Error(), "换一组关键词") {
		t.Fatalf("没给下一步, 它只会重搜同一条: %v", err)
	}
}

// 搜索**不声明 net 能力**: 它走 OS, 进程的 netns 里可以一张网卡都没有.
// 而这不是后门 —— 它拿不到正文, 拿正文要走 fetch, fetch 是 net 轴的
func TestSearchNeedsNoNetAxisButFetchDoes(t *testing.T) {
	if SearchTool(nil).Needs != "" {
		t.Fatal("web_search 不该要能力轴 —— 它不出网")
	}
	if fetchTool(false).Needs != abi.AxisNet {
		t.Fatal("fetch 必须走 net 轴, 否则取正文就绕过了出网审批")
	}
}

// 搜索侧的错误要原样传上去, 不能被裹成一句"搜索失败" ——
// "这台机器没配搜索"和"key 不对"的下一步完全不同
func TestSearchErrorPassesThrough(t *testing.T) {
	tool := SearchTool(func(string, int) ([]abi.SearchHit, error) {
		return nil, errors.New("这台机器没有配置搜索服务")
	})
	_, err := tool.Run(Toolbox{}, map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "没有配置搜索服务") {
		t.Fatalf("错误被吞了或改写了: %v", err)
	}
}
