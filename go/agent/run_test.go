package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runIn(t *testing.T, dir string, ctx context.Context, cmd string) (string, error) {
	t.Helper()
	tool := runTool()
	return tool.Run(Toolbox{Root: dir, Ctx: ctx}, map[string]any{"cmd": cmd})
}

func TestRunReportsExitCodeFirst(t *testing.T) {
	dir := t.TempDir()
	out, err := runIn(t, dir, context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("成功的命令不该返回 err: %v", err)
	}
	if !strings.HasPrefix(out, "[退出码 0") {
		t.Fatalf("退出码必须在第一行, got: %q", out)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("stdout 丢了: %q", out)
	}
}

// 非零退出**不是工具错误, 是结果**.
//
// 判错方向的代价很实: 变成 err 的话 Observation 只有 Err 没有 Result,
// 于是一次失败的测试**连输出一起丢了** —— 而那份输出正是 agent
// 修 bug 唯一的依据, 它只能瞎猜着再跑一遍.
func TestRunFailureKeepsOutput(t *testing.T) {
	dir := t.TempDir()
	out, err := runIn(t, dir, context.Background(),
		"echo 编译错误在这里 >&2; exit 3")
	if err != nil {
		t.Fatalf("非零退出不该变成 err (那会丢掉输出): %v", err)
	}
	if !strings.Contains(out, "退出码 3") {
		t.Fatalf("退出码没报出来, 模型会把失败当成功: %q", out)
	}
	if !strings.Contains(out, "编译错误在这里") {
		t.Fatalf("stderr 丢了 —— 报错几乎都在 stderr: %q", out)
	}
	if !strings.Contains(out, "失败") {
		t.Fatalf("非零退出没有明说是失败: %q", out)
	}
}

// 超时必须**真杀掉**, 而且是整个进程组.
//
// 只杀 /bin/sh 的话, 真正干活的孙子进程会变成孤儿继续跑 ——
// 占着 cgroup 的配额而没人再看它. "看起来停了其实没停"
// 的现象会出现在别处 (下一条命令莫名其妙内存不够), 极难查.
func TestRunTimeoutKillsWholeGroup(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/still-alive"
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	// sh 起一个后台孙子: 2 秒后写文件. 组杀生效的话它永远写不出来
	_, err := runIn(t, dir, ctx,
		"(sleep 2; echo x > "+marker+") & wait")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("超时了却没报错")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("等到命令自己结束才返回 (%v) —— 没有真杀", elapsed)
	}
	// 给孙子进程留够时间: 如果它没被杀, 2 秒后就会写出文件
	time.Sleep(2500 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("孙子进程活下来了 —— 只杀了 sh, 没杀进程组")
	}
}

// 太长的输出要留头也留尾.
//
// 只留前面是最糟的砍法: 编译错误确实在开头, 但**测试失败的摘要在结尾**
// (go test 的 FAIL 行、npm 的 error summary). 砍掉尾巴等于让模型
// 看着一屏 PASS 宣布测试通过了.
func TestRunKeepsBothEnds(t *testing.T) {
	head := "开头的编译错误"
	tail := "FAIL 结尾的失败摘要"
	body := head + "\n" + strings.Repeat("噪音噪音噪音噪音\n", 4000) + tail
	got := clampMiddle(body, runMaxBytes)

	if len(got) > runMaxBytes+400 {
		t.Fatalf("没截断, %d 字节", len(got))
	}
	if !strings.Contains(got, head) {
		t.Fatal("开头被砍了 —— 编译错误看不到了")
	}
	if !strings.Contains(got, tail) {
		t.Fatal("结尾被砍了 —— 测试失败摘要看不到了, 模型会把红的当成绿的")
	}
	if !strings.Contains(got, "省略") {
		t.Fatal("静默截断: 模型会以为自己看到了全部输出")
	}
}

