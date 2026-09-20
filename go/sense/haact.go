package sense

// 反过来的那半边: **OS 吆喝一声, 家里这台去做**.
//
// ── 为什么控制不由 OS 直接调 HA ──
//
//	HA 在他家路由器后面, OS 在机房. 要 OS 直接调, 就得把 HA 暴露到公网
//	或者打一条隧道进他家局域网 —— 为了开一盏灯, 把整个家开一道口子.
//
//	而采集端本来就在家里、本来就连着 OS(它一直在投信号). 让它顺便听一句
//	吆喝, 收到就在本地调 HA: **家里只有出站连接, 一个端口都不用开.**
//
// ── 这一侧是第二道闸 ──
//
//	采集端对信号做降采样(见 ha.go 开头), 对动作做白名单. 两件事同一个
//	道理: **闸要装在离外面最近的那一层**. 模型那边判错一次是难免的,
//	而"开灯"判错和"开锁"判错不是一回事 —— 后者这一侧根本不该放行.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// actAllowed 允许 OS 吆喝的服务域.
//
//	**锁和安防不在里面, 而且不是忘了**: 开灯错了再关掉, 开锁错了是另一
//	回事 —— 那种事该由他自己在 HA 上按, 不该由一个会判错的东西代劳.
//	要放开也得是他明确改这张表, 而不是模型某天想通了.
var actAllowed = map[string]bool{
	"light": true, "switch": true, "scene": true, "script": true,
	"cover": true, "climate": true, "fan": true, "media_player": true,
	"input_boolean": true, "humidifier": true, "vacuum": true,
}

// ActService 一次动作要调的服务和目标 —— **域是这一侧定的**.
//
//	OS 那边只说"把客厅灯关掉": 它不知道这台 HA 上有哪些实体, 也不该知道
//	(HA 在他家路由器后面, 机房那台够不着 /api/states). 名字到实体、实体到
//	域、域到白名单, 全在离 HA 最近的这一层做完.
type ActService struct {
	Domain  string // light
	Service string // turn_on
	Entity  string // light.living_room
}

// actWords OS 那边说的动作 → HA 的服务名. **只有这几个**:
// 多一个就是多一类"它可能替我做"的事, 而这张表是他唯一能一眼看完的闸
var actWords = map[string]string{
	"on": "turn_on", "off": "turn_off", "toggle": "toggle",
	"开": "turn_on", "关": "turn_off",
}

// resolveAct 把一句吆喝落到一个具体实体上.
//
//	states 是这一侧刚拉回来的那一份 —— 名字匹配必须拿**当下**的实体表做,
//	OS 那边存一份过期的清单只会指向一个已经改名的灯.
//
//	**匹配不唯一就不做**: 家里两盏灯都叫"灯"的时候, 挑一个执行比什么都
//	不做糟得多 —— 他要过好一会儿才发现开的是另一间屋.
func resolveAct(p map[string]any, states []HAState) (ActService, error) {
	word := strings.TrimSpace(fmt.Sprint(p["act"]))
	if p["act"] == nil || word == "" {
		return ActService{}, fmt.Errorf("没说要做什么")
	}
	svc, ok := actWords[strings.ToLower(word)]
	if !ok {
		return ActService{}, fmt.Errorf("不认得「%s」这个动作, 只有开/关/切换", word)
	}
	ent := strings.TrimSpace(fmt.Sprint(p["entity"]))
	if p["entity"] == nil {
		ent = ""
	}
	if ent == "" {
		name := strings.TrimSpace(fmt.Sprint(p["name"]))
		if p["name"] == nil || name == "" {
			return ActService{}, fmt.Errorf("没说对哪个东西")
		}
		hits := matchEntities(name, states)
		switch len(hits) {
		case 0:
			return ActService{}, fmt.Errorf("家里没有叫「%s」的东西", name)
		case 1:
			ent = hits[0]
		default:
			return ActService{}, fmt.Errorf("叫「%s」的有 %d 个(%s) —— 说准一点",
				name, len(hits), strings.Join(hits, "、"))
		}
	}
	dom, _, ok := strings.Cut(ent, ".")
	if !ok || dom == "" {
		return ActService{}, fmt.Errorf("实体 id 不对: %q", ent)
	}
	if !actAllowed[dom] {
		return ActService{}, fmt.Errorf("「%s」这一类不开放给它调 —— 锁、安防这些要你自己按", dom)
	}
	return ActService{Domain: dom, Service: svc, Entity: ent}, nil
}

