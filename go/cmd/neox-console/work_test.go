package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func home(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		t.Skip("没有家目录")
	}
	return h
}

// 派项目就是派工作区 —— 正常的路径要原样通过, ~ 要展开
func TestResolveWorkTakesProjectDirs(t *testing.T) {
	h := home(t)
	got, err := resolveWork("~/AI/记事本")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(h, "AI", "记事本") {
		t.Fatalf("~ 没展开: %q", got)
	}
	if got, err = resolveWork("  /tmp/proj/  "); err != nil || got != "/tmp/proj" {
		t.Fatalf("绝对路径没通过: %q %v", got, err)
	}
	// 空 = 不指定, 交给系统分匿名目录, 不是错
	if got, err = resolveWork(""); err != nil || got != "" {
		t.Fatalf("空应该是不指定: %q %v", got, err)
	}
}

// 这条路径会原样变成内核给它的 write 能力, 所以边界必须窄.
func TestResolveWorkRefusesTooMuch(t *testing.T) {
	h := home(t)
	for _, bad := range []string{"/", h, "~", h + "/", "./proj", "proj"} {
		if got, err := resolveWork(bad); err == nil {
			t.Errorf("%q 被放行了, 解析成 %q —— 那等于把这台机器交出去", bad, got)
		}
	}
}

// ~/.neox-os 底下放着 API key 和全部历史, bot 不能写它.
//
// 只有 work/ 是例外 —— 那本来就是分给 bot 的地方.
func TestResolveWorkProtectsTheLedgerAndKey(t *testing.T) {
	h := home(t)
	for _, bad := range []string{
		filepath.Join(h, ".neox-os"),
		filepath.Join(h, ".neox-os", "provider.json"),
		filepath.Join(h, ".neox-os", "events.jsonl"),
	} {
		if _, err := resolveWork(bad); err == nil {
			t.Errorf("%q 被放行了 —— 它能拿到密钥、能改自己的账本", bad)
		} else if !strings.Contains(err.Error(), "账本") {
			t.Errorf("%q 的拒绝理由说不清: %v", bad, err)
		}
	}
	if _, err := resolveWork(filepath.Join(h, ".neox-os", "work", "abc")); err != nil {
		t.Errorf("work/ 底下是分给 bot 的, 不该拒: %v", err)
	}
	// 按路径段比, 不是字符串前缀: 这个目录跟 .neox-os 没关系
	if _, err := resolveWork(filepath.Join(h, ".neox-os-backup")); err != nil {
		t.Errorf("%s 不在 .neox-os 里面, 被误伤了: %v", ".neox-os-backup", err)
	}
}

