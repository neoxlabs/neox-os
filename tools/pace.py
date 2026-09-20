#!/usr/bin/env python3
"""
pace —— 慢在哪: 从账本的时间戳拆出"模型往返 / 工具执行 / 调度间隙"。

    用户的话: "速度慢得不正确, 监控一下, 是我们调度的问题还是 HTTP 的问题。"
    账本里每条事件都带毫秒时间, 答案本来就躺在里面:

      batch/step → tool_ok     的间隔 = 工具真正跑了多久
      tool_ok → 下一个 batch   的间隔 = 模型往返(推理 + 网络)
      budget → usage → batch   同毫秒 = 调度开销

    2026-08-27 första跑的结论: 工具 0-3ms、调度 0-1ms、模型往返 15-44s ——
    99% 在等模型解码。write_file 前的间隙最长: 整个文件内容要当工具参数
    逐 token 吐出来。

用法: python3 tools/pace.py [最近多少条事件, 默认 4000]
"""
import json
import os
import sys
from collections import defaultdict

LEDGER = os.path.expanduser("~/.neox-os/events.jsonl")


def stat(v):
    v = sorted(v)
    n = len(v)
    if n == 0:
        return "无样本"
    return f"n={n:<4d} 中位 {v[n // 2]:>6d}ms   P90 {v[int(n * 0.9)]:>6d}ms   最大 {v[-1]:>7d}ms"


def main():
    most = int(sys.argv[1]) if len(sys.argv) > 1 else 4000
    rows = [json.loads(l) for l in open(LEDGER, encoding="utf-8")]
    recent = [r for r in rows[-most:] if r.get("kind") == "proc.output"]

    by_pid = defaultdict(list)
    for r in recent:
        pl = r.get("payload") or {}
        by_pid[r["pid"]].append((r["at"], pl.get("phase"), pl.get("tool", "")))

    tool_ms = defaultdict(list)
    model_ms = []
    for evs in by_pid.values():
        evs.sort(key=lambda e: e[0])
        last_result_at = None
        for i, (at, ph, tool) in enumerate(evs):
            if ph in ("tool_ok", "tool_err"):
                # 往回找同名 step/batch 的时刻 —— 中间可能夹着并行批次的别的结果
                for j in range(i - 1, -1, -1):
                    pat, pph, _ = evs[j]
                    if pph in ("step", "batch"):
                        tool_ms[tool].append(at - pat)
                        break
                last_result_at = at
            elif ph in ("step", "batch") and last_result_at is not None:
                model_ms.append(at - last_result_at)
                last_result_at = None

    print("== 模型往返(上个工具结果 → 下一步指令; 含推理和网络) ==")
    print(stat(model_ms))
    print("\n== 各工具执行耗时 ==")
    for tool, v in sorted(tool_ms.items(), key=lambda kv: -sorted(kv[1])[len(kv[1]) // 2]):
        print(f"{tool:14s} {stat(v)}")


if __name__ == "__main__":
    main()
