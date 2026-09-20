package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/agent"
	"github.com/neox-os/neox-os/osinit"
)

// newTestOS 一台空 OS, 时间由测试说了算.
func newTestOS(t *testing.T, now func() time.Time) *components {
	t.Helper()
	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	t.Cleanup(func() { o.Shutdown("测试结束") })
	c := &components{os: o}
	c.timers = osinit.NewTimers(o.Log(), now, c.fireInto)
	return c
}

func toolNamed(t *testing.T, c *components, p persona, name string) (run func(map[string]any) (string, error), ok bool) {
	t.Helper()
	ts := c.toolsFor(p, "p1")
	tool, found := ts.Get(name)
	if !found {
		return nil, false
	}
	return func(a map[string]any) (string, error) {
		return tool.Run(agent.Toolbox{Root: t.TempDir()}, a)
	}, true
}

// 提醒必须落进**内核的**闹钟登记处, 不是 bot 自己记一笔.
//
// 差别在进程死了以后: 内核那份落在事件日志里, 开机装得回来;
// bot 自己记的那份跟进程同生共死, 而用户不会知道它没了.
func TestRemindLandsInTheKernelAlarm(t *testing.T) {
	c := newTestOS(t, nil)
	run, ok := toolNamed(t, c, persona{app: "notes"}, "remind_me")
	if !ok {
		t.Fatal("接了闹钟, 工具表里却没有 remind_me")
	}
	at := time.Now().Add(2 * time.Hour).UnixMilli()
	if _, err := run(map[string]any{"at": float64(at), "text": "12 点的火车"}); err != nil {
		t.Fatal(err)
	}
	pend := c.timers.Pending()
	if len(pend) != 1 {
		t.Fatalf("闹钟没进登记处: %+v", pend)
	}
	// 挂在 **bot 标签**上而不是 pid: 进程会死, 对话不死 ——
	// 挂 pid 的话重启一次, 昨天设的提醒就找不到主人了
	if pend[0].Thread != "notes" {
		t.Errorf("闹钟挂在 %q 上, 应该挂 bot 标签 notes —— 重启后就认不回来了", pend[0].Thread)
	}
	if pend[0].Text != "12 点的火车" {
		t.Errorf("提醒原话被改了: %q", pend[0].Text)
	}
}

// 设一个过去的时刻要**当场**被拒, 而且拒绝要回到模型手里.
//
// console 这边 bot 是 in-proc 的, 调得到 Timers, 所以能在本地验的就在本地验:
// 那句拒绝直接变成工具结果, 模型下一步就能改对 —— 而不是像单向 Emit
// 那条路一样, 工具回 success、用户以为有人替他记着了.
func TestRemindRefusesThePastOutLoud(t *testing.T) {
	c := newTestOS(t, nil)
	run, _ := toolNamed(t, c, persona{app: "notes"}, "remind_me")
	past := time.Now().Add(-time.Hour).UnixMilli()
	out, err := run(map[string]any{"at": float64(past), "text": "早就过了"})
	if err == nil {
		t.Fatalf("过去的时间点被收下了, 工具还回了 %q —— 它会对用户说提醒设好了", out)
	}
	if len(c.timers.Pending()) != 0 {
		t.Error("被拒了却还是进了登记处")
	}
}

