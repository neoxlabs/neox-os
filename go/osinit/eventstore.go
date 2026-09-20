package osinit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/neox-os/neox-os/abi"
)

// 事件日志落盘.
//
// 内核对象②说的是"只追加、可重放". 之前只做到了前半句 ——
// 日志活在内存里, 进程一死对话就没了. 而"人不必待命在电脑边"
// 这个目标, 前提就是**它掉线之后还认得你**.
//
// ── 为什么是 JSONL 追加, 不是快照 ──
//
// EventLog 里早就有 Snapshot()/Restore(), 注释写着"落盘用",
// 但**没有任何人调用它** —— 这正是我自己定下的那条:
// 只支持不接入不算数, 那是 fail-open.
//
// 而且快照这个形态本身就不对: 每次写要重排整个日志 (O(n)),
// 写到一半崩了会毁掉之前所有的事件. 日志的语义是只追加,
// 落盘就该是**一条一行往后追加**:
//
//	· 崩溃最多丢最后一行, 前面的全在
//	· 写入是 O(1), 不随历史增长变慢
//	· 读回来只需顺序扫一遍
//
// 尾部半行是**正常现象**(断电/kill 都会留), 读的时候丢掉即可,
// 不当成错误 —— 当成错误会让一次意外掉电毁掉全部历史.
type EventStore struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	path string
}

// OpenEventStore 打开账本.
//
// keepConversations > 0 就先轮转再打开.
//
// ── 顺序为什么必须在这里面定死 ──
//
// 轮转是 rename 一个新文件盖住旧的. 如果账本**已经被打开**,
// 那个 fd 指向的是**已经被删掉的旧 inode** —— 后面所有写入都进了
// 一个没有名字的文件, 静默消失, 一点错都不报.
//
// 如果调用方先 Open 再 Rotate,
// 结果那次会话整段对话没落盘, 下次 /继续 接到了更早的一段,
// 用户看到的是"它把我刚说的全忘了".
//
// 所以不能靠调用方记住顺序 —— 记错一次就是丢数据, 而且查不出来.
// 把轮转收进来, 调用方**没有机会弄错**.
func OpenEventStore(path string, keepConversations ...int) (*EventStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if len(keepConversations) > 0 && keepConversations[0] > 0 {
		if _, err := rotateIfNeeded(path, keepConversations[0]); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &EventStore{f: f, w: bufio.NewWriter(f), path: path}, nil
}

// Append 落一条.
//
// 每条都 Flush —— 攒着写会在崩溃时丢掉一整批.
// 这里的代价可以接受: 事件是人的动作和工具调用的节奏, 不是高频流.
func (s *EventStore) Append(ev abi.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.w.Flush()
}

func (s *EventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.Flush()
	return s.f.Close()
}

// LoadEvents 读回来, 按进程分好.
//
// 半行直接丢. 断电和 kill 都会留下半行, 那是**正常现象**,
// 报错会让一次意外掉电毁掉全部历史.
// Forget 把这些进程的历史从**账本里**抹掉.
//
//	重写整个文件, 然后原子替换 —— append-only 的账本没有别的删法.
//	写临时文件再 rename: 写到一半断电不该留下半个账本,
//	那会让下次开机丢掉全部历史(比没删干净严重得多).
//
//	**必须跟 EventLog.Forget 成对调用**. 只删内存 = 下次开机复活;
//	只删账本 = 这次会话还看得见. 两个都做才叫删掉了.
func (s *EventStore) Forget(pids map[abi.ProcessID]bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		return 0, err
	}
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	tmp := s.path + ".rewrite"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		f.Close()
		return 0, err
	}
	writer := bufio.NewWriter(out)
	dropped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev abi.Event
		// 解不开的行**留着** —— 它可能是别人的半行, 删掉等于替用户
		// 做了一个我们没依据做的决定
		if json.Unmarshal(line, &ev) == nil && pids[ev.PID] {
			dropped++
			continue
		}
		writer.Write(line)
		writer.WriteByte('\n')
	}
	f.Close()
	if err := writer.Flush(); err != nil {
		out.Close()
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return 0, err
	}
	// 换了 inode, 原来那个 fd 指向的是已经没有名字的旧文件 ——
	// 不重开的话后面所有写入都静默消失.
	s.f.Close()
	reopened, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return dropped, err
	}
	s.f = reopened
	s.w = bufio.NewWriter(reopened)
	return dropped, nil
}