// 不给 stdin —— 交互式命令要立刻拿 EOF 退出, 而不是挂到超时
func TestRunHasNoStdin(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	out, err := runIn(t, dir, ctx, "cat")
	if err != nil {
		t.Fatalf("cat 应该立刻 EOF 退出, 结果超时了: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("挂住了 %v —— 说明它在等一个永远不会来的输入", time.Since(start))
	}
	_ = out
}

// 环境不继承宿主.
//
// 跟 OS spawn 进程时同一条规矩: 继承来的东西摘不掉, 临时约束会变成
// 永久约束. agent 进程里本来就不该有密钥, 但"明着只列这几个"
// 比"碰巧没有"可靠.
func TestRunEnvIsExplicitNotInherited(t *testing.T) {
	t.Setenv("NEOX_API_KEY", "sk-绝不能漏出去")
	dir := t.TempDir()
	out, err := runIn(t, dir, context.Background(), "env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sk-绝不能漏出去") {
		t.Fatal("宿主环境被子进程继承了 —— 密钥漏进了命令的环境")
	}
	if !strings.Contains(out, "PATH=") {
		t.Fatal("没有 PATH, 什么命令都找不到")
	}
}

// OS 发下来的出网配置**必须**传给子命令.
//
// OS 建好 netns、起好代理、把 http_proxy 写进进程环境后, run 也必须把
// 这份配置传给 curl; 否则 curl 会自己做 DNS, 而 netns 里没有 DNS,
// 报 "Could not resolve host". 授权、plan.net、netns 每一环都可能正确,
// 但少传环境变量仍会在最后一步断掉.
func TestRunPassesProxyConfigThrough(t *testing.T) {
	t.Setenv("http_proxy", "http://10.77.0.1:3128")
	t.Setenv("HTTPS_PROXY", "http://10.77.0.1:3128")
	t.Setenv("npm_config_proxy", "http://10.77.0.1:3128")
	dir := t.TempDir()
	out, err := runIn(t, dir, context.Background(), "env")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"http_proxy", "HTTPS_PROXY", "npm_config_proxy"} {
		if !strings.Contains(out, k+"=http://10.77.0.1:3128") {
			t.Errorf("%s 没传给子命令 —— 它会绕过代理自己做 DNS, 然后报解析失败", k)
		}
	}
}

// 但凭据不许传: NEOX_ABI_TOKEN 是发给 **agent** 的, 不是发给它随手起的
// 任何一条命令. 一条 `run curl` 不该有能力冒充 agent 去调 OS.
func TestRunDoesNotLeakAbiCredentials(t *testing.T) {
	t.Setenv("NEOX_ABI_TOKEN", "绝密令牌")
	t.Setenv("NEOX_ABI_SOCKET", "/run/neox-os/p1.sock")
	dir := t.TempDir()
	out, err := runIn(t, dir, context.Background(), "env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "绝密令牌") || strings.Contains(out, "NEOX_ABI_SOCKET") {
		t.Fatal("ABI 凭据漏给子命令了 —— 任何一条命令都能冒充 agent 调 OS")
	}
}

func TestRunRejectsEmptyCommand(t *testing.T) {
	dir := t.TempDir()
	if _, err := runIn(t, dir, context.Background(), "   "); err == nil {
		t.Fatal("空命令应该当场拒绝, 而不是去跑一个空的 sh")
	}
}

func TestRunBadDirIsActionable(t *testing.T) {
	dir := t.TempDir()
	tool := runTool()
	_, err := tool.Run(Toolbox{Root: dir, Ctx: context.Background()},
		map[string]any{"cmd": "pwd", "dir": "不存在的目录"})
	if err == nil {
		t.Fatal("目录不存在却没报错")
	}
	// 错误要告诉模型下一步干什么, 不是一句 ENOENT
	if !strings.Contains(err.Error(), "list_dir") {
		t.Fatalf("报错没给出下一步该怎么办: %v", err)
	}
}

// 起过的进程组要**报给宿主**, 否则"后台进程活不过这段对话"就是句空话.
//
// ── 这句话原来在 console 上是假的 ──
//
// 提示词里对模型许过: "你起的后台进程活不过这段对话 —— nohup、&、setsid
// 都拦不住, 那是命名空间的语义". 真内核那条路上确实如此; 而 console 是
// in-proc 的, **没有 pid 命名空间**.
//
// 一个验证服务如果占着 8123 端口不退出, 下一次验证就会遇到"端口已被占用";
// 这个错误与后台进程未被报告的真正原因隔着好几层.
func TestRunReportsWhatItStarted(t *testing.T) {
	var started []int
	box := Toolbox{Root: t.TempDir(), Started: func(pgid int) { started = append(started, pgid) }}
	tool := runTool()
	if _, err := tool.Run(box, map[string]any{"cmd": "echo hi"}); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0] <= 0 {
		t.Fatalf("没把起过的进程组报出去: %v —— 宿主就无从收拾它留下的后台进程", started)
	}
}

// 没有宿主要这份名单时不能崩 —— 测试和别的宿主自己管
func TestRunWorksWithoutATracker(t *testing.T) {
	if _, err := runTool().Run(Toolbox{Root: t.TempDir()}, map[string]any{"cmd": "echo hi"}); err != nil {
		t.Fatal(err)
	}
}

