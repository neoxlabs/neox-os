package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

// 多模态请求的形状: content 是**块数组**, 图片走 data: URI.
//
// 这一条钉的是 wire format 本身 —— 发错了各家的表现是一句
// 长得跟"模型不认图"一模一样的 400, 排查会整个走偏
func TestVisionWireShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"报错是 permission denied"}}]}`)
	}))
	defer srv.Close()

	v := NewVision(srv.URL, "k", "some-vision-model")
	v.Client = srv.Client()
	res, err := v.See(context.Background(), abi.SeeParams{
		MediaType: "image/png", DataB64: "QUJD", Question: "报错原文是什么"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "报错是 permission denied" || res.Model != "some-vision-model" {
		t.Fatalf("结果不对: %+v", res)
	}

	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("消息数不对: %v", got["messages"])
	}
	blocks, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("content 该是两个块(文字+图片), 得到 %v", blocks)
	}
	if blocks[0].(map[string]any)["text"] != "报错原文是什么" {
		t.Fatalf("问题没进第一个块: %v", blocks[0])
	}
	img := blocks[1].(map[string]any)["image_url"].(map[string]any)
	if url, _ := img["url"].(string); !strings.HasPrefix(url, "data:image/png;base64,QUJD") {
		t.Fatalf("图片不是 data URI 形状: %q", url)
	}
}

// 没有问题就拦住, **不替它编一个**.
//
// 编出来的问题会看起来像用户问的, 而它其实是我们瞎猜的 ——
// 拿回来的答案也就跟着偏
func TestNoQuestionRefused(t *testing.T) {
	v := NewVision("https://x", "k", "m")
	_, err := v.See(context.Background(), abi.SeeParams{DataB64: "QUJD"})
	if err == nil || !strings.Contains(err.Error(), "具体问题") {
		t.Fatalf("没问题该被拦: %v", err)
	}
}

// **"这个模型不认图"必须单独说.**
//
// 配错模型是这条路上最常见的错, 而它的回包跟别的 400 长得一样.
// 混成一句"看图失败"的话, 排查的人会去查图片格式、查大小、查 base64 ——
// 全是白费
func TestModelWithoutVisionSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"Invalid content type: image_url is not supported"}}`)
	}))
	defer srv.Close()
	v := NewVision(srv.URL, "k", "文本模型")
	v.Client = srv.Client()
	_, err := v.See(context.Background(), abi.SeeParams{
		MediaType: "image/png", DataB64: "QUJD", Question: "这是什么"})
	if err == nil || !strings.Contains(err.Error(), "认不认图") {
		t.Fatalf("没指向「配错模型」这个方向: %v", err)
	}
}

// 空回复多半是它其实不认图 —— 不能当成"模型没话说"
func TestEmptyAnswerIsSuspicious(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"  "}}]}`)
	}))
	defer srv.Close()
	v := NewVision(srv.URL, "k", "m")
	v.Client = srv.Client()
	_, err := v.See(context.Background(), abi.SeeParams{
		MediaType: "image/png", DataB64: "QUJD", Question: "这是什么"})
	if err == nil || !strings.Contains(err.Error(), "NEOX_MODEL_VISION") {
		t.Fatalf("空回复该指向配置: %v", err)
	}
}

// ── 装配的优先级 ──
//
//	① 显式配了视觉供应商        用它
//	② 没配, 但主模型自己认图    用主模型  ← 换个能看图的主模型就不用配第二家
//	③ 都没有                    nil
func TestViewerAssemblyPriority(t *testing.T) {
	main := NewOpenAICompatible("https://api.main.example", "main-key", "主模型")

	// ③ 什么都没配
	for _, k := range []string{"NEOX_VISION_KEY", "NEOX_VISION_MODEL",
		"NEOX_VISION_BASE", "NEOX_MODEL_VISION"} {
		t.Setenv(k, "")
	}
	if v := ViewerFromEnv(main); v != nil {
		t.Fatal("什么都没配却装出了视觉服务")
	}

	// ② 主模型自己认图 —— 复用主供应商的 base 和 key
	t.Setenv("NEOX_MODEL_VISION", "1")
	v := ViewerFromEnv(main)
	if v == nil || v.Model() != "主模型" {
		t.Fatalf("主模型认图时该直接用它: %v", v)
	}

	// ① 显式配的优先
	t.Setenv("NEOX_VISION_KEY", "vk")
	t.Setenv("NEOX_VISION_MODEL", "专门的视觉模型")
	v = ViewerFromEnv(main)
	if v == nil || v.Model() != "专门的视觉模型" {
		t.Fatalf("显式配的该优先: %v", v)
	}
	// 没给 base 就跟主供应商同一家 —— 常见情形是同厂商的两个模型
	if vm, ok := v.(*VisionModel); !ok || vm.BaseURL != "https://api.main.example" {
		t.Fatalf("没给 base 时该沿用主供应商的: %+v", v)
	}
}

// 主模型认不认图**只认显式声明, 不按名字猜**.
//
// 这是 knownContextTokens 那次的直接教训: 照文档抄的两个数一个都没验过,
// 而且都是错的. 视觉更糟 —— 猜错的表现不是报错, 是模型一本正经地
// 描述一张它根本没收到的图
func TestVisionIsNeverGuessedFromModelName(t *testing.T) {
	for _, k := range []string{"NEOX_VISION_KEY", "NEOX_VISION_MODEL",
		"NEOX_VISION_BASE", "NEOX_MODEL_VISION"} {
		t.Setenv(k, "")
	}
	// 名字里带 vision / vl / 4o 之类的一律不算数
	for _, name := range []string{"gpt-4o", "qwen-vl-max", "some-vision-pro"} {
		main := NewOpenAICompatible("https://api.x", "k", name)
		if v := ViewerFromEnv(main); v != nil {
			t.Fatalf("%s: 按名字猜出了视觉能力 —— 猜错时模型会描述一张它没收到的图", name)
		}
	}
}

/**
 * **看图那一眼花了多少, 要带回去**.
 *
 *	看图不便宜(一张图几千 token), 这笔钱必须进入花费页、每轮上限和判据;
 *	否则"看得见图"那一轮可能只报 spent: 354, 却漏掉刚使用的一张图的成本.
 *
 *	成本是重要指标. 一笔查不到的开销比数字大一点更糟:
 *	账目无法对上, 也无法知道差异来自哪里.
 */
func Test看图要把花费带回来(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"一个红方块"}}],`+
			`"usage":{"prompt_tokens":2418,"completion_tokens":37}}`)
	}))
	defer srv.Close()

	v := NewVision(srv.URL, "k", "看图模型")
	got, err := v.See(context.Background(), abi.SeeParams{
		MediaType: "image/png", DataB64: "aGk=", Question: "这是什么？"})
	if err != nil {
		t.Fatalf("看图失败: %v", err)
	}
	if got.PromptTokens != 2418 || got.CompletionTokens != 37 {
		t.Errorf("花费没带回来: prompt=%d completion=%d", got.PromptTokens, got.CompletionTokens)
	}
	if got.Text != "一个红方块" {
		t.Errorf("正文丢了: %q", got.Text)
	}
}