// 状态根要能挪 —— 这是"起第二个实例而不碰第一个的数据"的唯一前提.
//
// 挪不动的话, 任何一次真机验证都得先让用户关掉他正开着的 console,
// 而两个写者同时写同一份 append-only 账本, 坏了是不可逆的.
func TestNeoxHomeMoves(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("NEOX_HOME", tmp)
	if got := neoxHome(); got != tmp {
		t.Fatalf("NEOX_HOME 没生效: %q", got)
	}
	// 四个落盘点必须**一起**跟着挪 —— 挪一半比不挪更糟:
	// 账本在新家、名册在旧家, 于是测试实例照样会起用户的 bot
	if got := ledgerPath(); !strings.HasPrefix(got, tmp) {
		t.Errorf("账本没跟着挪: %q", got)
	}
	if got := newBotRoster().path; !strings.HasPrefix(got, tmp) {
		t.Errorf("名册没跟着挪: %q", got)
	}
	if got := newProviderStore().path; !strings.HasPrefix(got, tmp) {
		t.Errorf("供应商配置没跟着挪: %q", got)
	}
	root, err := newWorkPlanner(nil).plan(persona{name: "x", app: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(root.Dir, realPath(tmp)) {
		t.Errorf("匿名工作区没跟着挪: %q", root.Dir)
	}
}

// 用户把 bot 派到哪个工作区, **必须活过重启** —— 内置的那几个也一样.
//
// 不记的话下次开机它回到系统分的匿名目录, 而用户以为它还守着那个项目:
// 界面上一切正常, 它却在别处干活, 上次的成果一个字都看不见.
func TestWorkSurvivesRestartEvenForBuiltins(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	roster := newBotRoster()
	if err := roster.setWork("发版", "/Users/x/AI/release", true); err != nil {
		t.Fatal(err)
	}
	// 重新读一遍 = 模拟下一次开机
	if got := newBotRoster().works()["发版"]; got != "/Users/x/AI/release" {
		t.Fatalf("重启之后工作区丢了: %q", got)
	}
	// 改一次要覆盖, 不是追加出第二条
	if err := roster.setWork("发版", "/Users/x/AI/别处", true); err != nil {
		t.Fatal(err)
	}
	if n := len(newBotRoster().load()); n != 1 {
		t.Fatalf("同一个 bot 记了 %d 条 —— 会起出两个同名分身", n)
	}
	if got := newBotRoster().works()["发版"]; got != "/Users/x/AI/别处" {
		t.Fatalf("改了没生效: %q", got)
	}
}

// 换房间和换工作区**不能互相冲掉** —— 它们是同一条记录上的两件事.
func TestRoomAndWorkDoNotClobberEachOther(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	roster := newBotRoster()
	if err := roster.setWork("回归", "/Users/x/AI/qa", true); err != nil {
		t.Fatal(err)
	}
	if err := roster.setRoom("回归", "#这次发布", true); err != nil {
		t.Fatal(err)
	}
	if got := newBotRoster().works()["回归"]; got != "/Users/x/AI/qa" {
		t.Fatalf("换房间把工作区冲掉了: %q", got)
	}
	if got := newBotRoster().rooms()["回归"]; got != "#这次发布" {
		t.Fatalf("房间没记住: %q", got)
	}
}

// 内置的和界面上建的必须分得开.
//
// 拿名册那条去起内置 bot 会起出一个 bot 标签完全不同的同名分身 ——
// 界面上就是同一个人出现两次, 而且两个都活着、各说各的.
func TestFindPersonaTellsBuiltinApart(t *testing.T) {
	t.Setenv("NEOX_HOME", t.TempDir())
	roster := newBotRoster()
	if _, builtin, err := findPersona(roster, "发版"); err != nil || !builtin {
		t.Fatalf("内置的没认出来: builtin=%v err=%v", builtin, err)
	}
	if err := roster.setWork("我建的", "/Users/x/AI/mine", false); err != nil {
		t.Fatal(err)
	}
	p, builtin, err := findPersona(roster, "我建的")
	if err != nil || builtin {
		t.Fatalf("界面建的被当成内置: builtin=%v err=%v", builtin, err)
	}
	if p.work != "/Users/x/AI/mine" {
		t.Fatalf("起回来的身份里没带工作区: %q", p.work)
	}
	if _, _, err := findPersona(roster, "查无此人"); err == nil {
		t.Error("不存在的 bot 也认了")
	}
}

// 岗位说明压成一句给标签用 —— **按字符截, 不按字节**.
//
// 标签跟着每条创建事件进账本, 完整的岗位说明能有几百字, 那是每次开机
// 都要落一遍的钱; 而界面上只需要一句话.
func TestRoleLabelIsOneShortLine(t *testing.T) {
	got := roleLabel("负责登录与用户体系（P0）：\n用户表、注册/登录/登出、\nsession 鉴权")
	if strings.Contains(got, "\n") {
		t.Fatalf("换行没压掉: %q", got)
	}
	long := roleLabel(strings.Repeat("负责考勤", 40))
	if len([]rune(long)) > 41 {
		t.Fatalf("没截住: %d 个字", len([]rune(long)))
	}
	// 按字节截会切出半个汉字 —— 界面上就是一个乱码方块
	if strings.Contains(long, "�") {
		t.Fatal("截出了半个字")
	}
	if roleLabel("  ") != "" {
		t.Error("空的岗位不该变成一个空标签")
	}
}
