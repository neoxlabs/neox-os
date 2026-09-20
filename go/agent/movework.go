package agent

import (
	"fmt"
	"strings"
)

/**
 * move_work —— 把自己的工作区搬到另一个目录.
 *
 *	输入"你切到这个工作区去干活"时, bot 不能自行切换, 只能返回
 *	"我没法自己切, 得去平台层弄" —— 工作区迁移本来就是宿主已有的能力.
 *	搬家本来就是宿主已有的能力(rebind), 缺的只是让 bot 够得着它.
 *
 *	边界没有变宽的口子: 路径校验和"这一轮说完才搬"都在宿主的回调里
 *	(家目录/根/账本目录一律拒 —— 见 console 的 resolveWork);
 *	这里只负责把话递过去.
 */

// MoveWorkTool 搬工作区. move 由宿主给 —— 它才知道怎么换进程不断对话.
func MoveWorkTool(move func(path string) (string, error)) Tool {
	return Tool{
		Name: "move_work",
		Desc: "搬到另一个工作区。只在用户明确让你去那儿干活时用",
		Args: map[string]string{"path": "目标目录"},
		// 它最终换的是这个进程的写边界 —— 一律当写操作, 不进并发批次
		Mutates:        true,
		WorldSensitive: true,
		Run: func(_ Toolbox, args map[string]any) (string, error) {
			if move == nil {
				return "", fmt.Errorf("这台机器搬不了工作区")
			}
			path, _ := args["path"].(string)
			return move(strings.TrimSpace(path))
		},
	}
}

// WithMoveWork 挂上搬家能力. nil = 这台机器不支持, 工具表里就没有它.
func WithMoveWork(tools []Tool, move func(string) (string, error)) []Tool {
	if move != nil {
		return append(tools, MoveWorkTool(move))
	}
	return tools
}
