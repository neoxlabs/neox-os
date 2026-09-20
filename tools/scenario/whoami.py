#!/usr/bin/env python3
"""从跑着的客户端里问出 OS 的 token —— 它每次启动都是新的"""
import json, urllib.request, asyncio
try:
    import websockets
except ImportError:
    print(""); raise SystemExit
async def main():
    pages = json.load(urllib.request.urlopen("http://127.0.0.1:9333/json/list"))
    ws = [p for p in pages if p["type"] == "page"][0]["webSocketDebuggerUrl"]
    async with websockets.connect(ws, max_size=None) as c:
        await c.send(json.dumps({"id": 1, "method": "Runtime.evaluate",
                                 "params": {"expression": "window.neoxos.token", "returnByValue": True}}))
        while True:
            r = json.loads(await c.recv())
            if r.get("id") == 1:
                print(r["result"]["result"]["value"]); return
try: asyncio.run(main())
except Exception: print("")
