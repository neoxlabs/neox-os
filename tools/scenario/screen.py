#!/usr/bin/env python3
"""
screen —— **界面上到底是什么样**.

── 为什么非查不可 ──

    这台测试台所有的判据走的都是 HTTP 那条口子: 主干上有什么文件、
    谁合了什么、账本里说了什么. 而用户明天用的是 Electron ——
    **渲染进程里报一个错, 我这套东西一个字都看不见**.

    真机上出过一次: 界面卡在 21 秒之前的状态(rAF 被系统节流), 而
    SSE 一条不落全收到了. 那种毛病 API 侧永远查不出来, 因为数据是对的.

── 查什么 ──

    只查"看得见/看不见"这一层, 不查像素:

      · 渲染进程有没有报错(未捕获异常、React 崩了)
      · 屋里有没有真画出东西来(不是白屏)
      · 时间线上最后一条跟账本里最后一条对不对得上

用法: ./screen.py           查一遍, 有问题打印出来并返回 1
"""
import asyncio
import json
import sys
import urllib.request

try:
    import websockets
except ImportError:
    print("没装 websockets，查不了界面")
    raise SystemExit(0)


PROBE = r"""
(() => {
  const out = {};
  out.title = document.title;
  const root = document.body;
  out.text = (root.innerText || "").slice(0, 4000);
  out.chars = (root.innerText || "").trim().length;
  // React 崩了的话页面上只剩一个空壳
  out.nodes = root.querySelectorAll("*").length;
  out.errors = (window.__neoxErrors || []).slice(0, 5);
  return out;
})()
"""


async def look():
    pages = json.load(urllib.request.urlopen("http://127.0.0.1:9333/json/list"))
    page = [p for p in pages if p["type"] == "page"][0]
    async with websockets.connect(page["webSocketDebuggerUrl"], max_size=None) as c:
        # 先开日志, 再取快照 —— 只有开了才收得到后面的报错
        await c.send(json.dumps({"id": 1, "method": "Log.enable"}))
        await c.send(json.dumps({"id": 2, "method": "Runtime.enable"}))
        await c.send(json.dumps({"id": 3, "method": "Runtime.evaluate",
                                 "params": {"expression": PROBE, "returnByValue": True}}))
        got, errors = None, []
        while got is None:
            r = json.loads(await c.recv())
            if r.get("method") == "Log.entryAdded":
                e = r["params"]["entry"]
                if e.get("level") in ("error", "warning"):
                    errors.append(f"{e['level']}: {e.get('text', '')[:160]}")
            if r.get("method") == "Runtime.exceptionThrown":
                d = r["params"]["exceptionDetails"]
                errors.append("未捕获异常: " + str(d.get("text"))[:160])
            if r.get("id") == 3:
                got = r["result"]["result"]["value"]
        return got, errors


def main():
    # **只看这一轮的** —— 界面记的那份会一直攒着(见 shell/mishaps.ts).
    #   不划界的话, 二十分钟前的一条错每一轮都要报一遍 —— 跟账本那边
    #   栽的是同一个跟头(见 run.py 的 events_since).
    since = int(sys.argv[sys.argv.index("--since") + 1]) if "--since" in sys.argv else 0
    try:
        got, errors = asyncio.run(look())
    except Exception as e:
        print(f"连不上界面: {e}")
        return 0            # 客户端没开不算界面的问题

    return report(verdict(got, errors, since))


def verdict(got, errors, since=0) -> list:
    """界面上有没有出事 —— **抽出来是为了自检测得到它会不会响**"""
    bad = []
    # **白屏是最要命的一种**: API 全对, 而人什么都看不到
    if got["chars"] < 20:
        bad.append(f"界面上几乎没有字(只有 {got['chars']} 个) —— 白屏了?")
    if got["nodes"] < 30:
        bad.append(f"页面上只剩 {got['nodes']} 个节点 —— 渲染崩了?")
    # **界面自己记下的那些才是主力**.
    #
    #   CDP 那条只收得到"连上之后"发生的事; 而崩得最厉害的时刻往往在
    #   启动那几秒, 那时候还没人连着. window.__neoxErrors 从页面一起来
    #   就在记(见 shell/mishaps.ts).
    #
    #   这一行漏过一次: 探针把 __neoxErrors 取回来了, 而下面只遍历了
    #   CDP 那份 —— 往界面里塞一条错, 它照样说"界面正常". 一个空转的
    #   检查比没有更糟.
    for m in got.get("errors") or []:
        if since and (m.get("at") or 0) < since:
            continue                      # 这一轮之前的旧账, 不算在这一轮头上
        bad.append(f"界面记下一条{m.get('kind')}: " + str(m.get("text"))[:160])
    for e in errors:
        bad.append("渲染进程报错: " + e)

    return bad


def report(bad) -> int:
    if bad:
        print("界面有问题:")
        for b in bad:
            print("  ·", b)
        return 1
    print("界面正常")
    return 0


if __name__ == "__main__":
    sys.exit(main())
