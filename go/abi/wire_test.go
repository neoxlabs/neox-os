package abi

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestFrameRoundtrip(t *testing.T) {
	req := Request{ID: 7, Method: MEmit}
	req.Params, _ = json.Marshal(EmitParams{Payload: map[string]any{"中文": "值"}})

	frame, err := EncodeFrame(req)
	if err != nil {
		t.Fatal(err)
	}
	var d Decoder
	out, err := d.Push(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 frame, got %d", len(out))
	}
	var back Request
	if err := json.Unmarshal(out[0], &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != 7 || back.Method != MEmit {
		t.Fatalf("roundtrip mismatch: %+v", back)
	}
}

// 粘包: 一次收到多帧要全部解出
func TestDecoderCoalesced(t *testing.T) {
	a, _ := EncodeFrame(Request{ID: 1, Method: MEmit})
	b, _ := EncodeFrame(Request{ID: 2, Method: MCan})
	var d Decoder
	out, err := d.Push(append(a, b...))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
	var r Request
	_ = json.Unmarshal(out[1], &r)
	if r.ID != 2 {
		t.Fatalf("second frame id = %d", r.ID)
	}
}

// 半包: 分片到达要缓冲到完整才吐
func TestDecoderSplit(t *testing.T) {
	f, _ := EncodeFrame(Request{ID: 9, Method: MEmit, Params: json.RawMessage(
		`{"payload":"` + strings.Repeat("x", 500) + `"}`)})
	var d Decoder
	for _, cut := range [][2]int{{0, 3}, {3, 100}} {
		out, err := d.Push(f[cut[0]:cut[1]])
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Fatalf("不该在半包时吐出消息")
		}
	}
	out, err := d.Push(f[100:])
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if d.Pending() != 0 {
		t.Fatalf("缓冲没清干净: %d", d.Pending())
	}
}

// 坏长度直接报错, 不试图恢复 —— 恢复就是给攻击者留缝
func TestDecoderBadLength(t *testing.T) {
	bad := make([]byte, 8)
	binary.BigEndian.PutUint32(bad[:4], 0xffffffff)
	var d Decoder
	if _, err := d.Push(bad); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
}

func TestEncodeRejectsOversize(t *testing.T) {
	huge := strings.Repeat("x", MaxFrameBytes+10)
	if _, err := EncodeFrame(Request{ID: 1, Method: MEmit,
		Params: json.RawMessage(`"` + huge + `"`)}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
}

// ── 跨语言对拍 ──────────────────────────────────────────────
//
// 样本由 TypeScript 侧生成 (scratchpad/fixture.ts).
// 这是"两边只认同一份线格式"的唯一硬证据 —— 字段名对不上就是静默丢数据.

type fixture struct {
	Frames []string          `json:"frames"`
	Msgs   []json.RawMessage `json:"msgs"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/ts_frames.json")
	if err != nil {
		t.Fatalf("读样本失败 (先跑 scratchpad/fixture.ts 生成): %v", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Go 能解出 TS 编的每一帧, 且内容逐字节一致
func TestParityDecodeTSFrames(t *testing.T) {
	f := loadFixture(t)
	var d Decoder
	var all []byte
	for _, b64 := range f.Frames {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, raw...)
	}
	// 故意一次性全喂进去 —— 同时验证粘包
	out, err := d.Push(all)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(f.Msgs) {
		t.Fatalf("解出 %d 帧, TS 编了 %d 帧", len(out), len(f.Msgs))
	}
	for i, body := range out {
		if !jsonEqual(t, body, f.Msgs[i]) {
			t.Fatalf("第 %d 帧内容不一致\nGo 解出: %s\nTS 原文: %s", i, body, f.Msgs[i])
		}
	}
}

// TS 能解出 Go 编的帧 —— 反向由 TS 侧测试验证, 这里锁住 Go 编码的字节形状
func TestParityGoEncodingShape(t *testing.T) {
	f := loadFixture(t)
	for i, b64 := range f.Frames {
		tsFrame, _ := base64.StdEncoding.DecodeString(b64)
		// 用 TS 的原始消息重新编一遍, 长度前缀必须一致
		goFrame, err := EncodeFrame(json.RawMessage(f.Msgs[i]))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(goFrame[:4], tsFrame[:4]) {
			t.Fatalf("第 %d 帧长度前缀不一致: go=%v ts=%v", i, goFrame[:4], tsFrame[:4])
		}
	}
}

// 解析成具体类型也要对得上 —— 不只是 JSON 结构相同, 字段名要真的映射到位
func TestParityTypedFields(t *testing.T) {
	f := loadFixture(t)
	var d Decoder
	for _, b64 := range f.Frames {
		raw, _ := base64.StdEncoding.DecodeString(b64)
		out, err := d.Push(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range out {
			var probe struct {
				ID     int    `json:"id"`
				Method Method `json:"method"`
			}
			_ = json.Unmarshal(body, &probe)

			switch probe.Method {
			case MHello:
				var r Request
				mustUnmarshal(t, body, &r)
				var p HelloParams
				mustUnmarshal(t, r.Params, &p)
				if p.Token != "abc123" || p.Wire != WireVersion {
					t.Fatalf("hello 字段没映射到位: %+v", p)
				}
			case MCan:
				var r Request
				mustUnmarshal(t, body, &r)
				var p CanParams
				mustUnmarshal(t, r.Params, &p)
				if p.Axis != AxisRead || !strings.Contains(p.Scope, "文件") {
					t.Fatalf("can 字段没映射到位: %+v", p)
				}
			case MDecide:
				var r Request
				mustUnmarshal(t, body, &r)
				var p DecideParams
				mustUnmarshal(t, r.Params, &p)
				if p.Request.Present.Kind != "choice" ||
					len(p.Request.Present.Options) != 2 ||
					!p.Request.Present.Options[1].Destructive ||
					p.Request.OnTimeout == nil || p.Request.OnTimeout.Choose != "a" {
					t.Fatalf("decide 嵌套字段没映射到位: %+v", p.Request)
				}
			case "":
				// 回复帧
				var res Response
				mustUnmarshal(t, body, &res)
				if res.Result != nil && res.Result.Kind == "decision" {
					if res.Result.Resolution == nil ||
						res.Result.Resolution.By != "phone:刘" ||
						res.Result.Resolution.Values["note"] != "好" {
						t.Fatalf("decision 回复没映射到位: %+v", res.Result)
					}
				}
				if res.Error != nil && res.Error.Code != ErrUnauthenticated {
					t.Fatalf("错误码没映射到位: %+v", res.Error)
				}
			}
		}
	}
}

func mustUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, b)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}
