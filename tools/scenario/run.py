#!/usr/bin/env python3
"""
scenario —— 一台**自动跑真实场景**的测试台。

── 为什么不是手工戳 ──

    到今天为止, 所有的验证都是我自己一句句发给 bot、再一个个 curl 回来看.
    那种验证有两个毛病: 一是**只覆盖我想得到的路**, 二是**它不会重复** ——
    改完一处, 昨天验过的东西没人再验一遍.

    而这套东西的失败方式恰恰是"每一块都对, 合起来不对": 前端对着 mock
    做完、后端做完、两边都绿, 而应用不通. 那种问题只有**跑完整场景 + 查
    不变量**才抓得住.

── 抽象在哪儿 ──

    **场景**只说"人会怎么用": 开个项目、拉几个人、说一句人话.
    它不规定 bot 怎么干 —— 怎么分工、谁写哪块、用不用 pass, 都是它们的事.

    **不变量**才是判据: 不管它们怎么干, 干完之后这些必须成立.
    这样加一个场景只是加三行数据, 而每一条不变量对所有场景一起生效.

用法:
    ./run.py list                 列出场景
    ./run.py run <名字>           跑一个
    ./run.py rotate               按轮次跑下一个(定时器用这个)
"""
import base64
import json
import os
import re
import shutil
import subprocess
import threading
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# png 跟这个文件在同一个目录 —— 从别处调用时 cwd 不一定在这儿
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from png import digits_png  # noqa: E402

HOME = os.path.expanduser("~")
STAGE = f"{HOME}/AI/试验场"          # 场景自己的地盘, 不碰用户的项目
STATE = f"{HOME}/.neox-os/scenario-state.json"
LOG = f"{HOME}/.neox-os/scenario-log.jsonl"      # 每一轮都记一行, 跨轮次能对比
OS_URL = "http://127.0.0.1:7717"


# ── 跟 OS 说话 ──────────────────────────────────────────────

def token() -> str:
    """从跑着的 Electron 里问 token —— 它是每次启动新生成的"""
    out = subprocess.run(
        ["python3", os.path.join(os.path.dirname(__file__), "whoami.py")],
        capture_output=True, text=True)
    return out.stdout.strip()


def api(tok, path, body=None):
    req = urllib.request.Request(
        OS_URL + path,
        headers={"Authorization": "Bearer " + tok, "Content-Type": "application/json"},
        data=None if body is None else json.dumps(body).encode())
    return json.loads(urllib.request.urlopen(req).read() or b"null")


def git(repo, *args):
    # **core.quotePath=false 是必须的**.
    #
    #   缺省下 git 把非 ASCII 路径转成八进制转义: 草稿.txt 出来是
    #   "\346\210\221…". 于是 ⓪ 那条"一个文件都没多"里的
    #   startswith("我的草稿") 永远不成立 —— 哨兵被算成了产出,
    #   **那条不变量会一直空转**. 一人承担五件事运行 25 秒却没有产出时,
    #   仍会报 files:3 / bad:[] —— 假绿正是从这里漏出.
    out = subprocess.run(["git", "-C", repo, "-c", "core.quotePath=false", *args],
                         capture_output=True, text=True)
    return out.stdout.strip()


def run_in(cwd, *args, timeout=120):
    return subprocess.run(args, cwd=cwd, capture_output=True, text=True, timeout=timeout)


# ── 场景: 只说"人会怎么用" ──────────────────────────────────

SCENARIOS = [
    {
        "name": "两人做前端",
        "why": "最常见的一摊: 两个人一个项目, 一句话说完就走",
        "crew": [("小甲", "前端页面归你"), ("小乙", "数据和接口归你")],
        "say": "做个待办列表吧，React 写，能加能删能标记完成。你们分一下。",
        "wait": 600,
    },
    {
        "name": "一个人干小活",
        "why": "小活给一个人 —— 系统不该硬拆, 也不该有人空转",
        "crew": [("小丙", "这摊活都归你")],
        "say": "写个命令行的记账小工具，能记一笔、能看总数，Python 就行。",
        "wait": 420,
    },
    {
        "name": "三人抢同一个文件",
        "why": "并发的最坏情况: 三个人都要改同一处, 看合并和冲突自解",
        "crew": [("小丁", "你写页面"), ("小戊", "你写接口"), ("小己", "你负责验收")],
        "say": "做个最小的登录页，前后端都要，接口和页面要真连上。三个人一起弄。",
        "wait": 900,
    },
    {
        "name": "一个人扛五件事",
        "why": "任务量突然变大: 一个人五件活 —— 看它要不要人, 长活报不报中间进度",
        "crew": [("小庚", "这摊活都归你")],
        "say": "帮我把这几件都做了：流水导出成 csv、加个按月汇总、"
               "写个命令行入口、补几个测试、再写个 README。Python 就行。",
        "wait": 900,
    },
    {
        "name": "半路喊停",
        "why": "**必须停得住**: 干到一半喊停, 停完还能接着干",
        "crew": [("小辛", "这摊活都归你")],
        "say": "写个通讯录，能加人、能按名字查、能改能删，"
               "存成 json，再配上命令行和几个测试。Python。",
        "wait": 720,
        # 最多等这么久去逮它正在干活的那一刻 —— 不是"到点就喊"
        "stop_after": 240,
    },
    {
        "name": "活到一半换人",
        "why": "**交接**: 干到一半换个人接手 —— 接手的人得接得上, 项目卡上得留下痕迹",
        "crew": [("小壬", "这摊活先归你"), ("小癸", "有事你接手")],
        "say": "写个书签管理，能存网址、能按标题搜、能打标签、能导出，"
               "存成 json，再配上命令行和几个测试。Python。",
        "wait": 900,
        # 干到这个点上换人 —— 挑在它手里已经有活、但还没干完的时候
        # 最多等这么久去逮它正在干活的那一刻
        "hand_at": 240,
        "hand_to": "小癸",
    },
    {
        "name": "合错了要撤得回来",
        "why": "项目卡上写着'每一条都能撤'. **撤不回来的承诺不如不给** —— "
               "撤完主干得真的回到上一版, 而且人手写的东西一个字不能动",
        "crew": [("小子", "这摊活都归你")],
        "say": "写个温度换算的小工具，摄氏华氏互转，带几个测试，Python。",
        "wait": 600,
        "undo": True,
    },
    {
        "name": "烧到上限",
        "why": "花费上限的失败方式最隐蔽: 撞上限**默默死掉**跟"
               "'把手上的活收好再说清楚'在界面上长得一模一样",
        "crew": [("小丑", "这摊活都归你")],
        "say": "写个 markdown 转 html 的小工具，标题、列表、代码块、链接、"
               "表格都要，每样配测试，再写个 README。Python。",
        "wait": 600,
        # 一轮最多烧这么多.
        #
        #   **要够它干出点东西再撞线**: 设成 6000 的话, 光把活和工具说给
        #   模型听就不止这个数, 它列一次目录就到线了 —— 那验的是"上限
        #   拦不拦得住", 而这个场景要验的是**拦住之后**手上的活丢没丢.
        "cap": 40000,
    },
    {
        "name": "看得见图",
        "why": "多模态: 发一张图给它. **这条路坏过一次**, 而且坏的时候"
               "跟'模型不支持'长得一模一样 —— 图上画两位数字, 蒙中 1/100",
        "crew": [("小寅", "这摊活都归你")],
        "say": "这张图上是两位数字，是几？只回数字，别的都别说。",
        "wait": 300,
        "shows": "47",
    },
    {
        "name": "发张卡片",
        "why": "渲染: 它发的卡片人到底看不看得见 —— **界面不认识的卡片曾经被"
               "静默丢掉**, 而工具报的是成功",
        "crew": [("小卯", "这摊活都归你")],
        "say": "把这三个月的数给我摆个表看看：一月 100，二月 230，三月 90。",
        "wait": 300,
        "card": ["230", "二月"],
    },
    {
        "name": "一直干下去",
        "why": "**时间拉长才露头的那些**: 连着十来轮之后, 每一轮是不是越来越贵. "
               "别的场景都只跑一两分钟, 而明天是连着干几个小时",
        "crew": [("小辰", "这摊活都归你")],
        "say": "写个小的通讯录，Python，能加人能查人。",
        "wait": 300,
        # 头一句之后再连着说这些 —— 一句一轮
        "then": [
            "加个删除。",
            "查的时候不区分大小写。",
            "加个改电话。",
            "存成 json，重启还在。",
            "加个列出全部，按名字排。",
            "补几个测试。",
            "错误的输入要给清楚的提示。",
            "加个 --version。",
            "写个 README。",
        ],
    },
    {
        "name": "进一个有底子的项目",
        "why": "**明天真正要走的那条路**: 人把 bot 放进一个已经有历史、"
               "而且他自己还有没提交完的改动的项目 —— 别的场景全是空目录起步",
        "crew": [("小巳", "这摊活都归你")],
        "say": "给收款那块加个手续费，按金额百分比算，别忘了测试。",
        "wait": 600,
        "seed": True,
    },
    {
        "name": "两摊活同时开工",
        "why": "**明天就是这么用的**: 财务和 OA 两个项目一起开工. "
               "到现在为止一次只开一摊 —— 串台了谁都看不出来",
        "crew": [("小午", "这摊活都归你")],
        "say": "写个报销单，能填金额和事由，能算合计，Python，配测试。",
        "wait": 600,
        # 另一摊同时在干的活 —— 人手、项目、说的话都是另一套
        "beside": {
            "name": "另一摊",
            "crew": [("小未", "这摊活都归你")],
            "say": "写个请假条，能填天数和理由，能算剩余额度，Python，配测试。",
        },
    },
    {
        "name": "接着上一摊往下做",
        "why": "第二轮: 在已有代码上加东西, 看它们读不读得懂前一轮留下的东西",
        "reuse": True,
        # **每次要个不一样的**: 反复要求同一件事时, 首次完成后这个场景
        #   就再也验不到东西; 第二次运行会在 20 秒内完成且判据不会报错.
        "says": [
            "在刚才那个东西上再加一块：能按关键字搜。",
            "再加个统计吧，能数出来一共有多少条。",
            "输入不对的时候得给个说得清楚的提示，别直接崩。",
            "加个 --version，能看出来是哪一版。",
        ],
        "wait": 600,
    },
]


