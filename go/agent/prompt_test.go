package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func prompt() string { return BuildSystemPrompt(NewToolSet(DefaultTools()), "/work", "", "") }

// 提示词是每次请求的前缀, 变一个字节所有缓存全废.
// 所以它只能由工具表和显式参数决定 —— 不许拼时间/随机 id/机器名.
func TestPromptByteStable(t *testing.T) {
	first := prompt()
	for i := 0; i < 100; i++ {
		if prompt() != first {
			t.Fatal("提示词不稳定 —— 前缀缓存会全废")
		}
	}
}

// 提示词里提到的工具必须真的存在.
//
// 写死一个不存在的工具名, 模型会照着调, 然后拿到"没有这个工具",
// 而且它没有别的线索可以自纠 —— 提示词说有它就信.
func TestPromptMentionsOnlyRealTools(t *testing.T) {
	ts := NewToolSet(DefaultTools())
	p := prompt()
	// 提示词里出现的 xxx_yyy 形式的标识符, 挑出看起来像工具名的
	re := regexp.MustCompile(`\b[a-z]+_[a-z]+\b`)
	known := map[string]bool{}
	for _, n := range ts.Names() {
		known[n] = true
	}
	// 这些是输出协议里的字段名和状态值, 不是工具
	notTool := map[string]bool{
		"string_not_found": true, "ambiguous_match": true,
		"already_done": true, "old_string": true, "new_string": true,
	}
	for _, m := range re.FindAllString(p, -1) {
		if known[m] || notTool[m] {
			continue
		}
		t.Fatalf("提示词提到了不存在的工具 %q, 模型会照着调然后撞墙", m)
	}
}

// 每个工具都要有**像样的说明** —— 有能力不等于会用.
//
// ── 查工具说明, 不要求系统段重复工具表 ──
//
//	系统提示词里再抄一份工具表会重复付费, 因为每轮已经发送工具声明.
//
//	工具细节放在 tool description, 系统段只保留跨模型通用工程行为.
//	判据是**说明够不够模型照着用**. 一行光秃秃的名字跟没有一样:
//	即使有 search, 模型仍可能整文件读, 因此"别整文件读"要写在
//	search 自己的说明里.
func TestEveryToolHasRealGuidance(t *testing.T) {
	for _, tool := range WidestForAudit().All() {
		// **只查"有没有"**: "列目录"三个字对 list_dir 就够了,
		// 而给字数定个下限只会逼人写废话去凑
		if strings.TrimSpace(tool.Desc) == "" {
			t.Errorf("工具 %s 一句说明都没有 —— 模型不会想到用它", tool.Name)
		}
		for arg, help := range tool.Args {
			if strings.TrimSpace(help) == "" {
				t.Errorf("%s 的参数 %s 没有说明", tool.Name, arg)
			}
		}
	}
}

// 提示词描述的能力必须跟工具表一致 —— 两个方向都会出错.
//
// 说多了: 要求它做做不到的事, 它只会假装做到.
// 说少了: 它会守着不存在的限制. 例如有 run 却仍声明
// "你没有 shell, 不能跑命令",
// 那句话比没有更糟: 模型明明能跑测试, 却会照着提示词说"我没法验证".
func TestPromptMatchesActualCapabilities(t *testing.T) {
	p := prompt()
	// 有 run 就不许再说没有
	for _, stale := range []string{"你没有 shell", "不能跑命令", "没法运行代码"} {
		if strings.Contains(p, stale) {
			t.Fatalf("工具表里有 run, 提示词却还写着 %q —— 它会照着这句话放弃验证", stale)
		}
	}
	// 没有的东西不许承诺
	for _, banned := range []string{"打开浏览器", "访问网页", "下载依赖"} {
		if strings.Contains(p, banned) {
			t.Fatalf("提示词要求了我们做不到的事 (没有网络): %q", banned)
		}
	}
	// 出网的真实语义必须明说, 而且要给出**下一步**.
	//
	// 只说"没有网络"是不够的, 而且现在已经不对了: 网是能授权的.
	// 少了"怎么申请"这一句, 它会在 npm install 上换镜像源反复重试 ——
	// 那是它唯一想得出来的自救办法, 而问题根本不在源上.
	if !strings.Contains(p, "出网要授权") {
		t.Fatal("没说清出网是按主机名授权的, 它会把 403 当成网络故障")
	}
	if !strings.Contains(p, "request_access") {
		t.Fatal("没告诉它怎么申请授权 —— 撞了墙只能干瞪眼或者瞎重试")
	}
	// 反过来: 不许把"回读过"说成"能正常工作"
	// 反过来: 不许把"回读过"说成"能正常工作"
	if !strings.Contains(p, "没验就说没验") {
		t.Fatal("没有明确禁止它吹「应该没问题」")
	}
}

