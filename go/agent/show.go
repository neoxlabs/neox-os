package agent

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 卡片种类 → 这类卡片**没有它就等于没有**的那个字段.
//
// ── 为什么校验必须在这一侧 ──
//
// 渲染端对不认识的 spec 是**静默丢弃**的: 缺 type 或缺 id 就当这条事件
// 不存在, 界面上什么都不出现. 而工具这边如果只管发, 返回给模型的就是
// 一句"已展示" —— 模型接着往下说"如上图", 用户面前是一片空白.
//
// 所以要么这里挡住并说清缺了什么, 要么就得让渲染端渲染一张错误卡.
// 挡在产出侧更好: 模型当场能改, 而错误卡是给用户看的噪音.
//
// 值是**几选一**: 图和视频既可以给 src(data: URL), 也可以给 path
// (你工作区里的文件). **path 才是常用的那个** —— 你画完一张图存在
// 磁盘上, 你知道的就是那条路径; 要你把几百 KB 的 base64 写进一次输出,
// 又慢又占上下文.
var cardNeeds = map[string][]string{
	"image":    {"src", "path"},
	"video":    {"src", "path"},
	"audio":    {"src", "path"},
	"gallery":  {"images"},
	"link":     {"url"},
	"file":     {"path"},
	"code":     {"code"},
	"diff":     {"diff"},
	"table":    {"columns"},
	"doc":      {"body"},
	"weather":  {"place"},
	"progress": {"title"},
	"choice":   {"options"},
}

// showTool 往对话里插一张卡片.
//
// 这是 bot 的**多模态出口**: 除了说话, 它还能给出图、一组图、声音、
// 视频、网页、文件、代码、表格、天气这些用一段文字讲不清的东西.
//
// ── 这张表是开的, 不是封的 ──
//
// 界面对**不认识的种类不再丢弃**: 认不出来就照着字段老老实实画一张
// 通用卡(标题/正文/链接/图/列表都认). 所以这里加一个新种类不需要等
// 客户端跟着发版 —— 先能用, 再谈好不好看.
//
// 但**必填字段仍然要挡在这一侧**: 一张缺了 url 的网页卡, 界面上就是
// 一个空框, 而模型那边收到的是"已展示", 它会接着说"链接见上".
//
// 不占任何能力轴 —— 它不碰文件、不出网、不改世界, 只是往事件日志里
// 多写一条给界面看的记录. 真正受管的是拿到那张图的过程(fetch/read),
// 不是把它摆出来这一下.
func showTool() Tool {
	return Tool{
		Name: "show",
		// 种类**不在这儿抄一遍**: 填错了工具当场会说清有哪些可选,
		// 而这行字每一轮推理都要付钱. 腾出来的地方给下面那两句 ——
		// 它们是模型不会自己知道的事
		Desc: "在对话里插一张卡片(图/表格/代码/进度/网页…，列表外的也能填)。" +
			// ── 两项使用约束 ──
			//
			//	一、它自己发明了 `[[card: progress {…}]]` 这种写法直接
			//	写进回话里. 卡片的**唯一出口是这个工具** —— 正文里写
			//	标记不会变成卡片, 用户看到的是一段 JSON.
			//
			//	二、那张卡的内容是"提醒已设", 而 remind_me 的结果里
			//	已经写着同一句话. 一张只是把刚说过的话再摆一遍的卡,
			//	是**多一样要看的东西**, 不是多一条信息.
			//
			//	写在工具说明里而不是提示词层: 离用到它的那一刻最近.
			//	但它照样算进那 20900 字节的前缀预算(工具声明也在里面) ——
			//	所以上面那份种类清单让了位置出来
			"**只能从这儿发**：正文里写 [[card: …]] 不渲染，会原样显示成 JSON。" +
			"一句话说得完的事别发卡片",
		Args: map[string]string{
			// 种类和字段都**不在提示词里列**: 填错了工具当场会说清缺什么、
			// 有哪些种类可选. 在这儿抄一遍等于每轮推理都为同一份清单付钱
			// "列表外的也能填"上面那行已经说过了 —— 同一句话付两遍钱
			"kind": "卡片种类",
			"spec": "这张卡的字段, JSON 对象",
		},
		ArgOrder: []string{"kind", "spec"},
		Run: func(t Toolbox, a map[string]any) (string, error) {
			kind := strings.TrimSpace(argStr(a, "kind"))
			if kind == "" {
				return "", fmt.Errorf("没说是哪种卡片。常见的几种: %s；列表外的也能填, 界面会照字段画一张通用卡", knownCards())
			}
			spec, err := cardSpec(a["spec"])
			if err != nil {
				return "", err
			}
			/**
			 * ── 表里没有的种类**照发** ──
			 *
			 *	原来这里挡着: 界面对不认识的种类是静默丢弃的, 发出去等于
			 *	用户面前一片空白而模型以为已展示. 现在界面改成了"认不出
			 *	就照字段画一张通用卡"(见 view/opencard.tsx), 那条理由没了 ——
			 *	而挡着的代价是**这套东西的种类被这张表封死**: 加一种要改
			 *	两处代码再发一次版.
			 *
			 *	所以: 表里的按各自的必填字段校验(缺了就是空卡),
			 *	表外的只要求"别是空的", 并且**如实告诉它用户会看到什么样**.
			 */
			needs, known := cardNeeds[kind]
			if !known {
				if len(spec) == 0 {
					return "", fmt.Errorf("%s 卡片是空的 —— 至少给一个字段, 不然界面上就是个空框", kind)
				}
			} else if !hasAny(spec, needs...) {
				return "", fmt.Errorf("%s 卡片缺 %s —— 没有它这张卡在界面上是空的"+
					"（本地文件填 path，比如 path=\"图/架构.png\"）", kind, strings.Join(needs, " 或 "))
			}
			/**
			 * **进度卡还得真有个进度**.
			 *
			 *	title 有了就放行的话, 一张不带任何数值的进度卡照样发得出去,
			 *	而它在界面上画出来是**空条 + 0%** —— 不报错, 只是画了个
			 *	假数. 那比报错坏: 模型接着说"进度如上", 而"上面"写着 0%.
			 *
			 *	模型最自然会给 done/total: 卡片若只认 ratio/percent,
			 *	它说"5/8"而卡上就写着 0%.
			 *	渲染端现在也认 done/total 了, 这里挡的是**一个都没给**.
			 */
			if kind == "progress" && !hasAny(spec, "ratio", "percent", "done") {
				return "", fmt.Errorf("progress 卡片没有进度值 —— 界面上会画成 0%%。" +
					"给 done/total(比如 5 和 8), 或者 percent(0-100), 或者 ratio(0-1)")
			}
			if t.Sys == nil {
				return "", fmt.Errorf("这台机器上没有界面, 展示不了。把内容直接说出来")
			}
			// 复制一份再补字段.
			//
			//	直接往参数上写会**改掉调用方的 map**. 这不是洁癖:
			//	同一个 args 复用时, 第二次算 id 会把第一次写进去的 id
			//	也算进哈希 —— 于是 id 看起来永远不撞, 而防撞那一步
			//	其实早就失效了. 测试也跟着变成假绿.
			card := make(map[string]any, len(spec)+2)
			for k, v := range spec {
				card[k] = v
			}
			card["type"] = kind
			card["id"] = cardID(kind, spec)
			payload, err := json.Marshal(card)
			if err != nil {
				return "", fmt.Errorf("这张卡序列化不了: %w", err)
			}
			t.Sys.Emit(map[string]any{"channel": "ui", "text": string(payload)})
			// **表外的种类要如实说它长什么样**: 不说的话模型会以为界面
			// 给了它一张专门设计过的卡, 于是接着说"如上图所示的航班信息" ——
			// 而用户看到的是一张按字段排出来的通用卡
			if !known {
				return okResult("shown", fmt.Sprintf(
					"%s 卡片已经出现在对话里了（这个种类界面没有专门的样子，"+
						"是照你给的字段排出来的通用卡：标题、正文、链接、图、列表都认）。"+
						"别再用文字把它复述一遍。", kind)), nil
			}
			return okResult("shown", fmt.Sprintf(
				"%s 卡片已经出现在对话里了。别再用文字把它复述一遍。", kind)), nil
		},
	}
}