// ScanEvents 流式读一遍落盘的账本 —— fn 返回 false 就停.
//
// ── 为什么不能用 LoadEvents ──
//
//	那个把整份账本读进一张 map. 开机装回来时该这么做(反正要全装),
//	但"他今天去过哪儿"这类查询一天可能问好几次, 每次几 MB 不值.
//
// ── 为什么要读文件, 而不是读内存里那份 ──
//
//	内存那份被 CompactSense 压过: 只留最后一份摘要和它之后的信号
//	(见 compact.go —— 那是对的, 不然一台跑一年的机器内存里躺着全部历史).
//	于是 Replay 出来的位置信号只剩最近一小段 —— 真机测试里他问
//	"我上周三去哪儿了", 账本文件里明明有 34 条, 它答"手机没上报".
//
//	**落盘那份才是完整的流水**, 这类问题只能问它.
func ScanEvents(path string, fn func(abi.Event) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev abi.Event
		if json.Unmarshal(line, &ev) != nil {
			continue // 半行/坏行跳过 —— 同 LoadEvents
		}
		if !fn(ev) {
			return nil
		}
	}
	return nil
}

func LoadEvents(path string) (map[abi.ProcessID][]abi.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[abi.ProcessID][]abi.Event{}, nil
		}
		return nil, err
	}
	defer f.Close()

	out := map[abi.ProcessID][]abi.Event{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev abi.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // 半行/坏行跳过, 不是错误
		}
		out[ev.PID] = append(out[ev.PID], ev)
	}
	// scanner 自己的错误也不上报 —— 同样的理由
	return out, nil
}

// 账本轮转.
//
// 账本只增不减, 聊久了每次启动都要把整个文件读一遍 —— 越用越慢.
//
// ── 两条红线 ──
//
//	① **按整段对话挪, 不按行数砍.**
//	   从中间截断会留下半段历史: 前面的消息在, 后面的回答没了.
//	   恢复出来"看着对但其实不对" —— 那比没有历史更糟.
//
//	② **不许真删, 挪进归档.**
//	   账本里是对话原文. 为了启动快一点就把它抹掉,
//	   等于拿数据换启动方便. 归档文件留在原地,
//	   要翻旧账随时能翻, 只是不进每次启动的热路径.
//
//	③ **sense 不是一段对话, 不参与排队.**
//	   地点、关注、闹钟、打扰额度全挂在 sense 这一个 pid 上.
//	   而排序的依据是"最后一条事件的时间" —— 手机关了 / HA 停了,
//	   sense 就不再追加事件, 用户接着聊 20 次它就成了最老的那个,
//	   被整段挪进归档. 下次开机读活跃账本: **地点没了、关注没了、
//	   定好的提醒没了**, 一句报错都没有 —— 用户只会在某天发现
//	   "我让它提醒的事它没提醒".
const rotateAfterEvents = 20000

// compactAfterSenseEvents sense 攒到多少条才值得压紧.
//
// 摘要按窗口产出, 一天百来条 —— 5000 条约等于一个多月.
// 定得再低没有意义: 压紧要重写整个账本, 而这几千条读一遍是毫秒级的.
const compactAfterSenseEvents = 5000