// 层号不许重复 —— 重复说明插层时漏改, 后面全错位
func TestLayerNumbersUnique(t *testing.T) {
	// 这条查的是源码注释, 不是提示词本身
	seen := map[string]bool{}
	re := regexp.MustCompile(`层 (\d+):`)
	src := promptSource(t)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if seen[m[1]] {
			t.Fatalf("层号 %s 重复了 —— 插层时漏改, 后面全错位", m[1])
		}
		seen[m[1]] = true
	}
	if len(seen) < 10 {
		t.Fatalf("只找到 %d 层, 分层结构可能被破坏了", len(seen))
	}
}

// search 和 read_file 必须在**工具说明里**接起来 ——
// 这是搜索定位 + 分片读细节闭环的唯一说明处.
//
// 从提示词沉到工具说明了(见 agent/search.go): 它是这个工具的用法,
// 不是跨场景的判断方式
func TestSearchToolConnectsOffset(t *testing.T) {
	tool, ok := NewToolSet(DefaultTools()).Get("search")
	if !ok {
		t.Fatal("没有 search")
	}
	if !strings.Contains(tool.Desc, "offset") {
		t.Error("没告诉它 search 的行号能接 read_file, 两个工具接不起来")
	}
	if !strings.Contains(tool.Desc, "不要把整个文件读进来") {
		t.Error("没说清优先用 search —— 它会退回整文件读")
	}
}

// **回复跟随用户输入的语言.**
//
// 整份提示词是中文写的, 不说清楚的话模型默认跟着提示词走 ——
// 用户用英文问, 它照样中文答. 这是"通用性"上最便宜也最容易漏的一条:
// 只用中文测试无法覆盖这个偏差, 因此需要明确约束回复语言.
func TestPromptFollowsUserLanguage(t *testing.T) {
	p := prompt()
	if !strings.Contains(p, "用他说话的语言回他") {
		t.Fatal("没要求跟随用户的语言 —— 英文提问会拿到中文回答")
	}
}

