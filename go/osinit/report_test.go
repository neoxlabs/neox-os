package osinit

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func ev(kind abi.EventKind, p map[string]any) abi.Event {
	return abi.Event{PID: signalPID, Kind: kind, Payload: p}
}

func sigEv(src, kind string) abi.Event {
	return ev(abi.EvSignal, map[string]any{"source": src, "kind": kind})
}

// **从没导致过任何通知的种类是纯噪音候选.**
//
// 判据不是"它少", 是"它从没让任何人做过任何事" —— 一天两条但每次
// 都值得说的信号是好信号; 一天两千条而一次通知都没引发的, 是在花钱买噪音.
// 而这件事**只在统计上看得见**, 翻单条日志永远发现不了.
func TestReportFlagsSilentNoisyKinds(t *testing.T) {
	var evs []abi.Event
	for i := 0; i < 200; i++ {
		evs = append(evs, sigEv("ha.sensor", "sensor.changed"))
	}
	for i := 0; i < 3; i++ {
		evs = append(evs, sigEv("phone.mk", "call.incoming"))
		evs = append(evs, ev(abi.EvSignalDigest, map[string]any{"reason": "urgent"}))
		evs = append(evs, ev(abi.EvInterrupt, map[string]any{"verdict": "deliver"}))
	}
	txt := AnalyzeLedger(evs).Text()

	// 断的是**意图**(给出一条可直接执行的动作), 不是断字面 ——
	// 采集端是装在别人手机里的 APK, 无法直接修改, 所以建议要指向
	// /静音这类实际可操作的地方
	if !strings.Contains(txt, "/静音 ha.sensor/sensor.changed") {
		t.Fatalf("200 条从没导致通知的信号没被标出来, 或者建议没指向一个动得了的地方:\n%s", txt)
	}
	// 少而有效的那个不该被标
	if line := lineWith(txt, "call.incoming"); strings.Contains(line, "/静音") {
		t.Fatalf("有效的信号被标成噪音了: %s", line)
	}
}

// **迟到率是配置错误最直接的信号.**
//
// 容忍度 4 秒配上手机 10 秒的上传周期, 会让每条信号到达时都比水位线老,
// 全部走历史通道 —— 而它看起来完全正常.
func TestReportCallsOutHighLateRate(t *testing.T) {
	var evs []abi.Event
	for i := 0; i < 10; i++ {
		evs = append(evs, sigEv("phone.mk", "place.arrived"))
	}
	for i := 0; i < 40; i++ {
		evs = append(evs, ev(abi.EvSignalLate,
			map[string]any{"source": "phone.mk", "kind": "place.arrived"}))
	}
	txt := AnalyzeLedger(evs).Text()
	if !strings.Contains(txt, "太高") {
		t.Fatalf("80%% 迟到率没被喊出来:\n%s", txt)
	}
	for _, want := range []string{"上传周期", "时钟"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("报了迟到率却没说该查什么(缺 %q)", want)
		}
	}
}

// **安静率是整个感知层的验收判据**, 报告必须给这个数
func TestReportComputesQuietRate(t *testing.T) {
	var evs []abi.Event
	// 20 个窗口, 只开口 1 次 → 安静率 95%
	for i := 0; i < 20; i++ {
		evs = append(evs, sigEv("phone.mk", "location"))
		evs = append(evs, ev(abi.EvSignalDigest, map[string]any{"reason": "window"}))
	}
	evs = append(evs, ev(abi.EvInterrupt, map[string]any{"verdict": "deliver"}))
	txt := AnalyzeLedger(evs).Text()
	if !strings.Contains(txt, "安静率 95%") {
		t.Fatalf("安静率算错了:\n%s", txt)
	}
	if strings.Contains(txt, "太吵") {
		t.Fatal("95% 安静却说它吵")
	}
}