# ── 不变量: 不管它们怎么干, 干完之后这些必须成立 ──────────────

def check(project, crew, tok, since=0) -> list:
    """返回违反了的那些 —— 空 = 这一轮是干净的"""
    bad = []
    tracked_all = git(project, "ls-files").splitlines()

    # ⓪ **什么都没产出就是没干活**.
    #
    #    工作区可能指向一个已经不存在的仓库, 仍会读文件、写代码并报告
    #    "已经写好并验证通过" —— 而主干一个
    #    文件都没多. 那一轮报出来是 files: 0 / bad: [] —— **什么都没干,
    #    六条不变量一条没响**, 因为每一条都是"有东西才查".
    if nothing_shipped(tracked_all):
        bad.append("主干上一个文件都没多 —— 这一轮什么都没落地")

    # ① 版本控制里不许有垃圾. 工具写进家目录/临时目录的东西不是产物,
    #    版本控制垃圾可达到 3400 个文件、100 万行
    tracked = git(project, "ls-files").splitlines()
    junk = junk_in(tracked)
    if junk:
        bad.append(f"版本控制里有 {len(junk)} 个垃圾文件, 比如 {junk[0]}")

    # ② 主干起得来. "每块都绿而合起来不通"是这套东西最主要的失败方式
    #
    #    **要去找那个能验的地方, 不能假设它在根上**: React 应用可能放在
    #    todo-app/ 子目录, 若检查只在根上找 package.json, 12 个文件全绿也
    #    一样都没真验过.
    #    那正是假绿: 检查空转的样子跟通过一模一样.
    checked = 0
    for pkg in dirs_with(tracked, "package.json"):
        at = os.path.join(project, pkg)
        scripts = (json.load(open(f"{at}/package.json")).get("scripts") or {})
        if "build" in scripts:
            checked += 1
            if not os.path.isdir(f"{at}/node_modules"):
                run_in(at, "npm", "install", "--silent", "--no-audit", "--no-fund", timeout=300)
            r = run_in(at, "npm", "run", "build", "--silent", timeout=300)
            if r.returncode != 0:
                bad.append(f"{pkg or '.'} 里 npm run build 挂了: " + tail(r.stderr or r.stdout))
        if "test" in scripts:
            checked += 1
            r = run_in(at, "npm", "test", "--silent", timeout=300)
            if r.returncode != 0:
                bad.append(f"{pkg or '.'} 里 npm test 是红的: " + tail(r.stdout or r.stderr))
    # 没有 build 的 js 至少要能被解析 —— 语法坏了当场就知道
    for js in [f for f in tracked if f.endswith((".js", ".mjs", ".cjs")) and "node_modules" not in f][:8]:
        checked += 1
        r = run_in(project, "node", "--check", js, timeout=30)
        if r.returncode != 0:
            bad.append(f"{js} 语法就不对: " + tail(r.stderr))
    # python: 每个包/顶层模块都 import 一遍
    for py in [f for f in tracked if f.endswith(".py") and not os.path.basename(f).startswith("test")][:8]:
        checked += 1
        at = os.path.join(project, os.path.dirname(py))
        r = run_in(at, "python3", "-c", f"import {os.path.basename(py)[:-3]}", timeout=30)
        if r.returncode != 0 and ("ModuleNotFoundError" in r.stderr or "SyntaxError" in r.stderr):
            bad.append(f"import {py} 挂了: " + tail(r.stderr))

    # ③ 测试要么没有, 要么是绿的. 有测试却红着 = 它们自己没验就合了
    for at in dirs_with(tracked, "pytest.ini") + [project] :
        if not any("test" in f for f in tracked):
            break
        checked += 1
        r = run_in(project, "python3", "-m", "pytest", "-q", timeout=240)
        if r.returncode not in (0, 5) and "no tests ran" not in (r.stdout or ""):
            bad.append("pytest 是红的: " + tail(r.stdout))
        break

    #    **一样都验不了也算问题**: 既没有构建、也没有测试、也没有 import
    #    的一摊, 这条不变量就是空转 —— 而空转看起来跟通过一模一样.
    if checked == 0 and len(tracked) > 3:
        bad.append("这一摊没有任何可验的东西(没构建、没测试、没 import) —— 假绿正是从这儿漏的")

    # ③.5 **能起来的东西要真起来**.
    #
    #    上一轮三个人做了个登录页, 六条不变量全绿 —— 而它们验的只是
    #    "语法没错". 服务起不起得来、页面出不出得来、接口通不通,
    #    一样都没查. 我是自己 curl 了一遍才知道它真能用.
    #
    #    **"能跑"和"能编译"是两件事**, 而人要的是前者. 有入口就把它起
    #    起来打一下 —— 起不来、不应答, 当场就知道.
    port, entry = 39117, entry_point(project, tracked)
    if entry:
        checked += 1
        proc = subprocess.Popen(entry["argv"], cwd=entry["at"], env={**os.environ, "PORT": str(port)},
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True)
        heard = []
        threading.Thread(target=lambda: heard.append((proc.stdout.read() or b"").decode()),
                         daemon=True).start()
        try:
            code = knock(port, waits=25)
            # **它自己说在哪儿听着, 就去那儿敲**.
            #
            #   判据若设了 PORT=39117 后只敲这一个, server 写死 3000、却打印
            #   "Server listening on
            #   http://localhost:3000" —— 应用是好的(GET / 200、登录接口
            #   返回 token), 判据却报"起来了但不应答".
            #
            #   PORT 这个约定谁都没跟它说过. 人不会这样查: 人会看它
            #   说了什么, 然后去敲那个门. 判据也该跟着证据走.
            if code is None:
                said = port_said("".join(heard))
                if said and said != port:
                    code = knock(said, waits=5)
            if code is None:
                out = "".join(heard)[-300:]
                bad.append(f"{entry['what']} 起来了但不应答: {tail(out)}")
        finally:
            try:
                os.killpg(os.getpgid(proc.pid), 9)
            except Exception:
                pass

    # ④ 没人手上压着没合的活. 一条干完却没合的分支, 在别处看跟"什么都
    #    没干"一模一样 —— 那是最容易丢活的地方
    report = api(tok, "/project?path=" + urllib.parse.quote(project))
    for m in report.get("members", []):
        if m.get("unmerged", 0) > 0:
            bad.append(f"{m['bot']} 手上还压着 {m['unmerged']} 条没合的")

    # ⑤ 提交记录要说得出话. 全是"合入前把手上的活收一下"的话, git log
    #    就是一堆一模一样的句子, 谁在什么时候干了什么一个字看不出来
    #    **总数少的时候更该查, 不是更不该**: 原来要 >3 条才判, 于是
    #    "活到一半换人"那一轮三条里两条套话("交接前把手上的活收一下"
    #    "同步前把手上的活收一下")一声没响 —— 一份三条的记录全是套话,
    #    比二十条里有十条更没法看.
    subjects, dumb = boilerplate(git(project, "log", "--format=%s", "-20").splitlines())
    if subjects and len(dumb) > len(subjects) / 2:
        bad.append(f"{len(dumb)}/{len(subjects)} 条提交标题是套话")

    # ⑦ **过程里有没有卡住**.
    #
    #    前面六条查的全是"最后留下了什么", 而这套东西的毛病常常在过程里:
    #    进程可能撞上停滞被拦下、一直报同一个错，或申请权限后无人决策
    #    就那么停着. 产物照样能是好的(别人替他做完了), 而那个人白烧了
    #    一轮 —— 只看产物永远看不见.
    for who, kind, text in troubles(tok, crew, since):
        bad.append(f"{who} {kind}: {tail(text, 90)}")

    # ⑦ **同一样东西不许两个人各做一遍**.
    #
    #    一句"@所有人 …你们分一下"被逐个投给每个人时, 每个人收到的都是
    #    一句光秃秃的话 —— 谁也不知道其他人也收到了. 于是
    #    两个人各搭了一套 Vite 脚手架, 一个后来又把自己那套删了.
    #    那一轮 build 是绿的、测试是绿的、没有未合的活 —— **六条不变量
    #    一条没响**, 因为浪费掉的那半天不在任何一条的视野里.
    #
    #    判据只看结果: 同一个文件路径, 被两个不同的人分别新建过.
    #    怎么避免是它们的事(分工、pass、还是先问一句), 这里不规定.
    twice = double_built(project)
    if twice:
        bad.append(f"{len(twice)} 个文件被两个人各建了一遍, 比如 {twice[0]}")

    # ⑥ 人的东西没被动过. 我在主干目录里放一个哨兵文件, 谁都不该碰它
    sentinel = f"{project}/我的草稿.txt"
    if os.path.exists(sentinel) and open(sentinel).read() != SENTINEL:
        bad.append("主干目录里我那份没提交的东西被改了")

    return bad


