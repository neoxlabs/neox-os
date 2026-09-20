#!/usr/bin/env python3
"""把真实 HA 的 sqlite 历史导成变更日志 JSON, 给 Go 侧重放.

── 为什么要中间这一步 ──

这个仓库是零依赖的(只有一个 x/sys). 为读一次 sqlite 引一个 CGO 驱动
不值 —— 而导出是一次性的、可重复的, 导出来的 JSON 还能存进 testdata
当回归样本.
"""
import json, sqlite3, sys

def main(db, out):
    c = sqlite3.connect(db)
    rows = c.execute('''
        select m.entity_id, s.state, s.last_updated_ts
        from states s join states_meta m on s.metadata_id = m.metadata_id
        where s.state is not null
        order by s.last_updated_ts
    ''').fetchall()
    # HA 的 states 表**每次属性变化也写一行**(太阳高度角每几分钟变一次),
    # 所以这里导出的是"写入日志"而不是"状态变化日志" —— 那正是我们要验的:
    # 桥接能不能把它压回真正的状态变化
    data = [{"entity": e, "state": s, "ts": int(ts * 1000)} for e, s, ts in rows]
    json.dump({"changes": data}, open(out, "w"), ensure_ascii=False)
    print(f"导出 {len(data)} 条写入记录 → {out}")
    ents = {}
    for d in data:
        ents[d["entity"]] = ents.get(d["entity"], 0) + 1
    print("实体:", len(ents))

if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
