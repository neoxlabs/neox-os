package sense

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/neox-os/neox-os/abi"
)

// 百度地图 Web 服务的**一个**出口.
//
// ── 为什么收成一处 ──
//
//	原来逆地理、地理编码、地点检索、静态图、路线各写一遍: 各拼一次
//	URL、各 new 一个 http.Client、各翻译一次 240. 结果是翻译得不一样
//	(有的说"服务端", 有的只报状态码), 而地点检索那条干脆把地址写死了,
//	测试塞不进假服务器 —— **它是唯一一条未覆盖的, 也是容易
//	出问题的那一类**(拿名字找地方).
//
//	一处出口换来三件事: 报错翻译只有一份; 测试只要换一个 Host;
//	以后接新接口(路况、天气、公交)是加一个方法, 不是再抄一遍.
//
// ── 这把 key 能干什么 ──
//
//	2026-09-11 在服务器上逐个试过: 驾车(v2, 带路名/分段路况/红绿灯)、
//	骑行/步行/公交、道路和周边实时路况、天气(含逐小时和预警)、
//	地点检索和联想 —— 全部 status 0. **原来只用了轻量版驾车的一个数**,
//	于是实时路况被误报为不可见 —— key 从来都能.

const baiduHost = "https://api.map.baidu.com"

// Baidu 一把 key 一个客户端. 零值不可用, AK 空 = 没配.
type Baidu struct {
	AK string
	// HTTP 可换 —— 测试要塞一个假的
	HTTP *http.Client
	// Host 空 = 官方. 测试塞 httptest 的地址
	Host string
}

// Ready 配了没有.
func (b *Baidu) Ready() bool { return b != nil && strings.TrimSpace(b.AK) != "" }

// get 问一次, 解进 out. service 是给人看的服务名, 只用在报错里 ——
// 240 的真正原因是"建应用时没勾这一项", 说得出是哪一项他才改得对.
func (b *Baidu) get(ctx context.Context, path, service string, q url.Values, out any) error {
	if !b.Ready() {
		// **没配就说没配**, 不要去撞一个必然失败的请求: 那样错误信息
		// 会变成一句百度的状态码, 而真正的原因是这台机器压根没配 key
		return fmt.Errorf("没配地图 key(NEOX_BAIDU_AK): %w", abi.ErrNotReady)
	}
	q.Set("ak", b.AK)
	if q.Get("output") == "" {
		q.Set("output", "json")
	}
	host := b.Host
	if host == "" {
		host = baiduHost
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(host, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	cli := b.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("%s没连上: %w", service, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s回了 HTTP %d", service, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	var st struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("%s的响应解不开: %w", service, err)
	}
	if st.Status != 0 {
		msg := st.Message
		if msg == "" {
			msg = st.Msg
		}
		return baiduErr(service, st.Status, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s的响应解不开: %w", service, err)
	}
	return nil
}

// baiduErr 把状态码翻成"该去改什么".
//
//	状态码原话没有行动价值: 240 字面是"APP 服务被禁用", 但问题在建应用
//	那一步, 不是账号欠费. 模型拿到原话会把地图服务误报为被禁用,
//	然后退回去猜一个时间.
func baiduErr(service string, status int, msg string) error {
	switch {
	case status == 240 || status == 101:
		return fmt.Errorf("地图 key 用不了(%d): 建应用时类型要选「服务端」, 并勾上「%s」", status, service)
	case status == 210 || status == 211 || status == 102:
		return fmt.Errorf("地图 key 拒绝了这台机器(%d): key 绑了 IP 白名单或 SN 校验, 这台机器的出口 IP 不在里面", status)
	case status == 302 || status == 4 || (status >= 300 && status < 400):
		return fmt.Errorf("地图%s今天的配额用完了(%d) —— 明天恢复, 这之前别编一个数", service, status)
	case status == 401:
		return fmt.Errorf("地图接口并发超了(401) —— 过几秒再问一次")
	case status == 2:
		return fmt.Errorf("%s说参数不对: %s", service, msg)
	}
	return fmt.Errorf("%s说: %s(status %d)", service, msg, status)
}
