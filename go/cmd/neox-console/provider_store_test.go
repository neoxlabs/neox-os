package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neox-os/neox-os/osinit"
)

func readRaw(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到配置: %v", err)
	}
	return string(raw)
}

// 环境变量里的 key 绝不落盘.
//
// 这条最早是被"在设置里改了下权限档"暴露的: 那条路径 load 完直接 save,
// 而 load 会拿环境变量当种子 —— 于是一次无关的设置改动就把 key 抄到了磁盘上.
func TestEnvKeyNeverHitsDisk(t *testing.T) {
	dir := t.TempDir()
	secret := "sk-env-only-do-not-persist"
	s := &providerStore{path: filepath.Join(dir, "provider.json"), envKey: secret}

	cfg := s.load()
	if cfg.APIKey != secret {
		t.Fatalf("环境变量那把没被当成种子用上: %q", cfg.APIKey)
	}
	// 模拟"只改了权限档"那条路径
	cfg.Ask = "never"
	if err := s.save(cfg); err != nil {
		t.Fatalf("存不下: %v", err)
	}
	if strings.Contains(readRaw(t, s.path), secret) {
		t.Fatal("环境变量里的 key 被写进了文件")
	}
	// 存完再读, 这次运行仍然要有 key —— 不能为了不落盘把服务弄没
	if again := s.load(); again.APIKey != secret || !again.HasKey {
		t.Errorf("落盘后这次运行的 key 丢了: hasKey=%v", again.HasKey)
	}
}

// 用户在界面上亲手填的那把要存 —— 否则下次启动就没有推理服务了
func TestTypedKeyIsPersisted(t *testing.T) {
	dir := t.TempDir()
	s := &providerStore{path: filepath.Join(dir, "provider.json"), envKey: "sk-env"}
	typed := "sk-typed-by-user"
	if err := s.save(osinit.ProviderConfig{
		BaseURL: "https://api.deepseek.com", Model: "m", APIKey: typed,
	}); err != nil {
		t.Fatalf("存不下: %v", err)
	}
	if !strings.Contains(readRaw(t, s.path), typed) {
		t.Fatal("用户亲手填的 key 没存下来")
	}
	// 存着的优先于环境变量
	if got := s.load().APIKey; got != typed {
		t.Errorf("读回来的是 %q, 要用户填的那把", got)
	}
}

// 审批档必须活过重启.
//
// 这个档只对**新起的 bot** 生效, 所以"读不回来"的症状是:
// 改完当场没反应(本来就该没反应), 重启之后还是没反应 ——
// 而界面上它一直显示着你选的那一档。看起来全对, 实际全错。
func TestAskModeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.json")
	first := &providerStore{path: path}
	if err := first.save(osinit.ProviderConfig{APIKey: "sk-x", Ask: string(osinit.AskNever)}); err != nil {
		t.Fatalf("存不下: %v", err)
	}
	// 重启 = 换一个 store 从盘上读
	again := (&providerStore{path: path}).load()
	if again.AskModeOf() != osinit.AskNever {
		t.Errorf("重启后审批档变回了 %q —— 用户选的那一档没读回来", again.AskModeOf())
	}
}

// 落盘的文件不许放宽权限
func TestStoredConfigIsPrivate(t *testing.T) {
	dir := t.TempDir()
	s := &providerStore{path: filepath.Join(dir, "provider.json")}
	if err := s.save(osinit.ProviderConfig{APIKey: "sk-x"}); err != nil {
		t.Fatalf("存不下: %v", err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("配置文件是 %v, 要 0600", info.Mode().Perm())
	}
	var back osinit.ProviderConfig
	if err := json.Unmarshal([]byte(readRaw(t, s.path)), &back); err != nil {
		t.Errorf("存下去的解不开: %v", err)
	}
}

// **这把 key 是哪来的, 界面必须知道**.
//
// 环境变量那把从来不落盘 —— 而设置页据 HasKey 写着"已经存着一把".
// 那句话在这种机器上是假的: 关掉再开, key 就没了, 而用户以为它存着.
func TestKeyFromSaysWhereItLives(t *testing.T) {
	dir := t.TempDir()
	env := &providerStore{path: filepath.Join(dir, "p.json"), envKey: "sk-来自环境变量"}
	got := env.load()
	if got.KeyFrom != "env" {
		t.Fatalf("环境变量那把没标出来(得到 %q) —— 界面会说它存着, 而它一关就没", got.KeyFrom)
	}

	// 用户在界面上亲手填的那把才是真的存了盘
	typed := &providerStore{path: filepath.Join(dir, "q.json")}
	if err := typed.save(osinit.ProviderConfig{BaseURL: "https://x", Model: "m", APIKey: "sk-手填的"}); err != nil {
		t.Fatal(err)
	}
	if again := typed.load(); again.KeyFrom != "file" {
		t.Fatalf("手填并存了盘的那把标成了 %q", again.KeyFrom)
	}

	// 一把都没有的时候不能说"存着"
	none := &providerStore{path: filepath.Join(dir, "r.json")}
	if got := none.load(); got.KeyFrom != "" || got.HasKey {
		t.Fatalf("没有 key 却说有: %+v", got)
	}
}

// 每一格都得**写得进也读得回**.
//
//	写得进读不回比根本没这个设置更坏: 设置页上显示的还是用户选的那个,
//	而 OS 里跑的是缺省档 —— 看起来是好的。这个文件里的审批档和"认不认图"
//	都各自栽过一次, 所以这条改成一张**总表**: 新加一格忘了读回来, 这里红。
func TestProviderStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := &providerStore{path: filepath.Join(dir, "provider.json")}
	want := osinit.ProviderConfig{
		BaseURL: "https://gw.example.com/n1", Model: "m", APIKey: "k",
		Protocol: "openai", Ask: "always", Vision: true, Think: true,
		SearchAPI: "bocha", SearchURL: "https://s.example.com",
		SearchKey: "sk", CapTokens: 4242, AutoResume: true,
	}
	if err := store.save(want); err != nil {
		t.Fatal(err)
	}
	got := store.load()
	for _, c := range []struct {
		what      string
		got, want any
	}{
		{"地址", got.BaseURL, want.BaseURL},
		{"模型", got.Model, want.Model},
		// 协议读不回的后果最重: 用户把一个认不出来的自建网关改成
		// OpenAI 兼容, 重启之后又被按域名认回去 —— 整台机器不能推理
		{"协议", got.Protocol, want.Protocol},
		{"审批档", got.Ask, want.Ask},
		{"认不认图", got.Vision, want.Vision},
		{"想不想", got.Think, want.Think},
		{"搜索那家", got.SearchAPI, want.SearchAPI},
		{"搜索地址", got.SearchURL, want.SearchURL},
		{"搜索 key", got.SearchKey, want.SearchKey},
		{"每轮上限", got.CapTokens, want.CapTokens},
		{"自己接着干", got.AutoResume, want.AutoResume},
	} {
		if c.got != c.want {
			t.Errorf("%s 写得进读不回: %v ≠ %v", c.what, c.got, c.want)
		}
	}
}