def junk_in(tracked) -> list:
    """装出来的、缓存的、构建产物 —— 这些不是产物, 不该进版本控制"""
    return [f for f in tracked if f.split("/")[0] in
            (".npm", ".tmp", ".cache", ".config", ".local", "node_modules",
             "dist", "Library", ".pytest_cache")]


def boilerplate(subjects) -> tuple:
    """(算数的那些, 其中是套话的那些) —— 开局快照不算, 它本来就该是那句话"""
    subs = [s for s in subjects if not s.startswith("NeoxOS:")]
    dumb = [s for s in subs if s.startswith(
        ("合入前把手上的活", "同步前把手上的活", "交接前把手上的活", "[系统]", "你申请的"))]
    return subs, dumb


def nothing_shipped(tracked) -> bool:
    """这一轮有没有东西落到主干上 —— 除开开局就有的那几个"""
    return len([f for f in tracked if not f.startswith((".git", "我的草稿"))]) == 0


# 中途最多能静多久 —— 超过这个人就开始怀疑它是不是死了
#
#   把进展定义为至少两次提交是不对的: **一轮长活本来就只在末尾提交
#   一次**。三人各运行 436 秒并各提交一次时，会被报成"一次进展都没报";
#   判据在数一件本来就不会发生的事.
#
#   人真正会难受的是**长时间一个动静都没有**: 界面上一直转圈, 不知道
#   它是在干活还是卡死了. (干到哪一步、花了多少、多久, 界面本来就在
#   显示 —— 见 store.ts 的 turnProgress.) 所以量的是静默的长度.
QUIET_MOST = 150

SENTINEL = "这是人手写的东西，谁都别动\n"


def double_built(project) -> list:
    """同一个路径被两个不同的人分别新建过 —— 重复劳动的痕迹"""
    log = git(project, "log", "--no-merges", "--diff-filter=A",
              "--format=谁>%an", "--name-only")
    who, by = "", {}
    for line in log.splitlines():
        if line.startswith("谁>"):
            who = line[2:]
        elif line.strip() and who and who != "NeoxOS":
            by.setdefault(line.strip(), set()).add(who)
    return sorted(f for f, people in by.items() if len(people) > 1)


def entry_point(project, tracked):
    """
    这一摊有没有"能起来的东西".

        按人会怎么起来找: package.json 里的 start 脚本, 或者一眼就是
        入口的那几个名字. 找不到就算了 —— 不是每一摊都有服务.
    """
    for pkg in dirs_with(tracked, "package.json"):
        at = os.path.join(project, pkg)
        scripts = (json.load(open(f"{at}/package.json")).get("scripts") or {})
        if "start" in scripts:
            return {"argv": ["npm", "start", "--silent"], "at": at, "what": f"{pkg or '.'} 的 npm start"}
    for name in ("server.js", "app.js", "index.js", "main.js", "server.py", "app.py", "main.py"):
        for f in tracked:
            if os.path.basename(f) == name:
                argv = ["node", os.path.basename(f)] if f.endswith(".js") else ["python3", os.path.basename(f)]
                return {"argv": argv, "at": os.path.join(project, os.path.dirname(f)), "what": f}
    return None


def port_said(out) -> int:
    """它自己说在哪个端口听着 —— 找不到返回 0"""
    for m in re.finditer(r"(?::|port\s+|端口\s*)(\d{2,5})\b", out, re.I):
        n = int(m.group(1))
        if 1024 <= n <= 65535:
            return n
    return 0


def knock(port, waits=20):
    """敲门: 起来了就该应答. 返回状态码, None = 一直没应"""
    for _ in range(waits):
        time.sleep(1)
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/", timeout=2) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code          # 4xx/5xx 也算应答 —— 它活着
        except Exception:
            continue
    return None


def troubles(tok, crew, since=0):
    """
    这一轮里谁卡住过 —— 从账本里读, 不猜.

        三类都要: 停滞/失败(它自己知道停了)、还没拍板的申请(它在等人,
        而人可能不在)、模型连着出错(那不是它的错, 但活确实没干成).
    """
    names = who_by_pid(tok, crew)
    out, pending = [], {}
    for ev in events_since(tok, since, set(names)):
        who = names.get(ev.get("pid"))
        pl = ev.get("payload") or {}
        ph = pl.get("phase")
        if ph in ("turn_failed", "step_limit"):
            out.append((who, "这一轮停下来了", reason(pl)))
        elif ph == "stall" and pl.get("level") != "warn":
            # **warn 那一级是提醒, 不是停手**.
            #
            #   读取队友尚未合入的文件后可能收到一句"路径不对, 先 list_dir",
            #   但执行仍会继续完成数据层和冒烟脚本。若把它报告为已停止,
            #   理由还会为空(账本字段名是 msg). 测试台自己的假阳性跟假绿一样坏:
            #   一条查不实的问题会把真正的问题淹掉.
            out.append((who, "这一轮停下来了", reason(pl)))
        elif ph == "model_err" and pl.get("n", 0) >= 2:
            out.append((who, "模型连着出错", str(pl.get("err", ""))))
        elif ev["kind"] == "decide.requested":
            pending[(ev["pid"], pl.get("did"))] = str((pl.get("present") or {}).get("title", ""))
        elif ev["kind"] == "decide.resolved":
            pending.pop((ev["pid"], pl.get("did")), None)
    for (pid, _), title in pending.items():
        out.append((names.get(pid, "?"), "还等着人拍板", title.replace("\n", " ")))
    return out


def reason(pl) -> str:
    """账本里那句话 —— 字段名换过好几个, 一个都别漏"""
    for k in ("msg", "why", "detail", "err", "text"):
        if pl.get(k):
            return str(pl[k])
    return "(账本里没写理由)"


def dirs_with(tracked, name):
    """哪几个目录里有这个文件 —— 应用可能在根上, 也可能在子目录里"""
    out = []
    for f in tracked:
        if os.path.basename(f) == name and "node_modules" not in f:
            out.append(os.path.dirname(f))
    return out


def tail(text, n=200):
    text = (text or "").strip().replace("\n", " ")
    return text[-n:]


# ── 跑一轮 ──────────────────────────────────────────────────

# 人自己写了一半、还没提交的那一行 —— 谁都不许动
HALF_DONE = "    # TODO: 手续费还没想好怎么算\n"


def seed_project(project):
    """
    **铺一个有底子的项目**.

        别的场景全是空目录起步, 而人明天要走的是另一条路: 把 bot 放进
        一个已经有历史的项目, 而且他自己手上还有没提交完的改动.

        这条路上真正怕的两件事:
          · 他没提交完的那一行被谁顺手带走或者盖掉
          · 谁去改写了历史(rebase/reset), 别人的 worktree 下一次同步
            就撞上一堆莫名其妙的冲突

        所以底子里必须**同时**有: 几条历史, 和一行没提交的改动.
    """
    run_in(project, "git", "init", "-q")
    with open(f"{project}/pay.py", "w") as f:
        f.write("def charge(amount):\n    return amount\n")
    with open(f"{project}/test_pay.py", "w") as f:
        f.write("from pay import charge\n\n\ndef test_charge():\n"
                "    assert charge(100) == 100\n")
    run_in(project, "git", "add", "-A")
    run_in(project, "git", "-c", "user.name=我", "-c", "user.email=me@x",
           "commit", "-qm", "先把收款那块搭起来")
    with open(f"{project}/notes.md", "w") as f:
        f.write("# 手头的事\n\n- 手续费那块还没动\n")
    run_in(project, "git", "add", "-A")
    run_in(project, "git", "-c", "user.name=我", "-c", "user.email=me@x",
           "commit", "-qm", "记两笔待办")
    # **我自己写了一半、还没提交的那一行** —— 这才是最怕被动的东西
    with open(f"{project}/pay.py", "a") as f:
        f.write(HALF_DONE)


