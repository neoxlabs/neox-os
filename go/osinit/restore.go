package osinit

import (
	"fmt"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 开机恢复 —— 把散在各处的"记得也读一下"收成一处.
//
// ── 为什么值得单独做 ──
//
// **"重启之后没被恢复的状态"已经出过三次**:
//
//	S26  打扰额度   一天重启三次就打扰九次
//	S30  静音表     崩一次那个噪音种类又开始吵
//	S31  去重表     每重启一次账本里的信号就多一份
//
// 三次的形状完全一样: 一个只活在内存里的判断依据, 而**它挡的事情
// 恰恰在重启前后最容易发生**. 每次的修法也一样: 补一个 Restore,
// 再在 main 里补一处 for 循环.
//
// 而 main 里现在有七处那样的循环, 散在三百多行里. 加第八个状态时
// 忘掉一处, 症状还是"看起来一切正常, 只是某件事悄悄不算数了" ——
// **靠记性挡不住这类错, 第四次一定还会发生.**
//
// 所以收成一个函数: 调用方**没有机会漏**, 而"该恢复什么"这个问题
// 有了唯一的一份答案.
//
// ── 顺序不是随便的 ──
//
// 每一条都对着一个具体的失败, 见下面各步的注释.

// RestoreTargets 要恢复的东西. 允许为 nil —— 没开感知层的时候
// 大半都是 nil, 那是正常的
type RestoreTargets struct {
	Timers *Timers
	People *People
	// World 此刻的世界. **它是账本的一个投影** —— 不重建的话, 重启之后
	// OS 就不知道人在哪了, 而且不会自己好起来(采集端按移动触发,
	// 人不动就不报)
	World   *World
	Devices *Devices
	Places  *Places
	Watches *Watches
	Budget  *InterruptBudget
	Daily   *DailyReport
	Bus     *SignalBus
	// Presence 家里有没有人. **第四例** —— 它是从信号推导出来的状态,
	// 而 RecoverPending 只捞最后一份摘要之后的信号, 三小时前那条
	// "离开家了"不会重放(见 presence.Restore)
	Presence *Presence
}

// RestoreSummary 恢复了些什么 —— 要能打给用户看.
//
// **静默恢复是不行的**: "它记得我定的提醒"和"它把提醒忘了"
// 长得一模一样, 直到该响的时候.
type RestoreSummary struct {
	Wakes   int
	Facts   int
	People  int
	Devices int
	Places  int
	Watches int
	Mutes   int
	// PlaceNames 认得哪些地方. **要点名, 不能只报个数** ——
	// "认得 3 个地方"跟"认得家/公司/健身房"对用户的意义完全不一样,
	// 后者他能一眼看出少了哪个
	PlaceNames []string
	SeenKeys   int
	Pending    int
	Used       int
	Quota      int
	Held       int
	// SawPlacesTable 这次恢复到底管不管地点 —— 没装地点表的调用方
	// (比如只恢复闹钟的测试)不该被念叨"还不认得任何地方"
	SawPlacesTable bool
}

func (s RestoreSummary) Lines() []string {
	var out []string
	if s.Facts > 0 {
		out = append(out, fmt.Sprintf("🌐 装回 %d 条事实", s.Facts))
	}
	if s.People > 0 {
		out = append(out, fmt.Sprintf("👥 屋里 %d 个人", s.People))
	}
	if s.Devices > 0 {
		out = append(out, fmt.Sprintf("📱 屋里 %d 台设备", s.Devices))
	}
	if s.Wakes > 0 {
		out = append(out, fmt.Sprintf("⏰ 记着 %d 个提醒", s.Wakes))
	}
	// **认得哪些地方** —— 这一行 S32 重构时丢过一次, 而 S67 证明它是
	// 决定性的: 没认过"家"的话, 手机投的坐标查不到地名, "家里有没有人"
	// 永远是"不知道", 那件★从此不会被说, 而且是静默的
	if len(s.PlaceNames) > 0 {
		out = append(out, "📍 认得 "+strings.Join(s.PlaceNames, "、"))
	} else if s.SawPlacesTable {
		// **一个都没认过时要显眼地说** —— 那时候手机的位置信号会全部
		// 白投(查不到地名 → 判不出在不在家), 而用户无从知道
		out = append(out, "📍 还不认得任何地方 —— 位置信号会白投, "+
			"到了某个地方跟它说一句\"这儿是家\"")
	}
	if s.Watches > 0 {
		out = append(out, fmt.Sprintf("👁 盯着 %d 件事", s.Watches))
	}
	if s.Mutes > 0 {
		out = append(out, fmt.Sprintf("🔇 静着 %d 个信号种类(/静音 看是哪些)", s.Mutes))
	}
	if s.Used > 0 || s.Held > 0 {
		l := fmt.Sprintf("🔕 今天已经打扰过 %d/%d 次", s.Used, s.Quota)
		if s.Held > 0 {
			l += fmt.Sprintf(", 攒着 %d 条没说", s.Held)
		}
		out = append(out, l)
	}
	if s.Pending > 0 {
		out = append(out, fmt.Sprintf("◉ 捞回上次没来得及总结的 %d 条信号", s.Pending))
	}
	return out
}

// RestoreAll 按正确的顺序恢复一切.
//
// prior 是按进程分好的账本(LoadEvents 的返回). 每一项都**全扫一遍**,
// 不假设状态在哪个桶里 —— 各个 Restore 只认自己那几种事件.
func RestoreAll(prior map[abi.ProcessID][]abi.Event, t RestoreTargets) RestoreSummary {
	var s RestoreSummary

	// ⓪⁻ 人先于设备: 设备上挂着主人的 id, 而报出来的时候要认得出那是谁
	if t.People != nil {
		for _, evs := range prior {
			t.People.Restore(evs)
		}
		s.People = len(t.People.List())
	}

	// ⓪ 设备先于一切: 后面每一段("这条信号谁报的""这话往哪儿说")
	// 都要认得出设备. 装不回来的话, 一台一天只报一次的设备
	// (体重秤、电表)在重启后能失踪一整天
	if t.Devices != nil {
		for _, evs := range prior {
			t.Devices.Restore(evs)
		}
		s.Devices = len(t.Devices.List())
	}

	// ① 地点先于关注: 关注里存的是地点名, 报错信息要认得出那个名字
	if t.Places != nil {
		s.SawPlacesTable = true
		for _, evs := range prior {
			t.Places.Restore(evs)
		}
		for _, pl := range t.Places.Known() {
			s.PlaceNames = append(s.PlaceNames, pl.Name)
		}
		s.Places = len(s.PlaceNames)
	}
	if t.Watches != nil {
		for _, evs := range prior {
			t.Watches.Restore(evs)
		}
		s.Watches = len(t.Watches.List())
	}

	// ② **预算必须先于闹钟**.
	//
	// Timers.Restore 末尾会走一遍 Tick, 把过期的闹钟**当场补响** ——
	// 而补响走的是通知那条路. 预算还没恢复的话, 那几条补响不占额度,
	// 于是"一天最多打扰三次"在每次重启时都被悄悄突破一点.
	if t.Budget != nil {
		for _, evs := range prior {
			t.Budget.Restore(evs)
		}
		s.Used, s.Quota, s.Held = t.Budget.Stats()
	}
	RestoreDaily(prior, t.Daily)
	if t.Timers != nil {
		for _, evs := range prior {
			t.Timers.Restore(evs)
		}
		s.Wakes = len(t.Timers.Pending())
	}

	// ②′ 此刻的世界 —— **在地点之后**: 位置事实要查地名, 而地名表
	// 刚在 ① 里装回来
	if t.World != nil {
		for _, evs := range prior {
			s.Facts += t.World.Restore(evs)
		}
	}

	// ③ 家里有没有人 —— **必须在捞积压之前**: 捞回来的信号会走一遍
	// 投递, 而那时候判据要的上下文得已经在
	if t.Presence != nil {
		for _, evs := range prior {
			t.Presence.Restore(evs)
		}
	}

	// ④ **静音表必须先于捞回积压**.
	//
	// 反过来的话, 上次静掉的那个噪音种类会被整批捞回来再总结一遍,
	// 而用户以为自己已经让它闭嘴了.
	if t.Bus != nil {
		for _, evs := range prior {
			t.Bus.RestoreMutes(evs)
		}
		s.Mutes = len(t.Bus.Mutes())
		// ⑤ 去重表: 采集端在我们重启这会儿重传是常态, 表空着的话
		// 那一批会被当成新的 —— 账本多一份、摘要多一遍
		for _, evs := range prior {
			s.SeenKeys += t.Bus.RestoreSeen(evs)
		}
		// ⑥ 最后捞积压 —— 它要用到上面所有的判断依据
		for _, evs := range prior {
			s.Pending += t.Bus.RecoverPending(evs)
		}
	}
	return s
}

// RestoreDaily 单独一项 —— 日报是在预算之后才建出来的(它要用预算),
// 于是它赶不上 RestoreAll 那一趟.
//
// **RestoreAll 只能调一次**: 预算的 Restore 是累加的(调用方是
// "每个桶喂一遍"的循环, 见 S26), 调两次今天就白白多用几次额度.
// 所以补这一项的办法是单独一个函数, 不是再调一遍 RestoreAll.
func RestoreDaily(prior map[abi.ProcessID][]abi.Event, d *DailyReport) {
	if d == nil {
		return
	}
	for _, evs := range prior {
		d.Restore(evs)
	}
}
