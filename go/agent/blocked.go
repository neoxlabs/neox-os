package agent

import (
	"errors"
	"fmt"
)

/**
 * 「被拦下」跟「没做成」是两回事.
 *
 *	工具报错有两种来源, 界面上原来长得一模一样:
 *
 *	  没做成 —— 磁盘满了、命令挂了、路径不存在. 这是故障, 要修.
 *	  被拦下 —— 闸按设计挡住了一次危险动作(比如盖掉别人刚写的文件).
 *	            这是系统**正常工作**的样子.
 *
 *	都显示成"1 个没成", 用户就会去查一个根本不存在的故障;
 *	更糟的是, 看多了会开始怀疑这些闸是不是有毛病 —— 而它们恰恰
 *	是这个工作区里最不该被怀疑的东西.
 *
 *	模型那边**看到的文案不变**: 它需要的是"为什么拦你、接下来怎么办",
 *	跟这个分类无关. 这个标记只走事件, 只给界面看.
 */
type blockedError struct{ error }

// blockedf 造一个"被闸拦下"的错误 —— 文案照常给模型看, 只是额外带上分类.
func blockedf(format string, a ...any) error {
	return blockedError{fmt.Errorf(format, a...)}
}

func isBlocked(err error) bool {
	var b blockedError
	return errors.As(err, &b)
}
