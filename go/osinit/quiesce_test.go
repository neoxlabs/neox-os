package osinit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// **脚本靠 sleep 猜"窗口收完了", 猜早了最后几条就没进账本.**
//
// 而那跟"系统很安静"长得一模一样 —— 判决照样会说"通过", 只是它验的
// 是一份缺了尾巴的账本. 这跟 S50 那次(时间压缩把开门弄没了)是同一类:
// **证据少了一块, 而结论看起来没变.**
//
// 猜多少才够? 窗口 + 容忍度 + 补传的静默期 + 主动进程判断的时间 ——
// 每一项都可配, 而且最后一项取决于模型这次快不快. 猜不出来.
//
// 所以不猜: **等它自己安静下来** —— 账本连着 N 秒没长, 就是收完了.
func TestWaitQuietReturnsWhenLedgerStopsGrowing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e.jsonl")
	store, err := OpenEventStore(p)
	if err != nil {
		t.Fatal(err)
	}
	// 边写边等 —— 写完之后它才该返回
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			store.Append(abi.Event{Seq: i, PID: signalPID, At: int64(i),
				Kind: abi.EvSignal, Payload: map[string]any{"id": i}})
			time.Sleep(40 * time.Millisecond)
		}
		store.Close()
		close(done)
	}()

	start := time.Now()
	grew := WaitQuiet(p, 150*time.Millisecond, 5*time.Second)
	<-done
	if !grew {
		t.Fatal("账本明明长过, 却说它一直没动")
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("写还没写完就返回了 —— 最后几条会没进账本")
	}
	if n, _ := LoadEvents(p); len(n[signalPID]) != 5 {
		t.Fatalf("等完之后账本里只有 %d 条", len(n[signalPID]))
	}
}

// **超时要返回, 而且要说清它一直在长** ——
// 一个永远等下去的验收脚本比等早了更糟: 它会挂在 CI 里
func TestWaitQuietGivesUpAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e.jsonl")
	store, _ := OpenEventStore(p)
	defer store.Close()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				store.Append(abi.Event{PID: signalPID, Kind: abi.EvSignal,
					Payload: map[string]any{"id": 1}})
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	defer close(stop)

	start := time.Now()
	WaitQuiet(p, 500*time.Millisecond, 400*time.Millisecond)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("一直在长却等了 %s —— 验收会挂在 CI 里", d)
	}
}

// 账本还不存在时不该当成"已经安静了" —— 那说明进程还没起来
func TestWaitQuietWaitsForTheLedgerToAppear(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notyet.jsonl")
	go func() {
		time.Sleep(120 * time.Millisecond)
		os.WriteFile(p, []byte("{}\n"), 0o600)
	}()
	start := time.Now()
	WaitQuiet(p, 100*time.Millisecond, 3*time.Second)
	if time.Since(start) < 120*time.Millisecond {
		t.Fatal("账本还没出现就说安静了 —— 那时候进程根本还没起来")
	}
}