// **沙箱要真的关得住** —— 尤其是 shell 自己的重定向那条路.
//
// shell 的重定向会绕过工具层的路径检查: 一条「echo x 重定向到 /tmp/y」
// 就可能把文件写到工作区外面. 这里必须由沙箱拦截, 不能只依赖工具检查.
func TestSandboxKeepsWritesInsideTheWorkspace(t *testing.T) {
	if !CanSandbox() {
		t.Skip("这台机器没有沙箱")
	}
	root := t.TempDir()
	box := Toolbox{Root: root, Sandbox: true}
	outside := filepath.Join(t.TempDir(), "escaped.txt")

	// 工作区里照常写
	if _, err := runTool().Run(box, map[string]any{"cmd": "echo inside > ok.txt"}); err != nil {
		t.Fatalf("工作区里都写不了: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ok.txt")); err != nil {
		t.Fatalf("工作区里的文件没写成: %v", err)
	}

	// 工作区外面写不出去 —— **判据是文件在不在, 不是命令的退出码**:
	// 命令自己可能报别的错, 而我们要的是"那个文件没被创建"
	out, _ := runTool().Run(box, map[string]any{"cmd": "echo escaped > " + outside})
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("写到工作区外面去了 —— 沙箱形同虚设。工具说: %s", out)
	}
}

// 不开沙箱时**照旧**: 真内核那条路已经在管了, 再套一层只是多一个失败点.
func TestWithoutSandboxNothingChanges(t *testing.T) {
	root := t.TempDir()
	if _, err := runTool().Run(Toolbox{Root: root}, map[string]any{"cmd": "echo hi"}); err != nil {
		t.Fatal(err)
	}
}

// 路径进规则要转义 —— 一个名字里带引号的目录能把规则**改写成别的意思**,
// 那是注入, 而这里注入的后果是沙箱形同虚设.
func TestSandboxQuotesThePath(t *testing.T) {
	if !CanSandbox() {
		t.Skip("这台机器没有沙箱")
	}
	got := sandboxProfile(`/tmp/a"b\c`)
	if strings.Contains(got, `"/tmp/a"b`) {
		t.Fatalf("引号没转义, 规则被改写了:\n%s", got)
	}
	if !strings.Contains(got, `\"`) || !strings.Contains(got, `\\`) {
		t.Fatalf("转义不完整:\n%s", got)
	}
}

/**
 * 撞在"账本不归你写"上的时候, 要说真正的原因.
 *
 *	`git add src/index.css` 可能拿到
 *	"Unable to create …/.git/worktrees/小乙/index.lock: Operation not
 *	permitted". 这不是普通文件权限不足, 而是账本位于当前工作区之外,
 *	只能由宿主完成写入; 说错原因会让调用方继续查 refs、查 reflog、试绕法,
 *	耗尽一轮时间而没有任何可执行动作.
 */
func Test账本写不动的时候说清不归它做(t *testing.T) {
	out := []byte("fatal: Unable to create '/x/两人做前端/.git/worktrees/小乙/index.lock'" +
		": Operation not permitted")
	got := runResult("git add .", out, 128)
	if !strings.Contains(got, "merge_up") {
		t.Errorf("没告诉它该用什么:\n%s", got)
	}
	if !strings.Contains(got, "不归你做") {
		t.Errorf("没说清这件事本来就不归它:\n%s", got)
	}
	// 原样的报错不许吞 —— 它是事实
	if !strings.Contains(got, "index.lock") {
		t.Errorf("把原报错吞了:\n%s", got)
	}
	// 别的权限错跟 git 账本无关, 不该乱贴这句
	other := runResult("echo x > /etc/hosts", []byte("sh: /etc/hosts: Operation not permitted"), 1)
	if strings.Contains(other, "merge_up") {
		t.Errorf("跟 git 无关的权限错也贴上了这句:\n%s", other)
	}
}

// 子命令的钟点要跟 OS 一致.
//
//	不带 TZ 的话 `date` 报 UTC, 而 bot 刚在提醒卡片上看到的是北京时间 ——
//	同一句话里出现两个钟点会造成 8 小时差的误解.
func TestCommandEnvCarriesTimezone(t *testing.T) {
	was := time.Local
	t.Cleanup(func() { time.Local = was })
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("这台机器上没有时区表")
	}
	time.Local = loc
	var got string
	for _, kv := range commandEnv(t.TempDir()) {
		if strings.HasPrefix(kv, "TZ=") {
			got = kv
		}
	}
	if got != "TZ=Asia/Shanghai" {
		t.Fatalf("子命令的环境里没有时区: %q", got)
	}
}