// ── 提示词体积闸 ──
//
// **闸要装在大头上.** 这条是照着客户端的反面写的: 它有一个
// promptSize 闸钉着系统段 < 14000, 但**工具声明一个字都没钉** ——
// 而那边 155 个工具的 schema 是系统段的 6 倍(141555 : 23219).
// 闸装在了小头上, 等于没装.
//
// 所以这里数的是**整个前缀**: 系统段 + 工具声明. 它俩每一轮都发,
// 涨一个字节, 这段对话里每一次推理都多付一次.
//
// 体积样本: 15289 = 系统段 12529 + 8 个工具的声明 2760.
// (改写前系统段是 17420 —— 删掉的是那十二条"某工具报某错怎么办",
// 工具自己在出错那一刻会说, 提示词里那份只在稀释注意力.)
//
// ── 这个闸自己也装在小头上过 ──
//
// 上面那段话批评客户端"闸装在了小头上", 而它自己量的是 `DefaultTools()`
// 那几个 —— **真实进程挂的从来不是这一张表**: chat 侧还挂着 remind_me /
// name_place / watch_for / cancel, 配了搜索有 web_search, 认图还有 view_image.
//
//	缺省表   9 个工具
//	全表    16 个工具   19445 字节   ← 每一轮真付的 (含 recall)
//
// 差的这两千多字节从来没有被任何地方看住. 所以现在量**最大的那张表** ——
// 判据是"这台机器上跑得起来的最贵的前缀", 不是"我们默认导出的那张表".
//
// 新上限 20000. 涨的这一截买到的是上网、看图、查过去:
// fetch / web_search / view_image / recall.
//
// ── 再涨一次: 21000, 买的是"它能自己拉人"(recruit) ──
//
// 抬闸之前先按这条规矩查了一遍"有没有能删的" —— 逐个量了 18 个工具的
// 声明字节:
//
//	683 watch_for      六种信号的**枚举**: 模型猜不出 place.arrived 这种标识符
//	573 request_access "一次写全"那个例子: 它挡的是"装一个包问两次"的重复打扰
//	418 remind_me      365 recruit       …
//
// 最肥的两个都是**契约和枚举, 不是散文** —— 删了工具就不能用了.
// 声明该说的只是"是什么"; 判断和场景细节留在工具的错误文本里(那份在出错
// 那一刻才付, 声明是每一轮都付). recruit 就是照这条写的: 365 字节,
// 而它的四条前置判断(重名、没说岗位、没说理由、名字太长)一个字都不在声明里.
//
// ── 第三次: 24000, 而这次涨的不是内容, 是**量对了** ──
//
// 判据自己错了一处, 而且是同一个毛病的第三次:
//
//	系统段    用 **缺省表**(9 个工具)建的
//	工具声明  用 **全表**(18 个)算的
//
// 两边不是同一张表. "## 工具"那一节的内容跟着传进去的工具表走, 所以
// 系统段少算了两千多字节 —— 而真实进程两边用的都是全表.
//
// 量对之后是 23489. 涨的这一截**一个字的新内容都没有**, 全是本来就在付、
// 只是没被看住的钱.
//
// **那笔"将来可以省的"已经省了.**
//
// 上面记过一笔: 工具在前缀里出现两次 —— 一次是系统段"## 工具"那一节的
// 散文描述, 一次是原生协议的 tool_defs. 当时写着"得先验一件事: 去掉
// 散文那份之后, 模型会不会又退回整文件读. 没验之前不动它".
//
// 不重复发送散文工具表时, 三个行为检查仍满足要求:
//
//	找 app/ 里所有返回 409 的地方   → 一次 search 答完, 行号三处全对
//	改一个 255 行文件里的一个常量   → search 定位 → edit_file → 编译验证
//	问以前另一段对话里的事          → recall, 并如实说那个文件已经不在了
//
// 没有整文件读, 也没有忘掉自己有什么工具. 唯一真需要那张清单的是 show ——
// 它是唯一没有策略正文的工具, 补了一句(见 prompt.go 的 showLine), 几十
// 字节换掉四千, 而且那一句比清单更有用: 清单只说"能插卡片", 没说什么
// 时候该插.
//
// 24029 → 20008. 闸跟着降到 20500, **不留旧水位**: 省出来的空间不收回去,
// 下一次就会被悄悄填满, 而那时候没人记得它本来是省出来的.
//
// ── 第四次: 20700, 买的是"它能自己搬工作区"(move_work) ──
//
// move_work 把宿主的 rebind 能力交给 bot, 让"切到这个工作区去干活"
// 成为可执行请求; 否则即使宿主支持, bot 也只能要求用户去平台层操作.
//
// watch_for 的枚举和 request_access 的例子是**契约不是散文**,
// 不能为了容纳新能力删掉必要的调用说明.
// move_work 自己的声明 160 字节, 全表第二瘦(只比 list_dir 胖):
// 路径格式、目录不存在、家目录/账本一律拒 —— 这些全在错误文本里,
// 声明只说"是什么".
//
// ── 第五次: 20900, 买的是"别一口气说一屏" ──
//
// 手机聊天框一屏放不下三段长回复, 因此需要限制输出长度.
//
// **这一笔跟前四笔不是一回事: 它省的比花的多.**
// 前四笔买的是能力(工具/事实), 每轮都在付而收益是偶发的.
// 这一条买的是**输出变短** —— 一次超长回复是 500~2000 个输出 token,
// 而它一天发生几十次. 花 188 字节的前缀(还是缓存命中的那种)去换,
// 是这份提示词里回报最直接的一笔.
//
// 涨之前照规矩查了"有没有能删的": 九层挨个量了一遍(736~3041 字节),
// 没有散文 —— 最长的 layerCompletion 里那段"验证不许动用户真实数据"
// 防止测试记录污染交付物, layerEnvironmentCommon 则说明完整的出网授权链.
// 而新加的那条**没有另起一节**, 是并进 layerPacing 已有的列表里的:
// 单起一个"## 长度"要多花一倍.
// **量的是精简那一套** —— 见 agent/prompt_lean.go: 一套提示词, 就是它.
// 30 个工具的声明占 11503 字节, 比系统段本身还大 —— 下一刀在那儿.
//
// ── 14000 → 14500 ──
//
//	涨的这 500 字节是**从提示词搬下来的**, 不是新加的: "run 第一行是
//	退出码""要试什么往 .tmp/ 里放, 别动他的真实数据""找内容用 search
//	别整文件读" —— 这三条原来在系统段里, 现在在各自工具的说明里.
//	净账是省的: 系统段少了 10059, 工具说明多了 500.
//
// ── 为什么停在 15000 而不是更低 ──
//
//	剩下的 3427 字节系统段里, 有一千多是**岗位那一段**(工作区在哪、
//	PLAN.md/CHARTER.md 怎么维护、git 分支). 那些是 per-bot 的尾巴,
//	只在分配项目的 bot 上出现, 且各自有对应失败场景的
//	测试钉着(TestCharterGetsWrittenNotJustRead 之类).
//
//	为了凑一个人为设定的整数去砍掉一个有故事的规则, 是把判据倒过来用.
//	真实的账: 25201 → 14900.
//
// ── 15000 → 15100 ──
//
//	这两句分别防止无依据补全和地点范围误判:
//	  "不替工具圆场"  工具答"这儿还没起过名", 而它回了一句"在家" ——
//	                  他家早就从地点表里删掉了
//	  place 的 radius 圈小了他人在里面而系统说不认识, 而且一次不报错
//
// ── 15100 → 15300 ──
//
//	多的这 200 字节买的是**它不用再去 grep 自己的账本**。
//
//	缺少专用查询入口时, 一天的开销样本是 122 次 run 中有 78 次(64%)
//	用于翻账本和问时间:
//	它想知道"我设了哪些提醒""我记着哪些地点""现在几点"这些, 而**一个
//	专门的工具都没有**, 于是它用 grep 补, 每次一个完整往返, 输出还是
//	一坨原始 JSON 灌进上下文(一轮 prompt 从 37590 涨到 40908)。
//
//	200 字节的前缀(而且是缓存命中的那种)换掉几万 token 的往返, 是这
//	份提示词里回报最直接的一笔。
//
// ── 15300 → 15600 ──
//
//	多的是 find_place: 按名字查一个地方在哪。**它原来答不了这种问题** ——
//	web_search 是 nil、geocoding 要地址不要名字、fetch 得先知道 URL,
//	三条路都不通, 于是它拿手机当时的位置顶上, 再用那个坐标算路程。
//
//	这道闸拦的是**散文**, 不是能力。一个能把"凤凰国际广场在哪"从
//	"编一个"变成"查一个"的工具, 值这 300 字节。
//
// ── 15600 → 15700 ──
//
//	where 多了四个用法: 路况(traffic)、预报(weather)、起点(from)、
//	怎么去(mode)。缺少说明时, "路线是什么、要多久、堵不堵"三个问题
//	可能有两个靠猜, 甚至把按实时路况算出的结果说成"实时路况看不到";
//	"今天天气"也可能只得到"此刻多云"。where/place/find_place 的用法
//	只写在参数上、desc 只点名可省 400 字节, 剩下 50 字节用于能力说明。
//
// 生活助理不加载项目用的 PLAN.md/CHARTER.md 规则, 系统段可由 4KB 出头
// 缩至 2974 字节. where day= / history 提供两类账本查询能力,
// 占用的是工具声明预算, 不是重复的行为规则。
// 闸还留着: 它挡的是"提示词里又抄了一遍工具自己会说的话"。
const promptPrefixBudget = 17000

