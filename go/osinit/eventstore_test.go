package osinit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sub", "events.jsonl")
}

// 落盘要在事件产生的那一刻做. 退出时统一写等于"崩溃就全丢",
// 那正是要防的场景.
func TestEventsPersistedImmediately(t *testing.T) {
	p := storePath(t)
	st, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	st.Append(abi.Event{Seq: 0, PID: "p1", Kind: abi.EvInputRecv,
		Payload: map[string]any{"text": "你好"}})

	// 不 Close 就读 —— 模拟进程被 kill
	got, err := LoadEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["p1"]) != 1 {
		t.Fatalf("事件没有立刻落盘: %+v", got)
	}
}

// 尾部半行是正常现象(断电/kill 都会留). 报错会让一次意外掉电毁掉全部历史.
func TestPartialTailLineIsNotFatal(t *testing.T) {
	p := storePath(t)
	st, _ := OpenEventStore(p)
	for i := 0; i < 3; i++ {
		st.Append(abi.Event{Seq: i, PID: "p1", Kind: abi.EvProcOutput,
			Payload: map[string]any{"phase": "reply", "text": "话"}})
	}
	st.Close()

	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"seq":3,"pid":"p1","ki`) // 断在半路
	f.Close()

	got, err := LoadEvents(p)
	if err != nil {
		t.Fatalf("半行不该是错误: %v", err)
	}
	if len(got["p1"]) != 3 {
		t.Fatalf("半行毁掉了前面的历史: 剩 %d 条", len(got["p1"]))
	}
}

// 顺序必须保持 —— 对话恢复出来顺序错了比没有更糟
func TestOrderPreserved(t *testing.T) {
	p := storePath(t)
	st, _ := OpenEventStore(p)
	for i := 0; i < 20; i++ {
		st.Append(abi.Event{Seq: i, PID: "p1", Kind: abi.EvInputRecv,
			Payload: map[string]any{"text": "第几句"}})
	}
	st.Close()
	got, _ := LoadEvents(p)
	for i, ev := range got["p1"] {
		if ev.Seq != i {
			t.Fatalf("第 %d 条的 seq 是 %d", i, ev.Seq)
		}
	}
}

// 不同进程要分开 —— 混在一起会把两段对话串味
func TestEventsSplitByProcess(t *testing.T) {
	p := storePath(t)
	st, _ := OpenEventStore(p)
	st.Append(abi.Event{PID: "p1", Kind: abi.EvInputRecv, Payload: map[string]any{"text": "a"}})
	st.Append(abi.Event{PID: "p2", Kind: abi.EvInputRecv, Payload: map[string]any{"text": "b"}})
	st.Close()
	got, _ := LoadEvents(p)
	if len(got["p1"]) != 1 || len(got["p2"]) != 1 {
		t.Fatalf("进程没分开: %+v", got)
	}
}

// 文件不存在不是错误 —— 第一次启动就是这样
func TestMissingFileIsEmptyNotError(t *testing.T) {
	got, err := LoadEvents(filepath.Join(t.TempDir(), "没有.jsonl"))
	if err != nil {
		t.Fatalf("第一次启动不该报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatal("凭空多出了事件")
	}
}

// OS 装上 store 之后, 真跑一个进程要能在盘上留下痕迹
func TestOSWritesThroughToDisk(t *testing.T) {
	p := storePath(t)
	st, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	o := devOS(func(opt *Options) { opt.EventStore = st })
	defer o.Shutdown("t")
	pid, err := o.Spawn(abi.ProcessSpec{App: "x"}, InprocBody{
		Entry: func(ctx context.Context, pc ProcessContext) (any, error) { return "ok", nil }})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, o, pid, abi.StateExited)

	got, _ := LoadEvents(p)
	if len(got[pid]) == 0 {
		t.Fatal("进程跑完了盘上一条都没有")
	}
}

