package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// edit_file · 内容寻址.
//
// 为什么必须有它: 现在改一行要 write_file 整个文件 ——
//
//	· token 浪费: 改一个标题要重发几千字节
//	· 容易改坏: 模型重新输出整个文件时会顺手"优化"没让它动的地方
//	· 截断风险: 文件一大, 输出就被 max_tokens 砍掉
//
// 为什么是**内容寻址**而不是行号:
//
//	行号会漂. 模型读到的是第 42 行, 等它决定要改的时候文件可能已经变了,
//	而且行号本身也容易数错. 内容寻址使用读取到的原文进行精确匹配,
//	对不上就明确报错, 不会改错地方.
//
// 两个错误各有各的正确处理, 所以要分开报:
//
//	string_not_found  文件跟它以为的不一样 → 重读, 使用当前原文
//	ambiguous_match   那段文字不唯一 → 多带几行上下文让它唯一
//
// (这里原来写着"或者 replace_all" —— **那个参数不存在**. 注释里许一个
// 不存在的口子, 下一个人会照着它去写调用点, 或者以为功能丢了.
// 提示词的第 4 条规矩同样适用于注释: 不承诺做不到的事.)

var (
	ErrStringNotFound = errors.New("string_not_found")
	ErrAmbiguousMatch = errors.New("ambiguous_match")
)

// editTool 内容寻址编辑
func editTool() Tool {
	return Tool{
		Name: "edit_file",
		Desc: "改已有文件的一段(内容寻址)",
		Args: map[string]string{
			"path":       "文件路径",
			"old_string": "要替换的原文, 照抄你读到的, 要能唯一定位",
			"new_string": "替换成什么",
		},
		ArgOrder: []string{"path", "old_string", "new_string"},
		Needs:    abi.AxisWrite, ScopeArg: "path", Mutates: true,
		Run: func(t Toolbox, a map[string]any) (string, error) {
			path := argStr(a, "path")
			oldS := argStr(a, "old_string")
			newS := argStr(a, "new_string")

			if oldS == "" {
				return "", fmt.Errorf("%s: old_string 不能为空。"+
					"要新建文件用 write_file, 不要用 edit_file", path)
			}

			full := t.resolve(path)
			raw, err := os.ReadFile(full)
			if err != nil {
				if os.IsNotExist(err) {
					// **这条要说死**: 文件不存在时重试 edit 永远不会成功,
					// 必须切 write_file. 不说清楚模型会一直重试.
					return "", fmt.Errorf("%s: 文件不存在。**不要重试 edit_file**，"+
						"新建文件用 write_file", path)
				}
				return "", actionable(err, path, "读不了")
			}
			content := string(raw)

			n := strings.Count(content, oldS)
			switch n {
			case 0:
				return "", fmt.Errorf("%w: %s 里找不到那段原文。"+
					"文件跟你以为的不一样——重新 read_file 一次，照抄当前的原文再试，"+
					"别猜着改参数", ErrStringNotFound, path)
			case 1:
				// 正常路径
			default:
				return "", fmt.Errorf("%w: 那段原文在 %s 里出现了 %d 次，不唯一。"+
					"多带几行上下文让它唯一", ErrAmbiguousMatch, path, n)
			}

			// **已经是目标状态就别写** —— 重复写入会让"改了没"这件事变模糊,
			// 也会白白刷新 mtime.
			if oldS == newS {
				return okResult("already_done",
					fmt.Sprintf("%s 已经是目标内容，没有改动", path)), nil
			}

			updated := strings.Replace(content, oldS, newS, 1)
			if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
				return "", actionable(err, path, "写不回去")
			}
			// edit 天然是"读了再改"(old_string 对不上就失败), 所以它不需要
			// 那道闸; 但**改完这一版要记下来** —— 否则接着 write_file
			// 会被自己刚才的改动拦住
			t.Seen.note(full, []byte(updated))
			return okResult("success", fmt.Sprintf(
				"%s 已改。原来 %d 字节，现在 %d 字节。用 read_file 回读确认。",
				path, len(content), len(updated))), nil
		},
	}
}