// widestToolSet 这台机器上可能出现的**最贵**的一张工具表.
//
// 闸要量它, 不是量缺省表 —— 见上面那段. 这里给的回调都是空壳:
// 我们量的是声明的字节数, 跟它们实际干什么无关.

// **最贵那张表必须真的是最贵的**.
//
// 这个仓库为同一件事栽过三次: 闸量的是缺省表而系统段按全表建、
// 系统段按全表建而闸数缺省表的工具数… 每次都是"闸装在小头上",
// 每次都是加了个新工具之后才发现 —— 而那时候预算早就悄悄超了.
//
// 所以不再靠人记得往 widestToolSet 里补一格: 让它跟"这台机器
// 挂得出来的所有工具"对表, 少一个就红.
func TestWidestToolSetReallyIsWidest(t *testing.T) {
	widest := widestToolSet().Names()
	have := map[string]bool{}
	for _, n := range widest {
		have[n] = true
	}
	// DefaultToolsWith 里每个"回调不为 nil 就挂"的工具都要在里面
	for _, name := range []string{"web_search", "view_image", "recall",
		"recruit", "handoff", "remind_me", "cancel", "move_work",
		"where", "place", "remember", "forget_that", "set_timezone",
		"notify_when", "agenda", "find_place"} {
		if !have[name] {
			t.Errorf("%s 没进最贵的那张表 —— 预算闸就是装在小头上, "+
				"它的声明字节从来没被算过", name)
		}
	}
}

