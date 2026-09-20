#!/bin/bash
# winshot <输出文件> [窗口标题]
#
# 抓**整个窗口, 含窗口边框**. CDP 的 Page.captureScreenshot 只给页面内容 ——
# 红绿灯、圆角、窗口边缘一律不在画面里, 那类错位只能这么看见.
# (R68 就是这么漏掉的: 我看不见灯, 用户一眼就看见了.)
#
# ── 为什么走 Quartz 不走 AppleScript ──
#
#   System Events 那条路要**辅助功能授权**, 而它会莫名其妙地失效:
#   真机上撞见过一次, 所有 app(Chrome/Cursor/Termius)全报 0 个窗口 ——
#   看起来像"应用没有窗口", 其实是权限那一层塌了. 拿这种结果去判断
#   界面, 只会得出错误结论.
#
#   CGWindowList 不需要那个授权, 而且直接给**窗口号**: screencapture -l
#   抓的是这个窗口本身, 不是那块屏幕 —— 被别的窗口盖住也照样抓得对,
#   也就不用先把它抬到最前(那会打断用户正在干的事).
set -e
OUT="$1"
TITLE="${2:-NeoxOS Console}"
ID=$(TITLE="$TITLE" python3 -c '
import os, sys, Quartz
want = os.environ["TITLE"]
for w in Quartz.CGWindowListCopyWindowInfo(
        Quartz.kCGWindowListOptionAll | Quartz.kCGWindowListExcludeDesktopElements,
        Quartz.kCGNullWindowID):
    if (w.get("kCGWindowName") or "") == want:
        print(w.get("kCGWindowNumber")); sys.exit(0)
')
[ -z "$ID" ] && { echo "没找到标题为「$TITLE」的窗口"; exit 1; }
# -l 抓这个窗口本身; -o 去掉窗口阴影(阴影里会带进它后面的东西)
screencapture -o -x -l"$ID" "$OUT"
echo "$OUT  窗口号=$ID"
