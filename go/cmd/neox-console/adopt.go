package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

/**
 * adopt —— **把一摊活收进 git, 默认就这么干**.
 *
 * ── 为什么不能"要不要用 git"二选一 ──
 *
 *	不走 git 的项目, 整套东西一样都不剩: 每人一块地方(worktree)、
 *	谁干的(作者)、干出来了什么(提交)、交接(把分支交出去)、合入、
 *	验证证据(尾注)、"有人动了主干"的通知 —— 全部建在 git 上.
 *	而失败是**静悄悄**的: 界面只写一句"这个项目只能一个人干", 用户
 *	不会知道自己顺手丢掉了半个产品.
 *
 *	所以这里默认使用 git，不留下“要不要”的分支。
 *
 * ── 收进来 ≠ git init ──
 *
 *	原来那条路只 `git init` + 一个**空提交**, 不 add 任何东西.
 *	那样每个 bot 的 worktree 从空提交长出来, 里面**一个文件都没有** ——
 *	它对着一个空目录, 而用户的代码好端端躺在项目根上. 那比不用 git 更糟:
 *	它看起来像"这个项目是空的".
 *
 *	所以第一份快照要**把现有的文件收进来**. 收之前先写一份 .gitignore:
 *	25MB 的 pip 缓存一旦进入第一个提交，就会留在历史中，无法从提交中拿掉。
 *
 * ── 三条不收 ──
 *
 *	① 家目录、根目录、系统目录: 在那儿 git init 是灾难, 不是便利.
 *	② 太大: 几万个文件的目录多半是"这里面有不该进版本控制的东西",
 *	   硬收下去只会把仓库撑坏. 说清楚, 让人自己收拾.
 *	③ 已经在别人的仓库里: 那是他的仓库, 用他的就行.
 */

// 第一份快照的上限. 超过就不收 —— 多半是里面躺着 node_modules 之类的东西
const (
	maxFirstFiles = 20000
	maxFirstBytes = int64(300 << 20) // 300MB
)

// 默认忽略的那些: 全是"装出来的/编出来的/系统扔的", 没有一样是人写的.
//
//	**.env 也在里面**: 那里面是密钥. 一旦进了提交, 它就跟着每一条
//	分支到处走, 而且从历史里拿不掉.
const defaultIgnore = `# NeoxOS 起头时写的：装出来的、编出来的、系统扔的，都不进版本控制。
# 想收哪一样，把对应那行删掉就行。

# ── bot 的家和临时目录就在这儿 ──
#
# HOME 和 TMPDIR 都被指到了工作区(见 agent/run.go)：很多工具非要往家里
# 写缓存才肯干活，而它们的家如果在外面，隔离就漏了。代价是**工具写进
# 家目录的一切都落在这个项目里** —— 那些是副作用，不是产物。
#
# 真机上撞见过三次，一次比一次贵：
#   .tmp/smoke/ 三个草稿跟着一次提交进了主干
#   7.7MB 的 pip 缓存躺在 worktree 里
#   npm 的 .npm/_cacache —— **3400 个文件、100 万行、.git 涨到 52MB**
.tmp/
.npm/
.cache/
.config/
.local/
.pytest_cache/
.gradle/
.m2/
.cargo/
.rustup/
.deno/
.bun/
Library/

node_modules/
.venv/
venv/
__pycache__/
*.pyc
.pylibs/
dist/
build/
target/
.next/
.cache/
*.log
.DS_Store
.env
.env.*
`

// Adopt 把一个普通目录收进 git: 建仓库、写 .gitignore、把现有文件做成
// 第一份快照.
//
//	已经是仓库(而且有第一个提交)的, 一个字都不动 —— 那是用户自己的
//	历史, 我们只往上加分支.
func Adopt(dir string) error {
	if err := adoptable(dir); err != nil {
		return err
	}
	if !IsRepo(dir) {
		if _, err := runGit(dir, "init"); err != nil {
			return err
		}
	}
	/**
	 * **本来就有历史的仓库也要打点**.
	 *
	 *	已有历史只说明仓库已初始化，不说明它具备工作区隔离所需的忽略规则：
	 *
	 *	    版本控制里有 48 个垃圾文件, 比如
	 *	    Library/Caches/com.apple.python/…/_bootlocale.cpython-39.pyc
	 *
	 *	HOME 被指进工作区(隔离要的), 工具往家里写的缓存就落在项目里,
	 *	而宿主收活时 git add -A 一并收走. 人明天把 bot 放进自己的项目,
	 *	第一件事就是这个.
	 *
	 *	**但不许动他的 .gitignore** —— 那是他的东西, 而且是要提交的.
	 *	git 早就为这种事留了地方: .git/info/exclude 和 .git/info/attributes
	 *	是**本仓库本机**的规矩, 不进版本控制, 谁的文件都不用碰.
	 */
	housekeep(dir)
	if hasHEAD(dir) {
		return nil
	}
	// .gitignore 先写: 它决定下面那一 add 会收进来什么.
	// **已经有一份就不动** —— 那是用户的决定
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); os.IsNotExist(err) {
		if err := os.WriteFile(ignore, []byte(defaultIgnore), 0o644); err != nil {
			return err
		}
	}
	/**
	 * ── 锁文件不该成为冲突源 ──
	 *
	 *	三个人各自在自己的 worktree 里 npm install 时，
	 *	package-lock.json 会各不相同，**每个人合入都要解一次锁文件的
	 *	冲突** —— 而那是一份几千行的生成物, 解它既没意义也解不对.
	 *
	 *	git 早就给了办法: 声明这类文件"以我这边为准", 合并时不产生冲突.
	 *	锁文件是**生成物**, 真缺了什么下一次 install 会自己补回来 ——
	 *	而 package.json / pyproject.toml 那些**手写的**照旧正常合并,
	 *	该冲突的地方一处都不会少.
	 */
	writeMergePolicy(dir)

	files, bytes, err := wouldAdd(dir)
	if err != nil {
		return err
	}
	if files > maxFirstFiles || bytes > maxFirstBytes {
		return fmt.Errorf("这个目录太大了（%d 个文件 / %s），没敢直接收进 git —— "+
			"多半是里面躺着不该进版本控制的东西，加几行 .gitignore 再来", files, human(bytes))
	}
	if _, err := runGit(dir, "add", "-A"); err != nil {
		return err
	}
	_, err = runGit(dir, "-c", "user.name=NeoxOS", "-c", "user.email=os@neox.local",
		"commit", "--allow-empty", "-m", "NeoxOS: 把这摊活收进 git（第一份快照）")
	return err
}

