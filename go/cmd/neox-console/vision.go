package main

import (
	"context"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

/**
 * probeVision —— **真送一张图过去**, 看它认不认.
 *
 *	"这个模型认不认图"填错的表现不是报错, 是模型一本正经地描述一张
 *	它根本没收到的图. 所以这个勾必须能当场验: 送一张纯红的小图,
 *	问它什么颜色.
 *
 *	答案里有"红"就算通; 有回答但不是红 —— 那更值得说出来, 说明这条路
 *	通了但它看到的东西不对(常见于网关把图丢了、只把文字转过去).
 */
func probeVision(cfg osinit.ProviderConfig) string {
	viewer := buildViewer(osinit.ProviderConfig{
		BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Vision: true,
	})
	if viewer == nil {
		return "没有 key，试不了看图"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	res, err := viewer.See(ctx, abi.SeeParams{
		MediaType: "image/png", DataB64: redDotPNG,
		Question: "这张图是什么颜色？只回答颜色两个字。",
	})
	if err != nil {
		return "看图这条路不通：" + firstLine(err.Error(), 120)
	}
	said := strings.TrimSpace(res.Text)
	if said == "" {
		return "送过去了，但它什么都没说 —— 这个勾先别勾"
	}
	if strings.Contains(said, "红") || strings.Contains(strings.ToLower(said), "red") {
		return "能看图：图是红的，它也说" + firstLine(said, 40)
	}
	// **通了但看错了更要说**: 常见于网关把图丢了、只把文字转过去
	return "它回了「" + firstLine(said, 40) + "」，而那张图是纯红的 —— 图可能没真的送到"
}

// 一张 16×16 的**纯红** png (79 字节). 小到可以直接写在代码里,
// 而"这是什么颜色"这个问题没有第二种正确答案 —— 答不上来就是没收到图.
const redDotPNG = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR42mO4o6FBEmIY1TCqYfhqAAAyBCwQhCQ/2gAAAABJRU5ErkJggg=="
