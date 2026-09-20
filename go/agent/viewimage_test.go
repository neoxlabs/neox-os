package agent

import (
	"encoding/base64"
	"github.com/neox-os/neox-os/abi"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 各格式的最小文件头 —— 判类型只看这几个字节
var pngHead = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func writeFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// **按文件头判, 不按扩展名判.**
//
// 扩展名是别人起的名字, 跟内容没有必然关系. 认错的代价是白跑一次
// 供应商往返, 还拿回一句跟真实原因无关的拒绝
func TestImageTypeByHeaderNotExtension(t *testing.T) {
	for _, c := range []struct {
		head []byte
		want string
	}{
		{pngHead, "image/png"},
		{[]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, "image/jpeg"},
		{[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		{[]byte("GIF89a\x00\x00"), "image/gif"},
	} {
		got, ok := imageType(c.head)
		if !ok || got != c.want {
			t.Fatalf("认错了: %q → %q (要 %s)", c.head[:4], got, c.want)
		}
	}
	// 叫 .png 的文本文件不是图片
	if _, ok := imageType([]byte("这其实是一段文字")); ok {
		t.Fatal("文本被当成图片了 —— 送过去只会拿回一句莫名其妙的拒绝")
	}
}

// 问题是必填的. 缺省成"描述一下"的话模型会一直用那个 ——
// 而**默认值就是事实上的标准用法**, 于是每次拿回来的都是一段
// 面面俱到、正好没提你要的那一行的话
func TestQuestionIsRequired(t *testing.T) {
	tool := ViewImageTool(func(_, _, _ string) (abi.SeeResult, error) { return abi.SeeResult{Text: "看到了"}, nil })
	p := writeFile(t, "a.png", pngHead)
	_, err := tool.Run(Toolbox{}, map[string]any{"path": p, "question": "  "})
	if err == nil {
		t.Fatal("没给问题该被拦住")
	}
	if !strings.Contains(err.Error(), "具体问题") {
		t.Fatalf("没说清为什么要问题: %v", err)
	}
	// Validate 那一层也该拦 —— question 不在 Optional 里
	if err := tool.Validate(map[string]any{"path": p}); err == nil {
		t.Fatal("缺 question 该在参数校验就被拦")
	}
}

// 结果必须说清**这是转述, 不是原图**.
//
// 不说的话, 模型会把这段文字当成"我看过这张图了", 然后基于它没被问到的
// 细节下结论 —— 而那些细节根本不在这段文字里
func TestResultSaysItIsARetelling(t *testing.T) {
	tool := ViewImageTool(func(mt, data, q string) (abi.SeeResult, error) {
		if mt != "image/png" {
			t.Fatalf("类型传错: %s", mt)
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			t.Fatalf("图片不是合法 base64: %v", err)
		}
		if q != "报错原文是什么" {
			t.Fatalf("问题没原样传下去: %q", q)
		}
		return abi.SeeResult{Text: "报错是 permission denied"}, nil
	})
	p := writeFile(t, "shot.png", pngHead)
	got, err := tool.Run(Toolbox{}, map[string]any{
		"path": p, "question": "报错原文是什么"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "不是原图") {
		t.Fatalf("没说清这是转述: %q", got)
	}
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("结果丢了: %q", got)
	}
}

// 不是图片的文件要当场说清, 而且要指出该用哪个工具
func TestNonImageRejectedWithNextStep(t *testing.T) {
	tool := ViewImageTool(func(_, _, _ string) (abi.SeeResult, error) { return abi.SeeResult{}, nil })
	p := writeFile(t, "notes.txt", []byte("就是一段文字"))
	_, err := tool.Run(Toolbox{}, map[string]any{"path": p, "question": "写了什么"})
	if err == nil || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("该指向 read_file: %v", err)
	}
}

// read_file 撞到图片时说什么, **取决于这台机器认不认图**.
//
// 不认图的机器上说"用 view_image", 就是指向一个不存在的工具 ——
// 模型会照着调然后撞墙, 而它没有任何线索可以自纠
func TestReadFileImageHintFollowsTheMachine(t *testing.T) {
	p := writeFile(t, "a.png", pngHead)

	_, err := readFileTool(true).Run(Toolbox{}, map[string]any{"path": p})
	if err == nil || !strings.Contains(err.Error(), "view_image") {
		t.Fatalf("认图的机器该指向 view_image: %v", err)
	}

	_, err = readFileTool(false).Run(Toolbox{}, map[string]any{"path": p})
	if err == nil {
		t.Fatal("图片读不了要报错")
	}
	if strings.Contains(err.Error(), "view_image") {
		t.Fatalf("不认图的机器却指向了 view_image —— 它会照着调然后撞墙: %v", err)
	}
	if !strings.Contains(err.Error(), "不认图") {
		t.Fatalf("没说清这台机器看不了图: %v", err)
	}
}

// 没配视觉 = 工具表里根本没有 view_image, 而且 read_file 那句话也跟着变.
// **两处必须一起变** —— 这正是它们要写在一个函数里的理由
func TestVisionOffMeansNoToolAndNoHint(t *testing.T) {
	off := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil, nil, nil, nil, nil))
	if _, ok := off.Get("view_image"); ok {
		t.Fatal("没配视觉却挂上了 view_image")
	}
	on := NewToolSet(DefaultToolsWith(nil, nil, nil, nil, nil,
		func(_, _, _ string) (abi.SeeResult, error) { return abi.SeeResult{}, nil },
		nil, nil, nil))
	if _, ok := on.Get("view_image"); !ok {
		t.Fatal("配了视觉却没挂工具")
	}
	// 配了视觉之后, 表里的 read_file 必须是**知道有 view_image 的那一版**
	rf, _ := on.Get("read_file")
	p := writeFile(t, "a.png", pngHead)
	_, err := rf.Run(Toolbox{}, map[string]any{"path": p})
	if err == nil || !strings.Contains(err.Error(), "view_image") {
		t.Fatalf("read_file 没跟着换 —— 它还在说这台机器不认图: %v", err)
	}
}