// 吵的时候要喊出来, 而且要说清判据是什么
func TestReportCallsOutNoisy(t *testing.T) {
	var evs []abi.Event
	// 样本要够 —— 加了 minWindowsToJudge 之后, 10 个窗口会被
	// 正确地判成"样本太少". 这条测试要验的是"吵了要喊", 不是样本门槛
	for i := 0; i < 25; i++ {
		evs = append(evs, sigEv("x", "y"))
		evs = append(evs, ev(abi.EvSignalDigest, map[string]any{"reason": "window"}))
		evs = append(evs, ev(abi.EvInterrupt, map[string]any{"verdict": "deliver"}))
	}
	txt := AnalyzeLedger(evs).Text()
	if !strings.Contains(txt, "太吵") {
		t.Fatalf("每个窗口都开口却没被喊:\n%s", txt)
	}
	if !strings.Contains(txt, "不是'它做了多少事'") {
		t.Fatal("喊了吵却没说判据是什么")
	}
}

// **每一条数字后面都要跟一句"该怎么办".**
//
// "迟到 312 条"对读的人没有用 —— 他不知道 312 算多还是算少.
// 不给建议的报告等于没写.
func TestReportAlwaysGivesADirection(t *testing.T) {
	var evs []abi.Event
	for i := 0; i < 5; i++ {
		evs = append(evs, sigEv("phone.mk", "location"))
	}
	txt := AnalyzeLedger(evs).Text()
	if !strings.Contains(txt, "迟到率") || !strings.Contains(txt, "正常") {
		t.Fatalf("没有给出判断:\n%s", txt)
	}
}

// 空账本要说清是哪一种空, 别让人以为系统在跑
func TestEmptyLedgerSaysWhy(t *testing.T) {
	txt := AnalyzeLedger(nil).Text()
	if !strings.Contains(txt, "采集端没接上") {
		t.Fatalf("空账本没说清可能的原因:\n%s", txt)
	}
}

// **数不出来的东西要说"数不出来", 不能报 0.**
//
// 报 0 会让人以为"没有重传", 而真相是这个数根本没记 ——
// 静默的空白比一个诚实的"数不出来"危险得多.
func TestReportAdmitsWhatItCannotCount(t *testing.T) {
	txt := AnalyzeLedger([]abi.Event{sigEv("a", "b")}).Text()
	if !strings.Contains(txt, "去重次数账本里没有记") {
		t.Fatalf("没有承认数不出去重:\n%s", txt)
	}
}

// 攒下的占一半以上说明标准太松, 要给出下一步
func TestReportCallsOutTooManyDeferred(t *testing.T) {
	var evs []abi.Event
	evs = append(evs, ev(abi.EvInterrupt, map[string]any{"verdict": "deliver"}))
	for i := 0; i < 8; i++ {
		evs = append(evs, ev(abi.EvInterrupt, map[string]any{"verdict": "defer"}))
	}
	evs = append(evs, sigEv("x", "y"))
	txt := AnalyzeLedger(evs).Text()
	if !strings.Contains(txt, "一半以上被攒下了") {
		t.Fatalf("攒下的占八成没被喊:\n%s", txt)
	}
}

func lineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// **样本不够就不下判断.**
//
// 拿真实账本跑出来的: 一份只有 4 个窗口的演示数据, 报告当场断言
// "**它太吵了**" —— 而 4 个窗口里开口 1 次什么也说明不了.
//
// 给了没依据的建议跟不给建议一样等于没写, 而且更糟: 一个会因为
// 四个数据点就喊狼来了的报告, 第三次之后就没人看了.
func TestReportWithholdsJudgementOnSmallSamples(t *testing.T) {
	var evs []abi.Event
	for i := 0; i < 4; i++ {
		evs = append(evs, sigEv("phone.mk", "place.arrived"))
		evs = append(evs, ev(abi.EvSignalDigest, map[string]any{"reason": "window"}))
	}
	evs = append(evs, ev(abi.EvInterrupt, map[string]any{"verdict": "deliver"}))
	txt := AnalyzeLedger(evs).Text()

	if strings.Contains(txt, "太吵") {
		t.Fatalf("4 个窗口就断言它吵:\n%s", txt)
	}
	if !strings.Contains(txt, "样本太少") {
		t.Fatalf("样本不够却没说清:\n%s", txt)
	}
	// 数字本身还是要给 —— 不下判断不等于不给数据
	if !strings.Contains(txt, "安静率") {
		t.Fatal("样本少就连数字都不给了")
	}
}