// 到点了, 话要落进**设它的那个 bot** 的事件流里, 而且原话不许改.
func TestAlarmFiresIntoThatBotsStream(t *testing.T) {
	nowMs := time.Now().UnixMilli()
	clock := nowMs
	c := newTestOS(t, func() time.Time { return time.UnixMilli(clock) })

	// 一个顶着 bot=notes 标签的活进程
	pid, err := c.os.Spawn(abi.ProcessSpec{
		App: "notes", Name: "记事本", Labels: map[string]string{"bot": "notes"},
	}, osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
		<-ctx.Done()
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.timers.Set("notes", clock+1000, "12 点的火车"); err != nil {
		t.Fatal(err)
	}
	clock += 2000
	c.timers.Tick()

	if !streamHas(c, pid, "12 点的火车") {
		t.Fatal("闹钟响了, 但那句话没出现在这个 bot 的事件流里 —— 用户在界面上看不到它")
	}
}

// 找不到主人时**不许吞掉**: bot 可能被删了, 但提醒是用户要的.
func TestOrphanAlarmStillSpeaks(t *testing.T) {
	nowMs := time.Now().UnixMilli()
	clock := nowMs
	c := newTestOS(t, func() time.Time { return time.UnixMilli(clock) })
	if _, err := c.timers.Set("已经没了的bot", clock+1000, "该吃药了"); err != nil {
		t.Fatal(err)
	}
	clock += 2000
	c.timers.Tick()
	if !streamHas(c, abi.ProcessID("os"), "该吃药了") {
		t.Fatal("设它的 bot 没了, 这条提醒就静默消失了 —— 那是闹钟最糟的失败方式")
	}
}

// 晚响比不响强, 但**必须说清晚了多久** —— 用户据此判断还来不来得及.
func TestLateAlarmSaysHowLate(t *testing.T) {
	nowMs := time.Now().UnixMilli()
	clock := nowMs
	c := newTestOS(t, func() time.Time { return time.UnixMilli(clock) })
	if _, err := c.timers.Set("notes", clock+1000, "该出发了"); err != nil {
		t.Fatal(err)
	}
	clock += 40 * 60 * 1000 // 机器关了 40 分钟
	c.timers.Tick()
	found := ""
	for _, ev := range c.os.Log().Replay(abi.ProcessID("os"), 0) {
		if m, ok := ev.Payload.(map[string]any); ok {
			if s, _ := m["text"].(string); strings.Contains(s, "该出发了") {
				found = s
			}
		}
	}
	if found == "" {
		t.Fatal("错过的闹钟没补响")
	}
	if !strings.Contains(found, "晚了") {
		t.Fatalf("补响了却没说晚了多久: %q —— 用户没法判断还来不来得及", found)
	}
}

// 没接的组件不许出现在工具表里.
//
// 摆一个用不了的工具比没有更糟: 模型会照着调、许下做不到的承诺,
// 而它没有任何线索可以自纠 —— 提示词说有它就信.
func TestUnwiredComponentsAreAbsent(t *testing.T) {
	bare := (&components{}).toolsFor(persona{app: "notes"}, "p1")
	for _, name := range []string{"remind_me", "recall", "web_search", "view_image"} {
		if _, ok := bare.Get(name); ok {
			t.Errorf("没接这个服务, 工具表里却有 %s", name)
		}
	}
	c := newTestOS(t, nil)
	wired := c.toolsFor(persona{app: "notes"}, "p1")
	for _, name := range []string{"remind_me", "recall"} {
		if _, ok := wired.Get(name); !ok {
			t.Errorf("接了这个服务, 工具表里却没有 %s", name)
		}
	}
	// 这台机器没配搜索和视觉, 那就一个字都不该提
	for _, name := range []string{"web_search", "view_image"} {
		if _, ok := wired.Get(name); ok {
			t.Errorf("没配就不该挂 %s", name)
		}
	}
}

func streamHas(c *components, pid abi.ProcessID, want string) bool {
	for _, ev := range c.os.Log().Replay(pid, 0) {
		m, ok := ev.Payload.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := m["text"].(string); strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// 撤不掉必须**当场说**, 不能回一句"已经交给 OS 了".
//
// 把拒绝改成"撤掉了", 上面所有测试照样绿.
// 后果是最坏的那种 —— 用户以为提醒取消了, 到点它照样响;
// 或者他撤的其实是另一条, 而那一条从此再也不会响.
//
// **能加就得能减**, 而且"减没减掉"必须说实话.
func TestCancelTellsTheTruth(t *testing.T) {
	c := newTestOS(t, nil)
	run, ok := toolNamed(t, c, persona{app: "notes"}, "cancel")
	if !ok {
		t.Fatal("能设提醒却撤不掉 —— 说错一句话就永久多一条通知")
	}
	out, err := run(map[string]any{"id": "w999"})
	if err == nil {
		t.Fatalf("撤一条不存在的提醒, 工具回了 %q —— 用户以为撤掉了", out)
	}

	// 真有的那条要撤得掉, 而且真的从登记处消失
	set, _ := toolNamed(t, c, persona{app: "notes"}, "remind_me")
	at := time.Now().Add(time.Hour).UnixMilli()
	if _, err := set(map[string]any{"at": float64(at), "text": "该出发了"}); err != nil {
		t.Fatal(err)
	}
	id := c.timers.Pending()[0].ID
	if _, err := run(map[string]any{"id": id}); err != nil {
		t.Fatalf("撤真有的那条却失败了: %v", err)
	}
	if len(c.timers.Pending()) != 0 {
		t.Error("说撤掉了, 登记处里还在 —— 到点它照样响")
	}
}

// 重启之后 bot 要认得自己以前说过的话.
//
// 界面上"进程会死, 对话不死"一直成立(事件按 bot 标签拼得起来),
// 但 bot 自己原来是失忆的 —— 用户接着问"那个记事本做完了吗",
// 它答不知道, 而界面上明明摆着那段历史. 没有任何地方显示出错.
func TestHistoryFollowsTheBotNotThePid(t *testing.T) {
	c := newTestOS(t, nil)
	// 昨天那个进程
	old, err := c.os.Spawn(abi.ProcessSpec{
		App: "notes", Name: "记事本", Labels: map[string]string{"bot": "notes"},
	}, osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
		pc.Emit(map[string]any{"phase": "reply", "text": "记事本建好了"})
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	// 别的 bot 的历史不许混进来
	if _, err := c.os.Spawn(abi.ProcessSpec{
		App: "todo", Name: "待办", Labels: map[string]string{"bot": "todo"},
	}, osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
		pc.Emit(map[string]any{"phase": "reply", "text": "待办也建好了"})
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}

	// 进程是异步起的 —— 等它把话说完再看. 不等的话这条测试
	// 会时红时绿, 而那比它一直红更糟
	var hist []abi.Event
	for i := 0; i < 200; i++ {
		hist = c.historyOf("notes", "今天新起的pid")
		if joinedText(hist) != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(hist) == 0 {
		t.Fatal("重启后一句历史都没装回来 —— 它会当着用户的面装傻")
	}
	joined := joinedText(hist)
	// 别的 bot 那条也要等它说完, 否则"没混进来"可能只是因为它还没说
	for i := 0; i < 200 && joinedText(c.historyOf("todo", "x")) == ""; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(joined, "记事本建好了") {
		t.Error("装回来的历史里没有它自己说过的话")
	}
	if strings.Contains(joined, "待办也建好了") {
		t.Error("把别的 bot 的对话也装进来了 —— 它会以为那是自己干的")
	}
	// 自己这一辈子不算 —— 那段本来就在窗口里, 装两遍就是复读
	if len(c.historyOf("notes", old)) != 0 {
		t.Error("把自己当前这个进程的历史也当成'以前'装了一遍")
	}
}

func joinedText(evs []abi.Event) string {
	out := ""
	for _, ev := range evs {
		if m, ok := ev.Payload.(map[string]any); ok {
			if s, _ := m["text"].(string); s != "" {
				out += s + "\n"
			}
		}
	}
	return out
}

// 刚被换过工作区的, 下一轮必须**明说一句**.
//
// 换完之后它答"我在这房间实际用的工作区是 …/work/writer;
// 系统配置里写的 …/AI/发布文案 我没见过, 不猜是同一处".
// 它没说错 —— 装回来的历史里全是旧路径, 而且那些话是它自己说的.
// 系统段里那一句是新的, 但历史里有几十处旧的, 光靠一句压不过.
func TestMovedNoteOnlyAfterARebind(t *testing.T) {
	moved := movedNote(persona{rebound: true}, "/Users/x/AI/新地方")
	if !strings.Contains(moved, "/Users/x/AI/新地方") {
		t.Fatalf("换了地方却没说新路径: %q", moved)
	}
	if !strings.Contains(moved, "以前") {
		t.Fatalf("没说清历史里那些路径是旧的, 它会继续信历史: %q", moved)
	}
	if !strings.HasSuffix(moved, "\n") {
		t.Fatal("这一句要自成一行, 不然跟用户原话粘在一起")
	}
	// 没换过就一个字都不说 —— 每轮都带一句是纯噪音
	if got := movedNote(persona{}, "/Users/x/AI/新地方"); got != "" {
		t.Fatalf("没换过也说了: %q", got)
	}
	// 没有工作区(系统分的匿名目录)时也不说 —— 说了也没有信息
	if got := movedNote(persona{rebound: true}, ""); got != "" {
		t.Fatalf("没有路径却硬说: %q", got)
	}
}

// 拉进来的人: **同房间、同工作区、岗位记进名册**.
func TestHireLandsInTheSameRoomAndWorkspace(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	c := newTestOS(t, nil)
	c.roster = newBotRoster()
	var started []persona
	c.spawn = func(p persona) (abi.ProcessID, error) {
		started = append(started, p)
		return abi.ProcessID("p-new"), nil
	}
	boss := persona{name: "发版", thread: "#这次发布", work: "/Users/x/AI/oa"}
	out, err := c.hireFor(boss)("前端", "负责页面和交互")
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 {
		t.Fatalf("没起人: %v", started)
	}
	got := started[0]
	// 拉人是为了**一起干这摊活** —— 分开目录的话他连你写的代码都改不了
	if got.work != boss.work {
		t.Errorf("新人不在同一个工作区: %q", got.work)
	}
	if got.thread != boss.thread {
		t.Errorf("新人不在同一个房间: %q", got.thread)
	}
	if got.role != "负责页面和交互" {
		t.Errorf("岗位没带过去: %q", got.role)
	}
	// **岗位要活过重启**: 不记的话他变成通用助手, 上线只会问"要我干什么"
	saved := c.roster.load()
	if len(saved) != 1 || saved[0].Role != "负责页面和交互" || saved[0].Work != boss.work {
		t.Errorf("名册没记全: %+v", saved)
	}
	if !strings.Contains(out, "前端") {
		t.Errorf("没告诉招人的那个结果: %q", out)
	}
}

// 招人的自己不在房间里时, 说清楚**接下来会发生什么**.
//
// ── 判据换过一次 ──
//
// 第一版这里钉的是"要说实话: 你俩看不见对方". 那句话没错, 但它把
// 一个能自动解决的问题推回给了用户 —— 而 recruit 存在的理由恰恰是
// 不推回去. 现在直接开房间, 所以判据变成: 得说清房间叫什么、
// 以及**你这一轮说完才进得去**(换房间是换一个进程).
func TestHireSaysWhatHappensNext(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	c := newTestOS(t, nil)
	c.roster = newBotRoster()
	c.spawn = func(persona) (abi.ProcessID, error) { return "p-new", nil }
	out, err := c.hireFor(persona{name: "小记", work: "/Users/x/AI/oa"})("助手", "打杂")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "#oa") {
		t.Errorf("没说房间叫什么: %q", out)
	}
	if !strings.Contains(out, "说完话") {
		t.Errorf("没说清它自己什么时候进得去 —— 它会以为现在就能喊人: %q", out)
	}
	// 对话不会断这件事也要说: 不说的话它会以为重起 = 从头开始
	if !strings.Contains(out, "对话不会断") {
		t.Errorf("没说清重起之后历史还在: %q", out)
	}
}

// 重名当场拒 —— 名册和侧栏都按名字认人, 起两个同名的就是同一个人出现两次.
func TestHireRefusesDuplicateNames(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	c := newTestOS(t, nil)
	c.roster = newBotRoster()
	c.spawn = func(persona) (abi.ProcessID, error) { return "p-new", nil }
	if _, err := c.hireFor(persona{name: "发版"})("前端", "写页面"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.hireFor(persona{name: "发版"})("前端", "写页面"); err == nil {
		t.Fatal("重名也放行了 —— 界面上会出现两个同名的, 而且都活着")
	}
	// **内置的名字也不许撞**: 起一个也叫"回归"的, 用户在侧栏里分不出谁是谁
	if _, err := c.hireFor(persona{name: "发版"})("回归", "测试"); err == nil {
		t.Fatal("跟内置 bot 撞名也放行了")
	}
}

// 起不了进程的机器上, 这个能力整个不存在.
func TestNoHireWithoutSpawn(t *testing.T) {
	c := newTestOS(t, nil)
	if c.hireFor(persona{name: "x"}) != nil {
		t.Fatal("没有 spawn 却给了拉人的能力")
	}
}

// 招人的自己没房间时, **当场开一个** —— 拉进来却看不见对方等于没拉成.
func TestHireFormsARoomWhenThereIsNone(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	c := newTestOS(t, nil)
	c.roster = newBotRoster()
	var started []persona
	c.spawn = func(p persona) (abi.ProcessID, error) {
		started = append(started, p)
		return "p-new", nil
	}
	boss := persona{name: "OA助手", work: "/Users/x/AI/test-projects/oa"}
	out, err := c.hireFor(boss)("小登", "负责登录")
	if err != nil {
		t.Fatal(err)
	}
	// 房间名取项目目录最后一段 —— 那就是这摊活的名字
	if started[0].thread != "#oa" {
		t.Fatalf("房间名不对: %q", started[0].thread)
	}
	if !strings.Contains(out, "#oa") {
		t.Errorf("没告诉招人的那个房间叫什么: %q", out)
	}
	// **不能当场挪招人的那个**: 它正卡在 recruit 里等返回,
	// 换房间是换一个进程 —— 当场杀了它这一轮的活就断在半路
	if v, ok := c.pending.Load("OA助手"); !ok || v.(string) != "#oa" {
		t.Fatal("招人的那个没被排进「说完话再挪」的队列")
	}
	// 名册里记的也得是新房间, 否则重启后新人回到房间外面
	saved := c.roster.load()
	if len(saved) != 1 || saved[0].Thread != "#oa" {
		t.Errorf("名册里的房间不对: %+v", saved)
	}
}

// 已经在房间里的, 不许另开一个 —— 那会把一个组拆成两个.
func TestHireJoinsTheExistingRoom(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	c := newTestOS(t, nil)
	c.roster = newBotRoster()
	var started []persona
	c.spawn = func(p persona) (abi.ProcessID, error) {
		started = append(started, p)
		return "p-new", nil
	}
	boss := persona{name: "发版", thread: "#这次发布", work: "/Users/x/AI/oa"}
	if _, err := c.hireFor(boss)("前端", "写页面"); err != nil {
		t.Fatal(err)
	}
	if started[0].thread != "#这次发布" {
		t.Fatalf("没进已有的房间: %q", started[0].thread)
	}
	if _, ok := c.pending.Load("发版"); ok {
		t.Fatal("已经在房间里了还排队要挪 —— 那会白重起一次进程")
	}
}

// 排队的那次挪, **说完话才执行**, 而且只执行一次.
func TestPendingMoveRunsOnceWhenTheTurnEnds(t *testing.T) {
	c := &components{}
	moved := 0
	c.moveInto = func(name, thread string) error { moved++; return nil }
	c.pending.Store("OA助手", "#oa")
	c.applyPendingMove("别人")
	if moved != 0 {
		t.Fatal("挪错人了")
	}
	c.applyPendingMove("OA助手")
	c.applyPendingMove("OA助手")
	if moved != 1 {
		t.Fatalf("挪了 %d 次 —— 每次说完话都重起一遍进程, 对话会被反复打断", moved)
	}
}

// **状态事件的两种形态都要认**.
//
// 活着的时候 state 是 abi.ProcessState(原样的 Go 值), 从账本装回来之后
// 是 string(过了一趟 JSON). 只认 string 的话, "说完话再挪"这个功能
// 只在重放历史时生效、正常运行时不生效:
// 新人进了房间, 招人的那个永远没被挪过去.
//
// labels 可能是 map[string]string 也可能是 map[string]any;
// 同一类状态事件需要覆盖这两种形态.
func TestWaitingBotReadsBothPayloadShapes(t *testing.T) {
	c := newTestOS(t, nil)
	pid, err := c.os.Spawn(abi.ProcessSpec{App: "x", Name: "财务助手"},
		osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
			<-ctx.Done()
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	live := abi.Event{PID: pid, Kind: abi.EvProcState,
		Payload: map[string]any{"state": abi.StateWaiting}} // 活着的形态
	json := abi.Event{PID: pid, Kind: abi.EvProcState,
		Payload: map[string]any{"state": "waiting"}} // 过了一趟 JSON
	if got := waitingBot(c.os, live); got != "财务助手" {
		t.Errorf("活着的那种形态没认出来: %q —— 真跑的时候这个功能就是死的", got)
	}
	if got := waitingBot(c.os, json); got != "财务助手" {
		t.Errorf("装回来的那种形态没认出来: %q", got)
	}
	// 别的状态不许当成"说完话了"
	running := abi.Event{PID: pid, Kind: abi.EvProcState,
		Payload: map[string]any{"state": abi.StateRunning}}
	if got := waitingBot(c.os, running); got != "" {
		t.Errorf("还在干活就被当成说完了: %q —— 会在半路上换掉它的进程", got)
	}
}

// 这段对话结束时, 它起过的后台进程要**真的死掉**.
//
// 提示词里对模型许过"你起的后台进程活不过这段对话". 真内核那条路上是
// pid 命名空间保证的; console 是 in-proc 的, 没有命名空间 —— 这句话
// 只能靠 reap 变成真的.
//
// **判据是"它退出了", 不是"pid 还在不在"**: 被杀掉之后、被 Wait 回收之前
// 它是个僵尸, 而 kill(pid, 0) 对僵尸照样返回成功 —— 拿它当判据的话,
// 一个根本没杀掉的实现也能测过.
func TestStartedProcsAreReaped(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	crew := &startedProcs{}
	crew.track(cmd.Process.Pid)
	crew.reap()

	select {
	case <-exited: // 死了, 正是要的
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("对话结束了它还活着 —— 那句承诺就是空话, 而且它占着端口")
	}
	// 收第二遍不许崩: 进程可能早就自己结束了
	crew.track(cmd.Process.Pid)
	crew.reap()
}

// 闹钟那一条**必须自己说清楚"没有下一块了"**.
//
// say 那条通道默认是流: 界面收到一块就开一行, 等后面的块接上去, 行一直
// 开着(开着的行画一根光标, 而且按纯文本画). 关它靠的是进程状态变化 ——
// 而闹钟响的时候进程正停在 waiting, 不会再有状态变化.
//
// 否则一句早就说完的提醒, 后面会挂着一根不会消失的光标.
func TestWakeSaysItIsComplete(t *testing.T) {
	c := newTestOS(t, nil)
	pid, err := c.os.Spawn(abi.ProcessSpec{
		App: "notes", Labels: map[string]string{"bot": "notes", "name": "小记"},
	}, osinit.InprocBody{Entry: func(context.Context, osinit.ProcessContext) (any, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	c.fireInto(osinit.Wake{ID: "w1", Thread: "notes", Text: "该验收定时器了"}, 0)

	for _, ev := range c.os.Log().Replay(pid, 0) {
		m, ok := ev.Payload.(map[string]any)
		if !ok || ev.Kind != abi.EvProcOutput || m["channel"] != "say" {
			continue
		}
		text, _ := m["text"].(string)
		if !strings.Contains(text, "该验收定时器了") {
			continue
		}
		if m["done"] != true {
			t.Fatal("闹钟这条没说自己发完了 —— 界面会一直当它在写")
		}
		return
	}
	t.Fatal("闹钟那句话根本没发出来")
}

// 聊天里说"你去 X 干活"要能走通: 登记 → 这一轮说完 → 真搬
func TestMoveWork登记到说完再搬(t *testing.T) {
	dir := t.TempDir()
	var moved []string
	c := &components{rebind: func(name, work string) error {
		moved = append(moved, name+"→"+work)
		return nil
	}}
	move := c.moveWorkFor(persona{name: "小试"})
	if move == nil {
		t.Fatal("接了 rebind 却没有 move_work")
	}
	// 不存在的目录当场拒 —— 多半是路径听岔了
	if _, err := move(filepath.Join(dir, "不存在")); err == nil {
		t.Fatal("不存在的目录该拒")
	}
	said, err := move(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, dir) {
		t.Fatalf("回话没说搬去哪: %q", said)
	}
	// **这一轮还没说完, 不许搬**: 当场搬会把正干着的这一轮断在半路
	if len(moved) != 0 {
		t.Fatal("没说完就搬了")
	}
	c.applyPendingRebind("小试")
	if len(moved) != 1 || !strings.Contains(moved[0], dir) {
		t.Fatalf("没搬成: %v", moved)
	}
	// 幂等: 同一笔登记不搬两次
	c.applyPendingRebind("小试")
	if len(moved) != 1 {
		t.Fatalf("搬了两次: %v", moved)
	}
}

// 没接 rebind 的机器, 工具表里就不该有 move_work —— 摆一个用不了的
// 工具比没有更糟
func TestMoveWork没接就不挂(t *testing.T) {
	c := &components{}
	if c.moveWorkFor(persona{name: "小试"}) != nil {
		t.Fatal("没接 rebind 却给了 move_work")
	}
}
