package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// 拿到最后一条发给界面的卡片
func lastCard(t *testing.T, f *fakeSys) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.events) - 1; i >= 0; i-- {
		text, ok := f.events[i]["text"].(string)
		if !ok || f.events[i]["channel"] != "ui" {
			continue
		}
		var spec map[string]any
		if err := json.Unmarshal([]byte(text), &spec); err != nil {
			t.Fatalf("发出去的卡片不是合法 JSON: %v", err)
		}
		return spec
	}
	t.Fatal("一条 ui 卡片都没发出去")
	return nil
}

// 发出去的东西必须是渲染端认得的形状 —— type 和 id 都得是字符串,
// 缺任何一个都会被静默丢弃, 而工具却报了成功.
func TestShowEmitsRenderableSpec(t *testing.T) {
	f := newFakeSys()
	given := map[string]any{"place": "杭州", "temp": 31, "summary": "多云"}
	out, err := showTool().Run(Toolbox{Sys: f}, map[string]any{"kind": "weather", "spec": given})
	if err != nil {
		t.Fatalf("展示失败: %v", err)
	}
	spec := lastCard(t, f)
	if spec["type"] != "weather" {
		t.Errorf("type = %v, 要 weather", spec["type"])
	}
	if id, _ := spec["id"].(string); id == "" {
		t.Errorf("id 是空的 —— 渲染端会把这张卡丢掉")
	}
	if spec["place"] != "杭州" {
		t.Errorf("原字段没带过去: %v", spec)
	}
	// 别改调用方的 map —— 补 type/id 是发出去那一份的事
	if _, dirty := given["type"]; dirty {
		t.Errorf("工具把调用方的 spec 改脏了: %v", given)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("给模型的回执不对: %q", out)
	}
}

// spec 给一整段 JSON 字符串也算数 —— 模型两种写法都会出现
func TestShowAcceptsSpecAsString(t *testing.T) {
	f := newFakeSys()
	if _, err := showTool().Run(Toolbox{Sys: f}, map[string]any{
		"kind": "image", "spec": `{"src":"https://x/y.png","caption":"图"}`,
	}); err != nil {
		t.Fatalf("字符串 spec 被拒了: %v", err)
	}
	if got := lastCard(t, f)["src"]; got != "https://x/y.png" {
		t.Errorf("src = %v", got)
	}
}

// 缺那个"没有它就等于没有"的字段, 必须当场拒 ——
// 放过去的话界面上是空的, 而模型以为它已经展示过了.
func TestShowRejectsEmptyCard(t *testing.T) {
	for _, c := range []struct{ kind, missing string }{
		{"image", "src"}, {"code", "code"}, {"table", "columns"}, {"weather", "place"},
	} {
		f := newFakeSys()
		_, err := showTool().Run(Toolbox{Sys: f}, map[string]any{
			"kind": c.kind, "spec": map[string]any{"title": "有标题但没内容"},
		})
		if err == nil {
			t.Errorf("%s 缺 %s 竟然过了", c.kind, c.missing)
			continue
		}
		if !strings.Contains(err.Error(), c.missing) {
			t.Errorf("%s 的报错没说清缺什么: %v", c.kind, err)
		}
		f.mu.Lock()
		n := len(f.events)
		f.mu.Unlock()
		if n != 0 {
			t.Errorf("%s 被拒了却还是发了 %d 条事件", c.kind, n)
		}
	}
}

/**
 * 没说种类才报"有哪些" —— 而不是"表外的一律挡回去".
 *
 *	这条以前是反的: 表外的种类当场拒绝, 理由是界面会静默丢弃.
 *	界面改成"认不出就照字段画一张通用卡"之后那条理由没了, 而挡着的代价
 *	是种类被一张 Go 里的表封死. 见 Test表外的卡片种类也发得出去.
 */
func TestShow没说种类时报出可用的那几个(t *testing.T) {
	_, err := showTool().Run(Toolbox{Sys: newFakeSys()}, map[string]any{
		"kind": "", "spec": map[string]any{"src": "x"},
	})
	if err == nil {
		t.Fatal("没说种类竟然过了")
	}
	if !strings.Contains(err.Error(), "image") || !strings.Contains(err.Error(), "weather") {
		t.Errorf("报错里没列出可用种类: %v", err)
	}
}

// 同一张卡发两次, id 不能撞 —— id 是界面认回答的钥匙,
// 撞了就会"答了第一张, 第二张跟着变成已答"
func TestShowIDsDoNotCollide(t *testing.T) {
	f := newFakeSys()
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		// 每次都新建 —— 复用同一个 map 的话, 上一次补进去的 id
		// 会混进这一次的哈希, 于是"不撞"是假的
		args := map[string]any{"kind": "doc", "spec": map[string]any{"title": "同一张", "body": "同样内容"}}
		if _, err := showTool().Run(Toolbox{Sys: f}, args); err != nil {
			t.Fatalf("第 %d 次失败: %v", i, err)
		}
		id := lastCard(t, f)["id"].(string)
		if seen[id] {
			t.Fatalf("第 %d 次的 id 撞了: %s", i, id)
		}
		seen[id] = true
	}
}