// **一次都没开口不等于对.**
//
// 一个坏掉的、永远沉默的系统安静率满分 —— 而那正是最难发现的一种坏:
// 数字很好看, 用户只是慢慢地不再指望它.
//
// 报告量得到"它吵不吵", 量不到"它该说的说了没有". 后一半要拿一天
// 有事发生的活动去验(sense.HouseholdDay). 这句话必须印在报告上,
// 否则那个坏掉的系统会拿着满分蒙混过去.
func TestSilentSystemDoesNotLookLikeSuccess(t *testing.T) {
	var evs []abi.Event
	for i := 0; i < minWindowsToJudge+5; i++ {
		evs = append(evs, sigEv("ha.door", "door.opened"),
			ev(abi.EvSignalDigest, map[string]any{"reason": "window"}))
	}
	txt := AnalyzeLedger(evs).Text()
	if !strings.Contains(txt, "不等于对") {
		t.Fatalf("一次都没开口, 报告却只说'正常':\n%s", txt)
	}
}

// **报告只报数量, 从不显示它到底说了什么.**
//
// "通知去向: deliver=1 breakthrough=1 defer=2" —— 而调阈值时人要判的
// 恰恰是**"这句话该不该说"**: 一次打扰是不是值得, 只有看见那句话
// 才判得出来.
//
// 而那些话就躺在账本里(interrupt.verdict 的 text), 报告却不给看,
// 于是每次都要我手工 grep 账本 —— 而"手工 grep"正是这份报告
// 存在的理由.
//
// 更要紧的是**破例**那几条: 它们绕过了额度, 用户有权知道系统凭什么
// 破例(S? 那条"破例要标出来"说的是终端, 而报告这儿一样成立).
func TestReportShowsWhatItActuallySaid(t *testing.T) {
	evs := []abi.Event{
		sigEv("ha.lock.front", "lock.opened"),
		ev(abi.EvInterrupt, map[string]any{"verdict": "breakthrough",
			"text": "家里没人，但前门刚刚被解锁了", "why": "actionable: 无人时前门被解锁"}),
		ev(abi.EvInterrupt, map[string]any{"verdict": "deliver",
			"text": "快递到了"}),
		ev(abi.EvInterrupt, map[string]any{"verdict": "defer",
			"text": "客厅灯开了一小时"}),
	}
	txt := AnalyzeLedger(evs).Text()

	if !strings.Contains(txt, "家里没人，但前门刚刚被解锁了") {
		t.Fatalf("报告没显示它到底说了什么 —— 而调阈值要判的正是这个:\n%s", txt)
	}
	// **破例的要标出来**: 它绕过了额度, 凭什么破例得说清楚
	if !strings.Contains(txt, "无人时前门被解锁") {
		t.Fatalf("破例的没说清凭什么破例:\n%s", txt)
	}
	// 攒下的也要看得见 —— 那是"它想说但没说"的那一批,
	// 判"额度配小了还是标准太松"全靠它
	if !strings.Contains(txt, "客厅灯开了一小时") {
		t.Fatalf("攒下的那批看不见:\n%s", txt)
	}
}

// 一次都没开口的时候不该多出一段空标题 —— 每份报告多一段没内容的话
// 是纯噪音(这条在这个仓库里已经立过一次)
func TestReportSaysNothingWhenItSaidNothing(t *testing.T) {
	txt := AnalyzeLedger([]abi.Event{sigEv("ha.lock.front", "lock.opened")}).Text()
	if strings.Contains(txt, "它说了这些") {
		t.Fatalf("一次都没开口却起了个标题:\n%s", txt)
	}
}
