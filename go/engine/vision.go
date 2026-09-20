package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 看图 —— 跟推理、搜索同一条: **凭据只有引擎持有**, 出网只有这一个口子.
//
// ── 两种配法, 优先级是固定的 ──
//
//	① 显式配了视觉供应商 (NEOX_VISION_*)      用它
//	② 没配, 但主模型自己认图                  用主模型
//	③ 都没有                                  nil —— 这台机器不认图
//
// ②那一条正是用户要的形状: 换成一个本来就能看图的主模型之后,
// **不需要再配第二家** —— 而不是让"看图"永远绑死在一个额外的供应商上.
//
// ── 主模型认不认图, 只认显式声明 ──
//
// 不做"按模型名猜"的表. 这一条是 knownContextTokens 那次的直接教训:
// 那张表原来照文档抄了两个数, **一个都没验过, 而且都是错的**.
// 视觉支持更糟 —— 猜错的表现不是报错, 是模型一本正经地描述一张
// 它根本没收到的图.
//
// 所以: NEOX_MODEL_VISION=1 才认. 谁配谁负责, 而配的人是能当场验证的.
type Viewer interface {
	See(ctx context.Context, p abi.SeeParams) (abi.SeeResult, error)
	// Model 谁看的 —— 进事件日志, 看错了要能分清是哪个模型
	Model() string
}

const (
	visionTimeout = 90 * time.Second
	// visionMaxTokens 描述的长度上限. 一段描述再详细也不该顶掉半个上下文;
	// 而且问题是具体的, 答案本来就该短
	visionMaxTokens = 1500
)

// VisionModel 一个认图的 OpenAI 兼容端点.
//
// 复用 chat/completions: 各家的视觉接口都是在 content 里塞一个
// image_url 块, 而不是另开一条路径.
type VisionModel struct {
	BaseURL string
	APIKey  string
	ModelID string
	Client  *http.Client
}

func (v *VisionModel) Model() string { return v.ModelID }

// NewVision 显式构造. base/model 为空时不猜 —— 见文件头.
func NewVision(baseURL, apiKey, model string) *VisionModel {
	return &VisionModel{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		ModelID: model,
		Client:  &http.Client{Timeout: visionTimeout},
	}
}

// ViewerFromEnv 按环境变量和主供应商装配, 优先级见文件头.
//
// 返回 nil = 这台机器不认图. **nil 是一个有意义的答案**:
// 调用方据此不给 agent 挂 view_image 工具.
func ViewerFromEnv(main Provider) Viewer {
	key := strings.TrimSpace(os.Getenv("NEOX_VISION_KEY"))
	model := strings.TrimSpace(os.Getenv("NEOX_VISION_MODEL"))
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("NEOX_VISION_BASE")), "/")
	if key != "" && model != "" {
		if base == "" {
			// 没给 base 就跟主供应商同一家 —— 常见情形是同一个厂商
			// 的两个模型. 主供应商也说不出 base 的话就不猜.
			if oc, ok := main.(*OpenAICompatible); ok {
				base = oc.BaseURL
			}
		}
		if base != "" {
			return NewVision(base, key, model)
		}
	}
	// 主模型自己认图 —— 那就不需要第二家
	if oc, ok := main.(*OpenAICompatible); ok && mainModelSeesImages() {
		return NewVision(oc.BaseURL, oc.APIKey, oc.ModelID)
	}
	return nil
}

// mainModelSeesImages 主模型认不认图 —— **只认显式声明, 不按名字猜**.
// 猜错的表现不是报错, 是模型一本正经地描述一张它根本没收到的图.
func mainModelSeesImages() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NEOX_MODEL_VISION"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// 多模态消息: content 是块数组而不是字符串.
//
// 单独一套类型而不是把 oaMessage.Content 改成 any: 那个结构体是
// **每一次推理都要序列化**的东西, 把它的字段改成 interface{} 会让
// 所有普通请求也多走一遍反射, 而且 JSON 里 "content": "…" 和
// "content": [...] 两种形状混在一个类型里, 出错时极难看出是哪一种.
type visionMessage struct {
	Role    string        `json:"role"`
	Content []visionBlock `json:"content"`
}

type visionBlock struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageRef `json:"image_url,omitempty"`
}

type imageRef struct {
	URL string `json:"url"`
}

func (v *VisionModel) See(ctx context.Context, p abi.SeeParams) (abi.SeeResult, error) {
	if strings.TrimSpace(p.DataB64) == "" {
		return abi.SeeResult{}, fmt.Errorf("没有图片数据")
	}
	if strings.TrimSpace(p.Question) == "" {
		// 不给问题就退化成泛泛描述 —— 见 abi.MSee 的说明.
		// 在这里拦住而不是替它编一个问题: 编出来的问题会**看起来像**
		// 用户问的, 而它其实是我们瞎猜的
		return abi.SeeResult{}, fmt.Errorf("没有问题 —— 看图必须带一个具体问题")
	}
	mt := strings.TrimSpace(p.MediaType)
	if mt == "" {
		mt = "image/png"
	}

	body, err := json.Marshal(map[string]any{
		"model":      v.ModelID,
		"max_tokens": visionMaxTokens,
		"stream":     false,
		"messages": []visionMessage{{
			Role: "user",
			Content: []visionBlock{
				{Type: "text", Text: p.Question},
				{Type: "image_url", ImageURL: &imageRef{
					URL: "data:" + mt + ";base64," + p.DataB64}},
			},
		}},
	})
	if err != nil {
		return abi.SeeResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return abi.SeeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.APIKey)

	resp, err := v.Client.Do(req)
	if err != nil {
		return abi.SeeResult{}, fmt.Errorf("看图请求失败 (%s): %w", v.ModelID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var out oaResponse
	if json.Unmarshal(raw, &out) != nil {
		return abi.SeeResult{}, fmt.Errorf("看图回包解不开 (HTTP %d): %s",
			resp.StatusCode, trunc(string(raw), 200))
	}
	if out.Error != nil {
		return abi.SeeResult{}, visionRefusal(v.ModelID, out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK || len(out.Choices) == 0 {
		return abi.SeeResult{}, fmt.Errorf("看图失败 (HTTP %d): %s",
			resp.StatusCode, trunc(string(raw), 200))
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return abi.SeeResult{}, fmt.Errorf("%s 什么都没回 —— 多半是它其实不认图。"+
			"检查 NEOX_VISION_MODEL / NEOX_MODEL_VISION 配对了没有", v.ModelID)
	}
	// 这一眼花了多少要带回去 —— 看图不便宜, 而这笔钱原来谁都没记.
	// 见 abi.SeeResult 上那段.
	return abi.SeeResult{Text: text, Model: v.ModelID,
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens}, nil
}

// visionRefusal 供应商拒绝时的翻译.
//
// **"这个模型不认图"必须单独说**: 配错模型是这条路上最常见的错,
// 而它的回包跟别的 400 长得一样. 混成一句"看图失败"的话,
// 排查的人会去查图片格式、查大小、查 base64 —— 全是白费.
func visionRefusal(model, msg string) error {
	low := strings.ToLower(msg)
	for _, hint := range []string{"image", "vision", "multimodal", "content"} {
		if strings.Contains(low, hint) {
			return fmt.Errorf("%s 拒绝了这张图: %s。"+
				"**先确认这个模型认不认图** —— 配错模型的表现就是这样, "+
				"跟图片格式没关系", model, trunc(msg, 200))
		}
	}
	return fmt.Errorf("%s 拒绝: %s", model, trunc(msg, 200))
}
