#!/usr/bin/env python3
"""
selftest —— **证明每一条判据真的会响**.

── 为什么非有不可 ──

    这台测试台过去几天里, **判据自己的毛病比产品的毛病还多**:

      ⓪ 一个文件都没多   git 把中文路径转成八进制转义, 哨兵被算成产出,
                        这条**一直在空转** —— 一轮 25 秒什么都没干, 报的是 bad:[]
      ⑤ 提交标题是套话    要 >3 条提交才判, 于是一份三条全是套话的记录一声不吭
      ⑦ 谁卡住过         理由读的是 why/detail, 而账本里那个字段叫 msg

    三条的共同点: **空转的样子跟通过一模一样**. 而它们恰恰是用来挡
    "假绿"的 —— 一条不会响的判据比没有更糟, 它让人以为查过了.

    所以每加一条判据, 就在这儿放一份**故意弄坏的样本**, 看它响不响.
    这份东西不连客户端, 一秒钟跑完, 改判据的时候顺手跑一次.

用法: ./selftest.py
"""
import json
import os
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import run  # noqa: E402

FAILED = []


def check(name, ok, detail=""):
    print(("  ✓ " if ok else "  ✗ ") + name + ("" if ok else "  <- " + detail))
    if not ok:
        FAILED.append(name)


def repo(commits):
    """按 [(谁, 标题, {路径: 内容 或 None=删})] 造一个仓库"""
    d = tempfile.mkdtemp()
    subprocess.run(["git", "-C", d, "init", "-q"], check=True)
    for who, msg, files in commits:
        for path, body in files.items():
            full = os.path.join(d, path)
            if body is None:
                if os.path.exists(full):
                    os.remove(full)
                continue
            os.makedirs(os.path.dirname(full) or d, exist_ok=True)
            open(full, "w").write(body)
        subprocess.run(["git", "-C", d, "add", "-A"], capture_output=True)
        subprocess.run(["git", "-C", d, "-c", f"user.name={who}", "-c", "user.email=x@y",
                        "commit", "-qm", msg], capture_output=True)
    return d