// 重启后 pid 不许从头再来.
//
// 不跳过账本里用掉的号, 重启后第一个进程又叫 p1, 它的新事件会追加到
// 旧 p1 的流上 —— 两段毫不相干的对话在账本里混成一条.
// 恢复一次之后, 下一次全新对话又拿到 p1 会复用旧事件流.
func TestPIDsDoNotRestartAfterRestore(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	// 用**本实例的前缀** —— 别的实例的号带着别的前缀, 天然不会撞,
	// 要防的是本实例重启后复用自己用过的号
	pfx := o.pidPrefix
	o.RestoreEvents(map[abi.ProcessID][]abi.Event{
		abi.ProcessID(pfx + "1"): {{Kind: abi.EvInputRecv}},
		abi.ProcessID(pfx + "7"): {{Kind: abi.EvInputRecv}},
		abi.ProcessID(pfx + "3"): {{Kind: abi.EvInputRecv}},
	})
	pid, err := o.Spawn(abi.ProcessSpec{App: "x"}, InprocBody{
		Entry: func(ctx context.Context, pc ProcessContext) (any, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"1", "3", "7"} {
		if string(pid) == pfx+n {
			t.Fatalf("复用了账本里已经用掉的 %s —— 两段对话会串味", pfx+n)
		}
	}
}

// 空账本不该把计数器推高 —— 第一次启动就是这样
func TestEmptyRestoreKeepsCounter(t *testing.T) {
	o := devOS()
	defer o.Shutdown("t")
	o.RestoreEvents(map[abi.ProcessID][]abi.Event{})
	pid, _ := o.Spawn(abi.ProcessSpec{App: "x"}, InprocBody{
		Entry: func(ctx context.Context, pc ProcessContext) (any, error) { return nil, nil }})
	// 号从 1 起. 前缀是本实例的 (宿主 PID + 实例序号)
	if want := o.pidPrefix + "1"; string(pid) != want {
		t.Fatalf("第一个进程该是 %s, 拿到 %s", want, pid)
	}
}

func bigLedger(t *testing.T, convs, perConv int) string {
	t.Helper()
	p := storePath(t)
	st, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	for c := 0; c < convs; c++ {
		pid := abi.ProcessID(fmt.Sprintf("p%d", c))
		for i := 0; i < perConv; i++ {
			st.Append(abi.Event{Seq: i, PID: pid, At: int64(c*1000 + i),
				Kind: abi.EvInputRecv, Payload: map[string]any{"text": "话"}})
		}
	}
	st.Close()
	return p
}

// 轮转按**整段对话**挪, 不按行数砍.
// 从中间截断会留下半段历史: 输入在, 后面的回答没了 ——
// 恢复出来"看着对但其实不对", 比没有历史更糟.
func TestRotateMovesWholeConversations(t *testing.T) {
	p := bigLedger(t, 40, 600) // 24000 条, 超过阈值
	moved, err := rotateIfNeeded(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 30 {
		t.Fatalf("该挪走 30 段, 实际 %d", moved)
	}
	left, _ := LoadEvents(p)
	if len(left) != 10 {
		t.Fatalf("活跃账本剩 %d 段, 期望 10", len(left))
	}
	for pid, evs := range left {
		if len(evs) != 600 {
			t.Fatalf("%s 只剩 %d 条 —— 对话被从中间砍断了", pid, len(evs))
		}
	}
}

// 留下的必须是**最近的**几段 —— 挪走刚聊过的等于用户一回头就发现没了
func TestRotateKeepsMostRecent(t *testing.T) {
	p := bigLedger(t, 40, 600)
	rotateIfNeeded(p, 5)
	left, _ := LoadEvents(p)
	for _, want := range []abi.ProcessID{"p35", "p36", "p37", "p38", "p39"} {
		if _, ok := left[want]; !ok {
			t.Fatalf("最近的 %s 被挪走了", want)
		}
	}
}

// 不许真删 —— 挪走的必须能在归档里找回来.
// 为了启动快一点抹掉输入原文, 等于用数据完整性换启动速度.
func TestRotatedConversationsSurviveInArchive(t *testing.T) {
	p := bigLedger(t, 40, 600)
	rotateIfNeeded(p, 10)
	arch, err := LoadEvents(p + ".archive")
	if err != nil {
		t.Fatal(err)
	}
	if len(arch) != 30 {
		t.Fatalf("归档里只有 %d 段, 挪走的对话丢了", len(arch))
	}
	if len(arch["p0"]) != 600 {
		t.Fatalf("归档里的 p0 只有 %d 条, 不完整", len(arch["p0"]))
	}
}

// 没到阈值就一个字都不许动
func TestNoRotationUnderThreshold(t *testing.T) {
	p := bigLedger(t, 3, 10)
	before, _ := os.ReadFile(p)
	moved, err := rotateIfNeeded(p, 2)
	if err != nil || moved != 0 {
		t.Fatalf("不该轮转: moved=%d err=%v", moved, err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("没到阈值却改了账本")
	}
}

// 轮转之后还要能继续追加, 且新旧都在
func TestAppendStillWorksAfterRotate(t *testing.T) {
	p := bigLedger(t, 40, 600)
	rotateIfNeeded(p, 10)
	st, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	st.Append(abi.Event{PID: "新对话", Kind: abi.EvInputRecv,
		Payload: map[string]any{"text": "轮转之后说的话"}})
	st.Close()
	got, _ := LoadEvents(p)
	if len(got["新对话"]) != 1 {
		t.Fatal("轮转之后写不进去了")
	}
	if len(got) != 11 {
		t.Fatalf("轮转后账本里有 %d 段, 期望 10 旧 + 1 新", len(got))
	}
}

// 轮转之后写进去的必须还在.
//
// 先 Open 再 Rotate 会丢失一整段对话: 那个 fd 指向的是
// **已经被 rename 删掉的旧 inode**, 后面所有写入进了一个没有名字的文件,
// 静默消失、一点错都不报. 下次 /继续 接到了更早的一段,
// 用户看到的是"它把我刚说的全忘了".
//
// 所以轮转收进了 OpenEventStore —— 调用方没有机会弄错顺序.
func TestWritesAfterRotationAreNotLost(t *testing.T) {
	p := bigLedger(t, 40, 600)

	st, err := OpenEventStore(p, 10) // 打开时顺带轮转
	if err != nil {
		t.Fatal(err)
	}
	st.Append(abi.Event{PID: "刚说的话", Kind: abi.EvInputRecv,
		Payload: map[string]any{"text": "轮转之后说的这句话不能丢"}})
	st.Close()

	got, _ := LoadEvents(p)
	if len(got["刚说的话"]) != 1 {
		t.Fatal("轮转之后写的内容丢了 —— 写进了被删掉的 inode")
	}
	if len(got) != 11 {
		t.Fatalf("账本里 %d 段, 期望 10 旧 + 1 新", len(got))
	}
}

// 不传 keep 就不轮转 —— 缺省不该悄悄动用户的账本
func TestOpenWithoutKeepDoesNotRotate(t *testing.T) {
	p := bigLedger(t, 40, 600)
	st, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	got, _ := LoadEvents(p)
	if len(got) != 40 {
		t.Fatalf("没要求轮转却动了账本: 剩 %d 段", len(got))
	}
}

// 两个 OS 实例并发起进程, 号不许撞.
//
// 进程号必须**整台机器唯一**, 不只是单实例内唯一 —— socket 路径和
// 事件账本都是整台机器共享的. 同时运行三个 OS 实例时:
// 三个实例都想要 /run/neox-os/p1.sock, 后两个进程直接起不来(退出码 2);
// 三份 p1 的事件写进同一个账本, seq 乱成一团, 历史串味.
func TestTwoInstancesNeverCollideOnPIDs(t *testing.T) {
	a, b := devOS(), devOS()
	defer a.Shutdown("t")
	defer b.Shutdown("t")

	seen := map[abi.ProcessID]bool{}
	for i := 0; i < 5; i++ {
		for _, o := range []*OS{a, b} {
			pid, err := o.Spawn(abi.ProcessSpec{App: "x"}, InprocBody{
				Entry: func(ctx context.Context, pc ProcessContext) (any, error) {
					return nil, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			if seen[pid] {
				t.Fatalf("两个实例都分到了 %s —— socket 会撞, 账本会串味", pid)
			}
			seen[pid] = true
		}
	}
}

// **sense 不是一段对话, 是这台机器的状态 —— 它绝不能被轮转掉.**
//
// 地点、关注、闹钟、打扰额度全挂在 sense 这一个 pid 上. 而轮转是按
// "最后一条事件的时间"留最近 20 段 —— sense 跟对话一起排队.
//
// 于是: 手机关了 / HA 停了, sense 就不再追加事件; 用户接着聊 20 次,
// sense 就成了最老的那个, 被整段挪进归档. 下次开机 Restore 读活跃账本,
// **地点没了、关注没了、定好的提醒没了**, 而且一句报错都没有 ——
// 用户只会在某天发现"我让它提醒的事它没提醒".
func TestSenseIsNeverRotatedAway(t *testing.T) {
	p := bigLedger(t, 40, 600)

	// sense 的最后一条事件比所有对话都老 —— 正是"一阵子没有信号"的样子
	st, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	st.Append(abi.Event{Seq: 0, PID: signalPID, At: 1,
		Kind: abi.EvWakeSet, Payload: map[string]any{"id": "w1", "note": "吃药"}})
	st.Close()

	if _, err := rotateIfNeeded(p, 10); err != nil {
		t.Fatal(err)
	}
	left, _ := LoadEvents(p)
	if _, ok := left[signalPID]; !ok {
		t.Fatal("sense 被轮转掉了 —— 下次开机地点/关注/提醒全没了, 而且不报错")
	}
}

// 留 sense 不能占掉对话的名额 —— 它是额外的一段, 不是 20 段里的一段
func TestKeepingSenseDoesNotCostAConversationSlot(t *testing.T) {
	p := bigLedger(t, 40, 600)
	st, _ := OpenEventStore(p)
	st.Append(abi.Event{Seq: 0, PID: signalPID, At: 1, Kind: abi.EvWakeSet,
		Payload: map[string]any{"id": "w1"}})
	st.Close()

	rotateIfNeeded(p, 10)
	left, _ := LoadEvents(p)
	convs := 0
	for pid := range left {
		if pid != signalPID {
			convs++
		}
	}
	if convs != 10 {
		t.Fatalf("留了 %d 段对话, 期望 10 —— sense 挤掉了一段用户的对话", convs)
	}
}

// 落盘那份才是完整的流水 —— 内存那份被 CompactSense 压过.
// 查询"我上周三去哪儿了"时, 文件里可能有 34 条位置, 内存里却一条不剩
func TestScanEventsStreamsAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	st, err := OpenEventStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := st.Append(abi.Event{Seq: i, PID: "sense", Kind: abi.EvSignal,
			Payload: map[string]any{"kind": "location"}}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	n := 0
	if err := ScanEvents(path, func(abi.Event) bool { n++; return true }); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("读到 %d 条", n)
	}
	// fn 说停就停 —— 一份跑一年的账本不该为了一次查询整份读完
	seen := 0
	ScanEvents(path, func(abi.Event) bool { seen++; return seen < 2 })
	if seen != 2 {
		t.Fatalf("没停下来: %d", seen)
	}
	if err := ScanEvents(filepath.Join(t.TempDir(), "没有这个"), func(abi.Event) bool { return true }); err == nil {
		t.Fatal("文件不存在该报错 —— 调用方靠它决定退回内存那份")
	}
}