// matchEntities 按名字找实体: 先认实体 id, 再认 friendly_name 全等,
// 最后才认包含 —— **宽的那一档只在严的那几档都没中时才用**
func matchEntities(name string, states []HAState) []string {
	name = strings.TrimSpace(name)
	var exact, loose []string
	for _, st := range states {
		if st.EntityID == name {
			return []string{st.EntityID}
		}
		fn := strings.TrimSpace(attrString(st.Attributes, "friendly_name"))
		if fn == "" {
			continue
		}
		switch {
		case fn == name:
			exact = append(exact, st.EntityID)
		case strings.Contains(fn, name) || strings.Contains(name, fn):
			loose = append(loose, st.EntityID)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return loose
}

// Do 真去调一次 HA 的服务.
func (cfg HAConfig) Do(ctx context.Context, cli *http.Client, a ActService) error {
	body, _ := json.Marshal(map[string]any{"entity_id": a.Entity})
	url := strings.TrimRight(cfg.BaseURL, "/") + "/api/services/" + a.Domain + "/" + a.Service
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	if cli == nil {
		cli = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HA 回了 HTTP %d", resp.StatusCode)
	}
	return nil
}

// Listen 连上 OS 的事件流, 把冲着"ha"来的吆喝一条条交给 do.
//
//	**只读流, 不回话**: 做成了没做成由下一轮的状态比对说话(灯亮了就会有
//	一条 light.on 的信号) —— 那条才是事实, 见 abi.EvDeviceAct.
//
//	断了就退避重连. 一条安静的流会被路上每一跳掐掉, 而 OS 每 20 秒发一行
//	注释顶着(见 observe.go 那段心跳) —— 所以这边只要老实按行读就行.
func Listen(ctx context.Context, sink Sink, states func() []HAState,
	do func(ActService) error, log func(string, ...any)) {
	if log == nil {
		log = func(string, ...any) {}
	}
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		n, err := listenOnce(ctx, sink, states, do, log)
		if n > 0 {
			backoff = 2 * time.Second // 真收到过东西就重新从头退避
		}
		if err != nil && ctx.Err() == nil {
			log("事件流断了(%v), %s 后重连", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func listenOnce(ctx context.Context, sink Sink, states func() []HAState,
	do func(ActService) error, log func(string, ...any)) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(sink.URL, "/")+"/stream", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+sink.Token)
	req.Header.Set("Accept", "text/event-stream")
	cli := sink.HTTP
	if cli == nil {
		// **不设总超时**: 这条流本来就要一直开着
		cli = &http.Client{}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("事件流回了 HTTP %d", resp.StatusCode)
	}
	got := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "" || strings.HasPrefix(line, ":"):
			// 空行是分隔, 冒号开头是注释(live / beat) —— 按 SSE 规范忽略.
			// 它们唯一的作用是让这条线上一直有字节在走
			continue
		case strings.HasPrefix(line, "event: gap"):
			// OS 说这儿断过一段. 重连即可 —— 动作是瞬时的, 补不回来也不该补:
			// 十分钟前那句"开灯"现在执行才是真的吓人
			return got, fmt.Errorf("流中间断过(slow-consumer)")
		case !strings.HasPrefix(line, "data: "):
			continue
		}
		var ev abi.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			continue // 画不出来的一行不该拖垮整条流
		}
		if ev.Kind != abi.EvDeviceAct {
			continue
		}
		p, _ := ev.Payload.(map[string]any)
		if p == nil || strings.TrimSpace(fmt.Sprint(p["device"])) != "ha" {
			continue
		}
		got++
		var known []HAState
		if states != nil {
			known = states()
		}
		act, err := resolveAct(p, known)
		if err != nil {
			log("不做这条: %v", err)
			continue
		}
		if err := do(act); err != nil {
			log("%s %s 没做成: %v", act.Service, act.Entity, err)
			continue
		}
		log("做了: %s %s", act.Service, act.Entity)
	}
	return got, sc.Err()
}
