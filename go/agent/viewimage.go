package agent

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// view_image —— 看一张图.
//
// ── 它为什么带一个必填的问题 ──
//
// 拿回来的是**一次翻译**: 视觉模型看图, 写一段文字, 这段文字进上下文.
// 翻译一定会丢东西 —— 而丢掉哪些, 由问题决定.
//
//	"描述一下这张图"        → 一段面面俱到、正好没提报错那一行的话
//	"截图里的报错是什么"    → 那一行
//
// 所以问题不给缺省值. 缺省成"描述一下"的话, 模型会一直用那个 ——
// 它没有理由自己想出更好的问法, 而**默认值就是事实上的标准用法**.
//
// ── 上限为什么这么小 ──
//
// 图片走 base64 进 ABI 帧, 而帧上限是 16MB (abi.MaxFrameBytes).
// base64 会涨三分之一, 所以原图上限取 6MB —— 留足帧头和问题的余量.
// 撞上限时说清"这是我们的上限"并给出下一步, 不要让它去猜是不是网络问题.
const (
	viewMaxBytes = 6 << 20
	// 一次描述再详细也不该顶掉半个上下文
	viewMaxResultBytes = 8 << 10
)

// ViewImageTool 看图工具. see 为 nil 时**不要挂它** ——
// 同 web_search: 不承诺做不到的事.
func ViewImageTool(see func(mediaType, dataB64, question string) (abi.SeeResult, error)) Tool {
	return Tool{
		Name: "view_image",
		Desc: "看一张图片, 回答关于它的一个具体问题",
		Args: map[string]string{
			"path": "图片路径",
			"question": "你要从这张图里知道什么。**问具体的** —— " +
				"「截图里的报错原文是什么」比「描述一下这张图」有用得多",
		},
		ArgOrder: []string{"path", "question"},
		// 读文件, 所以走 read 轴 —— 跟 read_file 同一条.
		// 图片也是用户的东西, 不该因为"是图"就绕过可读范围
		Needs: abi.AxisRead, ScopeArg: "path", WorldSensitive: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			p := argStr(a, "path")
			q := strings.TrimSpace(argStr(a, "question"))
			if q == "" {
				return "", fmt.Errorf("question 是空的。看图要带一个具体问题 —— " +
					"「这张截图里的报错是什么」而不是「描述一下」: " +
					"看图的结果是一次翻译, 问什么才会翻出什么")
			}
			b, err := os.ReadFile(t.resolve(p))
			if err != nil {
				return "", actionable(err, p,
					"文件不存在。别重试同一路径——先 list_dir 看看真实的文件名")
			}
			mt, ok := imageType(b)
			if !ok {
				// **按内容判, 不按扩展名判**: 一个叫 .png 的文本文件
				// 送过去只会拿回一句莫名其妙的拒绝
				return "", fmt.Errorf("%s 不是图片(或者是我们不认识的格式)。"+
					"认得 png / jpeg / webp / gif；文本文件用 read_file", p)
			}
			if len(b) > viewMaxBytes {
				return "", fmt.Errorf("%s 有 %d KB, 超过 %d KB 的上限 —— "+
					"这是我们这一侧的限制, 不是网络问题。先缩一下"+
					"(run 里用 sips/convert 之类的工具), 或者换一张",
					p, len(b)/1024, viewMaxBytes/1024)
			}
			res, err := see(mt, base64.StdEncoding.EncodeToString(b), q)
			if err != nil {
				return "", err
			}
			/**
			 * **看图那一眼的钱要记一笔**.
			 *
			 *	看图走的是另一条 HTTP(engine/vision.go 自己发请求),
			 *	回包里的 usage 如果不转成事件: 花费页少算、每轮上限漏算、
			 *	测试台判据看不见 —— "看得见图"那一轮会报
			 *	spent: 354, 而它刚看完一张图.
			 *
			 *	发一条**跟推理一模一样**的 usage: 那三处本来就都在听
			 *	这一条, 于是一次性都对上, 不用各改一遍.
			 *
			 *	记在工具这一层, 因为这儿是唯一同时知道"花了多少"和
			 *	"怎么发事件"的地方.
			 */
			if t.Sys != nil && (res.PromptTokens > 0 || res.CompletionTokens > 0) {
				t.Sys.Emit(map[string]any{"phase": "usage", "what": "看图",
					"prompt": res.PromptTokens, "completion": res.CompletionTokens,
					"cached": 0})
			}
			text := strings.TrimSpace(res.Text)
			if text == "" {
				return "", fmt.Errorf("看了 %s 但什么都没说出来。换个问法试试", p)
			}
			if len(text) > viewMaxResultBytes {
				text = text[:viewMaxResultBytes] + "\n(描述太长, 截断了)"
			}
			// **说清这是转述, 不是原图**.
			//
			// 不说的话, 模型会把这段文字当成"我看过这张图了", 然后基于
			// 它没被问到的细节下结论 —— 而那些细节根本不在这段文字里.
			return fmt.Sprintf("(这是视觉模型看 %s 之后的回答, 不是原图；"+
				"它只回答了你问的那个问题)\n\n%s", p, text), nil
		},
	}
}

// imageType 按**文件头**认格式, 不看扩展名.
//
// 扩展名是用户或者别的程序起的名字, 跟内容没有必然关系.
// 认错的代价是白跑一次供应商往返, 还拿回一句跟真实原因无关的拒绝.
func imageType(b []byte) (string, bool) {
	switch {
	case len(b) > 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", true
	case len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg", true
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", true
	case len(b) > 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif", true
	}
	return "", false
}