def main():
    print("判据自检 —— 每条都要在弄坏的样本上响, 在好样本上闭嘴\n")

    # ⓪ 什么都没产出
    print("⓪ 主干上一个文件都没多")
    d = repo([("NeoxOS", "第一份快照", {"我的草稿.txt": run.SENTINEL, ".gitignore": "x"})])
    check("只剩开局那几个 → 响", run.nothing_shipped(run.git(d, "ls-files").splitlines()),
          "中文路径被 git 转义过就会漏 —— 见 git() 里的 core.quotePath")
    d2 = repo([("NeoxOS", "快照", {"我的草稿.txt": run.SENTINEL}),
               ("小甲", "干活", {"main.py": "print(1)"})])
    check("真落了东西 → 闭嘴", not run.nothing_shipped(run.git(d2, "ls-files").splitlines()))

    # ① 垃圾进版本控制
    print("① 版本控制里有垃圾")
    found = run.junk_in(["src/a.py", ".npm/_cacache/x", "node_modules/react/index.js"])
    check("缓存和依赖 → 响", len(found) == 2, str(found))
    check("正经代码 → 闭嘴", "src/a.py" not in found)

    # ⑤ 提交标题是套话
    print("⑤ 提交标题是套话")
    def dumb_ratio(subjects):
        # **调真的那份** —— 测试副本与被测实现分离时, 改了 run.py 这儿照样绿
        subs, dumb = run.boilerplate(subjects)
        return bool(subs) and len(dumb) > len(subs) / 2
    check("三条里两条套话 → 响", dumb_ratio(
        ["同步前把手上的活收一下", "交接前把手上的活收一下", "NeoxOS: 把这摊活收进 git"]),
        "原来要 >3 条才判, 短记录上是瞎的")
    check("说得出话 → 闭嘴", not dumb_ratio(
        ["接手小壬的活", "把手上的活交给小癸", "NeoxOS: 把这摊活收进 git"]))

    # ⑦ 谁卡住过 —— 理由要读得出来
    print("⑦ 卡住的理由")
    check("账本里叫 msg → 读得出", run.reason({"msg": "路径不对"}) == "路径不对",
          "原来只读 why/detail, 于是每条都报成空理由")
    check("真没写理由 → 说清楚", run.reason({}) == "(账本里没写理由)")

    # 重复劳动
    print("⑧ 同一样东西两个人各做一遍")
    d3 = repo([("小甲", "搭脚手架", {"package.json": "甲"}),
               ("小甲", "删掉自己那套", {"package.json": None}),
               ("小乙", "搭脚手架", {"package.json": "乙"}),
               ("小甲", "组件", {"App.jsx": "x"})])
    check("两个人各建一遍 → 响", run.double_built(d3) == ["package.json"], str(run.double_built(d3)))
    d4 = repo([("小甲", "搭脚手架", {"package.json": "甲", "App.jsx": "x"})])
    check("一个人建的 → 闭嘴", run.double_built(d4) == [])

    # ⓪的孪生: 能验的东西一样都没有
    print("② 一样可验的东西都没有")
    tracked = ["README.md", "NOTES.md", "docs/a.md", "b.md"]
    check("全是文档 → 该响", len(run.dirs_with(tracked, "package.json")) == 0
          and not any(f.endswith((".py", ".js")) for f in tracked))

    # 只看这一轮 —— 见 events_since
    print("⑨ 上一轮的旧账不许算到这一轮头上")
    class FakeStream:
        def __init__(self, evs): self.evs = evs
        def __iter__(self):
            for e in self.evs:
                yield ("data:" + json.dumps(e, ensure_ascii=False)).encode()
            yield b": live"
    old = {"pid": "p1", "at": 1000, "payload": {"phase": "step_limit", "msg": "上一轮撞的线"}}
    new = {"pid": "p1", "at": 3000, "payload": {"phase": "reply", "text": "这一轮说的话"}}
    run.urllib.request.urlopen = lambda url: FakeStream([old, new])
    got = run.events_since("t", 2000, {"p1"})
    check("界之前的丢掉", len(got) == 1 and got[0]["at"] == 3000, str(got))
    check("不划界就都算上", len(run.events_since("t", 0, {"p1"})) == 2)
    check("别人的事不算", run.events_since("t", 0, {"p9"}) == [])

    # 界面那条检查自己会不会响 —— 见 screen.py
    print("⑩ 界面记下的错要被看见")
    import screen
    got = {"chars": 4000, "nodes": 500,
           "errors": [{"kind": "error", "text": "TypeError: 读不到 undefined"}]}
    check("界面自己记的那条 → 响", any("读不到" in b for b in screen.verdict(got, [])),
          "写完那天它是空转的: 探针把 __neoxErrors 取回来了, 下面却只遍历 CDP 那份")
    check("CDP 那条 → 也响", any("崩了" in b for b in
                             screen.verdict({"chars": 4000, "nodes": 500, "errors": []}, ["崩了"])))
    check("白屏 → 响", any("白屏" in b for b in
                        screen.verdict({"chars": 3, "nodes": 500, "errors": []}, [])))
    check("好好的 → 闭嘴", screen.verdict({"chars": 4000, "nodes": 500, "errors": []}, []) == [])
    # 界面记的那份会一直攒着 —— 上一轮的旧错不该算到这一轮头上
    two = {"chars": 4000, "nodes": 500, "errors": [
        {"kind": "error", "text": "上一轮的", "at": 1000},
        {"kind": "error", "text": "这一轮的", "at": 3000}]}
    said = screen.verdict(two, [], 2000)
    check("界之前的旧错 → 不算", len(said) == 1 and "这一轮的" in said[0], str(said))

    # 卡片那条 —— 缺 type/id 的卡会被渲染端整张丢掉, 而工具报的是成功
    print("⑪ 发出去的卡片够不够渲染端画")
    run.api = lambda tok, path, body=None: [] if path == "/processes" else {}

    def cards_verdict(cards, want):
        run.events_since = lambda *a, **k: [
            {"pid": "p1", "payload": {"channel": "ui", "text": json.dumps(c, ensure_ascii=False)}}
            for c in cards]
        return run.card_shown("t", [{"pid": "p1"}], want)
    good = [{"type": "table", "id": "c1", "columns": ["月", "数"],
             "rows": [["二月", "230"]]}]
    check("像样的卡 → 闭嘴", cards_verdict(good, ["230", "二月"]) == [], str(cards_verdict(good, ["230", "二月"])))
    check("没 type → 响", any("type" in b for b in
                            cards_verdict([{"id": "c1", "rows": [["二月", "230"]]}], [])))
    check("没 id → 响", any("id" in b for b in
                          cards_verdict([{"type": "table", "rows": [["二月", "230"]]}], [])))
    check("数对不上 → 响", any("对不上" in b for b in cards_verdict(good, ["999"])))
    run.events_since = lambda *a, **k: []
    check("一张都没发 → 响", any("一张卡片都没发" in b for b in
                             run.card_shown("t", [{"pid": "p1"}], [])))

    # 这一轮花了多少 —— 自己数, 不借 /spend 那本账(它会随着人被删而缩水)
    print("⑫ 这一轮花了多少")
    run.events_since = lambda *a, **k: [
        {"pid": "p1", "payload": {"phase": "usage", "prompt": 1000, "completion": 50}},
        {"pid": "p1", "payload": {"phase": "usage", "prompt": 2000, "completion": 30}},
        {"pid": "p1", "payload": {"phase": "reply", "text": "干完了"}},
    ]
    got = run.spent_in_round("t", [{"pid": "p1"}], 0)
    check("送进去 + 吐出来都算上", got == 3080, str(got))
    run.events_since = lambda *a, **k: []
    check("一条 usage 都没有 → 零", run.spent_in_round("t", [{"pid": "p1"}], 0) == 0)

    # 花费突然翻倍 —— 十一条不变量一条都不会响, 因为活照样干完了
    print("⑫ 这一轮是不是突然变贵了")
    import tempfile
    log = tempfile.mktemp()
    with open(log, "w") as f:
        for n in (100_000, 110_000, 90_000, 105_000):
            f.write(json.dumps({"name": "两人做前端", "spent": n}) + "\n")
        f.write(json.dumps({"name": "别的场景", "spent": 5_000_000}) + "\n")
    run.LOG = log
    check("翻了一倍多 → 响", any("两倍" in b for b in run.too_dear("两人做前端", 260_000)))
    check("正常波动 → 闭嘴", run.too_dear("两人做前端", 130_000) == [])
    # **跟自己比, 不跟别人比**: 三人的场景本来就比一个人贵十几倍
    check("别的场景贵不算它头上", run.too_dear("两人做前端", 150_000) == [])
    # 历史太少的时候不判 —— 两三个点的中位数说明不了什么
    with open(log, "w") as f:
        f.write(json.dumps({"name": "新场景", "spent": 1000}) + "\n")
    check("历史不够 → 不判", run.too_dear("新场景", 999_999) == [])

    # 连着干十来轮之后越来越贵 —— 一两分钟的场景一条都碰不到
    print("⑬ 越干越贵")
    def trend(each):
        """把 keeps_up 里那段判断单拎出来验 —— 跟真的那份同一个算法"""
        real = [n for n in each if n > 0]
        if len(real) < 6:
            return []
        head, tail_ = sorted(real[:3])[1], sorted(real[-3:])[1]
        return ["越干越贵"] if head and tail_ > head * 2.5 else []
    check("后面贵一大截 → 响",
          trend([50, 52, 48, 90, 140, 160, 170, 180]) == ["越干越贵"])
    check("一直差不多 → 闭嘴",
          trend([50, 52, 48, 55, 51, 60, 49, 58]) == [])
    # **别被某一轮的抖动带偏**: 中间某一轮特别贵不算趋势
    check("中间抖一下 → 闭嘴",
          trend([50, 300, 48, 52, 51, 55, 49, 53]) == [])
    check("轮数太少 → 不判", trend([50, 500]) == [])

    # "还没开始"跟"已经干完"长得一模一样 —— 见 wait_idle
    print("⑭ 别把'还没开始'当成'已经干完'")
    import itertools
    states = itertools.chain(
        [[{"state": "waiting"}]],                 # 话刚投出去, 还没动
        [[{"state": "running"}]] * 2,             # 动起来了
        itertools.repeat([{"state": "waiting"}]))  # 干完了
    run.api = lambda tok, path, body=None: next(states) if path == "/processes" else {}
    run.time.sleep = lambda n: None
    run.approve = lambda tok: None
    check("先动起来再停 → 算干完", run.wait_idle("t", 100, gap=0) is True)
    # **一直没动过也不能当场就说干完了**: 话刚投出去那一刻正是这个样子.
    #   但也不能死等 —— 这个函数还用在"现在都闲着吗"上(十轮跑完之后那次),
    #   死等的话会报一句"没干完", 而它明明干完了. 所以是"静了几轮才算".
    polls = 0
    def quiet(tok, path, body=None):
        nonlocal polls
        if path == "/processes":
            polls += 1
        return [{"state": "waiting"}]
    run.api = quiet
    check("一直没动过 → 静几轮才算干完",
          run.wait_idle("t", 100, gap=0) is True and polls >= 3,
          f"只查了 {polls} 次就说干完了 —— 话刚投出去那一刻就是这个样子")

    # **场景里写了个字段, 得真有人读**.
    #
    #   加"一直干下去"的时候, 那句 `if sc.get("then")` 插进去的位置对不上,
    #   str.replace 匹配不到就**什么都不做, 也不报错** —— 于是十句跟进
    #   一句都没发出去, 而那一轮报的是 bad:[]. 同一个跟头连栽两次.
    print("⑮ 场景里的每个字段都得有人读")
    src = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "run.py")).read()
    known = {"name", "why", "crew", "say", "wait"}      # play 里直接用的
    dead = []
    for sc in run.SCENARIOS:
        for key in sc:
            if key in known:
                continue
            if f'sc.get("{key}")' not in src and f'sc["{key}"]' not in src:
                dead.append(f"{sc['name']}.{key}")
    check("没有没人读的字段", not dead, "、".join(dead))

    # 命中缓存的部分不计入新花费；否则缓存占比高时成本会被放大到约十倍
    print("⑯ 花费只算新花的那部分")
    run.events_since = lambda *a, **k: [
        {"pid": "p1", "payload": {"phase": "usage",
                                  "prompt": 430000, "cached": 426000, "completion": 2000}},
    ]
    got = run.spent_in_round("t", [{"pid": "p1"}], 0)
    check("缓存命中的不算新花", got == 6000, f"算成了 {got}")

    # 前缀缓存塌了 —— 最贵的一种静默故障, 别的一切照旧
    print("⑰ 前缀缓存塌没塌")
    check("命中 99% → 闭嘴", run.cold_prompts(430_000, 426_000) == [])
    check("命中 20% → 响", any("缓存塌了" in b for b in run.cold_prompts(430_000, 86_000)))
    check("一轮太短 → 不判", run.cold_prompts(9_000, 0) == [])

    # 有底子的项目: 他没提交完的那一行、他的历史, 谁都不许动
    print("⑱ 进别人有底子的项目")
    d = tempfile.mkdtemp()
    run.run_in(d, "git", "init", "-q")
    run.seed_project(d)
    check("原样不动 → 闭嘴", run.seed_kept(d) == [], str(run.seed_kept(d)))

    # 有人把他那一行盖掉了
    body = open(f"{d}/pay.py").read().replace(run.HALF_DONE, "")
    open(f"{d}/pay.py", "w").write(body)
    check("盖掉了他没提交的那行 → 响",
          any("没提交" in b for b in run.seed_kept(d)))

    # 有人改写了历史
    d2 = tempfile.mkdtemp()
    run.run_in(d2, "git", "init", "-q")
    run.seed_project(d2)
    run.run_in(d2, "git", "reset", "-q", "--hard", "HEAD~1")
    check("历史被改写 → 响", any("改写" in b for b in run.seed_kept(d2)))

    # 人手上那份没被覆盖: 测试红是必然的, 有没有告诉他才是判据
    print("⑲ 没覆盖你那份, 有没有说")
    import tempfile as _tf
    d = _tf.mkdtemp()
    run.run_in(d, "git", "init", "-q")
    open(f"{d}/pay.py", "w").write("x\n")
    run.run_in(d, "git", "add", "-A")
    run.run_in(d, "git", "-c", "user.name=我", "-c", "user.email=x@y", "commit", "-qm", "底子")
    open(f"{d}/pay.py", "w").write("我改了一行\n")          # 没提交的改动

    def with_result(text):
        run.events_since = lambda *a, **k: [{"pid": "p1", "payload": {
            "tool": "merge_up", "phase": "tool_ok", "result": text}}]
        return run.told_about_held("t", [{"pid": "p1"}], d, 0, ["pytest 是红的: x"])

    said = with_result("合进主干（master）了。（pay.py 没覆盖你手上那份）\n\n详细…")
    check("说在第一行 → 那条红不算问题", said == [], str(said))
    quiet = with_result("合进主干（master）了。3 files changed")
    check("一个字没提 → 响", any("一个字没提" in b for b in quiet), str(quiet))
    second = with_result("合进主干（master）了。\n\n注意：pay.py 没覆盖你手上那份")
    check("只写在第二段 → 也算没说(人看不到)", any("一个字没提" in b for b in second), str(second))

    # 两摊活不许串台 —— 串了的话两边照样构建绿、测试绿, 没别的判据会响
    print("⑳ 两摊活串不串台")
    import tempfile as _t2
    a, b = _t2.mkdtemp(), _t2.mkdtemp()
    def repo_with(d, names):
        run.run_in(d, "git", "init", "-q")
        for n in names:
            os.makedirs(os.path.dirname(os.path.join(d, n)) or d, exist_ok=True)
            open(os.path.join(d, n), "w").write("x")
        open(f"{d}/我的草稿.txt", "w").write(run.SENTINEL)
        run.run_in(d, "git", "add", "-A")
        run.run_in(d, "git", "-c", "user.name=x", "-c", "user.email=x@y", "commit", "-qm", "x")
    repo_with(a, ["expense.py", "test_expense.py"])
    repo_with(b, ["leave.py", "test_leave.py"])
    check("各干各的 → 闭嘴", run.apart(a, b) == [], str(run.apart(a, b)))

    c = _t2.mkdtemp()
    repo_with(c, ["leave.py", "expense.py"])      # 请假条跑到报销那摊里了
    check("串台了 → 响", any("串台" in x for x in run.apart(a, c)), str(run.apart(a, c)))

    # **同名不同内容不算串台**: CHARTER.md / PLAN.md 每个项目都有一份
    e1, e2 = _t2.mkdtemp(), _t2.mkdtemp()
    for d, body in ((e1, "报销那摊的章程"), (e2, "请假那摊的章程")):
        run.run_in(d, "git", "init", "-q")
        open(f"{d}/CHARTER.md", "w").write(body)
        open(f"{d}/PLAN.md", "w").write(body)
        open(f"{d}/{'expense' if d == e1 else 'leave'}.py", "w").write("x")
        open(f"{d}/我的草稿.txt", "w").write(run.SENTINEL)
        run.run_in(d, "git", "add", "-A")
        run.run_in(d, "git", "-c", "user.name=x", "-c", "user.email=x@y", "commit", "-qm", "x")
    check("同名不同内容 → 闭嘴", run.apart(e1, e2) == [], str(run.apart(e1, e2)))

    d0 = _t2.mkdtemp()
    repo_with(d0, [])
    check("一摊什么都没落地 → 响", any("没落地" in x for x in run.apart(a, d0)))
    check("另一摊没开起来 → 响", any("没开起来" in x for x in run.apart(a, "/不存在")))

    # 它们之间怎么说话 —— 十六个场景一直没查过这一块
    print("㉑ 交办的话像不像人说的")
    run.api = lambda tok, path, body=None: [] if path == "/processes" else {}

    def said_by(who, text):
        run.events_since = lambda *a, **k: [{"pid": "p1", "kind": "input.recv",
                                             "payload": {"from": who, "text": text}}]
        return run.talks_like_people("t", [{"pid": "p1"}], 0)
    spec = "做个最小登录页的后端。" + "端口读 PORT 默认 3000，POST /api/login 失败返回 400。" * 12
    check("902 字的规格书 → 响", any("规格书" in b for b in said_by("小丁", spec)))
    check("一句人话 → 闭嘴",
          said_by("小丁", "你做后端，登录接口按 CHARTER.md 那份契约来。") == [])
    # **教同行怎么干活**: 拉主干、自己验、合上去他自己都会
    check("教他用工具 → 响",
          any("他自己就会" in b for b in
              said_by("小丁", "后端你来。先 sync_down 拉主干，做完 merge_up。")))
    # **人说的和系统说的不在这条判据里**: 人爱说多长说多长
    check("人说的长句 → 不管", said_by("我", spec) == [])
    check("系统那一声 → 不管", said_by("系统", spec) == [])

    # 它自己说在哪儿听着, 就去那儿敲 —— 别死守一个谁都没说过的约定
    print("㉒ 敲门要敲它说的那扇")
    for text, want in (("Server listening on http://localhost:3000", 3000),
                       ("listening on port 8080", 8080),
                       ("已在端口 5173 启动", 5173),
                       ("http://127.0.0.1:39117/", 39117)):
        check(f"认得出 {want}", run.port_said(text) == want, str(run.port_said(text)))
    check("没说端口 → 0", run.port_said("起来了") == 0)
    # 低位端口不当端口: "HTTP/1.1 200" 里那个 200 不是
    check("不把随便一个数当端口", run.port_said("HTTP/1.1 200 OK") == 0)

    # 长活里的静默 —— 原来数的是"落没落够两次提交", 那是拍脑袋的数
    print("㉓ 长活中途有没有断过气")
    def gaps(times):
        run.events_since = lambda *a, **k: [
            {"pid": "p1", "at": t, "payload": {}} for t in times]
        return run.slow_and_silent("t", [{"pid": "p1", "spec": {"labels": {"name": "小甲"}}}], 400)
    check("一路有动静 → 闭嘴", gaps([0, 10_000, 20_000, 30_000]) == [])
    check("中间静了三分钟 → 响",
          any("静了" in b for b in gaps([0, 10_000, 200_000, 210_000])))
    # 短活不查 —— 一分钟的活静一会儿很正常
    run.events_since = lambda *a, **k: [{"pid": "p1", "at": t, "payload": {}} for t in (0, 200_000)]
    check("短活 → 不查",
          run.slow_and_silent("t", [{"pid": "p1", "spec": {"labels": {"name": "小甲"}}}], 100) == [])

    # 汇报有多长 —— **只记不判**: 先攒证据, 再决定要不要立规矩
    print("㉔ 汇报长度记下来")
    run.api = lambda tok, path, body=None: [] if path == "/processes" else {}
    run.events_since = lambda *a, **k: [
        {"pid": "p1", "payload": {"phase": "reply", "text": "做完了。"}},
        {"pid": "p1", "payload": {"phase": "done", "summary": "x" * 500, "hitLimit": True}},
        {"pid": "p1", "payload": {"phase": "step", "tool": "run"}},
    ]
    got = run.reply_sizes("t", [{"pid": "p1"}], 0)
    check("说的话都记上, 别的不记", got == [4, 500], str(got))

    # **按名字不按 pid**: 停掉重开之后 pid 换了, 人没换
    print("㉕ 重开之后那个进程也算这个人")
    crew = [{"pid": "p1", "spec": {"labels": {"name": "小辛"}}}]
    run.api = lambda tok, path, body=None: [
        {"pid": "p1", "spec": {"labels": {"name": "小辛"}}, "state": "exited"},
        {"pid": "p2", "spec": {"labels": {"name": "小辛"}}, "state": "waiting"},  # 停完起回来的
        {"pid": "p9", "spec": {"labels": {"name": "别人"}}, "state": "waiting"},
    ] if path == "/processes" else {}
    got = run.all_pids("t", crew)
    check("重开的那个也算上", got == {"p1", "p2"}, str(got))
    check("别人的不算", "p9" not in got)
    check("pid → 谁 也跟着扩", run.who_by_pid("t", crew) == {"p1": "小辛", "p2": "小辛"},
          str(run.who_by_pid("t", crew)))

    print()
    if FAILED:
        print(f"！{len(FAILED)} 条判据证明不了自己会响: " + "、".join(FAILED))
        return 1
    print("每条判据都在弄坏的样本上响了, 在好样本上闭了嘴。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
