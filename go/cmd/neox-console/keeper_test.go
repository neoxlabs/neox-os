package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
	"github.com/neox-os/neox-os/osinit"
)

func testKeeper(t *testing.T) (*botKeeper, *osinit.OS, *int) {
	t.Helper()
	o := osinit.New(osinit.Options{Mode: abi.ModeDev})
	t.Cleanup(func() { o.Shutdown("test") })
	n := 0
	k := &botKeeper{
		os:     o,
		roster: &botRoster{path: filepath.Join(t.TempDir(), "bots.json")},
		spawn: func(p persona) (abi.ProcessID, error) {
			n++
			return o.Spawn(abi.ProcessSpec{App: p.app, Name: p.name},
				osinit.InprocBody{Entry: func(ctx context.Context, pc osinit.ProcessContext) (any, error) {
					_, _ = pc.Recv()
					return nil, nil
				}})
		},
	}
	return k, o, &n
}

func waitTerm(t *testing.T, o *osinit.OS, pid abi.ProcessID) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := o.Info(pid); ok && info.State.IsTerminal() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s 没停下来", pid)
}

func TestEnsure活着的不再起(t *testing.T) {
	k, _, n := testKeeper(t)
	first, err := k.ensure("研究")
	if err != nil || first == "" {
		t.Fatalf("第一次该起来: %v %q", err, first)
	}
	again, err := k.ensure("研究")
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("不该再起一个: %s vs %s", first, again)
	}
	if *n != 1 {
		t.Fatalf("起了 %d 次 —— 活人只要认出来", *n)
	}
}

func TestEnsure死了会再起(t *testing.T) {
	k, o, n := testKeeper(t)
	first, err := k.ensure("研究")
	if err != nil {
		t.Fatal(err)
	}
	o.Kill(first, "test")
	waitTerm(t, o, first)
	second, err := k.ensure("研究")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("还是那个死人 —— 话会送到一具尸体上")
	}
	if *n != 2 {
		t.Fatalf("该死后再起一次, 起了 %d 次", *n)
	}
}

func TestEnsure墓碑不起(t *testing.T) {
	k, _, n := testKeeper(t)
	if err := k.roster.remove(map[string]bool{"研究": true}); err != nil {
		t.Fatal(err)
	}
	_, err := k.ensure("研究")
	if !errors.Is(err, errBuried) {
		t.Fatalf("墓碑该拒绝, 得到 %v", err)
	}
	if *n != 0 {
		t.Fatalf("删掉的还起了 %d 次", *n)
	}
}

func TestFromDead认得出人(t *testing.T) {
	k, o, _ := testKeeper(t)
	dead, err := k.ensure("研究")
	if err != nil {
		t.Fatal(err)
	}
	o.Kill(dead, "test")
	waitTerm(t, o, dead)
	live, err := k.fromDead(dead)
	if err != nil {
		t.Fatal(err)
	}
	if live == "" || live == dead {
		t.Fatalf("该起个新人, 得到 %q (旧的是 %q)", live, dead)
	}
}

func TestWatch死后自己起来(t *testing.T) {
	k, o, _ := testKeeper(t)
	pid, err := k.ensure("研究")
	if err != nil {
		t.Fatal(err)
	}
	stop := k.watch()
	defer stop()
	o.Kill(pid, "test")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if live := k.live("研究"); live != "" && live != pid {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("死了没人拉 —— 这就是用户要重启客户端的原因")
}
