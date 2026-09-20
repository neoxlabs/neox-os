package osinit

import (
	"fmt"
	"strings"
)

/**
 * fanout —— 一句话同时发给屋里几个人时, **每个人都得知道这一点**.
 *
 * ── 为什么在 OS 这一层, 不在界面里 ──
 *
 *	分工提示若只写在一个客户端里, 场景测试台走 /say 直接投时,
 *	收到的那句话**一个字的分工提示都没有** —— 于是两个人各自搭了一套
 *	Vite 脚手架, 其中一套需要随后清掉.
 *
 *	换句话说: 谁拆活、谁先别动手, 是**这套东西的协议**, 而它当时只存在
 *	于一个客户端里. 换个客户端(命令行、手机、第三方客户端)协议就没了 ——
 *	而 bot 那边完全看不出少了什么.
 *
 *	协议属于 OS.
 *
 * ── 为什么用"名单第一个"而不是"先认领" ──
 *
 *	认领要一个来回: 我说"我做 A", 你看到之后改做 B. 而群发是**同时**
 *	到达的 —— 谁都没来得及看见别人的认领. 名单顺序是确定的, 不需要
 *	任何来回.
 */

// fanoutNote 给第 index 个收话人多带的那一段. 只有一个人收就是空.
//
//	**话要短**: 这段每一轮都跟着用户那句话送进模型, 写成说明书既费钱
//	也没人真读 —— 说清"还发给了谁、谁拆活"就够了.
func fanoutNote(names []string, index int) string {
	if len(names) < 2 || index < 0 || index >= len(names) {
		return ""
	}
	others := make([]string, 0, len(names)-1)
	for i, one := range names {
		if i != index {
			others = append(others, one)
		}
	}
	tail := fmt.Sprintf("要动手的话等%s分工，先别改文件。就答句话的不用等。", names[0])
	if index == 0 {
		tail = "要动手的话你来分，用 pass 派给他们，别几个人做同一件事。"
	}
	return fmt.Sprintf("\n\n（这话也发给了 %s）\n%s", strings.Join(others, "、"), tail)
}