def start_beside(tok, spec, utterance) -> str:
    """
    **另一摊活同时开工**.

        人明天就是这么用的: 财务一摊、OA 一摊, 两边一起干. 而到现在为止
        所有场景都是一次只开一摊 —— 两摊之间串了台谁都看不出来.
    """
    project = f"{STAGE}/{spec['name']}"
    clean_one(tok, project)
    open(f"{project}/我的草稿.txt", "w").write(SENTINEL)
    room = "#" + os.path.basename(project)
    for name, role in spec["crew"]:
        api(tok, "/create", {"name": name, "work": project, "thread": room, "role": role})
    time.sleep(5)
    crew = [p for p in api(tok, "/processes")
            if ((p.get("spec") or {}).get("labels") or {}).get("thread") == room]
    for one in crew:
        api(tok, "/say", {"pid": one["pid"], "text": spec["say"], "said": spec["say"],
                          "from": "我", "utterance": utterance + "-b"})
    return project


def ours(project) -> set:
    """这摊自己的产物 —— 系统铺的那几个不算"""
    return {f for f in git(project, "ls-files").splitlines()
            if not f.startswith((".git", "我的草稿"))}


def blob(project, path) -> str:
    """这个文件在这摊里的内容指纹"""
    out = git(project, "rev-parse", f"HEAD:{path}")
    return out.strip()


def apart(here, there) -> list:
    """
    **两摊活不许串台**.

        判据只看结果: 这摊的产物不许出现在那摊里, 反过来也一样.
        串台了最可能的样子是"报销单里冒出一个请假条" —— 而两边各自
        构建、测试都照样绿, 没有任何别的判据会响.
    """
    if not there or not os.path.isdir(there):
        return ["另一摊没开起来 —— 这一轮验不出串不串台"]
    out = []
    mine, yours = ours(here), ours(there)
    # **认内容, 不认文件名**.
    #
    #   第一版比的是路径, 于是 CHARTER.md / PLAN.md 当场报了串台 ——
    #   而那是每个项目按惯例都会有的一份, **同名不同内容**, 不是串台.
    #   真串台的样子是一个文件**原样**出现在另一摊里.
    both = [f for f in sorted(mine & yours) if blob(here, f) == blob(there, f)]
    if both:
        out.append(f"两摊活串台了: {'、'.join(both[:3])} 两边一模一样")
    for name, files in (("这摊", mine), ("另一摊", yours)):
        if not files:
            out.append(f"{name}一个文件都没落地")
    return out


def reply_sizes(tok, crew, since) -> list:
    """这一轮它们跟人说的每句话有多长 —— 只记, 不判"""
    out = []
    for ev in events_since(tok, since, all_pids(tok, crew)):
        pl = ev.get("payload") or {}
        if pl.get("phase") == "reply" or (pl.get("phase") == "done" and pl.get("summary")):
            out.append(len(str(pl.get("text") or pl.get("summary") or "")))
    return sorted(out)


def talks_like_people(tok, crew, since) -> list:
    """
    **它们之间怎么说话**.

        十六个场景查的全是"活干成没有", 一条都没查过它们互相说的话.
        而真机上一眼就看出问题: 小丁给小戊派后端写了 **902 个字** ——
        端口读哪个环境变量、哪个状态码、JSON 里有哪几个字段, 一份规格书.

        为什么这是毛病而不是"认真":
          · 那些细节是它猜的 —— 它自己一行后端都没写过
          · 项目里就有一份 CHARTER.md, 两个人都看得见、改了还能同步;
            抄进一句话里的那份从抄完就开始过期
          · 人不这么说话

        判据只看长度: 交办的话超过一屏就是抄了不该抄的东西.
    """
    names = who_by_pid(tok, crew)
    out = []
    for ev in events_since(tok, since, set(names)):
        pl = ev.get("payload") or {}
        if ev.get("kind") != "input.recv":
            continue
        who = str(pl.get("from") or "")
        if who in ("", "我", "系统"):
            continue                      # 人说的和系统说的不在这条判据里
        said = str(pl.get("text") or "")
        for sign in ("sync_down", "merge_up", "hand_over"):
            if sign in said:
                # **教同行怎么干活**: 拉主干、自己验、合上去, 他自己都会 ——
                #   每个 bot 受的是同一套规矩. 重复一遍等于把他降成一双手,
                #   那时候人自己指挥得了, 要这个团队干嘛.
                out.append(f"{who} 在教同行用工具({sign}) —— 他自己就会: {tail(said, 60)}")
                break
        if len(said) > 300:
            out.append(f"{who} 交办给别人的话有 {len(said)} 个字 —— 那是规格书不是交代活: "
                       f"{tail(said, 60)}")
    return out


def told_about_held(tok, crew, project, since, bad) -> list:
    """
    **人手上那份没被覆盖 —— 测试红是必然的, 有没有告诉他才是判据**.

        这个场景故意让人和 bot 改同一个文件. 合完之后主干提交里是新的、
        磁盘上是他那份旧的(护住没提交的改动, 这一步是对的) —— 于是新
        测试配旧代码, **pytest 必然红**. 那不是毛病, 是这条路本来的样子.

        真正该查的是: **有没有人告诉他**. 而且要在第一行 —— 给人的
        那条通知只取第一行(agent/gitteam.go 的 oneLine), 写在第二段的话
        bot 看得见、人看不见, 可这件事只有他能解决.

        照报那条已知的红, 只会把真问题淹掉.
    """
    # **别去数 --porcelain 的前两列**: 那两列里的空格是有含义的, 而
    #   git() 会 strip 掉开头 —— " M pay.py" 到手是 "M pay.py", 于是
    #   按列取名字永远取错. diff --name-only 说的就是"改了没提交的那几个".
    dirty = [x for x in git(project, "diff", "--name-only").splitlines() if x]
    if not dirty:
        return bad
    kept = [b for b in bad if "pytest 是红的" not in b]
    for ev in events_since(tok, since, all_pids(tok, crew)):
        pl = ev.get("payload") or {}
        if pl.get("tool") != "merge_up" or pl.get("phase") != "tool_ok":
            continue
        first = str(pl.get("result") or "").split("\n")[0]
        if all(f in first for f in dirty):
            return kept                      # 说了, 而且说在第一行
    return kept + [f"{'、'.join(dirty)} 没落到主干目录上(护住了我没提交的改动),"
                   f" 而合入的回执第一行一个字没提 —— 我只会看到测试红了"]


def seed_kept(project) -> list:
    """他没提交完的那一行还在不在, 历史动没动过"""
    out = []
    body = open(f"{project}/pay.py").read() if os.path.exists(f"{project}/pay.py") else ""
    if HALF_DONE.strip() not in body:
        out.append("我自己写了一半没提交的那一行没了 —— 那是最不能丢的东西")
    # 历史不许被改写: 我那两条提交得原样还在
    mine = [x for x in git(project, "log", "--format=%s").splitlines()
            if x in ("先把收款那块搭起来", "记两笔待办")]
    if len(mine) != 2:
        out.append(f"我原来那两条提交只剩 {len(mine)} 条 —— 历史被改写了")
    return out


def clean_one(tok, project):
    """只清这个目录 —— **不动别人**. 另一摊正在开工, 清人就把它清没了"""
    shutil.rmtree(project, ignore_errors=True)
    os.makedirs(project, exist_ok=True)


def clean(tok, project):
    """
    清场 —— **只清这个场景自己那块**.

        原来是把整个试验场 rmtree 掉. 两个后果, 都在真机上撞到了:
        跑完的场景没法回头查(下一轮一起来产物就没了), 而"接着上一摊
        往下做"那个场景**注定拿不到上一摊** —— 它要的正是那份被删掉的
        东西.

        人也不清空硬盘再开工. 清的是"这一摊", 不是"所有摊".
    """
    # **/processes 只列还活着的**.
    #
    #   已经退出、进程表里清掉了的那些, 账本里的记录还在 —— 重启一次
    #   客户端, 侧栏就把它们全画回来. 记录可累积到 12 间测试屋子:
    #   三人抢同一个文件、两人做前端、烧到上限…… 次日打开时看到的
    #   就是这个.
    #
    #   "清掉所有会话记录"说的是账本里出现过的每一个, 不是此刻还在的那几个.
    api(tok, "/forget", {"pids": every_pid(tok)})
    shutil.rmtree(project, ignore_errors=True)
    os.makedirs(project, exist_ok=True)