// rotateIfNeeded 活跃账本太大就把旧对话挪进归档.
//
// **不导出**: 它必须在账本被打开之前跑, 而那个顺序由 OpenEventStore 保证.
// 导出它就等于把一个"记错就静默丢数据"的顺序交给调用方.
func rotateIfNeeded(path string, keepConversations int) (int, error) {
	byPID, err := LoadEvents(path)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, evs := range byPID {
		total += len(evs)
	}
	// sense 先摘出去: 它是这台机器的状态, 不是一段可以归档的对话.
	// 摘的是**排序的资格**, 不是名额 —— 它不占对话的 20 席,
	// 否则等于"多留一份状态就少留一段对话".
	convCount := 0
	for pid := range byPID {
		if pid != signalPID {
			convCount++
		}
	}
	// **压紧要有自己的闸.**
	//
	// 挂在对话轮转上是不够的: 轮转的门槛是"对话超过 20 段", 而一台
	// 主要在感知、很少聊天的机器**永远不会到那个门槛** —— 那恰恰是
	// 这个系统最终要跑的样子(它替你盯着, 你偶尔说句话).
	// 于是 sense 一路长到几万条, 每次开机全量重读.
	needRotate := total > rotateAfterEvents && convCount > keepConversations
	senseKeep, senseDrop := byPID[signalPID], []abi.Event(nil)
	if len(senseKeep) > compactAfterSenseEvents {
		senseKeep, senseDrop = compactSense(senseKeep)
	}
	if !needRotate && len(senseDrop) == 0 {
		return 0, nil
	}

	// 按最后一条事件的时间排 —— 留最近的几段
	type conv struct {
		pid abi.ProcessID
		at  int64
	}
	var convs []conv
	for pid, evs := range byPID {
		if pid == signalPID {
			continue
		}
		convs = append(convs, conv{pid, evs[len(evs)-1].At})
	}
	sort.Slice(convs, func(i, j int) bool { return convs[i].at > convs[j].at })

	keep := map[abi.ProcessID]bool{}
	if _, ok := byPID[signalPID]; ok {
		keep[signalPID] = true
	}
	for i := 0; i < keepConversations && i < len(convs); i++ {
		keep[convs[i].pid] = true
	}
	if !needRotate {
		// 只是来压紧 sense 的 —— 一段对话都不许动.
		// 不写这一句的话, "sense 长大了"会顺手把用户的对话也归档掉
		for _, c := range convs {
			keep[c.pid] = true
		}
	}

	// 先把要挪走的追加进归档, 再重写活跃账本 ——
	// 顺序不能反: 反了的话中间崩溃就是**真的丢数据**.
	arch, err := os.OpenFile(path+".archive", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	moved := 0
	aw := bufio.NewWriter(arch)
	// 压掉的先进归档 —— 红线② 对压紧同样成立: 不许真删.
	// 响过的闹钟、上上周的摘要, 是"校准阈值"唯一的原始素材
	// (--sense-report 就是对着账本跑的), 抹掉等于把调参的依据烧了
	for _, e := range senseDrop {
		b, _ := json.Marshal(e)
		aw.Write(append(b, '\n'))
	}
	for _, c := range convs {
		if keep[c.pid] {
			continue
		}
		for _, ev := range byPID[c.pid] {
			b, _ := json.Marshal(ev)
			aw.Write(append(b, '\n'))
		}
		moved++
	}
	if err := aw.Flush(); err != nil {
		arch.Close()
		return 0, err
	}
	if err := arch.Close(); err != nil {
		return 0, err
	}

	// 归档落定之后才动活跃账本. 写临时文件再改名 ——
	// 直接截断原文件的话, 写到一半崩溃会留下一个半截账本.
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriter(f)
	// sense 写在最前 —— convs 里已经没有它了, 漏在这儿的话
	// "不许轮转掉"就成了"轮转时悄悄丢掉", 比原来的 bug 还隐蔽
	for _, ev := range senseKeep {
		b, _ := json.Marshal(ev)
		w.Write(append(b, '\n'))
	}
	for i := len(convs) - 1; i >= 0; i-- {
		if !keep[convs[i].pid] {
			continue
		}
		for _, ev := range byPID[convs[i].pid] {
			b, _ := json.Marshal(ev)
			w.Write(append(b, '\n'))
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return 0, err
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return moved, nil
}
