package boot

import (
	"strings"
	"sync"
	"testing"

	"github.com/neox-os/neox-os/abi"
)

func collect() (func(string), *[]string, *sync.Mutex) {
	var mu sync.Mutex
	var lines []string
	return func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() }, &lines, &mu
}

func badProbe([]abi.Requirement) abi.EnforcementProbe {
	return abi.EnforcementProbe{Platform: "linux", Usable: false,
		Missing: []abi.Requirement{abi.ReqFSEnforce},
		Reason:  "无法满足: fs-enforce"}
}
func okProbe([]abi.Requirement) abi.EnforcementProbe {
	return abi.EnforcementProbe{Platform: "linux", Usable: true,
		Mechanisms: []abi.Mechanism{abi.MechLandlock, abi.MechCgroup2, abi.MechNetns}}
}

// 半启动状态不存在: 自检不过就在建 OS 之前返回错误
func TestConfinedRefusesWhenKernelCannotEnforce(t *testing.T) {
	log, lines, mu := collect()
	res, err := Boot(Options{Mode: abi.ModeConfined, VolumeRoot: "/work", Log: log, Probe: badProbe})
	if err == nil {
		t.Fatal("内核拦不住时必须拒绝启动")
	}
	if res != nil {
		t.Fatal("拒绝启动时不该返回半个 OS")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(*lines, "\n"), "内核无法强制约束") {
		t.Fatalf("必须说清原因: %v", *lines)
	}
	// 不许出现"已就绪" —— 那意味着它在自检失败后还是把 OS 建起来了
	for _, l := range *lines {
		if strings.Contains(l, "OS 就绪") {
			t.Fatal("自检失败后不该建 OS")
		}
	}
}

func TestConfinedBootsWhenKernelCan(t *testing.T) {
	log, lines, mu := collect()
	res, err := Boot(Options{Mode: abi.ModeConfined, VolumeRoot: "/work", Log: log, Probe: okProbe})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Shutdown("t")
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(*lines, "\n"), "内核自检通过") {
		t.Fatalf("%v", *lines)
	}
}

// dev 模式能起来, 但必须大声说自己不受约束
func TestDevModeWarnsLoudly(t *testing.T) {
	log, lines, mu := collect()
	res, err := Boot(Options{Mode: abi.ModeDev, VolumeRoot: "/work", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Shutdown("t")
	if res.OS.Mode() != abi.ModeDev {
		t.Fatal("模式不对")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(*lines, "\n"), "不受内核约束") {
		t.Fatalf("dev 模式必须大声警告: %v", *lines)
	}
}

// 关机幂等 —— 连按两次 Ctrl-C 不该走两遍
func TestShutdownIsIdempotent(t *testing.T) {
	log, lines, mu := collect()
	res, err := Boot(Options{Mode: abi.ModeDev, VolumeRoot: "/work", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	res.Shutdown("first")
	res.Shutdown("second")
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, l := range *lines {
		if strings.Contains(l, "已关机") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("关机走了 %d 遍", n)
	}
}