// hasAny 这几个键里有没有出现过一个
func hasAny(spec map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := spec[k]; ok {
			return true
		}
	}
	return false
}

// cardSpec 把 spec 参数收成一个对象.
//
// 两种形态都收: 模型有时给对象, 有时给一整段 JSON 字符串 ——
// 这跟"它写错了"不是一回事, 两种都是合法的表达, 不该为此浪费一轮.
func cardSpec(raw any) (map[string]any, error) {
	switch v := raw.(type) {
	case map[string]any:
		return v, nil
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			return nil, fmt.Errorf("spec 不是合法 JSON 对象: %v", err)
		}
		return out, nil
	case nil:
		return nil, fmt.Errorf("缺 spec —— 这张卡要展示什么内容")
	default:
		return nil, fmt.Errorf("spec 要是一个 JSON 对象, 收到的是 %T", raw)
	}
}

// cardID 给这张卡一个身份.
//
// 内容哈希 + 纳秒: 光靠内容, 同一张卡发两次会**撞成同一个 id**,
// 而 id 是界面认回答的那把钥匙 —— 撞了就会出现"答了第一张,
// 第二张跟着变成已答"这种鬼事.
func cardID(kind string, spec map[string]any) string {
	h := fnv.New64a()
	fmt.Fprint(h, kind)
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "|%s=%v", k, spec[k])
	}
	return kind + "-" + strconv.FormatUint(h.Sum64(), 36) +
		"-" + strconv.FormatInt(time.Now().UnixNano()%1e6, 36)
}

func knownCards() string {
	names := make([]string, 0, len(cardNeeds))
	for k := range cardNeeds {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, "/")
}
