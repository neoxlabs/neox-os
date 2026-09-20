package agent

import (
	"encoding/json"
	"strings"

	"github.com/neox-os/neox-os/abi"
)

// 工具表 → 原生工具协议的声明.
//
// ── 为什么走原生 ──
//
// 自造 JSON 协议 (让模型输出 {"tool":..,"args":..}) 的结构合法性完全靠
// 模型自觉. 这种约束会让整步作废:
//
//	· 模型在 JSON 后面多输出一句中文 → "invalid character 'ç' after top-level value"
//	· 输出被截断成半个对象           → "unexpected end of JSON input"
//
// 原生协议由供应商侧保证结构合法, 还能省掉提示词里那一大段格式说明 ——
// 那段字节本身也占着前缀.
//
// ── 字节稳定是硬要求 ──
//
// 声明进的是**请求的前缀**, 跟系统段一样, 变一个字节所有缓存全废.
// 所以不能直接 json.Marshal 一个 map: Go 的 map 序列化顺序是随机的,
// 那会让缓存**每一轮都失效** —— 而且这种失效是静默的, 只能从
// 缓存命中率突然掉到 0 才看得出来.
//
// 这里按 ArgOrder 手工拼, 顺序完全由工具表决定.

// ToolDefs 把工具表翻译成声明. 顺序跟工具表一致 (已排过序).
func ToolDefs(ts *ToolSet) []abi.ToolDef {
	out := make([]abi.ToolDef, 0, len(ts.list))
	for _, t := range ts.list {
		out = append(out, abi.ToolDef{
			Name: t.Name, Desc: t.Desc, Params: paramSchema(t)})
	}
	return out
}

// paramSchema 生成参数的 JSON Schema.
//
// 所有参数都声明成 string, 刻意如此:
//
//	模型给数字还是字符串各家不一 (offset 常见两种都有), 而我们的
//	argInt 两种都收. 声明成 number 反而会让"给了字符串"变成一次
//	供应商侧的校验失败 —— 那是把一个我们本来能吸收的差异变成硬错误.
func paramSchema(t Tool) json.RawMessage {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i, k := range t.ArgOrder {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		db, _ := json.Marshal(t.Args[k])
		b.Write(kb)
		b.WriteString(`:{"type":"string","description":`)
		b.Write(db)
		b.WriteByte('}')
	}
	b.WriteString(`},"required":[`)
	first := true
	for _, k := range t.ArgOrder {
		if t.Optional[k] {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		b.Write(kb)
	}
	// additionalProperties:false 会让模型多给一个键就整个调用失败.
	// 不设它 —— 多给的键我们自己忽略就行, 没必要把一次可用的调用
	// 变成一次硬错误.
	b.WriteString(`]}`)
	return json.RawMessage(b.String())
}

// parseArgs 把供应商给的参数字符串解开.
//
// 解不开**不是模型的错也不该当成模型的错**: 走原生协议时结构由供应商
// 保证, 解不开说明是传输/适配层出了问题. 报错要说清这一点,
// 否则排查的人会去调提示词 —— 那个方向永远查不出来.
func parseArgs(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}