/**
 * wouldAdd 这一 add 会收进来多少东西.
 *
 *	问 git 而不是自己走目录: .gitignore 该怎么解释只有它算得准,
 *	自己实现一份迟早跟它分叉.
 */
func wouldAdd(dir string) (files int, bytes int64, err error) {
	out, err := runGit(dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		name := strings.TrimSpace(line[2:])
		files++
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			bytes += info.Size()
		}
		// 早退: 已经超了就没必要把剩下几万个 stat 一遍
		if files > maxFirstFiles || bytes > maxFirstBytes {
			return files, bytes, nil
		}
	}
	return files, bytes, nil
}

/**
 * adoptable 这个地方能不能收.
 *
 *	家目录和根目录上 git init 是灾难: 一个 add -A 会把整台机器的
 *	文档、照片、密钥全扫进去, 而且从此每条 git 命令都要扫全盘.
 */
func adoptable(dir string) error {
	real := realPath(dir)
	if real == "" || real == "/" {
		return fmt.Errorf("不能在根目录上建仓库")
	}
	if home, err := os.UserHomeDir(); err == nil && realPath(home) == real {
		return fmt.Errorf("不能把整个家目录收进 git —— 给这摊活单独开个目录")
	}
	for _, bad := range []string{"/usr", "/etc", "/var", "/bin", "/sbin", "/opt", "/System", "/Library", "/Applications"} {
		if real == bad || strings.HasPrefix(real, bad+"/") && len(strings.Split(strings.Trim(real[len(bad):], "/"), "/")) < 2 {
			return fmt.Errorf("这是系统目录，不在这儿建仓库")
		}
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return fmt.Errorf("这个目录不在: %s", dir)
	}
	return nil
}

func human(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%dMB", bytes>>20)
	default:
		return fmt.Sprintf("%dKB", bytes>>10)
	}
}

// 生成物: 合并时以本地那份为准, 不产生冲突. 手写的依赖清单不在里面
const lockPolicy = `# 锁文件是生成物：合并时以本地那份为准，不解冲突。
# 真缺了什么，下一次 install 会自己补回来。
# 手写的那些（package.json / pyproject.toml / go.mod）照旧正常合并。
package-lock.json merge=ours
yarn.lock merge=ours
pnpm-lock.yaml merge=ours
npm-shrinkwrap.json merge=ours
poetry.lock merge=ours
Cargo.lock merge=ours
composer.lock merge=ours
Gemfile.lock merge=ours
`

/**
 * writeMergePolicy 把"锁文件怎么合"写进仓库.
 *
 *	两样都要: .gitattributes 说"这几个用 ours 策略", 而 ours 这个驱动
 *	要在仓库配置里定义(git 自带的 ours 只对整次合并有效, 按文件的那个
 *	要自己声明). 少一样都不生效, 而且**不生效的时候一点声音都没有** ——
 *	照样冲突, 没人知道为什么.
 */
/**
 * housekeep 给一个仓库配上 NeoxOS 这套规矩 —— **不碰用户的任何文件**.
 *
 *	写的是 .git/info/ 下面那两份: git 专门给"这个仓库在这台机器上的
 *	规矩"留的地方, 不进版本控制. 用户的 .gitignore / .gitattributes
 *	一个字都不动.
 *
 *	每次都写(而不是"没有才写"): 规矩会跟着版本变, 而这两份文件本来
 *	就是我们的, 没有"用户的决定"要保护.
 */
func housekeep(dir string) {
	info := filepath.Join(dir, ".git", "info")
	if !IsRepo(dir) {
		return
	}
	// worktree 里 .git 是个文件, 真正的 info/ 在主仓库那边
	if real, err := runGit(dir, "rev-parse", "--git-common-dir"); err == nil {
		got := strings.TrimSpace(real)
		if got != "" && got != ".git" {
			info = filepath.Join(got, "info")
		}
	}
	if os.MkdirAll(info, 0o755) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(info, "exclude"), []byte(defaultIgnore), 0o644)
	_ = os.WriteFile(filepath.Join(info, "attributes"), []byte(lockPolicy), 0o644)
	_, _ = runGit(dir, "config", "merge.ours.driver", "true")
}

func writeMergePolicy(dir string) {
	path := filepath.Join(dir, ".gitattributes")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(lockPolicy), 0o644); err != nil {
			return
		}
	}
	// "以我这边为准"就是"什么都不做, 保留当前那份"
	_, _ = runGit(dir, "config", "merge.ours.driver", "true")
}