// 超上限要说清"这是我们这一侧的限制", 别让它去猜是不是网络问题
func TestOversizeSaysItIsOurLimit(t *testing.T) {
	tool := ViewImageTool(func(_, _, _ string) (abi.SeeResult, error) {
		t.Fatal("超限的图不该发出去")
		return abi.SeeResult{}, nil
	})
	big := append(append([]byte{}, pngHead...), make([]byte, viewMaxBytes)...)
	p := writeFile(t, "big.png", big)
	_, err := tool.Run(Toolbox{}, map[string]any{"path": p, "question": "这是什么"})
	if err == nil || !strings.Contains(err.Error(), "不是网络问题") {
		t.Fatalf("没说清是我们的上限: %v", err)
	}
}

/**
 * **看一张图的钱要记一笔**.
 *
 *	看图走的是另一条 HTTP, 回包里的 usage 如果不转成事件: 花费页少算、
 *	每轮上限漏算、测试台判据看不见 —— "看得见图"那一轮会报
 *	spent: 354, 而它刚看完一张图.
 *
 *	记在工具这一层, 因为这儿是唯一同时知道"花了多少"和"怎么发事件"的
 *	地方. 发的是**跟推理一模一样**的 usage: 那三处本来就都在听这一条.
 */
func Test看图要记一笔账(t *testing.T) {
	f := newFakeSys()
	tool := ViewImageTool(func(_, _, _ string) (abi.SeeResult, error) {
		return abi.SeeResult{Text: "47", PromptTokens: 2418, CompletionTokens: 37}, nil
	})
	p := writeFile(t, "a.png", pngHead)
	if _, err := tool.Run(Toolbox{Sys: f}, map[string]any{"path": p, "question": "几?"}); err != nil {
		t.Fatalf("看图失败: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var found map[string]any
	for _, e := range f.events {
		if e["phase"] == "usage" {
			found = e
		}
	}
	if found == nil {
		t.Fatal("看完图一笔账都没记 —— 花费页、每轮上限、判据三处都会少算")
	}
	if found["prompt"] != int64(2418) || found["completion"] != int64(37) {
		t.Errorf("记的数不对: %v", found)
	}
	if found["what"] != "看图" {
		t.Errorf("没说清这笔是看图花的: %v", found)
	}
}

// 供应商没给用量的时候不许编一笔 —— 记一个假的比不记更糟
func Test看图没给用量就不记(t *testing.T) {
	f := newFakeSys()
	tool := ViewImageTool(func(_, _, _ string) (abi.SeeResult, error) {
		return abi.SeeResult{Text: "47"}, nil
	})
	p := writeFile(t, "a.png", pngHead)
	if _, err := tool.Run(Toolbox{Sys: f}, map[string]any{"path": p, "question": "几?"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e["phase"] == "usage" {
			t.Errorf("供应商没给用量, 却记了一笔: %v", e)
		}
	}
}