// 没有界面的时候要说人话, 不能崩
func TestShowWithoutUISaysSo(t *testing.T) {
	_, err := showTool().Run(Toolbox{}, map[string]any{
		"kind": "doc", "spec": map[string]any{"title": "t", "body": "b"},
	})
	if err == nil || !strings.Contains(err.Error(), "说出来") {
		t.Errorf("没界面时该让它把内容说出来, 实际: %v", err)
	}
}

// **进度卡还得真有个进度**.
//
// title 有了就放行的话, 一张不带数值的进度卡照样发得出去, 而它在界面上
// 画出来是空条 + 0% —— 不报错, 只是画了个假数. 那比报错坏: 模型接着说
// "进度如上", 而"上面"写着 0%.
func TestProgressCardNeedsAValue(t *testing.T) {
	show, _ := NewToolSet(DefaultTools()).Get("show")
	box := Toolbox{Sys: &fakeSys{}}
	_, err := show.Run(box, map[string]any{"kind": "progress",
		"spec": `{"title":"接口核对"}`})
	if err == nil {
		t.Fatal("没有进度值也放行了 —— 界面上会画成 0%, 而模型以为它展示了进度")
	}
	// 报错要说清**给什么才行**, 不是只说不行
	for _, want := range []string{"done", "percent", "ratio"} {
		if !contains(err.Error(), want) {
			t.Errorf("没告诉它可以给 %s: %v", want, err)
		}
	}
}

// done/total 是最自然的写法, 必须收
func TestProgressAcceptsDoneTotal(t *testing.T) {
	show, _ := NewToolSet(DefaultTools()).Get("show")
	if _, err := show.Run(Toolbox{Sys: &fakeSys{}}, map[string]any{"kind": "progress",
		"spec": `{"title":"接口核对","done":5,"total":8}`}); err != nil {
		t.Fatalf("给了 5/8 反而被拒: %v", err)
	}
}

/**
 * 种类这张表**不是封死的**.
 *
 *	原来表外的种类当场被挡回去, 理由是"界面对不认识的会静默丢弃".
 *	界面改成"认不出就照字段画一张通用卡"之后, 那条理由没了 —— 而挡着的
 *	代价是这套东西的种类被一张 Go 里的表封死: 加一种要改两处再发一次版.
 */
func Test表外的卡片种类也发得出去(t *testing.T) {
	f := newFakeSys()
	out, err := showTool().Run(Toolbox{Sys: f},
		map[string]any{"kind": "flight", "spec": map[string]any{"title": "MU5100", "from": "虹桥"}})
	if err != nil {
		t.Fatalf("表外的种类被挡了: %v", err)
	}
	// **要如实说它长什么样**: 不说的话模型以为有一张专门设计过的卡,
	// 接着就会说"如上图所示的航班信息"
	if !strings.Contains(out, "通用卡") {
		t.Errorf("没告诉它用户看到的是通用卡:\n%s", out)
	}
	if len(f.events) == 0 {
		t.Fatal("卡片没发出去")
	}
}

func Test表外的空卡片还是要挡(t *testing.T) {
	f := newFakeSys()
	// 一个字段都没有的话, 通用卡也画不出东西来 —— 界面上就是个空框
	if _, err := showTool().Run(Toolbox{Sys: f},
		map[string]any{"kind": "flight", "spec": map[string]any{}}); err == nil {
		t.Fatal("空卡片也发出去了")
	}
}

func Test没说种类要报可用的那几个(t *testing.T) {
	f := newFakeSys()
	_, err := showTool().Run(Toolbox{Sys: f},
		map[string]any{"kind": "", "spec": map[string]any{"title": "x"}})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("没提示有哪些常见种类: %v", err)
	}
}

// 卡片上写了什么, 工具要告诉它 —— 卡片写着"固镇县庙岗路",
// 它只拿到"发过去了", 于是配了一句"系统认不出是哪儿"
func TestWhereMapTellsWhatTheCardSays(t *testing.T) {
	f := newFakeSys()
	tool := WhereTool(WhereKit{
		Now: func() string { return "" },
		Card: func(string) (map[string]any, error) {
			return map[string]any{"type": "map", "title": "我现在在这儿",
				"sub": "固镇县庙岗路，汇金国际碧桂苑内", "lat": 33.3, "lon": 117.3}, nil
		},
	})
	out, err := tool.Run(Toolbox{Sys: f}, map[string]any{"map": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "固镇县庙岗路，汇金国际碧桂苑内") || strings.Contains(out, "我现在在这儿") {
		t.Fatalf("%q", out)
	}
	if lastCard(t, f)["type"] != "map" {
		t.Fatal("卡片没发出去")
	}
}
