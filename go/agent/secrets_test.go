package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secretBox(t *testing.T) (Toolbox, string) {
	home := t.TempDir()
	key := filepath.Join(home, "provider.json")
	tok := filepath.Join(home, "token")
	os.WriteFile(key, []byte(`{"apiKey":"sk-secret"}`), 0o600)
	os.WriteFile(tok, []byte("tok"), 0o600)
	work := filepath.Join(home, "work", "bot")
	os.MkdirAll(work, 0o755)
	return Toolbox{Root: work, Secrets: []string{key, tok}}, home
}

// 线上 bot 的工作目录就在 ~/.neox-os/work 下, 同一个家目录里躺着 key
func TestFileToolsRefuseSecrets(t *testing.T) {
	box, home := secretBox(t)
	sys := newFakeSys()
	ag := &Agent{ABI: sys, Tools: NewToolSet(DefaultTools()), Box: box, MaxSteps: 5}
	for _, p := range []string{filepath.Join(home, "provider.json"), "../../token", "/proc/1/environ"} {
		obs := ag.callTool(Step{Tool: "read_file", Args: map[string]any{"path": p}})
		if !strings.Contains(obs.Err, "密钥") || strings.Contains(obs.Result, "sk-secret") {
			t.Errorf("%s 被读到了: %+v", p, obs)
		}
	}
	// 普通文件照读
	os.WriteFile(filepath.Join(box.Root, "a.txt"), []byte("hi"), 0o644)
	if obs := ag.callTool(Step{Tool: "read_file", Args: map[string]any{"path": "a.txt"}}); obs.Err != "" {
		t.Fatalf("普通文件被拦了: %v", obs.Err)
	}
}

func TestRunRefusesNamingSecrets(t *testing.T) {
	box, home := secretBox(t)
	ag := &Agent{ABI: newFakeSys(), Tools: NewToolSet(DefaultTools()), Box: box, MaxSteps: 5}
	for _, cmd := range []string{
		"cat " + filepath.Join(home, "provider.json"),
		"cd /root/.neox-os; cat provider.json",
		"cat ~/" + filepath.Base(home) + "/token",
		"tr '\\0' '\\n' < /proc/1/environ",
	} {
		obs := ag.callTool(Step{Tool: "run", Args: map[string]any{"cmd": cmd}})
		if !strings.Contains(obs.Err, "密钥") {
			t.Errorf("没拦: %s → %+v", cmd, obs)
		}
	}
	// 说到 token 这个词不算 —— 太常见了
	if box.secretInCmd("echo token counting") != "" {
		t.Fatal("误伤")
	}
}

// 搜"apiKey"不能把 provider.json 那一行带回来
func TestSearchSkipsSecrets(t *testing.T) {
	box, home := secretBox(t)
	ag := &Agent{ABI: newFakeSys(), Tools: NewToolSet(DefaultTools()), Box: box, MaxSteps: 5}
	os.WriteFile(filepath.Join(box.Root, "notes.md"), []byte("apiKey 在设置页改"), 0o644)
	obs := ag.callTool(Step{Tool: "search", Args: map[string]any{"pattern": "apiKey", "path": home}})
	if strings.Contains(obs.Result, "sk-secret") {
		t.Fatalf("搜出来了: %s", obs.Result)
	}
	if !strings.Contains(obs.Result, "notes.md") {
		t.Fatalf("搜索根本没跑起来, 这条测试什么都没证明: %+v", obs)
	}
}