func TestPromptPrefixStaysLean(t *testing.T) {
	ts := widestToolSet()
	// **量最贵的那一种说法**: 两种边界说法长度不同, 而真实进程跑的是
	// 哪一种由宿主决定. 量小的那个就是又一次"闸装在小头上" ——
	// 这个仓库为同一件事栽过一次(缺省表 vs 全表).
	sys := widestPrompt(ts)
	n := requestBytes(sys, ToolDefs(ts), nil)
	t.Logf("前缀 %d 字节 (系统段 %d + %d 个工具的声明 %d)",
		n, len(sys), len(ts.Names()), n-len(sys))
	if n > promptPrefixBudget {
		t.Fatalf("前缀涨到 %d 字节, 超过 %d —— 每一轮推理都在多付这笔钱。"+
			"加东西之前先想想有没有能删的: 工具自己在出错那一刻会说的话, "+
			"不该在提示词里再抄一遍", n, promptPrefixBudget)
	}
}

// 配了搜索就要说清"先搜再取" —— 这句沉到 web_search 自己的说明里了.
//
//	原来它是提示词里一条按工具表开关的正文. 沉下来之后跟着工具走:
//	没配搜索的机器上, 这句话根本不出现 —— 而原来那种写法要靠
//	一个 if 去保证同一件事.
func TestSearchToolSaysFetchTheSource(t *testing.T) {
	tool, ok := WidestForAudit().Get("web_search")
	if !ok {
		t.Fatal("配了搜索却没挂出 web_search")
	}
	if !strings.Contains(tool.Desc, "fetch") {
		t.Error("没说清搜到之后要 fetch 正文 —— 摘要不能当依据")
	}
}

// 有 recall 就要说清它是干什么的 —— 同上, 沉到工具说明里
func TestRecallToolSaysItIsCrossSession(t *testing.T) {
	tool, ok := WidestForAudit().Get("recall")
	if !ok {
		t.Fatal("配了召回却没挂出 recall")
	}
	if !strings.Contains(tool.Desc, "对话") {
		t.Error("没说清它查的是别的对话 —— 它会继续说自己没有跨会话记忆")
	}
}

// 闸量的必须是**最大的那张表**.
//
// 这一条是给闸自己装的闸: 上一版量的是 DefaultTools(), 而真实进程挂的
// 是全表, 差两千多字节从来没人看住. 有人再把它改回缺省表, 这里当场红.
func TestBudgetGuardsTheWidestTable(t *testing.T) {
	wide := len(widestToolSet().Names())
	base := len(NewToolSet(DefaultTools()).Names())
	if wide <= base {
		t.Fatalf("闸量的表(%d 个)不比缺省表(%d 个)大 —— "+
			"那它又装回小头上了", wide, base)
	}
}

// widestPrompt 两种边界说法里更贵的那一份.
func widestPrompt(ts *ToolSet) string {
	loose := BuildSystemPromptIn(ts, "", "/work", "", "", false)
	strict := BuildSystemPromptIn(ts, "", "/work", "", "", true)
	if len(strict) > len(loose) {
		return strict
	}
	return loose
}

func promptSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("prompt.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 记忆请求有专门的 remember 工具, 不需要写工作区文件.
//
//	"记住 X"若只能靠写文件处理, 很可能写到授权范围外并多一次审批打扰.
//
//	remember 工具(osinit/notes.go)提供真正的长期存储入口;
//	给出可执行的路径比仅禁止写文件可靠.
func TestRememberingHasARealPlaceToGo(t *testing.T) {
	ts := NewToolSet(WithNotes(DefaultTools(),
		func(k, v string) (string, error) { return "", nil },
		func(string) (string, error) { return "", nil }))
	if _, ok := ts.Get("remember"); !ok {
		t.Fatal("没有 remember —— 它只能去写文件, 而那多半写到授权范围外")
	}
}

// 临时目录和"不许动真实数据" —— 沉到 run 自己的说明里了.
//
//	拿记账程序的**默认数据路径**验证持久化, 会把测试用的"买牛奶"
//	留在交付物里, 还可能被当成"持久化产物"保留.
//	因此说明必须给出临时目录, 不能只笼统要求验证正确性.
func TestRunToolGivesAScratchPlace(t *testing.T) {
	tool, ok := NewToolSet(DefaultTools()).Get("run")
	if !ok {
		t.Fatal("没有 run")
	}
	if !strings.Contains(tool.Desc, ".tmp/") {
		t.Error("没告诉它有临时目录 —— 它只能拿用户的真数据当试验田")
	}
	if !strings.Contains(tool.Desc, "绝不能") {
		t.Error("没禁止往用户真实数据里塞测试记录")
	}
}

// 提示词是 raw string, **里面一个反引号都不能有** —— 它会当场截断字符串.
//
// 给 `net:主机名`、`.tmp/`、`pypi.org` 加反引号这三种写法都会
// 提前结束 raw string, 让后续正文被当成 Go 代码并报语法错. 因此钉住:
// 提示词里要强调就用 ** **, 不要用反引号.
func TestPromptHasNoBackticks(t *testing.T) {
	src := promptSource(t)
	// 只查 layer 常量和 layerEnvironment 里的正文, 跳过 Go 注释
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		// 一行里出现奇数个反引号 = 它是 raw string 的开闭; 偶数 = 正文里用了
		if n := strings.Count(line, "`"); n >= 2 {
			t.Fatalf("prompt.go 第 %d 行正文里用了反引号, 会截断 raw string:\n%s", i+1, line)
		}
	}
}

// **需要人工决策的事, 先把事实查清再问.**
//
// 整理乱目录时, 即使只移动不删除、把垃圾放到"待删除/"等确认,
// 并指出"报价单有三个版本、合同草稿有两个", 也不足以支撑删除决策.
//
// **必须比对内容**: 两个合同草稿可能 md5 完全相同, 三个报价单却是
// 内容不同的真版本, 仅凭文件名无法区分.
//
// 而用户是**照着这个判断删东西的**. 名字像不代表内容一样,
// 名字不像也不代表内容不一样; 猜错一次, 他就丢了一份不该丢的文件.
//
// 提示词既要要求不可逆操作前确认, 也要要求确认所依据的事实经过核查.
func TestPromptRequiresEvidenceBeforeAskingUserToDecide(t *testing.T) {
	p := prompt()
	if !strings.Contains(p, "不猜") {
		t.Fatal("没要求它查实 —— 它会拿文件名当重复的证据")
	}
	// **也不许替工具圆场**: 工具答"这儿还没起过名"时, 不能擅自回答
	// "在家"; 家可能已从地点表里删除. 用户会依据答案出门,
	// 顺耳却没有事实支撑的补全会导致错误行动.
	if !strings.Contains(p, "不替工具圆场") {
		t.Fatal("没禁止它替工具圆场 —— 工具说不知道, 它会自己脑补一个")
	}
	if !strings.Contains(p, "先把事实查清再问") {
		t.Fatal("没要求拍板前先拿证据 —— 用户照着没依据的判断做不可逆的事")
	}
}