def every_pid(tok) -> list:
    """账本里出现过的每一个进程 —— 不只是此刻还在的那几个"""
    seen = {p["pid"] for p in api(tok, "/processes") if isinstance(p, dict)}
    raw = urllib.request.urlopen(f"{OS_URL}/stream?token={tok}")
    for line in raw:
        t = line.decode("utf8", "replace").rstrip()
        if t.startswith(": live"):
            break
        if not t.startswith("data:"):
            continue
        pid = json.loads(t[5:]).get("pid")
        if pid:
            seen.add(pid)
    return sorted(seen)


def latest_stage():
    """最近跑过的那一摊 —— "接着往下做"要接的就是它"""
    if not os.path.isdir(STAGE):
        return None
    dirs = [f"{STAGE}/{d}" for d in os.listdir(STAGE) if os.path.isdir(f"{STAGE}/{d}")]
    return max(dirs, key=os.path.getmtime) if dirs else None


def who_by_pid(tok, crew) -> dict:
    """pid → 谁 —— **每一次化身都算上**, 见 all_pids"""
    want = {((p.get("spec") or {}).get("labels") or {}).get("name") for p in crew}
    want.discard(None)
    out = {p["pid"]: ((p.get("spec") or {}).get("labels") or {}).get("name") for p in crew}
    for p in api(tok, "/processes"):
        if not isinstance(p, dict):
            continue
        got = ((p.get("spec") or {}).get("labels") or {}).get("name")
        if got in want:
            out[p["pid"]] = got
    return out


def all_pids(tok, crew) -> set:
    """
    **这几个人的每一次化身**.

        按开跑时那份 pid 名单筛事件, 会漏掉重开之后的那个进程 ——
        而"半路喊停"正是把人停掉再起回来: pid 换了, 人没换.
        真机上量出来的是 said: [] —— 那一轮它明明说了话、活也落地了.

        这跟 /stop 当初那条是同一课: **按名字不按 pid**. 一个 bot
        跨多次重启有多个 pid, 而人看到的自始至终是同一个人.
    """
    want = {((p.get("spec") or {}).get("labels") or {}).get("name") for p in crew}
    want.discard(None)
    out = {p["pid"] for p in crew}
    for p in api(tok, "/processes"):
        if not isinstance(p, dict):
            continue
        if ((p.get("spec") or {}).get("labels") or {}).get("name") in want:
            out.add(p["pid"])
    return out


def events_since(tok, since, pids):
    """
    这一轮里这几个人身上发生的事 —— **只要这一轮的**.

        真机上撞出来的样子: "接着上一摊往下做"接的是上一摊, 人也是同一个.
        而事件流一放就是从头开始, 于是上一轮 `烧到上限` 留下的那条
        "到止损线了"被当成这一轮的问题报了出来 —— 一条查不实的问题会
        把真正的问题淹掉, 而它看起来跟真的一模一样.

        账本里每条都带着 at(毫秒), 拿开跑那一刻当界就行.
    """
    out = []
    raw = urllib.request.urlopen(f"{OS_URL}/stream?token={tok}")
    for line in raw:
        t = line.decode("utf8", "replace").rstrip()
        if t.startswith(": live"):
            break
        if not t.startswith("data:"):
            continue
        ev = json.loads(t[5:])
        if ev.get("pid") not in pids:
            continue
        if since and (ev.get("at") or 0) < since:
            continue
        out.append(ev)
    return out


def card_shown(tok, crew, want, since=0) -> list:
    """
    **它发的卡片, 渲染端认不认**.

        真机上的失败方式: 界面对不认识的卡片种类**静默丢弃** —— 工具
        报的是"已展示", 模型接着说"如上表所示", 而人那边什么都没有.
        后来改成"认不出就照字段画一张通用卡", 但那条路一直没人验过.

        判据只看**发出去的那一份**够不够渲染端画: 有 type、有 id、
        该有的内容在. 缺 type 或 id 的卡会被丢掉, 而工具照样报成功.
    """
    cards = []
    for ev in events_since(tok, since, all_pids(tok, crew)):
        pl = ev.get("payload") or {}
        if pl.get("channel") != "ui":
            continue
        try:
            cards.append(json.loads(pl.get("text") or "{}"))
        except json.JSONDecodeError:
            return ["发出去的卡片不是合法 JSON —— 渲染端会整张丢掉"]
    if not cards:
        return ["让它摆个表, 一张卡片都没发出来"]

    out = []
    for c in cards:
        # **这两样缺一张卡就没了**: 渲染端拿 type 挑怎么画, 拿 id 认回答
        if not c.get("type"):
            out.append(f"发出去的卡片没有 type，会被丢掉: {tail(json.dumps(c, ensure_ascii=False), 90)}")
        if not c.get("id"):
            out.append("发出去的卡片没有 id —— 界面认不回它")
    blob = json.dumps(cards, ensure_ascii=False)
    missing = [w for w in want if w not in blob]
    if missing:
        out.append(f"卡片里没有 {'、'.join(missing)} —— 摆出来的表跟他说的数对不上")
    return out


def saw_it(tok, crew, want, since=0) -> list:
    """
    **它到底看没看见那张图**.

        这条路真机上坏过一次: 界面把图收下了、存进了 blob、时间线上也
        画出来了, 而**送给模型的那一份里根本没有图** —— 它照样答得头头
        是道("看起来像一张截图"), 跟真看见了长得一模一样.

        所以判据不能问"支不支持", 要问**答没答对**. 图上画两位数字,
        蒙中的概率 1/100.
    """
    said = []
    for ev in events_since(tok, since, all_pids(tok, crew)):
        pl = ev.get("payload") or {}
        if pl.get("phase") in ("reply", "done"):
            said.append(str(pl.get("text") or pl.get("summary") or ""))
    if not said:
        return ["发了图, 它一句话都没回"]
    last = said[-1]
    if want in last:
        return []
    # **说不出来跟说错了要分开**: 前者是这台机器没配视觉(该说清楚),
    #   后者是图送到了但它看不懂(那是模型的事) —— 两种都不是"通过".
    if any(w in last for w in ("看不到", "没有图", "不支持", "看不了", "没收到")):
        return [f"图发过去了, 它说自己看不到: {tail(last, 90)}"]
    return [f"图上是 {want}, 它答的是: {tail(last, 90)}"]


def on_screen(since=0) -> list:
    """界面上有没有出事 —— 见 screen.py. 客户端没开不算界面的问题"""
    here = os.path.dirname(os.path.abspath(__file__))
    r = subprocess.run([sys.executable, f"{here}/screen.py", "--since", str(since)],
                       capture_output=True, text=True, timeout=60)
    if r.returncode == 0:
        return []
    return ["界面: " + tail(r.stdout.strip().replace("\n", " "), 160)]


def cache_kept(tok, crew, since) -> tuple:
    """这一轮送进去多少、其中命中缓存多少 —— 见 cold_prompts"""
    sent = hit = 0
    for ev in events_since(tok, since, all_pids(tok, crew)):
        pl = ev.get("payload") or {}
        if pl.get("phase") != "usage":
            continue
        sent += int(pl.get("prompt") or 0)
        hit += int(pl.get("cached") or 0)
    return sent, hit


def cold_prompts(sent, hit) -> list:
    """
    **前缀缓存塌了, 每一轮的钱翻十倍**.

        长会话能撑住全靠前缀缓存: 真机上十轮下来送进去 43 万 token,
        其中 99% 是命中的, 真正新算的只有两三千.

        而缓存认的是**前缀一模一样**: 提示词里多一个时间戳、工具表顺序
        变一下、系统那句话每轮不同 —— 命中率当场归零, 而别的一切照旧:
        活照样干完, 测试照样绿, 只是每一轮的钱翻好几倍.

        这是这套东西最贵的一种静默故障, 而在此之前没有任何判据看它.
    """
    if sent < 50_000:
        return []                    # 太短的一轮, 缓存本来就还没建起来
    rate = hit / sent
    if rate < 0.7:
        return [f"前缀缓存塌了：送进去 {sent // 1000}k，只命中 {int(rate * 100)}%"
                f" —— 每一轮的钱要翻好几倍"]
    return []


