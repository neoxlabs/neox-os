package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

// botKeeper 名册上的人要活着.
//
//	"进程会死, 对话不死"开机时是成立的: 名册走一遍, 每个人起一个新进程.
//	开机之后就没人管了. 进程自己退了(崩了、跑完、被意外杀掉),
//	/say 回 ok:false, 界面锁死输入框, 用户只能重启客户端 ——
//	重启之所以管用, 只是因为它又走了一遍开机那条 spawn.
//
//	这个人把开机那条路接到运行中: 话送到死人身上就拉起来再投;
//	终态事件来了, 过一小会儿还没人顶着这个名字, 也拉起来.
var errBuried = errors.New("这个 bot 已经删了")

type botKeeper struct {
	os     *osinit.OS
	roster *botRoster
	spawn  func(persona) (abi.ProcessID, error)
	mu     sync.Mutex
}

func (k *botKeeper) live(name string) abi.ProcessID {
	if k == nil || k.os == nil || name == "" {
		return ""
	}
	for _, info := range k.os.List() {
		if info.Spec.Name == name && !info.State.IsTerminal() {
			return info.PID
		}
	}
	return ""
}

func (k *botKeeper) ensure(name string) (abi.ProcessID, error) {
	if k == nil || name == "" {
		return "", fmt.Errorf("没有要拉的人")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if live := k.live(name); live != "" {
		return live, nil
	}
	if k.roster != nil && k.roster.buried()[name] {
		return "", errBuried
	}
	next, _, err := findPersona(k.roster, name)
	if err != nil {
		return "", err
	}
	if k.roster != nil {
		if thread, ok := k.roster.rooms()[name]; ok {
			next.thread = thread
		}
		if work, ok := k.roster.works()[name]; ok {
			next.work = work
		}
	}
	next.returning = true
	return k.spawn(next)
}

func (k *botKeeper) fromDead(dead abi.ProcessID) (abi.ProcessID, error) {
	if k == nil || k.os == nil {
		return "", fmt.Errorf("没有 OS")
	}
	info, ok := k.os.Info(dead)
	if !ok || info.Spec.Name == "" {
		return "", fmt.Errorf("认不出这个进程是谁")
	}
	return k.ensure(info.Spec.Name)
}

// watch 谁终态了, 过一小会儿还没人顶着这个名字就拉起来.
//
//	停 / 换房间 / 换工作区也是"先杀再起". 立刻拉会跟它们撞成两个.
//	那几条路径自己会马上 spawn, 等 300ms 再看一眼就够了.
func (k *botKeeper) watch() func() {
	if k == nil || k.os == nil {
		return func() {}
	}
	return k.os.Log().SubscribeAll(func(e abi.Event) {
		if e.Kind != abi.EvProcState {
			return
		}
		body, _ := e.Payload.(map[string]any)
		// 活着的时候 state 是 ProcessState, 装回账本之后是 string ——
		// 只认一种, 真跑的时候这条路径就是死的. 见 waitingBot.
		if !terminalState(body["state"]) {
			return
		}
		info, ok := k.os.Info(e.PID)
		if !ok || info.Spec.Name == "" {
			return
		}
		name := info.Spec.Name
		go func() {
			time.Sleep(300 * time.Millisecond)
			if _, err := k.ensure(name); err != nil && !errors.Is(err, errBuried) {
				fmt.Fprintf(os.Stderr, "拉不回 %s: %v\n", name, err)
			}
		}()
	})
}

// sweep 名册上该在的人, 现在没人活着就拉起来.
//
//	订阅会漏: 删对话会先 Drop 再出终态, Info 已经没了, watch 认不出是谁.
//	漏了就靠这一圈兜住. 删掉的(墓碑)不在名单里.
func (k *botKeeper) sweep() {
	if k == nil {
		return
	}
	for name := range k.wanted() {
		if k.live(name) != "" {
			continue
		}
		if _, err := k.ensure(name); err != nil && !errors.Is(err, errBuried) {
			fmt.Fprintf(os.Stderr, "巡视拉不回 %s: %v\n", name, err)
		}
	}
}

func terminalState(v any) bool {
	switch state := v.(type) {
	case string:
		return abi.ProcessState(state).IsTerminal()
	case abi.ProcessState:
		return state.IsTerminal()
	}
	return false
}

func (k *botKeeper) wanted() map[string]bool {
	names := map[string]bool{}
	for _, p := range seedPersonas() {
		names[p.name] = true
	}
	if k.roster == nil {
		return names
	}
	for _, saved := range k.roster.load() {
		if !saved.Gone && !saved.Builtin {
			names[saved.Name] = true
		}
	}
	for name, gone := range k.roster.buried() {
		if gone {
			delete(names, name)
		}
	}
	return names
}