// 后台进程活不过这段对话 —— 这条事实必须写在提示词里.
//
// 生命周期边界的例子: nohup 启动每 2 秒写一行的循环, 累积 33 行后
// 切换对话(/新), 10 秒后仍是 33 行, **不会继续增长**.
//
// 原因是内核语义: agent 是自己 pid 命名空间的 PID 1, 它一退出, 内核把
// 命名空间里剩下的进程一起收掉. nohup/&/setsid 都拦不住.
// 这保证后台进程不能逃出会话隔离边界.
//
// 错的是**没人告诉 agent 这条规则**, 于是它对用户许了做不到的诺:
// "起好了, 一直跑着", "以后 PID 忘了用 pgrep 找它再 kill" ——
// 下次那个号码根本不在了. 而新起的那段对话只能猜:
// "它在 15:37:11 之后自己停了(可能正常退出或被别的方式终止了)".
func TestPromptStatesBackgroundJobsDieWithTheConversation(t *testing.T) {
	p := (&LLM{Tools: NewToolSet(DefaultTools()), Writable: "工作目录"}).systemPrompt()
	// ── 判据只查**后果**, 不查内核术语 ──
	//
	//	"pid 命名空间" 和 "nohup" 是**给读代码的人解释为什么**,
	//	模型需要知道的则是后果: 不能承诺后台任务"会一直跑着".
	//	写在提示词里的每一个术语都是每一轮都要付的钱.
	if !strings.Contains(p, "活不过这段对话") {
		t.Fatal("没说清后台进程的寿命 —— 它会对用户许做不到的诺")
	}
	if !strings.Contains(p, "一直跑着") {
		t.Fatal("没禁止它说'它会一直跑着' —— 而那正是用户会照着办的那句")
	}
}

// "验证不许动真实数据"这条规矩要覆盖**它刚写的程序**.
//
// 待办服务即使通过落盘、终止、重启后数据仍在的持久化验证,
// 用**程序的默认数据路径**测试仍会污染交付物: 验证脚本没有临时路径,
// web/todos.json 就会留下"买牛奶""写周报"等测试记录,
// 还可能被当成"持久化产物"而保留.
//
// 仅列"账本/数据库/配置"等**用户已有内容**不足以覆盖新程序的输出路径.
// 即使程序支持 DATA_FILE, 验证时未使用它也不会隔离测试数据.
func TestPromptCoversVerifyingAgainstProgramsOwnOutput(t *testing.T) {
	// 这条沉到 run 自己的说明里了 —— 它是"跑一条命令去验"这个动作的
	// 用法, 不是跨场景的判断方式
	tool, ok := NewToolSet(DefaultTools()).Get("run")
	if !ok {
		t.Fatal("没有 run")
	}
	for _, want := range []string{"你刚写的程序也算", "默认写到哪儿", "留在交付物里"} {
		if !strings.Contains(tool.Desc, want) {
			t.Errorf("规矩没覆盖'拿程序默认输出路径去验'这种情况(缺 %q)", want)
		}
	}
}

/**
 * "做出来对不上"不是停下来问的理由.
 *
 *	"流水导出成 csv、按月汇总、命令行入口、补测试、写 README"五件活
 *	即使遇到空工作区, 也有从零搭建记账小工具这个明显选项.
 *	仅因"数据格式对不上就白干了"而停下确认, 会把可逆的实现选择
 *	变成必须等待的人工决策.
 *
 *	那是**成本**, 不是不可逆. 文件改一改就是了, 而他等那句话可能要等
 *	一天. 这种过度确认可以让一轮在 25 秒结束, 却一个文件都不落地.
 */
func Test需求预设的东西不在也不该停下来问(t *testing.T) {
	if !strings.Contains(layerTriage, "对不上") {
		t.Error("没说清'做出来对不上'不算收不回来 —— 模型会拿它当停下来的理由")
	}
	// 真正该问的那条不许被冲掉
	if !strings.Contains(layerTriage, "收不回来") {
		t.Error("把'不可逆先问一句'那条弄丢了")
	}
	// 这一层是每一轮都要付的, 别写成一篇文章
	if n := len([]rune(layerTriage)); n > 700 {
		t.Errorf("这一层 %d 字, 太长了 —— 每一轮都要付", n)
	}
}