def keeps_up(tok, crew, asks, utterance, wait) -> list:
    """
    **连着干十来轮之后, 每一轮是不是越来越贵**.

        别的场景都只跑一两分钟, 而人明天是连着干几个小时. 只在时间拉长
        之后才露头的那些 —— 上下文不折叠、历史越堆越长、每一轮把前面
        所有的话重新读一遍 —— 一两分钟的场景一条都碰不到.

        判据是**后面几轮跟头几轮比**: 活是一样大的(都是"再加一个小功能"),
        所以每轮的花费本该在一个水平上. 后面比前头贵一大截, 说明背的
        包袱在涨, 而那正是长会话变慢变贵的样子.

        不设绝对值 —— 跟它自己的头几轮比.
    """
    one = crew[0]
    # 头一句还在干着 —— 得等它干完再说下一句, 否则就是打断而不是"接着说"
    wait_idle(tok, wait, gap=3)
    each = []
    for i, ask in enumerate(asks):
        at = int(time.time() * 1000)
        api(tok, "/say", {"pid": one["pid"], "text": ask, "said": ask,
                          "from": "我", "utterance": f"{utterance}-{i}"})
        if not wait_idle(tok, max(90, wait // 2), gap=3):
            return [f"第 {i + 2} 轮没干完就超时了：{ask}"]
        each.append(spent_in_round(tok, crew, at))

    real = [n for n in each if n > 0]
    if len(real) < 6:
        return []                       # 轮数太少, 比不出趋势
    head = sorted(real[:3])[1]          # 各取中位, 别被某一轮的抖动带偏
    tail_ = sorted(real[-3:])[1]
    if head and tail_ > head * 2.5:
        return [f"越干越贵：头几轮每轮 {head // 1000}k，后几轮 {tail_ // 1000}k —— "
                f"背的包袱在涨"]
    return []


def spent_in_round(tok, crew, since) -> int:
    """
    **这一轮花了多少** —— 自己数, 不借 /spend 那本账.

        第一版是开跑前后各问一次 /spend 相减, 结果第一次跑就量出个
        **负数**: -23704. 花费不可能是负的.

        原因: /spend 是按**现存的 bot** 现算的, 而每个场景开跑前都会把
        上一摊的人清掉 —— 他们的花费跟着一起没了, 于是合计往回缩.
        (那个行为本身是对的: 花费是按人看的, 人没了那一栏也就没了.)

        这一轮的账要自己数: 账本里每次推理都有一条 usage, 上面写着
        送进去多少、吐出来多少. 按这一轮、按这几个人筛, 加起来就是.
    """
    total = 0
    for ev in events_since(tok, since, all_pids(tok, crew)):
        pl = ev.get("payload") or {}
        if pl.get("phase") != "usage":
            continue
        # **命中缓存的那部分不是新花的钱**.
        #
        #   连续十轮运行时, 送入 token 可从 9 万涨到 43 万, 表面上是
        #   "越干越贵" —— 而其中 **99% 是缓存命中的**, 每轮真正新送进去
        #   的一直是两三千. 拿原始 prompt 当花费虚高十倍, 把一个健康的
        #   会话判成了有毛病.
        #
        #   判据要认的是"新花了多少", 不是"送了多大一坨".
        total += (int(pl.get("prompt") or 0) - int(pl.get("cached") or 0)
                  + int(pl.get("completion") or 0))
    return total


def too_dear(name, spent) -> list:
    """
    **这一轮是不是突然变贵了**.

        十一条不变量查的全是"对不对", 没有一条查"贵不贵". 而一个让每
        一轮都翻倍烧钱的改动 —— 提示词长了、上下文没折叠、工具表膨胀 ——
        **一条都不会响**: 活照样干完, 测试照样绿, 只是每次多花一倍的钱.

        判据跟这个场景**自己的历史**比, 不设绝对值: 三人抢同一个文件
        本来就比一个人干小活贵十几倍, 拿同一个数卡它们没有意义.

        少于三次的历史不判 —— 两三个点的中位数说明不了什么.
    """
    if not spent or not os.path.exists(LOG):
        return []
    past = []
    for line in open(LOG):
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if row.get("name") == name and row.get("spent"):
            past.append(int(row["spent"]))
    if len(past) < 3:
        return []
    mid = sorted(past)[len(past) // 2]
    if spent > mid * 2:
        return [f"这一轮花了 {spent // 1000}k，是它平常({mid // 1000}k)的两倍多"]
    return []


def set_cap(tok, tokens):
    """一轮最多烧多少 —— 0 = 不限(缺省)"""
    cfg = api(tok, "/provider")
    cfg["capTokens"] = tokens
    api(tok, "/provider", cfg)


def capped_well(tok, crew, project, since=0) -> list:
    """
    **撞上限要收好摊子再说话**.

        用户点过名的一条: "花费上限可以在设置，默认没有上限". 而上限这
        件事真正的难处不在拦住, 在**拦住之后**:

          · 默默死掉 —— 界面上就是一个停在半路的头像, 人不知道发生了
            什么, 更不知道该加预算还是另起一段
          · 手上的活丢了 —— 半成品只在工作区里, 没收进提交就等于没有

        判据**认事实, 不认措辞**. 上一版要求那段交代里出现"上限"或
        "预算", 而真机上它说的是"这一轮到了止损线" —— 一段写得相当
        完整的交代被判成"一句话都没留下". 系统自己有好几种说法, 判据
        跟着措辞走就永远在追.

        事实是: 那条收尾事件带着 hitLimit, 而且它真说了一段话.
    """
    names = who_by_pid(tok, crew)
    hit, account = False, {}
    for ev in events_since(tok, since, set(names)):
        pl = ev.get("payload") or {}
        if pl.get("phase") == "step_limit":
            hit = True
        # 撞线之后那段交代走的是 done + summary(见 agent.finalSummary),
        # 不是 reply —— 只盯着 reply 的话一个字都收不到
        if pl.get("phase") == "done" and pl.get("hitLimit"):
            hit = True
            account[ev["pid"]] = str(pl.get("summary") or "")

    if not hit:
        return ["设了上限却一次都没撞上 —— 这一轮验不出撞上限之后什么样"]

    out = []
    talked = [t for t in account.values() if len(t) > 80]
    if not talked:
        out.append("撞了上限, 它一句像样的交代都没留下 —— 界面上就是个停在半路的头像")
    for t in talked:
        for robot in ("budget exceeded", "ErrBudget", "context deadline", "panic:"):
            if robot in t:
                out.append(f"撞上限那句话是机器话, 人看不懂: {tail(t, 90)}")
                break

    # **手上的活不许丢**: 半成品只在工作区里, 没收进提交就等于没有.
    #   (合没合上去不算 —— 被拦腰砍断的一轮本来就该停在自己分支上,
    #    活好好地躺在那儿正是我们要的.)
    report = api(tok, "/project?path=" + urllib.parse.quote(project))
    for m in report.get("members") or []:
        if m.get("dirty", 0) > 0:
            out.append(f"{m['bot']} 撞上限之后手上还压着没收进提交的改动 —— 那部分等于丢了")
    return out


def undo_last(tok, project) -> list:
    """
    **合上去的东西要撤得回来**.

        项目卡上给的承诺是"每一条都能撤". 而撤销这条路上能出的岔子,
        没有一个是在报错里能看见的:

          · 撤完主干没回到上一版(说撤了, 其实没动)
          · 撤的时候把人手写的、还没提交的东西一起冲掉了
          · 撤完之后仓库处在半路状态(revert 冲突挂在那儿)

        判据就查这三样. 怎么撤是宿主的事(revert 还是别的), 不规定.
    """
    report = api(tok, "/project?path=" + urllib.parse.quote(project))
    merges = report.get("merges") or []
    if not merges:
        return ["主干上一次合并都没有 —— 这一轮撤不了, 也就验不出撤不撤得回来"]

    top = merges[0]
    before = set(git(project, "ls-files").splitlines())
    keep = open(f"{project}/我的草稿.txt").read() if os.path.exists(f"{project}/我的草稿.txt") else None

    try:
        api(tok, "/revert", {"project": project, "hash": top.get("hash")})
    except urllib.error.HTTPError as e:
        return ["撤不回来: " + e.read().decode("utf8", "replace")[:120]]

    after = set(git(project, "ls-files").splitlines())
    out = []
    if after == before:
        out.append(f"说撤了 {top.get('hash')}, 主干一个文件都没变")
    if keep is not None:
        now = open(f"{project}/我的草稿.txt").read() if os.path.exists(f"{project}/我的草稿.txt") else None
        if now != keep:
            out.append("撤的时候把我那份没提交的东西也冲掉了")
    # 撤完不许把仓库留在半路上 —— 那时候谁也不能接着干
    state = git(project, "status", "--porcelain=v1", "-b")
    if "REVERTING" in git(project, "status") or "UU " in state:
        out.append("撤完仓库停在半路上(还挂着没解的冲突)")
    return out


def handover(tok, one, after, to, utterance) -> list:
    """
    **干到一半换个人接手**.

        用户点过名的一条: "交接的项目卡可以搞一个地方存储显示出来".
        而交接跟交办不是一回事: 交办是"这件事你做, 我还在", 交接是
        "这摊活归你了, 我退出" —— 后者要把分支、进度、没做完的部分一起
        给过去, 少一样接手的人就得从头猜.

        说法要像人说的: 不点工具名, 不写步骤. 它自己该知道用 hand_over.
    """
    # **时机是条件, 不是秒数** —— 跟喊停同一个道理(见 wait_running).
    #   写死 90 秒时, 工作可能在此前就已完成并合入, 交出去的是
    #   一摊已经完工的活: 接手方没有内容可接, 这条路等于没验.
    who = ((one.get("spec") or {}).get("labels") or {}).get("name") or one["pid"]
    if not wait_running(tok, who, after):
        return ["一直没逮到它正在干活的时候 —— 这一轮验不出接不接得上"]
    api(tok, "/say", {"pid": one["pid"],
                      "text": f"我这边临时有别的事找你，这摊你交给{to}吧。",
                      "said": f"我这边临时有别的事找你，这摊你交给{to}吧。",
                      "from": "我", "utterance": utterance + "-h"})
    return []


def handed_over(tok, project, to) -> list:
    """交接得**留下痕迹** —— 项目卡上看不见的交接等于没发生过"""
    report = api(tok, "/project?path=" + urllib.parse.quote(project))
    got = report.get("handoffs") or []
    if not any(h.get("to") == to for h in got):
        return [f"说了交给{to}, 项目卡上一条交接记录都没有"]
    return []


def brake(tok, one, after, said, utterance) -> list:
    """
    **干到一半喊停, 得真停得住**.

        用户点过名的一条: "你需要在 BOT 上增加终止它的能力". 而"能终止"
        这件事只有在**它正干得起劲的时候**才验得出来 —— 停一个已经停下的
        人是不算数的.

        停完再说一句"接着干", 后面那些不变量(有没有产出、合没合上去)
        照旧生效 —— 停得住但停完就废了, 那不叫停得住.
    """
    who = ((one.get("spec") or {}).get("labels") or {}).get("name") or one["pid"]
    if not wait_running(tok, who, after):
        return ["一直没逮到它正在干活的时候 —— 这一轮验不出停不停得住"]
    api(tok, "/stop", {"name": who})
    for _ in range(12):
        time.sleep(5)
        if running(tok, who):
            continue
        # 停住了. **停完会换一个新进程**(它会带着记忆回来), 所以按名字找,
        # 不能拿停之前那个 pid 说话.
        pid = pid_of(tok, who)
        if pid:
            api(tok, "/say", {"pid": pid, "text": "接着干吧。", "said": "接着干吧。",
                              "from": "我", "utterance": utterance + "-2"})
            return []
        return ["停是停了, 但人没回来 —— 停完就废了不叫停得住"]
    return ["喊了停, 60 秒后它还在跑"]


def wait_running(tok, who, most) -> bool:
    """
    **逮它正在干活的那一刻**.

        原来是 time.sleep(75) 到点就喊. 真机上小辛 74 秒就把通讯录写完
        合上去了, 于是喊停喊在一个已经停下的人身上 —— 这条路一次都没
        验到, 而报出来只是一句"验不出".

        时机是**条件**, 不是秒数: 等到它在跑, 再给一小段让它真进到活里,
        然后就是现在. 活干得快慢会变, 判据不能挂在秒数上.
    """
    deadline = time.time() + most
    settled = None
    while time.time() < deadline:
        if running(tok, who):
            if settled is None:
                settled = time.time() + 20      # 让它从"读文件"进到"写东西"
            elif time.time() >= settled:
                return True
        else:
            settled = None                       # 停了就重新等下一个窗口
        time.sleep(2)
    return running(tok, who)


def alive(tok, who):
    """这个名字下现在还活着的进程"""
    return [p for p in api(tok, "/processes")
            if ((p.get("spec") or {}).get("labels") or {}).get("name") == who
            and p["state"] != "exited"]


def running(tok, who):
    return any(p["state"] == "running" for p in alive(tok, who))


def pid_of(tok, who):
    got = alive(tok, who)
    return got[0]["pid"] if got else None


def slow_and_silent(tok, crew, secs, since=0) -> list:
    """
    ⑧ **长活不许一声不吭**.

        用户点过名的一条: "长活中中间的进度, 你要想办法…你可以记录他
        第多少轮". 判据只看结果: 跑了这么久, 中途总得有过一次看得见的
        进展 —— 落一次提交, 或者发一张进度卡. 一次都没有的话, 人盯着
        一个转圈的头像等了十分钟, 不知道它是在干活还是卡死了.

        怎么报是它的事(提交、进度卡、说一句), 这里不规定.
    """
    if secs < 300:
        return []                       # 短活不用查
    names = who_by_pid(tok, crew)
    last, worst = {}, {}
    for ev in events_since(tok, since, set(names)):
        pid, at = ev.get("pid"), ev.get("at") or 0
        if pid in last:
            gap = (at - last[pid]) / 1000
            if gap > worst.get(pid, 0):
                worst[pid] = gap
        last[pid] = at
    dead = [f"{names[pid]} 静了 {int(g)} 秒" for pid, g in sorted(worst.items()) if g > QUIET_MOST]
    if dead:
        return [f"跑了 {secs} 秒，中途没动静：{'、'.join(dead)} —— 界面上就是一直转圈"]
    return []


def wait_idle(tok, most, gap=10):
    """
    等所有人停手. 顺便替人拍板 —— 装依赖那类申请, 人在的话本来就会批.

        **"还没开始"跟"已经干完"长得一模一样**: 话刚投出去那一刻谁都
        没在跑, 照着"没人 running 就是干完了"判, 当场就返回了.

        真机上"一直干下去"栽在这儿: 十句话本该一句一轮, 结果几秒钟内
        全投了出去、互相打断, 45 秒收工, 主干上一个文件都没多.

        所以要**先看见它动起来**, 再谈停没停手.
    """
    deadline = time.time() + most
    started, quiet = False, 0
    while time.time() < deadline:
        time.sleep(gap)
        approve(tok)
        busy = any(p["state"] == "running" for p in api(tok, "/processes"))
        if busy:
            started, quiet = True, 0
            continue
        quiet += 1
        if started:
            return True
        # **一直没动过也得有个了断**: 这个函数也用在"现在都闲着吗"上
        #   (十轮跑完之后那一次). 死等"先看见它动起来"的话, 那一次永远
        #   等不到, 于是报一句"没干完" —— 而它明明干完了.
        if quiet >= 3:
            return True
    return False


def approve(tok):
    """把还没拍板的都批了 —— 场景要的是"人在旁边点头"那种常态"""
    live = {p["pid"] for p in api(tok, "/processes") if p["state"] in ("waiting", "running", "created")}
    openq = {}
    raw = urllib.request.urlopen(f"{OS_URL}/stream?token={tok}")
    for line in raw:
        s = line.decode("utf8", "replace").rstrip()
        if s.startswith(": live"):
            break
        if not s.startswith("data:"):
            continue
        ev = json.loads(s[5:])
        pl = ev.get("payload") or {}
        did, pid = pl.get("did"), ev.get("pid")
        if not did or pid not in live:
            continue
        if ev["kind"] == "decide.requested":
            openq[(pid, did)] = True
        elif ev["kind"] == "decide.resolved":
            openq.pop((pid, did), None)
    for pid, did in openq:
        api(tok, "/decide", {"did": did, "pid": pid, "choice": "yes"})
    return len(openq)


def play(sc, tok):
    started = time.time()
    # **只看这一轮的事** —— 账本里每条都带着 at(毫秒). 见 events_since:
    #   "接着上一摊往下做"接的是上一摊、人也是同一个, 不划这条线的话
    #   上一轮留下的问题会被当成这一轮的报出来.
    since = int(started * 1000)

    if sc.get("reuse"):
        # 接着上一摊往下做 —— 接的是最近跑过的那个, 人也不会凭空接一摊
        project = latest_stage()
        if project is None:
            return {"name": sc["name"], "bad": ["没有上一摊可接 —— 先跑一个别的场景"], "secs": 0}
    else:
        project = f"{STAGE}/{sc['name']}"
        clean(tok, project)
        open(f"{project}/我的草稿.txt", "w").write(SENTINEL)  # 哨兵: 人的东西
        if sc.get("seed"):
            seed_project(project)
        for name, role in sc["crew"]:
            api(tok, "/create", {"name": name, "work": project,
                                 "thread": "#" + os.path.basename(project), "role": role})
        time.sleep(5)

    # 接续场景每次换一件事问 —— 按这摊活已经被加过几回来挑, 不靠随机
    if sc.get("says"):
        rounds = len([x for x in git(project, "log", "--format=%an").splitlines()
                      if x != "NeoxOS"])
        sc = dict(sc, say=sc["says"][rounds % len(sc["says"])])

    before = git(project, "rev-parse", "HEAD").strip() if sc.get("reuse") else ""
    room = "#" + os.path.basename(project)
    crew = [p for p in api(tok, "/processes")
            if ((p.get("spec") or {}).get("labels") or {}).get("thread") == room]
    if not crew:
        return {"name": sc["name"], "bad": ["一个人都没起来"], "secs": 0}
    # **交接那个场景只对第一个人说**: "@所有人"发出去的话, 接手的人
    #   一开始就在场、也已经动上手了 —— 那就不是交接, 是两个人一起干.
    talk_to = crew[:1] if sc.get("hand_at") else crew
    said = "@所有人 " + sc["say"] if len(talk_to) > 1 else sc["say"]
    u = "sc-" + str(int(time.time()))
    # **一次说给一屋子人**: "还发给了谁、谁拆活"那一段由 OS 加 ——
    # 它是协议不是客户端的事(见 osinit/fanout.go). 一个个投的话,
    # 收到的就是一句光秃秃的话, 于是两个人各搭一套脚手架.
    shot = []
    if sc.get("shows"):
        shot = [{"name": "图.png",
                 "data": base64.b64encode(digits_png(sc["shows"])).decode()}]
    if len(talk_to) > 1:
        api(tok, "/say", {"pids": [p["pid"] for p in talk_to], "text": said, "said": said,
                          "from": "我", "utterance": u, "images": shot})
    else:
        api(tok, "/say", {"pid": talk_to[0]["pid"], "text": said, "said": said,
                          "from": "我", "utterance": u, "images": shot})

    bad = []
    if sc.get("beside"):
        beside = start_beside(tok, sc["beside"], u)
    if sc.get("cap"):
        set_cap(tok, sc["cap"])
    if sc.get("then"):
        # 连着说十句, 一句一轮 —— 见 keeps_up
        bad += keeps_up(tok, crew, sc["then"], u, sc["wait"])
    if sc.get("hand_at"):
        bad += handover(tok, crew[0], sc["hand_at"], sc["hand_to"], u)
    if sc.get("stop_after"):
        bad += brake(tok, crew[0], sc["stop_after"], said, u)
    done = wait_idle(tok, sc["wait"])
    bad += check(project, crew, tok, since)
    # **第二轮该比的是"主干有没有前进", 不是"有没有文件"**.
    #
    #   ⓪ 查的是"主干上一个文件都没多" —— 接着上一摊往下做的时候, 上一轮
    #   留下的文件本来就在那儿, 于是这一轮一个字没干它也不响. 那正是假绿:
    #   空转的样子跟通过一模一样.
    if sc.get("reuse") and before and git(project, "rev-parse", "HEAD").strip() == before:
        bad.append("接着往下做, 主干却一步没动 —— 这一轮什么都没加上去")
    bad += slow_and_silent(tok, crew, int(time.time() - started), since)
    # **界面也得看一眼**: 前面所有判据走的都是 HTTP 那条口子, 数据全对
    #   而渲染进程崩了的话它们一个字都看不见. 见 screen.py
    bad += on_screen(since)
    bad += talks_like_people(tok, crew, since)
    if sc.get("beside"):
        bad += apart(project, beside)
    if sc.get("seed"):
        bad += seed_kept(project)
        bad = told_about_held(tok, crew, project, since, bad)
    if sc.get("card"):
        # 摆张表这一轮本来就不该产出文件
        bad = [b for b in bad if "都没落地" not in b and "没合的" not in b
               and "没有任何可验的东西" not in b]
        bad += card_shown(tok, crew, sc["card"], since)
    if sc.get("shows"):
        # 光看图这一轮本来就不该产出文件, ⓪④②在这儿说的不是问题
        bad = [b for b in bad if "都没落地" not in b and "没合的" not in b
               and "没有任何可验的东西" not in b]
        bad += saw_it(tok, crew, sc["shows"], since)
    # **产出要在撤销之前数**: 撤完主干本来就该回到开局那几个文件,
    #   在那之后数出来是 files:3 —— 跟"什么都没干"长得一模一样.
    made = len(git(project, "ls-files").splitlines())
    if sc.get("cap"):
        # **被拦腰砍断的一轮, 活停在自己分支上正是我们要的**.
        #   ⓪("主干上一个文件都没多")和④("手上还压着没合的")在别的场景
        #   里是坏事, 在这儿是这条路本来的样子 —— 拿它们当问题报, 真正
        #   的问题(交代没说清、活丢了)就被淹在噪音里.
        #   ⑦("这一轮停下来了: 到止损线了")同理: 那条线是这个场景**自己
        #   设的**, 撞上正是它的全部目的. 撞完之后处理得好不好, 由
        #   capped_well 单独判.
        bad = [b for b in bad if "都没落地" not in b and "没合的" not in b
               and "止损线" not in b]
        bad += capped_well(tok, crew, project, since)
        set_cap(tok, 0)                      # 别把上限留给下一个场景
    if sc.get("undo"):
        bad += undo_last(tok, project)
    if sc.get("hand_at"):
        bad += handed_over(tok, project, sc["hand_to"])
    if not done:
        bad.append(f"{sc['wait']}秒还没干完")
    spend = api(tok, "/spend")["total"]
    spent = spent_in_round(tok, crew, since)
    # **先量, 再说要不要立规矩**.
    #
    #   交办那条有用户的截图当证据, 所以直接改了. 汇报的话有多长, 我手上
    #   一个数都没有 —— 那就先记下来. 攒几轮之后要么看出问题, 要么证明
    #   它本来就没事; 现在拍一个上限只是我自己想当然.
    talk = reply_sizes(tok, crew, since)
    bad += cold_prompts(*cache_kept(tok, crew, since))
    bad += too_dear(sc["name"], spent)
    return {
        "name": sc["name"], "bad": bad, "secs": int(time.time() - started),
        "spent": spent, "said": talk,
        "tokens": spend["prompt"] + spend["completion"],
        "files": made,
    }


def wrap_up(rows):
    """全跑完的那张表 —— **一眼看出还剩什么**"""
    print()
    print(f"{'场景':<16}{'用时':>6}{'新花的钱':>10}  问题")
    bad_total = 0
    for r in rows:
        n = len(r.get("bad") or [])
        bad_total += n
        mark = "·" if n == 0 else f"{n} 条"
        print(f"{r['name']:<16}{r.get('secs', 0):>5}s{r.get('spent', 0) // 1000:>9}k  {mark}")
        for b in (r.get("bad") or []):
            print(f"{'':<16}{'':>16}  - {tail(b, 110)}")
    print()
    print(f"{len(rows)} 个场景，{bad_total} 条问题。" if bad_total else
          f"{len(rows)} 个场景全过。")


def main():
    what = sys.argv[1] if len(sys.argv) > 1 else "rotate"
    if what == "list":
        for i, sc in enumerate(SCENARIOS):
            print(f"{i}. {sc['name']:<14} {sc['why']}")
        return
    tok = token()
    if not tok:
        print("连不上 —— 客户端没开?")
        return
    if what == "run":
        picked = [s for s in SCENARIOS if s["name"] == sys.argv[2]]
    elif what == "all":
        # **全跑一遍** —— 改完东西之后要的是"现在就把十六条路全过一遍",
        #   而不是每 15 分钟一条、等一下午.
        #
        #   接续那个要排在别人后面 —— 它接的是"最近跑过的那一摊".
        picked = list(SCENARIOS)
    else:
        state = json.load(open(STATE)) if os.path.exists(STATE) else {"next": 0}
        i = state["next"] % len(SCENARIOS)
        picked = [SCENARIOS[i]]
        json.dump({"next": i + 1}, open(STATE, "w"))
    done = []
    for sc in picked:
        try:
            got = play(sc, tok)
        except Exception as e:
            # **测试台自己崩了, 也得留下一行**.
            #
            #   它是每 15 分钟无人值守跑的. 崩一次的话日志里只是少了一行,
            #   看起来跟"还没跑"一模一样 —— 每一轮只能看到那一行 JSON.
            #   改场景时一段代码插错位置会触发 UnboundLocalError, 使该轮
            #   没有任何记录.
            #
            #   崩了不是"没问题", 崩了本身就是这一轮最大的问题.
            import traceback  # noqa: PLC0415
            got = {"name": sc["name"], "bad": ["测试台自己崩了: " + tail(str(e), 120)],
                   "secs": 0, "trace": tail(traceback.format_exc().replace("\n", " "), 300)}
        finally:
            # **上限一定要还回去**.
            #
            #   这台测试台是无人值守跑的: 中途崩一次(网络、客户端退了),
            #   那个 40k 就留在设置里, 之后每一个场景都被悄悄砍断 ——
            #   而"干到一半不干了"恰恰是最难认出原因的那副样子.
            if sc.get("cap"):
                try:
                    set_cap(tok, 0)
                except Exception:
                    print("！上限没还回去, 手动改一下设置里的每轮上限")
        got["at"] = time.strftime("%m-%d %H:%M")
        line = json.dumps(got, ensure_ascii=False)
        print(line)
        with open(LOG, "a") as f:      # 跨轮次留痕: 同一条不变量反复响就是模式
            f.write(line + "\n")
        done.append(got)

    if len(done) > 1:
        wrap_up(done)


if __name__ == "__main__":
    main()
