package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * 下载缓存必须**在工作区外面**, 而且**真的写得进去**.
 *
 *	当 HOME 被指进各自的工作区时, npm install 可能 3 分钟仍未返回而被掐掉:
 *	缓存一人一份, 永远是冷的. 见 sharedCache.
 */
func Test下载缓存共用一份而且写得进去(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NEOX_HOME", home)
	dir := sharedCache()
	if dir == "" {
		t.Fatal("共用缓存目录没建出来")
	}

	root := t.TempDir() // 假装这是某个人的工作区
	env := commandEnv(root)
	want := map[string]bool{"npm_config_cache": false, "PIP_CACHE_DIR": false, "GOMODCACHE": false}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, ok := want[k]; !ok {
			continue
		}
		want[k] = true
		// 指回工作区就等于没改 —— 那正是慢的原因
		if strings.HasPrefix(v, root) {
			t.Errorf("%s 还指在工作区里: %s", k, v)
		}
		if !strings.HasPrefix(v, dir) {
			t.Errorf("%s 没指到共用那份: %s", k, v)
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("%s 一个字都没说 —— 那家包管理器还是各下各的", k)
		}
	}

	// 沙箱得让它写. 光设环境变量而内核挡着的话, 报出来是一句
	// 莫名其妙的权限错, 而 bot 会退回冷下载.
	if !sandboxAvailable() {
		t.Skip("这台机器没有 sandbox-exec")
	}
	probe := filepath.Join(dir, "npm", "试写")
	if err := os.MkdirAll(filepath.Dir(probe), 0o755); err != nil {
		t.Fatal(err)
	}
	argv := sandboxWrap(root, []string{"/bin/sh", "-c", "echo 有 > " + probe})
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("沙箱把共用缓存的写挡掉了: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(probe); strings.TrimSpace(string(got)) != "有" {
		t.Errorf("写进去了但内容不对: %q", got)
	}
}
